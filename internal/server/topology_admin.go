package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pw/llmgw/internal/config"
	"github.com/pw/llmgw/internal/discovery"
	"github.com/pw/llmgw/internal/preset"
	"github.com/pw/llmgw/internal/router"
	"github.com/pw/llmgw/internal/store"
)

// endpointInput is one protocol entrance of an instance.
type endpointInput struct {
	Protocol string `json:"protocol"`
	BaseURL  string `json:"base_url"`
}

// instanceInput is the write form of an instance.
type instanceInput struct {
	Name            string                `json:"name"`
	Preset          *string               `json:"preset"`
	APIKey          *string               `json:"api_key"`
	QuotaCredential *quotaCredentialInput `json:"quota_credential"`
	Endpoints       []endpointInput       `json:"endpoints"`
	Models          []string              `json:"models"`
	MaxFails        *int                  `json:"max_fails"`
	CooldownSecs    *int                  `json:"cooldown_secs"`
	TimeoutSecs     *int                  `json:"timeout_secs"`
	// AllowPrivateUpstream opts an instance into loopback/private endpoints.
	AllowPrivateUpstream bool `json:"allow_private_upstream"`
}

// quotaCredentialInput is the write form of the extra credential some
// vendors need to read the subscription quota. The secret is only written,
// never read back.
type quotaCredentialInput struct {
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
}

// quotaCredentialView is the read form of the quota credential: the access
// key id identifies the credential, the secret stays masked.
type quotaCredentialView struct {
	AccessKeyID string `json:"access_key_id,omitempty"`
	SecretHint  string `json:"secret_hint,omitempty"`
}

// instanceView is the read form of an instance.
type instanceView struct {
	ID              int64                `json:"id"`
	Name            string               `json:"name"`
	Preset          string               `json:"preset,omitempty"`
	PresetName      string               `json:"preset_name,omitempty"`
	Source          string               `json:"source"`
	DocsURL         string               `json:"docs_url,omitempty"`
	KeyHint         string               `json:"key_hint"`
	QuotaCredential *quotaCredentialView `json:"quota_credential,omitempty"`
	Endpoints       []endpointView       `json:"endpoints"`
	Models          []string             `json:"models"`
	MaxFails        int                  `json:"max_fails"`
	CooldownSecs    int                  `json:"cooldown_secs"`
	TimeoutSecs     int                  `json:"timeout_secs"`
	// AllowPrivateUpstream is the instance's loopback/private opt-in.
	AllowPrivateUpstream bool   `json:"allow_private_upstream"`
	CreatedAt            string `json:"created_at"`
	UpdatedAt            string `json:"updated_at"`
}

type endpointView struct {
	Protocol string `json:"protocol"`
	BaseURL  string `json:"base_url"`
}

// modelInstanceInput binds a model to one instance.
type modelInstanceInput struct {
	Instance string            `json:"instance"`
	Priority int               `json:"priority"`
	Weight   int               `json:"weight"`
	Windows  []timeWindowInput `json:"windows"`
}

// timeWindowInput is one time range of a candidate.
type timeWindowInput struct {
	Start    string `json:"start"`
	End      string `json:"end"`
	Priority *int   `json:"priority"`
	Only     bool   `json:"only"`
}

// modelInput is the write form of a model.
type modelInput struct {
	Name      string               `json:"name"`
	Upstream  string               `json:"upstream"`
	Strategy  string               `json:"strategy"`
	Instances []modelInstanceInput `json:"instances"`
}

// modelView is the read form of a model.
type modelView struct {
	ID        int64                `json:"id"`
	Name      string               `json:"name"`
	Upstream  string               `json:"upstream"`
	Strategy  string               `json:"strategy"`
	Instances []modelInstanceInput `json:"instances"`
	// Timezone is the timezone the time windows are evaluated in.
	Timezone string `json:"timezone"`
	// ServerTime is the moment the effective candidates were computed.
	ServerTime string `json:"server_time"`
	// Effective lists the candidates in the order they are tried right now.
	Effective []effectiveCandidate `json:"effective_candidates"`
	CreatedAt string               `json:"created_at"`
	UpdatedAt string               `json:"updated_at"`
}

