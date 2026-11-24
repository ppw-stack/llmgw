package modelmeta

import (
	"reflect"
	"testing"
)

func TestNormalizeCanonicalizesCapabilities(t *testing.T) {
	t.Parallel()
	meta := Metadata{
		DisplayName:     "  GLM-5.3  ",
		ContextLength:   -1,
		MaxOutputTokens: 131072,
		Capabilities:    []string{" Tools ", "VISION", "tools", ""},
		Protocols: map[string]Override{
			" Anthropic ": {
				ContextLength: -1,
				Capabilities:  []string{"Thinking", "TOOLS"},
			},
		},
	}
	meta.Normalize()
	if meta.DisplayName != "GLM-5.3" {
		t.Fatalf("display name = %q", meta.DisplayName)
	}
	if meta.ContextLength != 0 {
		t.Fatalf("context length = %d", meta.ContextLength)
	}
	if want := []string{"tools", "vision"}; !reflect.DeepEqual(meta.Capabilities, want) {
		t.Fatalf("capabilities = %v", meta.Capabilities)
	}
	ov, ok := meta.Protocols["anthropic"]
	if !ok {
		t.Fatalf("protocol keys = %v", meta.Protocols)
	}
	if ov.ContextLength != 0 || !reflect.DeepEqual(ov.Capabilities, []string{"thinking", "tools"}) {
		t.Fatalf("override = %+v", ov)
	}
}

func TestNormalizeDropsEmptyProtocolMap(t *testing.T) {
	t.Parallel()
	meta := Metadata{Protocols: map[string]Override{" ": {}}}
	meta.Normalize()
	if meta.Protocols != nil {
		t.Fatalf("protocols = %v", meta.Protocols)
	}
}

func TestForAppliesProtocolOverride(t *testing.T) {
	t.Parallel()
	meta := Metadata{
		ContextLength:   200000,
		MaxOutputTokens: 64000,
		Capabilities:    []string{"tools", "vision"},
		Protocols: map[string]Override{
			"anthropic": {
				MaxOutputTokens: 128000,
				Capabilities:    []string{"tools", "thinking"},
			},
		},
	}
	openai := meta.For("openai")
	if openai.ContextLength != 200000 || openai.MaxOutputTokens != 64000 {
		t.Fatalf("openai = %+v", openai)
	}
	if !reflect.DeepEqual(openai.Capabilities, []string{"tools", "vision"}) {
		t.Fatalf("openai capabilities = %v", openai.Capabilities)
	}
	if openai.Protocols != nil {
		t.Fatalf("openai protocols should be expanded: %v", openai.Protocols)
	}
	anthropic := meta.For("Anthropic")
	if anthropic.ContextLength != 200000 || anthropic.MaxOutputTokens != 128000 {
		t.Fatalf("anthropic = %+v", anthropic)
	}
	if !reflect.DeepEqual(anthropic.Capabilities, []string{"tools", "thinking"}) {
		t.Fatalf("anthropic capabilities = %v", anthropic.Capabilities)
	}
}

func TestForKeepsBaseWhenOverrideOmitsCapabilities(t *testing.T) {
	t.Parallel()
	meta := Metadata{
		ContextLength: 1000,
		Capabilities:  []string{"tools"},
		Protocols:     map[string]Override{"openai": {ContextLength: 2000}},
	}
	got := meta.For("openai")
	if got.ContextLength != 2000 {
		t.Fatalf("context length = %d", got.ContextLength)
	}
	if !reflect.DeepEqual(got.Capabilities, []string{"tools"}) {
		t.Fatalf("capabilities = %v", got.Capabilities)
	}
}

func TestMergePrefersExplicitValues(t *testing.T) {
	t.Parallel()
	explicit := Metadata{
		ContextLength: 1000,
		Capabilities:  []string{"tools"},
		Protocols: map[string]Override{
			"anthropic": {MaxOutputTokens: 2000},
		},
	}
	fallback := Metadata{
		DisplayName:     "Fallback",
		ContextLength:   9000,
		MaxOutputTokens: 4000,
		Capabilities:    []string{"vision"},
		Protocols: map[string]Override{
			"anthropic": {ContextLength: 8000, Capabilities: []string{"thinking"}},
		},
	}
	got := explicit.Merge(fallback)
	if got.DisplayName != "Fallback" {
		t.Fatalf("display name = %q", got.DisplayName)
	}
	if got.ContextLength != 1000 || got.MaxOutputTokens != 4000 {
		t.Fatalf("limits = %d/%d", got.ContextLength, got.MaxOutputTokens)
	}
	if !reflect.DeepEqual(got.Capabilities, []string{"tools"}) {
		t.Fatalf("capabilities = %v", got.Capabilities)
	}
	ov := got.Protocols["anthropic"]
	if ov.ContextLength != 8000 || ov.MaxOutputTokens != 2000 {
		t.Fatalf("anthropic override = %+v", ov)
	}
	if !reflect.DeepEqual(ov.Capabilities, []string{"thinking"}) {
		t.Fatalf("anthropic capabilities = %v", ov.Capabilities)
	}
}

func TestIsZero(t *testing.T) {
	t.Parallel()
	if !(Metadata{}).IsZero() {
		t.Fatal("empty metadata should be zero")
	}
	if (Metadata{ContextLength: 1}).IsZero() {
		t.Fatal("metadata with a limit is not zero")
	}
}
