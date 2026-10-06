// Package testnet provides a process-wide outbound network guard for tests.
//
// Production code falls back to http.DefaultClient in several places (the
// Responses HTTP client, the Responses chat client, the tokenize counter and the
// MCP tool executor). A test that forgets to inject an HTTP client therefore
// performs a real outbound request: at worst it spends API quota against a live
// provider, at best it hangs until the DNS or HTTP timeout expires. Installing
// this guard from TestMain turns that into an immediate, explicit failure.
package testnet

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
)

// AllowEnv makes the guard a no-op when set to a truthy value, for the small
// number of suites that intentionally reach a real endpoint.
const AllowEnv = "TRAJECTA_TEST_ALLOW_NETWORK"

// InstallLoopbackGuard replaces http.DefaultTransport so any outbound request
// to a host that is not loopback fails immediately with a descriptive error.
// Call it first thing in TestMain.
func InstallLoopbackGuard() {
	base := http.DefaultTransport
	http.DefaultTransport = GuardTransport{Base: base}
	if c := http.DefaultClient; c != nil {
		if c.Transport == nil {
			c.Transport = http.DefaultTransport
		}
	}
}

// GuardTransport is an http.RoundTripper that blocks non-loopback destinations.
type GuardTransport struct {
	Base http.RoundTripper
}

// RoundTrip implements http.RoundTripper.
func (g GuardTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if AllowNetwork() {
		return g.base().RoundTrip(req)
	}
	if req == nil || req.URL == nil {
		return nil, fmt.Errorf("testnet: refusing a request without a URL")
	}
	host := req.URL.Hostname()
	if isLoopback(host) {
		return g.base().RoundTrip(req)
	}
	return nil, fmt.Errorf(
		"testnet: outbound network access to %q (%s %s) is blocked in tests; "+
			"inject an HTTP client pointing at a test server, or set %s=1 to opt out",
		host, req.Method, req.URL.Redacted(), AllowEnv)
}

func (g GuardTransport) base() http.RoundTripper {
	if g.Base != nil {
		return g.Base
	}
	return http.DefaultTransport
}

// AllowNetwork reports whether the guard is disabled for this process.
func AllowNetwork() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(AllowEnv))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func isLoopback(host string) bool {
	if host == "" {
		return true
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}
