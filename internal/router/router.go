package router

import (
	"fmt"
	"sort"
	"sync/atomic"
	"time"

	"github.com/pw/llmgw/internal/config"
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
}

// Snapshot is an immutable routing table swapped atomically on reload.
type Snapshot struct {
	Version          uint64
	Config           *config.Config
	Index            map[Key][]Target
	ModelsByProtocol map[string][]string
	// ByModel holds the candidate chain of every model, independent of the
	// protocol. The console uses it to show which candidate is in charge
	// right now.
	ByModel map[string][]Target
	// Location is the timezone every time window is evaluated in.
	Location *time.Location
}

// Router holds the current snapshot. Reads are lock-free.
type Router struct {
	snap    atomic.Pointer[Snapshot]
	version atomic.Uint64
}

func New() *Router { return &Router{} }

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

	instances := make(map[string]config.EffectiveInstance, len(eff.Instances))
	for _, inst := range eff.Instances {
		instances[inst.Name] = inst
	}
	defaultTimeout := cfg.UpstreamTimeout.Value()
	location := cfg.Schedule.Location
	if location == nil {
		location = time.UTC
	}

	for _, m := range eff.Models {
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
				})
				if models[ep.Protocol] == nil {
					models[ep.Protocol] = map[string]struct{}{}
				}
				models[ep.Protocol][m.Name] = struct{}{}
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
		ByModel:          byModel,
		Location:         location,
	}, nil
}

func (r *Router) Snapshot() *Snapshot { return r.snap.Load() }

func (r *Router) Ready() bool { return r.snap.Load() != nil }

// Resolve returns the ordered candidate list of one model on one protocol.
// The first entry is the primary upstream; the rest are fallbacks that are
// tried when the previous ones fail.
//
// The order depends on the current time: a candidate whose time window is
// active may take over a better priority.
func (r *Router) Resolve(protocol, model string) ([]Target, error) {
	return r.ResolveAt(protocol, model, time.Now())
}

// ResolveAt is Resolve with an explicit moment. It exists so the tests can
// pin the clock.
func (r *Router) ResolveAt(protocol, model string, now time.Time) ([]Target, error) {
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
	ordered := orderCandidates(candidates, snap.Location, now)
	if len(ordered) == 0 {
		return nil, &NoAvailableTargetError{
			Model:    model,
			Protocol: protocol,
			Reason:   "every candidate is outside its time window",
		}
	}
	return ordered, nil
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

// ModelList returns the models visible on one protocol's endpoint.
func (r *Router) ModelList(protocol string) []string {
	snap := r.snap.Load()
	if snap == nil {
		return nil
	}
	return snap.ModelsByProtocol[protocol]
}
