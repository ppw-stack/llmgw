package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

func itoa(id int64) string { return strconv.FormatInt(id, 10) }

func providerBody(name, model string) string {
	return `{"name":"` + name + `","api_key":"sk-secret-1234",
	  "models":["` + model + `"],
	  "openai_base_url":"http://127.0.0.1:9/v1"}`
}

func decodeProvider(t *testing.T, resp *http.Response) providerView {
	t.Helper()
	defer resp.Body.Close()
	var view providerView
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	return view
}

func TestProviderCRUD(t *testing.T) {
	gw, _, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"k"})

	// Create
	resp := adminRequest(t, http.MethodPost, gw.URL+"/admin/api/providers", providerBody("ui-provider", "m1"))
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("create = %d %s", resp.StatusCode, body)
	}
	created := decodeProvider(t, resp)
	if created.KeyHint != "****1234" {
		t.Fatalf("key hint = %q", created.KeyHint)
	}
	if created.Source != "custom" {
		t.Fatalf("source = %q", created.Source)
	}
	if len(created.Models) != 1 || created.Models[0] != "m1" {
		t.Fatalf("models = %v", created.Models)
	}
	if created.ID == 0 {
		t.Fatal("missing id")
	}

	// List
	listResp := adminRequest(t, http.MethodGet, gw.URL+"/admin/api/providers", "")
	defer listResp.Body.Close()
	var list []providerView
	if err := json.NewDecoder(listResp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Name != "ui-provider" {
		t.Fatalf("list = %+v", list)
	}

	// Update without api_key preserves the stored key.
	update := `{"name":"ui-provider","models":["m2"],
	  "openai_base_url":"http://127.0.0.1:9/v1"}`
	resp = adminRequest(t, http.MethodPut, gw.URL+"/admin/api/providers/"+itoa(created.ID), update)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("update = %d %s", resp.StatusCode, body)
	}
	updated := decodeProvider(t, resp)
	if updated.KeyHint != "****1234" {
		t.Fatalf("keys were dropped on update: %q", updated.KeyHint)
	}
	if len(updated.Models) != 1 || updated.Models[0] != "m2" {
		t.Fatalf("model update lost: %+v", updated.Models)
	}

	// Duplicate name is rejected.
	resp = adminRequest(t, http.MethodPost, gw.URL+"/admin/api/providers", providerBody("ui-provider", "m3"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate name = %d", resp.StatusCode)
	}

	// Duplicate model on the same protocol is rejected.
	resp = adminRequest(t, http.MethodPost, gw.URL+"/admin/api/providers", providerBody("other", "m2"))
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("duplicate model = %d %s", resp.StatusCode, body)
	}

	// Delete
	resp = adminRequest(t, http.MethodDelete, gw.URL+"/admin/api/providers/"+itoa(created.ID), "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
	resp = adminRequest(t, http.MethodGet, gw.URL+"/admin/api/providers", "")
	defer resp.Body.Close()
	list = nil
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 {
		t.Fatalf("provider not deleted: %+v", list)
	}
}

func TestPresetProviderCRUD(t *testing.T) {
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

	// Create with only a key: URLs and models come from the preset.
	resp = adminRequest(t, http.MethodPost, gw.URL+"/admin/api/providers",
		`{"name":"minimax","preset":"minimax-mplan-global","api_key":"sk-cp-9999"}`)
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("create preset = %d %s", resp.StatusCode, body)
	}
	created := decodeProvider(t, resp)
	if created.Source != "builtin" || created.Preset != "minimax-mplan-global" {
		t.Fatalf("created = %+v", created)
	}
	if created.PresetName == "" || created.DocsURL == "" {
		t.Fatalf("missing preset metadata: %+v", created)
	}
	if created.OpenAIBaseURL != "https://api.minimax.io/v1" ||
		created.AnthropicBaseURL != "https://api.minimax.io/anthropic" {
		t.Fatalf("resolved urls = %+v", created)
	}
	if len(created.Models) != 1 || created.Models[0] != "MiniMax-M3.1-Flash" {
		t.Fatalf("default models = %v", created.Models)
	}

	// Models can be overridden, the preset cannot be swapped.
	resp = adminRequest(t, http.MethodPut, gw.URL+"/admin/api/providers/"+itoa(created.ID),
		`{"name":"minimax","preset":"minimax-mplan-global","models":["MiniMax-M3.1-Flash[1m]"]}`)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("update models = %d %s", resp.StatusCode, body)
	}
	updated := decodeProvider(t, resp)
	if len(updated.Models) != 1 || updated.Models[0] != "MiniMax-M3.1-Flash[1m]" {
		t.Fatalf("models = %v", updated.Models)
	}
	if updated.KeyHint != "****9999" {
		t.Fatalf("key hint = %q", updated.KeyHint)
	}

	// Omitting models keeps the stored override while replacing the key.
	resp = adminRequest(t, http.MethodPut, gw.URL+"/admin/api/providers/"+itoa(created.ID),
		`{"name":"minimax","preset":"minimax-mplan-global","api_key":"sk-cp-1234"}`)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("key-only update = %d %s", resp.StatusCode, body)
	}
	keyOnly := decodeProvider(t, resp)
	if len(keyOnly.Models) != 1 || keyOnly.Models[0] != "MiniMax-M3.1-Flash[1m]" {
		t.Fatalf("models reset on key-only update: %v", keyOnly.Models)
	}
	if keyOnly.KeyHint != "****1234" {
		t.Fatalf("key hint = %q", keyOnly.KeyHint)
	}

	resp = adminRequest(t, http.MethodPut, gw.URL+"/admin/api/providers/"+itoa(created.ID),
		`{"name":"minimax","preset":"","models":["MiniMax-M3.1-Flash"],"openai_base_url":"https://example.com/v1"}`)
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "preset cannot be changed") {
		t.Fatalf("preset switch = %d %s", resp.StatusCode, body)
	}

	// A preset provider may not carry custom base URLs.
	resp = adminRequest(t, http.MethodPost, gw.URL+"/admin/api/providers",
		`{"name":"bad","preset":"minimax-mplan-global","openai_base_url":"https://example.com/v1"}`)
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest || !strings.Contains(string(body), "supplies its own base URLs") {
		t.Fatalf("preset url = %d %s", resp.StatusCode, body)
	}
}
