package preset

import (
	"net/url"
	"strings"
	"testing"
)

func TestCatalogIsConsistent(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, p := range All() {
		if p.ID == "" || p.FamilyName == "" || p.DefaultName == "" {
			t.Fatalf("incomplete preset: %+v", p)
		}
		if seen[p.ID] {
			t.Fatalf("duplicate preset id %q", p.ID)
		}
		seen[p.ID] = true
		for _, raw := range []string{p.OpenAIBaseURL, p.AnthropicBaseURL} {
			u, err := url.Parse(raw)
			if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
				t.Fatalf("preset %q: invalid base url %q", p.ID, raw)
			}
		}
		if len(p.Models) == 0 {
			t.Fatalf("preset %q has no default models", p.ID)
		}
	}
}

func TestMiniMaxVariants(t *testing.T) {
	t.Parallel()
	global, ok := Lookup("minimax-mplan-global")
	if !ok || global.OpenAIBaseURL != "https://api.minimax.io/v1" {
		t.Fatalf("global preset = %+v", global)
	}
	cn, ok := Lookup("minimax-mplan-cn")
	if !ok {
		t.Fatal("missing minimax-mplan-cn preset")
	}
	if cn.OpenAIBaseURL != "https://api.minimax.cn/v1" ||
		cn.AnthropicBaseURL != "https://api.minimax.cn/anthropic" {
		t.Fatalf("cn preset urls = %+v", cn)
	}
	if cn.DefaultName != "minimax-cn" || cn.VariantName != "国内" {
		t.Fatalf("cn preset = %+v", cn)
	}
	if len(cn.Models) == 0 || cn.Models[0] != "MiniMax-M3.1-Flash" {
		t.Fatalf("cn preset models = %v", cn.Models)
	}
}

func TestLookupReturnsCopy(t *testing.T) {
	t.Parallel()
	p, ok := Lookup("minimax-mplan-global")
	if !ok {
		t.Fatal("expected the MiniMax preset to exist")
	}
	p.Models[0] = "mutated"
	again, _ := Lookup("minimax-mplan-global")
	if again.Models[0] == "mutated" {
		t.Fatal("Lookup must not expose the catalog's slice")
	}
	if _, ok := Lookup("nope"); ok {
		t.Fatal("unknown preset must not be found")
	}
}

func TestOpenCodeGoPreset(t *testing.T) {
	t.Parallel()
	p, ok := Lookup("opencode-go")
	if !ok {
		t.Fatal("missing opencode-go preset")
	}
	// The gateway appends /chat/completions to the OpenAI base and
	// /v1/messages to the Anthropic one.
	if p.OpenAIBaseURL != "https://opencode.ai/inference/go/openai/v1" {
		t.Fatalf("openai base url = %q", p.OpenAIBaseURL)
	}
	if p.AnthropicBaseURL != "https://opencode.ai/inference/go/anthropic" {
		t.Fatalf("anthropic base url = %q", p.AnthropicBaseURL)
	}
	if p.Label() != "OpenCode Go" {
		t.Fatalf("label = %q", p.Label())
	}
	if len(p.Models) == 0 {
		t.Fatal("no default models")
	}
}

func TestArkCodingPlanPreset(t *testing.T) {
	t.Parallel()
	p, ok := Lookup("ark-coding-plan-cn")
	if !ok {
		t.Fatal("missing ark-coding-plan-cn preset")
	}
	// The Coding Plan bases are the ones that draw from the subscription.
	if p.OpenAIBaseURL != "https://ark.cn-beijing.volces.com/api/coding/v3" {
		t.Fatalf("openai base url = %q", p.OpenAIBaseURL)
	}
	if p.AnthropicBaseURL != "https://ark.cn-beijing.volces.com/api/coding" {
		t.Fatalf("anthropic base url = %q", p.AnthropicBaseURL)
	}
	if p.Label() != "火山方舟（Coding Plan）" {
		t.Fatalf("label = %q", p.Label())
	}
	if p.DefaultName != "ark-coding" {
		t.Fatalf("default name = %q", p.DefaultName)
	}
	if len(p.Models) == 0 {
		t.Fatal("no default models")
	}
}

func TestLabelsOmitEmptyVariant(t *testing.T) {
	t.Parallel()
	global, _ := Lookup("minimax-mplan-global")
	if global.Label() != "MiniMax 编码套餐（国际）" {
		t.Fatalf("label = %q", global.Label())
	}
	goPreset, _ := Lookup("opencode-go")
	if strings.Contains(goPreset.Label(), "（）") {
		t.Fatalf("empty variant must not add parentheses: %q", goPreset.Label())
	}
}

func TestQuotaDeclarations(t *testing.T) {
	t.Parallel()
	cases := []struct {
		id   string
		kind string
		url  string
	}{
		{"opencode-go", "opencode-go", "https://opencode.ai/zen/go/v1/usage"},
		{"minimax-mplan-cn", "minimax", "https://api.minimax.cn/v1/token_plan/remains"},
		{"minimax-mplan-global", "minimax", "https://www.minimax.io/v1/token_plan/remains"},
	}
	for _, tc := range cases {
		p, ok := Lookup(tc.id)
		if !ok {
			t.Fatalf("missing preset %q", tc.id)
		}
		if p.Quota == nil {
			t.Fatalf("preset %q has no quota declaration", tc.id)
		}
		if p.Quota.Kind != tc.kind || p.Quota.URL != tc.url {
			t.Fatalf("preset %q quota = %+v", tc.id, p.Quota)
		}
		u, err := url.Parse(p.Quota.URL)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			t.Fatalf("preset %q: invalid quota url %q", tc.id, p.Quota.URL)
		}
	}
	// Ark publishes the plan windows on its account control plane, which
	// needs a signed request instead of a plain URL.
	ark, _ := Lookup("ark-coding-plan-cn")
	if ark.Quota == nil {
		t.Fatal("the ark preset has no quota declaration")
	}
	if ark.Quota.Kind != "ark" || ark.Quota.URL != "" {
		t.Fatalf("ark quota = %+v", ark.Quota)
	}
}

func TestLookupReturnsCopyOfQuota(t *testing.T) {
	t.Parallel()
	p, ok := Lookup("opencode-go")
	if !ok || p.Quota == nil {
		t.Fatal("expected the opencode-go preset with a quota declaration")
	}
	p.Quota.URL = "https://evil.test/usage"
	again, _ := Lookup("opencode-go")
	if again.Quota == nil || again.Quota.URL != "https://opencode.ai/zen/go/v1/usage" {
		t.Fatalf("the catalog was mutated: %+v", again.Quota)
	}
}
