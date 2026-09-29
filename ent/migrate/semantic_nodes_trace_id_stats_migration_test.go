package entmigrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// semantic_nodes.trace_id is high-cardinality, but ANALYZE samples only ~30k
// rows, so the planner's distinct estimate is capped near the sample size
// (observed: 29,253 for ~74M rows with ~289k distinct ids). At that estimate
// the derived-id repair's orphan anti-join and the merge alternative were
// within 0.04% of each other, and the planner picked the nested loop, which
// costs one random disk read per distinct id. The migration pins the estimate
// to a measured fraction of rows so it stays valid as the table grows.
func TestPostgresSemanticNodesTraceIDStatsMigrationSetsNDistinct(t *testing.T) {
	upMatches, err := filepath.Glob(filepath.Join("..", "postgres-migrations", "*_set_semantic_nodes_trace_id_n_distinct.up.sql"))
	if err != nil {
		t.Fatalf("glob up migration: %v", err)
	}
	if len(upMatches) != 1 {
		t.Fatalf("semantic_nodes trace id stats up migrations = %v, want exactly one", upMatches)
	}
	downMatches, err := filepath.Glob(filepath.Join("..", "postgres-migrations", "*_set_semantic_nodes_trace_id_n_distinct.down.sql"))
	if err != nil {
		t.Fatalf("glob down migration: %v", err)
	}
	if len(downMatches) != 1 {
		t.Fatalf("semantic_nodes trace id stats down migrations = %v, want exactly one", downMatches)
	}

	up, err := os.ReadFile(upMatches[0])
	if err != nil {
		t.Fatalf("read up migration: %v", err)
	}
	for _, want := range []string{
		`ALTER TABLE "semantic_nodes" ALTER COLUMN "trace_id" SET (n_distinct = -0.004)`,
	} {
		if !strings.Contains(string(up), want) {
			t.Fatalf("migration %s missing %q", upMatches[0], want)
		}
	}

	down, err := os.ReadFile(downMatches[0])
	if err != nil {
		t.Fatalf("read down migration: %v", err)
	}
	for _, want := range []string{
		`ALTER TABLE "semantic_nodes" ALTER COLUMN "trace_id" RESET (n_distinct)`,
	} {
		if !strings.Contains(string(down), want) {
			t.Fatalf("migration %s missing %q", downMatches[0], want)
		}
	}
}
