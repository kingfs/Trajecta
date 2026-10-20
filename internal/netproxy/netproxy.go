// Package netproxy owns the outbound proxy policy of the server.
//
// The policy is stored in the application database (app_settings key
// "network.proxy") and edited from the Monitor's System page. It follows the
// Linux convention operators already know from the http_proxy, https_proxy and
// no_proxy environment variables:
//
//   - http_proxy proxies plain HTTP requests
//   - https_proxy proxies HTTPS requests and defaults to http_proxy
//   - no_proxy is a comma-separated bypass list of host names, domain suffixes,
//     IP addresses, CIDR blocks and optional ports
//
// The policy is deliberately explicit: when the switch is off no outbound
// request is proxied, even if the process environment carries proxy variables.
// A proxied deployment is configured in one place, and an internal upstream
// cannot unexpectedly leave through a corporate proxy because of the shell that
// started the process.
package netproxy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// SettingKey is the app_settings row the Monitor writes and the server reads.
const SettingKey = "network.proxy"

// Settings is the persisted proxy policy.
type Settings struct {
	Enabled    bool   `json:"enabled"`
	HTTPProxy  string `json:"http_proxy"`
	HTTPSProxy string `json:"https_proxy"`
	NoProxy    string `json:"no_proxy"`
}

// DefaultSettings is the off state: every outbound request goes direct.
func DefaultSettings() Settings {
	return Settings{}
}

// Normalize trims the user-editable fields. It does not add a scheme to a proxy
// address; Compile does that when it parses the value.
func (s Settings) Normalize() Settings {
	return Settings{
		Enabled:    s.Enabled,
		HTTPProxy:  strings.TrimSpace(s.HTTPProxy),
		HTTPSProxy: strings.TrimSpace(s.HTTPSProxy),
		NoProxy:    strings.TrimSpace(s.NoProxy),
	}
}

// EffectiveHTTPSProxy is what an HTTPS request actually uses. An empty
// https_proxy means "same as http_proxy", which is the Linux default operators
// expect; an explicitly configured value wins.
func (s Settings) EffectiveHTTPSProxy() string {
	if strings.TrimSpace(s.HTTPSProxy) != "" {
		return strings.TrimSpace(s.HTTPSProxy)
	}
	return strings.TrimSpace(s.HTTPProxy)
}

// Resolver is an immutable compiled policy. A *Resolver is safe to share and is
// swapped atomically by Manager on every update.
type Resolver struct {
	settings   Settings
	httpProxy  *url.URL
	httpsProxy *url.URL
	noProxy    noProxyList
}

// Compile validates settings and turns them into a resolver.
func Compile(settings Settings) (*Resolver, error) {
	normalized := settings.Normalize()
	httpProxy, err := parseProxyURL(normalized.HTTPProxy)
	if err != nil {
		return nil, fmt.Errorf("http_proxy: %w", err)
	}
	httpsProxy, err := parseProxyURL(normalized.EffectiveHTTPSProxy())
	if err != nil {
		return nil, fmt.Errorf("https_proxy: %w", err)
	}
	noProxy, err := parseNoProxy(normalized.NoProxy)
	if err != nil {
		return nil, fmt.Errorf("no_proxy: %w", err)
	}
	if normalized.Enabled && httpProxy == nil && httpsProxy == nil {
		return nil, errors.New("http_proxy or https_proxy is required when the proxy is enabled")
	}
	// Persist the canonical form (with a scheme and an explicit port) so the
	// settings page round-trips exactly what the transport will use. An empty
	// https_proxy stays empty: that is what makes it follow http_proxy.
	if httpProxy != nil {
		normalized.HTTPProxy = httpProxy.String()
	}
	if normalized.HTTPSProxy != "" && httpsProxy != nil {
		normalized.HTTPSProxy = httpsProxy.String()
	}
	return &Resolver{
		settings:   normalized,
		httpProxy:  httpProxy,
		httpsProxy: httpsProxy,
		noProxy:    noProxy,
	}, nil
}

// Settings returns the normalized policy the resolver was built from.
func (r *Resolver) Settings() Settings {
	if r == nil {
		return DefaultSettings()
	}
	return r.settings
}

// ProxyURL returns the proxy a request with scheme and host uses, or nil for a
// direct connection. It implements the shape http.Transport.Proxy expects.
func (r *Resolver) ProxyURL(scheme, host, port string) *url.URL {
	if r == nil || !r.settings.Enabled {
		return nil
	}
	scheme = strings.ToLower(strings.TrimSpace(scheme))
	if scheme != "http" && scheme != "https" {
		return nil
	}
	host = trimHost(host)
	if host == "" || isLoopbackHost(host) {
		// Loopback is never proxied. This mirrors net/http's own rule and keeps
		// a localhost upstream, the Monitor itself and test servers working
		// even when an operator writes an incomplete NO_PROXY list.
		return nil
	}
	if r.noProxy.matches(host, effectivePort(scheme, port)) {
		return nil
	}
	if scheme == "https" {
		return r.httpsProxy
	}
	return r.httpProxy
}