// effectiveCandidate is the routing state of one candidate at the moment the
// model view was built.
type effectiveCandidate struct {
	Instance string `json:"instance"`
	// Priority is the priority that applies right now.
	Priority int `json:"priority"`
	// BasePriority is the priority the candidate is configured with.
	BasePriority int `json:"base_priority"`
	// Active reports whether the candidate may serve the model right now.
	Active bool `json:"active"`
	// Window describes the time window that is active right now.
	Window string `json:"window,omitempty"`
}

func (s *Server) adminInstances(w http.ResponseWriter, r *http.Request, id *adminIdentity, rawID string) {
	if rawID == "" {
		switch r.Method {
		case http.MethodGet:
			s.listInstances(w, r)
		case http.MethodPost:
			s.createInstance(w, r, id)
		default:
			writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	instanceID, ok := parseID(rawID)
	if !ok {
		writeAdminError(w, http.StatusBadRequest, "invalid instance id")
		return
	}
	switch r.Method {
	case http.MethodPut:
		s.updateInstance(w, r, id, instanceID)
	case http.MethodDelete:
		s.deleteInstance(w, r, id, instanceID)
	default:
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) adminModels(w http.ResponseWriter, r *http.Request, id *adminIdentity, rawID string) {
	if rawID == "" {
		switch r.Method {
		case http.MethodGet:
			s.listModels(w, r)
		case http.MethodPost:
			s.createModel(w, r, id)
		default:
			writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	modelID, ok := parseID(rawID)
	if !ok {
		writeAdminError(w, http.StatusBadRequest, "invalid model id")
		return
	}
	switch r.Method {
	case http.MethodPut:
		s.updateModel(w, r, id, modelID)
	case http.MethodDelete:
		s.deleteModel(w, r, id, modelID)
	default:
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) adminInstancePresets(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, http.StatusOK, preset.All())
}

// presetModelsTimeout bounds one model-list query made from the console.
const presetModelsTimeout = 10 * time.Second

var (
	presetModelsClient = &http.Client{Timeout: presetModelsTimeout}
	// presetModelsEndpoint is a variable so tests can point the handler at a
	// local server without reaching a real provider.
	presetModelsEndpoint = config.PresetModelsEndpoint
)

type presetModelsInput struct {
	Preset string `json:"preset"`
	APIKey string `json:"api_key"`
}

type presetModelsView struct {
	Models    []discovery.Candidate `json:"models"`
	Suggested []string              `json:"suggested"`
	Warning   string                `json:"warning,omitempty"`
}

// adminPresetModels queries the model list of a built-in preset with the
// submitted key. The key is only used for this request; nothing is stored.
func (s *Server) adminPresetModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var in presetModelsInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	pr, ok := preset.Lookup(strings.TrimSpace(in.Preset))
	if !ok {
		writeAdminError(w, http.StatusBadRequest, "unknown preset")
		return
	}
	apiKey := strings.TrimSpace(in.APIKey)
	if apiKey == "" {
		writeAdminError(w, http.StatusBadRequest, "an upstream API key is required")
		return
	}

	suggested, err := config.PresetModels(pr.ID)
	if err != nil {
		s.log.Error("load preset suggested models", "method", r.Method, "path", r.URL.Path,
			"request_id", requestID(r), "preset", pr.ID, "err", err)
		writeAdminError(w, http.StatusInternalServerError, "failed to load preset models")
		return
	}
	endpoint, protocol, err := presetModelsEndpoint(pr.ID)
	if err != nil {
		s.log.Error("resolve preset model list endpoint", "method", r.Method, "path", r.URL.Path,
			"request_id", requestID(r), "preset", pr.ID, "err", err)
		writeAdminError(w, http.StatusInternalServerError, "failed to load preset models")
		return
	}

	view := presetModelsView{
		Models:    []discovery.Candidate{},
		Suggested: suggested,
		Warning:   presetModelsWarning(pr),
	}
	ctx, cancel := context.WithTimeout(r.Context(), presetModelsTimeout)
	defer cancel()
	models, err := discovery.Fetch(ctx, presetModelsClient, endpoint, protocol, apiKey)
	if err != nil {
		s.log.Warn("query preset model list", "method", r.Method, "path", r.URL.Path,
			"request_id", requestID(r), "preset", pr.ID, "err", err)
		view.Warning = joinWarnings(view.Warning, "上游模型列表查询失败，已回退到预设建议")
		writeJSON(w, http.StatusOK, view)
		return
	}
	if len(models) == 0 {
		view.Warning = joinWarnings(view.Warning, "上游未返回可用的聊天模型，已回退到预设建议")
	} else {
		view.Models = models
	}
	writeJSON(w, http.StatusOK, view)
}

// presetModelsWarning explains why an upstream model list is not authoritative
// for the subscription the preset represents.
func presetModelsWarning(pr preset.Preset) string {
	switch pr.Family {
	case "minimax-mplan":
		return "上游模型列表可能包含已下线模型；请以实际调用为准"
	case "ark":
		return "上游返回的是账号可见模型，不保证在 Coding Plan 套餐内；请以实际调用为准"
	default:
		return ""
	}
}

func joinWarnings(existing, extra string) string {
	if existing == "" {
		return extra
	}
	if extra == "" {
		return existing
	}
	return existing + "；" + extra
}

// ---- instances ----

func (s *Server) listInstances(w http.ResponseWriter, r *http.Request) {
	records, err := s.store.ListInstanceRecords()
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	views := make([]instanceView, 0, len(records))
	for _, rec := range records {
		views = append(views, s.instanceView(r, rec))
	}
	writeJSON(w, http.StatusOK, views)
}

func (s *Server) instanceView(r *http.Request, rec store.InstanceRecord) instanceView {
	view := instanceView{
		ID:                   rec.ID,
		Name:                 rec.Name,
		Preset:               rec.Preset,
		Source:               "custom",
		KeyHint:              s.keyHint(rec.EncryptedKey),
		Models:               append([]string{}, rec.Models...),
		MaxFails:             rec.MaxFails,
		CooldownSecs:         rec.CooldownSecs,
		TimeoutSecs:          rec.TimeoutSecs,
		AllowPrivateUpstream: rec.AllowPrivateUpstream,
		CreatedAt:            rec.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		UpdatedAt:            rec.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
	}
	if rec.EncryptedQuotaCredential != "" {
		credential, err := s.quotaCredentialView(rec.EncryptedQuotaCredential)
		if err != nil {
			// A broken or foreign credential must not hide the whole
			// instance list: log it with the request and instance context
			// and degrade to an empty credential.
			s.log.Error("read quota credential", "method", r.Method, "path", r.URL.Path, "request_id", requestID(r), "instance", rec.Name, "err", err)
		} else {
			view.QuotaCredential = credential
		}
	}
	for _, ep := range rec.Endpoints {
		view.Endpoints = append(view.Endpoints, endpointView{Protocol: ep.Protocol, BaseURL: ep.BaseURL})
	}
	if view.Endpoints == nil {
		view.Endpoints = []endpointView{}
	}
	if rec.Preset != "" {
		view.Source = "builtin"
		if pr, ok := preset.Lookup(rec.Preset); ok {
			view.PresetName = pr.Label()
			view.DocsURL = pr.DocsURL
		}
	}
	return view
}

func (s *Server) createInstance(w http.ResponseWriter, r *http.Request, id *adminIdentity) {
	var in instanceInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	instance, err := in.toInstance("")
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.TrimSpace(in.APIKeyString()) == "" {
		writeAdminError(w, http.StatusBadRequest, "an upstream API key is required")
		return
	}

	records, err := s.store.ListInstanceRecords()
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, rec := range records {
		if rec.Name == instance.Name {
			writeAdminError(w, http.StatusConflict, "instance name already exists")
			return
		}
	}
	instances, models, err := s.store.Topology(s.box)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	instances = append(instances, instance)
	if err := s.validateTopology(instances, models); err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}

	encrypted, err := s.encryptKey(instance.APIKey)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	encryptedCredential, err := s.encryptQuotaCredential(instance.QuotaCredential)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	rec, err := s.store.CreateInstance(store.InstanceSpec{
		Name:                     instance.Name,
		Preset:                   instance.Preset,
		EncryptedKey:             encrypted,
		EncryptedQuotaCredential: encryptedCredential,
		Endpoints:                instance.Endpoints,
		Models:                   instance.Models,
		MaxFails:                 instance.MaxFails,
		CooldownSecs:             instance.CooldownSecs,
		TimeoutSecs:              instance.TimeoutSecs,
		AllowPrivateUpstream:     instance.AllowPrivateUpstream,
	})
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.reloadTopology(); err != nil {
		s.log.Error("reload topology after instance create", "method", r.Method, "path", r.URL.Path, "request_id", requestID(r), "err", err)
		writeAdminError(w, http.StatusInternalServerError, "instance saved but routing reload failed: "+err.Error())
		return
	}
	s.audit(r, id.Username, "instance.create", instance.Name, "success")
	writeJSON(w, http.StatusCreated, s.instanceView(r, *rec))
}

func (s *Server) updateInstance(w http.ResponseWriter, r *http.Request, id *adminIdentity, instanceID int64) {
	var in instanceInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	records, err := s.store.ListInstanceRecords()
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var existing *store.InstanceRecord
	for i := range records {
		if records[i].ID == instanceID {
			existing = &records[i]
			continue
		}
		if records[i].Name == strings.TrimSpace(in.Name) {
			writeAdminError(w, http.StatusConflict, "instance name already exists")
			return
		}
	}
	if existing == nil {
		writeAdminError(w, http.StatusNotFound, "instance not found")
		return
	}
	if in.Preset != nil && strings.TrimSpace(*in.Preset) != existing.Preset {
		writeAdminError(w, http.StatusBadRequest,
			"preset cannot be changed; delete this instance and create a new one")
		return
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

	presetID := existing.Preset
	instance, err := in.toInstance(presetID)
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}
	// The quota credential is only replaced when the request carries one;
	// an omitted field keeps the stored credential.
	encryptedCredential := existing.EncryptedQuotaCredential
	if in.QuotaCredential != nil {
		encryptedCredential, err = s.encryptQuotaCredential(instance.QuotaCredential)
		if err != nil {
			writeAdminError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	instances, models, err := s.store.Topology(s.box)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	replaced := make([]config.Instance, 0, len(instances))
	for _, cur := range instances {
		if cur.Name == existing.Name {
			replaced = append(replaced, instance)
			continue
		}
		replaced = append(replaced, cur)
	}
	if err := s.validateTopology(replaced, models); err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := s.store.UpdateInstance(instanceID, store.InstanceSpec{
		Name:                     instance.Name,
		Preset:                   instance.Preset,
		EncryptedKey:             encrypted,
		EncryptedQuotaCredential: encryptedCredential,
		Endpoints:                instance.Endpoints,
		Models:                   instance.Models,
		MaxFails:                 instance.MaxFails,
		CooldownSecs:             instance.CooldownSecs,
		TimeoutSecs:              instance.TimeoutSecs,
		AllowPrivateUpstream:     instance.AllowPrivateUpstream,
	}); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		}
		writeAdminError(w, status, err.Error())
		return
	}
	if err := s.reloadTopology(); err != nil {
		s.log.Error("reload topology after instance update", "method", r.Method, "path", r.URL.Path, "request_id", requestID(r), "err", err)
		writeAdminError(w, http.StatusInternalServerError, "instance saved but routing reload failed: "+err.Error())
		return
	}
	s.audit(r, id.Username, "instance.update", instance.Name, "success")
	writeJSON(w, http.StatusOK, s.instanceView(r, store.InstanceRecord{
		ID: instanceID,
		InstanceSpec: store.InstanceSpec{
			Name:                     instance.Name,
			Preset:                   instance.Preset,
			EncryptedKey:             encrypted,
			EncryptedQuotaCredential: encryptedCredential,
			Endpoints:                instance.Endpoints,
			Models:                   instance.Models,
			MaxFails:                 instance.MaxFails,
			CooldownSecs:             instance.CooldownSecs,
			TimeoutSecs:              instance.TimeoutSecs,
			AllowPrivateUpstream:     instance.AllowPrivateUpstream,
		},
		CreatedAt: existing.CreatedAt,
		UpdatedAt: time.Now().UTC(),
	}))
}

func (s *Server) deleteInstance(w http.ResponseWriter, r *http.Request, id *adminIdentity, instanceID int64) {
	if err := s.store.DeleteInstance(instanceID); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		} else if strings.Contains(err.Error(), "still used by") {
			status = http.StatusConflict
		}
		writeAdminError(w, status, err.Error())
		return
	}
	if err := s.reloadTopology(); err != nil {
		s.log.Error("reload topology after instance delete", "method", r.Method, "path", r.URL.Path, "request_id", requestID(r), "err", err)
		writeAdminError(w, http.StatusInternalServerError, "instance deleted but routing reload failed: "+err.Error())
		return
	}
	s.audit(r, id.Username, "instance.delete", strconv.FormatInt(instanceID, 10), "success")
	w.WriteHeader(http.StatusNoContent)
}

// ---- models ----

func (s *Server) listModels(w http.ResponseWriter, r *http.Request) {
	records, err := s.store.ListModelRecords()
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.modelViews(records))
}

