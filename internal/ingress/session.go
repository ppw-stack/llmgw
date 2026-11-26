package ingress

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// sessionHeaderCandidates are client headers that already identify a
// conversation. Checked in order when deriving a sticky routing key.
var sessionHeaderCandidates = []string{
	"x-opencode-session",
	"x-session-id",
	"x-claude-code-session-id",
	"x-conversation-id",
}

// SessionKey derives a stable conversation identifier for sticky routing.
// Client-supplied session headers take precedence; otherwise the gateway
// fingerprints the gateway key ID together with the conversation prefix so
// multi-turn requests land on the same upstream account.
func SessionKey(h http.Header, apiKeyID int64, body []byte) string {
	for _, name := range sessionHeaderCandidates {
		if v := strings.TrimSpace(h.Get(name)); v != "" {
			return "hdr:" + name + ":" + v
		}
	}
	prefix := ConversationPrefix(body)
	if prefix == "" {
		return ""
	}
	return fmt.Sprintf("key:%d:prefix:%s", apiKeyID, prefix)
}

// ConversationPrefix fingerprints the part of a request that identifies a
// conversation: its system prompt and first user message. Later turns repeat
// that prefix, which keeps the derived session stable, while a different
// conversation starts from a different prefix. Supports Chat Completions /
// Anthropic Messages (`system` + `messages`) and OpenAI Responses API
// (`instructions` + `input`).
func ConversationPrefix(body []byte) string {
	var payload struct {
		System       json.RawMessage `json:"system"`
		Instructions json.RawMessage `json:"instructions"`
		Messages     []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Input json.RawMessage `json:"input"`
	}
	if len(body) == 0 {
		return ""
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	h := sha256.New()
	wrote := false
	if len(payload.System) > 0 {
		h.Write(payload.System)
		wrote = true
	}
	if len(payload.Instructions) > 0 {
		h.Write(payload.Instructions)
		wrote = true
	}
	if len(payload.Messages) > 0 {
		for _, m := range payload.Messages {
			if m.Role == "system" || m.Role == "developer" {
				h.Write(m.Content)
				wrote = true
			}
		}
		for _, m := range payload.Messages {
			if m.Role == "user" {
				h.Write(m.Content)
				wrote = true
				break
			}
		}
	} else if len(payload.Input) > 0 {
		var items []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if err := json.Unmarshal(payload.Input, &items); err == nil {
			for _, m := range items {
				if m.Role == "system" || m.Role == "developer" {
					h.Write(m.Content)
					wrote = true
				}
			}
			for _, m := range items {
				if m.Role == "user" {
					h.Write(m.Content)
					wrote = true
					break
				}
			}
		} else {
			h.Write(payload.Input)
			wrote = true
		}
	}
	if !wrote {
		return ""
	}
	return hex.EncodeToString(h.Sum(nil))
}
