package config

import (
	"fmt"
	"net/netip"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Protocols supported by the gateway. v1 does not translate between them.
const (
	// ProtocolOpenAI is the OpenAI-compatible entrance.
	ProtocolOpenAI = "openai"
	// ProtocolAnthropic is the Anthropic-compatible entrance.
	ProtocolAnthropic = "anthropic"
)

// DefaultAnthropicVersion is sent to Anthropic-compatible upstreams.
const DefaultAnthropicVersion = "2023-06-01"

// DefaultTimeout bounds non-streaming upstream calls and the idle gap of a
// streaming response. The global default may be overridden with
// upstream_timeout; an instance may only lower it.
const DefaultTimeout = 120 * time.Second

// Duration lets YAML accept either "120s" or a bare number of seconds.
type Duration time.Duration

func (d *Duration) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err == nil {
		parsed, err := time.ParseDuration(s)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", s, err)
		}
		*d = Duration(parsed)
		return nil
	}
	var secs float64
	if err := node.Decode(&secs); err != nil {
		return fmt.Errorf("invalid duration: %w", err)
	}
	*d = Duration(time.Duration(secs * float64(time.Second)))
	return nil
}

func (d Duration) Value() time.Duration { return time.Duration(d) }

type Config struct {
	Server    Server     `yaml:"server"`
	Database  Database   `yaml:"database"`
	Log       Log        `yaml:"log"`
	Schedule  Schedule   `yaml:"schedule"`
	Instances []Instance `yaml:"instances"`
	Models    []Model    `yaml:"models"`
	Providers []Provider `yaml:"providers"`

	// UpstreamTimeout bounds non-streaming upstream calls and the idle gap of
	// a streaming response. An instance may lower it, never raise it.
	UpstreamTimeout Duration `yaml:"upstream_timeout"`
}

type Server struct {
	Addr              string   `yaml:"addr"`
	AdminUsername     string   `yaml:"admin_username"`
	AdminPassword     string   `yaml:"admin_password"`
	AdminToken        string   `yaml:"admin_token"`
	MetricsToken      string   `yaml:"metrics_token"`
	MasterKey         string   `yaml:"master_key"`
	SessionTTL        Duration `yaml:"session_ttl"`
	SecureCookie      bool     `yaml:"secure_cookie"`
	TrustedProxies    []string `yaml:"trusted_proxies"`
	ReadHeaderTimeout Duration `yaml:"read_header_timeout"`
	ShutdownTimeout   Duration `yaml:"shutdown_timeout"`
	MaxBodyBytes      int64    `yaml:"max_body_bytes"`

	// AdminAllowedCIDRs restricts the whole admin surface (console API and
	// login). Empty means "everyone the listener can reach", which is only
	// acceptable behind a network boundary.
	AdminAllowedCIDRs []string `yaml:"admin_allowed_cidrs"`

	// AdminEnabled opens or closes the whole management surface. It is a
	// pointer so that "not configured" (open) can be told apart from an
	// explicit false. Changing it takes effect on the next config reload,
	// which is the emergency lever when the console is suspected to be
	// compromised.
	AdminEnabled *bool `yaml:"admin_enabled"`

	// KeyRPM is the per-gateway-key request budget per minute. Zero disables
	// the limiter.
	KeyRPM int `yaml:"key_rpm"`

	// KeyMaxConcurrent caps the number of in-flight requests of one key.
	// Zero disables the cap.
	KeyMaxConcurrent int `yaml:"key_max_concurrent"`

	TrustedPrefixes []netip.Prefix `yaml:"-"`

	// AdminPrefixes holds the parsed admin_allowed_cidrs entries. The admin
	// surface only answers requests whose source address is inside one of
	// them; an empty list means "everybody the listener can reach".
	AdminPrefixes []netip.Prefix `yaml:"-"`
}

// AdminIsEnabled reports whether the management surface is open. An unset
// admin_enabled means open, so a config file that predates the option keeps
// the previous behaviour.
func (s *Server) AdminIsEnabled() bool {
	return s.AdminEnabled == nil || *s.AdminEnabled
}

// boolPtr returns a pointer to v. It exists for the optional booleans in the
// config, where "not set" and "set to false" mean different things.
func boolPtr(v bool) *bool { return &v }