func (s *Server) modelViews(records []store.ModelRecord) []modelView {
	now := time.Now()
	views := make([]modelView, 0, len(records))
	for _, rec := range records {
		views = append(views, modelView{
			ID:         rec.ID,
			Name:       rec.Name,
			Upstream:   rec.Upstream,
			Strategy:   rec.Strategy,
			Instances:  modelInstanceViews(rec.Instances),
			Timezone:   s.scheduleTimezone(),
			ServerTime: now.UTC().Format(time.RFC3339),
			Effective:  s.effectiveCandidates(rec.Name, now),
			CreatedAt:  rec.CreatedAt.UTC().Format("2006-01-02T15:04:05Z"),
			UpdatedAt:  rec.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z"),
		})
	}
	return views
}

// effectiveCandidates describes the current routing state of one model.
func (s *Server) effectiveCandidates(model string, now time.Time) []effectiveCandidate {
	snap := s.router.Snapshot()
	if snap == nil {
		return []effectiveCandidate{}
	}
	previews := snap.PreviewModel(model, now)
	out := make([]effectiveCandidate, 0, len(previews))
	for _, p := range previews {
		out = append(out, effectiveCandidate{
			Instance:     p.Instance,
			Priority:     p.Priority,
			BasePriority: p.BasePriority,
			Active:       p.Active,
			Window:       p.Window,
		})
	}
	return out
}

