package monitor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/netproxy"
	"github.com/kingfs/Trajecta/internal/store"
)

func TestNetworkProxySettingsDefaultsToOff(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	rr := httptest.NewRecorder()
	networkProxySettingsAPIHandler(st)(rr, httptest.NewRequest(http.MethodGet, "/api/settings/network-proxy", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var got networkProxySettingsView
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode GET body: %v", err)
	}
	if got.Enabled || got.HTTPProxy != "" || got.HTTPSProxy != "" || got.NoProxy != "" {
		t.Fatalf("default settings = %+v, want the off state", got)
	}
}

func TestNetworkProxySettingsPatchPersistsAndPublishes(t *testing.T) {
	// This test publishes into the process-wide manager; keep it out of the
	// parallel set and restore the default policy when it finishes.
	t.Cleanup(func() { _ = netproxy.Configure(netproxy.DefaultSettings()) })

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	body := `{"enabled":true,"http_proxy":"127.0.0.1:7890","https_proxy":"","no_proxy":" localhost,10.0.0.0/8 "}`
	rr := httptest.NewRecorder()
	networkProxySettingsAPIHandler(st)(rr, httptest.NewRequest(http.MethodPatch, "/api/settings/network-proxy", strings.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var got networkProxySettingsView
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode PATCH body: %v", err)
	}
	if !got.Enabled || got.HTTPProxy != "http://127.0.0.1:7890" || got.NoProxy != "localhost,10.0.0.0/8" {
		t.Fatalf("PATCH response = %+v, want the normalized stored settings", got)
	}
	if got.EffectiveHTTPSProxy != "http://127.0.0.1:7890" {
		t.Fatalf("effective_https_proxy = %q, want the HTTP proxy fallback", got.EffectiveHTTPSProxy)
	}

	var stored netproxy.Settings
	found, err := st.LoadAppSettingJSON(t.Context(), netproxy.SettingKey, &stored)
	if err != nil {
		t.Fatalf("LoadAppSettingJSON() error = %v", err)
	}
	if !found || !stored.Enabled || stored.HTTPProxy != "http://127.0.0.1:7890" {
		t.Fatalf("stored settings = %+v (found=%v), want the submitted policy", stored, found)
	}
	if current := netproxy.Current(); !current.Enabled || current.HTTPProxy != "http://127.0.0.1:7890" {
		t.Fatalf("published settings = %+v, want the submitted policy", current)
	}

	// A GET reads the stored row, not the request that wrote it.
	rr = httptest.NewRecorder()
	networkProxySettingsAPIHandler(st)(rr, httptest.NewRequest(http.MethodGet, "/api/settings/network-proxy", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("GET after PATCH status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var reloaded networkProxySettingsView
	if err := json.Unmarshal(rr.Body.Bytes(), &reloaded); err != nil {
		t.Fatalf("decode reloaded body: %v", err)
	}
	if reloaded.HTTPProxy != "http://127.0.0.1:7890" || !reloaded.Enabled {
		t.Fatalf("reloaded settings = %+v, want the stored policy", reloaded)
	}
}

func TestNetworkProxySettingsRejectsInvalidPolicy(t *testing.T) {
	t.Cleanup(func() { _ = netproxy.Configure(netproxy.DefaultSettings()) })

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	body := `{"enabled":true,"http_proxy":"ftp://proxy.internal:21","no_proxy":""}`
	rr := httptest.NewRecorder()
	networkProxySettingsAPIHandler(st)(rr, httptest.NewRequest(http.MethodPatch, "/api/settings/network-proxy", strings.NewReader(body)))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("PATCH status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	var stored netproxy.Settings
	found, err := st.LoadAppSettingJSON(t.Context(), netproxy.SettingKey, &stored)
	if err != nil {
		t.Fatalf("LoadAppSettingJSON() error = %v", err)
	}
	if found {
		t.Fatalf("invalid policy was stored: %+v", stored)
	}
	if current := netproxy.Current(); current.Enabled {
		t.Fatalf("invalid policy was published: %+v", current)
	}
}

func TestNetworkProxySettingsRouteIsAdminOnly(t *testing.T) {
	t.Parallel()

	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	manager := testJWTManager(t, time.Hour)
	adminToken := systemTestToken(t, manager, 1, "admin", "admin")
	viewerToken := systemTestToken(t, manager, 2, "viewer", "viewer")
	mux := http.NewServeMux()
	RegisterRoutes(mux, st, RouteOptions{MonitorAuthVerifier: manager, MonitorJWT: manager})

	if rr := systemTestRequest(mux, "/api/settings/network-proxy", ""); rr.Code != http.StatusUnauthorized {
		t.Fatalf("GET without credentials = %d, want 401", rr.Code)
	}
	if rr := systemTestRequest(mux, "/api/settings/network-proxy", viewerToken); rr.Code != http.StatusForbidden {
		t.Fatalf("GET as viewer = %d, want 403", rr.Code)
	}
	if rr := systemTestRequest(mux, "/api/settings/network-proxy", adminToken); rr.Code != http.StatusOK {
		t.Fatalf("GET as admin = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
}
