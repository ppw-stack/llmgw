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
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/pw/llmgw/internal/auth"
	"github.com/pw/llmgw/internal/config"
	"github.com/pw/llmgw/internal/ingress"
	"github.com/pw/llmgw/internal/router"
	"github.com/pw/llmgw/internal/store"
	"github.com/pw/llmgw/internal/transport"
)

type Options struct {
	Router     *router.Router
	Forwarder  *transport.Forwarder
	Store      *store.Store
	AdminToken string
	MaxBody    int64
	Log        *slog.Logger
}

type Server struct {
	router     *router.Router
	forwarder  *transport.Forwarder
	store      *store.Store
	adminToken string
	maxBody    int64
	log        *slog.Logger
}

func New(o Options) *Server {
	if o.MaxBody <= 0 {
		o.MaxBody = 32 << 20
	}
	return &Server{
		router:     o.Router,
		forwarder:  o.Forwarder,
		store:      o.Store,
		adminToken: o.AdminToken,
		maxBody:    o.MaxBody,
		log:        o.Log,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/openai/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		s.handleChat(w, r, config.ProtocolOpenAI)
	})
	mux.HandleFunc("/anthropic/v1/messages", func(w http.ResponseWriter, r *http.Request) {
		s.handleChat(w, r, config.ProtocolAnthropic)
	})
	mux.HandleFunc("/openai/v1/models", s.handleModels)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("/readyz", s.handleReady)
	mux.Handle("/metrics", metricsHandler())
	mux.HandleFunc("/admin/api/", s.handleAdmin)
	mux.HandleFunc("/", s.handleRoot)
	return s.recoverer(s.withRequestID(normalizePath(mux)))
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
		id := r.Header.Get("X-Request-Id")
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
	if r.URL.Path != "/" {
		protocol := protocolOfPath(r.URL.Path)
		ingress.WriteError(w, protocol, http.StatusNotFound, "invalid_request_error",
			"unknown path "+r.URL.Path+". OpenAI base_url must be <gateway>/openai/v1; "+
				"Anthropic base_url must be <gateway>/anthropic.")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, `<!doctype html>
<html><head><meta charset="utf-8"><title>llmgw</title></head>
<body style="font-family:ui-sans-serif,system-ui;max-width:720px;margin:48px auto;line-height:1.7">
<h1>llmgw</h1>
<p>轻量 LLM 网关。管理台在 M3 提供，当前可用接口：</p>
<ul>
  <li><code>POST /openai/v1/chat/completions</code></li>
  <li><code>GET  /openai/v1/models</code></li>
  <li><code>POST /anthropic/v1/messages</code></li>
  <li><code>GET  /admin/api/keys</code>（需 admin_token）</li>
  <li><code>GET  /metrics</code></li>
</ul>
<p>OpenAI base_url: <code>/openai/v1</code> · Anthropic base_url: <code>/anthropic</code></p>
</body></html>`)
}

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request, protocol string) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		ingress.WriteError(w, protocol, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	key, ok := s.authenticate(w, r, protocol)
	if !ok {
		return
	}
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

	target, err := s.router.Resolve(protocol, meta.Model)
	if err != nil {
		var nf *router.ModelNotFoundError
		if errors.As(err, &nf) && nf.Hint != "" {
			// The model exists, but on the other protocol's endpoint.
			ingress.WriteError(w, protocol, http.StatusBadRequest, "invalid_request_error",
				nf.Error())
			return
		}
		ingress.WriteError(w, protocol, http.StatusNotFound, "not_found_error", err.Error())
		return
	}

	go s.store.TouchKey(key.ID)

	s.forwarder.Forward(w, r, transport.Request{
		Protocol:  protocol,
		Meta:      meta,
		Target:    target,
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
	if _, ok := s.authenticate(w, r, config.ProtocolOpenAI); !ok {
		return
	}
	type model struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		OwnedBy string `json:"owned_by"`
	}
	out := struct {
		Object string  `json:"object"`
		Data   []model `json:"data"`
	}{Object: "list"}
	for _, m := range s.router.ModelList(config.ProtocolOpenAI) {
		out.Data = append(out.Data, model{ID: m, Object: "model", OwnedBy: "llmgw"})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (s *Server) authenticate(w http.ResponseWriter, r *http.Request, protocol string) (*store.APIKey, bool) {
	raw := auth.Extract(r)
	if raw == "" {
		ingress.WriteError(w, protocol, http.StatusUnauthorized, "authentication_error",
			"missing gateway key; send Authorization: Bearer sk-gw-... or x-api-key: sk-gw-...")
		return nil, false
	}
	k, err := s.store.KeyByHash(auth.Hash(raw))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			ingress.WriteError(w, protocol, http.StatusUnauthorized, "authentication_error", "invalid gateway key")
			return nil, false
		}
		s.log.Error("lookup gateway key", "err", err)
		ingress.WriteError(w, protocol, http.StatusInternalServerError, "api_error", "internal error")
		return nil, false
	}
	if !k.Enabled {
		ingress.WriteError(w, protocol, http.StatusUnauthorized, "authentication_error", "gateway key is disabled")
		return nil, false
	}
	return k, true
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