// scheduleTimezone returns the timezone the time windows are evaluated in.
func (s *Server) scheduleTimezone() string {
	snap := s.router.Snapshot()
	if snap == nil || snap.Location == nil {
		return config.DefaultTimezone
	}
	return snap.Location.String()
}

func modelInstanceViews(instances []store.ModelInstanceSpec) []modelInstanceInput {
	out := make([]modelInstanceInput, 0, len(instances))
	for _, mi := range instances {
		out = append(out, modelInstanceInput{
			Instance: mi.Instance,
			Priority: mi.Priority,
			Weight:   mi.Weight,
			Windows:  timeWindowViews(mi.Windows),
		})
	}
	return out
}

func timeWindowViews(windows []config.TimeWindow) []timeWindowInput {
	out := make([]timeWindowInput, 0, len(windows))
	for _, w := range windows {
		out = append(out, timeWindowInput{
			Start:    w.Start,
			End:      w.End,
			Priority: w.Priority,
			Only:     w.Only,
		})
	}
	return out
}

func (s *Server) createModel(w http.ResponseWriter, r *http.Request, id *adminIdentity) {
	var in modelInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	model, err := in.toModel()
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}
	records, err := s.store.ListModelRecords()
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, rec := range records {
		if rec.Name == model.Name {
			writeAdminError(w, http.StatusConflict, "model name already exists")
			return
		}
	}
	instances, models, err := s.store.Topology(s.box)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	models = append(models, model)
	if err := s.validateTopology(instances, models); err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}

	rec, err := s.store.CreateModel(store.ModelSpec{
		Name:      model.Name,
		Upstream:  model.Upstream,
		Strategy:  model.Strategy,
		Instances: modelInstanceSpecs(model.Instances),
	})
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.reloadTopology(); err != nil {
		s.log.Error("reload topology after model create", "method", r.Method, "path", r.URL.Path, "request_id", requestID(r), "err", err)
		writeAdminError(w, http.StatusInternalServerError, "model saved but routing reload failed: "+err.Error())
		return
	}
	s.audit(r, id.Username, "model.create", model.Name, "success")
	writeJSON(w, http.StatusCreated, s.modelViews([]store.ModelRecord{*rec})[0])
}

