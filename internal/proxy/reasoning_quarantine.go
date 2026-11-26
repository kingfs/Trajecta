package proxy

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"github.com/kingfs/Trajecta/internal/gateway"
	"github.com/kingfs/Trajecta/internal/recorder"
	"github.com/kingfs/Trajecta/pkg/xir/wire"
)

const (
	reasoningQuarantineTTL     = 24 * time.Hour
	reasoningQuarantineMaxKeys = 8192
)

// reasoningQuarantine remembers opaque reasoning blobs an upstream rejected
// with invalid_encrypted_content. Such a blob is bound to the relay's session
// or backend; once that context is gone the same bytes can never be verified
// again, and a client that keeps resending them fails every later turn.
//
// The map stores only a sha256, not the ciphertext: the proxy needs to recognise
// the blob when it reappears, not to read it. It is process-local because the
// remote session it describes is process-local too; a restart simply learns the
// invalid blob again on the first failure.
type reasoningQuarantine struct {
	mu      sync.Mutex
	entries map[string]map[[32]byte]time.Time
	ttl     time.Duration
	max     int
	now     func() time.Time
}

func newReasoningQuarantine() *reasoningQuarantine {
	return &reasoningQuarantine{
		entries: make(map[string]map[[32]byte]time.Time),
		ttl:     reasoningQuarantineTTL,
		max:     reasoningQuarantineMaxKeys,
		now:     time.Now,
	}
}

// Empty reports whether any live quarantine entry exists. The request
// transform uses it to keep the normal Responses fast path free of JSON parsing
// until an upstream actually rejects a blob.
func (q *reasoningQuarantine) Empty() bool {
	if q == nil {
		return true
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	q.pruneLocked(now)
	for _, entries := range q.entries {
		if len(entries) > 0 {
			return false
		}
	}
	return true
}

// Quarantined reports whether this upstream already rejected blobHash.
func (q *reasoningQuarantine) Quarantined(upstreamID string, blobHash [32]byte) bool {
	if q == nil {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	q.pruneLocked(now)
	entries := q.entries[upstreamID]
	if entries == nil {
		return false
	}
	expires, ok := entries[blobHash]
	return ok && now.Before(expires)
}

// QuarantineBlob records one rejected blob and returns its hash. added is false
// when the same blob was already quarantined for this upstream.
func (q *reasoningQuarantine) QuarantineBlob(upstreamID, blob string) (hash [32]byte, added bool) {
	hash = sha256.Sum256([]byte(blob))
	if q == nil || len(blob) == 0 {
		return hash, false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	now := q.now()
	q.pruneLocked(now)
	entries := q.entries[upstreamID]
	if entries == nil {
		entries = make(map[[32]byte]time.Time)
		q.entries[upstreamID] = entries
	}
	if expires, ok := entries[hash]; ok && now.Before(expires) {
		return hash, false
	}
	if len(entries) >= q.max {
		q.evictOldestLocked(entries)
	}
	entries[hash] = now.Add(q.ttl)
	return hash, true
}

func (q *reasoningQuarantine) pruneLocked(now time.Time) {
	for upstreamID, entries := range q.entries {
		for hash, expires := range entries {
			if !now.Before(expires) {
				delete(entries, hash)
			}
		}
		if len(entries) == 0 {
			delete(q.entries, upstreamID)
		}
	}
}

func (q *reasoningQuarantine) evictOldestLocked(entries map[[32]byte]time.Time) {
	var oldest [32]byte
	var oldestAt time.Time
	first := true
	for hash, expires := range entries {
		if first || expires.Before(oldestAt) {
			oldest, oldestAt, first = hash, expires, false
		}
	}
	if !first {
		delete(entries, oldest)
	}
}

// quarantineMatchingReasoning finds the top-level input item whose
// encrypted_content tail the upstream named in its error, quarantines its full
// blob, and returns the items the retry must drop. If the fingerprint does not
// match any item (for example a nested shape), every top-level encrypted item is
// quarantined so the retry cannot loop on the same failure.
func (h *Handler) quarantineMatchingReasoning(body []byte, upstreamID, prefix, suffix string) []gateway.DroppedReasoning {
	if h == nil || h.reasoningQuarantine == nil || len(body) == 0 {
		return nil
	}
	root, err := wire.Parse(body)
	if err != nil {
		return nil
	}
	input, ok := root.Get("input")
	if !ok || input.Kind != wire.Array {
		return nil
	}
	var matched []gateway.DroppedReasoning
	for i := 0; i < input.Len(); i++ {
		item, ok := input.Index(i)
		if !ok || item.Kind != wire.Object {
			continue
		}
		blob, ok := item.StringField("encrypted_content")
		if !ok || blob == "" {
			continue
		}
		id, _ := item.StringField("id")
		kind, _ := item.StringField("type")
		if strings.HasPrefix(blob, prefix) && strings.HasSuffix(blob, suffix) {
			hash, _ := h.reasoningQuarantine.QuarantineBlob(upstreamID, blob)
			matched = append(matched, gateway.DroppedReasoning{ID: id, Type: kind, Hash: hash})
		}
	}
	if len(matched) == 0 {
		return h.quarantineAllReasoning(input, upstreamID)
	}
	return matched
}

// quarantineAllReasoning is the fallback when the upstream's truncated blob
// fingerprint cannot be matched: quarantine every top-level encrypted item.
func (h *Handler) quarantineAllReasoning(input *wire.Node, upstreamID string) []gateway.DroppedReasoning {
	var dropped []gateway.DroppedReasoning
	for i := 0; i < input.Len(); i++ {
		item, ok := input.Index(i)
		if !ok || item.Kind != wire.Object {
			continue
		}
		blob, ok := item.StringField("encrypted_content")
		if !ok || blob == "" {
			continue
		}
		id, _ := item.StringField("id")
		kind, _ := item.StringField("type")
		hash, _ := h.reasoningQuarantine.QuarantineBlob(upstreamID, blob)
		dropped = append(dropped, gateway.DroppedReasoning{ID: id, Type: kind, Hash: hash})
	}
	return dropped
}

func reasoningQuarantinedEvent(items []gateway.DroppedReasoning, upstreamID string, at time.Time) recorder.RecordEvent {
	ids := make([]string, 0, len(items))
	types := make([]string, 0, len(items))
	hashes := make([]string, 0, len(items))
	for _, item := range items {
		if item.ID != "" {
			ids = append(ids, item.ID)
		}
		if item.Type != "" {
			types = append(types, item.Type)
		}
		hashes = append(hashes, hex.EncodeToString(item.Hash[:8]))
	}
	if len(ids) > 8 {
		ids = ids[:8]
	}
	if len(types) > 8 {
		types = types[:8]
	}
	if len(hashes) > 8 {
		hashes = hashes[:8]
	}
	return recorder.RecordEvent{
		Type: "reasoning.quarantined",
		Time: at,
		Attributes: map[string]interface{}{
			"upstream_id": upstreamID,
			"count":       len(items),
			"item_ids":    ids,
			"item_types":  types,
			"blob_sha256": hashes,
		},
	}
}
