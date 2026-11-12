package detector

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/observe"
	"github.com/kingfs/Trajecta/pkg/secretmask"
	"github.com/kingfs/Trajecta/pkg/xir/hook"
)

func emptyObservation() observe.TraceObservation {
	return observe.TraceObservation{TraceID: "test-trace"}
}

const (
	fixtureAWSKey    = "AKIAIOSFODNN7EXAMPLE"
	fixtureChineseID = "11010519491231002X"
)

func newTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestCatalogBuiltins(t *testing.T) {
	ids := IDs()
	want := []string{
		PluginSecretmask, PluginIndicators, PluginDangerousShell,
		PluginCredentialRe, PluginPIIObservation, PluginProviderSafety, PluginToolError,
	}
	for _, id := range want {
		if _, ok := Lookup(id); !ok {
			t.Fatalf("builtin plugin %q not registered", id)
		}
	}
	if len(ids) != len(want) {
		t.Fatalf("catalog has %d plugins, want %d: %v", len(ids), len(want), ids)
	}
	sm, _ := Lookup(PluginSecretmask)
	if !sm.Plugin.HasLane(LaneHot) || !sm.Plugin.HasLane(LaneAudit) {
		t.Fatalf("secretmask must run on both lanes, got %v", sm.Plugin.Lanes)
	}
	if sm.Plugin.Contract != ContractScanner {
		t.Fatalf("secretmask contract = %q", sm.Plugin.Contract)
	}
	ind, _ := Lookup(PluginIndicators)
	if ind.Plugin.HasLane(LaneHot) {
		t.Fatal("indicators are report-only and must not run on the hot lane")
	}
}

func TestDefaultConfigPreservesBehaviour(t *testing.T) {
	cfg := DefaultConfig()
	if !cfg.IsEnabled(PluginSecretmask) {
		t.Fatal("secretmask must default enabled to preserve masking")
	}
	if cfg.IsEnabled(PluginIndicators) {
		t.Fatal("indicators default disabled to preserve the audit projection output")
	}
	for _, id := range []string{PluginDangerousShell, PluginCredentialRe, PluginPIIObservation, PluginProviderSafety, PluginToolError} {
		if !cfg.IsEnabled(id) {
			t.Fatalf("observation plugin %q must default enabled", id)
		}
	}
}

