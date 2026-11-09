// Package session resolves client session signals from request headers and,
// when no header names one, from a small set of body fields. It is a pure
// identity helper: it does not route, store, or expose the raw session value
// in any fingerprint.
package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
)

// Kind names the signal a session key came from.
type Kind string

const (
	KindHeader           Kind = "header"
	KindPreviousResponse Kind = "previous_response"
	KindPromptCache      Kind = "prompt_cache"
	KindConversation     Kind = "conversation"
	KindMetadata         Kind = "metadata"
	KindBody             Kind = "body"
)

// Confidence says how directly the signal identifies a conversation.
type Confidence string

const (
	ConfidenceHigh   Confidence = "high"
	ConfidenceMedium Confidence = "medium"
	ConfidenceLow    Confidence = "low"
)

// Ref is one resolved session identity.
type Ref struct {
	// Key is the raw signal for internal use. It must never be persisted in a
	// trace, cassette or database row; Fingerprint is the safe form.
	Key         string     `json:"-"`
	Fingerprint string     `json:"fingerprint,omitempty"`
	Kind        Kind       `json:"kind,omitempty"`
	Confidence  Confidence `json:"confidence,omitempty"`
	Source      string     `json:"source,omitempty"`
}

// Valid reports whether a session was resolved.
func (r Ref) Valid() bool { return strings.TrimSpace(r.Key) != "" }

// headerSources preserves the precedence the router has used historically:
// a generic Session_id wins over a product-specific id when a client sends
// both. Changing that order would move existing conversations to another
// upstream for no routing benefit, so the compatibility order is intentional.
var headerSources = []struct {
	name   string
	source string
	kind   Kind
	conf   Confidence
}{
	{"Session_id", "header:session_id", KindHeader, ConfidenceMedium},
	{"X-Session-Id", "header:x-session-id", KindHeader, ConfidenceMedium},
	{"Session-Id", "header:session-id", KindHeader, ConfidenceMedium},
	{"X-Conversation-Id", "header:x-conversation-id", KindHeader, ConfidenceMedium},
	{"X-Claude-Code-Session-Id", "header:x-claude-code-session-id", KindHeader, ConfidenceHigh},
	{"X-DeepSeek-Harness-Session-Id", "header:x-deepseek-harness-session-id", KindHeader, ConfidenceHigh},
	{"X-Codex-Window-Id", "header:x-codex-window-id", KindHeader, ConfidenceHigh},
}

// Resolve extracts a session from headers first, then body refs. A malformed
// body never makes the function fail: headers still give a usable result.
func Resolve(headers http.Header, body []byte) Ref {
	if ref, ok := fromHeaders(headers); ok {
		return ref
	}
	return fromBody(body)
}

func fromHeaders(headers http.Header) (Ref, bool) {
	if headers == nil {
		return Ref{}, false
	}
	for _, candidate := range headerSources {
		value := strings.TrimSpace(headers.Get(candidate.name))
		if value == "" {
			continue
		}
		// X-Codex-Window-Id is `<window>:...`; the prefix identifies the
		// window, not the suffix.
		if candidate.name == "X-Codex-Window-Id" {
			value = windowPrefix(value)
		}
		if value == "" {
			continue
		}
		return newRef(value, candidate.kind, candidate.conf, candidate.source), true
	}
	if value := codexTurnSession(headers.Get("X-Codex-Turn-Metadata")); value != "" {
		return newRef(value, KindMetadata, ConfidenceHigh, "header:x-codex-turn-metadata"), true
	}
	return Ref{}, false
}

func fromBody(body []byte) Ref {
	body = []byte(strings.TrimSpace(string(body)))
	if len(body) == 0 || body[0] != '{' {
		return Ref{}
	}
	var payload struct {
		PreviousResponseID string          `json:"previous_response_id"`
		PromptCacheKey     string          `json:"prompt_cache_key"`
		ConversationID     string          `json:"conversation_id"`
		SessionID          string          `json:"session_id"`
		Metadata           json.RawMessage `json:"metadata"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return Ref{}
	}
	if value := strings.TrimSpace(payload.PreviousResponseID); value != "" {
		return newRef(value, KindPreviousResponse, ConfidenceHigh, "body:previous_response_id")
	}
	if value := strings.TrimSpace(payload.PromptCacheKey); value != "" {
		return newRef(value, KindPromptCache, ConfidenceMedium, "body:prompt_cache_key")
	}
	if value := metadataSession(payload.Metadata); value != "" {
		return newRef(value, KindMetadata, ConfidenceMedium, "body:metadata.session_id")
	}
	if value := strings.TrimSpace(payload.ConversationID); value != "" {
		return newRef(value, KindConversation, ConfidenceHigh, "body:conversation_id")
	}
	if value := strings.TrimSpace(payload.SessionID); value != "" {
		return newRef(value, KindBody, ConfidenceMedium, "body:session_id")
	}
	return Ref{}
}

func metadataSession(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var metadata struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(raw, &metadata); err != nil {
		return ""
	}
	return strings.TrimSpace(metadata.SessionID)
}

func codexTurnSession(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var payload struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.SessionID)
}

func windowPrefix(raw string) string {
	raw = strings.TrimSpace(raw)
	if prefix, _, ok := strings.Cut(raw, ":"); ok {
		return strings.TrimSpace(prefix)
	}
	return raw
}

func newRef(key string, kind Kind, confidence Confidence, source string) Ref {
	return Ref{
		Key:         key,
		Fingerprint: Fingerprint(key),
		Kind:        kind,
		Confidence:  confidence,
		Source:      source,
	}
}

// Fingerprint renders a session key for diagnostics and persistence without
// exposing it. It returns "sha256:<16 hex chars>".
func Fingerprint(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return "sha256:" + hex.EncodeToString(sum[:8])
}

// Compose builds the binding key for one (session, model) pair. The separator
// is a control character no client id or model name contains.
func Compose(key, model string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return key
	}
	return key + "\x1f" + model
}

// BindingFingerprint is the safe form of Compose for traces.
func BindingFingerprint(ref Ref, model string) string {
	if !ref.Valid() {
		return ""
	}
	return Fingerprint(Compose(ref.Key, model))
}
