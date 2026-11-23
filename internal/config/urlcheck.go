package config

import (
	"fmt"
	"net/netip"
	"net/url"
	"strings"
)

// BlockedAddress reports whether an upstream address points at something that
// is only reachable from the host itself or from its private network: loopback,
// RFC 1918, link-local (including the cloud metadata service), CGNAT,
// benchmarking ranges, multicast and reserved space.
//
// The gateway forwards requests to whatever an operator configures, so an
// unrestricted URL turns a stolen admin session into an internal-network
// proxy. Instances can opt out with allow_private_upstream for local mocks.
func BlockedAddress(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() {
		return true
	}
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsUnspecified() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() || addr.IsInterfaceLocalMulticast() {
		return true
	}
	for _, prefix := range blockedPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return !addr.IsGlobalUnicast()
}

var blockedPrefixes = []netip.Prefix{
	// "This network" and IETF protocol assignments.
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("192.0.0.0/24"),
	// Carrier-grade NAT.
	netip.MustParsePrefix("100.64.0.0/10"),
	// Benchmarking.
	netip.MustParsePrefix("198.18.0.0/15"),
	// Reserved for future use.
	netip.MustParsePrefix("240.0.0.0/4"),
	// Documentation ranges never host a real upstream.
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	// IPv6 documentation.
	netip.MustParsePrefix("2001:db8::/32"),
}

// blockedHostnames are names that resolve to the local host or to a cloud
// metadata endpoint. A DNS lookup cannot be trusted at validation time
// (rebinding), so the transport re-checks the resolved address on dial.
var blockedHostnames = []string{
	"localhost",
	"metadata",
	"instance-data",
	"metadata.google.internal",
}

// LooksPrivateHost reports whether a URL host names the local machine or a
// special-use domain. IP literals are checked with BlockedAddress.
func LooksPrivateHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if host == "" {
		return true
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return BlockedAddress(addr)
	}
	for _, name := range blockedHostnames {
		if host == name {
			return true
		}
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".localdomain"} {
		if strings.HasSuffix(host, suffix) {
			return true
		}
	}
	return false
}

// CheckUpstreamHost rejects an upstream URL that points inside the machine or
// the private network unless the instance explicitly allows it.
func CheckUpstreamHost(owner, field, raw string, allowPrivate bool) error {
	if allowPrivate || raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%s: %s is not a valid URL: %q: %w", owner, field, raw, err)
	}
	if LooksPrivateHost(u.Hostname()) {
		return fmt.Errorf("%s: %s points at a loopback, private or metadata host %q; "+
			"set allow_private_upstream: true on this instance if that is intentional",
			owner, field, u.Hostname())
	}
	return nil
}
