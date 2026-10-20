package netproxy

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestDisabledPolicyNeverProxies(t *testing.T) {
	t.Parallel()

	resolver, err := Compile(Settings{
		Enabled:    false,
		HTTPProxy:  "http://127.0.0.1:7890",
		HTTPSProxy: "http://127.0.0.1:7891",
		NoProxy:    "example.com",
	})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	for _, target := range []string{"http://upstream.example/v1", "https://upstream.example/v1"} {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		if got, err := resolver.ProxyFunc(req); err != nil || got != nil {
			t.Fatalf("ProxyFunc(%s) = %v, %v; want nil, nil", target, got, err)
		}
	}
}

func TestHTTPAndHTTPSUseTheirOwnProxy(t *testing.T) {
	t.Parallel()

	resolver, err := Compile(Settings{
		Enabled:    true,
		HTTPProxy:  "127.0.0.1:7890",
		HTTPSProxy: "127.0.0.1:7891",
	})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}

	httpReq := httptest.NewRequest(http.MethodGet, "http://upstream.example/v1/models", nil)
	got, err := resolver.ProxyFunc(httpReq)
	if err != nil {
		t.Fatalf("ProxyFunc(http) error = %v", err)
	}
	if got == nil || got.String() != "http://127.0.0.1:7890" {
		t.Fatalf("ProxyFunc(http) = %v, want http://127.0.0.1:7890", got)
	}

	httpsReq := httptest.NewRequest(http.MethodGet, "https://upstream.example/v1/chat/completions", nil)
	got, err = resolver.ProxyFunc(httpsReq)
	if err != nil {
		t.Fatalf("ProxyFunc(https) error = %v", err)
	}
	if got == nil || got.String() != "http://127.0.0.1:7891" {
		t.Fatalf("ProxyFunc(https) = %v, want http://127.0.0.1:7891", got)
	}
}

func TestHTTPSProxyDefaultsToHTTPProxy(t *testing.T) {
	t.Parallel()

	resolver, err := Compile(Settings{Enabled: true, HTTPProxy: "http://proxy.internal:3128"})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if got := resolver.Settings().EffectiveHTTPSProxy(); got != "http://proxy.internal:3128" {
		t.Fatalf("EffectiveHTTPSProxy() = %q", got)
	}
	req := httptest.NewRequest(http.MethodGet, "https://upstream.example/v1", nil)
	got, err := resolver.ProxyFunc(req)
	if err != nil {
		t.Fatalf("ProxyFunc() error = %v", err)
	}
	if got == nil || got.String() != "http://proxy.internal:3128" {
		t.Fatalf("ProxyFunc(https) = %v, want the HTTP proxy", got)
	}
}

func TestHTTPSOnlyPolicyLeavesHTTPDirect(t *testing.T) {
	t.Parallel()

	resolver, err := Compile(Settings{Enabled: true, HTTPSProxy: "http://secure-proxy.internal:3128"})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if got, _ := resolver.ProxyFunc(httptest.NewRequest(http.MethodGet, "http://upstream.example/v1", nil)); got != nil {
		t.Fatalf("ProxyFunc(http) = %v, want direct", got)
	}
	if got, _ := resolver.ProxyFunc(httptest.NewRequest(http.MethodGet, "https://upstream.example/v1", nil)); got == nil {
		t.Fatal("ProxyFunc(https) = nil, want the HTTPS proxy")
	}
}

func TestNoProxyMatchesHostsDomainsPortsAndCIDRs(t *testing.T) {
	t.Parallel()

	resolver, err := Compile(Settings{
		Enabled:    true,
		HTTPProxy:  "http://proxy.internal:3128",
		HTTPSProxy: "http://proxy.internal:3128",
		NoProxy:    " internal.local, .corp.example.com, exact.example.net, 10.0.0.0/8, 192.168.1.5, [fd00::1]:8443, api.internal:9000 ",
	})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}

	tests := []struct {
		target string
		want   bool // true means proxied
	}{
		{target: "http://internal.local/v1", want: false},
		{target: "http://api.internal.local/v1", want: false},
		{target: "https://corp.example.com/v1", want: false},
		{target: "https://team.corp.example.com/v1", want: false},
		{target: "https://other.example.com/v1", want: true},
		{target: "https://exact.example.net/v1", want: false},
		{target: "https://sub.exact.example.net/v1", want: false},
		{target: "http://10.1.2.3/v1", want: false},
		{target: "http://11.1.2.3/v1", want: true},
		{target: "http://192.168.1.5/v1", want: false},
		{target: "https://[fd00::1]:8443/v1", want: false},
		{target: "https://[fd00::1]:9443/v1", want: true},
		{target: "http://api.internal:9000/v1", want: false},
		{target: "http://api.internal:9001/v1", want: true},
	}

	for _, tc := range tests {
		req := httptest.NewRequest(http.MethodGet, tc.target, nil)
		got, err := resolver.ProxyFunc(req)
		if err != nil {
			t.Fatalf("ProxyFunc(%s) error = %v", tc.target, err)
		}
		if proxied := got != nil; proxied != tc.want {
			t.Fatalf("ProxyFunc(%s) proxied = %v (%v), want %v", tc.target, proxied, got, tc.want)
		}
	}
}

