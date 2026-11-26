package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/pw/llmgw/internal/alert"
	"github.com/pw/llmgw/internal/auth"
	"github.com/pw/llmgw/internal/clientip"
	"github.com/pw/llmgw/internal/config"
	"github.com/pw/llmgw/internal/ingress"
	"github.com/pw/llmgw/internal/quota"
	"github.com/pw/llmgw/internal/router"
	"github.com/pw/llmgw/internal/secret"
	"github.com/pw/llmgw/internal/store"
	"github.com/pw/llmgw/internal/transport"
)

type Options struct {
	Router         *router.Router
	Forwarder      *transport.Forwarder
	Store          *store.Store
	Box            *secret.Box
	AdminToken     string
	MetricsToken   string
	SessionTTL     time.Duration
	SecureCookie   bool
	TrustedProxies []netip.Prefix
	// AdminPrefixes restricts the admin surface when non-empty.
	AdminPrefixes []netip.Prefix
	// AdminEnabled opens or closes the management surface. Nil means open,
	// so an embedder that does not care keeps the previous behaviour.
	AdminEnabled *bool
	// KeyRPM and KeyMaxConcurrent bound one gateway key. Zero disables.
	KeyRPM           int
	KeyMaxConcurrent int
	MaxBody          int64
	Log              *slog.Logger
	UI               http.Handler
	// Quota asks the built-in providers about their subscription quota.
	// Tests replace it to point at a local server.
	Quota *quota.Service
	// Alert dispatches optional asynchronous webhook notifications.
	Alert *alert.Notifier
}

type Server struct {
	router         *router.Router
	forwarder      *transport.Forwarder
	store          *store.Store
	box            *secret.Box
	adminToken     string
	maxBody        int64
	log            *slog.Logger
	ui             http.Handler
	sessionTTL     time.Duration
	secureCookie   bool
	trustedProxies []netip.Prefix
	// adminPrefixes is the admin network whitelist. It is a pointer so the
	// config watcher can swap it while requests are in flight.
	adminPrefixes atomic.Pointer[[]netip.Prefix]
	// adminEnabled gates the management surface. It is atomic because the
	// config watcher flips it while requests are in flight.
	adminEnabled atomic.Bool
	// metricsToken guards the /metrics endpoint. It is atomic because the
	// config watcher swaps it while requests are in flight. An empty token
	// disables the endpoint.
	metricsToken atomic.Pointer[string]
	limiter      *loginLimiter
	keys         *keyCache
	keyLimiter   *keyLimiter
	loginSem     chan struct{}
	quota        *quota.Service
	alert        *alert.Notifier
}

