package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/pw/llmgw/internal/config"
	"github.com/pw/llmgw/internal/modelmeta"
)

func TestOpenAIModelsAdvertiseMetadata(t *testing.T) {
	gw, _, key := setupConfig(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"up-1"},
		func(cfg *config.Config) {
			cfg.Models[0].Metadata = modelmeta.Metadata{
				DisplayName:     "GPT Test",
				ContextLength:   200000,
				MaxOutputTokens: 64000,
				Capabilities:    []string{"tools", "vision"},
			}
		})

	body := getModels(t, gw.URL+"/openai/v1/models", key)
	var out struct {
		Object string `json:"object"`
		Data   []struct {
			ID              string   `json:"id"`
			Object          string   `json:"object"`
			ContextLength   int      `json:"context_length"`
			MaxOutputTokens int      `json:"max_output_tokens"`
			DisplayName     string   `json:"display_name"`
			Capabilities    []string `json:"capabilities"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v: %s", err, body)
	}
	if out.Object != "list" || len(out.Data) != 2 {
		t.Fatalf("response = %s", body)
	}
	var withMeta, withoutMeta bool
	for _, item := range out.Data {
		switch item.ID {
		case "gpt-test":
			withMeta = true
			if item.ContextLength != 200000 || item.MaxOutputTokens != 64000 {
				t.Fatalf("limits = %+v", item)
			}
			if item.DisplayName != "GPT Test" || len(item.Capabilities) != 2 {
				t.Fatalf("metadata = %+v", item)
			}
		case "claude-test":
			withoutMeta = true
			if item.ContextLength != 0 || item.DisplayName != "" || len(item.Capabilities) != 0 {
				t.Fatalf("model without metadata must stay minimal: %+v", item)
			}
		}
	}
	if !withMeta || !withoutMeta {
		t.Fatalf("models = %s", body)
	}
}

func TestAnthropicModelsUseAnthropicShape(t *testing.T) {
	gw, _, key := setupConfig(t, nil, func(w http.ResponseWriter, r *http.Request) {}, []string{"up-1"},
		func(cfg *config.Config) {
			cfg.Models[0].Metadata = modelmeta.Metadata{
				ContextLength: 200000,
				Protocols: map[string]modelmeta.Override{
					config.ProtocolAnthropic: {MaxOutputTokens: 128000},
				},
			}
		})

	body := getModels(t, gw.URL+"/anthropic/v1/models", key)
	var out struct {
		Data []struct {
			Type        string `json:"type"`
			ID          string `json:"id"`
			DisplayName string `json:"display_name"`
			Context     int    `json:"context_length"`
			MaxOutput   int    `json:"max_output_tokens"`
		} `json:"data"`
		HasMore bool `json:"has_more"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v: %s", err, body)
	}
	if len(out.Data) != 2 || out.HasMore {
		t.Fatalf("response = %s", body)
	}
	for _, item := range out.Data {
		if item.Type != "model" || item.DisplayName == "" {
			t.Fatalf("anthropic model shape = %+v", item)
		}
		if item.ID == "gpt-test" && (item.Context != 200000 || item.MaxOutput != 128000) {
			t.Fatalf("anthropic override = %+v", item)
		}
	}
}

func getModels(t *testing.T, url, key string) []byte {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body := readAll(t, resp)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	return []byte(body)
}
