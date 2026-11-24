package router

import (
	"testing"

	"github.com/pw/llmgw/internal/config"
	"github.com/pw/llmgw/internal/modelmeta"
)

func TestModelInfosExpandsMetadataPerProtocol(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{
		Instances: []config.Instance{
			{
				Name:      "openai-only",
				APIKey:    "k1",
				Endpoints: []config.Endpoint{{Protocol: config.ProtocolOpenAI, BaseURL: "https://a/v1"}},
				Models:    []string{"shared"},
			},
			{
				Name:      "anthropic-only",
				APIKey:    "k2",
				Endpoints: []config.Endpoint{{Protocol: config.ProtocolAnthropic, BaseURL: "https://b/anthropic"}},
				Models:    []string{"claude-only"},
			},
		},
		Models: []config.Model{
			{
				Name: "shared",
				Metadata: modelmeta.Metadata{
					ContextLength:   1000,
					MaxOutputTokens: 100,
					Capabilities:    []string{"tools"},
					Protocols: map[string]modelmeta.Override{
						"openai": {MaxOutputTokens: 200},
					},
				},
				Instances: []config.ModelInstance{{Name: "openai-only", Upstream: "shared"}},
			},
			{
				Name:      "claude-only",
				Instances: []config.ModelInstance{{Name: "anthropic-only", Upstream: "claude-only"}},
			},
		},
	}
	r := New()
	if err := r.Reload(cfg); err != nil {
		t.Fatal(err)
	}

	openai := r.ModelInfos(config.ProtocolOpenAI)
	if len(openai) != 1 || openai[0].Name != "shared" {
		t.Fatalf("openai infos = %+v", openai)
	}
	if openai[0].Metadata.MaxOutputTokens != 200 {
		t.Fatalf("openai override = %+v", openai[0].Metadata)
	}
	if openai[0].Metadata.Protocols != nil {
		t.Fatal("expanded metadata must not carry the protocol map")
	}

	anthropic := r.ModelInfos(config.ProtocolAnthropic)
	if len(anthropic) != 1 || anthropic[0].Name != "claude-only" {
		t.Fatalf("anthropic infos = %+v", anthropic)
	}
	if !anthropic[0].Metadata.IsZero() {
		t.Fatalf("unexpected metadata = %+v", anthropic[0].Metadata)
	}

	if got := r.ModelList(config.ProtocolOpenAI); len(got) != 1 || got[0] != "shared" {
		t.Fatalf("model list = %v", got)
	}
}
