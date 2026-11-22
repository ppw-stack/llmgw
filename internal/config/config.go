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

	"github.com/pw/llmgw/internal/preset"
	"gopkg.in/yaml.v3"
)

// Protocols supported by the gateway. v1 does not translate between them.
const (
	ProtocolOpenAI    = "openai"
	ProtocolAnthropic = "anthropic"
)

// DefaultAnthropicVersion is sent to Anthropic-compatible upstreams.
const DefaultAnthropicVersion = "2023-06-01"

// DefaultTimeout bounds non-streaming upstream calls and the idle gap of a
// streaming response. It is deliberately not configurable per provider.
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
	Server    Server           `yaml:"server"`
	Database  Database         `yaml:"database"`
	Log       Log              `yaml:"log"`
	Providers []Provider       `yaml:"providers"`
	Pricing   map[string]Price `yaml:"pricing"`
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

	TrustedPrefixes []netip.Prefix `yaml:"-"`
}

type Database struct {
	Path string `yaml:"path"`
}

type Log struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

// Provider is either a built-in preset (Preset set) or a custom upstream
// (openai_base_url / anthropic_base_url set). The same model list is exposed on
// both protocol entries; a code plan offers the same models through both.
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

type Price struct {
	InputPerMTok     float64 `yaml:"input_per_mtok"`
	OutputPerMTok    float64 `yaml:"output_per_mtok"`
	CacheReadPerMTok float64 `yaml:"cache_read_per_mtok"`
}

func defaults() *Config {
	c := &Config{}
	c.Server.Addr = ":8080"
	c.Server.AdminUsername = "admin"
	c.Server.SessionTTL = Duration(12 * time.Hour)
	c.Server.ReadHeaderTimeout = Duration(10 * time.Second)
	c.Server.ShutdownTimeout = Duration(30 * time.Second)
	c.Server.MaxBodyBytes = 32 << 20
	c.Database.Path = "./data/llmgw.db"
	c.Log.Level = "info"
	c.Log.Format = "json"
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
			msg = fmt.Errorf("%w; providers now accept api_key, models, openai_base_url and "+
				"anthropic_base_url (or preset); the old keys/endpoints form was removed", msg)
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
	if c.Database.Path == "" {
		return fmt.Errorf("database.path is required")
	}
	if len(c.Providers) == 0 {
		return fmt.Errorf("at least one provider is required")
	}

	seen := map[string]bool{}
	for pi := range c.Providers {
		p := &c.Providers[pi]
		p.Name = strings.TrimSpace(expandEnv(p.Name))
		p.Preset = strings.TrimSpace(expandEnv(p.Preset))
		p.APIKey = strings.TrimSpace(expandEnv(p.APIKey))
		p.OpenAIBaseURL = strings.TrimSpace(expandEnv(p.OpenAIBaseURL))
		p.AnthropicBaseURL = strings.TrimSpace(expandEnv(p.AnthropicBaseURL))
		if err := NormalizeProvider(p); err != nil {
			return err
		}
		if seen[p.Name] {
			return fmt.Errorf("duplicate provider %q", p.Name)
		}
		seen[p.Name] = true
	}
	return nil
}

// NormalizeProvider applies defaults and validates one provider. It does not
// expand environment variables; that is only meaningful for config.yaml input.
func NormalizeProvider(p *Provider) error {
	p.Name = strings.TrimSpace(p.Name)
	if p.Name == "" {
		return fmt.Errorf("provider name is required")
	}
	p.Preset = strings.TrimSpace(p.Preset)
	p.APIKey = strings.TrimSpace(p.APIKey)
	p.OpenAIBaseURL = strings.TrimSpace(p.OpenAIBaseURL)
	p.AnthropicBaseURL = strings.TrimSpace(p.AnthropicBaseURL)

	if p.Preset != "" {
		pr, ok := preset.Lookup(p.Preset)
		if !ok {
			return fmt.Errorf("provider %q: unknown preset %q", p.Name, p.Preset)
		}
		if p.OpenAIBaseURL != "" || p.AnthropicBaseURL != "" {
			return fmt.Errorf("provider %q: preset %q supplies its own base URLs; "+
				"remove openai_base_url/anthropic_base_url or use a custom provider", p.Name, p.Preset)
		}
		if len(p.Models) == 0 {
			models, err := ParseModels(pr.Models)
			if err != nil {
				return fmt.Errorf("provider %q: preset %q: %w", p.Name, p.Preset, err)
			}
			p.Models = models
		}
	} else {
		if p.OpenAIBaseURL == "" && p.AnthropicBaseURL == "" {
			return fmt.Errorf("provider %q: at least one of openai_base_url or anthropic_base_url is required", p.Name)
		}
	}

	var err error
	if p.OpenAIBaseURL, err = normalizeBaseURL(p.Name, "openai_base_url", p.OpenAIBaseURL); err != nil {
		return err
	}
	if p.AnthropicBaseURL, err = normalizeBaseURL(p.Name, "anthropic_base_url", p.AnthropicBaseURL); err != nil {
		return err
	}
	if p.Models, err = normalizeModels(p.Models); err != nil {
		return fmt.Errorf("provider %q: %w", p.Name, err)
	}
	return nil
}

func normalizeBaseURL(provider, field, raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("provider %q: %s must be an absolute http(s) URL, got %q",
			provider, field, raw)
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

// Effective is the routing-ready form of a provider.
type Effective struct {
	Name             string
	Preset           string
	APIKey           string
	Models           Models
	OpenAIBaseURL    string
	AnthropicBaseURL string
}

// Effective resolves presets and applies the shared defaults. It is used both
// when building the router and when rendering the admin API. On error the
// returned Effective is the zero value and the error already carries the
// provider context, so callers can report it as is.
func (p Provider) Effective() (Effective, error) {
	out := Effective{
		Name:             p.Name,
		Preset:           p.Preset,
		APIKey:           p.APIKey,
		Models:           p.Models,
		OpenAIBaseURL:    strings.TrimRight(strings.TrimSpace(p.OpenAIBaseURL), "/"),
		AnthropicBaseURL: strings.TrimRight(strings.TrimSpace(p.AnthropicBaseURL), "/"),
	}
	if p.Preset != "" {
		pr, ok := preset.Lookup(p.Preset)
		if !ok {
			return Effective{}, fmt.Errorf("provider %q: unknown preset %q", p.Name, p.Preset)
		}
		out.OpenAIBaseURL = strings.TrimRight(pr.OpenAIBaseURL, "/")
		out.AnthropicBaseURL = strings.TrimRight(pr.AnthropicBaseURL, "/")
		if len(out.Models) == 0 {
			models, err := ParseModels(pr.Models)
			if err != nil {
				return Effective{}, fmt.Errorf("provider %q: preset %q: %w", p.Name, p.Preset, err)
			}
			out.Models = models
		}
	}
	if out.OpenAIBaseURL == "" && out.AnthropicBaseURL == "" {
		return Effective{}, fmt.Errorf("provider %q: at least one upstream base URL is required", p.Name)
	}
	models, err := normalizeModels(out.Models)
	if err != nil {
		return Effective{}, fmt.Errorf("provider %q: %w", p.Name, err)
	}
	out.Models = models
	return out, nil
}
