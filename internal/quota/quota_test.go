package quota

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pw/llmgw/internal/preset"
)

// quotaServer starts a fake vendor endpoint and returns a client that talks
// to it instead of the compiled-in URL.
func quotaServer(t *testing.T, status int, body string) (*Client, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "Bearer ") {
			t.Errorf("authorization header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	client := &Client{
		HTTP:       srv.Client(),
		ResolveURL: func(string) string { return srv.URL },
	}
	return client, &calls
}

func TestFetchOpenCodeGo(t *testing.T) {
	t.Parallel()
	client, _ := quotaServer(t, http.StatusOK, `{"usage":{
		"rolling":{"status":"ok","percent":12,"resetsAt":"2026-11-23T04:23:18.000Z"},
		"weekly":{"status":"ok","percent":78,"resetsAt":"2026-11-30T00:00:00.000Z"},
		"monthly":{"status":"ok","percent":94,"resetsAt":"2026-12-20T14:43:31.000Z"}}}`)

	windows, err := client.Fetch(context.Background(),
		preset.QuotaSpec{Kind: KindOpenCodeGo, URL: "https://opencode.ai/zen/go/v1/usage"}, "sk-test", Credential{})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(windows) != 3 {
		t.Fatalf("windows = %+v", windows)
	}
	if windows[0].ID != Window5H || windows[0].UsedPct != 12 {
		t.Fatalf("5h window = %+v", windows[0])
	}
	if windows[0].ResetsAt != "2026-11-23T04:23:18.000Z" {
		t.Fatalf("5h reset = %q", windows[0].ResetsAt)
	}
	if windows[1].ID != WindowWeekly || windows[1].UsedPct != 78 {
		t.Fatalf("weekly window = %+v", windows[1])
	}
	if windows[2].ID != WindowMonthly || windows[2].UsedPct != 94 {
		t.Fatalf("monthly window = %+v", windows[2])
	}
}

func TestFetchOpenCodeGoDropsPlaceholderReset(t *testing.T) {
	t.Parallel()
	client, _ := quotaServer(t, http.StatusOK,
		`{"usage":{"rolling":{"status":"ok","percent":0,"resetsAt":"2026-12-01T00:00:00.000Z"}}}`)

	windows, err := client.Fetch(context.Background(),
		preset.QuotaSpec{Kind: KindOpenCodeGo, URL: "https://example.test/usage"}, "sk-test", Credential{})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(windows) != 1 {
		t.Fatalf("windows = %+v", windows)
	}
	// An untouched window reports a placeholder reset time that would show a
	// wrong countdown, so it is dropped.
	if windows[0].ResetsAt != "" {
		t.Fatalf("reset = %q", windows[0].ResetsAt)
	}
}

func TestFetchOpenCodeGoRateLimited(t *testing.T) {
	t.Parallel()
	client, _ := quotaServer(t, http.StatusOK,
		`{"usage":{"rolling":{"status":"rate-limited","percent":100,"resetsAt":"2026-11-23T04:23:18.000Z"}}}`)

	windows, err := client.Fetch(context.Background(),
		preset.QuotaSpec{Kind: KindOpenCodeGo, URL: "https://example.test/usage"}, "sk-test", Credential{})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if windows[0].Status != StatusExhausted || windows[0].UsedPct != 100 {
		t.Fatalf("window = %+v", windows[0])
	}
}

func TestFetchOpenCodeGoUnexpectedShape(t *testing.T) {
	t.Parallel()
	client, _ := quotaServer(t, http.StatusOK, `{"unexpected":true}`)

	if _, err := client.Fetch(context.Background(),
		preset.QuotaSpec{Kind: KindOpenCodeGo, URL: "https://example.test/usage"}, "sk-test", Credential{}); err == nil {
		t.Fatal("expected an error for an unknown response shape")
	}
}

func TestFetchMiniMax(t *testing.T) {
	t.Parallel()
	client, _ := quotaServer(t, http.StatusOK, `{"model_remains":[
		{"model_name":"general",
		 "current_interval_total_count":0,"current_interval_usage_count":0,
		 "current_interval_status":1,"current_interval_remaining_percent":68,
		 "current_weekly_total_count":0,"current_weekly_usage_count":0,
		 "current_weekly_status":1,"current_weekly_remaining_percent":88,
		 "end_time":1795417200000,"weekly_end_time":1795968000000},
		{"model_name":"video","current_interval_remaining_percent":100,
		 "current_weekly_remaining_percent":100,"current_interval_total_count":3,
		 "current_weekly_total_count":21}],
		"base_resp":{"status_code":0,"status_msg":"success"}}`)

	windows, err := client.Fetch(context.Background(),
		preset.QuotaSpec{Kind: KindMiniMax, URL: "https://api.minimax.cn/v1/token_plan/remains"}, "sk-test", Credential{})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	// The video group is not part of the coding plan, and the plan has no
	// monthly window.
	if len(windows) != 2 {
		t.Fatalf("windows = %+v", windows)
	}
	if windows[0].ID != Window5H || windows[0].UsedPct != 32 {
		t.Fatalf("5h window = %+v", windows[0])
	}
	if windows[0].ResetsAt != "2026-11-23T07:00:00Z" {
		t.Fatalf("5h reset = %q", windows[0].ResetsAt)
	}
	if windows[1].ID != WindowWeekly || windows[1].UsedPct != 12 {
		t.Fatalf("weekly window = %+v", windows[1])
	}
	if windows[1].ResetsAt != "2026-11-29T16:00:00Z" {
		t.Fatalf("weekly reset = %q", windows[1].ResetsAt)
	}
}

