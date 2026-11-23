// Package preset holds the built-in provider templates ("endpoints") that
// ship with the gateway. They are compiled into the binary so a deployment
// never has to hand-fill upstream base URLs and picks up fixes by upgrading.
package preset

// Preset describes one built-in coding-plan provider. A custom provider is any
// provider that does not reference a preset.
type Preset struct {
	ID               string   `json:"id"`
	Family           string   `json:"family"`
	FamilyName       string   `json:"family_name"`
	VariantName      string   `json:"variant_name"`
	DefaultName      string   `json:"default_name"`
	DocsURL          string   `json:"docs_url"`
	OpenAIBaseURL    string   `json:"openai_base_url"`
	AnthropicBaseURL string   `json:"anthropic_base_url"`
	Models           []string `json:"models"`
}

var all = []Preset{
	{
		ID:               "minimax-mplan-global",
		Family:           "minimax-mplan",
		FamilyName:       "MiniMax 编码套餐",
		VariantName:      "国际",
		DefaultName:      "minimax",
		DocsURL:          "https://platform.minimax.io/docs/m-plan/other-tools",
		OpenAIBaseURL:    "https://api.minimax.io/v1",
		AnthropicBaseURL: "https://api.minimax.io/anthropic",
		Models:           []string{"MiniMax-M3.1-Flash"},
	},
	{
		ID:               "minimax-mplan-cn",
		Family:           "minimax-mplan",
		FamilyName:       "MiniMax 编码套餐",
		VariantName:      "国内",
		DefaultName:      "minimax-cn",
		DocsURL:          "https://platform.minimaxi.com/docs/m-plan/other-tools",
		OpenAIBaseURL:    "https://api.minimax.cn/v1",
		AnthropicBaseURL: "https://api.minimax.cn/anthropic",
		Models:           []string{"MiniMax-M3.1-Flash"},
	},
	// OpenCode Go is the monthly subscription of the OpenCode console. The
	// gateway appends /chat/completions to the OpenAI base and /v1/messages
	// to the Anthropic one, so the two bases differ by one path segment.
	//
	// Go rejects requests without a stable x-opencode-session header with
	// "400 MissingSessionID". The gateway forwards the client's headers
	// untouched, so coding agents that carry a session header of their own
	// (Claude Code, Codex, ZCode, Pi, Hermes) work as they are.
	{
		ID:               "opencode-go",
		Family:           "opencode-go",
		FamilyName:       "OpenCode Go",
		DefaultName:      "opencode-go",
		DocsURL:          "https://opencode.ai/docs/console/go/",
		OpenAIBaseURL:    "https://opencode.ai/inference/go/openai/v1",
		AnthropicBaseURL: "https://opencode.ai/inference/go/anthropic",
		Models: []string{
			"glm-5.3", "glm-5.3-flash", "kimi-k3", "kimi-k3.1-pro",
			"deepseek-v4-pro", "mimo-v2.6-pro", "minimax-m3.1-flash", "longcat-2.5",
		},
	},
	// Volcengine Ark Coding Plan. The subscription is only served on the
	// /api/coding bases: the regular /api/v3 base does not draw from the
	// plan and is billed separately.
	{
		ID:               "ark-coding-plan-cn",
		Family:           "ark",
		FamilyName:       "火山方舟",
		VariantName:      "Coding Plan",
		DefaultName:      "ark-coding",
		DocsURL:          "https://ark.volcengine.com/region:cn-beijing/docs/coding-plan-personal-plan-overview",
		OpenAIBaseURL:    "https://ark.cn-beijing.volces.com/api/coding/v3",
		AnthropicBaseURL: "https://ark.cn-beijing.volces.com/api/coding",
		Models: []string{
			"ark-code-latest", "doubao-seed-2.1-pro", "doubao-seed-evolving",
			"glm-5.3", "kimi-k3.1-pro",
		},
	},
}

// Label is the display name of the preset: the family name, with the variant
// appended only when the family has one.
func (p Preset) Label() string {
	if p.VariantName == "" {
		return p.FamilyName
	}
	return p.FamilyName + "（" + p.VariantName + "）"
}

// All returns a copy of the preset catalog.
func All() []Preset {
	out := make([]Preset, len(all))
	for i, p := range all {
		out[i] = p
		out[i].Models = append([]string(nil), p.Models...)
	}
	return out
}

// Lookup finds a preset by id.
func Lookup(id string) (Preset, bool) {
	for _, p := range all {
		if p.ID == id {
			p.Models = append([]string(nil), p.Models...)
			return p, true
		}
	}
	return Preset{}, false
}
