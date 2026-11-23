package quota

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// miniMaxRemains is the answer of the MiniMax token plan endpoint.
//
// The endpoint is not publicly documented, so every field is optional and the
// reader stays defensive.
type miniMaxRemains struct {
	// ModelRemains holds one entry per model group.
	ModelRemains []miniMaxModel `json:"model_remains"`
	BaseResp     struct {
		StatusCode int    `json:"status_code"`
		StatusMsg  string `json:"status_msg"`
	} `json:"base_resp"`
}

// miniMaxModel is the allowance of one model group. Only the "general" group
// is the coding plan; "video" and friends are billed separately.
type miniMaxModel struct {
	ModelName string `json:"model_name"`
	// CurrentIntervalRemainingPercent is the share of the 5 hour window that
	// is still free.
	CurrentIntervalRemainingPercent *int `json:"current_interval_remaining_percent"`
	// CurrentIntervalStatus is 1 while the window counts down, 2 once it is
	// exhausted and 3 when there is no 5 hour limit at all.
	CurrentIntervalStatus *int `json:"current_interval_status"`
	// CurrentWeeklyRemainingPercent is the share of the weekly window that
	// is still free.
	CurrentWeeklyRemainingPercent *int `json:"current_weekly_remaining_percent"`
	// CurrentWeeklyStatus is 1 while the window counts down, 2 once it is
	// exhausted and 3 when the plan has no weekly limit.
	CurrentWeeklyStatus *int `json:"current_weekly_status"`
	// EndTime is when the 5 hour window refills, in epoch milliseconds.
	EndTime *int64 `json:"end_time"`
	// WeeklyEndTime is when the weekly window refills, in epoch milliseconds.
	WeeklyEndTime *int64 `json:"weekly_end_time"`
}

// fetchMiniMax reads the MiniMax token plan allowance.
//
// The plan has a rolling 5 hour window and a weekly window; there is no
// monthly window, so only those two are reported.
func (c *Client) fetchMiniMax(ctx context.Context, url, apiKey string) ([]Window, error) {
	var body miniMaxRemains
	if err := c.getJSON(ctx, url, apiKey, &body); err != nil {
		return nil, err
	}
	if body.BaseResp.StatusCode != 0 {
		// The endpoint answers 200 with a business error: 1004 means the
		// authorization header did not carry a usable key, 2049 means the
		// key itself is invalid.
		switch body.BaseResp.StatusCode {
		case 1004, 2049:
			return nil, &HTTPError{
				Status: http.StatusUnauthorized,
				Detail: sanitize(body.BaseResp.StatusMsg, apiKey),
			}
		}
		return nil, fmt.Errorf("token plan rejected the key: %s", body.BaseResp.StatusMsg)
	}

	var general *miniMaxModel
	for i := range body.ModelRemains {
		if body.ModelRemains[i].ModelName == "general" {
			general = &body.ModelRemains[i]
			break
		}
	}
	if general == nil {
		return nil, fmt.Errorf("unexpected token plan response shape")
	}

	windows := make([]Window, 0, 2)
	// The 5 hour window is always present.
	if general.CurrentIntervalRemainingPercent != nil {
		window := Window{
			ID:      Window5H,
			Label:   "5 小时",
			UsedPct: clampPercent(100 - float64(*general.CurrentIntervalRemainingPercent)),
			Status:  StatusOK,
		}
		if general.CurrentIntervalStatus != nil && *general.CurrentIntervalStatus == 2 {
			window.Status = StatusExhausted
			window.UsedPct = 100
		}
		if general.EndTime != nil {
			window.ResetsAt = millisToRFC3339(*general.EndTime)
		}
		windows = append(windows, window)
	}
	// The weekly window is optional: status 3 means the plan has no weekly
	// limit and the reported percentage is a constant 100.
	if general.CurrentWeeklyStatus != nil &&
		(*general.CurrentWeeklyStatus == 1 || *general.CurrentWeeklyStatus == 2) &&
		general.CurrentWeeklyRemainingPercent != nil {
		window := Window{
			ID:      WindowWeekly,
			Label:   "每周",
			UsedPct: clampPercent(100 - float64(*general.CurrentWeeklyRemainingPercent)),
			Status:  StatusOK,
		}
		if *general.CurrentWeeklyStatus == 2 {
			window.Status = StatusExhausted
			window.UsedPct = 100
		}
		if general.WeeklyEndTime != nil {
			window.ResetsAt = millisToRFC3339(*general.WeeklyEndTime)
		}
		windows = append(windows, window)
	}
	if len(windows) == 0 {
		return nil, fmt.Errorf("unexpected token plan response shape")
	}
	return windows, nil
}

// millisToRFC3339 turns an epoch millisecond stamp into RFC 3339 in UTC.
func millisToRFC3339(ms int64) string {
	return time.UnixMilli(ms).UTC().Format(time.RFC3339)
}
