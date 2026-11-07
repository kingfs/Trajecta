package proxy

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// A single upstream answering 404 deletes its per-attempt cassette and records
// the final failure on a fresh log file. That failure row used to lose the
// attempted upstream: selected_upstream_id, selected_upstream_base_url and
// selected_upstream_provider_preset were empty while the error message named the
// channel, so the trace could not be joined back to the provider it blamed.
func TestExhaustedUpstreamFailureKeepsAttemptedTargetAttribution(t *testing.T) {
	outputDir := t.TempDir()
	st, err := store.New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/responses" {
			t.Errorf("upstream path = %q, want /v1/responses", r.URL.Path)
		}
		// The NVIDIA NIM incident shape: the model exists, the provider's router
		// has no /v1/responses handler, so the miss is a plain-text 404 page.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, "404 page not found")
	}))
	defer upstream.Close()

	enabled := true
	cfg := &config.Config{
		Upstreams: []config.UpstreamTargetConfig{{
			ID:             "nvidia-nim-prod",
			Name:           "NVIDIA NIM",
			Enabled:        &enabled,
			Priority:       100,
			ModelDiscovery: router.ModelDiscoveryStaticOnly,
			StaticModels:   []string{"moonshotai/kimi-k2"},
			Upstream: config.UpstreamConfig{
				BaseURL:        upstream.URL + "/v1",
				ApiKey:         "upstream-secret",
				ProviderPreset: "nvidia_nim",
				APIType:        "responses_native",
			},
		}},
	}
	cfg.Debug.OutputDir = outputDir
	cfg.Debug.MaskKey = true

	handler, err := NewHandler(cfg, st)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	req, err := http.NewRequest(http.MethodPost, proxyServer.URL+"/v1/responses",
		bytes.NewBufferString(`{"model":"moonshotai/kimi-k2","input":"ping"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := proxyServer.Client().Do(req)
	if err != nil {
		t.Fatalf("client.Do() error = %v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	text := string(body)

	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%s", resp.StatusCode, text)
	}
	if !strings.Contains(text, upstreamHint404PathNotServed) {
		t.Fatalf("error body = %s, want the route/path 404 hint", text)
	}
	if strings.Contains(text, "does not serve this model id") {
		t.Fatalf("error body = %s, must not claim the model id is unknown", text)
	}

	entry := waitForLatestTraceEntry(t, st, time.Second)
	meta := entry.Header.Meta
	if meta.SelectedUpstreamID != "nvidia-nim-prod" {
		t.Fatalf("SelectedUpstreamID = %q, want nvidia-nim-prod", meta.SelectedUpstreamID)
	}
	if meta.SelectedUpstreamBaseURL != upstream.URL+"/v1" {
		t.Fatalf("SelectedUpstreamBaseURL = %q, want %q", meta.SelectedUpstreamBaseURL, upstream.URL+"/v1")
	}
	if meta.SelectedUpstreamProviderPreset != "nvidia_nim" {
		t.Fatalf("SelectedUpstreamProviderPreset = %q, want nvidia_nim", meta.SelectedUpstreamProviderPreset)
	}
	if !strings.Contains(meta.Error, upstreamHint404PathNotServed) {
		t.Fatalf("recorded error = %q, want the route/path 404 hint", meta.Error)
	}

	parsed := parseTracePrelude(t, outputDir, entry)
	failure := eventByType(parsed.Events, "routing.failure")
	if failure == nil {
		t.Fatalf("routing.failure event missing from trace events: %+v", parsed.Events)
		return
	}
	if got, _ := failure.Attributes["upstream_id"].(string); got != "nvidia-nim-prod" {
		t.Fatalf("routing.failure upstream_id = %q, want nvidia-nim-prod", got)
	}
	if selected := eventByType(parsed.Events, "routing.selected"); selected == nil {
		t.Fatalf("routing.selected event missing from failure trace: %+v", parsed.Events)
	} else if got, _ := selected.Attributes["upstream_id"].(string); got != "nvidia-nim-prod" {
		t.Fatalf("routing.selected upstream_id = %q, want nvidia-nim-prod", got)
	}
}

// addAttemptedTargetAttrs is the failure-side identity block. A database-managed
// channel has the route-target/channel/credential ids only there, so a failure
// event must carry them even though no successful dispatch recorded them.
func TestAddAttemptedTargetAttrs(t *testing.T) {
	t.Run("credential_identity_wins", func(t *testing.T) {
		attrs := map[string]interface{}{}
		addAttemptedTargetAttrs(attrs, &router.Selection{
			Target: &router.Target{
				ID:            "upstream-a",
				RouteTargetID: "target-a",
				ChannelID:     "channel-a",
				CredentialID:  "credential-a",
			},
			Credential: router.CredentialDecisionInfo{RouteTargetID: "credential-route-target"},
		})
		want := map[string]string{
			"upstream_id":     "upstream-a",
			"route_target_id": "credential-route-target",
			"channel_id":      "channel-a",
			"credential_id":   "credential-a",
		}
		for key, value := range want {
			if got, _ := attrs[key].(string); got != value {
				t.Fatalf("attrs[%q] = %q, want %q; attrs=%v", key, got, value, attrs)
			}
		}
	})

	t.Run("falls_back_to_target_identity", func(t *testing.T) {
		attrs := map[string]interface{}{}
		addAttemptedTargetAttrs(attrs, &router.Selection{
			Target: &router.Target{
				ID:            "upstream-b",
				RouteTargetID: "target-b",
				ChannelID:     "channel-b",
				CredentialID:  "credential-b",
			},
		})
		want := map[string]string{
			"upstream_id":     "upstream-b",
			"route_target_id": "target-b",
			"channel_id":      "channel-b",
			"credential_id":   "credential-b",
		}
		for key, value := range want {
			if got, _ := attrs[key].(string); got != value {
				t.Fatalf("attrs[%q] = %q, want %q; attrs=%v", key, got, value, attrs)
			}
		}
	})

	t.Run("no_selection_is_a_no_op", func(t *testing.T) {
		attrs := map[string]interface{}{"http_status": 502}
		addAttemptedTargetAttrs(attrs, nil)
		if len(attrs) != 1 {
			t.Fatalf("attrs = %v, want only the pre-existing key", attrs)
		}
	})
}

// waitForLatestTraceEntry waits for the failure row to be indexed. The recorder
// finalizes inline in this test (no Background workers are started), but the
// wait keeps the test independent of that detail.
func waitForLatestTraceEntry(t *testing.T, st *store.Store, timeout time.Duration) store.LogEntry {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		entries, err := st.ListRecent(1)
		if err == nil && len(entries) > 0 {
			return entries[0]
		}
		lastErr = err
		if time.Now().After(deadline) {
			if lastErr != nil {
				t.Fatalf("timed out waiting for the failure trace row: %v", lastErr)
			}
			t.Fatalf("timed out waiting for the failure trace row")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func parseTracePrelude(t *testing.T, outputDir string, entry store.LogEntry) *recordfile.ParsedPrelude {
	t.Helper()
	path := entry.LogPath
	if path == "" {
		path = firstCassetteUnder(t, outputDir)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%s) error = %v", path, err)
	}
	parsed, err := recordfile.ParsePrelude(content)
	if err != nil {
		t.Fatalf("recordfile.ParsePrelude(%s) error = %v", path, err)
	}
	return parsed
}

func firstCassetteUnder(t *testing.T, root string) string {
	t.Helper()
	var found string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".http") && found == "" {
			found = path
		}
		return nil
	})
	if err != nil {
		t.Fatalf("filepath.WalkDir(%s) error = %v", root, err)
	}
	if found == "" {
		t.Fatalf("no cassette recorded under %s", root)
	}
	return found
}

func eventByType(events []recordfile.RecordEvent, eventType string) *recordfile.RecordEvent {
	for i := range events {
		if events[i].Type == eventType {
			return &events[i]
		}
	}
	return nil
}
