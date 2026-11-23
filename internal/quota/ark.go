package quota

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// The Volcengine control plane. The plan windows of a Coding Plan or an
// Agent Plan live here, not on the inference host, and the endpoint only
// accepts a signed request.
const (
	arkOpenAPIHost    = "open.volcengineapi.com"
	arkOpenAPIService = "ark"
	arkOpenAPIVersion = "2024-01-01"
	arkOpenAPIRegion  = "cn-beijing"
	arkContentType    = "application/json; charset=utf-8"
	arkSignedHeaders  = "host;x-date;x-content-sha256;content-type"
)

// The actions that report the plan windows. The Coding Plan one answers with
// percentages, the Agent Plan one with absolute values.
const (
	arkActionCodingPlan = "GetCodingPlanUsage"
	arkActionAgentPlan  = "GetAFPUsage"
)

// arkQuotaResponse is the answer of the control plane.
type arkQuotaResponse struct {
	ResponseMetadata struct {
		Error *struct {
			Code    string `json:"Code"`
			Message string `json:"Message"`
		} `json:"Error"`
	} `json:"ResponseMetadata"`
	Result arkQuotaResult `json:"Result"`
}

// arkQuotaResult is the payload of a control plane answer.
type arkQuotaResult struct {
	Status      string           `json:"Status"`
	UpdateTime  int64            `json:"UpdateTimestamp"`
	QuotaUsage  []arkQuotaWindow `json:"QuotaUsage"`
	AFPFiveHour arkAgentWindow   `json:"AFPFiveHour"`
	AFPWeekly   arkAgentWindow   `json:"AFPWeekly"`
	AFPMonthly  arkAgentWindow   `json:"AFPMonthly"`
}

// arkQuotaWindow is one Coding Plan window.
type arkQuotaWindow struct {
	// Level is "session" (the rolling 5 hour window), "weekly" or "monthly".
	Level string `json:"Level"`
	// Percent is the share of the window that is already spent.
	Percent float64 `json:"Percent"`
	// ResetTimestamp is when the window refills, in epoch seconds.
	ResetTimestamp int64 `json:"ResetTimestamp"`
}

// arkAgentWindow is one Agent Plan window.
type arkAgentWindow struct {
	Quota     float64 `json:"Quota"`
	Used      float64 `json:"Used"`
	ResetTime int64   `json:"ResetTime"`
}

// fetchArk reads the plan windows of a Volcengine subscription.
//
// The credential is an AccessKey pair: the request is signed with the
// volcano variant of SigV4, which differs from AWS in three places - the
// canonical headers keep a fixed order, the algorithm string has no AWS4
// prefix and the credential scope ends in "request".
func (c *Client) fetchArk(ctx context.Context, cred Credential) ([]Window, error) {
	if cred.AccessKeyID == "" || cred.SecretAccessKey == "" {
		return nil, fmt.Errorf("the quota credential is incomplete")
	}
	host := c.ArkHost
	if host == "" {
		host = arkOpenAPIHost
	}
	// The Coding Plan is the common case; the Agent Plan answers with
	// absolute values instead of percentages.
	windows, err := c.arkAction(ctx, host, cred, arkActionCodingPlan)
	if err != nil {
		return nil, err
	}
	if len(windows) > 0 {
		return windows, nil
	}
	return c.arkAction(ctx, host, cred, arkActionAgentPlan)
}

// arkAction calls one control plane action and turns its answer into windows.
func (c *Client) arkAction(ctx context.Context, host string, cred Credential, action string) ([]Window, error) {
	endpoint, headers, err := signArkRequest(host, cred, action, arkOpenAPIRegion, time.Now())
	if err != nil {
		return nil, err
	}
	var body arkQuotaResponse
	if err := c.postJSON(ctx, endpoint, headers, &body); err != nil {
		return nil, err
	}
	if body.ResponseMetadata.Error != nil {
		code := body.ResponseMetadata.Error.Code
		message := sanitize(body.ResponseMetadata.Error.Message, cred.SecretAccessKey)
		switch code {
		case "InvalidAccessKey", "InvalidAuthorization", "SignatureDoesNotMatch":
			return nil, &HTTPError{Status: http.StatusUnauthorized, Detail: message}
		case "AccessDenied", "Forbidden":
			return nil, &HTTPError{Status: http.StatusForbidden, Detail: message}
		}
		return nil, fmt.Errorf("volcengine %s failed: %s: %s", action, code, message)
	}
	return arkWindows(body.Result), nil
}

