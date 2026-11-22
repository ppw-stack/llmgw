package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

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

	// The in-memory cache must serve the new key without a restart.
	call := post(t, gw.URL+"/openai/v1/chat/completions", created.Key,
		`{"model":"gpt-test","messages":[]}`)
	if body := readAll(t, call); call.StatusCode != http.StatusOK {
		t.Fatalf("new key status %d: %s", call.StatusCode, body)
	}
}

func TestMetricsToken(t *testing.T) {
	gw, _, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"up-1"},
		func(o *Options) { o.MetricsToken = "metrics-secret" })

	resp, err := http.Get(gw.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated metrics = %d", resp.StatusCode)
	}

	req, _ := http.NewRequest(http.MethodGet, gw.URL+"/metrics", nil)
	req.Header.Set("Authorization", "Bearer metrics-secret")
	authed, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer authed.Body.Close()
	if authed.StatusCode != http.StatusOK {
		t.Fatalf("authenticated metrics = %d", authed.StatusCode)
	}
}
