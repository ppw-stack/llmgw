package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/pw/llmgw/internal/config"
	"github.com/pw/llmgw/internal/netproxy"
	"github.com/pw/llmgw/internal/preset"
	"github.com/pw/llmgw/internal/store"
)

const (
	instanceTestTimeout          = 10 * time.Second
	instanceTestMaxResponseBytes = 1 << 20
)

type instanceTestAllowPrivateKey struct{}

type instanceTestInput struct {
	InstanceID           *int64          `json:"instance_id,omitempty"`
	Preset               string          `json:"preset,omitempty"`
	APIKey               *string         `json:"api_key,omitempty"`
	Endpoints            []endpointInput `json:"endpoints,omitempty"`
	AllowPrivateUpstream bool            `json:"allow_private_upstream"`
	ProxyURL             *string         `json:"proxy_url,omitempty"`
}

type endpointTestResult struct {
	Protocol   string `json:"protocol"`
	BaseURL    string `json:"base_url"`
	Status     string `json:"status"` // ok | reachable | auth_error | error
	OK         bool   `json:"ok"`
	HTTPStatus int    `json:"http_status,omitempty"`
	LatencyMS  int64  `json:"latency_ms"`
	ModelCount int    `json:"model_count,omitempty"`
	Message    string `json:"message"`
}

type instanceTestResponse struct {
	OK        bool                 `json:"ok"`
	Endpoints []endpointTestResult `json:"endpoints"`
}

// adminTestSavedInstance probes every protocol endpoint of a stored instance
// using its decrypted API key and optional proxy configuration.
func (s *Server) adminTestSavedInstance(w http.ResponseWriter, r *http.Request, instanceID int64) {
	if r.Method != http.MethodPost {
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	rec, err := s.findInstanceRecord(instanceID)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rec == nil {
		writeAdminError(w, http.StatusNotFound, "instance not found")
		return
	}

	apiKey, err := s.decryptOptional(rec.EncryptedKey)
	if err != nil {
		s.log.Error("decrypt instance key for connectivity test",
			"request_id", requestID(r), "instance", rec.Name, "err", err)
		writeAdminError(w, http.StatusInternalServerError, "cannot decrypt stored API key")
		return
	}
	rawProxy, err := s.decryptOptional(rec.EncryptedProxyURL)
	if err != nil {
		s.log.Error("decrypt proxy url for connectivity test",
			"request_id", requestID(r), "instance", rec.Name, "err", err)
		writeAdminError(w, http.StatusInternalServerError, "cannot decrypt stored proxy url")
		return
	}
	proxyURL, err := parseOptionalProxyURL("instance-test", rawProxy)
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}

	endpoints, err := resolveTestEndpoints(rec.Preset, rec.Endpoints, rec.AllowPrivateUpstream)
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}

	resp := s.runInstanceConnectivityTest(r.Context(), endpoints, apiKey, proxyURL, rec.AllowPrivateUpstream)
	writeJSON(w, http.StatusOK, resp)
}

