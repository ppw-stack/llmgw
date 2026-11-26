// Package router compiles immutable routing snapshots and resolves models to
// ordered upstream candidates.
package router

import (
	"fmt"
	"net/url"
	"sort"
	"sync/atomic"
	"time"

	"github.com/pw/llmgw/internal/config"
	"github.com/pw/llmgw/internal/modelmeta"
)

// Key identifies a routing entry: a model name inside one protocol.
type Key struct {
	Protocol string
	Model    string
}

// Target is one resolved upstream candidate for a request. A model may have
// several targets: they are ordered by priority and tried one after another
// until one of them succeeds.
type Target struct {
	Instance      string
	Model         string
	Priority      int
	Endpoint      string
	Protocol      string
	BaseURL       string
	Version       string
	UpstreamModel string
	Key           string
	Timeout       time.Duration
	MaxFails      int
	Cooldown      time.Duration
	// Windows are the compiled time windows of the candidate. They decide
	// the effective priority at the moment of the request.
	Windows []config.EffectiveWindow
	// AllowPrivate reports whether the instance may dial a loopback or
	// private address.
	AllowPrivate bool
	// SessionHeader is the header the upstream needs for session affinity.
	SessionHeader string
	// FailoverStatuses are the instance's extra retryable status codes.
	FailoverStatuses []int
	// ProxyURL is the optional outbound proxy configured on this instance.
	ProxyURL *url.URL
}

// ModelInfo is one model visible on a protocol endpoint together with the
// metadata advertised to clients.
type ModelInfo struct {
	Name     string
	Metadata modelmeta.Metadata
}

// DefaultQuotaDegradePct is the window usage percentage at or above which an
// instance is automatically demoted behind healthy candidates.
const DefaultQuotaDegradePct = 98.0

// QuotaProvider supplies cached subscription quota state for an upstream
// instance without performing network or database I/O on the request path.
type QuotaProvider interface {
	QuotaState(instance string) (maxUsedPct float64, exhausted bool, ok bool)
}

type quotaProviderBox struct {
	provider QuotaProvider
}

// ResolveOptions carries optional per-request context used by routing
// strategies such as sticky session affinity.
type ResolveOptions struct {
	SessionKey string
}

// Snapshot is an immutable routing table swapped atomically on reload.
type Snapshot struct {
	Version          uint64
	Config           *config.Config
	Index            map[Key][]Target
	ModelsByProtocol map[string][]string
	// InfosByProtocol carries the same models with their effective metadata.
	InfosByProtocol map[string][]ModelInfo
	// ByModel holds the candidate chain of every model, independent of the
	// protocol. The console uses it to show which candidate is in charge
	// right now.
	ByModel map[string][]Target
	// Strategies maps each model name to its configured routing strategy.
	Strategies map[string]string
	// Location is the timezone every time window is evaluated in.
	Location *time.Location
}

// Router holds the current snapshot. Reads are lock-free.
type Router struct {
	snap      atomic.Pointer[Snapshot]
	version   atomic.Uint64
	quota     atomic.Pointer[quotaProviderBox]
	rrCounter atomic.Uint64
}

func New() *Router { return &Router{} }

// SetQuotaProvider attaches an optional quota provider used to demote
// exhausted instances and rank least_used candidates. Passing nil disables
// quota-aware routing.
func (r *Router) SetQuotaProvider(p QuotaProvider) {
	if p == nil {
		r.quota.Store(nil)
		return
	}
	r.quota.Store(&quotaProviderBox{provider: p})
}

func (r *Router) quotaProvider() QuotaProvider {
	if box := r.quota.Load(); box != nil {
		return box.provider
	}
	return nil
}

// Reload validates cfg and atomically installs a new snapshot.
// On error the previous snapshot stays active.
func (r *Router) Reload(cfg *config.Config) error {
	snap, err := Build(cfg)
	if err != nil {
		return err
	}
	snap.Version = r.version.Add(1)
	r.snap.Store(snap)
	return nil
}

