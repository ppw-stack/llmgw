package preset

import (
	"strings"

	"github.com/pw/llmgw/internal/modelmeta"
)

// modelCatalog is the compiled-in fallback metadata of the upstream models the
// built-in presets serve. It is used only when the operator did not declare
// metadata on the logical model, and only when the candidate belongs to an
// instance created from a built-in preset.
//
// The numbers come from the vendors' own documentation, verified on
// 2026-11-24. Vendors revise them without notice; update the entry and the
// table in docs/operations.md together. When a vendor publishes the context
// window but not the maximum output, the output stays zero rather than
// guessing.
//
// Keys are lower-cased upstream model names.
var modelCatalog = map[string]modelmeta.Metadata{
	// https://docs.bigmodel.cn/cn/guide/models/text/glm-5.3
	"glm-5.3": {
		ContextLength:   1048576,
		MaxOutputTokens: 131072,
		Capabilities:    []string{modelmeta.CapTools, modelmeta.CapReasoning, modelmeta.CapJSON, modelmeta.CapCache},
	},
	// https://docs.bigmodel.cn/cn/guide/models/vlm/glm-5.3-flash
	"glm-5.3-flash": {
		ContextLength:   1048576,
		MaxOutputTokens: 131072,
		Capabilities: []string{modelmeta.CapTools, modelmeta.CapReasoning, modelmeta.CapJSON,
			modelmeta.CapCache, modelmeta.CapVision, modelmeta.CapVideo, modelmeta.CapDoc},
	},
	// https://platform.kimi.ai/docs/api/models-overview
	// The API defaults max_completion_tokens to 131072 and accepts up to
	// 1048576, so the advertised default is the useful upper bound.
	"kimi-k3": {
		ContextLength:   1048576,
		MaxOutputTokens: 131072,
		Capabilities: []string{modelmeta.CapTools, modelmeta.CapReasoning, modelmeta.CapJSON,
			modelmeta.CapCache, modelmeta.CapVision, modelmeta.CapVideo},
	},
	"kimi-k3.1-pro": {
		ContextLength:   1048576,
		MaxOutputTokens: 131072,
		Capabilities: []string{modelmeta.CapTools, modelmeta.CapReasoning, modelmeta.CapJSON,
			modelmeta.CapCache, modelmeta.CapVision, modelmeta.CapVideo},
	},
	// https://api-docs.deepseek.com/quick_start/pricing.md
	"deepseek-v4-pro": {
		ContextLength:   1048576,
		MaxOutputTokens: 393216,
		Capabilities:    []string{modelmeta.CapTools, modelmeta.CapReasoning, modelmeta.CapJSON, modelmeta.CapCache},
	},
	// https://platform.minimax.io/docs/guides/text-generation
	// The vendor publishes the 1M context window but not the output limit;
	// 512000 is the figure reported by models.dev.
	"minimax-m3.1-flash": {
		ContextLength:   1000000,
		MaxOutputTokens: 512000,
		Capabilities: []string{modelmeta.CapTools, modelmeta.CapReasoning,
			modelmeta.CapVision, modelmeta.CapVideo},
	},
	// https://mimo.mi.com/models/zh-CN/mimo-v2.6-pro
	"mimo-v2.6-pro": {
		ContextLength:   1048576,
		MaxOutputTokens: 131072,
		Capabilities: []string{modelmeta.CapTools, modelmeta.CapReasoning, modelmeta.CapJSON,
			modelmeta.CapCache, modelmeta.CapVision, modelmeta.CapVideo, modelmeta.CapAudio, modelmeta.CapSearch},
	},
	// https://longcat.chat/platform/docs/zh/api/model-params
	"longcat-2.5": {
		ContextLength:   1048576,
		MaxOutputTokens: 262144,
		Capabilities: []string{modelmeta.CapTools, modelmeta.CapReasoning,
			modelmeta.CapVision, modelmeta.CapVideo},
	},
	// https://ai.volcengine.com/model
	// The listed 2.1-pro build (260628) has a 256K window.
	"doubao-seed-2.1-pro": {
		ContextLength:   262144,
		MaxOutputTokens: 262144,
		Capabilities: []string{modelmeta.CapTools, modelmeta.CapReasoning, modelmeta.CapJSON,
			modelmeta.CapCache, modelmeta.CapVision, modelmeta.CapVideo},
	},
	// https://docs.volcengine.com/docs/ark/latest-model?lang=zh
	"doubao-seed-evolving": {
		ContextLength:   1048576,
		MaxOutputTokens: 262144,
		Capabilities: []string{modelmeta.CapTools, modelmeta.CapReasoning, modelmeta.CapJSON,
			modelmeta.CapCache, modelmeta.CapDoc, modelmeta.CapVision, modelmeta.CapVideo},
	},
	// ark-code-latest is intentionally absent: it is a control-plane alias
	// whose real model, and therefore its limits, change from the console.
}

// ModelMetadata returns the compiled-in metadata of one upstream model. The
// lookup is case-insensitive because presets and aliases disagree on case.
func ModelMetadata(upstream string) (modelmeta.Metadata, bool) {
	meta, ok := modelCatalog[strings.ToLower(strings.TrimSpace(upstream))]
	if !ok {
		return modelmeta.Metadata{}, false
	}
	return meta.Clone(), true
}
