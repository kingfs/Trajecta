package router

import (
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestStickyBindingsAreReclaimedWhenExpired pins that bindings do not
// accumulate for the life of the process.
//
// A binding key comes from the client (a session id, a Codex window id, or a
// previous_response_id) and used to be removed only when that same key was
// looked up again after its TTL had passed. A client that varies its session id
// therefore grew the map without bound, and nothing else ever reclaimed the
// memory.
func TestStickyBindingsAreReclaimedWhenExpired(t *testing.T) {
	now := time.Now()
	store := NewStickyBindingStore(time.Minute)
	store.now = func() time.Time { return now }

	for i := 0; i < 50; i++ {
		store.Bind("session-"+strconv.Itoa(i), "target-a")
	}

	// Move past the TTL and the sweep interval, then write once.
	now = now.Add(2 * time.Minute)
	store.Bind("session-fresh", "target-a")

	store.mu.Lock()
	size := len(store.bindings)
	_, freshKept := store.bindings["session-fresh"]
	store.mu.Unlock()

	if size != 1 {
		t.Fatalf("bindings after expiry = %d, want only the fresh binding", size)
	}
	if !freshKept {
		t.Fatal("the fresh binding was dropped by the sweep")
	}
	if _, ok := store.Lookup("session-0"); ok {
		t.Fatal("an expired binding still resolved")
	}
}

// TestStickyBindingsStayBoundedUnderDistinctKeys pins the hard cap: a client
// that never reuses a session id must not be able to grow the map past
// maxStickyBindings, and the entries kept must be the ones furthest from expiry.
func TestStickyBindingsStayBoundedUnderDistinctKeys(t *testing.T) {
	now := time.Now()
	store := NewStickyBindingStore(time.Hour)
	store.now = func() time.Time { return now }

	// Advance the clock per bind so the entries have distinct expiry times and
	// the documented eviction order (closest to expiry first) is observable.
	total := maxStickyBindings + 5_000
	for i := 0; i < total; i++ {
		now = now.Add(time.Microsecond)
		store.Bind("session-"+strconv.Itoa(i), "target-a")
	}

	store.mu.Lock()
	size := len(store.bindings)
	store.mu.Unlock()

	if size > maxStickyBindings {
		t.Fatalf("bindings = %d, want at most %d", size, maxStickyBindings)
	}
	// The oldest keys are the ones closest to expiry, so they are the ones
	// evicted; the newest must survive.
	if _, ok := store.Lookup("session-0"); ok {
		t.Fatal("the oldest binding survived eviction")
	}
	if _, ok := store.Lookup("session-" + strconv.Itoa(total-1)); !ok {
		t.Fatal("the newest binding was evicted")
	}
}

// TestStickyLookupStillReclaimsItsOwnExpiredKey pins that the lazy path is
// unchanged: a lookup of an expired key removes that key and reports a miss.
func TestStickyLookupStillReclaimsItsOwnExpiredKey(t *testing.T) {
	now := time.Now()
	store := NewStickyBindingStore(time.Minute)
	store.now = func() time.Time { return now }

	store.Bind("session-a", "target-a")
	if got, ok := store.Lookup("session-a"); !ok || got != "target-a" {
		t.Fatalf("Lookup before expiry = %q, %v", got, ok)
	}

	now = now.Add(2 * time.Minute)
	if got, ok := store.Lookup("session-a"); ok {
		t.Fatalf("Lookup after expiry = %q, %v, want a miss", got, ok)
	}

	store.mu.Lock()
	size := len(store.bindings)
	store.mu.Unlock()
	if size != 0 {
		t.Fatalf("bindings after expired lookup = %d, want 0", size)
	}
}

func stickyTestRequest(t *testing.T, headers map[string]string, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/chat/completions", strings.NewReader(body))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	return req
}

// TestExtractStickyKeyHeaderSources pins the header conventions this proxy
// accepts. Different clients name the same session differently, and reading only
// `Session_id` silently gave every other client no affinity at all.
func TestExtractStickyKeyHeaderSources(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		body    string
		want    string
	}{
		{name: "session_id", headers: map[string]string{"Session_id": "session-a"}, want: "session-a"},
		{name: "x-session-id", headers: map[string]string{"X-Session-Id": "session-b"}, want: "session-b"},
		{name: "session-id", headers: map[string]string{"Session-Id": "session-c"}, want: "session-c"},
		{name: "x-conversation-id", headers: map[string]string{"X-Conversation-Id": "conversation-d"}, want: "conversation-d"},
		{name: "x-claude-code-session-id", headers: map[string]string{"X-Claude-Code-Session-Id": "claude-e"}, want: "claude-e"},
		{name: "x-codex-window-id keeps the prefix", headers: map[string]string{"X-Codex-Window-Id": "window-f:turn-1"}, want: "window-f"},
		{name: "x-codex-turn-metadata session id", headers: map[string]string{"X-Codex-Turn-Metadata": `{"session_id":"codex-g"}`}, want: "codex-g"},
		{
			name:    "session_id wins over later headers",
			headers: map[string]string{"Session_id": "first", "X-Session-Id": "second", "X-Claude-Code-Session-Id": "third"},
			want:    "first",
		},
		{
			name:    "x-session-id wins over session-id",
			headers: map[string]string{"X-Session-Id": "second", "Session-Id": "third"},
			want:    "second",
		},
		{
			name:    "header wins over body",
			headers: map[string]string{"Session_id": "header-session"},
			body:    `{"previous_response_id":"resp-1"}`,
			want:    "header-session",
		},
		{name: "whitespace trimmed", headers: map[string]string{"Session_id": "  padded  "}, want: "padded"},
		{name: "no source", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractStickyKey(stickyTestRequest(t, tt.headers, tt.body), []byte(tt.body))
			if got != tt.want {
				t.Fatalf("extractStickyKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestExtractStickyKeyBodyFallbacks pins the body sources in priority order. The
// body is decoded once for all of them.
func TestExtractStickyKeyBodyFallbacks(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{name: "previous_response_id", body: `{"previous_response_id":"resp-1","prompt_cache_key":"cache-1"}`, want: "resp-1"},
		{name: "prompt_cache_key", body: `{"prompt_cache_key":"cache-1","session_id":"session-1"}`, want: "cache-1"},
		{name: "metadata session id", body: `{"metadata":{"session_id":"meta-1"},"conversation_id":"conversation-1","session_id":"session-1"}`, want: "meta-1"},
		{name: "conversation_id", body: `{"conversation_id":"conversation-1","session_id":"session-1"}`, want: "conversation-1"},
		{name: "session_id", body: `{"session_id":"session-1"}`, want: "session-1"},
		{name: "non-object metadata keeps the later fallbacks", body: `{"metadata":"not-an-object","session_id":"session-2"}`, want: "session-2"},
		{name: "invalid json", body: `not json`, want: ""},
		{name: "empty body", body: ``, want: ""},
		{name: "unrelated fields", body: `{"model":"gpt-5","input":"hello"}`, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := stickyTestRequest(t, nil, tt.body)
			if got := extractStickyKey(req, []byte(tt.body)); got != tt.want {
				t.Fatalf("extractStickyKey() = %q, want %q", got, tt.want)
			}
			if got := extractStickyKeyFromBody([]byte(tt.body)); got != tt.want {
				t.Fatalf("extractStickyKeyFromBody() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestComposeStickyKey pins the per-model binding key: one session keeps one
// provider route per model, and the model half is normalised so `gpt-5` and
// `GPT-5` name the same binding.
func TestComposeStickyKey(t *testing.T) {
	tests := []struct {
		name    string
		session string
		model   string
		want    string
	}{
		{name: "session and model", session: "session-a", model: "gpt-5", want: "session-a" + stickyKeySeparator + "gpt-5"},
		{name: "model is case-insensitive", session: "session-a", model: "GPT-5", want: "session-a" + stickyKeySeparator + "gpt-5"},
		{name: "both sides trimmed", session: "  session-a  ", model: "  gpt-5  ", want: "session-a" + stickyKeySeparator + "gpt-5"},
		{name: "no model keeps the session key", session: "session-a", model: "", want: "session-a"},
		{name: "no session means no binding", session: "", model: "gpt-5", want: ""},
		{name: "nothing", session: "", model: "", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := composeStickyKey(tt.session, tt.model); got != tt.want {
				t.Fatalf("composeStickyKey(%q, %q) = %q, want %q", tt.session, tt.model, got, tt.want)
			}
		})
	}
}

// TestStickyKeyFingerprintHidesTheSessionValue pins that diagnostics can identify
// a binding without carrying the client's session value.
func TestStickyKeyFingerprintHidesTheSessionValue(t *testing.T) {
	key := composeStickyKey("session-secret-value", "gpt-5")
	fingerprint := stickyKeyFingerprint(key)
	if fingerprint == "" {
		t.Fatal("stickyKeyFingerprint() = empty, want a fingerprint")
	}
	if strings.Contains(fingerprint, "session-secret-value") {
		t.Fatalf("fingerprint %q exposes the session value", fingerprint)
	}
	if again := stickyKeyFingerprint(key); again != fingerprint {
		t.Fatalf("fingerprint is not stable: %q != %q", again, fingerprint)
	}
	if other := stickyKeyFingerprint(composeStickyKey("session-secret-value", "gpt-4o")); other == fingerprint {
		t.Fatal("different models produced the same fingerprint")
	}
	if empty := stickyKeyFingerprint(""); empty != "" {
		t.Fatalf("stickyKeyFingerprint(\"\") = %q, want empty", empty)
	}
}
