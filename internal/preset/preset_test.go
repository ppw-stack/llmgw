package preset

import (
	"net/url"
	"testing"
)

func TestCatalogIsConsistent(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, p := range All() {
		if p.ID == "" || p.FamilyName == "" || p.DefaultName == "" {
			t.Fatalf("incomplete preset: %+v", p)
		}
		if seen[p.ID] {
			t.Fatalf("duplicate preset id %q", p.ID)
		}
		seen[p.ID] = true
		for _, raw := range []string{p.OpenAIBaseURL, p.AnthropicBaseURL} {
			u, err := url.Parse(raw)
			if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
				t.Fatalf("preset %q: invalid base url %q", p.ID, raw)
			}
		}
		if len(p.Models) == 0 {
			t.Fatalf("preset %q has no default models", p.ID)
		}
	}
}

func TestMiniMaxVariants(t *testing.T) {
	t.Parallel()
	global, ok := Lookup("minimax-mplan-global")
	if !ok || global.OpenAIBaseURL != "https://api.minimax.io/v1" {
		t.Fatalf("global preset = %+v", global)
	}
	cn, ok := Lookup("minimax-mplan-cn")
	if !ok {
		t.Fatal("missing minimax-mplan-cn preset")
	}
	if cn.OpenAIBaseURL != "https://api.minimax.cn/v1" ||
		cn.AnthropicBaseURL != "https://api.minimax.cn/anthropic" {
		t.Fatalf("cn preset urls = %+v", cn)
	}
	if cn.DefaultName != "minimax-cn" || cn.VariantName != "国内" {
		t.Fatalf("cn preset = %+v", cn)
	}
	if len(cn.Models) == 0 || cn.Models[0] != "MiniMax-M3.1-Flash" {
		t.Fatalf("cn preset models = %v", cn.Models)
	}
}

func TestLookupReturnsCopy(t *testing.T) {
	t.Parallel()
	p, ok := Lookup("minimax-mplan-global")
	if !ok {
		t.Fatal("expected the MiniMax preset to exist")
	}
	p.Models[0] = "mutated"
	again, _ := Lookup("minimax-mplan-global")
	if again.Models[0] == "mutated" {
		t.Fatal("Lookup must not expose the catalog's slice")
	}
	if _, ok := Lookup("nope"); ok {
		t.Fatal("unknown preset must not be found")
	}
}
