package session

import (
	"net/http"
	"strings"
	"testing"
)

func TestHeaderPrecedence(t *testing.T) {
	h := http.Header{}
	h.Set("Session_id", "generic")
	h.Set("X-Claude-Code-Session-Id", "claude")
	ref := Resolve(h, nil)
	if ref.Key != "generic" || ref.Kind != KindHeader || ref.Source != "header:session_id" {
		t.Fatalf("ref = %+v", ref)
	}
}

func TestCodexWindowPrefix(t *testing.T) {
	h := http.Header{}
	h.Set("X-Codex-Window-Id", "window-1:turn-2")
	ref := Resolve(h, nil)
	if ref.Key != "window-1" {
		t.Fatalf("key = %q, want window-1", ref.Key)
	}
}

func TestCodexTurnMetadata(t *testing.T) {
	h := http.Header{}
	h.Set("X-Codex-Turn-Metadata", `{"session_id":"s-1"}`)
	ref := Resolve(h, nil)
	if ref.Key != "s-1" || ref.Source != "header:x-codex-turn-metadata" {
		t.Fatalf("ref = %+v", ref)
	}
}

func TestBodyFallbacks(t *testing.T) {
	cases := []struct {
		body string
		key  string
		kind Kind
	}{
		{`{"previous_response_id":"r1"}`, "r1", KindPreviousResponse},
		{`{"prompt_cache_key":"c1"}`, "c1", KindPromptCache},
		{`{"metadata":{"session_id":"m1"}}`, "m1", KindMetadata},
		{`{"conversation_id":"conv"}`, "conv", KindConversation},
		{`{"session_id":"s1"}`, "s1", KindBody},
	}
	for _, tc := range cases {
		ref := Resolve(nil, []byte(tc.body))
		if ref.Key != tc.key || ref.Kind != tc.kind {
			t.Fatalf("Resolve(%s) = %+v", tc.body, ref)
		}
	}
}

func TestBodyMalformedFallsBackToEmpty(t *testing.T) {
	if ref := Resolve(nil, []byte(`{bad`)); ref.Valid() {
		t.Fatalf("malformed body resolved %+v", ref)
	}
	if ref := Resolve(nil, []byte(`[]`)); ref.Valid() {
		t.Fatalf("array body resolved %+v", ref)
	}
}

func TestFingerprintDoesNotExposeKey(t *testing.T) {
	ref := Resolve(nil, []byte(`{"session_id":"secret-session"}`))
	if ref.Fingerprint == "" || strings.Contains(ref.Fingerprint, "secret-session") {
		t.Fatalf("fingerprint = %q", ref.Fingerprint)
	}
	if !strings.HasPrefix(ref.Fingerprint, "sha256:") {
		t.Fatalf("fingerprint = %q", ref.Fingerprint)
	}
}

func TestComposeAndBindingFingerprint(t *testing.T) {
	ref := Resolve(nil, []byte(`{"session_id":"s"}`))
	key := Compose(ref.Key, "GPT-5")
	if key != "s\x1fgpt-5" {
		t.Fatalf("compose = %q", key)
	}
	if BindingFingerprint(ref, "gpt-5") == "" {
		t.Fatal("empty binding fingerprint")
	}
	if BindingFingerprint(Ref{}, "gpt-5") != "" {
		t.Fatal("empty session should have no binding fingerprint")
	}
}

func TestFingerprintStable(t *testing.T) {
	a := Fingerprint("x")
	b := Fingerprint("x")
	if a != b || a == "" {
		t.Fatalf("unstable fingerprint %q/%q", a, b)
	}
}
