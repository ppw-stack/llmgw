package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pw/llmgw/internal/config"
	"github.com/pw/llmgw/internal/router"
	"github.com/pw/llmgw/internal/store"
)

type endpointInput struct {
	Name     string            `json:"name"`
	Protocol string            `json:"protocol"`
	BaseURL  string            `json:"base_url"`
	Version  string            `json:"version"`
	Models   map[string]string `json:"models"`
}

type providerInput struct {
	Name      string          `json:"name"`
	Proxy     string          `json:"proxy"`
	TimeoutMS int             `json:"timeout_ms"`
	Keys      *[]string       `json:"keys"`
	Endpoints []endpointInput `json:"endpoints"`
}

type endpointView struct {
	Name     string            `json:"name"`
	Protocol string            `json:"protocol"`
	BaseURL  string            `json:"base_url"`
	Version  string            `json:"version"`
	Models   map[string]string `json:"models"`
}

type providerView struct {
	ID        int64          `json:"id"`
	Name      string         `json:"name"`
	Proxy     string         `json:"proxy"`
	TimeoutMS int            `json:"timeout_ms"`
	KeyCount  int            `json:"key_count"`
	KeyHints  []string       `json:"key_hints"`
	Endpoints []endpointView `json:"endpoints"`
}

func (s *Server) adminProviders(w http.ResponseWriter, r *http.Request, id *adminIdentity, rawID string) {
	if rawID == "" {
		switch r.Method {
		case http.MethodGet:
			s.listProviders(w)
		case http.MethodPost:
			s.createProvider(w, r, id)
		default:
			writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	providerID, ok := parseID(rawID)
	if !ok {
		writeAdminError(w, http.StatusBadRequest, "invalid provider id")
		return
	}
	switch r.Method {
	case http.MethodPut:
		s.updateProvider(w, r, id, providerID)
	case http.MethodDelete:
		s.deleteProvider(w, r, id, providerID)
	default:
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) listProviders(w http.ResponseWriter) {
	records, err := s.store.ListProviderRecords()
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	views := make([]providerView, 0, len(records))
	for _, rec := range records {
		views = append(views, s.providerView(rec))
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) providerView(rec store.ProviderRecord) providerView {
	hints := make([]string, 0, len(rec.EncryptedKeys))
	for _, enc := range rec.EncryptedKeys {
		hints = append(hints, s.keyHint(enc))
	}
	endpoints := make([]endpointView, 0, len(rec.Endpoints))
	for _, ep := range rec.Endpoints {
		endpoints = append(endpoints, endpointView{
			Name: ep.Name, Protocol: ep.Protocol, BaseURL: ep.BaseURL,
			Version: ep.Version, Models: ep.Models,
		})
	}
	return providerView{
		ID: rec.ID, Name: rec.Name, Proxy: rec.Proxy,
		TimeoutMS: rec.TimeoutMS, KeyCount: len(rec.EncryptedKeys),
		KeyHints: hints, Endpoints: endpoints,
	}
}

func (s *Server) keyHint(enc string) string {
	plain, err := s.box.Decrypt(enc)
	if err != nil || len(plain) < 4 {
		return "****"
	}
	return "****" + plain[len(plain)-4:]
}

func (s *Server) createProvider(w http.ResponseWriter, r *http.Request, id *adminIdentity) {
	var in providerInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	provider, err := in.toProvider()
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}

	current, err := s.store.Providers(s.box)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, existing := range current {
		if existing.Name == provider.Name {
			writeAdminError(w, http.StatusConflict, "provider name already exists")
			return
		}
	}
	if err := s.validateProviders(append(current, provider)); err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}

	encoded, err := s.encryptKeys(provider.Keys)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	timeoutMS := int(provider.Timeout.Value() / time.Millisecond)
	rec, err := s.store.CreateProvider(provider.Name, provider.Proxy, timeoutMS, encoded, provider.Endpoints)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.reloadProviders(); err != nil {
		s.log.Error("reload providers after create", "err", err)
		writeAdminError(w, http.StatusInternalServerError, "provider saved but routing reload failed: "+err.Error())
		return
	}
	s.audit(r, id.Username, "provider.create", provider.Name, "success")
	writeJSON(w, http.StatusCreated, s.providerView(*rec))
}

func (s *Server) updateProvider(w http.ResponseWriter, r *http.Request, id *adminIdentity, providerID int64) {
	var in providerInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	provider, err := in.toProvider()
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}

	records, err := s.store.ListProviderRecords()
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var existing *store.ProviderRecord
	for i := range records {
		if records[i].ID == providerID {
			existing = &records[i]
			continue
		}
		if records[i].Name == provider.Name {
			writeAdminError(w, http.StatusConflict, "provider name already exists")
			return
		}
	}
	if existing == nil {
		writeAdminError(w, http.StatusNotFound, "provider not found")
		return
	}

	// An omitted keys field preserves the stored keys; [] clears them.
	encoded, err := s.resolveKeys(in.Keys, existing)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Validate against the decrypted temporary keys so the router check sees
	// the same shape that will be live afterwards.
	plainKeys, err := s.decryptKeys(encoded)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	candidate := provider
	candidate.Keys = plainKeys
	current, err := s.store.Providers(s.box)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	replaced := make([]config.Provider, 0, len(current))
	for _, p := range current {
		if p.Name == existing.Name {
			replaced = append(replaced, candidate)
			continue
		}
		replaced = append(replaced, p)
	}
	if err := s.validateProviders(replaced); err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}

	timeoutMS := int(provider.Timeout.Value() / time.Millisecond)
	if err := s.store.UpdateProvider(providerID, provider.Name, provider.Proxy, timeoutMS,
		encoded, provider.Endpoints); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		}
		writeAdminError(w, status, err.Error())
		return
	}
	if err := s.reloadProviders(); err != nil {
		s.log.Error("reload providers after update", "err", err)
		writeAdminError(w, http.StatusInternalServerError, "provider saved but routing reload failed: "+err.Error())
		return
	}
	s.audit(r, id.Username, "provider.update", provider.Name, "success")
	rec := store.ProviderRecord{
		ID: providerID, Name: provider.Name, Proxy: provider.Proxy,
		TimeoutMS: timeoutMS, EncryptedKeys: encoded, Endpoints: provider.Endpoints,
	}
	writeJSON(w, http.StatusOK, s.providerView(rec))
}

