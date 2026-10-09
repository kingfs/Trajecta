package store

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// The console's 今天/近 7 天/近 30 天/全部 control is a bound on `recorded_at`, and
// three different queries answer the same list: the ent path behind the page,
// the raw path behind the counters above it, and the session summary read model.
// A window that reached only one of them would show a list whose own total
// disagreed with it, or a session list that ignored the control entirely.
func TestListWindowBoundsTracesCountersAndSessions(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	now := time.Now().UTC()
	writeTrace := func(name, sessionID string, recordedAt time.Time) {
		t.Helper()
		dir := t.TempDir()
		path := filepath.Join(dir, name+".http")
		if err := os.WriteFile(path, []byte("# trajecta/v3\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
		header := recordfile.RecordHeader{Version: "LLM_PROXY_V3"}
		header.Meta.RequestID = "req_" + name
		header.Meta.Time = recordedAt
		header.Meta.URL = "https://api.openai.com/v1/chat/completions"
		header.Meta.Method = http.MethodPost
		header.Meta.StatusCode = http.StatusOK
		header.Meta.Model = "gpt-window-" + name
		header.Meta.Provider = "openai_compatible"
		header.Meta.Operation = "chat.completions"
		header.Meta.Endpoint = "/v1/chat/completions"
		if err := st.UpsertLogWithGrouping(path, header, GroupingInfo{SessionID: sessionID, SessionSource: "header"}); err != nil {
			t.Fatalf("UpsertLogWithGrouping(%s) error = %v", name, err)
		}
	}

	writeTrace("recent", "sess_recent", now.Add(-2*time.Hour))
	writeTrace("old", "sess_old", now.Add(-40*24*time.Hour))
	st.FlushDerivedRefresh()

	all := ListFilter{}
	today := ListFilter{Since: now.Add(-24 * time.Hour)}

	assertCount := func(label string, filter ListFilter, want int) {
		t.Helper()
		page, err := st.ListPage(1, 50, filter)
		if err != nil {
			t.Fatalf("ListPage(%s) error = %v", label, err)
		}
		if page.Total != want {
			t.Errorf("ListPage(%s).Total = %d, want %d", label, page.Total, want)
		}
		stats, err := st.Stats(filter)
		if err != nil {
			t.Fatalf("Stats(%s) error = %v", label, err)
		}
		if stats.TotalRequest != want {
			t.Errorf("Stats(%s).TotalRequest = %d, want %d (the counters must agree with the list)", label, stats.TotalRequest, want)
		}
		ids, err := st.ListTraceIDs(filter, 50)
		if err != nil {
			t.Fatalf("ListTraceIDs(%s) error = %v", label, err)
		}
		if len(ids) != want {
			t.Errorf("ListTraceIDs(%s) = %d ids, want %d", label, len(ids), want)
		}
	}

	assertCount("all", all, 2)
	assertCount("since-yesterday", today, 1)

	sessions := func(label string, filter ListFilter) []string {
		t.Helper()
		page, err := st.ListSessionPage(1, 50, filter)
		if err != nil {
			t.Fatalf("ListSessionPage(%s) error = %v", label, err)
		}
		ids := make([]string, 0, len(page.Items))
		for _, item := range page.Items {
			ids = append(ids, item.SessionID)
		}
		return ids
	}

	if got := sessions("all", all); len(got) != 2 {
		t.Errorf("ListSessionPage(all) = %v, want both sessions", got)
	}
	windowed := strings.Join(sessions("since-yesterday", today), ",")
	if !strings.Contains(windowed, "sess_recent") || strings.Contains(windowed, "sess_old") {
		t.Errorf("ListSessionPage(since-yesterday) = %q, want only the session active in the window", windowed)
	}
}
