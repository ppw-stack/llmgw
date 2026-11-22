package server

import (
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
)

func itoa(id int64) string { return strconv.FormatInt(id, 10) }

// testAPIKey returns the upstream key used by the admin API tests. The
// environment variable injects a real key; the fallback is an obviously
// synthetic placeholder so no credential is stored in the repository.
func testAPIKey(env, suffix string) string {
	if v := strings.TrimSpace(os.Getenv(env)); v != "" {
		return v
	}
	return "sk-test-placeholder-" + suffix
}

// expectedKeyHint mirrors the hint the admin API returns for a stored key.
func expectedKeyHint(key string) string {
	if len(key) < 4 {
		return "****"
	}
	return "****" + key[len(key)-4:]
}

func instanceBody(name, apiKey string) string {
	return `{"name":"` + name + `","api_key":"` + apiKey + `",
	  "endpoints":[{"protocol":"openai","base_url":"http://127.0.0.1:9/v1"}]}`
}

func decodeInstance(t *testing.T, resp *http.Response) instanceView {
	t.Helper()
	defer resp.Body.Close()
	var view instanceView
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	return view
}

func decodeModel(t *testing.T, resp *http.Response) modelView {
	t.Helper()
	defer resp.Body.Close()
	var view modelView
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	return view
}

func TestInstanceCRUD(t *testing.T) {
	gw, _, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"k"})
	apiKey := testAPIKey("TEST_INSTANCE_API_KEY", "1234")

	// Create
	resp := adminRequest(t, http.MethodPost, gw.URL+"/admin/api/instances",
		instanceBody("ui-instance", apiKey))
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("create = %d %s", resp.StatusCode, body)
	}
	created := decodeInstance(t, resp)
	if created.KeyHint != expectedKeyHint(apiKey) {
		t.Fatalf("key hint = %q", created.KeyHint)
	}
	if created.Source != "custom" {
		t.Fatalf("source = %q", created.Source)
	}
	if len(created.Endpoints) != 1 || created.Endpoints[0].Protocol != "openai" {
		t.Fatalf("endpoints = %+v", created.Endpoints)
	}
	if created.MaxFails != 3 || created.CooldownSecs != 30 {
		t.Fatalf("breaker defaults = %+v", created)
	}
	if created.ID == 0 {
		t.Fatal("missing id")
	}

	// List
	listResp := adminRequest(t, http.MethodGet, gw.URL+"/admin/api/instances", "")
	defer listResp.Body.Close()
	var list []instanceView
	if err := json.NewDecoder(listResp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("list = %+v", list)
	}

	// Update without api_key preserves the stored key.
	update := `{"name":"ui-instance",
	  "endpoints":[{"protocol":"openai","base_url":"http://127.0.0.1:9/v2"}],
	  "max_fails":5,"cooldown_secs":15}`
	resp = adminRequest(t, http.MethodPut, gw.URL+"/admin/api/instances/"+itoa(created.ID), update)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("update = %d %s", resp.StatusCode, body)
	}
	updated := decodeInstance(t, resp)
	if updated.KeyHint != expectedKeyHint(apiKey) {
		t.Fatalf("keys were dropped on update: %q", updated.KeyHint)
	}
	if updated.MaxFails != 5 || updated.CooldownSecs != 15 {
		t.Fatalf("breaker params = %+v", updated)
	}
	if len(updated.Endpoints) != 1 || updated.Endpoints[0].BaseURL != "http://127.0.0.1:9/v2" {
		t.Fatalf("endpoints = %+v", updated.Endpoints)
	}

	// Duplicate name is rejected.
	resp = adminRequest(t, http.MethodPost, gw.URL+"/admin/api/instances",
		instanceBody("ui-instance", apiKey))
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate name = %d", resp.StatusCode)
	}

	// Delete
	resp = adminRequest(t, http.MethodDelete, gw.URL+"/admin/api/instances/"+itoa(created.ID), "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
}

