package netproxy

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"testing"
)

// proxyTestPassword returns the proxy password used by the netproxy tests.
// A random one-off password is generated at runtime so no credential literal
// is stored in the repository. The generated value is cached so every call in
// the same test binary returns the same password.
func proxyTestPassword() string {
	proxyTestPasswordOnce.Do(func() {
		buf := make([]byte, 18)
		if _, err := rand.Read(buf); err != nil {
			panic(fmt.Sprintf("generate the random proxy test password: %v", err))
		}
		proxyTestPasswordValue = base64.RawURLEncoding.EncodeToString(buf)
	})
	return proxyTestPasswordValue
}

var (
	proxyTestPasswordOnce  sync.Once
	proxyTestPasswordValue string
)

// A proxy address that cannot be split must report which address failed, so
// the caller can tell what went wrong.
func TestDialProxySafeWrapsSplitError(t *testing.T) {
	t.Parallel()

	dialer := &net.Dialer{}
	_, err := DialProxySafe(context.Background(), dialer, "tcp", "missing-port")
	if err == nil {
		t.Fatal("DialProxySafe succeeded with an address without a port")
	}
	if !strings.Contains(err.Error(), "missing-port") {
		t.Fatalf("error does not mention the address: %v", err)
	}
	var addrErr *net.AddrError
	if !errors.As(err, &addrErr) {
		t.Fatalf("error does not keep the original error chain: %v", err)
	}
}

// SanitizeError masks the proxy password in an error message.
func TestSanitizeErrorMasksPassword(t *testing.T) {
	t.Parallel()

	proxyPassword := proxyTestPassword()
	proxyURL, err := url.Parse((&url.URL{
		Scheme: "http",
		User:   url.UserPassword("alice", proxyPassword),
		Host:   "127.0.0.1:7890",
	}).String())
	if err != nil {
		t.Fatal(err)
	}
	got := SanitizeError(errors.New("dial failed for "+proxyPassword), proxyURL)
	if strings.Contains(got, proxyPassword) {
		t.Fatalf("error leaked the proxy password: %q", got)
	}
}
