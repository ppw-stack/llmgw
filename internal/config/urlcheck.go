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

// BlockedProxyAddress reports whether a proxy server address is unsafe to dial.
// Unlike upstream endpoints, a proxy commonly lives on localhost or the private
// network (for example 127.0.0.1:7890 or a LAN proxy), so loopback and RFC 1918
// / IPv6 ULA ranges are allowed. Link-local (including cloud metadata
// 169.254.169.254 and fe80::/10), unspecified, and multicast addresses are
// still rejected.
func BlockedProxyAddress(addr netip.Addr) bool {
	addr = addr.Unmap()
	if !addr.IsValid() {
		return true
	}
	if addr.IsUnspecified() ||
		addr.IsLinkLocalUnicast() || addr.IsLinkLocalMulticast() ||
		addr.IsMulticast() || addr.IsInterfaceLocalMulticast() {
		return true
	}
	return false
}

// blockedProxyHostnames are hostnames that must never be used as a proxy
// target, such as cloud instance metadata services.
var blockedProxyHostnames = []string{
	"metadata",
	"instance-data",
	"metadata.google.internal",
}

// LooksBlockedProxyHost reports whether a proxy URL host points at a cloud
// metadata hostname or a blocked IP literal.
func LooksBlockedProxyHost(host string) bool {
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if host == "" {
		return true
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		return BlockedProxyAddress(addr)
	}
	for _, name := range blockedProxyHostnames {
		if host == name {
			return true
		}
	}
	return false
}

// NormalizeProxyURL validates an optional instance proxy URL. Supported schemes
// match Go's net/http.Transport.Proxy: http, https, socks5, and socks5h.
func NormalizeProxyURL(owner, field, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		// url.Parse returns a *url.Error whose message repeats the whole raw
		// URL, including the userinfo. Unwrap it so a proxy password never
		// reaches a log line or an admin API response.
		if uerr, ok := err.(*url.Error); ok {
			err = uerr.Err
		}
		return "", fmt.Errorf("%s: %s is not a valid URL: %w", owner, field, err)
	}
	scheme := strings.ToLower(u.Scheme)
	switch scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return "", fmt.Errorf("%s: %s scheme %q is not supported (want http, https, socks5 or socks5h)",
			owner, field, u.Scheme)
	}
	if u.Host == "" || u.Hostname() == "" {
		return "", fmt.Errorf("%s: %s must include a host", owner, field)
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("%s: %s must not contain a path, query or fragment", owner, field)
	}
	if LooksBlockedProxyHost(u.Hostname()) {
		return "", fmt.Errorf("%s: %s points at a blocked link-local or metadata host %q",
			owner, field, u.Hostname())
	}
	u.Scheme = scheme
	u.Path = ""
	return u.String(), nil
}

// RedactProxyURL masks any password in a proxy URL for safe display in the
// admin console and logs. hasAuth is true when the URL carries a password.
func RedactProxyURL(raw string) (redacted string, hasAuth bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	if u.User == nil {
		return u.String(), false
	}
	if _, ok := u.User.Password(); !ok {
		return u.String(), false
	}
	user := u.User.Username()
	if user != "" {
		return u.Scheme + "://" + user + ":****@" + u.Host, true
	}
	return u.Scheme + "://****@" + u.Host, true
}
