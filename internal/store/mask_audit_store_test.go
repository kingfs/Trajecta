package store

import (
	"errors"
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

// TestCredentialMaskedEventReopensAfterIgnore pins the visibility rule: an
// ignored credential notification is only an acknowledgement of the occurrence
// the reader handled. A later credential that leaves the machine is new
// evidence, so the same row returns to unread and drops the read/resolved state
// instead of staying muted forever.
func TestCredentialMaskedEventReopensAfterIgnore(t *testing.T) {
	st := configTransactionTestStore(t)
	event, err := st.UpsertSystemEvent(SystemEvent{
		Fingerprint: "secretmask:rule:generic_sk_dash",
		Source:      "secretmask",
		Category:    "credential_masked",
		Severity:    "warning",
		Title:       "Sensitive credential replaced before leaving the machine",
		Message:     "rule generic.sk_dash replaced a credential",
		TraceID:     "trace-first",
	})
	if err != nil {
		t.Fatalf("UpsertSystemEvent() error = %v", err)
	}
	if err := st.MarkSystemEventRead(event.ID); err != nil {
		t.Fatalf("MarkSystemEventRead() error = %v", err)
	}
	if err := st.ResolveSystemEvent(event.ID); err != nil {
		t.Fatalf("ResolveSystemEvent() error = %v", err)
	}
	if err := st.IgnoreSystemEvent(event.ID); err != nil {
		t.Fatalf("IgnoreSystemEvent() error = %v", err)
	}

	if err := st.UpsertCredentialMaskAudits([]CredentialMaskAuditRecord{{
		TraceID:    "trace-second",
		Rule:       "generic.sk_dash",
		Kind:       "generic_key",
		Original:   "sk-second-credential",
		StandIn:    "sk-second-standin",
		Start:      10,
		DetectedAt: time.Date(2026, 10, 28, 7, 30, 0, 0, time.UTC),
	}}); err != nil {
		t.Fatalf("UpsertCredentialMaskAudits() error = %v", err)
	}

	got, err := st.GetSystemEventByFingerprint("secretmask:rule:generic_sk_dash")
	if err != nil {
		t.Fatalf("GetSystemEventByFingerprint() error = %v", err)
	}
	if got.Status != SystemEventStatusUnread {
		t.Errorf("status = %q, want unread after a new credential", got.Status)
	}
	if got.OccurrenceCount != 2 {
		t.Errorf("occurrence_count = %d, want 2", got.OccurrenceCount)
	}
	if !got.ReadAt.IsZero() || !got.ResolvedAt.IsZero() {
		t.Errorf("read_at/resolved_at = %v/%v, want both cleared by the new occurrence", got.ReadAt, got.ResolvedAt)
	}
	if got.TraceID != "trace-second" {
		t.Errorf("trace_id = %q, want the latest occurrence", got.TraceID)
	}
}

// TestOperationalEventStaysIgnoredAfterRepeat keeps the new reopening rule
// narrow: an ignored parse failure is noise the reader chose to mute, so a
// repeat must not put it back in the inbox.
func TestOperationalEventStaysIgnoredAfterRepeat(t *testing.T) {
	st := configTransactionTestStore(t)
	event, err := st.UpsertSystemEvent(SystemEvent{
		Fingerprint: "parser:trace-repeat:bad-json",
		Source:      "parser",
		Category:    "parse_failure",
		Severity:    "error",
		Title:       "Observation parse job failed",
		Message:     "bad json",
		TraceID:     "trace-repeat-1",
	})
	if err != nil {
		t.Fatalf("UpsertSystemEvent() error = %v", err)
	}
	if err := st.IgnoreSystemEvent(event.ID); err != nil {
		t.Fatalf("IgnoreSystemEvent() error = %v", err)
	}
	if _, err := st.UpsertSystemEvent(SystemEvent{
		Fingerprint: "parser:trace-repeat:bad-json",
		Source:      "parser",
		Category:    "parse_failure",
		Severity:    "error",
		Title:       "Observation parse job failed",
		Message:     "bad json again",
		TraceID:     "trace-repeat-2",
	}); err != nil {
		t.Fatalf("second UpsertSystemEvent() error = %v", err)
	}
	got, err := st.GetSystemEventByFingerprint("parser:trace-repeat:bad-json")
	if err != nil {
		t.Fatalf("GetSystemEventByFingerprint() error = %v", err)
	}
	if got.Status != SystemEventStatusIgnored {
		t.Errorf("status = %q, want the operational event to stay ignored", got.Status)
	}
	if got.OccurrenceCount != 2 {
		t.Errorf("occurrence_count = %d, want 2", got.OccurrenceCount)
	}
}

// TestCredentialMaskTraceMatchesResolvesPrefix is the read that replaces a
// per-trace event row: the audit table already names the trace and the rule, so
// a trace-id search can find the rule notification without storing the id twice.
func TestCredentialMaskTraceMatchesResolvesPrefix(t *testing.T) {
	st := configTransactionTestStore(t)
	const (
		firstTrace  = "f1592da8-2847-4eb5-ac67-10596c0836c7"
		secondTrace = "a8198d9e-55fc-4e20-98dd-ddf43306bd08"
	)
	if err := st.UpsertCredentialMaskAudits([]CredentialMaskAuditRecord{
		{TraceID: firstTrace, Rule: "generic.sk_dash", Original: "sk-first", StandIn: "sk-first-fake"},
		{TraceID: secondTrace, Rule: "openai.project_key", Original: "sk-second", StandIn: "sk-second-fake"},
	}); err != nil {
		t.Fatalf("UpsertCredentialMaskAudits() error = %v", err)
	}

	matches, err := st.CredentialMaskTraceMatches(firstTrace[:8])
	if err != nil {
		t.Fatalf("CredentialMaskTraceMatches(prefix) error = %v", err)
	}
	if got := matches["secretmask:rule:generic_sk_dash"]; got != firstTrace {
		t.Errorf("prefix match = %q, want %q", got, firstTrace)
	}
	if _, ok := matches["secretmask:rule:openai_project_key"]; ok {
		t.Error("prefix match returned a rule from another trace")
	}

	matches, err = st.CredentialMaskTraceMatches(firstTrace)
	if err != nil {
		t.Fatalf("CredentialMaskTraceMatches(exact) error = %v", err)
	}
	if got := matches["secretmask:rule:generic_sk_dash"]; got != firstTrace {
		t.Errorf("exact match = %q, want %q", got, firstTrace)
	}

	for _, query := range []string{"generic", "f1592"} {
		matches, err = st.CredentialMaskTraceMatches(query)
		if err != nil {
			t.Fatalf("CredentialMaskTraceMatches(%q) error = %v", query, err)
		}
		if len(matches) != 0 {
			t.Errorf("CredentialMaskTraceMatches(%q) = %v, want no trace scan", query, matches)
		}
	}
}

// TestReopenIgnoredCredentialMaskEvents pins the one-time repair: it reopens
// only credential notifications and leaves an ignored operational event alone.
func TestReopenIgnoredCredentialMaskEvents(t *testing.T) {
	st := configTransactionTestStore(t)
	credentialEvent, err := st.UpsertSystemEvent(SystemEvent{
		Fingerprint: "secretmask:rule:pii_chinese_id",
		Source:      "secretmask",
		Category:    "credential_masked",
		Severity:    "warning",
		Title:       "Sensitive credential replaced before leaving the machine",
	})
	if err != nil {
		t.Fatalf("UpsertSystemEvent(credential) error = %v", err)
	}
	operationalEvent, err := st.UpsertSystemEvent(SystemEvent{
		Fingerprint: "parser:trace-ignored:bad-json",
		Source:      "parser",
		Category:    "parse_failure",
		Severity:    "error",
		Title:       "Observation parse job failed",
	})
	if err != nil {
		t.Fatalf("UpsertSystemEvent(operational) error = %v", err)
	}
	for _, id := range []string{credentialEvent.ID, operationalEvent.ID} {
		if err := st.IgnoreSystemEvent(id); err != nil {
			t.Fatalf("IgnoreSystemEvent(%s) error = %v", id, err)
		}
	}

	reopened, err := st.ReopenIgnoredCredentialMaskEvents()
	if err != nil {
		t.Fatalf("ReopenIgnoredCredentialMaskEvents() error = %v", err)
	}
	if reopened != 1 {
		t.Fatalf("reopened = %d, want only the credential event", reopened)
	}
	got, err := st.GetSystemEvent(credentialEvent.ID)
	if err != nil {
		t.Fatalf("GetSystemEvent(credential) error = %v", err)
	}
	if got.Status != SystemEventStatusUnread {
		t.Errorf("credential status = %q, want unread", got.Status)
	}
	got, err = st.GetSystemEvent(operationalEvent.ID)
	if err != nil {
		t.Fatalf("GetSystemEvent(operational) error = %v", err)
	}
	if got.Status != SystemEventStatusIgnored {
		t.Errorf("operational status = %q, want ignored", got.Status)
	}

	reopened, err = st.ReopenIgnoredCredentialMaskEvents()
	if err != nil {
		t.Fatalf("second ReopenIgnoredCredentialMaskEvents() error = %v", err)
	}
	if reopened != 0 {
		t.Errorf("second reopened = %d, want 0", reopened)
	}
}

// TestCredentialMaskEventStatusIsScopedToTrace is the contract the UI depends
// on: acknowledging one trace's occurrence must not change the same rule's
// occurrence in another trace or the rule-level notification's newest pointer.
func TestCredentialMaskEventStatusIsScopedToTrace(t *testing.T) {
	st := configTransactionTestStore(t)
	const (
		rule   = "generic.sk_dash"
		traceA = "11111111-1111-4111-8111-111111111111"
		traceB = "22222222-2222-4222-8222-222222222222"
	)
	if err := st.UpsertCredentialMaskAudits([]CredentialMaskAuditRecord{
		{TraceID: traceA, Rule: rule, Original: "sk-a", StandIn: "sk-a-fake", DetectedAt: time.Date(2026, 10, 28, 7, 0, 0, 0, time.UTC)},
	}); err != nil {
		t.Fatalf("UpsertCredentialMaskAudits(A) error = %v", err)
	}
	if err := st.UpsertCredentialMaskAudits([]CredentialMaskAuditRecord{
		{TraceID: traceB, Rule: rule, Original: "sk-b", StandIn: "sk-b-fake", DetectedAt: time.Date(2026, 10, 28, 7, 1, 0, 0, time.UTC)},
	}); err != nil {
		t.Fatalf("UpsertCredentialMaskAudits(B) error = %v", err)
	}
	event, err := st.GetSystemEventByFingerprint("secretmask:rule:generic_sk_dash")
	if err != nil {
		t.Fatalf("GetSystemEventByFingerprint() error = %v", err)
	}
	if event.TraceID != traceB {
		t.Fatalf("event trace_id = %q, want newest %q", event.TraceID, traceB)
	}

	if _, err := st.SetCredentialMaskEventStatus(traceA, rule, SystemEventStatusIgnored); err != nil {
		t.Fatalf("SetCredentialMaskEventStatus(A, ignored) error = %v", err)
	}
	occurrenceA, found, err := st.CredentialMaskEventOccurrence(traceA, rule)
	if err != nil || !found {
		t.Fatalf("CredentialMaskEventOccurrence(A) = %+v, %v", occurrenceA, err)
	}
	if occurrenceA.Status != SystemEventStatusIgnored {
		t.Errorf("trace A status = %q, want ignored", occurrenceA.Status)
	}
	occurrenceB, found, err := st.CredentialMaskEventOccurrence(traceB, rule)
	if err != nil || !found {
		t.Fatalf("CredentialMaskEventOccurrence(B) = %+v, %v", occurrenceB, err)
	}
	if occurrenceB.Status != SystemEventStatusUnread {
		t.Errorf("trace B status = %q, want unread; trace A's action leaked", occurrenceB.Status)
	}
	event, err = st.GetSystemEventByFingerprint("secretmask:rule:generic_sk_dash")
	if err != nil {
		t.Fatalf("second GetSystemEventByFingerprint() error = %v", err)
	}
	if event.Status != SystemEventStatusUnread {
		t.Errorf("newest event status = %q, want unread after a historical occurrence was ignored", event.Status)
	}

	if _, err := st.SetCredentialMaskEventStatus(traceB, rule, SystemEventStatusIgnored); err != nil {
		t.Fatalf("SetCredentialMaskEventStatus(B, ignored) error = %v", err)
	}
	event, err = st.GetSystemEventByFingerprint("secretmask:rule:generic_sk_dash")
	if err != nil {
		t.Fatalf("third GetSystemEventByFingerprint() error = %v", err)
	}
	if event.Status != SystemEventStatusIgnored {
		t.Errorf("newest event status = %q, want ignored after the newest occurrence was ignored", event.Status)
	}

	if _, err := st.SetCredentialMaskEventStatus(traceA, rule, SystemEventStatusRead); err != nil {
		t.Fatalf("SetCredentialMaskEventStatus(A, read) error = %v", err)
	}
	event, err = st.GetSystemEventByFingerprint("secretmask:rule:generic_sk_dash")
	if err != nil {
		t.Fatalf("fourth GetSystemEventByFingerprint() error = %v", err)
	}
	if event.Status != SystemEventStatusIgnored {
		t.Errorf("newest event status = %q, want the newest occurrence's ignored state", event.Status)
	}
}

// TestCredentialMaskEventUpsertPreservesOccurrenceState pins the re-index path:
// a trace whose occurrence was already acknowledged keeps that state when its
// audit row is written again.
func TestCredentialMaskEventUpsertPreservesOccurrenceState(t *testing.T) {
	st := configTransactionTestStore(t)
	const (
		rule  = "openai.project_key"
		trace = "33333333-3333-4333-8333-333333333333"
	)
	record := CredentialMaskAuditRecord{
		TraceID: trace, Rule: rule, Original: "sk-reindex", StandIn: "sk-reindex-fake",
		DetectedAt: time.Date(2026, 10, 28, 8, 0, 0, 0, time.UTC),
	}
	if err := st.UpsertCredentialMaskAudits([]CredentialMaskAuditRecord{record}); err != nil {
		t.Fatalf("UpsertCredentialMaskAudits() error = %v", err)
	}
	if _, err := st.SetCredentialMaskEventStatus(trace, rule, SystemEventStatusResolved); err != nil {
		t.Fatalf("SetCredentialMaskEventStatus() error = %v", err)
	}
	if err := st.UpsertCredentialMaskAudits([]CredentialMaskAuditRecord{record}); err != nil {
		t.Fatalf("re-index UpsertCredentialMaskAudits() error = %v", err)
	}
	event, err := st.GetSystemEventByFingerprint("secretmask:rule:openai_project_key")
	if err != nil {
		t.Fatalf("GetSystemEventByFingerprint() error = %v", err)
	}
	if event.Status != SystemEventStatusResolved {
		t.Errorf("event status = %q, want the occurrence's resolved state after re-index", event.Status)
	}
	occurrence, found, err := st.CredentialMaskEventOccurrence(trace, rule)
	if err != nil || !found {
		t.Fatalf("CredentialMaskEventOccurrence() = %+v, %v", occurrence, err)
	}
	if occurrence.Status != SystemEventStatusResolved {
		t.Errorf("occurrence status = %q, want resolved", occurrence.Status)
	}
}

// TestCredentialMaskEventStatusRejectsUnknownOccurrence prevents an event id
// from acknowledging a trace that never carried its rule.
func TestCredentialMaskEventStatusRejectsUnknownOccurrence(t *testing.T) {
	st := configTransactionTestStore(t)
	_, err := st.SetCredentialMaskEventStatus("44444444-4444-4444-8444-444444444444", "generic.sk_dash", SystemEventStatusIgnored)
	if !errors.Is(err, ErrCredentialMaskOccurrenceNotFound) {
		t.Fatalf("SetCredentialMaskEventStatus() error = %v, want ErrCredentialMaskOccurrenceNotFound", err)
	}
}

// TestBackfillCredentialMaskEventStates pins the upgrade path from a rule-level
// status to the newest occurrence's state. Unread stays absent, an existing
// occurrence state wins, and ignored is seeded only when the visibility repair
// did not reopen it first.
func TestBackfillCredentialMaskEventStates(t *testing.T) {
	st := configTransactionTestStore(t)
	const (
		rule   = "generic.sk_dash"
		traceA = "55555555-5555-4555-8555-555555555555"
		traceB = "66666666-6666-4666-8666-666666666666"
	)
	if err := st.UpsertCredentialMaskAudits([]CredentialMaskAuditRecord{
		{TraceID: traceA, Rule: rule, Original: "sk-a", StandIn: "sk-a-fake", DetectedAt: time.Date(2026, 10, 28, 7, 0, 0, 0, time.UTC)},
	}); err != nil {
		t.Fatalf("UpsertCredentialMaskAudits(A) error = %v", err)
	}
	if err := st.UpsertCredentialMaskAudits([]CredentialMaskAuditRecord{
		{TraceID: traceB, Rule: rule, Original: "sk-b", StandIn: "sk-b-fake", DetectedAt: time.Date(2026, 10, 28, 7, 1, 0, 0, time.UTC)},
	}); err != nil {
		t.Fatalf("UpsertCredentialMaskAudits(B) error = %v", err)
	}
	// Simulate the old rule-level read status, and seed a conflicting state for
	// the newest occurrence that must win.
	if _, err := st.db.Exec(`UPDATE system_events SET status = 'read', read_at = ?, updated_at = ? WHERE fingerprint = 'secretmask:rule:generic_sk_dash'`,
		time.Date(2026, 10, 28, 7, 2, 0, 0, time.UTC), time.Date(2026, 10, 28, 7, 2, 0, 0, time.UTC)); err != nil {
		t.Fatalf("seed rule status error = %v", err)
	}
	if _, err := st.SetCredentialMaskEventStatus(traceB, rule, SystemEventStatusResolved); err != nil {
		t.Fatalf("SetCredentialMaskEventStatus(B) error = %v", err)
	}

	inserted, err := st.BackfillCredentialMaskEventStates()
	if err != nil {
		t.Fatalf("BackfillCredentialMaskEventStates() error = %v", err)
	}
	if inserted != 0 {
		t.Fatalf("inserted = %d, want 0 because the newest occurrence already has a state", inserted)
	}
	occurrenceA, found, err := st.CredentialMaskEventOccurrence(traceA, rule)
	if err != nil || !found {
		t.Fatalf("CredentialMaskEventOccurrence(A) = %+v, %v", occurrenceA, err)
	}
	if occurrenceA.Status != SystemEventStatusUnread {
		t.Errorf("occurrence A status = %q, want unread: only the newest occurrence inherits the old status", occurrenceA.Status)
	}
	occurrenceB, found, err := st.CredentialMaskEventOccurrence(traceB, rule)
	if err != nil || !found {
		t.Fatalf("CredentialMaskEventOccurrence(B) = %+v, %v", occurrenceB, err)
	}
	if occurrenceB.Status != SystemEventStatusResolved {
		t.Errorf("occurrence B status = %q, want the existing resolved state", occurrenceB.Status)
	}
	event, err := st.GetSystemEventByFingerprint("secretmask:rule:generic_sk_dash")
	if err != nil {
		t.Fatalf("GetSystemEventByFingerprint() error = %v", err)
	}
	if event.Status != SystemEventStatusResolved {
		t.Errorf("event status = %q, want resolved from the newest occurrence", event.Status)
	}
}

func TestBackfillCredentialMaskEventStatesSeedsReadStatus(t *testing.T) {
	st := configTransactionTestStore(t)
	const trace = "77777777-7777-4777-8777-777777777777"
	if err := st.UpsertCredentialMaskAudits([]CredentialMaskAuditRecord{
		{TraceID: trace, Rule: "openai.project_key", Original: "sk-read", StandIn: "sk-read-fake", DetectedAt: time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)},
	}); err != nil {
		t.Fatalf("UpsertCredentialMaskAudits() error = %v", err)
	}
	readAt := time.Date(2026, 10, 28, 9, 1, 0, 0, time.UTC)
	if _, err := st.db.Exec(`UPDATE system_events SET status = 'read', read_at = ?, updated_at = ? WHERE fingerprint = 'secretmask:rule:openai_project_key'`, readAt, readAt); err != nil {
		t.Fatalf("seed rule status error = %v", err)
	}
	inserted, err := st.BackfillCredentialMaskEventStates()
	if err != nil {
		t.Fatalf("BackfillCredentialMaskEventStates() error = %v", err)
	}
	if inserted != 1 {
		t.Fatalf("inserted = %d, want 1", inserted)
	}
	occurrence, found, err := st.CredentialMaskEventOccurrence(trace, "openai.project_key")
	if err != nil || !found {
		t.Fatalf("CredentialMaskEventOccurrence() = %+v, %v", occurrence, err)
	}
	if occurrence.Status != SystemEventStatusRead {
		t.Errorf("occurrence status = %q, want read", occurrence.Status)
	}
	if !occurrence.ReadAt.Equal(readAt) {
		t.Errorf("read_at = %s, want %s", occurrence.ReadAt, readAt)
	}
}
