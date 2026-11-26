package proxy

import (
	"crypto/sha256"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/gateway"
)

func TestReasoningQuarantineTracksBlobPerUpstreamAndExpires(t *testing.T) {
	now := time.Unix(1000, 0)
	q := newReasoningQuarantine()
	q.now = func() time.Time { return now }
	q.ttl = time.Minute
	q.max = 8

	hash, added := q.QuarantineBlob("up-a", "blob")
	if !added || !q.Quarantined("up-a", hash) {
		t.Fatal("quarantine did not record the blob")
	}
	if q.Quarantined("up-b", hash) {
		t.Fatal("a blob quarantined for one upstream leaked to another")
	}
	if q.Empty() {
		t.Fatal("quarantine with a live entry reported empty")
	}
	if _, again := q.QuarantineBlob("up-a", "blob"); again {
		t.Fatal("re-quarantining the same blob reported a new entry")
	}

	now = now.Add(2 * time.Minute)
	if q.Quarantined("up-a", hash) {
		t.Fatal("expired quarantine entry still matched")
	}
	if !q.Empty() {
		t.Fatal("quarantine with only expired entries did not report empty")
	}
}

func TestReasoningQuarantineEvictsAtItsBound(t *testing.T) {
	q := newReasoningQuarantine()
	q.max = 2
	q.ttl = time.Hour
	q.QuarantineBlob("up", "one")
	q.QuarantineBlob("up", "two")
	q.QuarantineBlob("up", "three")
	q.mu.Lock()
	got := len(q.entries["up"])
	q.mu.Unlock()
	if got > 2 {
		t.Fatalf("quarantine holds %d entries, want <= 2", got)
	}
}

func TestQuarantineMatchingReasoningFindsTheBlobFingerprint(t *testing.T) {
	h := &Handler{reasoningQuarantine: newReasoningQuarantine()}
	body := []byte(`{"model":"m","input":[` +
		`{"type":"reasoning","id":"rs_bad","encrypted_content":"AAAA-QUARANTINE-BLOB-ZZZZ"},` +
		`{"type":"reasoning","id":"rs_other","encrypted_content":"OTHER-BLOB"},` +
		`{"type":"message","role":"user","content":"hi"}]}`)
	dropped := h.quarantineMatchingReasoning(body, "up", "AAAA", "ZZZZ")
	if len(dropped) != 1 || dropped[0].ID != "rs_bad" {
		t.Fatalf("dropped = %+v, want only rs_bad", dropped)
	}
	if !h.reasoningQuarantine.Quarantined("up", sha256.Sum256([]byte("AAAA-QUARANTINE-BLOB-ZZZZ"))) {
		t.Fatal("matching blob was not quarantined")
	}
	if h.reasoningQuarantine.Quarantined("up", sha256.Sum256([]byte("OTHER-BLOB"))) {
		t.Fatal("non-matching blob was quarantined")
	}
}

func TestQuarantineMatchingReasoningFallsBackToAllEncryptedItems(t *testing.T) {
	h := &Handler{reasoningQuarantine: newReasoningQuarantine()}
	body := []byte(`{"model":"m","input":[` +
		`{"type":"reasoning","id":"rs_1","encrypted_content":"one"},` +
		`{"type":"compaction","id":"cmp_1","encrypted_content":"two"}]}`)
	dropped := h.quarantineMatchingReasoning(body, "up", "NOPE", "NOPE")
	if len(dropped) != 2 {
		t.Fatalf("fallback dropped = %+v, want both encrypted items", dropped)
	}
}

func TestReasoningQuarantinedEventIsBounded(t *testing.T) {
	ev := reasoningQuarantinedEvent([]gateway.DroppedReasoning{
		{ID: "rs_1", Type: "reasoning", Hash: sha256.Sum256([]byte("one"))},
	}, "up", time.Unix(0, 0))
	if ev.Type != "reasoning.quarantined" || ev.Attributes["count"] != 1 || ev.Attributes["upstream_id"] != "up" {
		t.Fatalf("event = %+v", ev)
	}
}
