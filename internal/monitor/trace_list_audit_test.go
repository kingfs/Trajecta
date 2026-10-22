package monitor

import (
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/observe"
	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// The list row's audit tag aggregates two tables that are written by different
// paths: the masking audit (values the proxy rewrote before they left) and the
// analysis findings (detectors reading the stored content). This pins that both
// land in one tally and that only PII/credential events make it sensitive.
func TestAttachTraceAuditCountsMaskEventsAndFindings(t *testing.T) {
	t.Parallel()

	outputDir := t.TempDir()
	writeTraceFixture(t, outputDir, "pii.http", buildRecordFixtureWithStatusHeadersAndMutator(
		t, "/v1/chat/completions", false, "200 OK", nil,
		`{"model":"m","messages":[{"role":"user","content":"我的身份证是 110101199003078515"}]}`,
		`{"choices":[]}`,
		func(header *recordfile.RecordHeader) {
			header.Meta.TraceID = "trace-pii"
			header.Meta.RequestID = "req-pii"
		},
	))
	writeTraceFixture(t, outputDir, "refusal.http", buildRecordFixtureWithStatusHeadersAndMutator(
		t, "/v1/chat/completions", false, "200 OK", nil,
		`{"model":"m","messages":[{"role":"user","content":"hello"}]}`,
		`{"choices":[]}`,
		func(header *recordfile.RecordHeader) {
			header.Meta.TraceID = "trace-refusal"
			header.Meta.RequestID = "req-refusal"
		},
	))

	st, err := store.New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()
	syncStore(t, st)

	items := traceListItemsFromEntries(mustListRecent(t, st, 10))
	piiID := traceIDForFile(t, items, "pii.http")
	refusalID := traceIDForFile(t, items, "refusal.http")

	if err := st.UpsertCredentialMaskAudits([]store.CredentialMaskAuditRecord{
		{TraceID: piiID, EventIndex: 0, Rule: "pii.chinese_id", Kind: "national_id", Original: "110101199003078515", StandIn: "110101199003070021", DetectedAt: time.Now().UTC()},
		{TraceID: piiID, EventIndex: 1, Rule: "openai.project_key", Kind: "api_key", Original: "sk-proj-abcdefghijklmnopqrstuvwxyz", StandIn: "sk-proj-abcdefghijklmnopqrstuvwxzz", DetectedAt: time.Now().UTC()},
	}); err != nil {
		t.Fatalf("UpsertCredentialMaskAudits() error = %v", err)
	}
	if err := st.SaveFindings(piiID, []observe.Finding{
		{ID: "finding-pii", TraceID: piiID, Category: "pii_leak", Severity: observe.SeverityHigh, Confidence: 0.95, Title: "Personal data: national_id"},
	}); err != nil {
		t.Fatalf("SaveFindings(pii) error = %v", err)
	}
	if err := st.SaveFindings(refusalID, []observe.Finding{
		{ID: "finding-refusal", TraceID: refusalID, Category: "model_refusal", Severity: observe.SeverityLow, Confidence: 0.95, Title: "Model refusal"},
	}); err != nil {
		t.Fatalf("SaveFindings(refusal) error = %v", err)
	}

	if err := attachTraceAudit(st, items); err != nil {
		t.Fatalf("attachTraceAudit() error = %v", err)
	}

	byID := make(map[string]*traceAuditView, len(items))
	for _, item := range items {
		byID[item.ID] = item.Audit
	}

	pii := byID[piiID]
	if pii == nil {
		t.Fatalf("trace-pii has no audit view: %#v", items)
	}
	if pii.Total != 3 || pii.Masked != 2 || pii.Findings != 1 {
		t.Fatalf("trace-pii total/masked/findings = %d/%d/%d, want 3/2/1", pii.Total, pii.Masked, pii.Findings)
	}
	if pii.PII != 2 || pii.Credential != 1 || pii.Sensitive != 3 || pii.Other != 0 {
		t.Fatalf("trace-pii breakdown = %+v, want pii=2 credential=1 sensitive=3 other=0", pii)
	}

	refusal := byID[refusalID]
	if refusal == nil {
		t.Fatalf("trace-refusal has no audit view")
	}
	if refusal.Total != 1 || refusal.Sensitive != 0 || refusal.Other != 1 {
		t.Fatalf("trace-refusal = %+v, want total=1 sensitive=0 other=1", refusal)
	}
}

// A row without any audit event must carry no tag at all: the UI hides the tag
// on an absent view, so an empty struct would be a bug.
func TestAttachTraceAuditLeavesCleanTracesNil(t *testing.T) {
	t.Parallel()

	outputDir := t.TempDir()
	writeTraceFixture(t, outputDir, "clean.http", buildRecordFixtureWithStatusHeadersAndMutator(
		t, "/v1/chat/completions", false, "200 OK", nil,
		`{"model":"m","messages":[{"role":"user","content":"hello"}]}`,
		`{"choices":[]}`,
		func(header *recordfile.RecordHeader) {
			header.Meta.TraceID = "trace-clean"
			header.Meta.RequestID = "req-clean"
		},
	))
	st, err := store.New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()
	syncStore(t, st)
	items := traceListItemsFromEntries(mustListRecent(t, st, 10))
	if err := attachTraceAudit(st, items); err != nil {
		t.Fatalf("attachTraceAudit() error = %v", err)
	}
	for _, item := range items {
		if item.Audit != nil {
			t.Fatalf("clean trace %s got audit %+v", item.ID, item.Audit)
		}
	}
}

// The preview reads the request section of the cassette only and shows the first
// user turn. The system prompt is skipped because every trace of one agent has
// the same one, and a long turn is cut at a rune boundary with an ellipsis.
func TestAttachTracePreviewsShowsFirstUserTurn(t *testing.T) {
	t.Parallel()

	outputDir := t.TempDir()
	longUser := strings.Repeat("用", tracePreviewRunes+10)
	reqBody := `{"model":"m","messages":[{"role":"system","content":"you are a helpful assistant"},{"role":"user","content":"` + longUser + `"}]}`
	writeTraceFixture(t, outputDir, "preview.http", buildRecordFixtureWithStatusHeadersAndMutator(
		t, "/v1/chat/completions", false, "200 OK", nil, reqBody, `{"choices":[]}`,
		func(header *recordfile.RecordHeader) {
			header.Meta.TraceID = "trace-preview"
			header.Meta.RequestID = "req-preview"
		},
	))

	st, err := store.New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()
	syncStore(t, st)
	items := traceListItemsFromEntries(mustListRecent(t, st, 10))
	attachTracePreviews(items)
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}
	want := string([]rune(longUser)[:tracePreviewRunes]) + "…"
	if items[0].RequestPreview != want {
		t.Fatalf("preview = %q, want %q", items[0].RequestPreview, want)
	}
	if strings.Contains(items[0].RequestPreview, "helpful assistant") {
		t.Fatalf("preview included the system prompt: %q", items[0].RequestPreview)
	}
}

func mustListRecent(t *testing.T, st *store.Store, limit int) []store.LogEntry {
	t.Helper()
	entries, err := st.ListRecent(limit)
	if err != nil {
		t.Fatalf("ListRecent() error = %v", err)
	}
	return entries
}

// traceIDForFile finds the indexed trace id of one fixture. The index assigns
// the id, so a fixture cannot choose it by writing the header.
func traceIDForFile(t *testing.T, items []traceListItem, name string) string {
	t.Helper()
	for _, item := range items {
		if strings.HasSuffix(item.logPath, name) {
			return item.ID
		}
	}
	t.Fatalf("no trace item for %s in %#v", name, items)
	return ""
}