func TestConfigRoundTripAndUnknownFilter(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)

	// Missing row falls back to defaults.
	cfg, err := LoadConfig(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.IsEnabled(PluginSecretmask) {
		t.Fatal("missing row should default to secretmask enabled")
	}

	// Persist a row that disables secretmask and carries an unknown id.
	stored := Config{Enabled: []string{PluginDangerousShell, "no.such.plugin"}}
	if err := SaveConfig(ctx, st, stored); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadConfig(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.IsEnabled(PluginSecretmask) {
		t.Fatal("stored row must be authoritative: secretmask disabled")
	}
	if cfg.IsEnabled("no.such.plugin") {
		t.Fatal("unknown plugin id must be filtered out")
	}
	if !cfg.IsEnabled(PluginDangerousShell) {
		t.Fatal("known enabled id must survive")
	}
}

func TestSnapshotAuditEngineAndRunner(t *testing.T) {
	snap, err := buildSnapshot(DefaultConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(snap.AuditEngine().Scanners()); got != 1 {
		t.Fatalf("default audit engine scanners = %d, want 1 (secretmask only)", got)
	}
	if snap.runner == nil {
		t.Fatal("runner must be built")
	}

	// Enabling indicators adds a second audit scanner.
	cfg := DefaultConfig()
	cfg.Enabled = append(cfg.Enabled, PluginIndicators)
	snap, err = buildSnapshot(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(snap.AuditEngine().Scanners()); got != 2 {
		t.Fatalf("audit engine scanners with indicators = %d, want 2", got)
	}

	// Disabling every observation plugin yields an empty runner, not the default set.
	cfg = Config{Enabled: []string{PluginSecretmask}}
	snap, err = buildSnapshot(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	findings, err := snap.Runner().Analyze(context.Background(), emptyObservation())
	if err != nil {
		t.Fatal(err)
	}
	if len(findings) != 0 {
		t.Fatalf("empty observation runner produced %d findings", len(findings))
	}
}

func TestMaskerBuiltinCredentialAndPII(t *testing.T) {
	ctx := context.Background()
	rt, err := NewRuntime(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	vault := secretmask.NewVault([]byte("test-seed-0123456789abcdef"))
	m := NewMasker(vault, rt, 0)

	body := []byte(`{"key":"` + fixtureAWSKey + `","id":"` + fixtureChineseID + `"}`)
	out, events := m.Mask(body)
	if len(events) != 2 {
		t.Fatalf("expected 2 mask events (credential + PII), got %d", len(events))
	}
	if string(out) == string(body) {
		t.Fatal("body should have been rewritten")
	}
	if len(out) != len(body) {
		t.Fatalf("masking changed length: %d -> %d", len(body), len(out))
	}
	names := map[string]bool{}
	for _, e := range events {
		names[e.RuleName()] = true
	}
	if !names["aws.access_key_id"] || !names["pii.chinese_id"] {
		t.Fatalf("unexpected rules masked: %v", names)
	}
}

func TestMaskerDisabledPluginStopsMasking(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	if err := SaveConfig(ctx, st, Config{Enabled: []string{PluginDangerousShell}}); err != nil {
		t.Fatal(err)
	}
	rt, err := NewRuntime(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	vault := secretmask.NewVault([]byte("test-seed-0123456789abcdef"))
	m := NewMasker(vault, rt, 0)
	body := []byte(`{"key":"` + fixtureAWSKey + `"}`)
	out, events := m.Mask(body)
	if len(events) != 0 || string(out) != string(body) {
		t.Fatalf("disabled secretmask plugin must not mask, got %d events", len(events))
	}
}

func TestMaskerDisablePIISetting(t *testing.T) {
	ctx := context.Background()
	st := newTestStore(t)
	settings, _ := json.Marshal(secretmaskSettings{DisablePII: true})
	cfg := Config{
		Enabled:  []string{PluginSecretmask},
		Settings: map[string]json.RawMessage{PluginSecretmask: settings},
	}
	if err := SaveConfig(ctx, st, cfg); err != nil {
		t.Fatal(err)
	}
	rt, err := NewRuntime(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	vault := secretmask.NewVault([]byte("test-seed-0123456789abcdef"))
	m := NewMasker(vault, rt, 0)
	body := []byte(`{"key":"` + fixtureAWSKey + `","id":"` + fixtureChineseID + `"}`)
	out, events := m.Mask(body)
	if len(events) != 1 || events[0].RuleName() != "aws.access_key_id" {
		t.Fatalf("disable_pii should mask only the credential, got %d events", len(events))
	}
	if !strings.Contains(string(out), fixtureChineseID) {
		t.Fatal("the national ID must remain untouched when PII is disabled")
	}
}

func TestMaskerNilVaultNoOp(t *testing.T) {
	rt, _ := NewRuntime(context.Background(), nil)
	m := NewMasker(nil, rt, 0)
	body := []byte(`{"key":"` + fixtureAWSKey + `"}`)
	out, events := m.Mask(body)
	if len(events) != 0 || string(out) != string(body) {
		t.Fatal("nil vault must be a no-op")
	}
}

func TestAuditEngineForPointScope(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Enabled = append(cfg.Enabled, PluginIndicators)
	snap, err := buildSnapshot(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	// secretmask declares message text; indicators declare only tool-call
	// arguments. So a message point runs secretmask alone, a tool-call point
	// runs both, and a usage point runs neither.
	if got := len(snap.AuditEngineFor(hook.PointRequestMessage).Scanners()); got != 1 {
		t.Fatalf("request.message scanners = %d, want 1 (secretmask)", got)
	}
	if got := len(snap.AuditEngineFor(hook.PointRequestToolCall).Scanners()); got != 2 {
		t.Fatalf("request.tool_call scanners = %d, want 2 (secretmask + indicators)", got)
	}
	if got := len(snap.AuditEngineFor(hook.PointResponseUsage).Scanners()); got != 0 {
		t.Fatalf("response.usage scanners = %d, want 0", got)
	}
}

func TestPluginPointsDeclared(t *testing.T) {
	sm, _ := Lookup(PluginSecretmask)
	if !sm.Plugin.HasPoint(hook.PointRequestMessage) {
		t.Fatal("secretmask must act on request.message")
	}
	if sm.Plugin.HasPoint(hook.PointResponseUsage) {
		t.Fatal("secretmask must not act on response.usage")
	}
	ind, _ := Lookup(PluginIndicators)
	if !ind.Plugin.HasPoint(hook.PointResponseToolCall) {
		t.Fatal("indicators must act on response.tool_call.arguments")
	}
	if ind.Plugin.HasPoint(hook.PointRequestMessage) {
		t.Fatal("indicators must not act on message text")
	}
}

func TestSecretmaskHotScanValidation(t *testing.T) {
	cfg := Config{Enabled: []string{PluginSecretmask}, Settings: map[string]json.RawMessage{
		PluginSecretmask: json.RawMessage(`{"hot_scan":"sideways"}`),
	}}
	if err := ValidateConfig(cfg); err == nil {
		t.Fatal("an unknown hot_scan value must fail validation")
	}
	cfg.Settings[PluginSecretmask] = json.RawMessage(`{"hot_scan":"fields"}`)
	if err := ValidateConfig(cfg); err != nil {
		t.Fatalf("fields mode must validate: %v", err)
	}
}

// In fields mode the DFA sees only projected content: a credential used as a
// JSON key is envelope, not content, and is deliberately left alone, while the
// same value in message text is masked.
func TestSecretmaskFieldsModeScopesToContent(t *testing.T) {
	cfg := Config{Enabled: []string{PluginSecretmask}, Settings: map[string]json.RawMessage{
		PluginSecretmask: json.RawMessage(`{"hot_scan":"fields"}`),
	}}
	snap, err := buildSnapshot(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}
	key := "AKIAIOSFODNN7EXAMPLE"
	body := []byte(`{"model":"gpt-5","` + key + `":"envelope","messages":[{"role":"user","content":"key ` + key + `"}]}`)
	vault := secretmask.NewVault([]byte("fields-mode-seed"))
	m := NewMasker(vault, nil, 0)
	// Bind the snapshot directly; the masker normally reads it from a Runtime,
	// and a nil Runtime would take the byte path.
	masked, events := maskWithSnapshot(m, snap, body, "chat_completions")
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1 (the content value only)", len(events))
	}
	if !bytes.Contains(masked, []byte(`"`+key+`":"envelope"`)) {
		t.Fatalf("the JSON-key occurrence was masked in fields mode: %s", masked)
	}
	if bytes.Contains(masked, []byte("key "+key)) {
		t.Fatalf("the content occurrence was not masked: %s", masked)
	}
}

// maskWithSnapshot mirrors Masker.MaskWithProtocol against an explicit snapshot
// so a test can install a config without a Runtime.
func maskWithSnapshot(m *Masker, snap *Snapshot, src []byte, protocol string) ([]byte, []secretmask.MaskEvent) {
	scope, outcome := ParseRequestScopeWithOutcome(protocol, src, ScopeOptions{
		MaxBytes:      m.maxScopeBytes,
		RequireFields: snap.hasHotFields(),
	})
	if outcome != ScopeProjected {
		return m.replacement(src, snap.collectCandidates(src), nil)
	}
	return m.replacement(src, snap.collectCandidatesScoped(src, scope), scope.SealedSpans())
}
