package store

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/appdbmigrate"
	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// TestPostgresOverviewDistributionsMatchPerColumnQueries pins the grouped pass
// against the per-column one it replaces.
//
// overviewDistributions answers the five Overview top-N lists from a single scan
// on Postgres (GROUPING SETS) and from five separate GROUP BY queries elsewhere.
// The optimisation is only worth having if both produce exactly the same five
// lists, including the parts of the contract that are easy to lose when rows are
// routed by which column came back non-NULL: values that are empty are excluded,
// the list is truncated to the limit, and ties fall back to label ASC.
//
// The fixture deliberately contains a tie (so the tiebreak is exercised), more
// distinct values than the limit (so truncation is), an empty value in every
// dimension (so the exclusion is) and a failure reason on one row only.
func TestPostgresOverviewDistributionsMatchPerColumnQueries(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TRAJECTA_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set TRAJECTA_TEST_POSTGRES_DSN to a disposable Postgres test database DSN")
	}
	if err := appdbmigrate.MigrateUp("postgres", dsn, 0); err != nil {
		t.Fatalf("MigrateUp(postgres) error = %v", err)
	}

	dir := t.TempDir()
	st, err := NewWithDatabaseOptions(dir, "postgres", dsn, 4, 4, DatabaseOptions{AutoMigrate: false})
	if err != nil {
		t.Fatalf("NewWithDatabaseOptions(postgres) error = %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Logf("Close(postgres store) error = %v", err)
		}
	})
	if st.driver != "postgres" {
		t.Fatalf("driver = %q, want postgres", st.driver)
	}

	// A per-run marker keeps the fixture from seeing rows other tests left behind.
	marker := fmt.Sprintf("ovdist%d", time.Now().UTC().UnixNano())
	seedOverviewDistributionFixture(t, st, dir, marker)

	// filter matches every seeded row and nothing else.
	filter := "request_id LIKE ?"
	args := []any{marker + "%"}
	const limit = 2

	grouped, err := st.overviewDistributions(overviewBreakdownColumns, filter, args, limit)
	if err != nil {
		t.Fatalf("overviewDistributions() error = %v", err)
	}

	// want[m][label] is the expected count, written out rather than derived from
	// the query under test.
	want := map[string]map[string]int{
		"model":                  {"m1": 5, "m2": 1, "m3": 1},
		"provider":               {"p1": 5, "p2": 1, "p3": 1},
		"endpoint":               {"e1": 5, "e2": 1, "e3": 1},
		"selected_upstream_id":   {"u1": 5, "u2": 1, "u3": 1},
		"routing_failure_reason": {"no_target": 1, "all_excluded": 1},
	}
	for _, column := range overviewBreakdownColumns {
		got := grouped[column]
		// limit=2 truncates; the expectation below is the ranked top-N, so build
		// it from the full count map with the same (count DESC, label ASC) rule.
		full := rankCountItems(want[column], 0)
		expected := full
		if len(expected) > limit {
			expected = expected[:limit]
		}
		if !reflect.DeepEqual(got, expected) {
			t.Fatalf("%s grouped pass = %+v, want %+v (full ranking %+v)", column, got, expected, full)
		}
	}

	// The same five lists from the per-column queries the function falls back to.
	// This is the parity assertion: it fails if the grouped pass drifts from the
	// query shape every other driver still uses.
	for _, column := range overviewBreakdownColumns {
		perColumn, err := st.overviewCountBy(column, filter, args, limit)
		if err != nil {
			t.Fatalf("overviewCountBy(%s) error = %v", column, err)
		}
		if !reflect.DeepEqual(grouped[column], perColumn) {
			t.Fatalf("grouped pass and per-column query disagree for %s: grouped=%+v per-column=%+v",
				column, grouped[column], perColumn)
		}
	}

	// An empty value must not surface as a label on any dimension.
	for _, column := range overviewBreakdownColumns {
		for _, item := range grouped[column] {
			if strings.TrimSpace(item.Label) == "" {
				t.Fatalf("%s grouped pass returned an empty label: %+v", column, grouped[column])
			}
		}
	}
}

// seedOverviewDistributionFixture writes six log rows through the normal
// recording path. request_id is prefixed with marker so the test can scope its
// own window.
func seedOverviewDistributionFixture(t *testing.T, st *Store, dir string, marker string) {
	t.Helper()

	type fixture struct {
		model    string
		provider string
		endpoint string
		upstream string
		reason   string
	}
	rows := []fixture{
		// m1/p1/e1/u1 is the heavy value on every dimension.
		{"m1", "p1", "e1", "u1", ""},
		{"m1", "p1", "e2", "u1", ""},
		{"m1", "p2", "e1", "u2", ""},
		{"m2", "p1", "e1", "u1", ""},
		{"m3", "p3", "e3", "u3", ""},
		// One row carries the failure reasons, so that dimension is non-trivial.
		{"m1", "p1", "e1", "u1", "no_target"},
	}
	// A second reason on its own row keeps that dimension at two values.
	rows = append(rows, fixture{"m1", "p1", "e1", "u1", "all_excluded"})

	for index, row := range rows {
		recordPath := filepath.Join(dir, fmt.Sprintf("%s-%d.http", marker, index))
		if err := os.WriteFile(recordPath, []byte("# overview distributions\n"), 0o644); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", recordPath, err)
		}
		header := recordfile.RecordHeader{Version: "LLM_PROXY_V3"}
		header.Meta.RequestID = fmt.Sprintf("%s-%d", marker, index)
		header.Meta.Time = time.Now().UTC().Add(time.Duration(index) * time.Second)
		header.Meta.URL = "https://api.openai.com/v1/chat/completions"
		header.Meta.Method = http.MethodPost
		header.Meta.StatusCode = http.StatusOK
		header.Meta.Model = row.model
		header.Meta.Provider = row.provider
		header.Meta.Endpoint = row.endpoint
		header.Meta.Operation = "chat.completions"
		header.Meta.SelectedUpstreamID = row.upstream
		header.Meta.RoutingFailureReason = row.reason
		if err := st.UpsertLogWithGrouping(recordPath, header, GroupingInfo{}); err != nil {
			t.Fatalf("UpsertLogWithGrouping(%s) error = %v", recordPath, err)
		}
	}
}
