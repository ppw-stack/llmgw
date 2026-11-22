package config

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Protocols supported by the gateway. v1 does not translate between them.
const (
	ProtocolOpenAI    = "openai"
	ProtocolAnthropic = "anthropic"
)

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
	AdminToken        string   `yaml:"admin_token"`
	ReadHeaderTimeout Duration `yaml:"read_header_timeout"`
	ShutdownTimeout   Duration `yaml:"shutdown_timeout"`
	MaxBodyBytes      int64    `yaml:"max_body_bytes"`
}

type Database struct {
	Path string `yaml:"path"`
}

type Log struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"`
}

type Provider struct {
	Name      string     `yaml:"name"`
	Keys      []string   `yaml:"keys"`
	Timeout   Duration   `yaml:"timeout"`
	Proxy     string     `yaml:"proxy"`
	Endpoints []Endpoint `yaml:"endpoints"`
}

type Endpoint struct {
	Name     string            `yaml:"name"`
	Protocol string            `yaml:"protocol"`
	BaseURL  string            `yaml:"base_url"`
	Version  string            `yaml:"version"`
	Models   map[string]string `yaml:"models"`
}

type Price struct {
	InputPerMTok     float64 `yaml:"input_per_mtok"`
	OutputPerMTok    float64 `yaml:"output_per_mtok"`
	CacheReadPerMTok float64 `yaml:"cache_read_per_mtok"`
}

func defaults() *Config {
	c := &Config{}
	c.Server.Addr = ":8080"
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
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if err := c.normalize(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) normalize() error {
	if c.Server.Addr == "" {
		c.Server.Addr = ":8080"
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
		if p.Name == "" {
			return fmt.Errorf("providers[%d]: name is required", pi)
		}
		if seen[p.Name] {
			return fmt.Errorf("duplicate provider %q", p.Name)
		}
		seen[p.Name] = true
		if p.Timeout == 0 {
			p.Timeout = Duration(120 * time.Second)
		}
		for ki := range p.Keys {
			p.Keys[ki] = expandEnv(strings.TrimSpace(p.Keys[ki]))
		}
		// Unset ${ENV} variables expand to "", which would otherwise be treated
		// as a valid (empty) key. Drop them instead.
		kept := p.Keys[:0]
		for _, k := range p.Keys {
			if k != "" {
				kept = append(kept, k)
			}
		}
		p.Keys = kept
		if len(p.Endpoints) == 0 {
			return fmt.Errorf("provider %q: at least one endpoint is required", p.Name)
		}
		epSeen := map[string]bool{}
		for ei := range p.Endpoints {
			ep := &p.Endpoints[ei]
			if ep.Name == "" {
				ep.Name = ep.Protocol
			}
			if epSeen[ep.Name] {
				return fmt.Errorf("provider %q: duplicate endpoint %q", p.Name, ep.Name)
			}
			epSeen[ep.Name] = true
			switch ep.Protocol {
			case ProtocolOpenAI, ProtocolAnthropic:
			default:
				return fmt.Errorf("provider %q endpoint %q: protocol must be %q or %q",
					p.Name, ep.Name, ProtocolOpenAI, ProtocolAnthropic)
			}
			u, err := url.Parse(ep.BaseURL)
			if err != nil || u.Scheme == "" || u.Host == "" {
				return fmt.Errorf("provider %q endpoint %q: invalid base_url %q",
					p.Name, ep.Name, ep.BaseURL)
			}
			ep.BaseURL = strings.TrimRight(ep.BaseURL, "/")
			if ep.Protocol == ProtocolAnthropic && ep.Version == "" {
				ep.Version = "2023-06-01"
			}
			if len(ep.Models) == 0 {
				return fmt.Errorf("provider %q endpoint %q: models must not be empty",
					p.Name, ep.Name)
			}
		}
	}
	return nil
}