func (s *Server) updateModel(w http.ResponseWriter, r *http.Request, id *adminIdentity, modelID int64) {
	var in modelInput
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	records, err := s.store.ListModelRecords()
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var existing *store.ModelRecord
	for i := range records {
		if records[i].ID == modelID {
			existing = &records[i]
			continue
		}
		if records[i].Name == strings.TrimSpace(in.Name) {
			writeAdminError(w, http.StatusConflict, "model name already exists")
			return
		}
	}
	if existing == nil {
		writeAdminError(w, http.StatusNotFound, "model not found")
		return
	}
	model, err := in.toModel()
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}
	instances, models, err := s.store.Topology(s.box)
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	replaced := make([]config.Model, 0, len(models))
	for _, cur := range models {
		if cur.Name == existing.Name {
			replaced = append(replaced, model)
			continue
		}
		replaced = append(replaced, cur)
	}
	if err := s.validateTopology(instances, replaced); err != nil {
		writeAdminError(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := s.store.UpdateModel(modelID, store.ModelSpec{
		Name:      model.Name,
		Upstream:  model.Upstream,
		Strategy:  model.Strategy,
		Instances: modelInstanceSpecs(model.Instances),
	}); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		}
		writeAdminError(w, status, err.Error())
		return
	}
	if err := s.reloadTopology(); err != nil {
		s.log.Error("reload topology after model update", "method", r.Method, "path", r.URL.Path, "request_id", requestID(r), "err", err)
		writeAdminError(w, http.StatusInternalServerError, "model saved but routing reload failed: "+err.Error())
		return
	}
	s.audit(r, id.Username, "model.update", model.Name, "success")
	writeJSON(w, http.StatusOK, s.modelViews([]store.ModelRecord{{
		ID: modelID,
		ModelSpec: store.ModelSpec{
			Name:      model.Name,
			Upstream:  model.Upstream,
			Strategy:  model.Strategy,
			Instances: modelInstanceSpecs(model.Instances),
		},
		CreatedAt: existing.CreatedAt,
		UpdatedAt: time.Now().UTC(),
	}})[0])
}

