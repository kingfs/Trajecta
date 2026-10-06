package store

import (
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// TestSessionFiltersAgreeAcrossReadPaths is the differential gate for the session_summaries canary
// read switch (`database.use_session_summary_read`).
//
// The switch exists to serve the same session list from a derived read model instead of folding the
// logs on every request, so both paths have to answer the same filter the same way - otherwise
// turning the switch on silently changes what operators see. The summary row only keeps the
// session's last model, its provider list and its failure counter, so any filter that needs more
// than that has to stay on the log path.
func TestSessionFiltersAgreeAcrossReadPaths(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	base := time.Date(2026, 4, 16, 8, 0, 0, 0, time.UTC)
	writeSessionFilterLog(t, st, dir, "a-1.http", "sess-a", "gpt-alpha", base, http.StatusOK)
	writeSessionFilterLog(t, st, dir, "a-2.http", "sess-a", "claude-beta", base.Add(time.Minute), http.StatusOK)
	writeSessionFilterLog(t, st, dir, "b-1.http", "sess-b", "gpt-alpha", base.Add(2*time.Minute), http.StatusOK)
	writeSessionFilterLog(t, st, dir, "c-1.http", "sess-c", "gpt-alpha", base.Add(3*time.Minute), http.StatusInternalServerError)
	writeSessionFilterLog(t, st, dir, "d-1.http", "sess-d", "gpt-alpha", base.Add(4*time.Minute), http.StatusOK)
	writeSessionFilterLog(t, st, dir, "d-2.http", "sess-d", "gpt-alpha", base.Add(5*time.Minute), http.StatusInternalServerError)

	if err := st.RebuildSessionSummaries(); err != nil {
		t.Fatalf("RebuildSessionSummaries() error = %v", err)
	}

	all := []string{"sess-a", "sess-b", "sess-c", "sess-d"}
	for _, tc := range []struct {
		name   string
		filter ListFilter
		want   []string
	}{
		{name: "no filter", filter: ListFilter{}, want: all},
		{name: "provider", filter: ListFilter{Provider: "openai_compatible"}, want: all},
		{name: "model the session used first", filter: ListFilter{Model: "gpt-alpha"}, want: all},
		{name: "free-text search for a model the session used first", filter: ListFilter{Query: "gpt-alpha"}, want: all},
		{name: "free-text search for the last model", filter: ListFilter{Query: "claude-beta"}, want: []string{"sess-a"}},
		{name: "status success excludes a session with a failed request", filter: ListFilter{Status: "success"}, want: []string{"sess-a", "sess-b"}},
		{name: "status error", filter: ListFilter{Status: "error"}, want: []string{"sess-c", "sess-d"}},
		{name: "status failed alias", filter: ListFilter{Status: "failed"}, want: []string{"sess-c", "sess-d"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st.useSessionSummaryRead = false
			fromLogs := sessionIDsOf(t, st, tc.filter)
			st.useSessionSummaryRead = true
			fromSummaries := sessionIDsOf(t, st, tc.filter)
			if !reflect.DeepEqual(fromLogs, fromSummaries) {
				t.Fatalf("filter %+v: log-derived path returned %v but the summary read path returned %v; the same filter must not depend on database.use_session_summary_read", tc.filter, fromLogs, fromSummaries)
			}
			if !reflect.DeepEqual(fromLogs, tc.want) {
				t.Fatalf("filter %+v returned %v, want %v", tc.filter, fromLogs, tc.want)
			}
		})
	}
}

func sessionIDsOf(t *testing.T, st *Store, filter ListFilter) []string {
	t.Helper()
	page, err := st.ListSessionPage(1, 50, filter)
	if err != nil {
		t.Fatalf("ListSessionPage(%+v) error = %v", filter, err)
	}
	ids := make([]string, 0, len(page.Items))
	for _, item := range page.Items {
		ids = append(ids, item.SessionID)
	}
	sort.Strings(ids)
	return ids
}

func writeSessionFilterLog(t *testing.T, st *Store, dir string, name string, sessionID string, model string, recordedAt time.Time, statusCode int) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("# "+name+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
	header := recordfile.RecordHeader{Version: "LLM_PROXY_V3"}
	header.Meta.RequestID = name
	header.Meta.Time = recordedAt
	header.Meta.Model = model
	header.Meta.Provider = "openai_compatible"
	header.Meta.Operation = "chat_completions"
	header.Meta.Endpoint = "/v1/chat/completions"
	header.Meta.URL = "https://api.openai.com/v1/chat/completions"
	header.Meta.Method = http.MethodPost
	header.Meta.StatusCode = statusCode
	header.Meta.DurationMs = 500
	header.Meta.TTFTMs = 100
	header.Meta.ExchangeKind = "entry"
	header.Usage.TotalTokens = 10
	if err := st.UpsertLogWithGrouping(path, header, GroupingInfo{SessionID: sessionID, SessionSource: "header.session_id"}); err != nil {
		t.Fatalf("UpsertLogWithGrouping(%s) error = %v", path, err)
	}
}
