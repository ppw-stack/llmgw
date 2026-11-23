package config

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pw/llmgw/internal/preset"
)

// Routing strategies. Only failover is implemented in v1; the field exists so
// the configuration shape does not have to change when the others arrive.
const (
	// StrategyFailover tries the primary instance first and falls back to the
	// next candidate when it fails.
	StrategyFailover = "failover"
)

// Circuit breaker defaults for one instance.
const (
	// DefaultMaxFails is the failure count that opens an instance's circuit.
	DefaultMaxFails = 3
	// DefaultCooldownSecs is how long an open circuit is skipped before retry.
	DefaultCooldownSecs = 30
)

// Endpoint is one protocol entrance of an instance.
type Endpoint struct {
	Protocol string `yaml:"protocol"`
	BaseURL  string `yaml:"base_url"`
}

// Instance is a physical upstream: one account plus the protocol entrances it
// serves. It carries no routing strategy - the strategy belongs to the model
// that uses the instance, because one instance may serve several models with
// different strategies.
//
// Models lists the upstream model names the account can serve. It is the
// capability half of the topology: the model side says which instance it
// wants, this side says what the instance can actually handle. It is
// required, so a typo in a model's upstream name fails at configuration time
// instead of at the first upstream call.
type Instance struct {
	Name         string     `yaml:"name"`
	Preset       string     `yaml:"preset"`
	APIKey       string     `yaml:"api_key"`
	Endpoints    []Endpoint `yaml:"endpoints"`
	Models       []string   `yaml:"models"`
	MaxFails     int        `yaml:"max_fails"`
	CooldownSecs int        `yaml:"cooldown_secs"`
	TimeoutSecs  int        `yaml:"timeout_secs"`
}

// ModelInstance binds a model to one instance.
type ModelInstance struct {
	Name     string `yaml:"name"`
	Priority int    `yaml:"priority"`
	Weight   int    `yaml:"weight"`
	// Windows are the time ranges in which this candidate takes over
	// another priority or is the only candidate allowed to serve the model.
	Windows []TimeWindow `yaml:"windows"`
}

// Model is a logical model. It owns the strategy and the ordered candidate
// list of the instances that serve it.
type Model struct {
	Name      string          `yaml:"name"`
	Upstream  string          `yaml:"upstream"`
	Strategy  string          `yaml:"strategy"`
	Instances []ModelInstance `yaml:"instances"`
}

// Effective is the routing-ready topology: instances with their keys and
// models with their ordered candidate lists.
type Effective struct {
	Instances []EffectiveInstance
	Models    []EffectiveModel
}

// EffectiveInstance is one instance in its routing-ready form.
type EffectiveInstance struct {
	Name         string
	APIKey       string
	Endpoints    []EffectiveEndpoint
	Models       []string
	MaxFails     int
	CooldownSecs int
	Timeout      time.Duration
}

// EffectiveEndpoint is one protocol entrance of an instance.
type EffectiveEndpoint struct {
	Protocol string
	BaseURL  string
}

// EffectiveModel is one model in its routing-ready form.
type EffectiveModel struct {
	Name      string
	Upstream  string
	Strategy  string
	Instances []EffectiveModelInstance
}

// EffectiveModelInstance is one candidate of a model.
type EffectiveModelInstance struct {
	Instance string
	Priority int
	Weight   int
	// Windows are the compiled time windows of the candidate.
	Windows []EffectiveWindow
}

// presetEndpoints turns a built-in preset into the endpoints of an instance.
func presetEndpoints(pr preset.Preset) []Endpoint {
	var out []Endpoint
	if pr.OpenAIBaseURL != "" {
		out = append(out, Endpoint{
			Protocol: ProtocolOpenAI,
			BaseURL:  strings.TrimRight(pr.OpenAIBaseURL, "/"),
		})
	}
	if pr.AnthropicBaseURL != "" {
		out = append(out, Endpoint{
			Protocol: ProtocolAnthropic,
			BaseURL:  strings.TrimRight(pr.AnthropicBaseURL, "/"),
		})
	}
	return out
}