func TestNoProxyWildcardDisablesProxying(t *testing.T) {
	t.Parallel()

	resolver, err := Compile(Settings{Enabled: true, HTTPProxy: "http://proxy.internal:3128", NoProxy: "*"})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	if got, _ := resolver.ProxyFunc(httptest.NewRequest(http.MethodGet, "https://upstream.example/v1", nil)); got != nil {
		t.Fatalf("ProxyFunc() = %v, want direct", got)
	}
}

func TestLoopbackIsAlwaysDirect(t *testing.T) {
	t.Parallel()

	resolver, err := Compile(Settings{Enabled: true, HTTPProxy: "http://proxy.internal:3128"})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	for _, target := range []string{
		"http://localhost:8080/v1",
		"http://api.localhost/v1",
		"http://127.0.0.1:8080/v1",
		"http://127.9.9.9/v1",
		"http://[::1]:8080/v1",
	} {
		if got, _ := resolver.ProxyFunc(httptest.NewRequest(http.MethodGet, target, nil)); got != nil {
			t.Fatalf("ProxyFunc(%s) = %v, want direct", target, got)
		}
	}
}

func TestCompileRejectsInvalidSettings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		settings Settings
		want     string
	}{
		{name: "enabled without a proxy", settings: Settings{Enabled: true}, want: "http_proxy or https_proxy"},
		{name: "unsupported scheme", settings: Settings{Enabled: true, HTTPProxy: "ftp://proxy.internal:21"}, want: "unsupported proxy scheme"},
		{name: "proxy with a path", settings: Settings{Enabled: true, HTTPProxy: "http://proxy.internal:3128/path"}, want: "must not contain a path"},
		{name: "bad proxy port", settings: Settings{Enabled: true, HTTPProxy: "http://proxy.internal:notaport"}, want: "invalid port"},
		{name: "bad no_proxy cidr", settings: Settings{Enabled: true, HTTPProxy: "http://proxy.internal:3128", NoProxy: "10.0.0.0/99"}, want: "not a valid CIDR"},
		{name: "bad no_proxy port", settings: Settings{Enabled: true, HTTPProxy: "http://proxy.internal:3128", NoProxy: "example.com:70000"}, want: "invalid port"},
		{name: "url in no_proxy", settings: Settings{Enabled: true, HTTPProxy: "http://proxy.internal:3128", NoProxy: "http://example.com"}, want: "looks like a URL"},
		{name: "wildcard in no_proxy", settings: Settings{Enabled: true, HTTPProxy: "http://proxy.internal:3128", NoProxy: "foo.*.example.com"}, want: "unsupported wildcard"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Compile(tc.settings)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Compile() error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestCompileAcceptsCommonProxyForms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		raw  string
		want string
	}{
		{raw: "proxy.internal:3128", want: "http://proxy.internal:3128"},
		{raw: "http://proxy.internal", want: "http://proxy.internal:80"},
		{raw: "https://proxy.internal", want: "https://proxy.internal:443"},
		{raw: "socks5://proxy.internal", want: "socks5://proxy.internal:1080"},
		{raw: "socks5h://user:pass@proxy.internal:1080", want: "socks5://user:pass@proxy.internal:1080"},
	}
	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			t.Parallel()
			resolver, err := Compile(Settings{Enabled: true, HTTPProxy: tc.raw})
			if err != nil {
				t.Fatalf("Compile() error = %v", err)
			}
			got, err := resolver.ProxyFunc(httptest.NewRequest(http.MethodGet, "http://upstream.example/v1", nil))
			if err != nil {
				t.Fatalf("ProxyFunc() error = %v", err)
			}
			if got == nil || got.String() != tc.want {
				t.Fatalf("proxy = %v, want %s", got, tc.want)
			}
		})
	}
}

func TestCompileCanonicalizesStoredProxyFields(t *testing.T) {
	t.Parallel()

	resolver, err := Compile(Settings{
		Enabled:    true,
		HTTPProxy:  " proxy.internal:3128 ",
		HTTPSProxy: " ",
		NoProxy:    " internal.local ",
	})
	if err != nil {
		t.Fatalf("Compile() error = %v", err)
	}
	got := resolver.Settings()
	if got.HTTPProxy != "http://proxy.internal:3128" {
		t.Fatalf("HTTPProxy = %q, want the canonical URL", got.HTTPProxy)
	}
	// An empty https_proxy must stay empty in storage: filling it in would pin
	// the fallback and stop a later change to http_proxy from propagating.
	if got.HTTPSProxy != "" {
		t.Fatalf("HTTPSProxy = %q, want it to stay empty", got.HTTPSProxy)
	}
	if got.EffectiveHTTPSProxy() != "http://proxy.internal:3128" {
		t.Fatalf("EffectiveHTTPSProxy() = %q", got.EffectiveHTTPSProxy())
	}
	if got.NoProxy != "internal.local" {
		t.Fatalf("NoProxy = %q, want it trimmed", got.NoProxy)
	}
}