func New(o Options) (*Server, error) {
	if o.MaxBody <= 0 {
		o.MaxBody = 32 << 20
	}
	if o.SessionTTL <= 0 {
		o.SessionTTL = 12 * time.Hour
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	cache, err := newKeyCache(o.Store)
	if err != nil {
		return nil, err
	}
	box := o.Box
	if box == nil {
		// Tests may omit the box; generate an ephemeral one so provider APIs
		// still work, while production always passes a persisted key.
		key := make([]byte, 32)
		_, _ = rand.Read(key)
		box, err = secret.New(key)
		if err != nil {
			return nil, err
		}
	}
	quotaService := o.Quota
	if quotaService == nil {
		quotaService = quota.NewService()
	}
	s := &Server{
		router:         o.Router,
		forwarder:      o.Forwarder,
		store:          o.Store,
		box:            box,
		adminToken:     o.AdminToken,
		maxBody:        o.MaxBody,
		log:            o.Log,
		ui:             o.UI,
		sessionTTL:     o.SessionTTL,
		secureCookie:   o.SecureCookie,
		trustedProxies: o.TrustedProxies,
		limiter:        newLoginLimiter(5*time.Minute, 10, 10000),
		keys:           cache,
		keyLimiter:     newKeyLimiter(o.KeyRPM, o.KeyMaxConcurrent),
		loginSem:       make(chan struct{}, 4),
		quota:          quotaService,
		alert:          o.Alert,
	}
	s.SetMetricsToken(o.MetricsToken)
	// A nil AdminEnabled means the management surface stays open.
	s.adminEnabled.Store(o.AdminEnabled == nil || *o.AdminEnabled)
	s.SetAdminPrefixes(o.AdminPrefixes)
	if s.router != nil {
		s.router.SetQuotaProvider(s.quota)
	}
	return s, nil
}

// SetAdminEnabled opens or closes the management surface. The config watcher
// calls it on every reload, so the console can be shut down without a
// restart. It reports whether the state actually changed.
func (s *Server) SetAdminEnabled(enabled bool) bool {
	if s.adminEnabled.Swap(enabled) == enabled {
		return false
	}
	if enabled {
		s.log.Info("management console enabled")
	} else {
		s.log.Warn("management console disabled by configuration")
	}
	return true
}

// SetAdminPrefixes replaces the admin network whitelist. An empty list means
// every source that can reach the listener is allowed, which is only safe
// behind a network boundary.
//
// The list is copied: the caller owns the slice it passes in and may reuse
// or change it afterwards, while the whitelist is read by every request that
// reaches the management surface.
func (s *Server) SetAdminPrefixes(prefixes []netip.Prefix) {
	cp := make([]netip.Prefix, len(prefixes))
	copy(cp, prefixes)
	s.adminPrefixes.Store(&cp)
}

// SetKeyLimits updates the per-key request budget. Zero disables a
// dimension.
func (s *Server) SetKeyLimits(rpm, maxConcurrent int) {
	s.keyLimiter.setLimits(rpm, maxConcurrent)
}

// SetAlertWebhook updates the alert webhook URL and cooldown at runtime.
func (s *Server) SetAlertWebhook(webhookURL string, cooldown time.Duration) {
	if s.alert != nil {
		s.alert.SetWebhook(webhookURL, cooldown)
	}
}

// SetMetricsToken swaps the token that guards the /metrics endpoint. An
// empty token disables the endpoint.
//
// The config watcher calls it on every reload, so the metrics surface can be
// turned on and off without a restart.
func (s *Server) SetMetricsToken(token string) {
	s.metricsToken.Store(&token)
}

// metricsTokenValue returns the token that guards the /metrics endpoint. An
// empty string means the endpoint is disabled.
func (s *Server) metricsTokenValue() string {
	if p := s.metricsToken.Load(); p != nil {
		return *p
	}
	return ""
}

// adminSurfaceOpen reports whether the management surface is reachable.
func (s *Server) adminSurfaceOpen() bool {
	return s.adminEnabled.Load()
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/openai/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		s.handleChat(w, r, config.ProtocolOpenAI, transport.EndpointDefault)
	})
	mux.HandleFunc("/openai/v1/responses", func(w http.ResponseWriter, r *http.Request) {
		s.handleChat(w, r, config.ProtocolOpenAI, transport.EndpointOpenAIResponses)
	})
	mux.HandleFunc("/anthropic/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		s.handleChat(w, r, config.ProtocolAnthropic, transport.EndpointDefault)
	})
	mux.HandleFunc("/anthropic/v1/messages/count_tokens", func(w http.ResponseWriter, r *http.Request) {
		s.handleChat(w, r, config.ProtocolAnthropic, transport.EndpointAnthropicCountTokens)
	})
	mux.HandleFunc("/openai/v1/models", s.handleModels)
	mux.HandleFunc("/anthropic/v1/models", s.handleAnthropicModels)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", s.handleReady)
	mux.Handle("/metrics", s.metricsHandler())
	mux.Handle("/admin/api/", s.adminGuard(http.HandlerFunc(s.handleAdmin)))
	mux.HandleFunc("/", s.handleRoot)
	return s.securityHeaders(s.recoverer(s.withRequestID(normalizePath(mux))))
}

const contentSecurityPolicy = "default-src 'self'; script-src 'self'; " +
	"style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self' data:; " +
	"connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"

