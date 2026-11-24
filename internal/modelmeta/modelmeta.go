// Package modelmeta defines the optional metadata a gateway model advertises
// on the model-list endpoints and in the management console.
//
// The metadata is declared by the operator. It is never derived from the
// upstream response, because vendors return different shapes (most only
// return the model id) and an alias may point at a different upstream model.
package modelmeta

import "strings"

// Capability names understood by the gateway and the console. The list is
// documented but not enforced: an operator may add a vendor-specific tag.
const (
	CapTools     = "tools"
	CapVision    = "vision"
	CapVideo     = "video"
	CapAudio     = "audio"
	CapReasoning = "reasoning"
	CapJSON      = "json"
	CapCache     = "cache"
	CapSearch    = "search"
	CapDoc       = "doc"
)

// Metadata is the optional description of one logical model.
type Metadata struct {
	// DisplayName is the human-readable name shown by clients. It defaults to
	// the model id on the client side when empty.
	DisplayName string `json:"display_name,omitempty" yaml:"display_name,omitempty"`
	// ContextLength is the maximum prompt plus completion token budget the
	// vendor advertises.
	ContextLength int `json:"context_length,omitempty" yaml:"context_length,omitempty"`
	// MaxOutputTokens is the vendor's maximum completion length.
	MaxOutputTokens int `json:"max_output_tokens,omitempty" yaml:"max_output_tokens,omitempty"`
	// Capabilities lists the protocol-independent capabilities. See the Cap*
	// constants for the tags the console offers.
	Capabilities []string `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
	// Protocols overrides the fields above for one protocol. The key is a
	// protocol name such as "openai" or "anthropic".
	Protocols map[string]Override `json:"protocols,omitempty" yaml:"protocols,omitempty"`
}

// Override replaces individual fields of Metadata for one protocol. A zero
// number or an empty capability list means "inherit the common value".
type Override struct {
	ContextLength   int      `json:"context_length,omitempty" yaml:"context_length,omitempty"`
	MaxOutputTokens int      `json:"max_output_tokens,omitempty" yaml:"max_output_tokens,omitempty"`
	Capabilities    []string `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
}

// IsZero reports whether nothing is declared, so the model-list endpoint can
// keep its minimal OpenAI-compatible shape.
func (m Metadata) IsZero() bool {
	return m.DisplayName == "" && m.ContextLength == 0 && m.MaxOutputTokens == 0 &&
		len(m.Capabilities) == 0 && len(m.Protocols) == 0
}

// For returns the metadata expanded for one protocol. The protocol map is
// cleared because the caller only needs the effective values.
func (m Metadata) For(protocol string) Metadata {
	out := m.Clone()
	out.Protocols = nil
	ov, ok := m.Protocols[strings.ToLower(strings.TrimSpace(protocol))]
	if !ok {
		return out
	}
	if ov.ContextLength > 0 {
		out.ContextLength = ov.ContextLength
	}
	if ov.MaxOutputTokens > 0 {
		out.MaxOutputTokens = ov.MaxOutputTokens
	}
	if ov.Capabilities != nil {
		out.Capabilities = append([]string(nil), ov.Capabilities...)
	}
	return out
}

// Merge fills the unset fields of m from fallback. Explicit values always
// win, including capabilities: an empty list means "not declared" because
// Normalize turns an empty list into nil.
func (m Metadata) Merge(fallback Metadata) Metadata {
	out := m.Clone()
	if out.DisplayName == "" {
		out.DisplayName = fallback.DisplayName
	}
	if out.ContextLength == 0 {
		out.ContextLength = fallback.ContextLength
	}
	if out.MaxOutputTokens == 0 {
		out.MaxOutputTokens = fallback.MaxOutputTokens
	}
	if out.Capabilities == nil {
		out.Capabilities = append([]string(nil), fallback.Capabilities...)
	}
	if len(fallback.Protocols) == 0 {
		return out
	}
	if out.Protocols == nil {
		out.Protocols = make(map[string]Override, len(fallback.Protocols))
	}
	for name, fb := range fallback.Protocols {
		cur, ok := out.Protocols[name]
		if !ok {
			out.Protocols[name] = fb.Clone()
			continue
		}
		if cur.ContextLength == 0 {
			cur.ContextLength = fb.ContextLength
		}
		if cur.MaxOutputTokens == 0 {
			cur.MaxOutputTokens = fb.MaxOutputTokens
		}
		if cur.Capabilities == nil {
			cur.Capabilities = append([]string(nil), fb.Capabilities...)
		}
		out.Protocols[name] = cur
	}
	return out
}

// Clone returns a deep copy so callers cannot mutate shared declarations.
func (m Metadata) Clone() Metadata {
	out := m
	out.Capabilities = append([]string(nil), m.Capabilities...)
	if len(m.Protocols) > 0 {
		out.Protocols = make(map[string]Override, len(m.Protocols))
		for name, ov := range m.Protocols {
			out.Protocols[name] = ov.Clone()
		}
	}
	return out
}

// Clone returns a deep copy of the override.
func (o Override) Clone() Override {
	out := o
	out.Capabilities = append([]string(nil), o.Capabilities...)
	return out
}

// Normalize trims and lower-cases the declared values and drops duplicates.
// An empty capability list becomes nil, meaning "inherit".
func (m *Metadata) Normalize() {
	m.DisplayName = strings.TrimSpace(m.DisplayName)
	if m.ContextLength < 0 {
		m.ContextLength = 0
	}
	if m.MaxOutputTokens < 0 {
		m.MaxOutputTokens = 0
	}
	m.Capabilities = normalizeCapabilities(m.Capabilities)
	if len(m.Protocols) == 0 {
		m.Protocols = nil
		return
	}
	out := make(map[string]Override, len(m.Protocols))
	for name, ov := range m.Protocols {
		key := strings.ToLower(strings.TrimSpace(name))
		if key == "" {
			continue
		}
		if ov.ContextLength < 0 {
			ov.ContextLength = 0
		}
		if ov.MaxOutputTokens < 0 {
			ov.MaxOutputTokens = 0
		}
		ov.Capabilities = normalizeCapabilities(ov.Capabilities)
		out[key] = ov
	}
	if len(out) == 0 {
		m.Protocols = nil
		return
	}
	m.Protocols = out
}

func normalizeCapabilities(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, raw := range in {
		capability := strings.ToLower(strings.TrimSpace(raw))
		if capability == "" || seen[capability] {
			continue
		}
		seen[capability] = true
		out = append(out, capability)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