// PresetModels returns the upstream model names a built-in preset declares.
func PresetModels(id string) ([]string, error) {
	pr, ok := preset.Lookup(id)
	if !ok {
		return nil, fmt.Errorf("unknown preset %q", id)
	}
	parsed, err := ParseModels(pr.Models)
	if err != nil {
		return nil, fmt.Errorf("preset %q: %w", id, err)
	}
	seen := make(map[string]bool, len(parsed))
	out := make([]string, 0, len(parsed))
	for _, upstream := range parsed {
		if seen[upstream] {
			continue
		}
		seen[upstream] = true
		out = append(out, upstream)
	}
	sort.Strings(out)
	return out, nil
}

// sameEndpoints reports whether two endpoint lists describe the same
// entrances, ignoring the order.
func sameEndpoints(a, b []Endpoint) bool {
	if len(a) != len(b) {
		return false
	}
	index := make(map[string]string, len(b))
	for _, ep := range b {
		index[ep.Protocol] = ep.BaseURL
	}
	for _, ep := range a {
		if index[ep.Protocol] != ep.BaseURL {
			return false
		}
	}
	return true
}

// NormalizeInstance validates one instance and fills in the defaults. It does
// not expand environment variables; that is only meaningful for config.yaml.
func NormalizeInstance(in *Instance) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return fmt.Errorf("instance name is required")
	}
	in.Preset = strings.TrimSpace(in.Preset)
	in.APIKey = strings.TrimSpace(in.APIKey)

	if in.Preset != "" {
		pr, ok := preset.Lookup(in.Preset)
		if !ok {
			return fmt.Errorf("instance %q: unknown preset %q", in.Name, in.Preset)
		}
		defaults := presetEndpoints(pr)
		if len(in.Endpoints) > 0 {
			// Endpoints that already match the preset are accepted: the
			// legacy provider form expands into them.
			if !sameEndpoints(in.Endpoints, defaults) {
				return fmt.Errorf("instance %q: preset %q supplies its own endpoints; "+
					"remove endpoints or use a custom instance", in.Name, in.Preset)
			}
		} else {
			in.Endpoints = defaults
		}
	} else if len(in.Endpoints) == 0 {
		return fmt.Errorf("instance %q: at least one endpoint is required", in.Name)
	}

	seen := map[string]bool{}
	for i := range in.Endpoints {
		ep := &in.Endpoints[i]
		ep.Protocol = strings.ToLower(strings.TrimSpace(ep.Protocol))
		switch ep.Protocol {
		case ProtocolOpenAI, ProtocolAnthropic:
		default:
			return fmt.Errorf("instance %q: endpoint %d has unknown protocol %q (want %q or %q)",
				in.Name, i, ep.Protocol, ProtocolOpenAI, ProtocolAnthropic)
		}
		if seen[ep.Protocol] {
			return fmt.Errorf("instance %q: protocol %q is declared twice", in.Name, ep.Protocol)
		}
		seen[ep.Protocol] = true
		base, err := normalizeBaseURL(in.Name, "endpoints[].base_url", ep.BaseURL)
		if err != nil {
			return err
		}
		ep.BaseURL = base
	}

	models, err := normalizeModelNames(in.Name, in.Models)
	if err != nil {
		return err
	}
	in.Models = models

	if in.MaxFails <= 0 {
		in.MaxFails = DefaultMaxFails
	}
	if in.CooldownSecs <= 0 {
		in.CooldownSecs = DefaultCooldownSecs
	}
	if in.TimeoutSecs < 0 {
		return fmt.Errorf("instance %q: timeout_secs must not be negative", in.Name)
	}
	return nil
}

// normalizeModelNames trims, de-duplicates and sorts the upstream model names
// one instance declares. An instance must declare at least one, otherwise it
// can never serve anything.
func normalizeModelNames(owner string, raw []string) ([]string, error) {
	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		name := strings.TrimSpace(item)
		if name == "" {
			return nil, fmt.Errorf("instance %q: models must not contain an empty name", owner)
		}
		if seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("instance %q: at least one supported model is required in models", owner)
	}
	sort.Strings(out)
	return out, nil
}

