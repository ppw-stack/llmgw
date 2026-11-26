package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
)

func TestSavedInstanceConnectivityTest(t *testing.T) {
	t.Parallel()
	upstreamKey := testAPIKey("TEST_CONN_API_KEY", "1111")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			if r.Header.Get("Authorization") != "Bearer "+upstreamKey {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[{"id":"gpt-a"},{"id":"gpt-b"}]}`)
		case "/v1/models":
			if r.Header.Get("x-api-key") != upstreamKey {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(upstream.Close)

	gw, _, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"k"})

	createBody := `{"name":"probe-inst","api_key":"` + upstreamKey + `",
		"models":["gpt-a"],
		"allow_private_upstream":true,
		"endpoints":[
			{"protocol":"openai","base_url":"` + upstream.URL + `"},
			{"protocol":"anthropic","base_url":"` + upstream.URL + `"}
		]}`
	resp := adminRequest(t, http.MethodPost, gw.URL+"/admin/api/instances", createBody)
	if resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("create = %d %s", resp.StatusCode, body)
	}
	created := decodeInstance(t, resp)

	// Test saved instance: OpenAI returns 200 (2 models), Anthropic returns 404 (reachable).
	resp = adminRequest(t, http.MethodPost, gw.URL+"/admin/api/instances/"+itoa(created.ID)+"/test", "")
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("test saved = %d %s", resp.StatusCode, body)
	}
	var out instanceTestResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		resp.Body.Close()
		t.Fatal(err)
	}
	resp.Body.Close()

	if !out.OK || len(out.Endpoints) != 2 {
		t.Fatalf("unexpected test result: %+v", out)
	}
	if out.Endpoints[0].Status != "ok" || out.Endpoints[0].ModelCount != 2 {
		t.Fatalf("openai endpoint = %+v", out.Endpoints[0])
	}
	if out.Endpoints[1].Status != "reachable" || !out.Endpoints[1].OK {
		t.Fatalf("anthropic endpoint = %+v", out.Endpoints[1])
	}

	// Test draft reusing stored key via instance_id.
	draftBody := `{"instance_id":` + itoa(created.ID) + `,
		"allow_private_upstream":true,
		"endpoints":[{"protocol":"openai","base_url":"` + upstream.URL + `"}]}`
	resp = adminRequest(t, http.MethodPost, gw.URL+"/admin/api/instances/test", draftBody)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("test draft = %d %s", resp.StatusCode, body)
	}
	out = instanceTestResponse{}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		resp.Body.Close()
		t.Fatal(err)
	}
	resp.Body.Close()
	if !out.OK || len(out.Endpoints) != 1 || out.Endpoints[0].Status != "ok" {
		t.Fatalf("unexpected draft result: %+v", out)
	}

	// Test draft with wrong key -> auth_error.
	badDraft := `{"api_key":"wrong-key",
		"allow_private_upstream":true,
		"endpoints":[{"protocol":"openai","base_url":"` + upstream.URL + `"}]}`
	resp = adminRequest(t, http.MethodPost, gw.URL+"/admin/api/instances/test", badDraft)
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("test bad draft = %d %s", resp.StatusCode, body)
	}
	out = instanceTestResponse{}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		resp.Body.Close()
		t.Fatal(err)
	}
	resp.Body.Close()
	if out.OK || len(out.Endpoints) != 1 || out.Endpoints[0].Status != "auth_error" {
		t.Fatalf("expected auth_error, got %+v", out)
	}
}

func TestDraftConnectivityTestRejectsPrivateWithoutOptIn(t *testing.T) {
	t.Parallel()
	gw, _, _, srv := setupConfigServer(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"k"}, nil)
	srv.forwarder.SetBlockPrivate(true)

	body := `{"api_key":"k","allow_private_upstream":false,
		"endpoints":[{"protocol":"openai","base_url":"http://127.0.0.1:18080/v1"}]}`
	resp := adminRequest(t, http.MethodPost, gw.URL+"/admin/api/instances/test", body)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		raw, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 400 for private host without opt-in, got %d %s", resp.StatusCode, raw)
	}
}

func TestDraftConnectivityTestViaProxyAndRedactsPassword(t *testing.T) {
	t.Parallel()
	var proxyHits atomic.Int32
	proxySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxyHits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"data":[{"id":"m1"}]}`)
	}))
	t.Cleanup(proxySrv.Close)

	gw, _, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"k"})

	// 1. Working proxy routes the probe.
	body := `{"api_key":"k","proxy_url":"` + proxySrv.URL + `",
		"endpoints":[{"protocol":"openai","base_url":"http://example.invalid/v1"}]}`
	resp := adminRequest(t, http.MethodPost, gw.URL+"/admin/api/instances/test", body)
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("proxy test = %d %s", resp.StatusCode, raw)
	}
	var out instanceTestResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		resp.Body.Close()
		t.Fatal(err)
	}
	resp.Body.Close()
	if !out.OK || proxyHits.Load() != 1 {
		t.Fatalf("expected proxy hit and OK, got hits=%d out=%+v", proxyHits.Load(), out)
	}

	// 2. Broken proxy with password never leaks the password in the error message.
	secretPass := proxyTestPassword()
	badProxy := (&url.URL{
		Scheme: "http",
		User:   url.UserPassword("u", secretPass),
		Host:   "127.0.0.1:1",
	}).String()
	badBody := `{"api_key":"k","proxy_url":"` + badProxy + `",
		"endpoints":[{"protocol":"openai","base_url":"http://example.invalid/v1"}]}`
	resp = adminRequest(t, http.MethodPost, gw.URL+"/admin/api/instances/test", badBody)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if strings.Contains(string(raw), secretPass) {
		t.Fatalf("connectivity test leaked proxy password: %s", raw)
	}
}

func TestDataPlaneResponsesAndCountTokensEndpoints(t *testing.T) {
	t.Parallel()
	var cap capture
	gw, _, gwKey := setup(t, &cap, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/responses":
			_, _ = io.WriteString(w, `{"id":"resp_1","object":"response","output":[],"usage":{"input_tokens":10,"output_tokens":5}}`)
		case "/v1/messages/count_tokens":
			_, _ = io.WriteString(w, `{"input_tokens":17}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}, []string{"up-key"})

	// 1. OpenAI /openai/responses (normalized to /openai/v1/responses)
	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/openai/responses",
		strings.NewReader(`{"model":"gpt-test","input":"hello"}`))
	req.Header.Set("Authorization", "Bearer "+gwKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"resp_1"`) {
		t.Fatalf("responses = %d %s", resp.StatusCode, body)
	}

	// 2. Anthropic /anthropic/messages/count_tokens (normalized)
	req, _ = http.NewRequest(http.MethodPost, gw.URL+"/anthropic/messages/count_tokens",
		strings.NewReader(`{"model":"claude-test","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("x-api-key", gwKey)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"input_tokens":17`) {
		t.Fatalf("count_tokens = %d %s", resp.StatusCode, body)
	}

	// 3. Anthropic /anthropic/models (normalized to /anthropic/v1/models)
	req, _ = http.NewRequest(http.MethodGet, gw.URL+"/anthropic/models", nil)
	req.Header.Set("x-api-key", gwKey)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"claude-test"`) {
		t.Fatalf("anthropic models = %d %s", resp.StatusCode, body)
	}

	reqs := cap.all()
	if len(reqs) != 2 || reqs[0].Path != "/responses" || reqs[1].Path != "/v1/messages/count_tokens" {
		t.Fatalf("unexpected upstream requests: %+v", reqs)
	}
}
