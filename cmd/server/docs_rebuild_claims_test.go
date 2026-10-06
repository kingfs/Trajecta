package main

import (
	"os"
	"strings"
	"testing"
)

// TestLegacyMigrationDocNamesTheRebuildEntryForOverviewMetricMembers keeps the migration guide
// honest about a table it tells operators to rewrite.
//
// `layout apply` repoints overview_metric_bucket_members.path in the same transaction as the file
// rename, and LEGACY_MIGRATION.md lists that column among the three cassette-path columns. It used
// to say the column had no rebuild entry, which is the opposite of what operators need to know: the
// column is only maintained incrementally, so a process that died before settling its queue leaves
// it short, and `server db summary rebuild overview` (Store.RebuildOverviewMetricBuckets) is exactly
// the entry that recomputes it - together with overview_metric_buckets - from logs.
func TestLegacyMigrationDocNamesTheRebuildEntryForOverviewMetricMembers(t *testing.T) {
	body, err := os.ReadFile("../../docs/LEGACY_MIGRATION.md")
	if err != nil {
		t.Fatalf("read docs/LEGACY_MIGRATION.md: %v", err)
	}
	doc := string(body)

	const column = "`overview_metric_bucket_members.path`"
	if !strings.Contains(doc, column) {
		t.Fatalf("docs/LEGACY_MIGRATION.md no longer documents %s: this gate has to be updated with the column list", column)
	}
	tableRowSeen := false
	for _, line := range strings.Split(doc, "\n") {
		if !strings.Contains(line, column) {
			continue
		}
		if strings.Contains(line, "没有重建入口") {
			t.Fatalf("docs/LEGACY_MIGRATION.md still claims %s has no rebuild entry, but Store.RebuildOverviewMetricBuckets and `server db summary rebuild overview` recompute it from logs: %s", column, line)
		}
		if strings.HasPrefix(strings.TrimSpace(line), "|") {
			tableRowSeen = true
			if !strings.Contains(line, "db summary rebuild overview") {
				t.Fatalf("the cassette-path table in docs/LEGACY_MIGRATION.md describes %s without naming its rebuild entry `server db summary rebuild overview`: %s", column, line)
			}
		}
	}
	if !tableRowSeen {
		t.Fatalf("docs/LEGACY_MIGRATION.md no longer lists %s in the cassette-path table: this gate has to be updated with the table", column)
	}
	if !strings.Contains(doc, "RebuildOverviewMetricBuckets") {
		t.Fatalf("docs/LEGACY_MIGRATION.md does not name Store.RebuildOverviewMetricBuckets as the implementation behind the rebuild entry")
	}
}