func (s *Server) deleteProvider(w http.ResponseWriter, r *http.Request, id *adminIdentity, providerID int64) {
	if err := s.store.DeleteProvider(providerID); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		}
		writeAdminError(w, status, err.Error())
		return
	}
	if err := s.reloadProviders(); err != nil {
		s.log.Error("reload providers after delete", "err", err)
		writeAdminError(w, http.StatusInternalServerError, "provider deleted but routing reload failed: "+err.Error())
		return
	}
	s.audit(r, id.Username, "provider.delete", strconv.FormatInt(providerID, 10), "success")
	w.WriteHeader(http.StatusNoContent)
}

func (in providerInput) toProvider() (config.Provider, error) {
	timeout := time.Duration(in.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = 120 * time.Second
	}
	var keys []string
	if in.Keys != nil {
		keys = *in.Keys
	}
	endpoints := make([]config.Endpoint, 0, len(in.Endpoints))
	for _, ep := range in.Endpoints {
		endpoints = append(endpoints, config.Endpoint{
			Name: strings.TrimSpace(ep.Name), Protocol: ep.Protocol,
			BaseURL: strings.TrimSpace(ep.BaseURL), Version: ep.Version,
			Models: ep.Models,
		})
	}
	p := config.Provider{
		Name:      strings.TrimSpace(in.Name),
		Proxy:     strings.TrimSpace(in.Proxy),
		Timeout:   config.Duration(timeout),
		Keys:      keys,
		Endpoints: endpoints,
	}
	if err := config.NormalizeProvider(&p); err != nil {
		return config.Provider{}, err
	}
	return p, nil
}

func (s *Server) validateProviders(providers []config.Provider) error {
	snap := s.router.Snapshot()
	if snap == nil {
		return errors.New("router is not ready")
	}
	cfg := *snap.Config
	cfg.Providers = providers
	_, err := router.Build(&cfg)
	return err
}

func (s *Server) encryptKeys(keys []string) ([]string, error) {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		enc, err := s.box.Encrypt(k)
		if err != nil {
			return nil, err
		}
		out = append(out, enc)
	}
	return out, nil
}

func (s *Server) decryptKeys(encoded []string) ([]string, error) {
	out := make([]string, 0, len(encoded))
	for _, enc := range encoded {
		plain, err := s.box.Decrypt(enc)
		if err != nil {
			return nil, err
		}
		out = append(out, plain)
	}
	return out, nil
}

func (s *Server) resolveKeys(keys *[]string, existing *store.ProviderRecord) ([]string, error) {
	if keys == nil {
		return existing.EncryptedKeys, nil
	}
	return s.encryptKeys(*keys)
}

func (s *Server) reloadProviders() error {
	providers, err := s.store.Providers(s.box)
	if err != nil {
		return err
	}
	snap := s.router.Snapshot()
	if snap == nil {
		return errors.New("router is not ready")
	}
	cfg := *snap.Config
	cfg.Providers = providers
	return s.router.Reload(&cfg)
}