func TestManagerApplySwapsPolicyForAnExistingTransport(t *testing.T) {
	t.Parallel()

	manager := NewManager()
	transport := manager.Transport()

	if err := manager.Apply(Settings{Enabled: true, HTTPProxy: "http://proxy.internal:3128"}); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	proxy, err := transport.Proxy(httptest.NewRequest(http.MethodGet, "https://upstream.example/v1", nil))
	if err != nil {
		t.Fatalf("transport.Proxy() error = %v", err)
	}
	if proxy == nil || proxy.Host != "proxy.internal:3128" {
		t.Fatalf("transport.Proxy() = %v, want proxy.internal:3128", proxy)
	}

	if err := manager.Apply(Settings{}); err != nil {
		t.Fatalf("Apply(disabled) error = %v", err)
	}
	proxy, err = transport.Proxy(httptest.NewRequest(http.MethodGet, "https://upstream.example/v1", nil))
	if err != nil {
		t.Fatalf("transport.Proxy() error = %v", err)
	}
	if proxy != nil {
		t.Fatalf("transport.Proxy() after disabling = %v, want nil", proxy)
	}
}

func TestManagerApplyRejectsUpdateWithoutChangingCurrentPolicy(t *testing.T) {
	t.Parallel()

	manager := NewManager()
	if err := manager.Apply(Settings{Enabled: true, HTTPProxy: "http://proxy.internal:3128"}); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if err := manager.Apply(Settings{Enabled: true}); err == nil {
		t.Fatal("Apply(invalid) error = nil, want a validation error")
	}
	if got := manager.Current(); !got.Enabled || got.HTTPProxy != "http://proxy.internal:3128" {
		t.Fatalf("Current() = %+v, want the previous policy", got)
	}
}

func TestManagerLoadReadsThePersistedRow(t *testing.T) {
	t.Parallel()

	loader := &fakeLoader{found: true, value: Settings{
		Enabled:    true,
		HTTPProxy:  " http://proxy.internal:3128 ",
		HTTPSProxy: "",
		NoProxy:    " internal.local ",
	}}
	manager := NewManager()
	if err := manager.Load(context.Background(), loader); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	got := manager.Current()
	if !got.Enabled || got.HTTPProxy != "http://proxy.internal:3128" || got.NoProxy != "internal.local" {
		t.Fatalf("Current() = %+v, want normalized persisted settings", got)
	}
}

func TestManagerLoadDefaultsToOffWhenTheRowIsMissing(t *testing.T) {
	t.Parallel()

	manager := NewManager()
	if err := manager.Apply(Settings{Enabled: true, HTTPProxy: "http://proxy.internal:3128"}); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if err := manager.Load(context.Background(), &fakeLoader{}); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got := manager.Current(); got.Enabled {
		t.Fatalf("Current() = %+v, want the default off state", got)
	}
}

func TestManagerLoadPropagatesStoreErrors(t *testing.T) {
	t.Parallel()

	manager := NewManager()
	err := manager.Load(context.Background(), &fakeLoader{err: errors.New("database unavailable")})
	if err == nil || !strings.Contains(err.Error(), "database unavailable") {
		t.Fatalf("Load() error = %v, want the store error", err)
	}
}

func TestTransportActuallyRoutesThroughTheProxy(t *testing.T) {
	t.Parallel()

	var seen = make(chan string, 1)
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.URL.String()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("proxied"))
	}))
	t.Cleanup(proxyServer.Close)

	manager := NewManager()
	if err := manager.Apply(Settings{Enabled: true, HTTPProxy: proxyServer.URL}); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	client := manager.Client(5 * time.Second)

	// example.invalid cannot resolve, so a 200 can only have come from the
	// proxy. httptest's proxy server also strips nothing from an absolute-form
	// request URL.
	response, err := client.Get("http://upstream.example.invalid/v1/models")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", response.StatusCode)
	}
	parsed, err := url.Parse(<-seen)
	if err != nil {
		t.Fatalf("proxy saw an unparseable URL: %v", err)
	}
	if parsed.Host != "upstream.example.invalid" || parsed.Path != "/v1/models" {
		t.Fatalf("proxy saw %q, want http://upstream.example.invalid/v1/models", parsed.String())
	}
}

type fakeLoader struct {
	found bool
	value Settings
	err   error
}

func (f *fakeLoader) LoadAppSettingJSON(_ context.Context, _ string, out any) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	if !f.found {
		return false, nil
	}
	settings, ok := out.(*Settings)
	if !ok {
		return false, errors.New("unexpected output type")
	}
	*settings = f.value
	return true, nil
}
