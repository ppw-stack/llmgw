package router

import (
	"testing"
	"time"

	"github.com/pw/llmgw/internal/config"
)

type stubQuota struct {
	states map[string]struct {
		pct       float64
		exhausted bool
	}
}

func (s stubQuota) QuotaState(instance string) (float64, bool, bool) {
	st, ok := s.states[instance]
	if !ok {
		return 0, false, false
	}
	return st.pct, st.exhausted, true
}

func multiInstanceConfig(strategy string, priorities ...int) *config.Config {
	names := []string{"inst-a", "inst-b", "inst-c"}
	cfg := &config.Config{}
	cands := make([]config.ModelInstance, 0, len(priorities))
	for i, p := range priorities {
		name := names[i]
		cfg.Instances = append(cfg.Instances, config.Instance{
			Name:      name,
			APIKey:    "k",
			Endpoints: []config.Endpoint{{Protocol: config.ProtocolOpenAI, BaseURL: "https://api.example.com/v1"}},
			Models:    []string{"m-up"},
		})
		cands = append(cands, config.ModelInstance{
			Name:     name,
			Upstream: "m-up",
			Priority: p,
		})
	}
	cfg.Models = []config.Model{{
		Name:      "m",
		Strategy:  strategy,
		Instances: cands,
	}}
	return cfg
}

func TestQuotaAwareDemotion(t *testing.T) {
	t.Parallel()
	rt := New()
	if err := rt.Reload(multiInstanceConfig(config.StrategyFailover, 0, 1, 2)); err != nil {
		t.Fatal(err)
	}
	rt.SetQuotaProvider(stubQuota{states: map[string]struct {
		pct       float64
		exhausted bool
	}{
		"inst-a": {pct: 99, exhausted: false},
		"inst-b": {pct: 40, exhausted: false},
		"inst-c": {pct: 100, exhausted: true},
	}})

	targets, err := rt.Resolve(config.ProtocolOpenAI, "m")
	if err != nil {
		t.Fatal(err)
	}
	if len(targets) != 3 {
		t.Fatalf("len = %d, want 3", len(targets))
	}
	// inst-b is healthy so it moves first; inst-a (P0) and inst-c (P2) are
	// degraded and stay as fallbacks in their priority order.
	got := []string{targets[0].Instance, targets[1].Instance, targets[2].Instance}
	want := []string{"inst-b", "inst-a", "inst-c"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}

	previews := rt.PreviewModel("m", time.Now())
	if len(previews) != 3 || previews[0].Instance != "inst-b" || previews[0].QuotaDegraded {
		t.Fatalf("unexpected previews[0]: %+v", previews)
	}
	if !previews[1].QuotaDegraded || previews[1].Instance != "inst-a" {
		t.Fatalf("expected inst-a degraded in preview: %+v", previews[1])
	}
}

func TestStrategyRoundRobinSamePriorityOnly(t *testing.T) {
	t.Parallel()
	rt := New()
	// inst-a and inst-b share P0; inst-c is P1 fallback.
	if err := rt.Reload(multiInstanceConfig(config.StrategyRoundRobin, 0, 0, 1)); err != nil {
		t.Fatal(err)
	}

	firstChoices := make([]string, 0, 4)
	for i := 0; i < 4; i++ {
		targets, err := rt.Resolve(config.ProtocolOpenAI, "m")
		if err != nil {
			t.Fatal(err)
		}
		if targets[2].Instance != "inst-c" {
			t.Fatalf("P1 fallback must stay last, got %v", targets[2].Instance)
		}
		firstChoices = append(firstChoices, targets[0].Instance)
	}
	if firstChoices[0] == firstChoices[1] || firstChoices[0] != firstChoices[2] || firstChoices[1] != firstChoices[3] {
		t.Fatalf("round_robin did not alternate P0 candidates: %v", firstChoices)
	}
}

func TestStrategyLeastUsed(t *testing.T) {
	t.Parallel()
	rt := New()
	if err := rt.Reload(multiInstanceConfig(config.StrategyLeastUsed, 0, 0, 0)); err != nil {
		t.Fatal(err)
	}
	rt.SetQuotaProvider(stubQuota{states: map[string]struct {
		pct       float64
		exhausted bool
	}{
		"inst-a": {pct: 75},
		"inst-b": {pct: 20},
		"inst-c": {pct: 50},
	}})

	targets, err := rt.Resolve(config.ProtocolOpenAI, "m")
	if err != nil {
		t.Fatal(err)
	}
	got := []string{targets[0].Instance, targets[1].Instance, targets[2].Instance}
	want := []string{"inst-b", "inst-c", "inst-a"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("least_used order = %v, want %v", got, want)
		}
	}
}

func TestStrategyStickyStablePerSessionAndSpreads(t *testing.T) {
	t.Parallel()
	rt := New()
	if err := rt.Reload(multiInstanceConfig(config.StrategySticky, 0, 0, 0)); err != nil {
		t.Fatal(err)
	}

	// Same session key always picks the exact same primary and keeps all fallbacks.
	first, err := rt.ResolveWithOptions(config.ProtocolOpenAI, "m", ResolveOptions{SessionKey: "ses_alpha"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		again, err := rt.ResolveWithOptions(config.ProtocolOpenAI, "m", ResolveOptions{SessionKey: "ses_alpha"})
		if err != nil {
			t.Fatal(err)
		}
		if again[0].Instance != first[0].Instance || len(again) != 3 {
			t.Fatalf("sticky not stable: got %q vs %q", again[0].Instance, first[0].Instance)
		}
	}

	// Across many distinct sessions, sticky spreads across more than one instance.
	seen := map[string]bool{}
	for i := 0; i < 30; i++ {
		tgs, err := rt.ResolveWithOptions(config.ProtocolOpenAI, "m", ResolveOptions{
			SessionKey: "ses_" + string(rune('a'+i)) + "_" + string(rune('0'+i%10)),
		})
		if err != nil {
			t.Fatal(err)
		}
		seen[tgs[0].Instance] = true
	}
	if len(seen) < 2 {
		t.Fatalf("sticky should distribute distinct sessions across accounts, got %v", seen)
	}
}
