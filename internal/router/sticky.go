package router

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultStickyBindingTTL = time.Hour

	// maxStickyBindings caps the live binding map.
	//
	// The keys are client-supplied: a `Session_id`, an
	// `X-Claude-Code-Session-Id`, an `X-Codex-Window-Id` prefix, an
	// `X-Codex-Turn-Metadata` session id, or a body `previous_response_id`. An
	// entry previously disappeared only when the client happened to reuse that
	// exact key (Lookup deletes it once expired), so a client that varies its
	// session id grew the map without bound for the life of the process. The cap
	// makes the allocation finite; 100k bindings is roughly a few megabytes and
	// is far above the number of sessions a single proxy serves concurrently.
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

func extractStickyKey(req *http.Request, body []byte) string {
	if req == nil {
		return extractStickyKeyFromBody(body)
	}
	if key := strings.TrimSpace(req.Header.Get("Session_id")); key != "" {
		return key
	}
	if key := strings.TrimSpace(req.Header.Get("X-Claude-Code-Session-Id")); key != "" {
		return key
	}
	if key := stickyWindowPrefix(req.Header.Get("X-Codex-Window-Id")); key != "" {
		return key
	}
	if key := stickySessionFromMetadata(req.Header.Get("X-Codex-Turn-Metadata")); key != "" {
		return key
	}
	return extractStickyKeyFromBody(body)
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

func stickySessionFromMetadata(raw string) string {
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

func extractStickyKeyFromBody(body []byte) string {
	body = bytes.TrimSpace(body)
	if len(body) == 0 || !json.Valid(body) {
		return ""
	}
	var payload struct {
		PreviousResponseID string `json:"previous_response_id"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return strings.TrimSpace(payload.PreviousResponseID)
}
