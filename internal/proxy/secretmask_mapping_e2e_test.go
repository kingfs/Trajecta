package proxy

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/store"
)

// TestHandlerMaskingMappingSurvivesRestart is the regression test for the
// two-fake bug: a stand-in the model saw in an earlier process came back as an
// unknown value to the new process, so the request path masked it into a second
// stand-in and the local side never saw the real value again.
//
// The test runs two handlers over one output directory. The first masks the real
// key and records the pair. The second starts with a fresh seed, receives the
// first fake as part of the conversation (which is what an upstream context or a
// compacted history carries), and must leave it unchanged on the way up while
// still restoring it on the way back.
func TestHandlerMaskingMappingSurvivesRestart(t *testing.T) {
	outputDir := t.TempDir()
	st, err := store.New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	var mu sync.Mutex
	var upstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		mu.Lock()
		upstreamBody = string(body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer upstream.Close()

	cfg := func() *config.Config {
		c := &config.Config{}
		c.Upstream.BaseURL = upstream.URL + "/v1"
		c.Debug.OutputDir = outputDir
		c.SecretMasking.Enabled = true
		c.SecretMasking.MappingStore.Enabled = true
		c.SecretMasking.MappingStore.LoadLimit = 100
		return c
	}
	bodyFor := func(value string) string {
		return `{"model":"gpt-5","messages":[{"role":"user","content":"key ` + value + `"}]}`
	}
	post := func(t *testing.T, url, body string) string {
		t.Helper()
		resp, err := http.Post(url+"/v1/chat/completions", "application/json", strings.NewReader(body))
		if err != nil {
			t.Fatalf("POST error = %v", err)
		}
		defer resp.Body.Close()
		got, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, body = %s", resp.StatusCode, got)
		}
		return string(got)
	}
	lastUpstream := func(t *testing.T) string {
		t.Helper()
		mu.Lock()
		defer mu.Unlock()
		return upstreamBody
	}

	first, err := NewHandler(cfg(), st)
	if err != nil {
		t.Fatalf("first NewHandler() error = %v", err)
	}
	firstServer := httptest.NewServer(first)
	clientBody := post(t, firstServer.URL, bodyFor(testOpenAIProjectKey))
	if clientBody != bodyFor(testOpenAIProjectKey) {
		t.Fatalf("first client body = %q, want the request body with the real key restored", clientBody)
	}
	firstUpstream := lastUpstream(t)
	index := strings.Index(firstUpstream, "sk-proj-")
	if index < 0 || index+len(testOpenAIProjectKey) > len(firstUpstream) {
		t.Fatalf("the upstream body has no stand-in: %s", firstUpstream)
	}
	firstFake := firstUpstream[index : index+len(testOpenAIProjectKey)]
	if firstFake == testOpenAIProjectKey {
		t.Fatal("the upstream received the real key")
	}
	firstServer.Close()
	if err := first.Close(); err != nil {
		t.Fatalf("first handler Close() error = %v", err)
	}

	second, err := NewHandler(cfg(), st)
	if err != nil {
		t.Fatalf("second NewHandler() error = %v", err)
	}
	defer second.Close()
	secondServer := httptest.NewServer(second)
	defer secondServer.Close()

	// The model context from before the restart holds firstFake. It must not be
	// masked again, and the local client must still see the real value.
	contextBody := bodyFor(firstFake)
	secondClientBody := post(t, secondServer.URL, contextBody)
	if got := lastUpstream(t); got != contextBody {
		t.Fatalf("the second process changed a recorded stand-in on the way up:\ngot  %s\nwant %s", got, contextBody)
	}
	if secondClientBody != bodyFor(testOpenAIProjectKey) {
		t.Fatalf("the local side did not get the real value back:\ngot  %s\nwant %s",
			secondClientBody, bodyFor(testOpenAIProjectKey))
	}

	// The real value still renders to the recorded fake, not a new one.
	post(t, secondServer.URL, bodyFor(testOpenAIProjectKey))
	if got := lastUpstream(t); got != bodyFor(firstFake) {
		t.Fatalf("the same real value got a different stand-in after restart:\ngot  %s\nwant %s",
			got, bodyFor(firstFake))
	}

	// Exactly one pair was recorded for the value, however many turns used it.
	if err := second.Close(); err != nil {
		t.Fatalf("second handler Close() error = %v", err)
	}
	mappings, err := st.OpenSecretMappingStore(filepath.Join(outputDir, store.SecretMappingFileName))
	if err != nil {
		t.Fatalf("OpenSecretMappingStore() error = %v", err)
	}
	defer mappings.Close()
	loaded, err := mappings.LoadMappings(10)
	if err != nil {
		t.Fatalf("LoadMappings() error = %v", err)
	}
	if len(loaded) != 1 {
		t.Fatalf("persisted %d mappings, want 1: %+v", len(loaded), loaded)
	}
	if loaded[0].Real != testOpenAIProjectKey || loaded[0].Fake != firstFake {
		t.Fatalf("persisted pair = %+v, want the real key and %q", loaded[0], firstFake)
	}
}

// TestNewSecretVaultMappingStoreConfig pins the wiring: disabling the mapping
// store keeps the vault in memory and creates no file, while a custom path is
// opened and flushed on Close.
func TestNewSecretVaultMappingStoreConfig(t *testing.T) {
	outputDir := t.TempDir()
	st, err := store.New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	cfg := &config.Config{}
	cfg.Debug.OutputDir = outputDir
	cfg.SecretMasking.Enabled = true

	cfg.SecretMasking.MappingStore.Enabled = false
	inMemory, err := newSecretVault(cfg, st)
	if err != nil {
		t.Fatalf("newSecretVault(disabled) error = %v", err)
	}
	if inMemory == nil {
		t.Fatal("newSecretVault(disabled) = nil, want an in-memory vault")
	}
	if _, err := os.Stat(filepath.Join(outputDir, store.SecretMappingFileName)); !os.IsNotExist(err) {
		t.Fatalf("mapping store disabled but the file exists: %v", err)
	}
	if err := inMemory.Close(); err != nil {
		t.Fatalf("in-memory Close() error = %v", err)
	}

	custom := filepath.Join(t.TempDir(), "custom-mappings.db")
	cfg.SecretMasking.MappingStore.Enabled = true
	cfg.SecretMasking.MappingStore.Path = custom
	cfg.SecretMasking.MappingStore.LoadLimit = 10
	persistent, err := newSecretVault(cfg, st)
	if err != nil {
		t.Fatalf("newSecretVault(custom path) error = %v", err)
	}
	body := []byte(`{"messages":[{"role":"user","content":"use ` + testOpenAIProjectKey + `"}]}`)
	if _, events := persistent.Mask(body); len(events) != 1 {
		t.Fatalf("Mask() produced %d events, want 1", len(events))
	}
	if err := persistent.Close(); err != nil {
		t.Fatalf("persistent Close() error = %v", err)
	}
	if _, err := os.Stat(custom); err != nil {
		t.Fatalf("custom mapping file was not created: %v", err)
	}
}
