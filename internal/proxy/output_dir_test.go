package proxy

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/store"
)

// TestRecorderUsesTheResolvedTraceOutputDirectory pins that a configuration which names only
// trace.output_dir records where it says.
//
// The store, the default SQLite path and the startup log all resolve trace.output_dir with a
// debug.output_dir fallback (Config.TraceOutputDir), but the handler handed Debug.OutputDir
// straight to the recorder. A configuration that set only trace.output_dir - the key the
// "trace" namespace and the store use - therefore wrote its cassettes relative to the process
// working directory while every index row pointed at that unexpected location, and a working
// directory the process cannot write made every request fail, because a recording error
// returns 500. The test moves the process into an empty directory first, so a regression shows
// up as a cassette outside the configured directory instead of a stray tree in the checkout.
func TestRecorderUsesTheResolvedTraceOutputDirectory(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping working-directory test in short mode")
	}

	for _, tc := range []struct {
		name     string
		traceDir bool
		debugDir bool
	}{
		{name: "only trace.output_dir is set", traceDir: true},
		{name: "only debug.output_dir is set", debugDir: true},
		{name: "both are set and trace wins", traceDir: true, debugDir: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// t.Chdir cannot be combined with t.Parallel, and the point of the test is what
			// happens relative to the working directory.
			workDir := t.TempDir()
			t.Chdir(workDir)

			upstream := newStubUpstream(t)
			st, err := store.New(t.TempDir())
			if err != nil {
				t.Fatalf("store.New() error = %v", err)
			}
			t.Cleanup(func() { _ = st.Close() })

			cfg := &config.Config{
				Upstreams: []config.UpstreamTargetConfig{{
					ID:             "trace-dir-target",
					Enabled:        boolPtr(true),
					Priority:       100,
					ModelDiscovery: router.ModelDiscoveryStaticOnly,
					StaticModels:   []string{"gpt-5"},
					Upstream: config.UpstreamConfig{
						BaseURL:        upstream.URL + "/v1",
						ApiKey:         "placeholder-key",
						ProviderPreset: "openai",
						APIType:        "chat_completions",
					},
				}},
			}
			expectedDir := t.TempDir()
			switch {
			case tc.traceDir:
				cfg.Trace.OutputDir = expectedDir
			case tc.debugDir:
				cfg.Debug.OutputDir = expectedDir
			}
			if tc.traceDir && tc.debugDir {
				// trace.output_dir must win over debug.output_dir, as TraceOutputDir documents.
				cfg.Debug.OutputDir = t.TempDir()
			}
			cfg.Debug.MaskKey = true

			handler, err := NewHandler(cfg, st)
			if err != nil {
				t.Fatalf("NewHandler() error = %v", err)
			}
			proxySrv := newStubProxyServer(t, handler)

			body := `{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`
			resp, err := http.Post(proxySrv.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
			if err != nil {
				t.Fatalf("post error = %v", err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d, want 200", resp.StatusCode)
			}

			recorded := findCassettes(t, expectedDir)
			if len(recorded) == 0 {
				t.Fatalf("no cassette under the configured directory %s; files under the working directory: %v", expectedDir, findCassettes(t, workDir))
			}
			if stray := findCassettes(t, workDir); len(stray) != 0 {
				t.Fatalf("cassettes were written under the process working directory instead of the configured directory: %v", stray)
			}
		})
	}
}

func newStubUpstream(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newStubProxyServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return srv
}

func findCassettes(t *testing.T, root string) []string {
	t.Helper()
	var found []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".http") {
			found = append(found, path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir(%s) error = %v", root, err)
	}
	return found
}
