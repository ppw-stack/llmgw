// Package alert dispatches lightweight asynchronous webhook notifications for
// operational events such as circuit-breaker trips and upstream auth failures.
//
// Delivery runs on a single background worker backed by a bounded queue so an
// unreachable webhook can never block the data plane.
package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	EventBreakerOpen       = "breaker_open"
	EventUpstreamAuthError = "upstream_auth_error"
)

const (
	DefaultCooldown = 5 * time.Minute
	queueCapacity   = 64
	sendTimeout     = 5 * time.Second
)

// Event is the JSON payload posted to the configured webhook URL. It includes
// a top-level "text" field for out-of-the-box compatibility with Slack /
// generic chat webhooks alongside structured fields.
type Event struct {
	Type      string `json:"type"`
	Severity  string `json:"severity"`
	Instance  string `json:"instance"`
	Status    int    `json:"status,omitempty"`
	Title     string `json:"title"`
	Message   string `json:"message"`
	Text      string `json:"text"`
	Timestamp string `json:"timestamp"`
}

// Notifier deduplicates and asynchronously sends alert events to a webhook.
type Notifier struct {
	log    *slog.Logger
	client *http.Client

	mu       sync.Mutex
	webhook  string
	cooldown time.Duration
	lastSent map[string]time.Time
	now      func() time.Time

	ch   chan Event
	done chan struct{}
	once sync.Once
}

// New creates a Notifier and starts its background delivery goroutine.
func New(webhookURL string, cooldown time.Duration, log *slog.Logger) *Notifier {
	if log == nil {
		log = slog.Default()
	}
	if cooldown <= 0 {
		cooldown = DefaultCooldown
	}
	n := &Notifier{
		log: log,
		client: &http.Client{
			Timeout: sendTimeout,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		webhook:  strings.TrimSpace(webhookURL),
		cooldown: cooldown,
		lastSent: make(map[string]time.Time),
		now:      time.Now,
		ch:       make(chan Event, queueCapacity),
		done:     make(chan struct{}),
	}
	go n.loop()
	return n
}

// SetWebhook updates the webhook URL and deduplication cooldown at runtime.
// Passing an empty webhookURL disables delivery.
func (n *Notifier) SetWebhook(webhookURL string, cooldown time.Duration) {
	if n == nil {
		return
	}
	if cooldown <= 0 {
		cooldown = DefaultCooldown
	}
	n.mu.Lock()
	n.webhook = strings.TrimSpace(webhookURL)
	n.cooldown = cooldown
	n.mu.Unlock()
}

// Emit enqueues an event unless the webhook is disabled or the same
// (type, instance) key was already sent within the cooldown window.
func (n *Notifier) Emit(ev Event) {
	if n == nil {
		return
	}
	n.mu.Lock()
	url := n.webhook
	if url == "" {
		n.mu.Unlock()
		return
	}
	key := ev.Type + ":" + ev.Instance
	now := n.now()
	if prev, ok := n.lastSent[key]; ok && now.Sub(prev) < n.cooldown {
		n.mu.Unlock()
		return
	}
	n.lastSent[key] = now
	n.mu.Unlock()

	if ev.Timestamp == "" {
		ev.Timestamp = now.UTC().Format(time.RFC3339)
	}
	if ev.Text == "" {
		ev.Text = "[" + ev.Title + "] " + ev.Message
	}

	select {
	case n.ch <- ev:
	default:
		n.log.Warn("alert webhook queue full; dropping event", "type", ev.Type, "instance", ev.Instance)
	}
}

// Close stops the background worker after draining queued events.
func (n *Notifier) Close() {
	if n == nil {
		return
	}
	n.once.Do(func() {
		close(n.ch)
		<-n.done
	})
}

func (n *Notifier) loop() {
	defer close(n.done)
	for ev := range n.ch {
		n.deliver(ev)
	}
}

func (n *Notifier) deliver(ev Event) {
	n.mu.Lock()
	target := n.webhook
	n.mu.Unlock()
	if target == "" {
		return
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		n.log.Warn("build alert webhook request failed", "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "llmgw-alert/1.0")
	resp, err := n.client.Do(req)
	if err != nil {
		n.log.Warn("deliver alert webhook failed", "type", ev.Type, "instance", ev.Instance, "err", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		n.log.Warn("alert webhook returned non-2xx", "type", ev.Type, "instance", ev.Instance, "status", resp.StatusCode)
	}
}
