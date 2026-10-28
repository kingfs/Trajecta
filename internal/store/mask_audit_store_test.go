package store

import (
	"strings"
	"testing"
	"time"
)

// TestCredentialMaskAuditRoundTrip is the contract an auditor depends on: the
// original and the stand-in come back out of the table exactly as they went in,
// keyed to the trace, and idempotently - a cassette re-index must land on the
// same row rather than duplicating a finding.
func TestCredentialMaskAuditRoundTrip(t *testing.T) {
	st := configTransactionTestStore(t)

	const original = "sk-proj-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAT3BlbkFJBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	standIn := original[:20] + "ZZZZ" + original[24:]
	record := CredentialMaskAuditRecord{
		TraceID:    "trace-round-trip",
		EventIndex: 0,
		Rule:       "openai.project_key",
		Vendor:     "OpenAI",
		Kind:       "api_key",
		Confidence: "high",
		Score:      100,
		Reasons:    "strong_shape,context",
		Start:      76,
		End:        76 + len(original),
		Original:   original,
		StandIn:    standIn,
		DetectedAt: time.Date(2026, 10, 15, 7, 0, 0, 0, time.UTC),
	}
	if err := st.UpsertCredentialMaskAudits([]CredentialMaskAuditRecord{record}); err != nil {
		t.Fatalf("UpsertCredentialMaskAudits() error = %v", err)
	}

	page, err := st.ListCredentialMaskAudits(CredentialMaskAuditFilter{TraceID: "trace-round-trip"})
	if err != nil {
		t.Fatalf("ListCredentialMaskAudits() error = %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("total = %d, items = %d, want 1 each", page.Total, len(page.Items))
	}
	got := page.Items[0]
	if got.Original != original {
		t.Errorf("original = %q, want the value that was masked", got.Original)
	}
	if got.StandIn != standIn {
		t.Errorf("stand_in = %q, want the stand-in that left the machine", got.StandIn)
	}
	if got.ValueLength != len(original) {
		t.Errorf("value_length = %d, want %d", got.ValueLength, len(original))
	}
	if got.Start != 76 || got.End != 76+len(original) {
		t.Errorf("offsets = (%d, %d), want (76, %d)", got.Start, got.End, 76+len(original))
	}
	if got.Rule != "openai.project_key" || got.Vendor != "OpenAI" {
		t.Errorf("rule/vendor = %q/%q, want openai.project_key/OpenAI", got.Rule, got.Vendor)
	}
	if !got.DetectedAt.Equal(record.DetectedAt) {
		t.Errorf("detected_at = %s, want %s", got.DetectedAt, record.DetectedAt)
	}

	// A second index of the same cassette must update in place. The event index is
	// what makes that true; if the upsert appended, a re-indexed trace would show
	// the same credential twice.
	record.Confidence = "medium"
	if err := st.UpsertCredentialMaskAudits([]CredentialMaskAuditRecord{record}); err != nil {
		t.Fatalf("second UpsertCredentialMaskAudits() error = %v", err)
	}
	page, err = st.ListCredentialMaskAudits(CredentialMaskAuditFilter{TraceID: "trace-round-trip"})
	if err != nil {
		t.Fatalf("second ListCredentialMaskAudits() error = %v", err)
	}
	if page.Total != 1 {
		t.Fatalf("total after re-index = %d, want 1", page.Total)
	}
	if page.Items[0].Confidence != "medium" {
		t.Errorf("confidence = %q, want the re-indexed value", page.Items[0].Confidence)
	}
}

// TestCredentialMaskAuditRejectsRecordsWithoutValues pins the sync path's shape:
// the prelude does not carry the pair, so a record built from it has no original
// and must not create a row. Otherwise every background index pass would store
// findings with an empty credential.
func TestCredentialMaskAuditRejectsRecordsWithoutValues(t *testing.T) {
	st := configTransactionTestStore(t)

	if err := st.UpsertCredentialMaskAudits([]CredentialMaskAuditRecord{
		{TraceID: "trace-no-values", EventIndex: 0, Rule: "openai.project_key"},
		{TraceID: "", EventIndex: 0, Rule: "openai.project_key", Original: "x", StandIn: "y"},
	}); err != nil {
		t.Fatalf("UpsertCredentialMaskAudits() error = %v", err)
	}
	page, err := st.ListCredentialMaskAudits(CredentialMaskAuditFilter{})
	if err != nil {
		t.Fatalf("ListCredentialMaskAudits() error = %v", err)
	}
	if page.Total != 0 {
		t.Fatalf("total = %d, want 0 rows without a value and a trace", page.Total)
	}
}

// TestCredentialMaskAuditValuesAreEncryptedAtRest pins that the stored column is
// not the credential. The local secret box is configured by New, so a row read
// straight out of the table must carry the envelope rather than the bytes, while
// the query path still returns the original.
func TestCredentialMaskAuditValuesAreEncryptedAtRest(t *testing.T) {
	st := configTransactionTestStore(t)
	if st.SecretStorageMode() != "encrypted-local" {
		t.Skipf("secret storage mode is %q, not encrypted-local", st.SecretStorageMode())
	}

	const original = "AIzaSyA1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q"
	const standIn = "AIzaSyZ9y8X7w6V5u4T3s2R1q0P9o8N7m6L5k4J"
	if err := st.UpsertCredentialMaskAudits([]CredentialMaskAuditRecord{{
		TraceID:    "trace-encrypted",
		EventIndex: 0,
		Rule:       "google.api_key",
		Original:   original,
		StandIn:    standIn,
	}}); err != nil {
		t.Fatalf("UpsertCredentialMaskAudits() error = %v", err)
	}

	var storedOriginal, storedStandIn string
	if err := st.db.QueryRow(
		`SELECT original_value, stand_in_value FROM credential_mask_audits WHERE trace_id = ?`,
		"trace-encrypted",
	).Scan(&storedOriginal, &storedStandIn); err != nil {
		t.Fatalf("read raw row: %v", err)
	}
	if storedOriginal == original || storedStandIn == standIn {
		t.Fatalf("the raw columns hold plaintext credentials")
	}
	if !strings.HasPrefix(storedOriginal, "tlsec:") {
		t.Errorf("stored original = %q, want the local secret envelope", storedOriginal)
	}

	page, err := st.ListCredentialMaskAudits(CredentialMaskAuditFilter{TraceID: "trace-encrypted"})
	if err != nil {
		t.Fatalf("ListCredentialMaskAudits() error = %v", err)
	}
	if len(page.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(page.Items))
	}
	if page.Items[0].Original != original || page.Items[0].StandIn != standIn {
		t.Errorf("decrypted pair = (%q, %q), want the originals", page.Items[0].Original, page.Items[0].StandIn)
	}
}