// Build compiles a snapshot without installing it.
func Build(cfg *config.Config) (*Snapshot, error) {
	eff, err := cfg.Effective()
	if err != nil {
		return nil, err
	}
	idx := make(map[Key][]Target)
	models := map[string]map[string]struct{}{}
	infos := map[string]map[string]ModelInfo{}
	strategies := make(map[string]string, len(eff.Models))

	instances := make(map[string]config.EffectiveInstance, len(eff.Instances))
	proxyURLs := make(map[string]*url.URL, len(eff.Instances))
	for _, inst := range eff.Instances {
		instances[inst.Name] = inst
		if inst.ProxyURL != "" {
			parsed, err := url.Parse(inst.ProxyURL)
			if err != nil {
				return nil, fmt.Errorf("instance %q: invalid proxy_url: %w", inst.Name, err)
			}
			proxyURLs[inst.Name] = parsed
		}
	}
	defaultTimeout := cfg.UpstreamTimeout.Value()
	location := cfg.Schedule.Location
	if location == nil {
		location = time.UTC
	}

	for _, m := range eff.Models {
		strategies[m.Name] = m.Strategy
		for _, cand := range m.Instances {
			inst, ok := instances[cand.Instance]
			if !ok {
				return nil, fmt.Errorf("model %q: instance %q does not exist", m.Name, cand.Instance)
			}
			timeout := inst.Timeout
			if timeout <= 0 {
				timeout = defaultTimeout
			}
			for _, ep := range inst.Endpoints {
				version := ""
				if ep.Protocol == config.ProtocolAnthropic {
					version = config.DefaultAnthropicVersion
				}
				k := Key{Protocol: ep.Protocol, Model: m.Name}
				idx[k] = append(idx[k], Target{
					Instance:         inst.Name,
					Model:            m.Name,
					Priority:         cand.Priority,
					Endpoint:         ep.Protocol,
					Protocol:         ep.Protocol,
					BaseURL:          ep.BaseURL,
					Version:          version,
					UpstreamModel:    cand.Upstream,
					Key:              inst.APIKey,
					Timeout:          timeout,
					MaxFails:         inst.MaxFails,
					Cooldown:         time.Duration(inst.CooldownSecs) * time.Second,
					Windows:          cand.Windows,
					AllowPrivate:     inst.AllowPrivateUpstream,
					SessionHeader:    inst.SessionHeader,
					FailoverStatuses: append([]int(nil), inst.FailoverStatuses...),
					ProxyURL:         proxyURLs[inst.Name],
				})
				if models[ep.Protocol] == nil {
					models[ep.Protocol] = map[string]struct{}{}
				}
				models[ep.Protocol][m.Name] = struct{}{}
				if infos[ep.Protocol] == nil {
					infos[ep.Protocol] = map[string]ModelInfo{}
				}
				if _, ok := infos[ep.Protocol][m.Name]; !ok {
					infos[ep.Protocol][m.Name] = ModelInfo{
						Name:     m.Name,
						Metadata: m.Metadata.For(ep.Protocol),
					}
				}
			}
		}
	}

	modelsByProtocol := make(map[string][]string, len(models))
	for proto, set := range models {
		list := make([]string, 0, len(set))
		for m := range set {
			list = append(list, m)
		}
		sort.Strings(list)
		modelsByProtocol[proto] = list
	}

	infosByProtocol := make(map[string][]ModelInfo, len(infos))
	for proto, set := range infos {
		list := make([]ModelInfo, 0, len(set))
		for _, info := range set {
			list = append(list, info)
		}
		sort.Slice(list, func(a, b int) bool { return list[a].Name < list[b].Name })
		infosByProtocol[proto] = list
	}

	// The candidate chain of a model is protocol specific: an instance that
	// only speaks OpenAI does not show up on the Anthropic chain. The
	// console shows one chain per model, so the OpenAI chain wins and any
	// other protocol serves as the fallback.
	byModel := make(map[string][]Target)
	for key, targets := range idx {
		if key.Protocol != config.ProtocolOpenAI {
			continue
		}
		byModel[key.Model] = targets
	}
	for key, targets := range idx {
		if _, ok := byModel[key.Model]; ok {
			continue
		}
		byModel[key.Model] = targets
	}

	return &Snapshot{
		Config:           cfg,
		Index:            idx,
		ModelsByProtocol: modelsByProtocol,
		InfosByProtocol:  infosByProtocol,
		ByModel:          byModel,
		Strategies:       strategies,
		Location:         location,
	}, nil
}

func (r *Router) Snapshot() *Snapshot { return r.snap.Load() }

func (r *Router) Ready() bool { return r.snap.Load() != nil }

// Resolve returns the ordered candidate list of one model on one protocol.
// The first entry is the primary upstream; the rest are fallbacks that are
// tried when the previous ones fail.
//
// The order depends on the current time, cached subscription quota, and the
// model's routing strategy.
func (r *Router) Resolve(protocol, model string) ([]Target, error) {
	return r.ResolveAtWithOptions(protocol, model, time.Now(), ResolveOptions{})
}