func (s *Server) deleteModel(w http.ResponseWriter, r *http.Request, id *adminIdentity, modelID int64) {
	if err := s.store.DeleteModel(modelID); err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, store.ErrNotFound) {
			status = http.StatusNotFound
		}
		writeAdminError(w, status, err.Error())
		return
	}
	if err := s.reloadTopology(); err != nil {
		s.log.Error("reload topology after model delete", "method", r.Method, "path", r.URL.Path, "request_id", requestID(r), "err", err)
		writeAdminError(w, http.StatusInternalServerError, "model deleted but routing reload failed: "+err.Error())
		return
	}
	s.audit(r, id.Username, "model.delete", strconv.FormatInt(modelID, 10), "success")
	w.WriteHeader(http.StatusNoContent)
}

// ---- shared helpers ----

// APIKeyString returns the submitted upstream key, if any.
func (in instanceInput) APIKeyString() string {
	if in.APIKey == nil {
		return ""
	}
	return *in.APIKey
}

// toInstance validates one instance input. An empty preset keeps the stored
// one.
func (in instanceInput) toInstance(presetID string) (config.Instance, error) {
	if in.Preset != nil {
		presetID = strings.TrimSpace(*in.Preset)
	}
	instance := config.Instance{
		Name:                 strings.TrimSpace(in.Name),
		Preset:               presetID,
		APIKey:               strings.TrimSpace(in.APIKeyString()),
		AllowPrivateUpstream: in.AllowPrivateUpstream,
	}
	if in.QuotaCredential != nil {
		instance.QuotaCredential = config.QuotaCredential{
			AccessKeyID:     strings.TrimSpace(in.QuotaCredential.AccessKeyID),
			SecretAccessKey: strings.TrimSpace(in.QuotaCredential.SecretAccessKey),
		}
	}
	for _, ep := range in.Endpoints {
		instance.Endpoints = append(instance.Endpoints, config.Endpoint{
			Protocol: strings.TrimSpace(ep.Protocol),
			BaseURL:  strings.TrimSpace(ep.BaseURL),
		})
	}
	for _, name := range in.Models {
		instance.Models = append(instance.Models, strings.TrimSpace(name))
	}
	if in.MaxFails != nil {
		instance.MaxFails = *in.MaxFails
	}
	if in.CooldownSecs != nil {
		instance.CooldownSecs = *in.CooldownSecs
	}
	if in.TimeoutSecs != nil {
		instance.TimeoutSecs = *in.TimeoutSecs
	}
	if err := config.NormalizeInstance(&instance); err != nil {
		return config.Instance{}, err
	}
	return instance, nil
}