// adminDisabledMessage is returned while the management surface is closed.
const adminDisabledMessage = "the management console is disabled by configuration"

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", contentSecurityPolicy)
		if s.isHTTPS(r) {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// isHTTPS reports whether the request reached the gateway over TLS. A forwarded
// proto header is only believed when the immediate peer is a configured proxy:
// otherwise any client could claim to be on HTTPS.
func (s *Server) isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if !clientip.IsTrustedPeer(r, s.trustedProxies) {
		return false
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// adminGuard rejects admin requests from outside the configured networks.
// An empty list keeps the historical behaviour, because the operator may rely
// on a network boundary in front of the gateway.
//
// A closed management surface short-circuits the whole check: no credential,
// not even the static admin token, opens it while it is shut.
func (s *Server) adminGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.adminSurfaceOpen() {
			writeAdminError(w, http.StatusForbidden, adminDisabledMessage)
			return
		}
		allowed := s.adminPrefixes.Load()
		if allowed == nil || len(*allowed) == 0 {
			next.ServeHTTP(w, r)
			return
		}
		// The immediate peer is the trustworthy part; the resolved client
		// address additionally lets the policy work behind a reverse proxy,
		// provided that proxy is listed in trusted_proxies and overwrites or
		// appends X-Forwarded-For itself.
		peer, peerOK := clientip.PeerAddr(r)
		resolved, err := netip.ParseAddr(s.clientIP(r))
		ok := (peerOK && prefixContains(*allowed, peer)) ||
			(err == nil && prefixContains(*allowed, resolved))
		if !ok {
			s.log.Warn("admin request rejected by admin_allowed_cidrs",
				"request_id", requestID(r), "ip", s.clientIP(r), "path", r.URL.Path)
			writeAdminError(w, http.StatusForbidden, "admin access is restricted by network policy")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func prefixContains(prefixes []netip.Prefix, addr netip.Addr) bool {
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

func normalizePath(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = ingress.Normalize(r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

type requestIDKey struct{}

func (s *Server) withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := validRequestID(r.Header.Get("X-Request-Id"))
		if id == "" {
			buf := make([]byte, 8)
			_, _ = rand.Read(buf)
			id = hex.EncodeToString(buf)
		}
		w.Header().Set("X-Request-Id", id)
		ctx := context.WithValue(r.Context(), requestIDKey{}, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// validRequestID accepts only short, header-safe client supplied IDs.
func validRequestID(v string) string {
	if len(v) < 8 || len(v) > 128 {
		return ""
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '-', r == '_', r == '.':
		default:
			return ""
		}
	}
	return v
}

func requestID(r *http.Request) string {
	if v, ok := r.Context().Value(requestIDKey{}).(string); ok {
		return v
	}
	return ""
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic while serving",
					"request_id", requestID(r), "path", r.URL.Path,
					"panic", rec, "stack", string(debug.Stack()))
				ingress.WriteError(w, protocolOfPath(r.URL.Path), http.StatusInternalServerError,
					"api_error", "internal error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func protocolOfPath(path string) string {
	if strings.HasPrefix(path, "/anthropic") {
		return config.ProtocolAnthropic
	}
	return config.ProtocolOpenAI
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if isDataPlanePath(r.URL.Path) {
		// The console is shut down: say so instead of pretending the path
		// does not exist. The data plane keeps its own error shape.
		if !s.adminSurfaceOpen() && strings.HasPrefix(r.URL.Path, "/admin") {
			writeAdminError(w, http.StatusForbidden, adminDisabledMessage)
			return
		}
		protocol := protocolOfPath(r.URL.Path)
		ingress.WriteError(w, protocol, http.StatusNotFound, "invalid_request_error",
			"unknown path "+r.URL.Path+". OpenAI base_url must be <gateway>/openai/v1; "+
				"Anthropic base_url must be <gateway>/anthropic.")
		return
	}
	if !s.adminSurfaceOpen() {
		// Do not serve the console shell: a page that loads and then fails
		// every request is harder to diagnose than a plain refusal.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusForbidden)
		_, _ = io.WriteString(w, adminDisabledMessage+"\n")
		return
	}
	if s.ui != nil {
		s.ui.ServeHTTP(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	metricsLine := ""
	if s.metricsTokenValue() != "" {
		metricsLine = "  <li><code>GET  /metrics</code>（需 metrics_token）</li>\n"
	}
	_, _ = io.WriteString(w, `<!doctype html>
<html><head><meta charset="utf-8"><title>llmgw</title></head>
<body style="font-family:ui-sans-serif,system-ui;max-width:720px;margin:48px auto;line-height:1.7">
<h1>llmgw</h1>
<p>Web 控制台未构建。运行 <code>make web</code> 后重新编译。</p>
<ul>
  <li><code>POST /openai/v1/chat/completions</code></li>
  <li><code>POST /openai/v1/responses</code></li>
  <li><code>GET  /openai/v1/models</code></li>
  <li><code>POST /anthropic/v1/messages</code></li>
  <li><code>POST /anthropic/v1/messages/count_tokens</code></li>
  <li><code>GET  /anthropic/v1/models</code></li>
  <li><code>GET  /admin/api/keys</code>（需 admin_token）</li>
`+metricsLine+`</ul>
</body></html>`)
}

// isDataPlanePath reports whether an unknown path belongs to the API surface
// rather than the SPA, so callers get a JSON error instead of an HTML shell.
func isDataPlanePath(path string) bool {
	for _, prefix := range []string{"/openai", "/anthropic", "/admin", "/v1"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request, protocol, endpoint string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		ingress.WriteError(w, protocol, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	key, ok := s.authenticate(w, r, protocol)
	if !ok {
		return
	}
	release, ok := s.reserveKey(w, r, key, protocol)
	if !ok {
		return
	}
	defer release()
	body, err := io.ReadAll(io.LimitReader(r.Body, s.maxBody+1))
	if err != nil {
		ingress.WriteError(w, protocol, http.StatusBadRequest, "invalid_request_error", "cannot read request body")
		return
	}
	if int64(len(body)) > s.maxBody {
		ingress.WriteError(w, protocol, http.StatusRequestEntityTooLarge, "invalid_request_error", "request body too large")
		return
	}

	var meta ingress.Meta
	if protocol == config.ProtocolAnthropic {
		meta, err = ingress.ParseAnthropic(body)
	} else {
		meta, err = ingress.ParseOpenAI(body)
	}
	if err != nil {
		ingress.WriteError(w, protocol, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}

	if endpoint == transport.EndpointAnthropicCountTokens {
		meta.Stream = false
	}
	if !key.AllowsModel(meta.Model) {
		ingress.WriteError(w, protocol, http.StatusForbidden, "permission_error",
			"gateway key is not allowed to access model "+meta.Model)
		return
	}

	opts := router.ResolveOptions{}
	if snap := s.router.Snapshot(); snap != nil && snap.Strategies[meta.Model] == config.StrategySticky {
		sessionKey, err := ingress.SessionKey(r.Header, key.ID, body)
		if err != nil {
			// The body was already validated; a fingerprint failure only
			// costs session affinity, so route without it instead of failing
			// an otherwise valid request.
			s.log.Warn("derive sticky session key failed; routing without session affinity",
				"request_id", requestID(r), "model", meta.Model, "err", err)
		} else {
			opts.SessionKey = sessionKey
		}
	}
	targets, err := s.router.ResolveWithOptions(protocol, meta.Model, opts)
	if err != nil {
		var nf *router.ModelNotFoundError
		if errors.As(err, &nf) && nf.Hint != "" {
			// The model exists, but on the other protocol's endpoint.
			ingress.WriteError(w, protocol, http.StatusBadRequest, "invalid_request_error",
				nf.Error())
			return
		}
		var na *router.NoAvailableTargetError
		if errors.As(err, &na) {
			// The model exists but every candidate is outside its time
			// window right now.
			ingress.WriteError(w, protocol, http.StatusServiceUnavailable, "overloaded_error",
				na.Error())
			return
		}
		ingress.WriteError(w, protocol, http.StatusNotFound, "not_found_error", err.Error())
		return
	}

	s.store.TouchKey(key.ID)

	s.forwarder.Forward(w, r, transport.Request{
		Protocol:  protocol,
		Endpoint:  endpoint,
		Meta:      meta,
		Targets:   targets,
		APIKeyID:  key.ID,
		RequestID: requestID(r),
	})
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		ingress.WriteError(w, config.ProtocolOpenAI, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	key, ok := s.authenticate(w, r, config.ProtocolOpenAI)
	if !ok {
		return
	}
	release, ok := s.reserveKey(w, r, key, config.ProtocolOpenAI)
	if !ok {
		return
	}
	defer release()
	type model struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
		// The fields below are extensions over the OpenAI shape. They are
		// omitted entirely when the operator declared no metadata, so the
		// minimal response stays byte-compatible with before.
		DisplayName     string   `json:"display_name,omitempty"`
		ContextLength   int      `json:"context_length,omitempty"`
		MaxOutputTokens int      `json:"max_output_tokens,omitempty"`
		Capabilities    []string `json:"capabilities,omitempty"`
	}
	out := struct {
		Object string  `json:"object"`
		Data   []model `json:"data"`
	}{Object: "list", Data: []model{}}
	for _, info := range s.router.ModelInfos(config.ProtocolOpenAI) {
		if !key.AllowsModel(info.Name) {
			continue
		}
		out.Data = append(out.Data, model{
			ID:              info.Name,
			Object:          "model",
			OwnedBy:         "llmgw",
			DisplayName:     info.Metadata.DisplayName,
			ContextLength:   info.Metadata.ContextLength,
			MaxOutputTokens: info.Metadata.MaxOutputTokens,
			Capabilities:    info.Metadata.Capabilities,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(out); err != nil {
		s.log.Warn("encode models response failed",
			"request_id", requestID(r), "path", r.URL.Path, "err", err)
	}
}

// handleAnthropicModels serves the Anthropic-shaped model list. Anthropic
// clients read type/id/display_name; the size and capability fields are the
// same gateway extensions as on the OpenAI endpoint.
func (s *Server) handleAnthropicModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		ingress.WriteError(w, config.ProtocolAnthropic, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	key, ok := s.authenticate(w, r, config.ProtocolAnthropic)
	if !ok {
		return
	}
	release, ok := s.reserveKey(w, r, key, config.ProtocolAnthropic)
	if !ok {
		return
	}
	defer release()
	type model struct {
		Type            string   `json:"type"`
		ID              string   `json:"id"`
		DisplayName     string   `json:"display_name"`
		CreatedAt       string   `json:"created_at,omitempty"`
		ContextLength   int      `json:"context_length,omitempty"`
		MaxOutputTokens int      `json:"max_output_tokens,omitempty"`
		Capabilities    []string `json:"capabilities,omitempty"`
	}
	out := struct {
		Data    []model `json:"data"`
		HasMore bool    `json:"has_more"`
		FirstID string  `json:"first_id,omitempty"`
		LastID  string  `json:"last_id,omitempty"`
	}{Data: []model{}}
	for _, info := range s.router.ModelInfos(config.ProtocolAnthropic) {
		if !key.AllowsModel(info.Name) {
			continue
		}
		display := info.Metadata.DisplayName
		if display == "" {
			display = info.Name
		}
		out.Data = append(out.Data, model{
			Type:            "model",
			ID:              info.Name,
			DisplayName:     display,
			ContextLength:   info.Metadata.ContextLength,
			MaxOutputTokens: info.Metadata.MaxOutputTokens,
			Capabilities:    info.Metadata.Capabilities,
		})
	}
	if len(out.Data) > 0 {
		out.FirstID = out.Data[0].ID
		out.LastID = out.Data[len(out.Data)-1].ID
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(out); err != nil {
		s.log.Warn("encode models response failed",
			"request_id", requestID(r), "path", r.URL.Path, "err", err)
	}
}

func (s *Server) authenticate(w http.ResponseWriter, r *http.Request, protocol string) (*store.APIKey, bool) {
	raw := auth.Extract(r)
	if raw == "" {
		ingress.WriteError(w, protocol, http.StatusUnauthorized, "authentication_error",
			"missing gateway key; send Authorization: Bearer sk-gw-... or x-api-key: sk-gw-...")
		return nil, false
	}
	// Authentication is served from memory; the database is only consulted at
	// startup and by admin mutations.
	k, ok := s.keys.get(auth.Hash(raw))
	if !ok {
		ingress.WriteError(w, protocol, http.StatusUnauthorized, "authentication_error", "invalid gateway key")
		return nil, false
	}
	if !k.Enabled {
		ingress.WriteError(w, protocol, http.StatusUnauthorized, "authentication_error", "gateway key is disabled")
		return nil, false
	}
	if k.IsExpired(time.Now()) {
		ingress.WriteError(w, protocol, http.StatusUnauthorized, "authentication_error", "gateway key has expired")
		return nil, false
	}
	return &k, true
}

// reserveKey applies the per-key budget before any expensive work happens.
func (s *Server) reserveKey(w http.ResponseWriter, r *http.Request, key *store.APIKey, protocol string) (func(), bool) {
	release, ok, retry := s.keyLimiter.acquireWithLimits(key.ID, key.RPMLimit, key.MaxConcurrent)
	if !ok {
		if retry > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(retry.Seconds())+1))
		}
		ingress.WriteError(w, protocol, http.StatusTooManyRequests, "rate_limit_error",
			"gateway key exceeded its request budget; slow down and retry")
		return nil, false
	}
	return release, true
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if !s.router.Ready() || s.store.Ping(ctx) != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, "not ready\n")
		return
	}
	_, _ = io.WriteString(w, "ready\n")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeAdminError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{
		"error": map[string]any{"type": "invalid_request", "message": msg},
	})
}

func parseID(s string) (int64, bool) {
	id, err := strconv.ParseInt(s, 10, 64)
	return id, err == nil
}