type Database struct {
	Path string `yaml:"path"`
	// RetentionDays prunes request logs, audit logs and daily usage rows
	// older than this many days. Zero keeps everything forever.
	RetentionDays int `yaml:"retention_days"`
}

type Log struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// Provider is the legacy single-upstream form. It is still accepted in
// config.yaml and is expanded into one instance plus its models on load.
type Provider struct {
	Name             string `yaml:"name"`
	Preset           string `yaml:"preset"`
	APIKey           string `yaml:"api_key"`
	Models           Models `yaml:"models"`
	OpenAIBaseURL    string `yaml:"openai_base_url"`
	AnthropicBaseURL string `yaml:"anthropic_base_url"`
}

// Models maps a public model name to the upstream model name. YAML accepts
// either a single string or a list; each entry is "name" or "name=upstream".
type Models map[string]string

// UnmarshalYAML accepts either a single model line or a list of them. Each
// line is "name" or "name=upstream".
func (m *Models) UnmarshalYAML(node *yaml.Node) error {
	var raw []string
	switch node.Kind {
	case yaml.ScalarNode:
		var s string
		if err := node.Decode(&s); err != nil {
			return err
		}
		raw = []string{s}
	case yaml.SequenceNode:
		if err := node.Decode(&raw); err != nil {
			return err
		}
	default:
		return fmt.Errorf("models must be a string or a list of strings")
	}
	parsed, err := ParseModels(raw)
	if err != nil {
		return err
	}
	*m = parsed
	return nil
}

// ParseModels turns "name" / "name=upstream" lines into a Models map.
func ParseModels(items []string) (Models, error) {
	out := make(Models, len(items))
	for _, raw := range items {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		publicName, upstream := line, line
		if i := strings.Index(line, "="); i >= 0 {
			publicName = strings.TrimSpace(line[:i])
			upstream = strings.TrimSpace(line[i+1:])
		}
		if publicName == "" || upstream == "" {
			return nil, fmt.Errorf("model %q: both the public name and upstream name are required", line)
		}
		if prev, dup := out[publicName]; dup && prev != upstream {
			return nil, fmt.Errorf("model %q is declared twice with different upstream names", publicName)
		}
		out[publicName] = upstream
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("models must not be empty")
	}
	return out, nil
}

// List renders models as sorted "name" / "name=upstream" lines.
func (m Models) List() []string {
	out := make([]string, 0, len(m))
	for publicName, upstream := range m {
		if publicName == upstream {
			out = append(out, publicName)
		} else {
			out = append(out, publicName+"="+upstream)
		}
	}
	sort.Strings(out)
	return out
}

func defaults() *Config {
	c := &Config{}
	c.Server.Addr = ":8080"
	c.Server.AdminUsername = "admin"
	c.Server.AdminEnabled = boolPtr(true)
	c.Server.SessionTTL = Duration(12 * time.Hour)
	c.Server.ReadHeaderTimeout = Duration(10 * time.Second)
	c.Server.ShutdownTimeout = Duration(30 * time.Second)
	c.Server.MaxBodyBytes = 32 << 20
	c.Database.Path = "./data/llmgw.db"
	c.Log.Level = "info"
	c.Log.Format = "json"
	c.Schedule.Timezone = DefaultTimezone
	c.UpstreamTimeout = Duration(DefaultTimeout)
	return c
}

var envPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

func expandEnv(s string) string {
	return envPattern.ReplaceAllStringFunc(s, func(m string) string {
		name := m[2 : len(m)-1]
		return os.Getenv(name)
	})
}

