package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/pw/llmgw/internal/config"
	"github.com/pw/llmgw/internal/preset"
	"github.com/pw/llmgw/internal/router"
	"github.com/pw/llmgw/internal/store"
)

type providerInput struct {
	Name             string   `json:"name"`
	Preset           *string  `json:"preset"`
	APIKey           *string  `json:"api_key"`
	Models           []string `json:"models"`
	OpenAIBaseURL    string   `json:"openai_base_url"`
	AnthropicBaseURL string   `json:"anthropic_base_url"`
}

type providerView struct {
	ID               int64    `json:"id"`
	Name             string   `json:"name"`
	Preset           string   `json:"preset,omitempty"`
	PresetName       string   `json:"preset_name,omitempty"`
	Source           string   `json:"source"`
	DocsURL          string   `json:"docs_url,omitempty"`
	KeyHint          string   `json:"key_hint"`
	Models           []string `json:"models"`
	OpenAIBaseURL    string   `json:"openai_base_url"`
	AnthropicBaseURL string   `json:"anthropic_base_url"`
}

func (s *Server) adminProviders(w http.ResponseWriter, r *http.Request, id *adminIdentity, rawID string) {
	if rawID == "" {
		switch r.Method {
		case http.MethodGet:
			s.listProviders(w, r)
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

func (s *Server) adminProviderPresets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, preset.All())
}

func (s *Server) listProviders(w http.ResponseWriter, r *http.Request) {
	records, err := s.store.ListProviderRecords()
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	views := make([]providerView, 0, len(records))
	for _, rec := range records {
		views = append(views, s.providerView(r, rec))
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) providerView(r *http.Request, rec store.ProviderRecord) providerView {
	view := providerView{
		ID:               rec.ID,
		Name:             rec.Name,
		Preset:           rec.Preset,
		Source:           "custom",
		KeyHint:          s.keyHint(rec.EncryptedKey),
		Models:           rec.Models.List(),
		OpenAIBaseURL:    rec.OpenAIBaseURL,
		AnthropicBaseURL: rec.AnthropicBaseURL,
	}
	if rec.Preset != "" {
		view.Source = "builtin"
		if pr, ok := preset.Lookup(rec.Preset); ok {
			view.PresetName = pr.FamilyName + "（" + pr.VariantName + "）"
			view.DocsURL = pr.DocsURL
		}
		p := config.Provider{Name: rec.Name, Preset: rec.Preset, Models: rec.Models}
		eff, err := p.Effective()
		if err != nil {
			// The stored values stay in the view, but the mismatch must be
			// visible in the logs instead of being swallowed.
			s.log.Error("resolve preset for provider view",
				"method", r.Method, "path", r.URL.Path, "request_id", requestID(r),
				"provider", rec.Name, "preset", rec.Preset, "err", err)
		} else {
			view.OpenAIBaseURL = eff.OpenAIBaseURL
			view.AnthropicBaseURL = eff.AnthropicBaseURL
			view.Models = eff.Models.List()
		}
	}
	if view.Models == nil {
		view.Models = []string{}
	}
	return view
}

func (s *Server) keyHint(enc string) string {
	if enc == "" {
		return ""
	}
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
	apiKey := trimOrEmpty(in.APIKey)
	presetID := trimOrEmpty(in.Preset)
	provider, err := in.toProvider(presetID, apiKey)
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

	encrypted, err := s.encryptKey(provider.APIKey)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	rec, err := s.store.CreateProvider(store.ProviderSpec{
		Name:             provider.Name,
		Preset:           provider.Preset,
		EncryptedKey:     encrypted,
		Models:           provider.Models,
		OpenAIBaseURL:    provider.OpenAIBaseURL,
		AnthropicBaseURL: provider.AnthropicBaseURL,
	})
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
	writeJSON(w, http.StatusCreated, s.providerView(r, *rec))
}

func (s *Server) updateProvider(w http.ResponseWriter, r *http.Request, id *adminIdentity, providerID int64) {
	var in providerInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid JSON body")
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
		if records[i].Name == strings.TrimSpace(in.Name) {
			writeAdminError(w, http.StatusConflict, "provider name already exists")
			return
		}
	}
	if existing == nil {
		writeAdminError(w, http.StatusNotFound, "provider not found")
		return
	}
	presetID := existing.Preset
	if in.Preset != nil {
		presetID = trimOrEmpty(in.Preset)
	}
	if presetID != existing.Preset {
		writeAdminError(w, http.StatusBadRequest,
			"preset cannot be changed; delete this provider and create a new one")
		return
	}
	// Omitting models keeps the stored list; an explicit [] falls back to the
	// preset defaults (or is rejected for a custom provider).
	if in.Models == nil {
		in.Models = existing.Models.List()
	}

	// An omitted or empty api_key means "keep the stored key".
	encrypted := existing.EncryptedKey
	if in.APIKey != nil && strings.TrimSpace(*in.APIKey) != "" {
		encrypted, err = s.encryptKey(strings.TrimSpace(*in.APIKey))
		if err != nil {
			writeAdminError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	plainKey := ""
	if encrypted != "" {
		if plainKey, err = s.box.Decrypt(encrypted); err != nil {
			writeAdminError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}

	provider, err := in.toProvider(presetID, plainKey)
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}
	current, err := s.store.Providers(s.box)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	replaced := make([]config.Provider, 0, len(current))
	for _, p := range current {
		if p.Name == existing.Name {
			replaced = append(replaced, provider)
			continue
		}
		replaced = append(replaced, p)
	}
	if err := s.validateProviders(replaced); err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}

	spec := store.ProviderSpec{
		Name:             provider.Name,
		Preset:           provider.Preset,
		EncryptedKey:     encrypted,
		Models:           provider.Models,
		OpenAIBaseURL:    provider.OpenAIBaseURL,
		AnthropicBaseURL: provider.AnthropicBaseURL,
	}
	if err := s.store.UpdateProvider(providerID, spec); err != nil {
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
	writeJSON(w, http.StatusOK, s.providerView(r, store.ProviderRecord{
		ID: providerID, ProviderSpec: spec,
	}))
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

// trimOrEmpty reads an optional request field: a missing pointer and a blank
// value are both treated as "not supplied".
func trimOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}

func (in providerInput) toProvider(presetID, apiKey string) (config.Provider, error) {
	var models config.Models
	if len(in.Models) > 0 {
		parsed, err := config.ParseModels(in.Models)
		if err != nil {
			return config.Provider{}, fmt.Errorf("invalid models: %w", err)
		}
		models = parsed
	}
	p := config.Provider{
		Name:             strings.TrimSpace(in.Name),
		Preset:           presetID,
		APIKey:           apiKey,
		Models:           models,
		OpenAIBaseURL:    strings.TrimSpace(in.OpenAIBaseURL),
		AnthropicBaseURL: strings.TrimSpace(in.AnthropicBaseURL),
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

func (s *Server) encryptKey(key string) (string, error) {
	if strings.TrimSpace(key) == "" {
		return "", nil
	}
	return s.box.Encrypt(strings.TrimSpace(key))
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