// ResolveWithOptions is Resolve with per-request routing options such as a
// sticky session key.
func (r *Router) ResolveWithOptions(protocol, model string, opts ResolveOptions) ([]Target, error) {
	return r.ResolveAtWithOptions(protocol, model, time.Now(), opts)
}

// ResolveAt is Resolve with an explicit moment. It exists so the tests can
// pin the clock.
func (r *Router) ResolveAt(protocol, model string, now time.Time) ([]Target, error) {
	return r.ResolveAtWithOptions(protocol, model, now, ResolveOptions{})
}

// ResolveAtWithOptions is ResolveAt with explicit per-request routing options.
func (r *Router) ResolveAtWithOptions(protocol, model string, now time.Time, opts ResolveOptions) ([]Target, error) {
	snap := r.snap.Load()
	if snap == nil {
		return nil, &ModelNotFoundError{Model: model, Protocol: protocol, Hint: protocolHint(nil, protocol, model), Reason: "router is not ready"}
	}
	t, ok := snap.Index[Key{Protocol: protocol, Model: model}]
	if !ok {
		return nil, &ModelNotFoundError{
			Model:    model,
			Protocol: protocol,
			Hint:     protocolHint(snap, protocol, model),
		}
	}
	// Snapshot is a shared immutable routing table read lock-free by every
	// request, so hand out a copy to keep callers from mutating it.
	candidates := make([]Target, len(t))
	copy(candidates, t)
	strategy := snap.Strategies[model]
	ordered := orderCandidatesWithStrategy(candidates, snap.Location, now, strategy, r.quotaProvider(), opts, &r.rrCounter)
	if len(ordered) == 0 {
		return nil, &NoAvailableTargetError{
			Model:    model,
			Protocol: protocol,
			Reason:   "every candidate is outside its time window",
		}
	}
	return ordered, nil
}

// PreviewModel returns the candidates of one model with their current routing
// state, including live quota degradation from the attached QuotaProvider.
func (r *Router) PreviewModel(model string, now time.Time) []Preview {
	snap := r.snap.Load()
	if snap == nil {
		return nil
	}
	return snap.previewModelWithQuota(model, now, r.quotaProvider())
}

func protocolHint(snap *Snapshot, protocol, model string) string {
	if snap == nil {
		return ""
	}
	other := config.ProtocolOpenAI
	if protocol == config.ProtocolOpenAI {
		other = config.ProtocolAnthropic
	}
	if _, ok := snap.Index[Key{Protocol: other, Model: model}]; ok {
		return fmt.Sprintf("model %q is available via the %s endpoint", model, endpointFor(other))
	}
	return ""
}

func endpointFor(protocol string) string {
	if protocol == config.ProtocolAnthropic {
		return "Anthropic (/anthropic)"
	}
	return "OpenAI (/openai/v1)"
}

// ModelNotFoundError is returned when the model is not configured.
type ModelNotFoundError struct {
	Model    string
	Protocol string
	Hint     string
	Reason   string
}

func (e *ModelNotFoundError) Error() string {
	msg := fmt.Sprintf("model %q is not configured for the %s protocol", e.Model, endpointFor(e.Protocol))
	if e.Hint != "" {
		msg += "; " + e.Hint
	}
	if e.Reason != "" {
		msg += " (" + e.Reason + ")"
	}
	return msg
}

// NoAvailableTargetError is returned when the model is configured but none of
// its candidates may serve it at the moment. It happens when every candidate
// is restricted to a time window that is currently closed.
type NoAvailableTargetError struct {
	Model    string
	Protocol string
	Reason   string
}

func (e *NoAvailableTargetError) Error() string {
	msg := fmt.Sprintf("model %q has no upstream instance available at the moment", e.Model)
	if e.Reason != "" {
		msg += " (" + e.Reason + ")"
	}
	return msg
}

// ModelInfos returns the models visible on one protocol's endpoint together
// with their effective metadata.
func (r *Router) ModelInfos(protocol string) []ModelInfo {
	snap := r.snap.Load()
	if snap == nil {
		return nil
	}
	// Snapshot is shared by every request, so hand out a copy to keep
	// callers from mutating the immutable routing table.
	infos := snap.InfosByProtocol[protocol]
	out := make([]ModelInfo, len(infos))
	copy(out, infos)
	return out
}

// ModelList returns the models visible on one protocol's endpoint.
func (r *Router) ModelList(protocol string) []string {
	snap := r.snap.Load()
	if snap == nil {
		return nil
	}
	return snap.ModelsByProtocol[protocol]
}
