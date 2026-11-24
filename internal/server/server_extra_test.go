package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

// testMetricsToken returns the token that guards the metrics endpoint. Set
// LLMGW_TEST_METRICS_TOKEN to inject a real one; without it a random token is
// generated at runtime, so the repository holds no credential literal.
func testMetricsToken(t *testing.T) string {
	t.Helper()
	if token := strings.TrimSpace(os.Getenv("LLMGW_TEST_METRICS_TOKEN")); token != "" {
		return token
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		t.Fatalf("generate a metrics token: %v", err)
	}
	return hex.EncodeToString(buf)
}

func adminRequest(t *testing.T, method, url, body string) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer admin-test")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestCreatedKeyWorksImmediately(t *testing.T) {
	gw, _, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}, []string{"up-1"})

	resp := adminRequest(t, http.MethodPost, gw.URL+"/admin/api/keys", `{"name":"new"}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create status = %d", resp.StatusCode)
	}
	var created struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.Key == "" {
		t.Fatal("no key returned")
	}

	// The console echoes the key back in the list instead of showing it once.
	listResp := adminRequest(t, http.MethodGet, gw.URL+"/admin/api/keys", "")
	defer listResp.Body.Close()
	if listResp.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d", listResp.StatusCode)
	}
	var listed []struct {
		Name string `json:"name"`
		Key  string `json:"key"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&listed); err != nil {
		t.Fatal(err)
	}
	var echoed string
	for _, k := range listed {
		if k.Name == "new" {
			echoed = k.Key
		}
	}
	if echoed != created.Key {
		t.Fatalf("listed keys = %+v, want new key echoed", listed)
	}

	// The in-memory cache must serve the new key without a restart.
	call := post(t, gw.URL+"/openai/v1/chat/completions", created.Key,
		`{"model":"gpt-test","messages":[]}`)
	if body := readAll(t, call); call.StatusCode != http.StatusOK {
		t.Fatalf("new key status %d: %s", call.StatusCode, body)
	}
}

func TestMetricsToken(t *testing.T) {
	token := testMetricsToken(t)
	gw, _, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"up-1"},
		func(o *Options) { o.MetricsToken = token })

	resp, err := http.Get(gw.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated metrics = %d", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, gw.URL+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	authed, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer authed.Body.Close()
	if authed.StatusCode != http.StatusOK {
		t.Fatalf("authenticated metrics = %d", authed.StatusCode)
	}
}
