package store

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// TestTraceStatusFilterAcceptsTheFailedAlias pins the vocabulary of the trace-list `status` filter
// on every read path at once.
//
// `/api/sessions?status=failed` has always filtered (the session read path maps failed to error), and
// so do the Responses audit API and `analyze`, but the trace list matched neither "success" nor
// "error" for "failed" and therefore returned the unfiltered list: measured with one 200 and one 500
// trace, `status=error` returned 1 and `status=failed` returned 2. The filter compares Stats,
// ListPage and ListTraceIDs as well as the expected traces, because a filter that is ignored by both
// builders agrees with itself - the totals invariant next door cannot see this bug.
func TestTraceStatusFilterAcceptsTheFailedAlias(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer st.Close()

	now := time.Now().UTC()
	write := func(name string, statusCode int, errorText string, kind string) {
		t.Helper()
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", name, err)
		}
		if err := st.UpsertLog(path, recordfile.RecordHeader{
			Version: "LLM_PROXY_V3",
			Meta: recordfile.MetaData{
				RequestID:    name,
				Time:         now,
				Model:        "gpt-5",
				Provider:     "openai_compatible",
				Endpoint:     "/v1/chat/completions",
				URL:          "/v1/chat/completions",
				Method:       http.MethodPost,
				StatusCode:   statusCode,
				Error:        errorText,
				ExchangeKind: kind,
			},
		}); err != nil {
			t.Fatalf("UpsertLog(%q) error = %v", name, err)
		}
	}
	write("ok.http", http.StatusOK, "", "entry")
	write("boom.http", http.StatusInternalServerError, "upstream exploded", "entry")
	// A stream that reported 200 and then failed is a failed trace for this filter.
	write("mid-stream.http", http.StatusOK, "stream interrupted", "entry")
	// A child model exchange is never part of the client-visible list.
	write("child.http", http.StatusInternalServerError, "child exploded", "model")

	cases := []struct {
		name   string
		status string
		want   []string
	}{
		{name: "no filter", status: "", want: []string{"boom.http", "mid-stream.http", "ok.http"}},
		{name: "success", status: "success", want: []string{"ok.http"}},
		{name: "error", status: "error", want: []string{"boom.http", "mid-stream.http"}},
		{name: "failed alias", status: "failed", want: []string{"boom.http", "mid-stream.http"}},
		{name: "failed alias is case insensitive", status: "FAILED", want: []string{"boom.http", "mid-stream.http"}},
		{name: "unrecognised value does not filter", status: "nonsense", want: []string{"boom.http", "mid-stream.http", "ok.http"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			page, err := st.ListPage(1, 50, ListFilter{Status: tc.status})
			if err != nil {
				t.Fatalf("ListPage(%q) error = %v", tc.status, err)
			}
			got := traceNamesOf(page.Items)
			if !equalStringSlices(got, tc.want) {
				t.Fatalf("status=%q listed %v, want %v", tc.status, got, tc.want)
			}

			stats, err := st.Stats(ListFilter{Status: tc.status})
			if err != nil {
				t.Fatalf("Stats(%q) error = %v", tc.status, err)
			}
			if stats.TotalRequest != len(tc.want) {
				t.Fatalf("status=%q Stats.TotalRequest = %d, want %d: the aggregate and the list must apply the same filter", tc.status, stats.TotalRequest, len(tc.want))
			}
			ids, err := st.ListTraceIDs(ListFilter{Status: tc.status}, 50)
			if err != nil {
				t.Fatalf("ListTraceIDs(%q) error = %v", tc.status, err)
			}
			if len(ids) != len(tc.want) {
				t.Fatalf("status=%q ListTraceIDs = %v, want %d ids: the aggregate and the list must apply the same filter", tc.status, ids, len(tc.want))
			}
		})
	}
}

func traceNamesOf(items []LogEntry) []string {
	names := make([]string, 0, len(items))
	for _, item := range items {
		names = append(names, item.Header.Meta.RequestID)
	}
	sort.Strings(names)
	return names
}

func equalStringSlices(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
