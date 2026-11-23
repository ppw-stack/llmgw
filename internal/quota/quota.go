// Package quota reads the subscription quota of the built-in coding plan
// endpoints: the 5 hour, weekly and monthly allowance windows a vendor
// enforces.
//
// The upstream key only ever travels to the vendor hosts compiled into the
// preset catalog. It is never sent to a base URL a user typed in.
package quota

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/pw/llmgw/internal/preset"
)

// Window is one allowance window of a subscription.
type Window struct {
	// ID identifies the window: "5h", "weekly" or "monthly".
	ID string `json:"id"`
	// Label is the display name of the window.
	Label string `json:"label"`
	// UsedPct is the share of the window that is already spent, 0 to 100.
	UsedPct float64 `json:"used_pct"`
	// ResetsAt is when the window refills, RFC 3339 in UTC. It is empty
	// when the vendor does not report one.
	ResetsAt string `json:"resets_at,omitempty"`
	// Status is "ok" or "exhausted".
	Status string `json:"status,omitempty"`
}

// The window identifiers and their display names.
const (
	Window5H      = "5h"
	WindowWeekly  = "weekly"
	WindowMonthly = "monthly"
)

// The window status values.
const (
	StatusOK        = "ok"
	StatusExhausted = "exhausted"
)

// Report is the quota of one upstream instance.
type Report struct {
	InstanceID int64  `json:"instance_id"`
	Instance   string `json:"instance"`
	Preset     string `json:"preset,omitempty"`
	// Supported reports whether the instance's preset exposes a quota
	// endpoint at all.
	Supported bool `json:"supported"`
	// Windows are the allowance windows the vendor reported.
	Windows []Window `json:"windows"`
	// FetchedAt is when the vendor was asked, RFC 3339 in UTC.
	FetchedAt string `json:"fetched_at,omitempty"`
	// Error explains why the windows are missing.
	Error string `json:"error,omitempty"`
}

// Credential is the extra credential a vendor needs to read the quota of an
// instance. Volcengine, for example, publishes the plan windows of a Coding
// Plan only on its account control plane, which asks for an AccessKey pair
// instead of the inference key.
type Credential struct {
	AccessKeyID     string
	SecretAccessKey string
}

// Empty reports whether the credential carries nothing.
func (c Credential) Empty() bool {
	return c.AccessKeyID == "" && c.SecretAccessKey == ""
}

// Target is one instance whose quota can be queried.
type Target struct {
	ID       int64
	Name     string
	PresetID string
	APIKey   string
	// Credential is the extra credential the vendor needs, if any.
	Credential Credential
	// Err replaces the upstream query when the key cannot be read.
	Err string
}

// DefaultTTL is how long a quota report stays fresh.
const DefaultTTL = 60 * time.Second

// Service answers quota questions about instances. Reports are cached for a
// short while so a polling console does not hammer the vendor endpoints.
type Service struct {
	client *Client
	cache  *Cache
	ttl    time.Duration
}

// NewService returns a service with the default HTTP client and TTL.
func NewService() *Service {
	return NewServiceWithClient(NewClient(), DefaultTTL)
}

// NewServiceWithClient returns a service that uses the given client. Tests
// use it to point the quota endpoints at a local server.
func NewServiceWithClient(client *Client, ttl time.Duration) *Service {
	if client == nil {
		client = NewClient()
	}
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Service{client: client, cache: NewCache(), ttl: ttl}
}

// Report returns the quota of one instance.
//
// It never returns an error: a failure is reported inside the result, so one
// broken instance cannot hide the windows of the others.
func (s *Service) Report(ctx context.Context, target Target) Report {
	report := Report{
		InstanceID: target.ID,
		Instance:   target.Name,
		Preset:     target.PresetID,
		Windows:    []Window{},
	}
	p, ok := preset.Lookup(target.PresetID)
	if !ok || p.Quota == nil {
		return report
	}
	report.Supported = true
	if target.Err != "" {
		report.Error = target.Err
		return report
	}
	if strings.TrimSpace(target.APIKey) == "" && target.Credential.Empty() {
		report.Error = "未配置上游 API Key"
		return report
	}
	if cached, ok := s.cache.get(target.ID, s.ttl); ok {
		return cached
	}

	unlock := s.cache.lock(target.ID)
	defer unlock()
	// Another caller may have filled the cache while this one waited.
	if cached, ok := s.cache.get(target.ID, s.ttl); ok {
		return cached
	}

	windows, err := s.client.Fetch(ctx, *p.Quota, target.APIKey, target.Credential)
	report.FetchedAt = time.Now().UTC().Format(time.RFC3339)
	if err != nil {
		report.Error = describeError(err)
	} else {
		report.Windows = windows
	}
	s.cache.set(target.ID, report)
	return report
}

// describeError turns an upstream failure into a message the console shows.
func describeError(err error) string {
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		switch httpErr.Status {
		case http.StatusUnauthorized:
			return "上游 API Key 无效"
		case http.StatusForbidden:
			return "该 Key 没有对应的订阅"
		}
		return fmt.Sprintf("上游返回 %d：%s", httpErr.Status, httpErr.Detail)
	}
	if errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err) {
		return "查询上游额度超时"
	}
	return err.Error()
}
