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
