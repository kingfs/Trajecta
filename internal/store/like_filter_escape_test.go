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

// TestLogFiltersTreatLikeWildcardsLiterally pins the filter semantics of the traffic list against
// the characters LIKE gives a meaning to.
//
// `escapeLike` escapes `%`, `_` and the backslash, so the SQL that consumes it must declare
// `ESCAPE '\'`; without that clause SQLite has no escape character at all and the escape
// backslashes the pattern now contains become literal characters. A user filtering the traffic list
// by `gpt_oss` - an ordinary model id - would then match nothing, while the same query works on
// Postgres, whose LIKE escapes with a backslash by default. The free-text `q` filter always
// declared ESCAPE, which is the behaviour every filter here has to share.
func TestLogFiltersTreatLikeWildcardsLiterally(t *testing.T) {
	st, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	dir := t.TempDir()
	records := []struct {
		name       string
		model      string
		endpoint   string
		upstreamID string
	}{
		{name: "underscore", model: "gpt_oss", endpoint: "/v1/chat_completions", upstreamID: "local_ollama"},
		{name: "plain", model: "gptoss", endpoint: "/v1/chatcompletions", upstreamID: "localollama"},
		{name: "percent", model: "50%off", endpoint: "/v1/percent%endpoint", upstreamID: "upstream%percent"},
	}
	for i, record := range records {
		path := filepath.Join(dir, record.name+".http")
		if err := os.WriteFile(path, []byte("# "+record.name+"\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", path, err)
		}
		header := recordfile.RecordHeader{Version: "LLM_PROXY_V3"}
		header.Meta.RequestID = "req-like-" + record.name
		header.Meta.Time = time.Date(2026, 7, 3, 9, i, 0, 0, time.UTC)
		header.Meta.URL = "https://api.openai.com" + record.endpoint
		header.Meta.Method = http.MethodPost
		header.Meta.StatusCode = http.StatusOK
		header.Meta.Model = record.model
		header.Meta.Endpoint = record.endpoint
		header.Meta.Provider = "openai"
		header.Meta.SelectedUpstreamID = record.upstreamID
		header.Meta.ExchangeKind = "entry"
		header.Meta.TraceID = "trace-" + record.name
		if err := st.UpsertLogWithGrouping(path, header, GroupingInfo{}); err != nil {
			t.Fatalf("UpsertLogWithGrouping(%s) error = %v", path, err)
		}
	}

	// statsFilteredBy drives the SQL clause every non-ent filter consumer shares (Stats,
	// ListTraceIDs, the session page), while listPage drives the ent predicates. Both have to
	// answer the same way for the same filter, which is what makes the missing ESCAPE a defect.
	for _, tc := range []struct {
		name   string
		filter ListFilter
		want   []string
		wantBy string
	}{
		{name: "model with an underscore", filter: ListFilter{Model: "gpt_oss"}, want: []string{"gpt_oss"}},
		{name: "model without one", filter: ListFilter{Model: "gptoss"}, want: []string{"gptoss"}},
		{name: "model filter stays a substring match", filter: ListFilter{Model: "gpt"}, want: []string{"gpt_oss", "gptoss"}},
		{name: "model with a percent sign", filter: ListFilter{Model: "50%off"}, want: []string{"50%off"}},
		{name: "endpoint with an underscore", filter: ListFilter{Endpoint: "/v1/chat_completions"}, want: []string{"gpt_oss"}},
		{name: "upstream with an underscore", filter: ListFilter{SelectedUpstream: "local_ollama"}, want: []string{"gpt_oss"}},
		{name: "upstream with a percent sign", filter: ListFilter{SelectedUpstream: "upstream%percent"}, want: []string{"50%off"}},
		{name: "free-text search with an underscore", filter: ListFilter{Query: "gpt_oss"}, want: []string{"gpt_oss"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			page, err := st.ListPage(1, 50, tc.filter)
			if err != nil {
				t.Fatalf("ListPage(%+v) error = %v", tc.filter, err)
			}
			got := make([]string, 0, len(page.Items))
			for _, item := range page.Items {
				got = append(got, item.Header.Meta.Model)
			}
			sort.Strings(got)
			want := append([]string(nil), tc.want...)
			sort.Strings(want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("ListPage(%+v) returned %v, want %v", tc.filter, got, want)
			}

			// Stats shares the SQL clause with ListTraceIDs and the session page, so it has to
			// reach the same conclusion as the ent-backed list for the same filter.
			stats, err := st.Stats(tc.filter)
			if err != nil {
				t.Fatalf("Stats(%+v) error = %v", tc.filter, err)
			}
			if stats.TotalRequest != len(tc.want) {
				t.Fatalf("Stats(%+v).TotalRequest = %d, want %d: a filter value is matched literally, so LIKE's own wildcards in it must not act as wildcards (and the escape backslashes must not become literal characters), which is what ListPage already does", tc.filter, stats.TotalRequest, len(tc.want))
			}

			traceIDs, err := st.ListTraceIDs(tc.filter, 50)
			if err != nil {
				t.Fatalf("ListTraceIDs(%+v) error = %v", tc.filter, err)
			}
			if len(traceIDs) != len(tc.want) {
				t.Fatalf("ListTraceIDs(%+v) returned %d id(s) %v, want %d", tc.filter, len(traceIDs), traceIDs, len(tc.want))
			}
		})
	}
}
