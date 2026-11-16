package store

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// BenchmarkUpstreamDetailAnalytics measures the analytics portion of the
// channel detail read on the same 8-upstream fixture as
// BenchmarkUpstreamAnalytics. The list-scan arm is the pre-refactor path
// (ListUpstreamAnalytics over every upstream, then a linear pick of the target
// id); the scoped arm is upstreamAnalyticsForID, whose aggregate and supporting
// queries are all restricted to the target id. The remaining detail queries
// (traces, failure reasons, timeline) were already id-scoped and are unchanged.
func BenchmarkUpstreamDetailAnalytics(b *testing.B) {
	st, err := newSQLiteTestStore(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()

	const upstreams = 8
	const logsPerUpstream = 30
	dir := b.TempDir()
	now := time.Now().UTC()
	header := benchmarkStoreHeader()
	for u := 0; u < upstreams; u++ {
		upstreamID := fmt.Sprintf("bench-upstream-%02d", u)
		for i := 0; i < logsPerUpstream; i++ {
			path := filepath.Join(dir, fmt.Sprintf("%s-%d.http", upstreamID, i))
			if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
				b.Fatal(err)
			}
			header.Meta.RequestID = fmt.Sprintf("%s-%d", upstreamID, i)
			header.Meta.Model = fmt.Sprintf("bench-model-%d", i%5)
			header.Meta.Time = now.Add(-time.Duration(i) * time.Minute)
			header.Meta.StatusCode = 200
			if i%4 == 0 {
				header.Meta.StatusCode = 500
			}
			header.Meta.SelectedUpstreamID = upstreamID
			header.Usage.TotalTokens = 10
			if err := st.UpsertLogWithGrouping(path, header, GroupingInfo{}); err != nil {
				b.Fatal(err)
			}
		}
	}
	since := now.Add(-24 * time.Hour)
	const target = "bench-upstream-03"

	b.Run("list-scan", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			list, err := st.ListUpstreamAnalytics(8, 5, since, "")
			if err != nil {
				b.Fatal(err)
			}
			found := false
			for _, item := range list {
				if item.UpstreamID == target {
					found = true
					break
				}
			}
			if !found {
				b.Fatal("target upstream missing from list")
			}
		}
	})
	b.Run("scoped", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := st.upstreamAnalyticsForID(target, 8, 5, since, ""); err != nil {
				b.Fatal(err)
			}
		}
	})
}