// adminTestInstanceDraft probes an unsaved instance form payload. When
// instance_id is supplied (editing an existing instance), omitted API key or
// omitted proxy_url falls back to the stored credentials so the user can test
// without re-entering secrets.
func (s *Server) adminTestInstanceDraft(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var in instanceTestInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	var existing *store.InstanceRecord
	if in.InstanceID != nil {
		rec, err := s.findInstanceRecord(*in.InstanceID)
		if err != nil {
			writeAdminError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if rec == nil {
			writeAdminError(w, http.StatusNotFound, "instance not found")
			return
		}
		existing = rec
	}

	presetID := strings.TrimSpace(in.Preset)
	if existing != nil && presetID == "" {
		presetID = existing.Preset
	}

	var apiKey string
	if in.APIKey != nil && strings.TrimSpace(*in.APIKey) != "" {
		apiKey = strings.TrimSpace(*in.APIKey)
	} else if existing != nil {
		var err error
		apiKey, err = s.decryptOptional(existing.EncryptedKey)
		if err != nil {
			s.log.Error("decrypt instance key for draft connectivity test",
				"request_id", requestID(r), "instance", existing.Name, "err", err)
			writeAdminError(w, http.StatusInternalServerError, "cannot decrypt stored API key")
			return
		}
	}

	var rawProxy string
	if in.ProxyURL != nil {
		rawProxy = strings.TrimSpace(*in.ProxyURL)
	} else if existing != nil {
		var err error
		rawProxy, err = s.decryptOptional(existing.EncryptedProxyURL)
		if err != nil {
			s.log.Error("decrypt proxy url for draft connectivity test",
				"request_id", requestID(r), "instance", existing.Name, "err", err)
			writeAdminError(w, http.StatusInternalServerError, "cannot decrypt stored proxy url")
			return
		}
	}
	proxyURL, err := parseOptionalProxyURL("instance-test", rawProxy)
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}

	rawEndpoints := make([]config.Endpoint, 0, len(in.Endpoints))
	for _, ep := range in.Endpoints {
		rawEndpoints = append(rawEndpoints, config.Endpoint{
			Protocol: strings.ToLower(strings.TrimSpace(ep.Protocol)),
			BaseURL:  strings.TrimRight(strings.TrimSpace(ep.BaseURL), "/"),
		})
	}
	endpoints, err := resolveTestEndpoints(presetID, rawEndpoints, in.AllowPrivateUpstream)
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}

	resp := s.runInstanceConnectivityTest(r.Context(), endpoints, apiKey, proxyURL, in.AllowPrivateUpstream)
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) findInstanceRecord(instanceID int64) (*store.InstanceRecord, error) {
	records, err := s.store.ListInstanceRecords()
	if err != nil {
		return nil, err
	}
	for i := range records {
		if records[i].ID == instanceID {
			return &records[i], nil
		}
	}
	return nil, nil
}

func (s *Server) decryptOptional(enc string) (string, error) {
	if enc == "" {
		return "", nil
	}
	return s.box.Decrypt(enc)
}

func parseOptionalProxyURL(owner, raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	normalized, err := config.NormalizeProxyURL(owner, "proxy_url", raw)
	if err != nil {
		return nil, err
	}
	parsed, err := url.Parse(normalized)
	if err != nil {
		return nil, fmt.Errorf("%s: invalid proxy_url", owner)
	}
	return parsed, nil
}

func resolveTestEndpoints(presetID string, custom []config.Endpoint, allowPrivate bool) ([]config.Endpoint, error) {
	if presetID != "" {
		pr, ok := preset.Lookup(presetID)
		if !ok {
			return nil, fmt.Errorf("unknown preset %q", presetID)
		}
		var out []config.Endpoint
		if pr.OpenAIBaseURL != "" {
			out = append(out, config.Endpoint{
				Protocol: config.ProtocolOpenAI,
				BaseURL:  strings.TrimRight(pr.OpenAIBaseURL, "/"),
			})
		}
		if pr.AnthropicBaseURL != "" {
			out = append(out, config.Endpoint{
				Protocol: config.ProtocolAnthropic,
				BaseURL:  strings.TrimRight(pr.AnthropicBaseURL, "/"),
			})
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("preset %q defines no endpoints", presetID)
		}
		return out, nil
	}

	if len(custom) == 0 {
		return nil, fmt.Errorf("at least one endpoint is required")
	}
	seen := make(map[string]bool, len(custom))
	out := make([]config.Endpoint, 0, len(custom))
	for i, ep := range custom {
		if ep.Protocol != config.ProtocolOpenAI && ep.Protocol != config.ProtocolAnthropic {
			return nil, fmt.Errorf("endpoints[%d]: unsupported protocol %q", i, ep.Protocol)
		}
		if seen[ep.Protocol] {
			return nil, fmt.Errorf("endpoints[%d]: duplicate protocol %q", i, ep.Protocol)
		}
		seen[ep.Protocol] = true
		u, err := url.Parse(ep.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return nil, fmt.Errorf("endpoints[%d]: base_url must be a valid http(s) URL", i)
		}
		if err := config.CheckUpstreamHost("instance-test", fmt.Sprintf("endpoints[%d].base_url", i), ep.BaseURL, allowPrivate); err != nil {
			return nil, err
		}
		out = append(out, ep)
	}
	return out, nil
}