func TestFetchMiniMaxWithoutWeeklyWindow(t *testing.T) {
	t.Parallel()
	client, _ := quotaServer(t, http.StatusOK, `{"model_remains":[
		{"model_name":"general","current_interval_status":1,"current_interval_remaining_percent":68,
		 "current_weekly_status":3,"current_weekly_remaining_percent":100}],
		"base_resp":{"status_code":0,"status_msg":"success"}}`)

	windows, err := client.Fetch(context.Background(),
		preset.QuotaSpec{Kind: KindMiniMax, URL: "https://example.test/remains"}, "sk-test", Credential{})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(windows) != 1 || windows[0].ID != Window5H {
		t.Fatalf("windows = %+v", windows)
	}
}

func TestFetchMiniMaxRejectedKey(t *testing.T) {
	t.Parallel()
	client, _ := quotaServer(t, http.StatusOK,
		`{"base_resp":{"status_code":2049,"status_msg":"invalid api key"}}`)

	_, err := client.Fetch(context.Background(),
		preset.QuotaSpec{Kind: KindMiniMax, URL: "https://example.test/remains"}, "sk-test", Credential{})
	if err == nil {
		t.Fatal("expected an error for a rejected key")
	}
	// A rejected key is an authentication failure, so the console shows the
	// same message as for the other vendors.
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusUnauthorized {
		t.Fatalf("error = %v", err)
	}
}

func TestFetchHidesTheKeyInErrors(t *testing.T) {
	t.Parallel()
	const secret = "sk-super-secret-value"
	client, _ := quotaServer(t, http.StatusUnauthorized, `{"error":"invalid key `+secret+`"}`)

	_, err := client.Fetch(context.Background(),
		preset.QuotaSpec{Kind: KindOpenCodeGo, URL: "https://example.test/usage"}, secret, Credential{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("the error leaks the key: %v", err)
	}
}

func TestFetchUnknownKind(t *testing.T) {
	t.Parallel()
	client, calls := quotaServer(t, http.StatusOK, `{}`)

	if _, err := client.Fetch(context.Background(),
		preset.QuotaSpec{Kind: "nope", URL: "https://example.test/usage"}, "sk-test", Credential{}); err == nil {
		t.Fatal("expected an error for an unknown kind")
	}
	if calls.Load() != 0 {
		t.Fatal("an unknown kind must not reach the network")
	}
}

func TestServiceCachesReports(t *testing.T) {
	t.Parallel()
	client, calls := quotaServer(t, http.StatusOK, `{"usage":{
		"rolling":{"status":"ok","percent":12,"resetsAt":"2026-11-23T04:23:18.000Z"}}}`)

	svc := NewServiceWithClient(client, time.Minute)
	target := Target{ID: 7, Name: "go", PresetID: "opencode-go", APIKey: "sk-test"}
	first := svc.Report(context.Background(), target)
	second := svc.Report(context.Background(), target)
	if calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1", calls.Load())
	}
	if !first.Supported || len(first.Windows) != 1 {
		t.Fatalf("report = %+v", first)
	}
	if second.FetchedAt != first.FetchedAt {
		t.Fatalf("the cached report changed: %q vs %q", second.FetchedAt, first.FetchedAt)
	}
}

func TestServiceSkipsUnsupportedPresets(t *testing.T) {
	t.Parallel()
	client, calls := quotaServer(t, http.StatusOK, `{}`)

	svc := NewServiceWithClient(client, time.Minute)
	// A custom instance has no vendor quota endpoint.
	report := svc.Report(context.Background(), Target{ID: 1, Name: "custom", PresetID: "", APIKey: "sk"})
	if report.Supported {
		t.Fatalf("report = %+v", report)
	}
	if len(report.Windows) != 0 || report.Error != "" {
		t.Fatalf("report = %+v", report)
	}
	if calls.Load() != 0 {
		t.Fatal("an unsupported preset must not reach the network")
	}
}

func TestServiceNeedsTheArkCredential(t *testing.T) {
	t.Parallel()
	client, calls := quotaServer(t, http.StatusOK, `{}`)

	svc := NewServiceWithClient(client, time.Minute)
	// Ark publishes the plan windows on its control plane, which asks for an
	// AccessKey pair instead of the inference key.
	report := svc.Report(context.Background(),
		Target{ID: 1, Name: "ark", PresetID: "ark-coding-plan-cn", APIKey: "sk-inference"})
	if !report.Supported {
		t.Fatalf("report = %+v", report)
	}
	if report.Error == "" {
		t.Fatalf("report = %+v", report)
	}
	if calls.Load() != 0 {
		t.Fatal("a missing credential must not reach the network")
	}
}

func TestServiceReportsAMissingKey(t *testing.T) {
	t.Parallel()
	client, calls := quotaServer(t, http.StatusOK, `{}`)

	svc := NewServiceWithClient(client, time.Minute)
	report := svc.Report(context.Background(), Target{ID: 1, Name: "go", PresetID: "opencode-go"})
	if !report.Supported {
		t.Fatalf("report = %+v", report)
	}
	if report.Error == "" {
		t.Fatalf("report = %+v", report)
	}
	if calls.Load() != 0 {
		t.Fatal("a missing key must not reach the network")
	}
}

func TestServiceTurnsUpstreamStatusIntoAMessage(t *testing.T) {
	t.Parallel()
	client, _ := quotaServer(t, http.StatusUnauthorized, `{"error":"unauthorized"}`)

	svc := NewServiceWithClient(client, time.Minute)
	report := svc.Report(context.Background(),
		Target{ID: 1, Name: "go", PresetID: "opencode-go", APIKey: "sk-test"})
	if report.Error != "上游 API Key 无效" {
		t.Fatalf("error = %q", report.Error)
	}
}
