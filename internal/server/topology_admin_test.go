package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/pw/llmgw/internal/config"
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
	  "allow_private_upstream":true,
	  "models":["gpt-upstream"],
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
	  "allow_private_upstream":true,
	  "models":["gpt-upstream"],
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
		`{"name":"ui-model","upstream":"gpt-upstream","strategy":"failover",
		  "instances":[{"instance":"test","priority":0},{"instance":"backup","priority":1}]}`)
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("create model = %d %s", resp.StatusCode, body)
	}
	created := decodeModel(t, resp)
	if created.Name != "ui-model" || created.Upstream != "gpt-upstream" {
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

	// The other providers are part of the catalog as well.
	byID := map[string]int{}
	for i, p := range presets {
		byID[p.ID] = i
	}
	for _, id := range []string{"opencode-go", "ark-coding-plan-cn"} {
		i, ok := byID[id]
		if !ok {
			t.Fatalf("preset %q missing from the catalog: %+v", id, presets)
		}
		if presets[i].OpenAIBaseURL == "" || presets[i].AnthropicBaseURL == "" ||
			len(presets[i].Models) == 0 {
			t.Fatalf("preset %q is incomplete: %+v", id, presets[i])
		}
	}

	// Create with only a key: the endpoints come from the preset.
	presetKey := testAPIKey("TEST_PRESET_API_KEY", "9999")
	resp = adminRequest(t, http.MethodPost, gw.URL+"/admin/api/instances",
		`{"name":"minimax","preset":"minimax-mplan-global","api_key":"`+presetKey+`",
		  "models":["MiniMax-M3.1-Flash"]}`)
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
		  "models":["MiniMax-M3.1-Flash"],
		  "endpoints":[{"protocol":"openai","base_url":"https://example.com/v1"}]}`)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "supplies its own endpoints") {
		t.Fatalf("preset endpoint = %d %s", resp.StatusCode, body)
	}
}

// TestPresetWithoutVariantNamesThePreset checks that a preset without a
// variant is shown by its family name alone.
func TestPresetWithoutVariantNamesThePreset(t *testing.T) {
	t.Parallel()
	gw, _, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"k"})

	apiKey := testAPIKey("TEST_OPENCODE_API_KEY", "8888")
	resp := adminRequest(t, http.MethodPost, gw.URL+"/admin/api/instances",
		`{"name":"opencode-go","preset":"opencode-go","api_key":"`+apiKey+`",
		  "models":["glm-5.3"]}`)
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("create preset = %d %s", resp.StatusCode, body)
	}
	created := decodeInstance(t, resp)
	if created.PresetName != "OpenCode Go" {
		t.Fatalf("preset name = %q", created.PresetName)
	}
	if len(created.Endpoints) != 2 ||
		created.Endpoints[0].BaseURL != "https://opencode.ai/inference/go/openai/v1" ||
		created.Endpoints[1].BaseURL != "https://opencode.ai/inference/go/anthropic" {
		t.Fatalf("resolved endpoints = %+v", created.Endpoints)
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

func TestInstanceRequiresModels(t *testing.T) {
	gw, _, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"k"})
	resp := adminRequest(t, http.MethodPost, gw.URL+"/admin/api/instances",
		`{"name":"no-models","api_key":"k",
		  "allow_private_upstream":true,
		  "endpoints":[{"protocol":"openai","base_url":"http://127.0.0.1:9/v1"}]}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "supported model") {
		t.Fatalf("missing models = %d %s", resp.StatusCode, body)
	}
}

// usePresetModelsServer points the discovery handler at a local stub.
func usePresetModelsServer(t *testing.T, status int, payload string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, payload)
	}))
	oldClient, oldEndpoint := presetModelsClient, presetModelsEndpoint
	presetModelsClient = srv.Client()
	presetModelsEndpoint = func(string) (string, string, error) {
		return srv.URL + "/models", config.ProtocolOpenAI, nil
	}
	t.Cleanup(func() {
		presetModelsClient = oldClient
		presetModelsEndpoint = oldEndpoint
		srv.Close()
	})
}

