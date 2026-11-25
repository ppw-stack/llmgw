package netproxy

import (
	"context"
	"errors"
	"net"
	"net/url"
	"strings"
	"testing"
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

	proxyURL, err := url.Parse("http://alice:secret-value@127.0.0.1:7890")
	if err != nil {
		t.Fatal(err)
	}
	got := SanitizeError(errors.New("dial failed for secret-value"), proxyURL)
	if strings.Contains(got, "secret-value") {
		t.Fatalf("error leaked the proxy password: %q", got)
	}
}
