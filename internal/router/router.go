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

// Target is the single resolved upstream destination for a request.
type Target struct {
	Provider      string
	Endpoint      string
	Protocol      string
	BaseURL       string
	Version       string
	UpstreamModel string
	Timeout       time.Duration
	ProviderIndex int
	Keys          []string
	Proxy         string
}

type EndpointView struct {
	Name     string            `json:"name"`
	Protocol string            `json:"protocol"`
	BaseURL  string            `json:"base_url"`
	Models   map[string]string `json:"models"`
}

type ProviderView struct {
	Name      string         `json:"name"`
	Keys      int            `json:"keys"`
	Endpoints []EndpointView `json:"endpoints"`
}

// Snapshot is an immutable routing table swapped atomically on reload.
type Snapshot struct {
	Version          uint64
	Config           *config.Config
	Index            map[Key]Target
	Providers        []ProviderView
	ModelsByProtocol map[string][]string
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
	idx := make(map[Key]Target)
	providers := make([]ProviderView, 0, len(cfg.Providers))
	models := map[string]map[string]struct{}{}

	for pi := range cfg.Providers {
		p := &cfg.Providers[pi]
		pv := ProviderView{Name: p.Name, Keys: len(p.Keys)}
		for ei := range p.Endpoints {
			ep := &p.Endpoints[ei]
			switch ep.Protocol {
			case config.ProtocolOpenAI, config.ProtocolAnthropic:
			default:
				return nil, fmt.Errorf("provider %q endpoint %q: unsupported protocol %q",
					p.Name, ep.Name, ep.Protocol)
			}
			pv.Endpoints = append(pv.Endpoints, EndpointView{
				Name:     ep.Name,
				Protocol: ep.Protocol,
				BaseURL:  ep.BaseURL,
				Models:   ep.Models,
			})
			if models[ep.Protocol] == nil {
				models[ep.Protocol] = map[string]struct{}{}
			}
			for alias, upstream := range ep.Models {
				k := Key{Protocol: ep.Protocol, Model: alias}
				if prev, dup := idx[k]; dup {
					return nil, fmt.Errorf(
						"model %q is declared twice for protocol %q: %s/%s and %s/%s; "+
							"a model may belong to exactly one endpoint per protocol",
						alias, ep.Protocol, prev.Provider, prev.Endpoint, p.Name, ep.Name)
				}
				idx[k] = Target{
					Provider:      p.Name,
					Endpoint:      ep.Name,
					Protocol:      ep.Protocol,
					BaseURL:       ep.BaseURL,
					Version:       ep.Version,
					UpstreamModel: upstream,
					Timeout:       p.Timeout.Value(),
					ProviderIndex: pi,
					Keys:          p.Keys,
					Proxy:         p.Proxy,
				}
				models[ep.Protocol][alias] = struct{}{}
			}
		}
		providers = append(providers, pv)
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

	return &Snapshot{
		Config:           cfg,
		Index:            idx,
		Providers:        providers,
		ModelsByProtocol: modelsByProtocol,
	}, nil
}

func (r *Router) Snapshot() *Snapshot { return r.snap.Load() }

func (r *Router) Ready() bool { return r.snap.Load() != nil }

// Resolve performs the only routing decision v1 has: a hash lookup.
func (r *Router) Resolve(protocol, model string) (Target, error) {
	snap := r.snap.Load()
	if snap == nil {
		return Target{}, &ModelNotFoundError{Model: model, Protocol: protocol, Hint: protocolHint(nil, protocol, model), Reason: "router is not ready"}
	}
	if t, ok := snap.Index[Key{Protocol: protocol, Model: model}]; ok {
		return t, nil
	}
	return Target{}, &ModelNotFoundError{
		Model:    model,
		Protocol: protocol,
		Hint:     protocolHint(snap, protocol, model),
	}
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

// ModelList returns the models visible on one protocol's endpoint.
func (r *Router) ModelList(protocol string) []string {
	snap := r.snap.Load()
	if snap == nil {
		return nil
	}
	return snap.ModelsByProtocol[protocol]
}