// NormalizeModel validates one model and fills in the defaults. The candidate
// list is sorted by priority; the written order breaks ties.
func NormalizeModel(in *Model) error {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" {
		return fmt.Errorf("model name is required")
	}
	in.Upstream = strings.TrimSpace(in.Upstream)
	if in.Upstream == "" {
		in.Upstream = in.Name
	}
	in.Strategy = strings.ToLower(strings.TrimSpace(in.Strategy))
	if in.Strategy == "" {
		in.Strategy = StrategyFailover
	}
	if in.Strategy != StrategyFailover {
		return fmt.Errorf("model %q: strategy %q is not supported yet (only %q)",
			in.Name, in.Strategy, StrategyFailover)
	}
	if len(in.Instances) == 0 {
		return fmt.Errorf("model %q: at least one instance is required", in.Name)
	}
	seen := map[string]bool{}
	for i := range in.Instances {
		mi := &in.Instances[i]
		mi.Name = strings.TrimSpace(mi.Name)
		if mi.Name == "" {
			return fmt.Errorf("model %q: instance name is required", in.Name)
		}
		if seen[mi.Name] {
			return fmt.Errorf("model %q: instance %q is listed twice", in.Name, mi.Name)
		}
		seen[mi.Name] = true
		if mi.Priority < 0 {
			return fmt.Errorf("model %q: instance %q has a negative priority", in.Name, mi.Name)
		}
		if mi.Weight <= 0 {
			mi.Weight = 100
		}
		// The windows are validated here so a broken range fails at load
		// time. The compiled form is produced by Effective.
		if _, err := compileWindows(fmt.Sprintf("model %q: instance %q", in.Name, mi.Name), mi.Windows); err != nil {
			return err
		}
	}
	sort.SliceStable(in.Instances, func(a, b int) bool {
		return in.Instances[a].Priority < in.Instances[b].Priority
	})
	return nil
}

// Effective compiles the topology into its routing-ready form. It does not
// modify the receiver, so it is safe to call on a shared configuration.
func (c *Config) Effective() (Effective, error) {
	out := Effective{}
	known := make(map[string]bool, len(c.Instances))
	supports := make(map[string]map[string]bool, len(c.Instances))
	for i := range c.Instances {
		inst := &c.Instances[i]
		if inst.Name == "" {
			return Effective{}, fmt.Errorf("instance name is required")
		}
		if known[inst.Name] {
			return Effective{}, fmt.Errorf("duplicate instance %q", inst.Name)
		}
		known[inst.Name] = true

		eff := EffectiveInstance{
			Name:         inst.Name,
			APIKey:       inst.APIKey,
			Models:       append([]string(nil), inst.Models...),
			MaxFails:     inst.MaxFails,
			CooldownSecs: inst.CooldownSecs,
		}
		if eff.MaxFails <= 0 {
			eff.MaxFails = DefaultMaxFails
		}
		if eff.CooldownSecs <= 0 {
			eff.CooldownSecs = DefaultCooldownSecs
		}
		if inst.TimeoutSecs > 0 {
			eff.Timeout = time.Duration(inst.TimeoutSecs) * time.Second
		}
		names := make(map[string]bool, len(inst.Models))
		for _, name := range inst.Models {
			names[name] = true
		}
		supports[inst.Name] = names
		endpoints := inst.Endpoints
		if len(endpoints) == 0 && inst.Preset != "" {
			// A preset instance that was not expanded yet still routes.
			pr, ok := preset.Lookup(inst.Preset)
			if !ok {
				return Effective{}, fmt.Errorf("instance %q: unknown preset %q", inst.Name, inst.Preset)
			}
			endpoints = presetEndpoints(pr)
		}
		for _, ep := range endpoints {
			if ep.BaseURL == "" {
				continue
			}
			eff.Endpoints = append(eff.Endpoints, EffectiveEndpoint{
				Protocol: ep.Protocol,
				BaseURL:  ep.BaseURL,
			})
		}
		out.Instances = append(out.Instances, eff)
	}

	seenModel := make(map[string]bool, len(c.Models))
	for i := range c.Models {
		m := &c.Models[i]
		if m.Name == "" {
			return Effective{}, fmt.Errorf("model name is required")
		}
		if seenModel[m.Name] {
			return Effective{}, fmt.Errorf("model %q is declared twice", m.Name)
		}
		seenModel[m.Name] = true

		upstream := m.Upstream
		if upstream == "" {
			upstream = m.Name
		}
		strategy := m.Strategy
		if strategy == "" {
			strategy = StrategyFailover
		}
		eff := EffectiveModel{Name: m.Name, Upstream: upstream, Strategy: strategy}
		for _, mi := range m.Instances {
			if !known[mi.Name] {
				return Effective{}, fmt.Errorf("model %q: instance %q does not exist", m.Name, mi.Name)
			}
			// Every candidate must declare the upstream name: putting an
			// instance in the chain means it is expected to serve it.
			if !supports[mi.Name][upstream] {
				return Effective{}, fmt.Errorf(
					"model %q: instance %q does not declare upstream model %q in its models list",
					m.Name, mi.Name, upstream)
			}
			weight := mi.Weight
			if weight <= 0 {
				weight = 100
			}
			windows, err := compileWindows(
				fmt.Sprintf("model %q: instance %q", m.Name, mi.Name), mi.Windows)
			if err != nil {
				return Effective{}, err
			}
			eff.Instances = append(eff.Instances, EffectiveModelInstance{
				Instance: mi.Name,
				Priority: mi.Priority,
				Weight:   weight,
				Windows:  windows,
			})
		}
		// Lower priority wins; the written order breaks ties.
		sort.SliceStable(eff.Instances, func(a, b int) bool {
			return eff.Instances[a].Priority < eff.Instances[b].Priority
		})
		out.Models = append(out.Models, eff)
	}
	return out, nil
}

