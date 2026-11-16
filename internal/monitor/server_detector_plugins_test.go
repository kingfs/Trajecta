package monitor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/internal/detector"
	"github.com/kingfs/Trajecta/pkg/secretmask"
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
	if len(resp.Items) != len(detector.Plugins()) {
		t.Fatalf("items = %d, want %d", len(resp.Items), len(detector.Plugins()))
	}
	byID := map[string]detectorPluginItem{}
	for _, item := range resp.Items {
		byID[item.ID] = item
	}
	if !byID[detector.PluginSecretmask].Enabled {
		t.Fatal("secretmask must default enabled")
	}
	if byID[detector.PluginIndicators].Enabled {
		t.Fatal("indicators must default disabled to preserve the audit projection output")
	}
	if !byID[detector.PluginSecretmask].AlwaysOn {
		t.Fatal("the credential/PII union machine must be marked always-on")
	}
	if byID[detector.PluginIndicators].AlwaysOn {
		t.Fatal("the report-only indicator machine must stay toggleable")
	}
}

func TestDetectorPluginBuiltinCannotBeDisabled(t *testing.T) {
	st := newPluginTestStore(t)
	detail := detectorPluginDetailAPIHandler(st, nil, nil)

	// Disabling a builtin DFA capability is rejected: it is compiled
	// infrastructure, not a toggleable algorithm.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/detector-plugins/"+detector.PluginSecretmask, strings.NewReader(`{"enabled":false}`))
	detail.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("disable status = %d body=%s, want 400", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "plugin_builtin") {
		t.Fatalf("disable error = %s, want plugin_builtin", rr.Body.String())
	}
	cfg, err := detector.LoadConfig(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.IsEnabled(detector.PluginSecretmask) {
		t.Fatal("secretmask must remain enabled after a rejected disable")
	}

	// Enabling (idempotent) is accepted.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, "/api/detector-plugins/"+detector.PluginSecretmask, strings.NewReader(`{"enabled":true}`))
	detail.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("enable status = %d body=%s", rr.Code, rr.Body.String())
	}
	cfg, _ = detector.LoadConfig(context.Background(), st)
	if !cfg.IsEnabled(detector.PluginSecretmask) {
		t.Fatal("secretmask should be enabled")
	}
}

// The report-only indicator machine is a separate Ragel machine, not the
// compiled union machine: an operator can turn its audit detections off.
func TestDetectorPluginIndicatorsCanBeDisabled(t *testing.T) {
	st := newPluginTestStore(t)
	detail := detectorPluginDetailAPIHandler(st, nil, nil)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/detector-plugins/"+detector.PluginIndicators, strings.NewReader(`{"enabled":false}`))
	detail.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("disable indicators status = %d body=%s", rr.Code, rr.Body.String())
	}
	cfg, err := detector.LoadConfig(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IsEnabled(detector.PluginIndicators) {
		t.Fatal("indicators should be disabled after PATCH")
	}

	rr = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, "/api/detector-plugins/"+detector.PluginIndicators, strings.NewReader(`{"enabled":true}`))
	detail.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("enable indicators status = %d body=%s", rr.Code, rr.Body.String())
	}
	cfg, _ = detector.LoadConfig(context.Background(), st)
	if !cfg.IsEnabled(detector.PluginIndicators) {
		t.Fatal("indicators should be enabled after PATCH")
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
	if !rt.Snapshot().Config.IsEnabled(detector.PluginDangerousShell) {
		t.Fatal("initial snapshot should enable dangerous_shell")
	}
	// A toggleable observation plugin can be disabled; republish must update the
	// live snapshot. The builtin DFA capability stays on regardless.
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/detector-plugins/"+detector.PluginDangerousShell, strings.NewReader(`{"enabled":false}`))
	detectorPluginDetailAPIHandler(st, rt, nil).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if rt.Snapshot().Config.IsEnabled(detector.PluginDangerousShell) {
		t.Fatal("republish should disable dangerous_shell in the live snapshot")
	}
	if !rt.Snapshot().Config.IsEnabled(detector.PluginSecretmask) {
		t.Fatal("builtin secretmask must stay enabled in the live snapshot")
	}
}

func TestDetectorPluginRejectsInvalidSettings(t *testing.T) {
	st := newPluginTestStore(t)
	detail := detectorPluginDetailAPIHandler(st, nil, nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch,
		"/api/detector-plugins/"+detector.PluginSecretmask,
		strings.NewReader(`{"settings":{"hot_scan":"sideways"}}`))
	detail.ServeHTTP(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rr.Code, rr.Body.String())
	}
	cfg, err := detector.LoadConfig(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if raw := cfg.SettingsFor(detector.PluginSecretmask); len(raw) != 0 {
		t.Fatalf("invalid settings were persisted: %s", raw)
	}
}

func TestDetectorSandboxReportsFindingAndStandIn(t *testing.T) {
	handler := detectorSandboxAPIHandler(nil)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/detector-plugins/secretmask/test",
		strings.NewReader(`{"body":"key AKIAIOSFODNN7EXAMPLE phone 13800138000"}`))
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp detectorTestResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Findings) < 2 {
		t.Fatalf("findings = %+v, want the key and the phone", resp.Findings)
	}
	var sawKey bool
	for _, finding := range resp.Findings {
		if finding.Rule == "aws.access_key_id" {
			sawKey = true
			if !finding.Maskable || finding.StandIn == "" {
				t.Fatalf("key finding = %+v, want a maskable finding with a stand-in", finding)
			}
			if len(finding.StandIn) != finding.End-finding.Start {
				t.Fatalf("stand-in length %d != value length %d", len(finding.StandIn), finding.End-finding.Start)
			}
		}
	}
	if !sawKey {
		t.Fatal("aws.access_key_id was not reported")
	}
}

func TestDetectorSandboxHonoursSuppression(t *testing.T) {
	handler := detectorSandboxAPIHandler(suppressionStub{})
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/detector-plugins/secretmask/test",
		strings.NewReader(`{"body":"key AKIAIOSFODNN7EXAMPLE"}`))
	handler.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp detectorTestResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	for _, finding := range resp.Findings {
		if finding.Rule == "aws.access_key_id" {
			if !finding.Suppressed || finding.StandIn != "" {
				t.Fatalf("suppressed finding = %+v", finding)
			}
			return
		}
	}
	t.Fatal("aws.access_key_id was not reported")
}

func TestDetectorStatsEndpoint(t *testing.T) {
	st := newPluginTestStore(t)
	rt, err := detector.NewRuntime(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	detectorPluginStatsAPIHandler(st, rt, nil, nil).ServeHTTP(
		rr, httptest.NewRequest(http.MethodGet, "/api/detector-plugins/stats", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var resp detectorStatsResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Mappings != 0 || resp.Suppressions != 0 {
		t.Fatalf("empty stats = %+v", resp)
	}
}

// suppressionStub is a suppression admin that reports every aws access key as
// suppressed.
type suppressionStub struct{}

func (suppressionStub) ListSuppressions(bool, func(secretmask.Suppression) error) error { return nil }
func (suppressionStub) AddSuppression(string, string, string) (secretmask.Suppression, error) {
	return secretmask.Suppression{}, nil
}
func (suppressionStub) DeleteSuppression(uint64) (secretmask.Suppression, bool, error) {
	return secretmask.Suppression{}, false, nil
}
func (suppressionStub) Suppressed(rule string, real []byte) bool {
	return rule == "aws.access_key_id" && string(real) == "AKIAIOSFODNN7EXAMPLE"
}
