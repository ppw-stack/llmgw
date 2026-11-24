package config

import (
	"testing"

	"github.com/pw/llmgw/internal/modelmeta"
	"gopkg.in/yaml.v3"
)

func TestModelMetadataFromYAML(t *testing.T) {
	t.Parallel()
	body := `
instances:
  - name: custom
    endpoints:
      - protocol: openai
        base_url: https://example.com/v1
    models: [m]
models:
  - name: m
    metadata:
      display_name: Demo
      context_length: 200000
      max_output_tokens: 64000
      capabilities: [Tools, VISION]
      protocols:
        anthropic:
          max_output_tokens: 128000
          capabilities: [tools, thinking]
    instances:
      - name: custom
        upstream: m
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(body), &cfg); err != nil {
		t.Fatal(err)
	}
	meta := cfg.Models[0].Metadata
	if meta.DisplayName != "Demo" || meta.ContextLength != 200000 || meta.MaxOutputTokens != 64000 {
		t.Fatalf("metadata = %+v", meta)
	}
	anthropic := meta.For(ProtocolAnthropic)
	if anthropic.MaxOutputTokens != 128000 {
		t.Fatalf("anthropic max output = %d", anthropic.MaxOutputTokens)
	}
	if len(anthropic.Capabilities) != 2 || anthropic.Capabilities[1] != "thinking" {
		t.Fatalf("anthropic capabilities = %v", anthropic.Capabilities)
	}
}

func TestEffectiveMetadataFallsBackToPreset(t *testing.T) {
	t.Parallel()
	cfg := Config{
		Instances: []Instance{
			{
				Name:   "go",
				Preset: "opencode-go",
				Models: []string{"glm-5.3"},
			},
		},
		Models: []Model{
			{
				Name: "glm-5.3",
				Instances: []ModelInstance{
					{Name: "go", Upstream: "glm-5.3"},
				},
			},
		},
	}
	eff, err := cfg.Effective()
	if err != nil {
		t.Fatal(err)
	}
	meta := eff.Models[0].Metadata
	if meta.ContextLength != 1048576 {
		t.Fatalf("context length = %d", meta.ContextLength)
	}
	if meta.MaxOutputTokens != 131072 {
		t.Fatalf("max output = %d", meta.MaxOutputTokens)
	}
	if len(meta.Capabilities) == 0 {
		t.Fatal("preset capabilities should be applied")
	}
}

func TestEffectiveMetadataKeepsExplicitValues(t *testing.T) {
	t.Parallel()
	cfg := Config{
		Instances: []Instance{
			{
				Name:   "go",
				Preset: "opencode-go",
				Models: []string{"glm-5.3"},
			},
		},
		Models: []Model{
			{
				Name: "glm-5.3",
				Metadata: modelmeta.Metadata{
					ContextLength: 32000,
					Capabilities:  []string{"tools"},
				},
				Instances: []ModelInstance{
					{Name: "go", Upstream: "glm-5.3"},
				},
			},
		},
	}
	eff, err := cfg.Effective()
	if err != nil {
		t.Fatal(err)
	}
	meta := eff.Models[0].Metadata
	if meta.ContextLength != 32000 {
		t.Fatalf("explicit context length was overwritten: %d", meta.ContextLength)
	}
	if meta.MaxOutputTokens != 131072 {
		t.Fatalf("missing field should still fall back: %d", meta.MaxOutputTokens)
	}
	if len(meta.Capabilities) != 1 || meta.Capabilities[0] != "tools" {
		t.Fatalf("explicit capabilities were overwritten: %v", meta.Capabilities)
	}
}

func TestEffectiveMetadataSkipsCustomInstances(t *testing.T) {
	t.Parallel()
	cfg := Config{
		Instances: []Instance{
			{
				Name:   "custom",
				Models: []string{"glm-5.3"},
			},
		},
		Models: []Model{
			{
				Name: "glm-5.3",
				Instances: []ModelInstance{
					{Name: "custom", Upstream: "glm-5.3"},
				},
			},
		},
	}
	eff, err := cfg.Effective()
	if err != nil {
		t.Fatal(err)
	}
	if !eff.Models[0].Metadata.IsZero() {
		t.Fatalf("custom instances must not inherit preset metadata: %+v", eff.Models[0].Metadata)
	}
}