// ExpandProvider turns the legacy single-upstream provider into an instance
// plus the models it served. It is the bridge that keeps old config files and
// old databases working.
func ExpandProvider(p Provider) (Instance, []Model, error) {
	inst := Instance{
		Name:   strings.TrimSpace(p.Name),
		Preset: strings.TrimSpace(p.Preset),
		APIKey: strings.TrimSpace(p.APIKey),
	}
	if inst.Name == "" {
		return Instance{}, nil, fmt.Errorf("provider name is required")
	}
	var presetDef preset.Preset
	if inst.Preset != "" {
		pr, ok := preset.Lookup(inst.Preset)
		if !ok {
			return Instance{}, nil, fmt.Errorf("provider %q: unknown preset %q", inst.Name, inst.Preset)
		}
		presetDef = pr
		if p.OpenAIBaseURL != "" || p.AnthropicBaseURL != "" {
			return Instance{}, nil, fmt.Errorf("provider %q: preset %q supplies its own base URLs; "+
				"remove openai_base_url/anthropic_base_url or use a custom provider", inst.Name, inst.Preset)
		}
		inst.Endpoints = presetEndpoints(pr)
	} else {
		if p.OpenAIBaseURL != "" {
			inst.Endpoints = append(inst.Endpoints, Endpoint{
				Protocol: ProtocolOpenAI,
				BaseURL:  strings.TrimSpace(p.OpenAIBaseURL),
			})
		}
		if p.AnthropicBaseURL != "" {
			inst.Endpoints = append(inst.Endpoints, Endpoint{
				Protocol: ProtocolAnthropic,
				BaseURL:  strings.TrimSpace(p.AnthropicBaseURL),
			})
		}
	}

	models := p.Models
	if len(models) == 0 {
		if inst.Preset == "" {
			return Instance{}, nil, fmt.Errorf("provider %q: models are required", inst.Name)
		}
		parsed, err := ParseModels(presetDef.Models)
		if err != nil {
			return Instance{}, nil, fmt.Errorf("provider %q: preset %q: %w", inst.Name, inst.Preset, err)
		}
		models = parsed
	}
	normalized, err := normalizeModels(models)
	if err != nil {
		return Instance{}, nil, fmt.Errorf("provider %q: %w", inst.Name, err)
	}
	models = normalized

	out := make([]Model, 0, len(models))
	for alias, upstream := range models {
		out = append(out, Model{
			Name:     strings.TrimSpace(alias),
			Upstream: strings.TrimSpace(upstream),
			Strategy: StrategyFailover,
			Instances: []ModelInstance{
				{Name: inst.Name, Priority: 0, Weight: 100},
			},
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	// The legacy form knows exactly which upstream models it serves, so the
	// capability list is derived from them instead of being hand-written.
	seenUpstream := make(map[string]bool, len(out))
	upstreams := make([]string, 0, len(out))
	for _, m := range out {
		if seenUpstream[m.Upstream] {
			continue
		}
		seenUpstream[m.Upstream] = true
		upstreams = append(upstreams, m.Upstream)
	}
	sort.Strings(upstreams)
	inst.Models = upstreams
	return inst, out, nil
}
