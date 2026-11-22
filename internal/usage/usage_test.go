package usage

import (
	"testing"

	"github.com/pw/llmgw/internal/config"
)

func pricing() map[string]config.Price {
	return map[string]config.Price{
		"gpt-4":             {InputPerMTok: 10, OutputPerMTok: 20},
		"gpt-4o":            {InputPerMTok: 1, OutputPerMTok: 2},
		"gpt-4o-mini":       {InputPerMTok: 0.1, OutputPerMTok: 0.2},
		"claude-sonnet-4-5": {InputPerMTok: 3, OutputPerMTok: 15},
	}
}

func TestExactMatch(t *testing.T) {
	p, ok := lookup(pricing(), "gpt-4")
	if !ok || p.InputPerMTok != 10 {
		t.Fatalf("got %+v ok=%v", p, ok)
	}
}

func TestPrefixRequiresBoundary(t *testing.T) {
	// "gpt-4" must not price "gpt-4o".
	p, ok := lookup(pricing(), "gpt-4o")
	if !ok || p.InputPerMTok != 1 {
		t.Fatalf("gpt-4o priced as %+v", p)
	}
	// A dated snapshot falls back to the longest matching prefix.
	p, ok = lookup(pricing(), "gpt-4o-2024-08-06")
	if !ok || p.InputPerMTok != 1 {
		t.Fatalf("snapshot priced as %+v", p)
	}
	// Unknown family must not match.
	if _, ok := lookup(pricing(), "gpt-5"); ok {
		t.Fatal("gpt-5 should not match")
	}
}

func TestLongestPrefixWins(t *testing.T) {
	p, _ := lookup(pricing(), "gpt-4o-mini-2024")
	if p.InputPerMTok != 0.1 {
		t.Fatalf("expected mini pricing, got %+v", p)
	}
}

func TestCost(t *testing.T) {
	cost := Cost(pricing(), "claude-sonnet-4-5", Usage{PromptTokens: 1000, CompletionTokens: 1000})
	want := (1000*3.0 + 1000*15.0) / 1_000_000
	if cost != want {
		t.Fatalf("cost = %v want %v", cost, want)
	}
}
