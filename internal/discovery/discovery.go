// Package discovery queries the model catalog an upstream provider exposes so
// the console can offer model names as candidates when an instance is created.
package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/pw/llmgw/internal/config"
)

// maxResponseBytes bounds the model list a provider may return.
const maxResponseBytes = 1 << 20

// Candidate is one model an upstream reports as available.
type Candidate struct {
	ID string `json:"id"`
	// Protocols lists the ingress protocols the upstream says the model
	// supports. It is empty when the provider does not expose that metadata.
	Protocols []string `json:"protocols,omitempty"`
}

// Fetch asks a provider for its model list. protocol selects the auth header
// so the request matches the way the gateway calls chat completions.
func Fetch(ctx context.Context, client *http.Client, endpoint, protocol, apiKey string) ([]Candidate, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build model list request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if protocol == config.ProtocolAnthropic {
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	} else {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request model list: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read model list: %w", err)
	}
	if len(body) > maxResponseBytes {
		return nil, fmt.Errorf("model list response exceeded 1 MiB")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model list returned HTTP %d", resp.StatusCode)
	}
	return Parse(body)
}

// Parse extracts the usable chat models from one model-list response.
//
// Providers describe capabilities differently:
//
//   - OpenCode Go exposes an "api" object per model ("chat-completions",
//     "messages", "responses", ...).
//   - Volcengine Ark exposes "api_supported" ("chat/completions", "messages",
//     "images/generations", ...).
//   - MiniMax exposes only id/created/owned_by.
//
// When a response carries capability metadata, models without chat or messages
// support are dropped. When it carries none, every id is kept because the
// provider gave no basis for filtering.
func Parse(body []byte) ([]Candidate, error) {
	var payload struct {
		Data []struct {
			ID           string                     `json:"id"`
			API          map[string]json.RawMessage `json:"api"`
			APISupported map[string]bool            `json:"api_supported"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode model list: %w", err)
	}

	hasMetadata := false
	for _, item := range payload.Data {
		if item.API != nil || item.APISupported != nil {
			hasMetadata = true
			break
		}
	}

	out := make([]Candidate, 0, len(payload.Data))
	seen := make(map[string]bool, len(payload.Data))
	for _, item := range payload.Data {
		if item.ID == "" || seen[item.ID] {
			continue
		}

		protocols := protocolsOf(item.API, item.APISupported)
		if item.API != nil || item.APISupported != nil {
			if len(protocols) == 0 {
				continue
			}
		} else if hasMetadata {
			// The provider describes capabilities for other models but not
			// this one; it is not safe to present it as a chat candidate.
			continue
		}

		seen[item.ID] = true
		out = append(out, Candidate{ID: item.ID, Protocols: protocols})
	}
	return out, nil
}

// protocolsOf maps a provider's capability keys to ingress protocols.
func protocolsOf(api map[string]json.RawMessage, supported map[string]bool) []string {
	var protocols []string
	if api != nil {
		if _, ok := api["chat-completions"]; ok {
			protocols = append(protocols, config.ProtocolOpenAI)
		}
		if _, ok := api["messages"]; ok {
			protocols = append(protocols, config.ProtocolAnthropic)
		}
		return protocols
	}
	if supported != nil {
		if supported["chat/completions"] {
			protocols = append(protocols, config.ProtocolOpenAI)
		}
		if supported["messages"] {
			protocols = append(protocols, config.ProtocolAnthropic)
		}
	}
	return protocols
}
