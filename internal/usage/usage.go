package usage

import (
	"strings"

	"github.com/pw/llmgw/internal/config"
)

// Usage is the token accounting for one request.
type Usage struct {
	PromptTokens     int64
	CompletionTokens int64
	CacheReadTokens  int64
	CacheWriteTokens int64
	Estimated        bool
}

// Cost estimates the USD cost using the configured price table.
// Matching is exact first, then longest model-name prefix.
func Cost(pricing map[string]config.Price, model string, u Usage) float64 {
	p, ok := lookup(pricing, model)
	if !ok {
		return 0
	}
	const million = 1_000_000.0
	cost := float64(u.PromptTokens) * p.InputPerMTok / million
	cost += float64(u.CompletionTokens) * p.OutputPerMTok / million
	cost += float64(u.CacheReadTokens) * p.CacheReadPerMTok / million
	return cost
}

func lookup(pricing map[string]config.Price, model string) (config.Price, bool) {
	if p, ok := pricing[model]; ok {
		return p, true
	}
	best := ""
	for name := range pricing {
		if strings.HasPrefix(model, name) && len(name) > len(best) {
			best = name
		}
	}
	if best == "" {
		return config.Price{}, false
	}
	return pricing[best], true
}
