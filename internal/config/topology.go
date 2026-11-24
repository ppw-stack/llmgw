package config

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pw/llmgw/internal/modelmeta"
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

// QuotaCredential is the extra credential some vendors need to read the
// subscription quota of an instance.
//
// It is never used for inference: Volcengine, for example, publishes the plan
// windows of a Coding Plan only on its account control plane, which asks for
// an AccessKey pair instead of the inference key.
type QuotaCredential struct {
	AccessKeyID     string `yaml:"access_key_id" json:"access_key_id,omitempty"`
	SecretAccessKey string `yaml:"secret_access_key" json:"secret_access_key,omitempty"`
}

// Empty reports whether the credential carries nothing.
func (c QuotaCredential) Empty() bool {
	return c.AccessKeyID == "" && c.SecretAccessKey == ""
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
	Name            string          `yaml:"name"`
	Preset          string          `yaml:"preset"`
	APIKey          string          `yaml:"api_key"`
	QuotaCredential QuotaCredential `yaml:"quota_credential"`
	Endpoints       []Endpoint      `yaml:"endpoints"`
	Models          []string        `yaml:"models"`
	MaxFails        int             `yaml:"max_fails"`
	CooldownSecs    int             `yaml:"cooldown_secs"`
	TimeoutSecs     int             `yaml:"timeout_secs"`
	// AllowPrivateUpstream permits endpoints that point at loopback or a
	// private address. Without it, such an endpoint is rejected so a stolen
	// admin session cannot use the gateway as an internal-network proxy.
	AllowPrivateUpstream bool `yaml:"allow_private_upstream"`
	// SessionHeader names a header the upstream requires for session
	// affinity. OpenCode Go, for example, rejects requests without a stable
	// x-opencode-session with "400 MissingSessionID". When the client does
	// not send it, the gateway derives a value from the conversation prefix
	// so prompt caching keeps working across turns.
	SessionHeader string `yaml:"session_header"`
	// FailoverStatuses lists extra upstream status codes that mean another
	// candidate may still succeed. The defaults (429 and 5xx) always apply;
	// this list only adds vendor-specific ones, such as MiniMax's 402 when
	// the subscription window is exhausted.
	FailoverStatuses []int `yaml:"failover_statuses"`
}

// ModelInstance binds a model to one concrete upstream model of one instance.
type ModelInstance struct {
	Name string `yaml:"name"`
	// Upstream is the model name this candidate actually sends to the
	// instance. It must be declared in the instance's models list.
	Upstream string `yaml:"upstream"`
	Priority int    `yaml:"priority"`
	// Windows are the time ranges in which this candidate takes over
	// another priority or is the only candidate allowed to serve the model.
	Windows []TimeWindow `yaml:"windows"`
}

// Model is a logical model. It owns the strategy and the ordered candidate
// list of the instance + upstream model pairs that serve it.
type Model struct {
	Name     string `yaml:"name"`
	Strategy string `yaml:"strategy"`
	// Metadata is the optional description advertised on the model-list
	// endpoints. It is never derived from the upstream model.
	Metadata  modelmeta.Metadata `yaml:"metadata,omitempty"`
	Instances []ModelInstance    `yaml:"instances"`
}

// Effective is the routing-ready topology: instances with their keys and
// models with their ordered candidate lists.
type Effective struct {
	Instances []EffectiveInstance
	Models    []EffectiveModel
}

// EffectiveInstance is one instance in its routing-ready form.
type EffectiveInstance struct {
	Name string
	// Preset is the built-in endpoint the instance was created from. It is
	// used to fill model metadata from the compiled-in catalog.
	Preset       string
	APIKey       string
	Endpoints    []EffectiveEndpoint
	Models       []string
	MaxFails     int
	CooldownSecs int
	Timeout      time.Duration
	// AllowPrivateUpstream mirrors the instance flag so the transport can
	// re-check the resolved address right before dialing.
	AllowPrivateUpstream bool
	// SessionHeader mirrors the instance flag so the transport can inject a
	// derived session value when the client did not send one.
	SessionHeader string
	// FailoverStatuses are the instance's extra retryable status codes.
	FailoverStatuses []int
}

// EffectiveEndpoint is one protocol entrance of an instance.
type EffectiveEndpoint struct {
	Protocol string
	BaseURL  string
}

