package preset

import (
	"net/url"
	"testing"
)

func TestCatalogIsConsistent(t *testing.T) {
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

func TestLookupReturnsCopy(t *testing.T) {
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