// TestCredentialMaskAuditRaisesOneEventPerRule pins the notification shape: the
// feed gets an entry the monitor can render, it does not repeat the credential,
// and a second masking of the same rule updates that entry instead of adding one.
func TestCredentialMaskAuditRaisesOneEventPerRule(t *testing.T) {
	st := configTransactionTestStore(t)

	const original = "sk-proj-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAT3BlbkFJBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"
	records := []CredentialMaskAuditRecord{
		{TraceID: "trace-event-1", EventIndex: 0, Rule: "openai.project_key", Vendor: "OpenAI", Original: original, StandIn: "stand-in-1"},
		{TraceID: "trace-event-1", EventIndex: 1, Rule: "openai.project_key", Vendor: "OpenAI", Original: original, StandIn: "stand-in-2"},
		{TraceID: "trace-event-1", EventIndex: 2, Rule: "google.api_key", Vendor: "Google", Original: "AIzaSyA1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q", StandIn: "stand-in-3"},
	}
	if err := st.UpsertCredentialMaskAudits(records); err != nil {
		t.Fatalf("UpsertCredentialMaskAudits() error = %v", err)
	}

	page, err := st.ListSystemEvents(SystemEventFilter{Category: "credential_masked", PageSize: 50})
	if err != nil {
		t.Fatalf("ListSystemEvents() error = %v", err)
	}
	if page.Total != 2 {
		t.Fatalf("system events = %d, want one per rule", page.Total)
	}
	for _, event := range page.Items {
		if event.Source != "secretmask" || event.Severity != "warning" {
			t.Errorf("event source/severity = %q/%q, want secretmask/warning", event.Source, event.Severity)
		}
		if event.TraceID == "" {
			t.Error("event has no trace id to link the recording from")
		}
		details := string(event.DetailsJSON)
		if strings.Contains(details, original) || strings.Contains(details, "stand-in-1") {
			t.Errorf("event details leaked a credential: %s", details)
		}
		if !strings.Contains(details, `"audit":"credential_mask_audits"`) {
			t.Errorf("event details do not point at the audit rows: %s", details)
		}
	}

	// A later request that masks the same rule updates the existing entry.
	if err := st.UpsertCredentialMaskAudits([]CredentialMaskAuditRecord{
		{TraceID: "trace-event-2", EventIndex: 0, Rule: "openai.project_key", Vendor: "OpenAI", Original: original, StandIn: "stand-in-4"},
	}); err != nil {
		t.Fatalf("second UpsertCredentialMaskAudits() error = %v", err)
	}
	page, err = st.ListSystemEvents(SystemEventFilter{Category: "credential_masked", PageSize: 50})
	if err != nil {
		t.Fatalf("second ListSystemEvents() error = %v", err)
	}
	if page.Total != 2 {
		t.Fatalf("system events after a second trace = %d, want the same two", page.Total)
	}
	for _, event := range page.Items {
		if event.Fingerprint == "secretmask:rule:openai_project_key" && event.OccurrenceCount < 2 {
			t.Errorf("occurrence_count = %d, want the repeat to accumulate", event.OccurrenceCount)
		}
	}
}