// EffectiveModel is one model in its routing-ready form.
type EffectiveModel struct {
	Name     string
	Strategy string
	// Metadata is the explicit metadata merged with the built-in preset
	// defaults of the candidate chain.
	Metadata  modelmeta.Metadata
	Instances []EffectiveModelInstance
}

// EffectiveModelInstance is one candidate of a model.
type EffectiveModelInstance struct {
	Instance string
	// Upstream is the model name the candidate sends to the instance.
	Upstream string
	Priority int
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

// PresetModelsEndpoint returns the model-list endpoint of a built-in preset.
// The OpenAI-compatible base is preferred because every supported provider
// exposes the list there; the Anthropic base is the fallback.
func PresetModelsEndpoint(id string) (string, string, error) {
	pr, ok := preset.Lookup(id)
	if !ok {
		return "", "", fmt.Errorf("unknown preset %q", id)
	}
	if pr.OpenAIBaseURL != "" {
		return strings.TrimRight(pr.OpenAIBaseURL, "/") + "/models", ProtocolOpenAI, nil
	}
	if pr.AnthropicBaseURL != "" {
		return strings.TrimRight(pr.AnthropicBaseURL, "/") + "/v1/models", ProtocolAnthropic, nil
	}
	return "", "", fmt.Errorf("preset %q has no endpoint to query models from", id)
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
	in.SessionHeader = strings.TrimSpace(in.SessionHeader)
	if in.SessionHeader != "" && !validHeaderName(in.SessionHeader) {
		return fmt.Errorf("instance %q: session_header %q is not a valid HTTP header name", in.Name, in.SessionHeader)
	}
	in.QuotaCredential.AccessKeyID = strings.TrimSpace(in.QuotaCredential.AccessKeyID)
	in.QuotaCredential.SecretAccessKey = strings.TrimSpace(in.QuotaCredential.SecretAccessKey)
	if in.QuotaCredential.AccessKeyID != "" && in.QuotaCredential.SecretAccessKey == "" {
		return fmt.Errorf("instance %q: quota_credential needs both access_key_id and secret_access_key", in.Name)
	}
	if in.QuotaCredential.SecretAccessKey != "" && in.QuotaCredential.AccessKeyID == "" {
		return fmt.Errorf("instance %q: quota_credential needs both access_key_id and secret_access_key", in.Name)
	}

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
		// A preset that needs session affinity ships the header name, so a
		// deployment does not have to know the vendor quirk.
		if in.SessionHeader == "" {
			in.SessionHeader = pr.SessionHeader
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
		base, err := normalizeBaseURL(in.Name, "endpoints[].base_url", ep.BaseURL, in.AllowPrivateUpstream)
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
	if err := normalizeFailoverStatuses(in); err != nil {
		return err
	}
	return nil
}

// normalizeFailoverStatuses trims, de-duplicates and sorts the extra retryable
// status codes of one instance. Only 4xx and 5xx make sense: a success or a
// redirect never describes a failed attempt.
func normalizeFailoverStatuses(in *Instance) error {
	if len(in.FailoverStatuses) == 0 {
		in.FailoverStatuses = nil
		return nil
	}
	seen := make(map[int]bool, len(in.FailoverStatuses))
	out := make([]int, 0, len(in.FailoverStatuses))
	for _, code := range in.FailoverStatuses {
		if code < 400 || code > 599 {
			return fmt.Errorf("instance %q: failover_statuses must be HTTP error codes (400-599), got %d", in.Name, code)
		}
		if seen[code] {
			continue
		}
		seen[code] = true
		out = append(out, code)
	}
	sort.Ints(out)
	in.FailoverStatuses = out
	return nil
}

// validHeaderName reports whether name is an RFC 7230 token, which is what
// net/http accepts as a request header name.
func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	const tchar = "!#$%&'*+-.^_`|~"
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte(tchar, c) >= 0:
		default:
			return false
		}
	}
	return true
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
	in.Strategy = strings.ToLower(strings.TrimSpace(in.Strategy))
	if in.Strategy == "" {
		in.Strategy = StrategyFailover
	}
	if in.Strategy != StrategyFailover {
		return fmt.Errorf("model %q: strategy %q is not supported yet (only %q)",
			in.Name, in.Strategy, StrategyFailover)
	}
	in.Metadata.Normalize()
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
		mi.Upstream = strings.TrimSpace(mi.Upstream)
		if mi.Upstream == "" {
			return fmt.Errorf("model %q: instance %q: upstream model is required", in.Name, mi.Name)
		}
		if mi.Priority < 0 {
			return fmt.Errorf("model %q: instance %q has a negative priority", in.Name, mi.Name)
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
			Name:                 inst.Name,
			Preset:               inst.Preset,
			APIKey:               inst.APIKey,
			Models:               append([]string(nil), inst.Models...),
			MaxFails:             inst.MaxFails,
			CooldownSecs:         inst.CooldownSecs,
			AllowPrivateUpstream: inst.AllowPrivateUpstream,
			SessionHeader:        inst.SessionHeader,
			FailoverStatuses:     append([]int(nil), inst.FailoverStatuses...),
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
	instancesByName := make(map[string]EffectiveInstance, len(out.Instances))
	for _, inst := range out.Instances {
		instancesByName[inst.Name] = inst
	}
	for i := range c.Models {
		m := &c.Models[i]
		if m.Name == "" {
			return Effective{}, fmt.Errorf("model name is required")
		}
		if seenModel[m.Name] {
			return Effective{}, fmt.Errorf("model %q is declared twice", m.Name)
		}
		seenModel[m.Name] = true

		strategy := m.Strategy
		if strategy == "" {
			strategy = StrategyFailover
		}
		eff := EffectiveModel{Name: m.Name, Strategy: strategy, Metadata: m.Metadata}
		for _, mi := range m.Instances {
			if !known[mi.Name] {
				return Effective{}, fmt.Errorf("model %q: instance %q does not exist", m.Name, mi.Name)
			}
			candidateUpstream := strings.TrimSpace(mi.Upstream)
			if candidateUpstream == "" {
				return Effective{}, fmt.Errorf("model %q: instance %q: upstream model is required", m.Name, mi.Name)
			}
			// Every candidate must declare its own upstream name: putting an
			// instance in the chain means it is expected to serve it.
			if !supports[mi.Name][candidateUpstream] {
				return Effective{}, fmt.Errorf(
					"model %q: instance %q does not declare upstream model %q in its models list",
					m.Name, mi.Name, candidateUpstream)
			}
			windows, err := compileWindows(
				fmt.Sprintf("model %q: instance %q", m.Name, mi.Name), mi.Windows)
			if err != nil {
				return Effective{}, err
			}
			eff.Instances = append(eff.Instances, EffectiveModelInstance{
				Instance: mi.Name,
				Upstream: candidateUpstream,
				Priority: mi.Priority,
				Windows:  windows,
			})
		}
		// Lower priority wins; the written order breaks ties.
		sort.SliceStable(eff.Instances, func(a, b int) bool {
			return eff.Instances[a].Priority < eff.Instances[b].Priority
		})
		eff.Metadata = eff.Metadata.Merge(presetMetadata(eff.Instances, instancesByName))
		out.Models = append(out.Models, eff)
	}
	return out, nil
}

