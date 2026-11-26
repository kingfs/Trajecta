package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"testing"
)

type fakeReasoningQuarantine struct {
	empty  bool
	hashes map[[32]byte]bool
}

func (f *fakeReasoningQuarantine) Empty() bool { return f.empty }

func (f *fakeReasoningQuarantine) Quarantined(_ string, hash [32]byte) bool {
	return f.hashes[hash]
}

func TestDropQuarantinedReasoningRemovesOnlyTheMatchingItem(t *testing.T) {
	bad := "AAAA-QUARANTINE-BLOB-ZZZZ"
	good := "GOOD-REASONING-BLOB"
	body := []byte(`{"model":"gpt-6.1-sol","input":[` +
		`{"type":"reasoning","id":"rs_bad","encrypted_content":"` + bad + `","summary":[]},` +
		`{"type":"message","role":"user","content":"hi"},` +
		`{"type":"reasoning","id":"rs_good","encrypted_content":"` + good + `","summary":[]}]}`)
	quarantine := &fakeReasoningQuarantine{hashes: map[[32]byte]bool{sha256.Sum256([]byte(bad)): true}}
	dropped, rewritten, ok := dropQuarantinedReasoning(body, "up", quarantine)
	if !ok || len(dropped) != 1 || dropped[0].ID != "rs_bad" {
		t.Fatalf("dropped = %+v, ok = %v", dropped, ok)
	}
	if !json.Valid(rewritten) {
		t.Fatalf("rewritten body is not valid JSON: %s", rewritten)
	}
	if bytes.Contains(rewritten, []byte("rs_bad")) || bytes.Contains(rewritten, []byte(bad)) {
		t.Fatalf("quarantined item survived: %s", rewritten)
	}
	if !bytes.Contains(rewritten, []byte("rs_good")) || !bytes.Contains(rewritten, []byte(`"content":"hi"`)) {
		t.Fatalf("non-quarantined items were lost: %s", rewritten)
	}
}

func TestDropQuarantinedReasoningIsInertWithoutEntries(t *testing.T) {
	body := []byte(`{"model":"m","input":[{"type":"reasoning","id":"rs","encrypted_content":"blob"}]}`)
	quarantine := &fakeReasoningQuarantine{empty: true}
	dropped, rewritten, ok := dropQuarantinedReasoning(body, "up", quarantine)
	if ok || dropped != nil || !bytes.Equal(rewritten, body) {
		t.Fatalf("empty quarantine changed the body: dropped=%+v ok=%v", dropped, ok)
	}
}

func TestRequestTransformKeepsRecordBodyWhenDroppingQuarantinedReasoning(t *testing.T) {
	bad := "AAAA-QUARANTINE-BLOB-ZZZZ"
	body := []byte(`{"model":"m","input":[{"type":"reasoning","id":"rs_bad","encrypted_content":"` + bad + `"},{"type":"message","role":"user","content":"hi"}]}`)
	quarantine := &fakeReasoningQuarantine{hashes: map[[32]byte]bool{sha256.Sum256([]byte(bad)): true}}
	transform := RequestTransform{
		RequestedModel:  "m",
		UpstreamModel:   "m",
		RequestProtocol: "responses",
		UpstreamID:      "up",
		Quarantine:      quarantine,
	}
	result, err := transform.Apply(body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(result.RecordBody, body) {
		t.Fatalf("RecordBody changed:\n got %s\nwant %s", result.RecordBody, body)
	}
	if bytes.Contains(result.WireBody, []byte("rs_bad")) {
		t.Fatalf("WireBody still contains the quarantined item: %s", result.WireBody)
	}
	if len(result.DroppedReasoning) != 1 || result.DroppedReasoning[0].ID != "rs_bad" {
		t.Fatalf("DroppedReasoning = %+v", result.DroppedReasoning)
	}
}

func TestRequestTransformDropsOnlyForResponsesProtocol(t *testing.T) {
	bad := "AAAA-QUARANTINE-BLOB-ZZZZ"
	body := []byte(`{"model":"m","input":[{"type":"reasoning","id":"rs_bad","encrypted_content":"` + bad + `"}]}`)
	quarantine := &fakeReasoningQuarantine{hashes: map[[32]byte]bool{sha256.Sum256([]byte(bad)): true}}
	transform := RequestTransform{
		RequestedModel:  "m",
		UpstreamModel:   "m",
		RequestProtocol: "chat_completions",
		UpstreamID:      "up",
		Quarantine:      quarantine,
	}
	result, err := transform.Apply(body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(result.WireBody, body) {
		t.Fatalf("non-Responses body changed: %s", result.WireBody)
	}
}