// arkWindows turns the result of a control plane action into windows.
func arkWindows(result arkQuotaResult) []Window {
	windows := make([]Window, 0, 3)
	for _, w := range result.QuotaUsage {
		id, label, ok := arkWindowName(w.Level)
		if !ok {
			continue
		}
		window := Window{
			ID:      id,
			Label:   label,
			UsedPct: clampPercent(w.Percent),
			Status:  StatusOK,
		}
		if window.UsedPct >= 100 {
			window.Status = StatusExhausted
		}
		if w.ResetTimestamp > 0 {
			window.ResetsAt = time.Unix(w.ResetTimestamp, 0).UTC().Format(time.RFC3339)
		}
		windows = append(windows, window)
	}
	if len(windows) > 0 {
		return windows
	}
	// The Agent Plan answers with absolute values instead of percentages.
	for _, w := range []struct {
		id    string
		label string
		raw   arkAgentWindow
	}{
		{Window5H, "5 小时", result.AFPFiveHour},
		{WindowWeekly, "每周", result.AFPWeekly},
		{WindowMonthly, "每月", result.AFPMonthly},
	} {
		if w.raw.Quota <= 0 {
			continue
		}
		window := Window{
			ID:      w.id,
			Label:   w.label,
			UsedPct: clampPercent(w.raw.Used / w.raw.Quota * 100),
			Status:  StatusOK,
		}
		if window.UsedPct >= 100 {
			window.Status = StatusExhausted
		}
		if w.raw.ResetTime > 0 {
			window.ResetsAt = time.Unix(w.raw.ResetTime, 0).UTC().Format(time.RFC3339)
		}
		windows = append(windows, window)
	}
	return windows
}

// arkWindowName maps the level of a Coding Plan window to a window id.
func arkWindowName(level string) (string, string, bool) {
	switch strings.ToLower(level) {
	case "session", "5h", "fivehour", "five_hour", "rolling_5h":
		return Window5H, "5 小时", true
	case "weekly", "week", "7d":
		return WindowWeekly, "每周", true
	case "monthly", "month":
		return WindowMonthly, "每月", true
	default:
		return "", "", false
	}
}

// signArkRequest builds the signed control plane request.
//
// The canonical headers keep a fixed order instead of the alphabetical one
// SigV4 uses, the algorithm string is "HMAC-SHA256" without the AWS4 prefix
// and the signing key is derived from the secret without a prefix. The body
// is always empty for these actions.
func signArkRequest(host string, cred Credential, action, region string, now time.Time) (string, map[string]string, error) {
	if cred.AccessKeyID == "" || cred.SecretAccessKey == "" {
		return "", nil, fmt.Errorf("the quota credential is incomplete")
	}
	xDate := now.UTC().Format("20060102T150405Z")
	shortDate := now.UTC().Format("20060102")
	payloadHash := sha256Hex(nil)

	query := url.Values{}
	query.Set("Action", action)
	query.Set("Region", region)
	query.Set("Version", arkOpenAPIVersion)
	// The canonical query string is sorted by key, like SigV4 does.
	canonicalQuery := query.Encode()

	canonicalHeaders := "host:" + host + "\n" +
		"x-date:" + xDate + "\n" +
		"x-content-sha256:" + payloadHash + "\n" +
		"content-type:" + arkContentType + "\n"
	canonicalRequest := "POST\n/\n" + canonicalQuery + "\n" + canonicalHeaders + "\n" +
		arkSignedHeaders + "\n" + payloadHash
	scope := shortDate + "/" + region + "/" + arkOpenAPIService + "/request"
	stringToSign := "HMAC-SHA256\n" + xDate + "\n" + scope + "\n" + sha256Hex([]byte(canonicalRequest))

	kDate := hmacSHA256([]byte(cred.SecretAccessKey), []byte(shortDate))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(arkOpenAPIService))
	kSigning := hmacSHA256(kService, []byte("request"))
	signature := hex.EncodeToString(hmacSHA256(kSigning, []byte(stringToSign)))

	authorization := "HMAC-SHA256 Credential=" + cred.AccessKeyID + "/" + scope +
		", SignedHeaders=" + arkSignedHeaders + ", Signature=" + signature
	headers := map[string]string{
		"Authorization":    authorization,
		"X-Date":           xDate,
		"X-Content-Sha256": payloadHash,
		"Content-Type":     arkContentType,
		"Accept":           "application/json",
	}
	return "https://" + host + "/?" + canonicalQuery, headers, nil
}

// sha256Hex is the hex digest of the given bytes.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// hmacSHA256 signs the data with the key.
func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}