// toModel validates one model input.
func (in modelInput) toModel() (config.Model, error) {
	model := config.Model{
		Name:     strings.TrimSpace(in.Name),
		Upstream: strings.TrimSpace(in.Upstream),
		Strategy: strings.TrimSpace(in.Strategy),
	}
	for _, mi := range in.Instances {
		model.Instances = append(model.Instances, config.ModelInstance{
			Name:     strings.TrimSpace(mi.Instance),
			Priority: mi.Priority,
			Weight:   mi.Weight,
			Windows:  toTimeWindows(mi.Windows),
		})
	}
	if err := config.NormalizeModel(&model); err != nil {
		return config.Model{}, err
	}
	return model, nil
}

// toTimeWindows converts the submitted windows into their configuration form.
func toTimeWindows(in []timeWindowInput) []config.TimeWindow {
	if len(in) == 0 {
		return nil
	}
	out := make([]config.TimeWindow, 0, len(in))
	for _, w := range in {
		out = append(out, config.TimeWindow{
			Start:    strings.TrimSpace(w.Start),
			End:      strings.TrimSpace(w.End),
			Priority: w.Priority,
			Only:     w.Only,
		})
	}
	return out
}

func modelInstanceSpecs(instances []config.ModelInstance) []store.ModelInstanceSpec {
	out := make([]store.ModelInstanceSpec, 0, len(instances))
	for _, mi := range instances {
		out = append(out, store.ModelInstanceSpec{
			Instance: mi.Name,
			Priority: mi.Priority,
			Weight:   mi.Weight,
			Windows:  mi.Windows,
		})
	}
	return out
}

// validateTopology checks that a prospective topology can be routed.
func (s *Server) validateTopology(instances []config.Instance, models []config.Model) error {
	snap := s.router.Snapshot()
	if snap == nil {
		return errors.New("router is not ready")
	}
	cfg := *snap.Config
	cfg.Instances = instances
	cfg.Models = models
	_, err := router.Build(&cfg)
	return err
}

