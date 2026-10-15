package proxy

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/pkg/secretmask"
)

// TestSecretMaskEventsCarryNoValuesAndAuditRecordsDo pins the split the whole
// audit design rests on. The prelude event is copied into the trace index, served
// to the monitor and exported; the audit row is the operator-readable record of
// the pair. If a future change folds the pair into the event, the credential
// ends up in the cassette prelude, which is the thing masking exists to avoid.
func TestSecretMaskEventsCarryNoValuesAndAuditRecordsDo(t *testing.T) {
	vault := secretmask.NewVault(nil)
	source := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"key ` + testOpenAIProjectKey + `"}]}`)
	_, events := vault.Mask(source)
	if len(events) != 1 {
		t.Fatalf("Mask() produced %d events, want 1", len(events))
	}
	at := time.Date(2026, 10, 15, 7, 0, 0, 0, time.UTC)

	recorded := secretMaskEvents(events, at)
	if len(recorded) != 1 {
		t.Fatalf("secretMaskEvents() produced %d events, want 1", len(recorded))
	}
	if recorded[0].Type != "secret.masked" {
		t.Errorf("event type = %q, want secret.masked", recorded[0].Type)
	}
	serialized, err := json.Marshal(recorded[0].Attributes)
	if err != nil {
		t.Fatalf("marshal event attributes: %v", err)
	}
	if strings.Contains(string(serialized), testOpenAIProjectKey) {
		t.Errorf("the prelude event carries the credential: %s", serialized)
	}
	original, standIn := events[0].AuditValues()
	if strings.Contains(string(serialized), standIn) {
		t.Errorf("the prelude event carries the stand-in: %s", serialized)
	}

	audit := secretMaskAuditRecords(events)
	if len(audit) != 1 {
		t.Fatalf("secretMaskAuditRecords() produced %d rows, want 1", len(audit))
	}
	row := audit[0]
	if row.Original != testOpenAIProjectKey {
		t.Errorf("audit original = %q, want %q", row.Original, testOpenAIProjectKey)
	}
	if row.Original != original || row.StandIn != standIn {
		t.Errorf("audit pair = (%q, %q), want (%q, %q)", row.Original, row.StandIn, original, standIn)
	}
	if row.StandIn == row.Original {
		t.Error("audit row has no stand-in, so nothing shows what left the machine")
	}
	if len(row.StandIn) != len(row.Original) {
		t.Errorf("stand-in is %d bytes, want the original's %d", len(row.StandIn), len(row.Original))
	}
	if row.Rule != "openai.project_key" || row.Kind != "api_key" || row.Vendor != "OpenAI" {
		t.Errorf("audit rule/kind/vendor = %q/%q/%q, want openai.project_key/api_key/OpenAI", row.Rule, row.Kind, row.Vendor)
	}
	if row.Confidence != "high" || row.Score < 70 {
		t.Errorf("audit confidence/score = %q/%d, want a high-confidence finding", row.Confidence, row.Score)
	}
	if row.Start != events[0].Start || row.End != events[0].End {
		t.Errorf("audit offsets = (%d, %d), want the event's (%d, %d)", row.Start, row.End, events[0].Start, events[0].End)
	}
	if row.TraceID != "" {
		t.Errorf("the request path set a trace id (%q) it cannot know yet", row.TraceID)
	}
}

// TestSecretMaskAuditRecordsOwnsItsBytes pins that the stand-in in an audit row
// survives the buffer being rewritten. Mask overwrites the source in place, so an
// event that aliased it would report the stand-in as the original after a second
// pass over the same slice.
func TestSecretMaskAuditRecordsOwnsItsBytes(t *testing.T) {
	vault := secretmask.NewVault(nil)
	source := []byte(`{"messages":[{"role":"user","content":"key ` + testGoogleAPIKey + `"}]}`)
	_, events := vault.Mask(source)
	if len(events) != 1 {
		t.Fatalf("Mask() produced %d events, want 1", len(events))
	}
	rows := secretMaskAuditRecords(events)
	if len(rows) != 1 {
		t.Fatalf("secretMaskAuditRecords() produced %d rows, want 1", len(rows))
	}
	want := rows[0].Original

	// Re-run the detector over the same buffer, as a retry against a second
	// upstream does.
	vault.Mask(source)
	if rows[0].Original != want {
		t.Errorf("audit original changed to %q after the buffer was rewritten; it aliased the source", rows[0].Original)
	}
}

// TestSecretMaskAuditRecordsEmptyForNoEvents keeps the common case free: an
// exchange with no credential must not allocate a slice the caller has to check.
func TestSecretMaskAuditRecordsEmptyForNoEvents(t *testing.T) {
	if rows := secretMaskAuditRecords(nil); rows != nil {
		t.Errorf("secretMaskAuditRecords(nil) = %v, want nil", rows)
	}
	if events := secretMaskEvents(nil, time.Now()); events != nil {
		t.Errorf("secretMaskEvents(nil) = %v, want nil", events)
	}
}
