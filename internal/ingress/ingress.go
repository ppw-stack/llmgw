package ingress

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/pw/llmgw/internal/config"
	"github.com/tidwall/gjson"
)

const (
	PathOpenAIChat    = "/openai/v1/chat/completions"
	PathOpenAIModels  = "/openai/v1/models"
	PathAnthropicMsgs = "/anthropic/v1/messages"
)

// Normalize makes common base_url mistakes work anyway.
func Normalize(path string) string {
	if path == "" {
		return "/"
	}
	// Collapse repeated /v1/v1/ segments produced by a base_url that already
	// contains /v1 while the SDK appends another one.
	for strings.Contains(path, "/v1/v1/") {
		path = strings.ReplaceAll(path, "/v1/v1/", "/v1/")
	}
	path = strings.TrimSuffix(path, "/")
	if path == "" {
		return "/"
	}
	switch path {
	case "/anthropic/messages", "/anthropic/v1/message":
		return PathAnthropicMsgs
	case "/openai/chat/completions":
		return PathOpenAIChat
	case "/openai/models":
		return PathOpenAIModels
	}
	return path
}

// ProtocolOf maps a normalized path to its protocol.
func ProtocolOf(path string) (string, bool) {
	switch path {
	case PathOpenAIChat, PathOpenAIModels:
		return config.ProtocolOpenAI, true
	case PathAnthropicMsgs:
		return config.ProtocolAnthropic, true
	}
	return "", false
}

// Meta is the small slice of the body the gateway needs before forwarding.
type Meta struct {
	Model   string
	Stream  bool
	RawBody []byte
}

func ParseOpenAI(body []byte) (Meta, error) {
	m := Meta{RawBody: body}
	if !gjson.ValidBytes(body) {
		return m, ErrInvalidJSON
	}
	m.Model = gjson.GetBytes(body, "model").String()
	m.Stream = gjson.GetBytes(body, "stream").Bool()
	if m.Model == "" {
		return m, ErrMissingModel
	}
	return m, nil
}

func ParseAnthropic(body []byte) (Meta, error) {
	m := Meta{RawBody: body}
	if !gjson.ValidBytes(body) {
		return m, ErrInvalidJSON
	}
	m.Model = gjson.GetBytes(body, "model").String()
	m.Stream = gjson.GetBytes(body, "stream").Bool()
	if m.Model == "" {
		return m, ErrMissingModel
	}
	return m, nil
}

type Error struct{ Message string }

func (e *Error) Error() string { return e.Message }

var (
	ErrInvalidJSON  = &Error{Message: "request body is not valid JSON"}
	ErrMissingModel = &Error{Message: "request body must contain a model field"}
)

// WriteError encodes an error in the format of the ingress protocol, so that
// the official SDK of that protocol can parse it.
func WriteError(w http.ResponseWriter, protocol string, status int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if protocol == config.ProtocolAnthropic {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"type": "error",
			"error": map[string]any{
				"type":    errType,
				"message": message,
			},
		})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    errType,
			"param":   nil,
			"code":    nil,
		},
	})
}