// ProxyFunc adapts the resolver to http.Transport.Proxy.
func (r *Resolver) ProxyFunc(req *http.Request) (*url.URL, error) {
	if r == nil || req == nil || req.URL == nil {
		return nil, nil
	}
	return r.ProxyURL(req.URL.Scheme, req.URL.Hostname(), req.URL.Port()), nil
}

// Manager holds the current policy. A transport built once with
// Manager.ProxyFunc observes later updates without being replaced.
type Manager struct {
	current atomic.Pointer[Resolver]

	transportOnce sync.Once
	transport     *http.Transport
}

// NewManager returns a manager in the default (direct) state.
func NewManager() *Manager {
	m := &Manager{}
	resolver, _ := Compile(DefaultSettings())
	m.current.Store(resolver)
	return m
}

// Apply validates settings and publishes them. A rejected update leaves the
// previously published policy untouched.
func (m *Manager) Apply(settings Settings) error {
	resolver, err := Compile(settings)
	if err != nil {
		return err
	}
	m.current.Store(resolver)
	return nil
}

// Load reads the persisted policy and applies it. A missing row means the
// default off state.
func (m *Manager) Load(ctx context.Context, loader SettingLoader) error {
	if loader == nil {
		return m.Apply(DefaultSettings())
	}
	var settings Settings
	found, err := loader.LoadAppSettingJSON(ctx, SettingKey, &settings)
	if err != nil {
		return err
	}
	if !found {
		settings = DefaultSettings()
	}
	return m.Apply(settings)
}

// Current returns the published policy.
func (m *Manager) Current() Settings {
	if m == nil {
		return DefaultSettings()
	}
	if resolver := m.current.Load(); resolver != nil {
		return resolver.Settings()
	}
	return DefaultSettings()
}

// ProxyFunc returns the http.Transport.Proxy function for the current policy.
func (m *Manager) ProxyFunc(req *http.Request) (*url.URL, error) {
	if m == nil {
		return nil, nil
	}
	resolver := m.current.Load()
	if resolver == nil {
		return nil, nil
	}
	return resolver.ProxyFunc(req)
}

// Transport returns one shared transport whose proxy decision is read per
// request. Callers that already own a transport only need to set its Proxy
// field to Manager.ProxyFunc.
func (m *Manager) Transport() *http.Transport {
	if m == nil {
		return &http.Transport{}
	}
	m.transportOnce.Do(func() {
		m.transport = &http.Transport{
			Proxy: m.ProxyFunc,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			MaxIdleConnsPerHost:   100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}
	})
	return m.transport
}

// Client returns a client with the manager's transport and the given timeout.
func (m *Manager) Client(timeout time.Duration) *http.Client {
	return &http.Client{Timeout: timeout, Transport: m.Transport()}
}

// SettingLoader is the slice of the application store Load needs.
type SettingLoader interface {
	LoadAppSettingJSON(context.Context, string, any) (bool, error)
}

var defaultManager = NewManager()

// Default returns the process-wide manager the server publishes its settings
// into and the integrations read.
func Default() *Manager {
	return defaultManager
}

// Configure publishes settings on the process-wide manager.
func Configure(settings Settings) error {
	return defaultManager.Apply(settings)
}

// Load applies the persisted policy on the process-wide manager.
func Load(ctx context.Context, loader SettingLoader) error {
	return defaultManager.Load(ctx, loader)
}

// Current returns the process-wide policy.
func Current() Settings {
	return defaultManager.Current()
}

// ProxyFunc is the process-wide http.Transport.Proxy function.
func ProxyFunc(req *http.Request) (*url.URL, error) {
	return defaultManager.ProxyFunc(req)
}

// Transport returns the process-wide transport.
func Transport() *http.Transport {
	return defaultManager.Transport()
}

// Client returns a client on the process-wide transport.
func Client(timeout time.Duration) *http.Client {
	return defaultManager.Client(timeout)
}

// InstallDefaultTransport makes http.DefaultTransport follow this policy.
//
// Several long-lived clients in the process are created without an explicit
// transport (the local Responses chat client, the tokenize counter, the MCP
// tool executor, the release checker and the provider probes). Replacing the
// default transport once at startup covers all of them, and because the proxy
// function reads the current policy per request, a settings update needs no
// restart.
func InstallDefaultTransport() {
	http.DefaultTransport = Transport()
}

// RedactedProxyURL renders a configured proxy for a log line without the
// password. It keeps the scheme, host, port and user so the operator can tell
// which proxy is in use.
func RedactedProxyURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parseable := raw
	if !strings.Contains(parseable, "://") {
		parseable = "http://" + parseable
	}
	parsed, err := url.Parse(parseable)
	if err != nil {
		return "(invalid)"
	}
	return parsed.Redacted()
}

func parseProxyURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	// A bare host:port is the common form in shell proxy variables and in
	// proxy clients; Linux tools accept it as HTTP.
	parseable := raw
	if !strings.Contains(parseable, "://") {
		parseable = "http://" + parseable
	}
	parsed, err := url.Parse(parseable)
	if err != nil {
		return nil, err
	}
	scheme := strings.ToLower(parsed.Scheme)
	switch scheme {
	case "http", "https", "socks5", "socks5h":
	case "":
		return nil, errors.New("proxy URL is missing a scheme")
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q (use http, https or socks5)", parsed.Scheme)
	}
	if scheme == "socks5h" {
		scheme = "socks5"
	}
	if parsed.Hostname() == "" {
		return nil, errors.New("proxy URL is missing a host")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return nil, errors.New("proxy URL must not contain a path")
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("proxy URL must not contain a query or fragment")
	}
	if port := parsed.Port(); port != "" {
		if _, err := validPort(port); err != nil {
			return nil, err
		}
	} else {
		parsed.Host = net.JoinHostPort(parsed.Hostname(), defaultProxyPort(scheme))
	}
	parsed.Scheme = scheme
	return parsed, nil
}

func defaultProxyPort(scheme string) string {
	switch scheme {
	case "https":
		return "443"
	case "socks5":
		return "1080"
	default:
		return "80"
	}
}

func validPort(port string) (int, error) {
	value, err := strconv.Atoi(port)
	if err != nil || value <= 0 || value > 65535 {
		return 0, fmt.Errorf("invalid port %q", port)
	}
	return value, nil
}

type noProxyRule struct {
	host    string
	ip      net.IP
	network *net.IPNet
	port    string
}

type noProxyList struct {
	all   bool
	rules []noProxyRule
}

func parseNoProxy(raw string) (noProxyList, error) {
	list := noProxyList{}
	entries := strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == ';'
	})
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if entry == "*" {
			list.all = true
			continue
		}
		if strings.Contains(entry, "://") {
			return noProxyList{}, fmt.Errorf("%q looks like a URL; use a host name, IP address or CIDR block", entry)
		}
		host, port := splitNoProxyHostPort(entry)
		if port != "" {
			if _, err := validPort(port); err != nil {
				return noProxyList{}, fmt.Errorf("%q: %w", entry, err)
			}
		}
		if host == "" {
			return noProxyList{}, fmt.Errorf("%q has no host", entry)
		}
		if strings.Contains(host, "/") {
			_, network, err := net.ParseCIDR(host)
			if err != nil {
				return noProxyList{}, fmt.Errorf("%q is not a valid CIDR block", entry)
			}
			list.rules = append(list.rules, noProxyRule{network: network, port: port})
			continue
		}
		host = strings.TrimPrefix(strings.TrimPrefix(host, "*."), ".")
		host = trimHost(host)
		if host == "" {
			return noProxyList{}, fmt.Errorf("%q has no host", entry)
		}
		if strings.ContainsAny(host, "*[]") {
			return noProxyList{}, fmt.Errorf("%q has an unsupported wildcard or bracket", entry)
		}
		if ip := net.ParseIP(host); ip != nil {
			list.rules = append(list.rules, noProxyRule{ip: ip, port: port})
			continue
		}
		list.rules = append(list.rules, noProxyRule{host: host, port: port})
	}
	return list, nil
}

// splitNoProxyHostPort separates an optional port from an entry. Domain names
// and IPv4 addresses have at most one colon; an unbracketed IPv6 address has
// more and is treated as a bare address.
func splitNoProxyHostPort(entry string) (string, string) {
	if strings.HasPrefix(entry, "[") {
		if host, port, err := net.SplitHostPort(entry); err == nil {
			return host, port
		}
		if strings.HasSuffix(entry, "]") {
			return strings.TrimSuffix(strings.TrimPrefix(entry, "["), "]"), ""
		}
		return entry, ""
	}
	if strings.Count(entry, ":") == 1 {
		if host, port, err := net.SplitHostPort(entry); err == nil {
			return host, port
		}
	}
	return entry, ""
}

func (l noProxyList) matches(host, port string) bool {
	if l.all {
		return true
	}
	host = trimHost(host)
	if host == "" {
		return false
	}
	ip := net.ParseIP(host)
	for _, rule := range l.rules {
		if rule.port != "" && rule.port != port {
			continue
		}
		if rule.network != nil {
			if ip != nil && rule.network.Contains(ip) {
				return true
			}
			continue
		}
		if rule.ip != nil {
			if ip != nil && rule.ip.Equal(ip) {
				return true
			}
			continue
		}
		// A domain entry matches the apex and every subdomain, which is what
		// curl and the common Linux proxy wrappers do. A leading dot or "*."
		// is accepted as decoration rather than as a different rule.
		if host == rule.host || strings.HasSuffix(host, "."+rule.host) {
			return true
		}
	}
	return false
}

func trimHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
}

func isLoopbackHost(host string) bool {
	host = trimHost(host)
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func effectivePort(scheme, port string) string {
	if port != "" {
		return port
	}
	switch strings.ToLower(scheme) {
	case "https":
		return "443"
	case "http":
		return "80"
	default:
		return ""
	}
}
