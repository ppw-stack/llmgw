package server

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pw/llmgw/internal/config"
	"github.com/pw/llmgw/internal/quota"
	"github.com/pw/llmgw/internal/store"
)

// TestAdminQuota checks that the quota endpoint reports the windows of a
// built-in instance and leaves custom instances alone.
func TestAdminQuota(t *testing.T) {
	vendor := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"usage":{
			"rolling":{"status":"ok","percent":12,"resetsAt":"2026-11-23T04:23:18.000Z"},
			"weekly":{"status":"ok","percent":78,"resetsAt":"2026-11-30T00:00:00.000Z"},
			"monthly":{"status":"ok","percent":94,"resetsAt":"2026-12-20T14:43:31.000Z"}}}`)
	}))
	t.Cleanup(vendor.Close)

	client := &quota.Client{
		HTTP:       vendor.Client(),
		ResolveURL: func(string) string { return vendor.URL },
	}
	gw, _, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"k"},
		func(o *Options) { o.Quota = quota.NewServiceWithClient(client, 0) })

	create := adminRequest(t, http.MethodPost, gw.URL+"/admin/api/instances",
		`{"name":"go","api_key":"sk-test","preset":"opencode-go","models":["glm-5.3"]}`)
	if create.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(create.Body)
		create.Body.Close()
		t.Fatalf("create = %d %s", create.StatusCode, body)
	}
	create.Body.Close()

	resp := adminRequest(t, http.MethodGet, gw.URL+"/admin/api/quota", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("quota = %d", resp.StatusCode)
	}
	var payload quotaReportsResponse
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	// The seeded custom instance and the new built-in one.
	if len(payload.Reports) != 2 {
		t.Fatalf("reports = %+v", payload.Reports)
	}

	var builtin *quota.Report
	for i := range payload.Reports {
		report := payload.Reports[i]
		if report.Preset == "opencode-go" {
			builtin = &payload.Reports[i]
			continue
		}
		// A custom instance has no vendor quota endpoint.
		if report.Supported {
			t.Fatalf("a custom instance reports a quota: %+v", report)
		}
	}
	if builtin == nil {
		t.Fatalf("the built-in instance has no report: %+v", payload.Reports)
	}
	if !builtin.Supported || len(builtin.Windows) != 3 {
		t.Fatalf("report = %+v", builtin)
	}
	if builtin.Windows[0].ID != quota.Window5H || builtin.Windows[0].UsedPct != 12 {
		t.Fatalf("window = %+v", builtin.Windows[0])
	}
	if builtin.Error != "" {
		t.Fatalf("error = %q", builtin.Error)
	}
}

// TestAdminQuotaLogsUndecryptableProxy makes sure a proxy that cannot be
// decrypted is logged on the server. The console only sees a generic message,
// so the real reason has to reach the log.
func TestAdminQuotaLogsUndecryptableProxy(t *testing.T) {
	t.Parallel()

	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelError}))

	gw, st, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"k"},
		func(o *Options) { o.Log = logger })

	if _, err := st.CreateInstance(store.InstanceSpec{
		Name:              "broken-proxy",
		Models:            []string{"gpt-upstream"},
		Endpoints:         []config.Endpoint{{Protocol: config.ProtocolOpenAI, BaseURL: "https://api.example.com/v1"}},
		EncryptedProxyURL: "not-valid-ciphertext",
	}); err != nil {
		t.Fatal(err)
	}

	resp := adminRequest(t, http.MethodGet, gw.URL+"/admin/api/quota", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("quota = %d", resp.StatusCode)
	}
	if !strings.Contains(logs.String(), "decrypt proxy url") {
		t.Fatalf("the decryption failure was not logged: %q", logs.String())
	}
	if !strings.Contains(logs.String(), "broken-proxy") {
		t.Fatalf("the log does not name the instance: %q", logs.String())
	}
}

// TestAdminQuotaRejectsOtherMethods makes sure the quota endpoint is read
// only.
func TestAdminQuotaRejectsOtherMethods(t *testing.T) {
	gw, _, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"k"})

	resp := adminRequest(t, http.MethodPost, gw.URL+"/admin/api/quota", "{}")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("post = %d", resp.StatusCode)
	}
}

// TestAdminQuotaWithTheArkCredential checks that an instance can carry its
// own quota credential and that the secret never leaves the server.
func TestAdminQuotaWithTheArkCredential(t *testing.T) {
	vendor := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "HMAC-SHA256 Credential=AKLT-test/") {
			t.Errorf("authorization header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"Result":{"QuotaUsage":[
			{"Level":"session","Percent":3.5,"ResetTimestamp":1795410005},
			{"Level":"weekly","Percent":1.2,"ResetTimestamp":1795968000},
			{"Level":"monthly","Percent":72.4,"ResetTimestamp":1796399999}]}}`)
	}))
	t.Cleanup(vendor.Close)

	client := &quota.Client{
		HTTP:    vendor.Client(),
		ArkHost: strings.TrimPrefix(vendor.URL, "https://"),
	}
	gw, _, _ := setup(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"k"},
		func(o *Options) { o.Quota = quota.NewServiceWithClient(client, 0) })

	body := `{"name":"ark","api_key":"sk-inference","preset":"ark-coding-plan-cn",
		"models":["ark-code-latest"],
		"quota_credential":{"access_key_id":"AKLT-test","secret_access_key":"sk-secret"}}`
	resp := adminRequest(t, http.MethodPost, gw.URL+"/admin/api/instances", body)
	if resp.StatusCode != http.StatusCreated {
		payload, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("create = %d %s", resp.StatusCode, payload)
	}
	created := decodeInstance(t, resp)
	if created.QuotaCredential == nil {
		t.Fatal("the instance view has no quota credential")
	}
	if created.QuotaCredential.AccessKeyID != "AKLT-test" {
		t.Fatalf("access key id = %q", created.QuotaCredential.AccessKeyID)
	}
	// The secret is masked, never returned.
	if created.QuotaCredential.SecretHint != "****cret" {
		t.Fatalf("secret hint = %q", created.QuotaCredential.SecretHint)
	}

	quotaResp := adminRequest(t, http.MethodGet, gw.URL+"/admin/api/quota", "")
	defer quotaResp.Body.Close()
	var payload quotaReportsResponse
	if err := json.NewDecoder(quotaResp.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	var ark *quota.Report
	for i := range payload.Reports {
		if payload.Reports[i].Preset == "ark-coding-plan-cn" {
			ark = &payload.Reports[i]
		}
	}
	if ark == nil {
		t.Fatalf("the ark instance has no report: %+v", payload.Reports)
	}
	if !ark.Supported || len(ark.Windows) != 3 {
		t.Fatalf("report = %+v", ark)
	}
	if ark.Windows[0].ID != quota.Window5H || ark.Windows[0].UsedPct != 3.5 {
		t.Fatalf("window = %+v", ark.Windows[0])
	}
	if ark.Error != "" {
		t.Fatalf("error = %q", ark.Error)
	}

	// An update without the credential keeps the stored one.
	update := `{"name":"ark","models":["ark-code-latest"]}`
	resp = adminRequest(t, http.MethodPut, gw.URL+"/admin/api/instances/"+itoa(created.ID), update)
	if resp.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		t.Fatalf("update = %d %s", resp.StatusCode, payload)
	}
	updated := decodeInstance(t, resp)
	if updated.QuotaCredential == nil || updated.QuotaCredential.AccessKeyID != "AKLT-test" {
		t.Fatalf("the credential was dropped: %+v", updated.QuotaCredential)
	}
}