// TestCredentialMaskAuditAndEventAreAtomic pins the failure mode that made a
// trace show a masked credential in its audit tab while the event feed stayed
// empty: the rows were committed first and the notification was written after.
// If the event cannot be written, the rows have to roll back with it so the next
// attempt can write both.
func TestCredentialMaskAuditAndEventAreAtomic(t *testing.T) {
	st := configTransactionTestStore(t)
	if _, err := st.db.Exec(`DROP TABLE system_events`); err != nil {
		t.Fatalf("DROP TABLE system_events error = %v", err)
	}
	err := st.UpsertCredentialMaskAudits([]CredentialMaskAuditRecord{{
		TraceID:  "trace-atomic",
		Rule:     "pii.chinese_id",
		Kind:     "national_id",
		Original: "11010519491231002X",
		StandIn:  "110101195001011234",
	}})
	if err == nil {
		t.Fatal("UpsertCredentialMaskAudits() error = nil, want the event failure to surface")
	}
	page, err := st.ListCredentialMaskAudits(CredentialMaskAuditFilter{TraceID: "trace-atomic"})
	if err != nil {
		t.Fatalf("ListCredentialMaskAudits() error = %v", err)
	}
	if page.Total != 0 {
		t.Fatalf("audit rows = %d, want none after the event insert rolled the transaction back", page.Total)
	}
}

// TestReconcileCredentialMaskEventsHealsMissingEvent pins the historical repair
// path. An audit table written before the notification existed - or by a process
// killed between the two writes - has rows but no event; a pass recreates the
// event once, keeps its count at one, and does nothing on the next pass.
func TestReconcileCredentialMaskEventsHealsMissingEvent(t *testing.T) {
	st := configTransactionTestStore(t)
	const original = "11010519491231002X"
	if err := st.UpsertCredentialMaskAudits([]CredentialMaskAuditRecord{
		{TraceID: "trace-heal-1", EventIndex: 0, Rule: "pii.chinese_id", Kind: "national_id", Vendor: "", Confidence: "high", Score: 95, Start: 12, Original: original, StandIn: "110101195001011234"},
		{TraceID: "trace-heal-2", EventIndex: 0, Rule: "pii.chinese_id", Kind: "national_id", Vendor: "", Confidence: "high", Score: 95, Start: 24, Original: original, StandIn: "110101195001011234"},
	}); err != nil {
		t.Fatalf("UpsertCredentialMaskAudits() error = %v", err)
	}
	if _, err := st.db.Exec(`DELETE FROM system_events WHERE source = 'secretmask'`); err != nil {
		t.Fatalf("delete events error = %v", err)
	}

	inserted, err := st.ReconcileCredentialMaskEvents()
	if err != nil {
		t.Fatalf("ReconcileCredentialMaskEvents() error = %v", err)
	}
	if inserted != 1 {
		t.Fatalf("inserted = %d, want one event for the one rule", inserted)
	}
	event, err := st.GetSystemEventByFingerprint("secretmask:rule:pii_chinese_id")
	if err != nil {
		t.Fatalf("GetSystemEventByFingerprint() error = %v", err)
	}
	if event.OccurrenceCount != 1 {
		t.Errorf("occurrence_count = %d, want 1 for a recovered first occurrence", event.OccurrenceCount)
	}
	if event.TraceID != "trace-heal-2" {
		t.Errorf("trace_id = %q, want the latest occurrence trace-heal-2", event.TraceID)
	}
	if !strings.Contains(string(event.DetailsJSON), `"length":18`) {
		t.Errorf("details_json = %s, want the stored value length without the value", event.DetailsJSON)
	}
	if strings.Contains(string(event.DetailsJSON), original) {
		t.Errorf("details_json carries the identity number: %s", event.DetailsJSON)
	}

	inserted, err = st.ReconcileCredentialMaskEvents()
	if err != nil {
		t.Fatalf("second ReconcileCredentialMaskEvents() error = %v", err)
	}
	if inserted != 0 {
		t.Fatalf("second inserted = %d, want 0: reconciliation must be idempotent", inserted)
	}
	event, err = st.GetSystemEventByFingerprint("secretmask:rule:pii_chinese_id")
	if err != nil {
		t.Fatalf("second GetSystemEventByFingerprint() error = %v", err)
	}
	if event.OccurrenceCount != 1 {
		t.Errorf("occurrence_count after a repeat pass = %d, want 1", event.OccurrenceCount)
	}
}
