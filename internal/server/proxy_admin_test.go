package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/pw/llmgw/internal/config"
)

func TestInstanceProxyCRUDAndRedaction(t *testing.T) {
	t.Parallel()
	gw, _, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"k"})
	apiKey := testAPIKey("TEST_INSTANCE_PROXY_API_KEY", "9999")

	// 1. Create with authenticated proxy URL.
	createBody := `{"name":"proxied-inst","api_key":"` + apiKey + `",
		"models":["gpt-upstream"],
		"endpoints":[{"protocol":"openai","base_url":"https://api.example.com/v1"}],
		"proxy_url":"socks5://alice:demo-secret@127.0.0.1:1080"}`
	resp := adminRequest(t, http.MethodPost, gw.URL+"/admin/api/instances", createBody)
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("create = %d %s", resp.StatusCode, body)
	}
	rawBody, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rawBody), "demo-secret") {
		t.Fatalf("create response leaked proxy password: %s", rawBody)
	}
	var created instanceView
	if err := json.Unmarshal(rawBody, &created); err != nil {
		t.Fatal(err)
	}
	if created.ProxyURL != "socks5://alice:****@127.0.0.1:1080" || !created.ProxyHasAuth {
		t.Fatalf("created proxy view = (%q, %v)", created.ProxyURL, created.ProxyHasAuth)
	}

	// 2. Update omitting proxy_url preserves the stored proxy.
	keepBody := `{"name":"proxied-inst",
		"models":["gpt-upstream"],
		"endpoints":[{"protocol":"openai","base_url":"https://api.example.com/v1"}],
		"max_fails":5}`
	resp = adminRequest(t, http.MethodPut, gw.URL+"/admin/api/instances/"+itoa(created.ID), keepBody)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("update preserve = %d %s", resp.StatusCode, body)
	}
	preserved := decodeInstance(t, resp)
	if preserved.ProxyURL != "socks5://alice:****@127.0.0.1:1080" || !preserved.ProxyHasAuth {
		t.Fatalf("omitted proxy_url should be preserved, got (%q, %v)", preserved.ProxyURL, preserved.ProxyHasAuth)
	}

	// 3. Update with unauthenticated proxy replaces it and clears proxy_has_auth.
	replaceBody := `{"name":"proxied-inst",
		"models":["gpt-upstream"],
		"endpoints":[{"protocol":"openai","base_url":"https://api.example.com/v1"}],
		"proxy_url":"http://127.0.0.1:7890"}`
	resp = adminRequest(t, http.MethodPut, gw.URL+"/admin/api/instances/"+itoa(created.ID), replaceBody)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("update replace = %d %s", resp.StatusCode, body)
	}
	replaced := decodeInstance(t, resp)
	if replaced.ProxyURL != "http://127.0.0.1:7890" || replaced.ProxyHasAuth {
		t.Fatalf("replaced proxy view = (%q, %v)", replaced.ProxyURL, replaced.ProxyHasAuth)
	}

	// 4. Update with empty proxy_url clears it.
	clearBody := `{"name":"proxied-inst",
		"models":["gpt-upstream"],
		"endpoints":[{"protocol":"openai","base_url":"https://api.example.com/v1"}],
		"proxy_url":""}`
	resp = adminRequest(t, http.MethodPut, gw.URL+"/admin/api/instances/"+itoa(created.ID), clearBody)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("update clear = %d %s", resp.StatusCode, body)
	}
	cleared := decodeInstance(t, resp)
	if cleared.ProxyURL != "" || cleared.ProxyHasAuth {
		t.Fatalf("cleared proxy view = (%q, %v)", cleared.ProxyURL, cleared.ProxyHasAuth)
	}

	// 5. Blocked metadata proxy is rejected with 400.
	badBody := `{"name":"bad-proxy","api_key":"` + apiKey + `",
		"models":["gpt-upstream"],
		"endpoints":[{"protocol":"openai","base_url":"https://api.example.com/v1"}],
		"proxy_url":"http://169.254.169.254:80"}`
	resp = adminRequest(t, http.MethodPost, gw.URL+"/admin/api/instances", badBody)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("metadata proxy status = %d, want 400", resp.StatusCode)
	}
}

func TestPresetModelsUsesSubmittedProxy(t *testing.T) {
	// Not parallel because it swaps package-level presetModelsEndpoint.
	var proxyHits atomic.Int64
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"proxy-discovered-model"}]}`)
	}))
	t.Cleanup(proxySrv.Close)

	origEndpoint := presetModelsEndpoint
	presetModelsEndpoint = func(string) (string, string, error) {
		return "http://preset.example.test/v1/models", config.ProtocolOpenAI, nil
	}
	t.Cleanup(func() { presetModelsEndpoint = origEndpoint })

	gw, _, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"k"})
	body := `{"preset":"minimax-mplan-cn","api_key":"sk-test-placeholder","proxy_url":"` + proxySrv.URL + `"}`
	resp := adminRequest(t, http.MethodPost, gw.URL+"/admin/api/preset-models", body)
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("preset-models = %d %s", resp.StatusCode, raw)
	}
	defer resp.Body.Close()
	var view presetModelsView
	if err := json.NewDecoder(resp.Body).Decode(&view); err != nil {
		t.Fatal(err)
	}
	if proxyHits.Load() != 1 {
		t.Fatalf("proxyHits = %d, want 1", proxyHits.Load())
	}
	if len(view.Models) != 1 || view.Models[0].ID != "proxy-discovered-model" {
		t.Fatalf("models = %+v", view.Models)
	}

	// Invalid proxy_url returns 400.
	bad := `{"preset":"minimax-mplan-cn","api_key":"sk-test-placeholder","proxy_url":"ftp://127.0.0.1:21"}`
	resp2 := adminRequest(t, http.MethodPost, gw.URL+"/admin/api/preset-models", bad)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid proxy status = %d, want 400", resp2.StatusCode)
	}
}
