package transport

import (
	"bytes"

	"github.com/pw/llmgw/internal/config"
	"github.com/tidwall/gjson"
)

type usageAcc struct {
	prompt     int64
	completion int64
	cacheRead  int64
	cacheWrite int64
	seen       bool
	estimated  bool
}

// observeLine inspects one SSE line and updates usage when the protocol
// reports it. It never mutates the bytes that get forwarded.
func (u *usageAcc) observeLine(protocol string, line []byte) {
	trimmed := bytes.TrimSpace(line)
	if !bytes.HasPrefix(trimmed, []byte("data:")) {
		return
	}
	payload := bytes.TrimSpace(trimmed[len("data:"):])
	if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
		return
	}
	switch protocol {
	case config.ProtocolAnthropic:
		if r := gjson.GetBytes(payload, "message.usage"); r.Exists() && r.IsObject() {
			u.prompt = r.Get("input_tokens").Int()
			u.cacheRead = r.Get("cache_read_input_tokens").Int()
			u.cacheWrite = r.Get("cache_creation_input_tokens").Int()
			u.seen = true
		}
		if gjson.GetBytes(payload, "type").String() == "message_delta" {
			if r := gjson.GetBytes(payload, "usage"); r.Exists() && r.IsObject() {
				// Standard Anthropic reports input_tokens at message_start, but
				// some compatible upstreams (MiniMax) only report it here.
				if v := r.Get("input_tokens").Int(); v > 0 {
					u.prompt = v
				}
				u.completion = r.Get("output_tokens").Int()
				if v := r.Get("cache_read_input_tokens").Int(); v > 0 {
					u.cacheRead = v
				}
				u.seen = true
			}
		}
	default:
		if r := gjson.GetBytes(payload, "usage"); r.Exists() && r.IsObject() {
			u.prompt = r.Get("prompt_tokens").Int()
			u.completion = r.Get("completion_tokens").Int()
			u.cacheRead = openAICacheRead(r)
			u.seen = true
		}
	}
}

// openAICacheRead reads the cached prompt tokens from the two usage shapes
// OpenAI-compatible upstreams use: the OpenAI nested counter and the
// DeepSeek-style flat counter.
func openAICacheRead(usage gjson.Result) int64 {
	if v := usage.Get("prompt_tokens_details.cached_tokens").Int(); v > 0 {
		return v
	}
	return usage.Get("prompt_cache_hit_tokens").Int()
}

// observeBody extracts usage from a non-streaming JSON response.
func (u *usageAcc) observeBody(protocol string, body []byte) {
	if len(body) == 0 {
		return
	}
	switch protocol {
	case config.ProtocolAnthropic:
		if r := gjson.GetBytes(body, "usage"); r.Exists() && r.IsObject() {
			u.prompt = r.Get("input_tokens").Int()
			u.completion = r.Get("output_tokens").Int()
			u.cacheRead = r.Get("cache_read_input_tokens").Int()
			u.cacheWrite = r.Get("cache_creation_input_tokens").Int()
			u.seen = true
		}
	default:
		if r := gjson.GetBytes(body, "usage"); r.Exists() && r.IsObject() {
			u.prompt = r.Get("prompt_tokens").Int()
			u.completion = r.Get("completion_tokens").Int()
			u.cacheRead = openAICacheRead(r)
			u.seen = true
		}
	}
}
