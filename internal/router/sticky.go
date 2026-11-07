package router

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultStickyBindingTTL = time.Hour

	// defaultStickyMaxCostRatio is how much more expensive the bound target may
	// be before a session is moved to the cheapest alternative. Two leaves
	// ordinary jitter and prompt-size variance alone while still breaking a
	// binding to hardware that is several times slower. Operators can raise,
	// lower or disable the check with router.selection.sticky_max_cost_ratio.
	defaultStickyMaxCostRatio = 2.0

	// stickyKeySeparator joins the session value and the model name in one
	// binding key. It is a control character no client id or model name
	// contains, so the composed key cannot collide with a bare session value.
	stickyKeySeparator = "\x1f"

	// maxStickyBindings caps the live binding map.
	//
	// The keys are client-supplied: a `Session_id`, an `X-Session-Id`, a
	// `Session-Id`, an `X-Conversation-Id`, an `X-Claude-Code-Session-Id`, an
	// `X-Codex-Window-Id` prefix, an `X-Codex-Turn-Metadata` session id, or a
	// body `previous_response_id` / `prompt_cache_key` / `metadata.session_id` /
	// `conversation_id` / `session_id`. An entry previously disappeared only
	// when the client happened to reuse that exact key (Lookup deletes it once
	// expired), so a client that varies its session id grew the map without
	// bound for the life of the process. The cap makes the allocation finite;
	// 100k bindings is roughly a few megabytes and is far above the number of
	// sessions a single proxy serves concurrently.
	maxStickyBindings = 100_000

	// stickyEvictionBatch is how far below the cap a capacity sweep goes. The
	// cap is enforced in batches so the O(n log n) selection of the entries
	// nearest to expiry is paid once per batch instead of on every write once
	// the map is saturated.
	stickyEvictionBatch = maxStickyBindings / 10
)

type stickyBinding struct {
	targetID  string
	expiresAt time.Time
}

type StickyBindingStore struct {
	mu        sync.Mutex
	ttl       time.Duration
	bindings  map[string]stickyBinding
	now       func() time.Time
	nextSweep time.Time
}

func NewStickyBindingStore(ttl time.Duration) *StickyBindingStore {
	if ttl <= 0 {
		ttl = defaultStickyBindingTTL
	}
	return &StickyBindingStore{
		ttl:      ttl,
		bindings: make(map[string]stickyBinding),
		now:      time.Now,
	}
}

// sweepInterval is how long a completed sweep suppresses the next one, so the
// cleanup cost is amortised across writes instead of paid per write.
func (s *StickyBindingStore) sweepInterval() time.Duration {
	interval := s.ttl / 4
	if interval < time.Second {
		interval = time.Second
	}
	if interval > time.Minute {
		interval = time.Minute
	}
	return interval
}

// evictLocked drops every expired binding and then, while the map is still
// above target (all remaining entries are live), the entries closest to expiry.
// Since every binding gets the same TTL, "closest to expiry" is the oldest
// insertion. The caller holds s.mu.
func (s *StickyBindingStore) evictLocked(now time.Time, target int) {
	for key, binding := range s.bindings {
		if !binding.expiresAt.IsZero() && !now.Before(binding.expiresAt) {
			delete(s.bindings, key)
		}
	}
	remove := len(s.bindings) - target
	if remove <= 0 {
		return
	}
	keys := make([]string, 0, len(s.bindings))
	for key := range s.bindings {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return s.bindings[keys[i]].expiresAt.Before(s.bindings[keys[j]].expiresAt)
	})
	for _, key := range keys[:remove] {
		delete(s.bindings, key)
	}
}

func (s *StickyBindingStore) Lookup(key string) (string, bool) {
	key = strings.TrimSpace(key)
	if s == nil || key == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	binding, ok := s.bindings[key]
	if !ok {
		return "", false
	}
	if !binding.expiresAt.IsZero() && !s.now().Before(binding.expiresAt) {
		delete(s.bindings, key)
		return "", false
	}
	return binding.targetID, true
}

