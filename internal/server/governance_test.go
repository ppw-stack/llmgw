package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestPerKeyPolicyModelAllowlistExpiryAndRPM(t *testing.T) {
	t.Parallel()
	gw, _, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"c1","choices":[],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
	}, []string{"up-key"})

	// 1. Create a key restricted to gpt-test with rpm_limit=1.
	createBody := `{"name":"bot","rpm_limit":1,"allowed_models":["gpt-test"]}`
	resp := adminRequest(t, http.MethodPost, gw.URL+"/admin/api/keys", createBody)
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("create key = %d %s", resp.StatusCode, raw)
	}
	var created struct {
		ID            int64    `json:"id"`
		Key           string   `json:"key"`
		RPMLimit      int      `json:"rpm_limit"`
		AllowedModels []string `json:"allowed_models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		resp.Body.Close()
		t.Fatal(err)
	}
	resp.Body.Close()
	if created.RPMLimit != 1 || len(created.AllowedModels) != 1 || created.AllowedModels[0] != "gpt-test" {
		t.Fatalf("unexpected created key: %+v", created)
	}

	// 2. /openai/v1/models only lists gpt-test (claude-test is filtered out).
	req, _ := http.NewRequest(http.MethodGet, gw.URL+"/openai/v1/models", nil)
	req.Header.Set("Authorization", "Bearer "+created.Key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"gpt-test"`) || strings.Contains(string(body), `"claude-test"`) {
		t.Fatalf("models list not filtered: %d %s", resp.StatusCode, body)
	}

	// 3. Second request within the minute hits per-key rpm_limit=1 -> 429.
	req, _ = http.NewRequest(http.MethodPost, gw.URL+"/openai/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+created.Key)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 from per-key rpm_limit=1, got %d", resp.StatusCode)
	}

	// 4. Raise rpm_limit via PATCH and test forbidden model -> 403.
	resp = adminRequest(t, http.MethodPatch, gw.URL+"/admin/api/keys/"+itoa(created.ID), `{"rpm_limit":100}`)
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("patch key = %d %s", resp.StatusCode, raw)
	}
	resp.Body.Close()

	req, _ = http.NewRequest(http.MethodPost, gw.URL+"/openai/v1/chat/completions",
		strings.NewReader(`{"model":"claude-test","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+created.Key)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("expected 403 for disallowed model, got %d", resp.StatusCode)
	}

	// 5. Set expires_at in the past -> 401.
	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	resp = adminRequest(t, http.MethodPatch, gw.URL+"/admin/api/keys/"+itoa(created.ID), `{"expires_at":"`+past+`"}`)
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("patch expires = %d %s", resp.StatusCode, raw)
	}
	resp.Body.Close()

	req, _ = http.NewRequest(http.MethodPost, gw.URL+"/openai/v1/chat/completions",
		strings.NewReader(`{"model":"gpt-test","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+created.Key)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401 for expired key, got %d", resp.StatusCode)
	}
}

func TestInstanceBreakerVisibilityAndReset(t *testing.T) {
	t.Parallel()
	gw, _, gwKey := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}, []string{"up-key"})

	// Create instance in DB so admin API lists it.
	resp := adminRequest(t, http.MethodPost, gw.URL+"/admin/api/instances", `{
		"name":"flaky","api_key":"k","models":["m-up"],
		"allow_private_upstream":true,"max_fails":1,"cooldown_secs":60,
		"endpoints":[{"protocol":"openai","base_url":"http://127.0.0.1:1"}]
	}`)
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("create instance = %d %s", resp.StatusCode, raw)
	}
	created := decodeInstance(t, resp)

	resp = adminRequest(t, http.MethodPost, gw.URL+"/admin/api/models", `{
		"name":"flaky-m","strategy":"failover",
		"instances":[{"instance":"flaky","upstream":"m-up","priority":0}]
	}`)
	if resp.StatusCode != http.StatusCreated {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("create model = %d %s", resp.StatusCode, raw)
	}
	resp.Body.Close()

	// Trigger failure to open breaker (max_fails=1).
	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/openai/v1/chat/completions",
		strings.NewReader(`{"model":"flaky-m","messages":[{"role":"user","content":"hi"}]}`))
	req.Header.Set("Authorization", "Bearer "+gwKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	// Instance list reports breaker.open == true.
	resp = adminRequest(t, http.MethodGet, gw.URL+"/admin/api/instances", "")
	var list []instanceView
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		resp.Body.Close()
		t.Fatal(err)
	}
	resp.Body.Close()
	var found *instanceView
	for i := range list {
		if list[i].ID == created.ID {
			found = &list[i]
		}
	}
	if found == nil || found.Breaker == nil || !found.Breaker.Open {
		t.Fatalf("expected breaker open in instance list, got %+v", found)
	}

	// Reset breaker -> open == false.
	resp = adminRequest(t, http.MethodPost, gw.URL+"/admin/api/instances/"+itoa(created.ID)+"/reset-breaker", "")
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("reset breaker = %d %s", resp.StatusCode, raw)
	}
	resetView := decodeInstance(t, resp)
	if resetView.Breaker == nil || resetView.Breaker.Open {
		t.Fatalf("expected breaker closed after reset, got %+v", resetView.Breaker)
	}
}