func TestModelCRUD(t *testing.T) {
	gw, _, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"k"})
	apiKey := testAPIKey("TEST_MODEL_API_KEY", "4321")

	resp := adminRequest(t, http.MethodPost, gw.URL+"/admin/api/instances",
		instanceBody("backup", apiKey))
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("create instance = %d %s", resp.StatusCode, body)
	}
	instance := decodeInstance(t, resp)

	// A model may list several instances; the order is the priority.
	resp = adminRequest(t, http.MethodPost, gw.URL+"/admin/api/models",
		`{"name":"ui-model","upstream":"ui-upstream","strategy":"failover",
		  "instances":[{"instance":"test","priority":0},{"instance":"backup","priority":1}]}`)
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("create model = %d %s", resp.StatusCode, body)
	}
	created := decodeModel(t, resp)
	if created.Name != "ui-model" || created.Upstream != "ui-upstream" {
		t.Fatalf("model = %+v", created)
	}
	if len(created.Instances) != 2 {
		t.Fatalf("candidates = %+v", created.Instances)
	}
	if created.Instances[0].Instance != "test" || created.Instances[1].Instance != "backup" {
		t.Fatalf("candidates not ordered: %+v", created.Instances)
	}

	// The model is routed right away: the primary instance stays first.
	listResp := adminRequest(t, http.MethodGet, gw.URL+"/admin/api/models", "")
	defer listResp.Body.Close()
	var list []modelView
	if err := json.NewDecoder(listResp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Fatalf("models = %+v", list)
	}

	// A model that references an unknown instance is rejected.
	resp = adminRequest(t, http.MethodPost, gw.URL+"/admin/api/models",
		`{"name":"ghost-model","instances":[{"instance":"ghost"}]}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown instance = %d %s", resp.StatusCode, body)
	}

	// An instance that still serves a model cannot be deleted.
	resp = adminRequest(t, http.MethodDelete, gw.URL+"/admin/api/instances/"+itoa(instance.ID), "")
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("delete instance in use = %d %s", resp.StatusCode, body)
	}

	// Delete the model, then the instance.
	resp = adminRequest(t, http.MethodDelete, gw.URL+"/admin/api/models/"+itoa(created.ID), "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete model = %d", resp.StatusCode)
	}
	resp = adminRequest(t, http.MethodDelete, gw.URL+"/admin/api/instances/"+itoa(instance.ID), "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete instance = %d", resp.StatusCode)
	}
}

func TestDuplicateModelNameIsRejected(t *testing.T) {
	gw, _, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"k"})
	resp := adminRequest(t, http.MethodPost, gw.URL+"/admin/api/models",
		`{"name":"gpt-test","instances":[{"instance":"test"}]}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate model = %d %s", resp.StatusCode, body)
	}
}

func TestPresetInstanceCRUD(t *testing.T) {
	gw, _, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"k"})

	// The catalog is served to the console.
	resp := adminRequest(t, http.MethodGet, gw.URL+"/admin/api/provider-presets", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("presets = %d", resp.StatusCode)
	}
	var presets []struct {
		ID               string   `json:"id"`
		OpenAIBaseURL    string   `json:"openai_base_url"`
		AnthropicBaseURL string   `json:"anthropic_base_url"`
		Models           []string `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&presets); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(presets) == 0 || presets[0].ID != "minimax-mplan-global" {
		t.Fatalf("presets = %+v", presets)
	}
	if presets[0].OpenAIBaseURL != "https://api.minimax.io/v1" ||
		presets[0].AnthropicBaseURL != "https://api.minimax.io/anthropic" {
		t.Fatalf("preset urls = %+v", presets[0])
	}

	// Create with only a key: the endpoints come from the preset.
	presetKey := testAPIKey("TEST_PRESET_API_KEY", "9999")
	resp = adminRequest(t, http.MethodPost, gw.URL+"/admin/api/instances",
		`{"name":"minimax","preset":"minimax-mplan-global","api_key":"`+presetKey+`"}`)
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("create preset = %d %s", resp.StatusCode, body)
	}
	created := decodeInstance(t, resp)
	if created.Source != "builtin" || created.Preset != "minimax-mplan-global" {
		t.Fatalf("created = %+v", created)
	}
	if created.PresetName == "" || created.DocsURL == "" {
		t.Fatalf("missing preset metadata: %+v", created)
	}
	if len(created.Endpoints) != 2 {
		t.Fatalf("resolved endpoints = %+v", created.Endpoints)
	}
	if created.Endpoints[0].BaseURL != "https://api.minimax.io/v1" ||
		created.Endpoints[1].BaseURL != "https://api.minimax.io/anthropic" {
		t.Fatalf("resolved endpoints = %+v", created.Endpoints)
	}

	// The preset cannot be swapped.
	resp = adminRequest(t, http.MethodPut, gw.URL+"/admin/api/instances/"+itoa(created.ID),
		`{"name":"minimax","preset":""}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "preset cannot be changed") {
		t.Fatalf("preset switch = %d %s", resp.StatusCode, body)
	}

	// A preset instance may not carry custom endpoints.
	resp = adminRequest(t, http.MethodPost, gw.URL+"/admin/api/instances",
		`{"name":"bad","preset":"minimax-mplan-global","api_key":"k",
		  "endpoints":[{"protocol":"openai","base_url":"https://example.com/v1"}]}`)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "supplies its own endpoints") {
		t.Fatalf("preset endpoint = %d %s", resp.StatusCode, body)
	}
}

// TestProviderCompatView keeps the legacy read model working.
func TestProviderCompatView(t *testing.T) {
	gw, _, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"k"})
	resp := adminRequest(t, http.MethodGet, gw.URL+"/admin/api/providers", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("providers = %d", resp.StatusCode)
	}
	var list []providerCompatView
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if len(list) != 1 || list[0].Name != "test" {
		t.Fatalf("providers = %+v", list)
	}
	if len(list[0].Models) != 2 {
		t.Fatalf("models = %+v", list[0].Models)
	}
	if list[0].OpenAIBaseURL == "" {
		t.Fatalf("base url = %+v", list[0])
	}
}