func (s *Server) connectivityTestClient() *http.Client {
	blockPrivate := s.forwarder != nil && s.forwarder.BlockPrivate()
	dialer := &net.Dialer{Timeout: instanceTestTimeout, KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		Proxy:                 netproxy.ProxyFunc,
		MaxIdleConns:          8,
		IdleConnTimeout:       30 * time.Second,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
		ResponseHeaderTimeout: instanceTestTimeout,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if !blockPrivate {
				return dialer.DialContext(ctx, network, addr)
			}
			if _, viaProxy := netproxy.FromContext(ctx); viaProxy {
				return netproxy.DialProxySafe(ctx, dialer, network, addr)
			}
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			allowPrivate, _ := ctx.Value(instanceTestAllowPrivateKey{}).(bool)
			ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if !allowPrivate && config.BlockedAddress(ip) {
					continue
				}
				return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			}
			return nil, fmt.Errorf("upstream host %q resolves only to private or reserved addresses", host)
		},
	}
	return &http.Client{
		Timeout:   instanceTestTimeout,
		Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func (s *Server) runInstanceConnectivityTest(
	ctx context.Context,
	endpoints []config.Endpoint,
	apiKey string,
	proxyURL *url.URL,
	allowPrivate bool,
) instanceTestResponse {
	client := s.connectivityTestClient()
	defer client.CloseIdleConnections()

	results := make([]endpointTestResult, len(endpoints))
	var wg sync.WaitGroup
	for i, ep := range endpoints {
		wg.Add(1)
		go func(idx int, endpoint config.Endpoint) {
			defer wg.Done()
			results[idx] = probeEndpoint(ctx, client, endpoint, apiKey, proxyURL, allowPrivate)
		}(i, ep)
	}
	wg.Wait()

	allOK := len(results) > 0
	for _, r := range results {
		if !r.OK {
			allOK = false
			break
		}
	}
	return instanceTestResponse{
		OK:        allOK,
		Endpoints: results,
	}
}

func probeEndpoint(
	parent context.Context,
	client *http.Client,
	ep config.Endpoint,
	apiKey string,
	proxyURL *url.URL,
	allowPrivate bool,
) endpointTestResult {
	res := endpointTestResult{
		Protocol: ep.Protocol,
		BaseURL:  ep.BaseURL,
	}
	ctx, cancel := context.WithTimeout(parent, instanceTestTimeout)
	defer cancel()
	if allowPrivate {
		ctx = context.WithValue(ctx, instanceTestAllowPrivateKey{}, true)
	}
	if proxyURL != nil {
		ctx = netproxy.WithURL(ctx, proxyURL)
	}

	probeURL := ep.BaseURL + "/models"
	if ep.Protocol == config.ProtocolAnthropic {
		probeURL = ep.BaseURL + "/v1/models"
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probeURL, nil)
	if err != nil {
		res.Status = "error"
		res.Message = netproxy.SanitizeError(err, proxyURL)
		return res
	}
	req.Header.Set("Accept", "application/json")
	if ep.Protocol == config.ProtocolAnthropic {
		req.Header.Set("anthropic-version", "2023-06-01")
		if apiKey != "" {
			req.Header.Set("x-api-key", apiKey)
		}
	} else if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	start := time.Now()
	resp, err := client.Do(req)
	res.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		res.Status = "error"
		res.Message = netproxy.SanitizeError(err, proxyURL)
		return res
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, instanceTestMaxResponseBytes))
	res.HTTPStatus = resp.StatusCode

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		res.Status = "ok"
		res.OK = true
		if count := countModelsInBody(body); count > 0 {
			res.ModelCount = count
			res.Message = fmt.Sprintf("连接正常（返回 %d 个模型）", count)
		} else {
			res.Message = "连接正常"
		}
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		res.Status = "auth_error"
		res.OK = false
		res.Message = fmt.Sprintf("鉴权失败（HTTP %d），请检查 API Key", resp.StatusCode)
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed:
		res.Status = "reachable"
		res.OK = true
		res.Message = fmt.Sprintf("网络可达，上游未实现 /models 接口（HTTP %d）", resp.StatusCode)
	default:
		res.Status = "error"
		res.OK = false
		res.Message = fmt.Sprintf("上游返回 HTTP %d", resp.StatusCode)
	}
	return res
}

func countModelsInBody(body []byte) int {
	if len(body) == 0 {
		return 0
	}
	var payload struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0
	}
	return len(payload.Data)
}
