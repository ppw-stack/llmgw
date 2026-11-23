package router

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pw/llmgw/internal/config"
)

// windowTestKey returns the upstream key used by the window tests. Set
// TEST_WINDOW_API_KEY to inject a real one; the fallback is an obviously
// synthetic placeholder so no credential is stored in the repository.
func windowTestKey(suffix string) string {
	if v := strings.TrimSpace(os.Getenv("TEST_WINDOW_API_KEY")); v != "" {
		return v
	}
	return "sk-test-placeholder-" + suffix
}

// windowConfig builds a topology with one model and two candidates. The first
// candidate carries the given time windows, the second one is the plain
// fallback.
func windowConfig(t *testing.T, tz string, basePriority int, windows []config.TimeWindow) *config.Config {
	t.Helper()
	loc, err := time.LoadLocation(tz)
	if err != nil {
		t.Fatalf("load location %q: %v", tz, err)
	}
	return &config.Config{
		Schedule: config.Schedule{Timezone: tz, Location: loc},
		Instances: []config.Instance{
			{
				Name:      "cheap",
				APIKey:    windowTestKey("cheap"),
				Endpoints: []config.Endpoint{{Protocol: config.ProtocolOpenAI, BaseURL: "https://cheap/v1"}},
				Models:    []string{"m"},
			},
			{
				Name:      "plain",
				APIKey:    windowTestKey("plain"),
				Endpoints: []config.Endpoint{{Protocol: config.ProtocolOpenAI, BaseURL: "https://plain/v1"}},
				Models:    []string{"m"},
			},
		},
		Models: []config.Model{
			{
				Name: "m",
				Instances: []config.ModelInstance{
					{Name: "cheap", Upstream: "m", Priority: basePriority, Windows: windows},
					{Name: "plain", Upstream: "m", Priority: 0},
				},
			},
		},
	}
}

func intPtr(v int) *int { return &v }

func instanceNames(targets []Target) []string {
	out := make([]string, 0, len(targets))
	for _, t := range targets {
		out = append(out, t.Instance)
	}
	return out
}