// reloadTopology rebuilds the routing table from the database.
func (s *Server) reloadTopology() error {
	instances, models, err := s.store.Topology(s.box)
	if err != nil {
		return err
	}
	snap := s.router.Snapshot()
	if snap == nil {
		return errors.New("router is not ready")
	}
	cfg := *snap.Config
	cfg.Instances = instances
	cfg.Models = models
	return s.router.Reload(&cfg)
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

func (s *Server) encryptKey(key string) (string, error) {
	if strings.TrimSpace(key) == "" {
		return "", nil
	}
	return s.box.Encrypt(strings.TrimSpace(key))
}

// encryptQuotaCredential encrypts the extra credential of an instance.
func (s *Server) encryptQuotaCredential(cred config.QuotaCredential) (string, error) {
	if cred.Empty() {
		return "", nil
	}
	raw, err := json.Marshal(cred)
	if err != nil {
		return "", fmt.Errorf("encode quota credential: %w", err)
	}
	encrypted, err := s.box.Encrypt(string(raw))
	if err != nil {
		return "", fmt.Errorf("encrypt quota credential: %w", err)
	}
	return encrypted, nil
}

// quotaCredentialView masks the secret of the quota credential. The access key
// id stays visible: it identifies the credential without granting access.
func (s *Server) quotaCredentialView(encrypted string) (*quotaCredentialView, error) {
	raw, err := s.box.Decrypt(encrypted)
	if err != nil {
		return nil, fmt.Errorf("decrypt quota credential: %w", err)
	}
	var cred config.QuotaCredential
	if err := json.Unmarshal([]byte(raw), &cred); err != nil {
		return nil, fmt.Errorf("decode quota credential: %w", err)
	}
	return &quotaCredentialView{
		AccessKeyID: cred.AccessKeyID,
		SecretHint:  secretHint(cred.SecretAccessKey),
	}, nil
}

// secretHint masks a secret the way the upstream key hint does.
func secretHint(secret string) string {
	if len(secret) < 4 {
		return "****"
	}
	return "****" + secret[len(secret)-4:]
}

// providerCompatView is the legacy aggregated read model of /providers.
type providerCompatView struct {
	ID               int64    `json:"id"`
	Name             string   `json:"name"`
	Preset           string   `json:"preset"`
	PresetName       string   `json:"preset_name,omitempty"`
	Source           string   `json:"source"`
	DocsURL          string   `json:"docs_url,omitempty"`
	KeyHint          string   `json:"key_hint"`
	Models           []string `json:"models"`
	OpenAIBaseURL    string   `json:"openai_base_url"`
	AnthropicBaseURL string   `json:"anthropic_base_url"`
}

// adminProviders keeps the legacy read-only view of the upstream topology so
// existing scripts keep working. New clients use /instances and /models.
func (s *Server) adminProviders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAdminError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	records, err := s.store.ListInstanceRecords()
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	models, err := s.store.ListModelRecords()
	if err != nil {
		writeAdminError(w, http.StatusInternalServerError, err.Error())
		return
	}
	views := make([]providerCompatView, 0, len(records))
	for _, rec := range records {
		view := providerCompatView{
			ID:      rec.ID,
			Name:    rec.Name,
			Preset:  rec.Preset,
			Source:  "custom",
			KeyHint: s.keyHint(rec.EncryptedKey),
			Models:  []string{},
		}
		for _, ep := range rec.Endpoints {
			switch ep.Protocol {
			case config.ProtocolOpenAI:
				view.OpenAIBaseURL = ep.BaseURL
			case config.ProtocolAnthropic:
				view.AnthropicBaseURL = ep.BaseURL
			}
		}
		for _, m := range models {
			for _, mi := range m.Instances {
				if mi.Instance == rec.Name {
					view.Models = append(view.Models, m.Name)
					break
				}
			}
		}
		if rec.Preset != "" {
			view.Source = "builtin"
			if pr, ok := preset.Lookup(rec.Preset); ok {
				view.PresetName = pr.Label()
				view.DocsURL = pr.DocsURL
			}
		}
		views = append(views, view)
	}
	writeJSON(w, http.StatusOK, views)
}
