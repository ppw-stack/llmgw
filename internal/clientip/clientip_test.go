package clientip

import (
	"net/http"
	"net/netip"
	"testing"
)

func req(remote, xff string) *http.Request {
	r, _ := http.NewRequest(http.MethodGet, "/", nil)
	r.RemoteAddr = remote
	if xff != "" {
		r.Header.Set("X-Forwarded-For", xff)
	}
	return r
}

func prefixes(t *testing.T, cidrs ...string) []netip.Prefix {
	t.Helper()
	var out []netip.Prefix
	for _, c := range cidrs {
		p, err := netip.ParsePrefix(c)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	return out
}

func TestIgnoresForwardedHeaderFromUntrustedPeer(t *testing.T) {
	got := Resolve(req("203.0.113.9:1234", "1.2.3.4"), nil)
	if got != "203.0.113.9" {
		t.Fatalf("got %q", got)
	}
}

func TestUsesForwardedHeaderFromTrustedPeer(t *testing.T) {
	trusted := prefixes(t, "10.0.0.0/8")
	got := Resolve(req("10.0.0.5:1234", "1.2.3.4"), trusted)
	if got != "1.2.3.4" {
		t.Fatalf("got %q", got)
	}
}

func TestSkipsTrustedHops(t *testing.T) {
	trusted := prefixes(t, "10.0.0.0/8")
	got := Resolve(req("10.0.0.5:1234", "1.2.3.4, 10.0.0.7"), trusted)
	if got != "1.2.3.4" {
		t.Fatalf("got %q", got)
	}
}

func TestSingleIPTrusted(t *testing.T) {
	trusted := []netip.Prefix{netip.MustParsePrefix("10.0.0.5/32")}
	got := Resolve(req("10.0.0.5:1234", "198.51.100.7"), trusted)
	if got != "198.51.100.7" {
		t.Fatalf("got %q", got)
	}
}

func TestIPv4MappedPeerIsUnmapped(t *testing.T) {
	trusted := prefixes(t, "127.0.0.1/32")
	got := Resolve(req("[::ffff:127.0.0.1]:1234", "1.2.3.4"), trusted)
	if got != "1.2.3.4" {
		t.Fatalf("got %q", got)
	}
}

func TestMalformedForwardedEntriesAreIgnored(t *testing.T) {
	trusted := prefixes(t, "10.0.0.0/8")
	got := Resolve(req("10.0.0.5:1234", "not-an-ip, 1.2.3.4"), trusted)
	if got != "1.2.3.4" {
		t.Fatalf("got %q", got)
	}
}
