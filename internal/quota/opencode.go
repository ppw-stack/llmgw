package quota

import (
	"context"
	"fmt"
)

// openCodeUsage is the answer of the Go usage endpoint.
//
// The route is a first party endpoint without published documentation, so
// every field is optional and the reader stays defensive: a missing window
// is skipped instead of failing the whole query.
type openCodeUsage struct {
	Usage struct {
		Rolling *openCodeWindow `json:"rolling"`
		Weekly  *openCodeWindow `json:"weekly"`
		Monthly *openCodeWindow `json:"monthly"`
	} `json:"usage"`
}

type openCodeWindow struct {
	Status string `json:"status"`
	// Percent is the share of the window already spent, 0 to 100.
	Percent  *float64 `json:"percent"`
	ResetsAt string   `json:"resetsAt"`
}

// fetchOpenCodeGo reads the Go allowance windows: the rolling 5 hour, the
// weekly and the monthly budget of the subscription.
func (c *Client) fetchOpenCodeGo(ctx context.Context, url, apiKey string) ([]Window, error) {
	var body openCodeUsage
	if err := c.getJSON(ctx, url, apiKey, &body); err != nil {
		return nil, err
	}
	windows := make([]Window, 0, 3)
	for _, source := range []struct {
		id    string
		label string
		raw   *openCodeWindow
	}{
		{Window5H, "5 小时", body.Usage.Rolling},
		{WindowWeekly, "每周", body.Usage.Weekly},
		{WindowMonthly, "每月", body.Usage.Monthly},
	} {
		if source.raw == nil || source.raw.Percent == nil {
			continue
		}
		window := Window{
			ID:      source.id,
			Label:   source.label,
			UsedPct: clampPercent(*source.raw.Percent),
			Status:  StatusOK,
		}
		if source.raw.Status == "rate-limited" {
			window.Status = StatusExhausted
			window.UsedPct = 100
		}
		// An untouched window reports a placeholder reset time (now plus the
		// window length) even though the window already ended, so the
		// countdown is only meaningful once something was spent.
		if window.UsedPct > 0 && source.raw.ResetsAt != "" {
			window.ResetsAt = source.raw.ResetsAt
		}
		windows = append(windows, window)
	}
	if len(windows) == 0 {
		return nil, fmt.Errorf("unexpected usage response shape")
	}
	return windows, nil
}
