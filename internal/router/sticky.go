package router

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kingfs/Trajecta/pkg/session"
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

// extractStickyKey resolves the client's session identifier from the request.
//
// The signal set lives in pkg/session so the router, the recorder and audit
// consumers cannot drift on which headers or body fields name a conversation.
// The value is client-supplied, so it must never be written to a log line, a
// cassette, or the application database; diagnostics use
// stickyKeyFingerprint instead.
func extractStickyKey(req *http.Request, body []byte) string {
	if req == nil {
		return extractStickyKeyFromBody(body)
	}
	return session.Resolve(req.Header, body).Key
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

// extractStickyKeyFromBody resolves a session from the body alone. It is kept
// as a small wrapper for tests and callers that have no request headers.
func extractStickyKeyFromBody(body []byte) string {
	return session.Resolve(nil, body).Key
}
