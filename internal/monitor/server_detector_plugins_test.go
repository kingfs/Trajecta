package monitor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/internal/detector"
)

func TestDetectorPluginsAPIDefaults(t *testing.T) {
	st := newPluginTestStore(t)
	rr := httptest.NewRecorder()
	detectorPluginsAPIHandler(st).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/detector-plugins", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp detectorPluginsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Items) != len(detector.IDs()) {
		t.Fatalf("items = %d, want %d", len(resp.Items), len(detector.IDs()))
	}
	byID := map[string]detectorPluginItem{}
	for _, item := range resp.Items {
		byID[item.ID] = item
	}
	if !byID[detector.PluginSecretmask].Enabled {
		t.Fatal("secretmask must default enabled")
	}
	if byID[detector.PluginIndicators].Enabled {
		t.Fatal("indicators must default disabled")
	}
}

func TestDetectorPluginEnableDisableRoundTrip(t *testing.T) {
	st := newPluginTestStore(t)
	detail := detectorPluginDetailAPIHandler(st, nil, nil)

	// Disable secretmask (no vault mappings, so no conflict).
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/detector-plugins/"+detector.PluginSecretmask, strings.NewReader(`{"enabled":false}`))
	detail.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("disable status = %d body=%s", rr.Code, rr.Body.String())
	}

	cfg, err := detector.LoadConfig(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IsEnabled(detector.PluginSecretmask) {
		t.Fatal("secretmask should be disabled after PATCH")
	}

	// Re-enable.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, "/api/detector-plugins/"+detector.PluginSecretmask, strings.NewReader(`{"enabled":true}`))
	detail.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("enable status = %d body=%s", rr.Code, rr.Body.String())
	}
	cfg, _ = detector.LoadConfig(context.Background(), st)
	if !cfg.IsEnabled(detector.PluginSecretmask) {
		t.Fatal("secretmask should be re-enabled")
	}
}

func TestDetectorPluginUnknownID(t *testing.T) {
	st := newPluginTestStore(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/detector-plugins/no.such.plugin", strings.NewReader(`{"enabled":true}`))
	detectorPluginDetailAPIHandler(st, nil, nil).ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
}

func TestDetectorPluginSettingsUpdate(t *testing.T) {
	st := newPluginTestStore(t)
	rr := httptest.NewRecorder()
	body := `{"settings":{"disable_pii":true}}`
	req := httptest.NewRequest(http.MethodPatch, "/api/detector-plugins/"+detector.PluginSecretmask, strings.NewReader(body))
	detectorPluginDetailAPIHandler(st, nil, nil).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	cfg, _ := detector.LoadConfig(context.Background(), st)
	raw := cfg.SettingsFor(detector.PluginSecretmask)
	if !strings.Contains(string(raw), "disable_pii") {
		t.Fatalf("settings not persisted: %s", raw)
	}
}

func TestDetectorPluginPatchRequiresField(t *testing.T) {
	st := newPluginTestStore(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/detector-plugins/"+detector.PluginSecretmask, strings.NewReader(`{}`))
	detectorPluginDetailAPIHandler(st, nil, nil).ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestDetectorPluginRepublishUpdatesSnapshot(t *testing.T) {
	st := newPluginTestStore(t)
	ctx := context.Background()
	rt, err := detector.NewRuntime(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	if !rt.Snapshot().Config.IsEnabled(detector.PluginSecretmask) {
		t.Fatal("initial snapshot should enable secretmask")
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/detector-plugins/"+detector.PluginSecretmask, strings.NewReader(`{"enabled":false}`))
	detectorPluginDetailAPIHandler(st, rt, nil).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if rt.Snapshot().Config.IsEnabled(detector.PluginSecretmask) {
		t.Fatal("republish should disable secretmask in the live snapshot")
	}
}
