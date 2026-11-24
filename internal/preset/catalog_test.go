package preset

import (
	"strings"
	"testing"
)

func TestModelMetadataIsCaseInsensitive(t *testing.T) {
	meta, ok := ModelMetadata("  MiniMax-M3.1-Flash ")
	if !ok {
		t.Fatal("MiniMax-M3.1-Flash should be in the catalog")
	}
	if meta.ContextLength != 1000000 {
		t.Fatalf("context length = %d", meta.ContextLength)
	}
	if _, ok := ModelMetadata("minimax-m3.1-flash"); !ok {
		t.Fatal("lower-case lookup should work")
	}
}

func TestModelMetadataSkipsDynamicAlias(t *testing.T) {
	if _, ok := ModelMetadata("ark-code-latest"); ok {
		t.Fatal("ark-code-latest is a control-plane alias and must not carry fixed limits")
	}
}

func TestModelCatalogEntriesAreServedByPresets(t *testing.T) {
	served := map[string]bool{}
	for _, p := range All() {
		for _, model := range p.Models {
			served[strings.ToLower(strings.TrimSpace(model))] = true
		}
	}
	for name, meta := range modelCatalog {
		if !served[name] {
			t.Errorf("catalog entry %q is not served by any preset", name)
		}
		if meta.ContextLength <= 0 {
			t.Errorf("%q: context length must be positive", name)
		}
		if meta.MaxOutputTokens <= 0 {
			t.Errorf("%q: max output must be positive", name)
		}
		if meta.MaxOutputTokens > meta.ContextLength {
			t.Errorf("%q: max output %d exceeds context %d",
				name, meta.MaxOutputTokens, meta.ContextLength)
		}
		if len(meta.Capabilities) == 0 {
			t.Errorf("%q: capabilities should not be empty", name)
		}
	}
}