func (s *StickyBindingStore) Bind(key string, targetID string) {
	key = strings.TrimSpace(key)
	targetID = strings.TrimSpace(targetID)
	if s == nil || key == "" || targetID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	// Bind is the only path that grows the map, so it is also where expired
	// entries are reclaimed. A zero nextSweep makes the first Bind sweep.
	if !now.Before(s.nextSweep) {
		s.evictLocked(now, maxStickyBindings)
		s.nextSweep = now.Add(s.sweepInterval())
	}
	// Make room in a batch, so that the entry about to be inserted is the only
	// one that can push the map to the cap: the invariant after every Bind is
	// len(bindings) <= maxStickyBindings.
	if len(s.bindings) >= maxStickyBindings {
		s.evictLocked(now, maxStickyBindings-stickyEvictionBatch)
	}
	s.bindings[key] = stickyBinding{
		targetID:  targetID,
		expiresAt: now.Add(s.ttl),
	}
}

// stickyHeaderSources are the request headers consulted for a session id, in
// priority order. Each one is a real client convention: OpenAI/Codex SDKs and
// Claude Code disagree on the name, and a proxy that only read `Session_id`
// silently gave no affinity to every other client.
var stickyHeaderSources = []string{
	"Session_id",
	"X-Session-Id",
	"Session-Id",
	"X-Conversation-Id",
	"X-Claude-Code-Session-Id",
}

// extractStickyKey resolves the client's session identifier from the request.
//
// It reads a small, ordered set of headers first and falls back to the body.
// The value is client-supplied and identifies a conversation, so it must never
// be written to a log line, a cassette, or the application database: diagnostics
// use stickyKeyFingerprint instead.
func extractStickyKey(req *http.Request, body []byte) string {
	if req == nil {
		return extractStickyKeyFromBody(body)
	}
	for _, header := range stickyHeaderSources {
		if key := strings.TrimSpace(req.Header.Get(header)); key != "" {
			return key
		}
	}
	if key := stickyWindowPrefix(req.Header.Get("X-Codex-Window-Id")); key != "" {
		return key
	}
	if key := stickySessionIDFromJSON([]byte(req.Header.Get("X-Codex-Turn-Metadata"))); key != "" {
		return key
	}
	return extractStickyKeyFromBody(body)
}

// composeStickyKey binds one session per model: `session + "\x1f" + model`.
//
// The model has to be part of the key because the binding chooses the route
// target - and therefore the provider-side prompt cache - for one model only.
// A request for a second model on the same session must not rewrite the first
// model's binding (which would break its cache locality) and must get its own
// binding so it can keep that model's cache warm on its own provider.
//
// A session without a model keeps the historical session-only key; no session
// means no binding at all.
func composeStickyKey(session string, model string) string {
	session = strings.TrimSpace(session)
	if session == "" {
		return ""
	}
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return session
	}
	return session + stickyKeySeparator + model
}

// stickyKeyFingerprint renders a sticky key for diagnostics without exposing the
// client's session value. Every recorded routing event and every persisted
// routing summary stores this fingerprint, never the key itself.
func stickyKeyFingerprint(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(key))
	return fmt.Sprintf("sha256:%x", sum[:8])
}

func stickyWindowPrefix(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	prefix, _, ok := strings.Cut(raw, ":")
	if !ok {
		return raw
	}
	return strings.TrimSpace(prefix)
}

// stickySessionIDFromJSON reads `{"session_id": ...}` from a header value or a
// raw body field. It returns "" for empty or malformed input instead of failing
// the request: a session hint is an optimisation, never a requirement.
func stickySessionIDFromJSON(raw []byte) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return ""
	}
	var payload struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.SessionID)
}

// extractStickyKeyFromBody decodes the request body once and returns the first
// session-ish field it carries. The order matches how the client protocols
// chain a conversation: the Responses API's previous response, then the Chat
// Completions prompt-cache key, then the common metadata/conversation/session
// fields. metadata is kept raw so a non-object metadata field cannot make the
// whole body unreadable and lose the other fallbacks.
func extractStickyKeyFromBody(body []byte) string {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return ""
	}
	var payload struct {
		PreviousResponseID string          `json:"previous_response_id"`
		PromptCacheKey     string          `json:"prompt_cache_key"`
		Metadata           json.RawMessage `json:"metadata"`
		ConversationID     string          `json:"conversation_id"`
		SessionID          string          `json:"session_id"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	for _, candidate := range []string{
		payload.PreviousResponseID,
		payload.PromptCacheKey,
		stickySessionIDFromJSON(payload.Metadata),
		payload.ConversationID,
		payload.SessionID,
	} {
		if key := strings.TrimSpace(candidate); key != "" {
			return key
		}
	}
	return ""
}