// presetMetadata resolves the compiled-in defaults of the first candidate that
// references a built-in preset. Custom instances never contribute defaults:
// the same upstream name may be served through a proxy with lower limits.
func presetMetadata(candidates []EffectiveModelInstance, instances map[string]EffectiveInstance) modelmeta.Metadata {
	for _, cand := range candidates {
		inst, ok := instances[cand.Instance]
		if !ok || inst.Preset == "" {
			continue
		}
		if meta, ok := preset.ModelMetadata(cand.Upstream); ok {
			return meta
		}
	}
	return modelmeta.Metadata{}
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
			Strategy: StrategyFailover,
			Instances: []ModelInstance{
				{
					Name:     inst.Name,
					Upstream: strings.TrimSpace(upstream),
					Priority: 0,
				},
			},
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })

	// The legacy form knows exactly which upstream models it serves, so the
	// capability list is derived from them instead of being hand-written.
	seenUpstream := make(map[string]bool, len(out))
	upstreams := make([]string, 0, len(out))
	for _, m := range out {
		for _, mi := range m.Instances {
			if seenUpstream[mi.Upstream] {
				continue
			}
			seenUpstream[mi.Upstream] = true
			upstreams = append(upstreams, mi.Upstream)
		}
	}
	sort.Strings(upstreams)
	inst.Models = upstreams
	return inst, out, nil
}