func TestResolveAtAppliesWindowPriority(t *testing.T) {
	t.Parallel()
	r := New()
	cfg := windowConfig(t, "UTC", 10, []config.TimeWindow{
		{Start: "00:00", End: "08:00", Priority: intPtr(0)},
	})
	if err := r.Reload(cfg); err != nil {
		t.Fatalf("reload: %v", err)
	}

	inside, err := r.ResolveAt(config.ProtocolOpenAI, "m", time.Date(2026, 3, 1, 2, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("resolve inside: %v", err)
	}
	if got := instanceNames(inside); len(got) != 2 || got[0] != "cheap" {
		t.Fatalf("inside the window the cheap candidate wins: %v", got)
	}

	outside, err := r.ResolveAt(config.ProtocolOpenAI, "m", time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("resolve outside: %v", err)
	}
	if got := instanceNames(outside); len(got) != 2 || got[0] != "plain" {
		t.Fatalf("outside the window the configured order wins: %v", got)
	}
}

func TestResolveAtUsesTheScheduleTimezone(t *testing.T) {
	t.Parallel()
	r := New()
	cfg := windowConfig(t, "Asia/Shanghai", 10, []config.TimeWindow{
		{Start: "00:00", End: "08:00", Priority: intPtr(0)},
	})
	if err := r.Reload(cfg); err != nil {
		t.Fatalf("reload: %v", err)
	}

	// 2026-03-01T00:30Z is 08:30 in Shanghai, so the window is closed.
	moment := time.Date(2026, 3, 1, 0, 30, 0, 0, time.UTC)
	targets, err := r.ResolveAt(config.ProtocolOpenAI, "m", moment)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := instanceNames(targets); got[0] != "plain" {
		t.Fatalf("outside the local window the plain candidate wins: %v", got)
	}

	// 2026-03-01T16:30Z is 00:30 in Shanghai, so the window is open.
	moment = time.Date(2026, 3, 1, 16, 30, 0, 0, time.UTC)
	targets, err = r.ResolveAt(config.ProtocolOpenAI, "m", moment)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := instanceNames(targets); got[0] != "cheap" {
		t.Fatalf("inside the local window the cheap candidate wins: %v", got)
	}
}

func TestResolveAtDropsCandidatesOutsideTheirOnlyWindow(t *testing.T) {
	t.Parallel()
	r := New()
	cfg := windowConfig(t, "UTC", 10, []config.TimeWindow{
		{Start: "00:00", End: "08:00", Only: true},
	})
	if err := r.Reload(cfg); err != nil {
		t.Fatalf("reload: %v", err)
	}

	targets, err := r.ResolveAt(config.ProtocolOpenAI, "m", time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := instanceNames(targets); len(got) != 1 || got[0] != "plain" {
		t.Fatalf("the restricted candidate must be dropped: %v", got)
	}

	targets, err = r.ResolveAt(config.ProtocolOpenAI, "m", time.Date(2026, 3, 1, 2, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got := instanceNames(targets); len(got) != 2 {
		t.Fatalf("inside the window every candidate is available: %v", got)
	}
}

func TestResolveAtReportsNoAvailableCandidate(t *testing.T) {
	t.Parallel()
	r := New()
	cfg := windowConfig(t, "UTC", 10, []config.TimeWindow{
		{Start: "00:00", End: "08:00", Only: true},
	})
	// Restrict the second candidate as well so nothing is left outside the
	// window.
	cfg.Models[0].Instances[1].Windows = []config.TimeWindow{
		{Start: "00:00", End: "08:00", Only: true},
	}
	if err := r.Reload(cfg); err != nil {
		t.Fatalf("reload: %v", err)
	}

	_, err := r.ResolveAt(config.ProtocolOpenAI, "m", time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC))
	if err == nil {
		t.Fatal("expected an error when every candidate is closed")
	}
	na, ok := err.(*NoAvailableTargetError)
	if !ok {
		t.Fatalf("error = %T %v", err, err)
	}
	if na.Model != "m" {
		t.Fatalf("error = %+v", na)
	}
}

func TestResolveAtHandlesWindowsAroundMidnight(t *testing.T) {
	t.Parallel()
	r := New()
	cfg := windowConfig(t, "UTC", 10, []config.TimeWindow{
		{Start: "22:00", End: "06:00", Priority: intPtr(0)},
	})
	if err := r.Reload(cfg); err != nil {
		t.Fatalf("reload: %v", err)
	}

	for _, hour := range []int{22, 23, 0, 2, 5} {
		moment := time.Date(2026, 3, 1, hour, 30, 0, 0, time.UTC)
		targets, err := r.ResolveAt(config.ProtocolOpenAI, "m", moment)
		if err != nil {
			t.Fatalf("resolve at %d: %v", hour, err)
		}
		if got := instanceNames(targets); got[0] != "cheap" {
			t.Fatalf("the window wraps midnight, %d must be inside: %v", hour, got)
		}
	}
	for _, hour := range []int{6, 12, 21} {
		moment := time.Date(2026, 3, 1, hour, 30, 0, 0, time.UTC)
		targets, err := r.ResolveAt(config.ProtocolOpenAI, "m", moment)
		if err != nil {
			t.Fatalf("resolve at %d: %v", hour, err)
		}
		if got := instanceNames(targets); got[0] != "plain" {
			t.Fatalf("%d must be outside the window: %v", hour, got)
		}
	}
}

func TestResolveKeepsTheConfiguredOrderWithoutWindows(t *testing.T) {
	t.Parallel()
	r := New()
	cfg := windowConfig(t, "UTC", 0, nil)
	if err := r.Reload(cfg); err != nil {
		t.Fatalf("reload: %v", err)
	}
	targets, err := r.Resolve(config.ProtocolOpenAI, "m")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// Both candidates have the same priority, so the configured order wins.
	if got := instanceNames(targets); len(got) != 2 || got[0] != "cheap" {
		t.Fatalf("order = %v", got)
	}
}

func TestPreviewModelReportsTheCurrentState(t *testing.T) {
	t.Parallel()
	r := New()
	cfg := windowConfig(t, "UTC", 10, []config.TimeWindow{
		{Start: "00:00", End: "08:00", Priority: intPtr(0)},
	})
	if err := r.Reload(cfg); err != nil {
		t.Fatalf("reload: %v", err)
	}
	snap := r.Snapshot()
	previews := snap.PreviewModel("m", time.Date(2026, 3, 1, 2, 0, 0, 0, time.UTC))
	if len(previews) != 2 {
		t.Fatalf("previews = %+v", previews)
	}
	if previews[0].Instance != "cheap" || previews[0].Priority != 0 || previews[0].Window != "00:00-08:00" {
		t.Fatalf("first preview = %+v", previews[0])
	}
	if previews[0].BasePriority != 10 {
		t.Fatalf("base priority = %d", previews[0].BasePriority)
	}
	if previews[1].Instance != "plain" || previews[1].Priority != 0 || previews[1].Window != "" {
		t.Fatalf("second preview = %+v", previews[1])
	}
}