// Load reads and validates a config file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	c := defaults()
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil {
		msg := fmt.Errorf("parse %s: %w", path, err)
		if strings.Contains(err.Error(), "config.Provider") {
			msg = fmt.Errorf("%w; the legacy providers form now accepts name, api_key, models, "+
				"openai_base_url and anthropic_base_url (or preset); the old keys/endpoints form "+
				"was removed", msg)
		}
		if strings.Contains(err.Error(), "config.Instance") {
			msg = fmt.Errorf("%w; an instance accepts name, api_key, preset and a list of "+
				"endpoints ({protocol, base_url})", msg)
		}
		if strings.Contains(err.Error(), "field pricing not found in type config.Config") {
			msg = fmt.Errorf("%w; the optional pricing section was removed because the gateway "+
				"no longer estimates request cost, so delete it from the config file", msg)
		}
		return nil, msg
	}
	if err := c.normalize(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) normalize() error {
	rawPassword := c.Server.AdminPassword
	rawAdminToken := c.Server.AdminToken
	rawMetricsToken := c.Server.MetricsToken
	rawMasterKey := c.Server.MasterKey
	c.Server.AdminPassword = expandEnv(rawPassword)
	c.Server.AdminToken = expandEnv(rawAdminToken)
	c.Server.MetricsToken = expandEnv(rawMetricsToken)
	c.Server.MasterKey = expandEnv(rawMasterKey)
	if strings.Contains(rawPassword, "${") && c.Server.AdminPassword == "" {
		return fmt.Errorf("server.admin_password references an environment variable that is empty or unset")
	}
	if strings.Contains(rawAdminToken, "${") && c.Server.AdminToken == "" {
		return fmt.Errorf("server.admin_token references an environment variable that is empty or unset")
	}
	if strings.Contains(rawMetricsToken, "${") && c.Server.MetricsToken == "" {
		return fmt.Errorf("server.metrics_token references an environment variable that is empty or unset")
	}
	if strings.Contains(rawMasterKey, "${") && c.Server.MasterKey == "" {
		return fmt.Errorf("server.master_key references an environment variable that is empty or unset")
	}
	if c.Server.Addr == "" {
		c.Server.Addr = ":8080"
	}
	if c.Server.AdminUsername == "" {
		c.Server.AdminUsername = "admin"
	}
	if c.Server.AdminEnabled == nil {
		c.Server.AdminEnabled = boolPtr(true)
	}
	if c.Server.SessionTTL == 0 {
		c.Server.SessionTTL = Duration(12 * time.Hour)
	}
	for _, raw := range c.Server.TrustedProxies {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if !strings.Contains(raw, "/") {
			addr, err := netip.ParseAddr(raw)
			if err != nil {
				return fmt.Errorf("server.trusted_proxies: invalid address %q", raw)
			}
			c.Server.TrustedPrefixes = append(c.Server.TrustedPrefixes,
				netip.PrefixFrom(addr, addr.BitLen()))
			continue
		}
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return fmt.Errorf("server.trusted_proxies: invalid CIDR %q", raw)
		}
		c.Server.TrustedPrefixes = append(c.Server.TrustedPrefixes, prefix.Masked())
	}
	if c.Server.ShutdownTimeout == 0 {
		c.Server.ShutdownTimeout = Duration(30 * time.Second)
	}
	if c.Server.MaxBodyBytes <= 0 {
		c.Server.MaxBodyBytes = 32 << 20
	}
	if c.Server.KeyRPM < 0 {
		return fmt.Errorf("server.key_rpm must not be negative")
	}
	if c.Server.KeyMaxConcurrent < 0 {
		return fmt.Errorf("server.key_max_concurrent must not be negative")
	}
	for _, raw := range c.Server.AdminAllowedCIDRs {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if !strings.Contains(raw, "/") {
			addr, err := netip.ParseAddr(raw)
			if err != nil {
				return fmt.Errorf("server.admin_allowed_cidrs: invalid address %q", raw)
			}
			c.Server.AdminPrefixes = append(c.Server.AdminPrefixes,
				netip.PrefixFrom(addr, addr.BitLen()))
			continue
		}
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return fmt.Errorf("server.admin_allowed_cidrs: invalid CIDR %q", raw)
		}
		c.Server.AdminPrefixes = append(c.Server.AdminPrefixes, prefix.Masked())
	}
	if c.Database.Path == "" {
		return fmt.Errorf("database.path is required")
	}
	if c.Database.RetentionDays < 0 {
		return fmt.Errorf("database.retention_days must not be negative")
	}
	if c.UpstreamTimeout <= 0 {
		c.UpstreamTimeout = Duration(DefaultTimeout)
	}
	// The schedule timezone is resolved once here so that a typo fails at
	// load time instead of silently shifting every time window.
	loc, err := resolveLocation(c.Schedule.Timezone)
	if err != nil {
		return fmt.Errorf("schedule.timezone: %w", err)
	}
	c.Schedule.Location = loc
	// An empty topology is legal: the gateway starts with no upstream and the
	// operator adds the instances in the console. Refusing to start here
	// would turn a fresh deployment into a restart loop, because the console
	// is the only place where the first instance can be created. A model
	// without its instance is still rejected below, so a half-written
	// topology cannot slip through.

	for pi := range c.Providers {
		p := &c.Providers[pi]
		p.Name = strings.TrimSpace(expandEnv(p.Name))
		p.Preset = strings.TrimSpace(expandEnv(p.Preset))
		p.APIKey = strings.TrimSpace(expandEnv(p.APIKey))
		p.OpenAIBaseURL = strings.TrimSpace(expandEnv(p.OpenAIBaseURL))
		p.AnthropicBaseURL = strings.TrimSpace(expandEnv(p.AnthropicBaseURL))
	}
	if err := c.expandLegacyProviders(); err != nil {
		return err
	}

	seenInstance := map[string]bool{}
	for ii := range c.Instances {
		p := &c.Instances[ii]
		p.Name = strings.TrimSpace(expandEnv(p.Name))
		p.Preset = strings.TrimSpace(expandEnv(p.Preset))
		p.APIKey = strings.TrimSpace(expandEnv(p.APIKey))
		p.QuotaCredential.AccessKeyID = strings.TrimSpace(expandEnv(p.QuotaCredential.AccessKeyID))
		p.QuotaCredential.SecretAccessKey = strings.TrimSpace(expandEnv(p.QuotaCredential.SecretAccessKey))
		for ei := range p.Endpoints {
			p.Endpoints[ei].BaseURL = strings.TrimSpace(expandEnv(p.Endpoints[ei].BaseURL))
		}
		if err := NormalizeInstance(p); err != nil {
			return err
		}
		if seenInstance[p.Name] {
			return fmt.Errorf("duplicate instance %q", p.Name)
		}
		seenInstance[p.Name] = true
	}

	seenModel := map[string]bool{}
	for mi := range c.Models {
		m := &c.Models[mi]
		m.Name = strings.TrimSpace(expandEnv(m.Name))
		for ii := range m.Instances {
			m.Instances[ii].Name = strings.TrimSpace(expandEnv(m.Instances[ii].Name))
			m.Instances[ii].Upstream = strings.TrimSpace(expandEnv(m.Instances[ii].Upstream))
		}
		if err := NormalizeModel(m); err != nil {
			return err
		}
		if seenModel[m.Name] {
			return fmt.Errorf("duplicate model %q", m.Name)
		}
		seenModel[m.Name] = true
		for _, mi := range m.Instances {
			if !seenInstance[mi.Name] {
				return fmt.Errorf("model %q: instance %q does not exist", m.Name, mi.Name)
			}
		}
	}
	// Compiling the topology once here makes a bad declaration fail at load
	// time instead of at the first upstream call.
	if _, err := c.Effective(); err != nil {
		return err
	}
	return nil
}

// expandLegacyProviders turns the legacy provider list into the instance and
// model form. Old config files therefore keep working unchanged.
func (c *Config) expandLegacyProviders() error {
	for _, p := range c.Providers {
		inst, models, err := ExpandProvider(p)
		if err != nil {
			return err
		}
		c.Instances = append(c.Instances, inst)
		c.Models = append(c.Models, models...)
	}
	c.Providers = nil
	return nil
}

func normalizeBaseURL(owner, field, raw string, allowPrivate bool) (string, error) {
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("%s: %s must be an absolute http(s) URL, got %q",
			owner, field, raw)
	}
	if err := CheckUpstreamHost(owner, field, raw, allowPrivate); err != nil {
		return "", err
	}
	return strings.TrimRight(raw, "/"), nil
}

func normalizeModels(m Models) (Models, error) {
	if len(m) == 0 {
		return nil, fmt.Errorf("models must not be empty")
	}
	out := make(Models, len(m))
	for publicName, upstream := range m {
		name := strings.TrimSpace(publicName)
		up := strings.TrimSpace(upstream)
		if name == "" || up == "" {
			return nil, fmt.Errorf("model names must not be empty")
		}
		out[name] = up
	}
	return out, nil
}
