package quota

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pw/llmgw/internal/preset"
)

// arkServer starts a fake control plane that answers the quota actions.
func arkServer(t *testing.T, handler func(action string) (int, string)) *Client {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		action := r.URL.Query().Get("Action")
		if got := r.Header.Get("Authorization"); !strings.HasPrefix(got, "HMAC-SHA256 Credential=") {
			t.Errorf("authorization header = %q", got)
		}
		status, body := handler(action)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "https://")
	return &Client{HTTP: srv.Client(), ArkHost: host}
}

func TestSignArkRequest(t *testing.T) {
	t.Parallel()
	cred := Credential{AccessKeyID: "AKLT-test", SecretAccessKey: "sk-test-secret"}
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)

	endpoint, headers, err := signArkRequest("open.volcengineapi.com", cred, "GetCodingPlanUsage", "cn-beijing", now)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	if endpoint != "https://open.volcengineapi.com/?Action=GetCodingPlanUsage&Region=cn-beijing&Version=2024-01-01" {
		t.Fatalf("endpoint = %q", endpoint)
	}
	if headers["X-Date"] != "20261123T120000Z" {
		t.Fatalf("x-date = %q", headers["X-Date"])
	}
	if headers["X-Content-Sha256"] != sha256Hex(nil) {
		t.Fatalf("payload hash = %q", headers["X-Content-Sha256"])
	}
	want := "HMAC-SHA256 Credential=AKLT-test/20261123/cn-beijing/ark/request, " +
		"SignedHeaders=host;x-date;x-content-sha256;content-type, Signature=" +
		"e1317724b58f8e26e8a7f76c13c68f96402833b07fb67838ae834d590f63a008"
	if headers["Authorization"] != want {
		t.Fatalf("authorization =\n%q\nwant\n%q", headers["Authorization"], want)
	}
}

func TestFetchArkCodingPlan(t *testing.T) {
	t.Parallel()
	client := arkServer(t, func(action string) (int, string) {
		if action != "GetCodingPlanUsage" {
			t.Errorf("action = %q", action)
		}
		return http.StatusOK, `{"Result":{"Status":"Running","UpdateTimestamp":1795406968,
			"QuotaUsage":[
			  {"Level":"session","Percent":3.4972075,"ResetTimestamp":1795410005,"Cap":100},
			  {"Level":"weekly","Percent":1.1968243,"ResetTimestamp":1795968000,"Cap":100},
			  {"Level":"monthly","Percent":72.415083,"ResetTimestamp":1796399999,"Cap":100}]}}`
	})

	windows, err := client.Fetch(context.Background(),
		preset.QuotaSpec{Kind: KindArk}, "sk-inference",
		Credential{AccessKeyID: "AKLT-test", SecretAccessKey: "sk-test-secret"})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(windows) != 3 {
		t.Fatalf("windows = %+v", windows)
	}
	if windows[0].ID != Window5H || windows[0].UsedPct != 3.4972075 {
		t.Fatalf("5h window = %+v", windows[0])
	}
	if windows[0].ResetsAt != "2026-11-23T05:00:05Z" {
		t.Fatalf("5h reset = %q", windows[0].ResetsAt)
	}
	if windows[1].ID != WindowWeekly || windows[2].ID != WindowMonthly {
		t.Fatalf("windows = %+v", windows)
	}
	if windows[2].UsedPct != 72.415083 {
		t.Fatalf("monthly window = %+v", windows[2])
	}
}

func TestFetchArkFallsBackToAgentPlan(t *testing.T) {
	t.Parallel()
	var actions []string
	client := arkServer(t, func(action string) (int, string) {
		actions = append(actions, action)
		if action == "GetCodingPlanUsage" {
			return http.StatusOK, `{"Result":{"QuotaUsage":[]}}`
		}
		return http.StatusOK, `{"Result":{
			"AFPFiveHour":{"Quota":1000,"Used":250,"ResetTime":1795410005},
			"AFPWeekly":{"Quota":50000,"Used":12500,"ResetTime":1795968000},
			"AFPMonthly":{"Quota":0,"Used":0,"ResetTime":0}}}`
	})

	windows, err := client.Fetch(context.Background(),
		preset.QuotaSpec{Kind: KindArk}, "sk-inference",
		Credential{AccessKeyID: "AKLT-test", SecretAccessKey: "sk-test-secret"})
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if len(actions) != 2 || actions[0] != "GetCodingPlanUsage" || actions[1] != "GetAFPUsage" {
		t.Fatalf("actions = %v", actions)
	}
	// The Agent Plan answers with absolute values, so the percentage is derived.
	if len(windows) != 2 {
		t.Fatalf("windows = %+v", windows)
	}
	if windows[0].ID != Window5H || windows[0].UsedPct != 25 {
		t.Fatalf("5h window = %+v", windows[0])
	}
	if windows[1].ID != WindowWeekly || windows[1].UsedPct != 25 {
		t.Fatalf("weekly window = %+v", windows[1])
	}
}

func TestFetchArkReportsAnAuthFailure(t *testing.T) {
	t.Parallel()
	client := arkServer(t, func(action string) (int, string) {
		return http.StatusOK, `{"ResponseMetadata":{"Error":{"Code":"InvalidAccessKey","Message":"bad key"}}}`
	})

	_, err := client.Fetch(context.Background(),
		preset.QuotaSpec{Kind: KindArk}, "sk-inference",
		Credential{AccessKeyID: "AKLT-test", SecretAccessKey: "sk-test-secret"})
	if err == nil {
		t.Fatal("expected an error")
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusUnauthorized {
		t.Fatalf("error = %v", err)
	}
}

func TestFetchArkNeedsACredential(t *testing.T) {
	t.Parallel()
	client := arkServer(t, func(action string) (int, string) {
		t.Error("the control plane must not be called without a credential")
		return http.StatusOK, "{}"
	})

	if _, err := client.Fetch(context.Background(),
		preset.QuotaSpec{Kind: KindArk}, "sk-inference", Credential{}); err == nil {
		t.Fatal("expected an error")
	}
}
