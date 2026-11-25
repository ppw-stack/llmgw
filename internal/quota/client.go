package quota

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/pw/llmgw/internal/netproxy"
	"github.com/pw/llmgw/internal/preset"
)

// The kinds of quota endpoint the gateway knows how to read.
const (
	KindOpenCodeGo = "opencode-go"
	KindMiniMax    = "minimax"
	KindArk        = "ark"
)

// maxQuotaBody caps how much of an upstream response is read.
const maxQuotaBody = 1 << 20

// HTTPError is an upstream answer that is not a successful one.
type HTTPError struct {
	// Status is the HTTP status the vendor answered with.
	Status int
	// Detail is a short excerpt of the upstream body. It never contains the
	// upstream key.
	Detail string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("quota endpoint answered %d: %s", e.Status, e.Detail)
}

// Client queries the quota endpoints of the built-in providers.
type Client struct {
	// HTTP is the client used for the queries.
	HTTP *http.Client
	// ResolveURL rewrites the quota URL before the request. Tests use it to
	// point at a local server; production leaves it nil.
	ResolveURL func(string) string
	// ArkHost overrides the Volcengine control plane host. Tests use it to
	// point at a local server; production leaves it empty.
	ArkHost string
}

// NewClient returns a client with the default timeout and per-request proxy
// support.
func NewClient() *Client {
	return &Client{
		HTTP: &http.Client{
			Timeout:   10 * time.Second,
			Transport: netproxy.NewTransport(),
		},
	}
}

// httpClient returns the client used for the queries.
func (c *Client) httpClient() *http.Client {
	if c.HTTP == nil {
		return http.DefaultClient
	}
	return c.HTTP
}

// Fetch asks the vendor about the allowance windows of one subscription.
func (c *Client) Fetch(ctx context.Context, spec preset.QuotaSpec, apiKey string, cred Credential) ([]Window, error) {
	switch spec.Kind {
	case KindOpenCodeGo:
		url := spec.URL
		if c.ResolveURL != nil {
			url = c.ResolveURL(url)
		}
		return c.fetchOpenCodeGo(ctx, url, apiKey)
	case KindMiniMax:
		url := spec.URL
		if c.ResolveURL != nil {
			url = c.ResolveURL(url)
		}
		return c.fetchMiniMax(ctx, url, apiKey)
	case KindArk:
		// The control plane address is compiled in, so the signed request
		// always goes to the vendor.
		return c.fetchArk(ctx, cred)
	default:
		return nil, fmt.Errorf("unknown quota kind %q", spec.Kind)
	}
}

// getJSON performs the quota query and decodes the JSON body.
func (c *Client) getJSON(ctx context.Context, url, apiKey string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("build quota request: %w", err)
	}
	// The quota endpoints authenticate with the same bearer token as the
	// inference API, not with x-api-key.
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Accept", "application/json")

	httpClient := c.httpClient()
	resp, err := httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("query quota endpoint: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxQuotaBody))
	if err != nil {
		return fmt.Errorf("read quota response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return &HTTPError{Status: resp.StatusCode, Detail: sanitize(string(body), apiKey)}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode quota response: %w", err)
	}
	return nil
}

// postJSON performs a signed POST with an empty body and decodes the JSON
// answer. It is used by the Volcengine control plane, whose request signature
// covers the body hash.
func (c *Client) postJSON(ctx context.Context, endpoint string, headers map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build quota request: %w", err)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return fmt.Errorf("query quota endpoint: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxQuotaBody))
	if err != nil {
		return fmt.Errorf("read quota response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return &HTTPError{Status: resp.StatusCode, Detail: sanitize(string(body), "")}
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode quota response: %w", err)
	}
	return nil
}

// sanitize removes the upstream key from a message and keeps it short, so an
// error can be shown in the console without leaking the credential.
func sanitize(msg, apiKey string) string {
	if apiKey != "" {
		msg = strings.ReplaceAll(msg, apiKey, "***")
	}
	msg = strings.TrimSpace(msg)
	if len(msg) > 200 {
		msg = msg[:200] + "…"
	}
	return msg
}

// clampPercent keeps a percentage inside 0 to 100.
func clampPercent(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
