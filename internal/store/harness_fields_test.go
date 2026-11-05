package store

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// The client-harness classification is derived by internal/harness and written
// as a `harness.detected` prelude event; these columns are its indexed mirror.
// The round trip is what the Monitor list depends on: the write path must store
// the fields and the tag list, the read path must return them, and the harness
// filter must select exactly the classified rows on both read paths (the ent
// page and the raw counters above it).
func TestLogClientHarnessRoundTripAndFilter(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	write := func(name string, mutate func(*recordfile.MetaData)) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), name+".http")
		if err := os.WriteFile(path, []byte("# trajecta/v3\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
		header := recordfile.RecordHeader{Version: "LLM_PROXY_V3"}
		header.Meta.RequestID = "req_" + name
		header.Meta.Time = time.Now().UTC()
		header.Meta.URL = "https://api.anthropic.com/v1/messages"
		header.Meta.Method = "POST"
		header.Meta.StatusCode = 200
		header.Meta.Provider = "anthropic"
		header.Meta.Endpoint = "/v1/messages"
		if mutate != nil {
			mutate(&header.Meta)
		}
		traceID, err := st.UpsertLogWithGroupingAndTraceID(path, header, GroupingInfo{SessionID: "sess_" + name, SessionSource: "header"})
		if err != nil {
			t.Fatalf("UpsertLogWithGroupingAndTraceID(%s) error = %v", name, err)
		}
		return traceID
	}

	claudeTraceID := write("claude", func(meta *recordfile.MetaData) {
		meta.ClientHarness = "claude-code"
		meta.ClientHarnessName = "Claude Code"
		meta.ClientHarnessVersion = "1.2.3"
		meta.ClientHarnessKind = "agent-cli"
		meta.ClientHarnessConfidence = "high"
		meta.ClientHarnessTags = []string{"anthropic-sdk", "tools"}
	})
	codexTraceID := write("codex", func(meta *recordfile.MetaData) {
		meta.ClientHarness = "codex-cli"
		meta.ClientHarnessName = "Codex CLI"
		meta.ClientHarnessKind = "agent-cli"
		meta.ClientHarnessConfidence = "medium"
		meta.ClientHarnessTags = []string{"openai-sdk"}
	})
	plainTraceID := write("plain", nil)

	// Read back through the detail path: every field and the tag order survive.
	entry, err := st.GetByID(claudeTraceID)
	if err != nil {
		t.Fatalf("GetByID(claude) error = %v", err)
	}
	meta := entry.Header.Meta
	if meta.ClientHarness != "claude-code" {
		t.Errorf("ClientHarness = %q, want claude-code", meta.ClientHarness)
	}
	if meta.ClientHarnessName != "Claude Code" {
		t.Errorf("ClientHarnessName = %q, want Claude Code", meta.ClientHarnessName)
	}
	if meta.ClientHarnessVersion != "1.2.3" {
		t.Errorf("ClientHarnessVersion = %q, want 1.2.3", meta.ClientHarnessVersion)
	}
	if meta.ClientHarnessKind != "agent-cli" {
		t.Errorf("ClientHarnessKind = %q, want agent-cli", meta.ClientHarnessKind)
	}
	if meta.ClientHarnessConfidence != "high" {
		t.Errorf("ClientHarnessConfidence = %q, want high", meta.ClientHarnessConfidence)
	}
	if want := []string{"anthropic-sdk", "tools"}; !reflect.DeepEqual(meta.ClientHarnessTags, want) {
		t.Errorf("ClientHarnessTags = %v, want %v", meta.ClientHarnessTags, want)
	}

	// A trace recorded before the classification existed has no fields and no
	// tags, and the row still decodes without an error.
	plain, err := st.GetByID(plainTraceID)
	if err != nil {
		t.Fatalf("GetByID(plain) error = %v", err)
	}
	if plain.Header.Meta.ClientHarness != "" || len(plain.Header.Meta.ClientHarnessTags) != 0 {
		t.Errorf("unclassified trace = harness %q tags %v, want empty", plain.Header.Meta.ClientHarness, plain.Header.Meta.ClientHarnessTags)
	}

	// The list page carries the same facts, and the filter is exact and
	// case-insensitive: the console sends the canonical id, but a hand-written
	// query must not be silently case-sensitive.
	page, err := st.ListPage(1, 50, ListFilter{Harness: "CLAUDE-CODE"})
	if err != nil {
		t.Fatalf("ListPage(harness=CLAUDE-CODE) error = %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("ListPage(harness=CLAUDE-CODE) = %d items (total %d), want only the claude trace", len(page.Items), page.Total)
	}
	if page.Items[0].ID != claudeTraceID {
		t.Errorf("ListPage(harness=CLAUDE-CODE) item = %q, want %q", page.Items[0].ID, claudeTraceID)
	}
	if got := page.Items[0].Header.Meta.ClientHarnessTags; !reflect.DeepEqual(got, []string{"anthropic-sdk", "tools"}) {
		t.Errorf("list item tags = %v, want [anthropic-sdk tools]", got)
	}

	codexPage, err := st.ListPage(1, 50, ListFilter{Harness: "codex-cli"})
	if err != nil {
		t.Fatalf("ListPage(harness=codex-cli) error = %v", err)
	}
	if codexPage.Total != 1 || codexPage.Items[0].ID != codexTraceID {
		t.Fatalf("ListPage(harness=codex-cli) = %+v, want only the codex trace", codexPage.Items)
	}

	unknownPage, err := st.ListPage(1, 50, ListFilter{Harness: "no-such-harness"})
	if err != nil {
		t.Fatalf("ListPage(unknown harness) error = %v", err)
	}
	if unknownPage.Total != 0 {
		t.Errorf("ListPage(unknown harness).Total = %d, want 0", unknownPage.Total)
	}

	// The counters above the list and the id list use the raw SQL path; they
	// must select the same rows as the ent-backed page or the list header
	// disagrees with the list itself.
	stats, err := st.Stats(ListFilter{Harness: "claude-code"})
	if err != nil {
		t.Fatalf("Stats(harness=claude-code) error = %v", err)
	}
	if stats.TotalRequest != 1 {
		t.Errorf("Stats(harness=claude-code).TotalRequest = %d, want 1", stats.TotalRequest)
	}
	ids, err := st.ListTraceIDs(ListFilter{Harness: "Codex-CLI"}, 50)
	if err != nil {
		t.Fatalf("ListTraceIDs(harness=Codex-CLI) error = %v", err)
	}
	if !reflect.DeepEqual(ids, []string{codexTraceID}) {
		t.Errorf("ListTraceIDs(harness=Codex-CLI) = %v, want [%s]", ids, codexTraceID)
	}

	// No filter means no predicate: all three rows stay listed.
	all, err := st.ListPage(1, 50, ListFilter{})
	if err != nil {
		t.Fatalf("ListPage(no harness) error = %v", err)
	}
	if all.Total != 3 {
		t.Errorf("ListPage(no harness).Total = %d, want 3", all.Total)
	}

	// The session list degrades to the same raw log clause when a filter its
	// summary row cannot answer is present, so a harness filter must reach that
	// clause with the session-table alias and select only the matching session.
	sessions, err := st.ListSessionPage(1, 50, ListFilter{Harness: "codex-cli"})
	if err != nil {
		t.Fatalf("ListSessionPage(harness=codex-cli) error = %v", err)
	}
	if sessions.Total != 1 || len(sessions.Items) != 1 || sessions.Items[0].SessionID != "sess_codex" {
		t.Errorf("ListSessionPage(harness=codex-cli) = %+v, want only sess_codex", sessions.Items)
	}

	// A derived tag list the decoder cannot read is treated as "no tags" rather
	// than failing the read: the column is a mirror, not the durable record.
	if _, err := st.db.Exec(`UPDATE logs SET client_harness_tags_json = 'not-json' WHERE trace_id = ?`, claudeTraceID); err != nil {
		t.Fatalf("corrupt tags update error = %v", err)
	}
	corrupt, err := st.GetByID(claudeTraceID)
	if err != nil {
		t.Fatalf("GetByID(corrupt tags) error = %v", err)
	}
	if corrupt.Header.Meta.ClientHarness != "claude-code" || len(corrupt.Header.Meta.ClientHarnessTags) != 0 {
		t.Errorf("corrupt tags read = harness %q tags %v, want the id and no tags", corrupt.Header.Meta.ClientHarness, corrupt.Header.Meta.ClientHarnessTags)
	}
}
