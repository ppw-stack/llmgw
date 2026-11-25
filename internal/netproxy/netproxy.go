// Package netproxy shares per-request outbound proxy selection and safe proxy
// dialing across the data-plane forwarder, quota client and model discovery.
package netproxy

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/pw/llmgw/internal/config"
)

type proxyURLKey struct{}

// WithURL attaches an explicit outbound proxy URL to ctx.
func WithURL(ctx context.Context, proxyURL *url.URL) context.Context {
	if proxyURL == nil {
		return ctx
	}
	return context.WithValue(ctx, proxyURLKey{}, proxyURL)
}

// FromContext returns the explicit outbound proxy URL attached to ctx, if any.
func FromContext(ctx context.Context) (*url.URL, bool) {
	u, ok := ctx.Value(proxyURLKey{}).(*url.URL)
	return u, ok && u != nil
}

// ProxyFunc resolves the outbound proxy for req. An instance-level proxy
// attached to req.Context() wins; otherwise the process environment variables
// (HTTP_PROXY / HTTPS_PROXY / NO_PROXY) apply.
func ProxyFunc(req *http.Request) (*url.URL, error) {
	if u, ok := FromContext(req.Context()); ok {
		return u, nil
	}
	return http.ProxyFromEnvironment(req)
}

// DialProxySafe resolves a proxy server address and refuses link-local
// (including cloud metadata), unspecified and multicast addresses while
// permitting loopback and private LAN proxies.
func DialProxySafe(ctx context.Context, dialer *net.Dialer, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	for _, ip := range ips {
		if config.BlockedProxyAddress(ip) {
			continue
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
	}
	return nil, fmt.Errorf("proxy host %q resolves only to blocked addresses", host)
}

// NewTransport builds an http.Transport that honours per-request context
// proxies and guards proxy dials against link-local and metadata addresses.
func NewTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		Proxy:                 ProxyFunc,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		ExpectContinueTimeout: time.Second,
		ForceAttemptHTTP2:     true,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			if _, ok := FromContext(ctx); ok {
				return DialProxySafe(ctx, dialer, network, addr)
			}
			return dialer.DialContext(ctx, network, addr)
		},
	}
}

// SanitizeError removes any proxy password from an error message so it cannot
// leak into logs or admin API responses.
func SanitizeError(err error, proxyURL *url.URL) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if proxyURL != nil && proxyURL.User != nil {
		if pass, ok := proxyURL.User.Password(); ok && pass != "" {
			msg = strings.ReplaceAll(msg, pass, "****")
		}
	}
	return msg
}