func TestPresetModelsValidation(t *testing.T) {
	gw, _, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"k"})

	req, err := http.NewRequest(http.MethodPost, gw.URL+"/admin/api/preset-models",
		strings.NewReader(`{"preset":"opencode-go","api_key":"k"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated = %d", resp.StatusCode)
	}

	resp = adminRequest(t, http.MethodPost, gw.URL+"/admin/api/preset-models",
		`{"preset":"nope","api_key":"k"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("unknown preset = %d", resp.StatusCode)
	}

	resp = adminRequest(t, http.MethodPost, gw.URL+"/admin/api/preset-models",
		`{"preset":"opencode-go"}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing key = %d", resp.StatusCode)
	}

	resp = adminRequest(t, http.MethodGet, gw.URL+"/admin/api/preset-models", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET = %d", resp.StatusCode)
	}
}

func TestPresetModelsQuery(t *testing.T) {
	gw, _, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"k"})
	usePresetModelsServer(t, http.StatusOK, `{"data":[
		{"id":"glm-5.3","api":{"chat-completions":{}}},
		{"id":"gpt-6-luna","api":{"responses":{}}}
	]}`)

	resp := adminRequest(t, http.MethodPost, gw.URL+"/admin/api/preset-models",
		`{"preset":"opencode-go","api_key":"sk-test"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var view presetModelsView
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	if len(view.Models) != 1 || view.Models[0].ID != "glm-5.3" {
		t.Fatalf("models = %+v", view.Models)
	}
	if len(view.Models[0].Protocols) != 1 || view.Models[0].Protocols[0] != config.ProtocolOpenAI {
		t.Fatalf("protocols = %+v", view.Models[0].Protocols)
	}
	if len(view.Suggested) == 0 {
		t.Fatal("missing suggested models")
	}
	if view.Warning != "" {
		t.Fatalf("warning = %q", view.Warning)
	}
}

func TestPresetModelsWarnsAboutPlanScope(t *testing.T) {
	gw, _, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"k"})
	usePresetModelsServer(t, http.StatusOK,
		`{"data":[{"id":"doubao-seed-2-1-pro-260915","api_supported":{"chat/completions":true}}]}`)

	resp := adminRequest(t, http.MethodPost, gw.URL+"/admin/api/preset-models",
		`{"preset":"ark-coding-plan-cn","api_key":"sk-test"}`)
	defer resp.Body.Close()
	var view presetModelsView
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(view.Warning, "Coding Plan") {
		t.Fatalf("warning = %q", view.Warning)
	}
}

func TestPresetModelsFailureFallsBack(t *testing.T) {
	gw, _, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"k"})
	usePresetModelsServer(t, http.StatusUnauthorized, `{"error":"invalid key"}`)

	resp := adminRequest(t, http.MethodPost, gw.URL+"/admin/api/preset-models",
		`{"preset":"opencode-go","api_key":"sk-test"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	var view presetModelsView
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	if len(view.Models) != 0 {
		t.Fatalf("models = %+v", view.Models)
	}
	if len(view.Suggested) == 0 {
		t.Fatal("missing fallback suggestions")
	}
	if !strings.Contains(view.Warning, "查询失败") {
		t.Fatalf("warning = %q", view.Warning)
	}
}

func TestModelRejectsAnUndeclaredUpstream(t *testing.T) {
	gw, _, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"k"})
	resp := adminRequest(t, http.MethodPost, gw.URL+"/admin/api/models",
		`{"name":"bad-model","upstream":"not-declared","instances":[{"instance":"test"}]}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest ||
		!strings.Contains(string(body), "does not declare upstream model") {
		t.Fatalf("undeclared upstream = %d %s", resp.StatusCode, body)
	}
}
