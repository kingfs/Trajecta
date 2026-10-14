package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/recordfile"
)

const (
	testOpenAIProjectKey = "sk-proj-" + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" +
		"T3BlbkFJ" + "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	// google.api_key pins an exact 39-byte length, so the fixture must too.
	testGoogleAPIKey = "AIza" + "SyA1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q"
)

// TestSecretMaskingFixtures guards the fixture constants against the rule
// bounds: a key that is one byte too long simply never matches, and the test
// would then pass while asserting nothing.
func TestSecretMaskingFixtures(t *testing.T) {
	if got := len(testOpenAIProjectKey); got != 132 {
		t.Errorf("openai project key is %d bytes, want 132", got)
	}
	if got := len(testGoogleAPIKey); got != 39 {
		t.Errorf("google api key is %d bytes, want 39", got)
	}
}

// secretMaskingBody is a request body carrying two credentials of different
// strengths: the OpenAI project key is conclusive on shape alone, the Google key
// is conclusive on prefix plus exact length.
func secretMaskingBody() string {
	return `{"model":"gpt-5","messages":[{"role":"user","content":"key one ` +
		testOpenAIProjectKey + ` and key two ` + testGoogleAPIKey + `"}]}`
}

// TestHandlerMasksCredentialsOnTheWire is the end-to-end contract: the upstream
// must never receive the real value, must receive something the same length and
// shape, and a value it echoes back must reach the client as the original.
//
// The upstream deliberately echoes the key it was given rather than a
// precomputed stand-in, so the assertion that the client sees the original key
// exercises both directions and depends on the same-length property: the test
// recovers the stand-in by slicing the request body at the real key's offset and
// length.
func TestHandlerMasksCredentialsOnTheWire(t *testing.T) {
	outputDir := t.TempDir()
	st, err := store.New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	var upstreamBody string
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upstreamBody = string(body)

		// Echo back whatever stood in for the OpenAI project key, located by
		// prefix and sized by the real value's length.
		seen := "(missing)"
		if i := bytes.Index(body, []byte("sk-proj-")); i >= 0 {
			if i+len(testOpenAIProjectKey) <= len(body) {
				seen = string(body[i : i+len(testOpenAIProjectKey)])
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"echo": seen})
	}))
	defer upstreamServer.Close()

	cfg := &config.Config{}
	cfg.Upstream.BaseURL = upstreamServer.URL + "/v1"
	cfg.Debug.OutputDir = outputDir
	cfg.SecretMasking.Enabled = true

	handler, err := NewHandler(cfg, st)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	resp, err := proxyServer.Client().Post(proxyServer.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(secretMaskingBody()))
	if err != nil {
		t.Fatalf("client.Post() error = %v", err)
	}
	respBody, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", resp.StatusCode, respBody)
	}

	// The upstream saw stand-ins, never the real values.
	for _, secret := range []string{testOpenAIProjectKey, testGoogleAPIKey} {
		if strings.Contains(upstreamBody, secret) {
			t.Errorf("the upstream received the real value %q", secret)
		}
	}
	if !strings.Contains(upstreamBody, "sk-proj-") {
		t.Errorf("the stand-in lost the vendor prefix: %s", upstreamBody)
	}
	if !strings.Contains(upstreamBody, "T3BlbkFJ") {
		t.Errorf("the stand-in lost the vendor watermark: %s", upstreamBody)
	}
	if !strings.Contains(upstreamBody, "AIza") {
		t.Errorf("the stand-in for the Google key lost its prefix: %s", upstreamBody)
	}
	if len(upstreamBody) != len(secretMaskingBody()) {
		t.Errorf("body length changed: got %d, want %d; content-length would desynchronise",
			len(upstreamBody), len(secretMaskingBody()))
	}

	// The model's echo came back as the original value.
	var echoed struct {
		Echo string `json:"echo"`
	}
	if err := json.Unmarshal(respBody, &echoed); err != nil {
		t.Fatalf("decode response %q: %v", respBody, err)
	}
	if echoed.Echo != testOpenAIProjectKey {
		t.Errorf("client received %q, want the original key restored", echoed.Echo)
	}

	// The cassette holds the original bytes, so replay and inspection see the
	// real request.
	path := findRecordedHTTP(t, outputDir)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read cassette %q: %v", path, err)
	}
	parsed, err := waitForRecordedPrelude(path, time.Second)
	if err != nil {
		t.Fatalf("waitForRecordedPrelude() error = %v", err)
	}
	_, recordedBody, _, _ := recordfile.ExtractSections(raw, parsed)
	if !strings.Contains(string(recordedBody), testOpenAIProjectKey) {
		t.Errorf("the cassette did not record the original body: %s", recordedBody)
	}

	// The audit trail names the rule without carrying the value.
	var masked, restored int
	for _, event := range parsed.Events {
		switch event.Type {
		case "secret.masked":
			masked++
			if rule, _ := event.Attributes["rule"].(string); rule == "" {
				t.Errorf("secret.masked event has no rule: %+v", event.Attributes)
			}
			for _, secret := range []string{testOpenAIProjectKey, testGoogleAPIKey} {
				if strings.Contains(recordfileEventText(event), secret) {
					t.Errorf("secret.masked event leaked the value %q", secret)
				}
			}
		case "secret.restored":
			restored++
		}
	}
	if masked != 2 {
		t.Errorf("recorded %d secret.masked events, want 2", masked)
	}
	if restored != 1 {
		t.Errorf("recorded %d secret.restored events, want 1", restored)
	}
}

// TestHandlerLeavesBodiesAloneWhenMaskingIsOff pins that the feature is opt-in
// and that a disabled vault costs nothing: the upstream sees the exact client
// bytes and no masking event is written.
func TestHandlerLeavesBodiesAloneWhenMaskingIsOff(t *testing.T) {
	outputDir := t.TempDir()
	st, err := store.New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	var upstreamBody string
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		upstreamBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer upstreamServer.Close()

	cfg := &config.Config{}
	cfg.Upstream.BaseURL = upstreamServer.URL + "/v1"
	cfg.Debug.OutputDir = outputDir

	handler, err := NewHandler(cfg, st)
	if err != nil {
		t.Fatalf("NewHandler() error = %v", err)
	}
	proxyServer := httptest.NewServer(handler)
	defer proxyServer.Close()

	resp, err := proxyServer.Client().Post(proxyServer.URL+"/v1/chat/completions", "application/json",
		strings.NewReader(secretMaskingBody()))
	if err != nil {
		t.Fatalf("client.Post() error = %v", err)
	}
	_, _ = io.ReadAll(resp.Body)
	resp.Body.Close()

	if upstreamBody != secretMaskingBody() {
		t.Fatalf("upstream body changed with masking off\n got %s\nwant %s", upstreamBody, secretMaskingBody())
	}

	parsed, err := waitForRecordedPrelude(findRecordedHTTP(t, outputDir), time.Second)
	if err != nil {
		t.Fatalf("waitForRecordedPrelude() error = %v", err)
	}
	for _, event := range parsed.Events {
		if event.Type == "secret.masked" || event.Type == "secret.restored" {
			t.Errorf("masking is off but %q was recorded", event.Type)
		}
	}
}

// recordfileEventText renders an event for a leak check.
func recordfileEventText(event recordfile.RecordEvent) string {
	raw, err := json.Marshal(event.Attributes)
	if err != nil {
		return ""
	}
	return string(raw)
}
