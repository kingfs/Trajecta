package legacymigrate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// fakePathIndex is an in-memory stand-in for the Postgres trace index. It keeps
// the same key invariant as the real schema: a path holds at most one set of
// rows (logs.path and overview_metric_bucket_members.path are primary keys).
type fakePathIndex struct {
	mu       sync.Mutex
	rows     map[string]PathRefs
	moveErr  error
	countErr error
	moves    [][2]string
}

func newFakePathIndex() *fakePathIndex {
	return &fakePathIndex{rows: map[string]PathRefs{}}
}

func (f *fakePathIndex) seed(path string, refs PathRefs) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[path] = refs
}

func (f *fakePathIndex) CountRefs(_ context.Context, cassettePath string) (PathRefs, error) {
	if f.countErr != nil {
		return PathRefs{}, f.countErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.rows[cassettePath], nil
}

func (f *fakePathIndex) MovePath(_ context.Context, oldPath, newPath string) (PathRefs, error) {
	if f.moveErr != nil {
		return PathRefs{}, f.moveErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, taken := f.rows[newPath]; taken {
		return PathRefs{}, fmt.Errorf("logs.path already has a row for %s", newPath)
	}
	refs := f.rows[oldPath]
	delete(f.rows, oldPath)
	if refs.Rows() > 0 {
		f.rows[newPath] = refs
	}
	f.moves = append(f.moves, [2]string{oldPath, newPath})
	return refs, nil
}

func (f *fakePathIndex) Close() error { return nil }

// withFakeIndex swaps the index seam for one test and seeds it.
func withFakeIndex(t *testing.T, index *fakePathIndex) {
	t.Helper()
	previous := openCassettePathIndex
	openCassettePathIndex = func(context.Context, string) (cassettePathIndex, error) { return index, nil }
	t.Cleanup(func() { openCassettePathIndex = previous })
}

func planMove(from, to, model string) LayoutMove {
	return LayoutMove{From: from, To: to, Model: model}
}

func TestApplyCassetteLayoutMovesFilesAndRepointsIndex(t *testing.T) {
	root := t.TempDir()
	source := writeLayoutCassette(t, root, "gpt-5.5/2026/01/02/a.http", recordfile.FileMagic, "gpt-5.5")
	index := newFakePathIndex()
	index.seed("/vault/gpt-5.5/2026/01/02/a.http", PathRefs{Logs: 1, Exchanges: 1, OverviewMembers: 3})
	withFakeIndex(t, index)

	report, err := ApplyCassetteLayout(context.Background(), ApplyOptions{
		Root:        root,
		DBPrefix:    "/vault",
		PostgresDSN: "postgres://test",
		Moves:       []LayoutMove{planMove("gpt-5.5/2026/01/02/a.http", "unknown-site/gpt-5.5/2026/01/02/a.http", "gpt-5.5")},
		Workers:     1,
		VerifyModel: true,
	})
	if err != nil {
		t.Fatalf("ApplyCassetteLayout() error = %v", err)
	}
	if report.Failed() || report.Moved != 1 {
		t.Fatalf("report = %+v, want one move and no failure", report)
	}
	if report.DatabaseRows != 5 {
		t.Fatalf("DatabaseRows = %d, want 5", report.DatabaseRows)
	}
	if report.CassettesWithoutIndex != 0 {
		t.Fatalf("CassettesWithoutIndex = %d, want 0", report.CassettesWithoutIndex)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("source still exists: %v", err)
	}
	target := filepath.Join(root, "unknown-site", "gpt-5.5", "2026", "01", "02", "a.http")
	if _, err := os.Stat(target); err != nil {
		t.Fatalf("target missing: %v", err)
	}
	if _, ok := index.rows["/vault/unknown-site/gpt-5.5/2026/01/02/a.http"]; !ok {
		t.Fatalf("index was not repointed: %+v", index.rows)
	}
	if _, stale := index.rows["/vault/gpt-5.5/2026/01/02/a.http"]; stale {
		t.Fatalf("index kept the old path: %+v", index.rows)
	}
}

func TestApplyCassetteLayoutIsIdempotent(t *testing.T) {
	root := t.TempDir()
	writeLayoutCassette(t, root, "gpt-5.5/2026/01/02/a.http", recordfile.FileMagic, "gpt-5.5")
	index := newFakePathIndex()
	index.seed("/vault/gpt-5.5/2026/01/02/a.http", PathRefs{Logs: 1})
	withFakeIndex(t, index)

	options := ApplyOptions{
		Root:        root,
		DBPrefix:    "/vault",
		PostgresDSN: "postgres://test",
		Moves:       []LayoutMove{planMove("gpt-5.5/2026/01/02/a.http", "unknown-site/gpt-5.5/2026/01/02/a.http", "gpt-5.5")},
		Workers:     1,
	}
	if _, err := ApplyCassetteLayout(context.Background(), options); err != nil {
		t.Fatalf("first ApplyCassetteLayout() error = %v", err)
	}
	second, err := ApplyCassetteLayout(context.Background(), options)
	if err != nil {
		t.Fatalf("second ApplyCassetteLayout() error = %v", err)
	}
	if second.Failed() {
		t.Fatalf("second run reported failures: %+v", second.Failures)
	}
	if second.AlreadyApplied != 1 || second.Moved != 0 {
		t.Fatalf("second run = moved %d already %d, want 0 and 1", second.Moved, second.AlreadyApplied)
	}
	if len(index.moves) != 1 {
		t.Fatalf("index moved %d times, want 1", len(index.moves))
	}
}

func TestApplyCassetteLayoutRepairsInterruptedRun(t *testing.T) {
	root := t.TempDir()
	// The file already sits at the target but the index still points at the
	// source: the state a crash between rename and commit leaves behind.
	writeLayoutCassette(t, root, "unknown-site/gpt-5.5/2026/01/02/a.http", recordfile.FileMagic, "gpt-5.5")
	index := newFakePathIndex()
	index.seed("/vault/gpt-5.5/2026/01/02/a.http", PathRefs{Logs: 1, OverviewMembers: 2})
	withFakeIndex(t, index)

	report, err := ApplyCassetteLayout(context.Background(), ApplyOptions{
		Root:        root,
		DBPrefix:    "/vault",
		PostgresDSN: "postgres://test",
		Moves:       []LayoutMove{planMove("gpt-5.5/2026/01/02/a.http", "unknown-site/gpt-5.5/2026/01/02/a.http", "gpt-5.5")},
		Workers:     1,
	})
	if err != nil {
		t.Fatalf("ApplyCassetteLayout() error = %v", err)
	}
	if report.Resumed != 1 || report.Failed() {
		t.Fatalf("report = %+v, want one resumed move", report)
	}
	if _, ok := index.rows["/vault/unknown-site/gpt-5.5/2026/01/02/a.http"]; !ok {
		t.Fatalf("index was not repaired: %+v", index.rows)
	}
}

func TestApplyCassetteLayoutRollsBackWhenIndexFails(t *testing.T) {
	root := t.TempDir()
	source := writeLayoutCassette(t, root, "gpt-5.5/2026/01/02/a.http", recordfile.FileMagic, "gpt-5.5")
	index := newFakePathIndex()
	index.seed("/vault/gpt-5.5/2026/01/02/a.http", PathRefs{Logs: 1})
	index.moveErr = fmt.Errorf("unique violation on logs.path")
	withFakeIndex(t, index)

	report, err := ApplyCassetteLayout(context.Background(), ApplyOptions{
		Root:        root,
		DBPrefix:    "/vault",
		PostgresDSN: "postgres://test",
		Moves:       []LayoutMove{planMove("gpt-5.5/2026/01/02/a.http", "unknown-site/gpt-5.5/2026/01/02/a.http", "gpt-5.5")},
		Workers:     1,
	})
	if err != nil {
		t.Fatalf("ApplyCassetteLayout() error = %v", err)
	}
	if report.FailedMoves != 1 || report.Moved != 0 {
		t.Fatalf("report = %+v, want one failure and no move", report)
	}
	if len(report.Failures) != 1 || report.Failures[0].Stage != "database" {
		t.Fatalf("failures = %+v, want a database failure", report.Failures)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("source was not rolled back: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "unknown-site", "gpt-5.5", "2026", "01", "02", "a.http")); !os.IsNotExist(err) {
		t.Fatalf("target still exists after rollback: %v", err)
	}
}

func TestApplyCassetteLayoutRefusesToOverwrite(t *testing.T) {
	root := t.TempDir()
	source := writeLayoutCassette(t, root, "gpt-5.5/2026/01/02/a.http", recordfile.FileMagic, "gpt-5.5")
	target := writeLayoutCassette(t, root, "unknown-site/gpt-5.5/2026/01/02/a.http", recordfile.FileMagic, "gpt-5.5")
	index := newFakePathIndex()
	withFakeIndex(t, index)

	report, err := ApplyCassetteLayout(context.Background(), ApplyOptions{
		Root:        root,
		DBPrefix:    "/vault",
		PostgresDSN: "postgres://test",
		Moves:       []LayoutMove{planMove("gpt-5.5/2026/01/02/a.http", "unknown-site/gpt-5.5/2026/01/02/a.http", "gpt-5.5")},
		Workers:     1,
	})
	if err != nil {
		t.Fatalf("ApplyCassetteLayout() error = %v", err)
	}
	if report.TargetExists != 1 || report.FailedMoves != 1 {
		t.Fatalf("report = %+v, want one occupied target", report)
	}
	for _, path := range []string{source, target} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s disappeared: %v", path, err)
		}
	}
}

func TestApplyCassetteLayoutDryRunTouchesNothing(t *testing.T) {
	root := t.TempDir()
	source := writeLayoutCassette(t, root, "gpt-5.5/2026/01/02/a.http", recordfile.FileMagic, "gpt-5.5")
	index := newFakePathIndex()
	index.seed("/vault/gpt-5.5/2026/01/02/a.http", PathRefs{Logs: 1})
	withFakeIndex(t, index)

	report, err := ApplyCassetteLayout(context.Background(), ApplyOptions{
		Root:        root,
		DBPrefix:    "/vault",
		PostgresDSN: "postgres://test",
		Moves:       []LayoutMove{planMove("gpt-5.5/2026/01/02/a.http", "unknown-site/gpt-5.5/2026/01/02/a.http", "gpt-5.5")},
		Workers:     1,
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("ApplyCassetteLayout() error = %v", err)
	}
	if report.Moved != 1 || report.DatabaseRows != 1 {
		t.Fatalf("report = %+v, want a planned move and one planned index row", report)
	}
	if report.DryRun != true || report.Note == "" {
		t.Fatalf("dry run was not reported: %+v", report)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("dry run moved the source: %v", err)
	}
	if len(index.moves) != 0 {
		t.Fatalf("dry run wrote to the index: %+v", index.moves)
	}
}

func TestApplyCassetteLayoutRejectsModelDrift(t *testing.T) {
	root := t.TempDir()
	source := writeLayoutCassette(t, root, "gpt-5.5/2026/01/02/a.http", recordfile.FileMagic, "gpt-5.4")
	index := newFakePathIndex()
	withFakeIndex(t, index)

	report, err := ApplyCassetteLayout(context.Background(), ApplyOptions{
		Root:        root,
		DBPrefix:    "/vault",
		PostgresDSN: "postgres://test",
		Moves:       []LayoutMove{planMove("gpt-5.5/2026/01/02/a.http", "unknown-site/gpt-5.5/2026/01/02/a.http", "gpt-5.5")},
		Workers:     1,
		VerifyModel: true,
	})
	if err != nil {
		t.Fatalf("ApplyCassetteLayout() error = %v", err)
	}
	if report.FailedMoves != 1 || report.Failures[0].Stage != "verify" {
		t.Fatalf("report = %+v, want a verify failure", report)
	}
	if _, err := os.Stat(source); err != nil {
		t.Fatalf("source moved despite model drift: %v", err)
	}
}

func TestApplyCassetteLayoutValidatesPlanBeforeMoving(t *testing.T) {
	root := t.TempDir()
	writeLayoutCassette(t, root, "gpt-5.5/2026/01/02/a.http", recordfile.FileMagic, "gpt-5.5")
	withFakeIndex(t, newFakePathIndex())

	_, err := ApplyCassetteLayout(context.Background(), ApplyOptions{
		Root:        root,
		PostgresDSN: "postgres://test",
		Moves: []LayoutMove{
			planMove("gpt-5.5/2026/01/02/a.http", "unknown-site/gpt-5.5/2026/01/02/a.http", "gpt-5.5"),
			planMove("../../etc/passwd", "unknown-site/etc/passwd", "etc"),
		},
		Workers: 1,
	})
	if err == nil || !strings.Contains(err.Error(), "escapes the cassette root") {
		t.Fatalf("error = %v, want an escape refusal", err)
	}
	if _, err := os.Stat(filepath.Join(root, "gpt-5.5", "2026", "01", "02", "a.http")); err != nil {
		t.Fatalf("a rejected plan moved a file: %v", err)
	}
}

func TestApplyCassetteLayoutFiltersAndLimits(t *testing.T) {
	root := t.TempDir()
	index := newFakePathIndex()
	withFakeIndex(t, index)

	moves := []LayoutMove{
		planMove("gpt-5.5/2026/01/02/a.http", "unknown-site/gpt-5.5/2026/01/02/a.http", "gpt-5.5"),
		planMove("gpt-5.5/2026/01/02/b.http", "unknown-site/gpt-5.5/2026/01/02/b.http", "gpt-5.5"),
		planMove("deepseek-flash/2026/01/02/c.http", "unknown-site/deepseek-flash/2026/01/02/c.http", "deepseek-flash"),
	}
	for _, move := range moves {
		writeLayoutCassette(t, root, move.From, recordfile.FileMagic, move.Model)
	}

	report, err := ApplyCassetteLayout(context.Background(), ApplyOptions{
		Root:        root,
		DBPrefix:    "/vault",
		PostgresDSN: "postgres://test",
		Moves:       moves,
		Workers:     1,
		DryRun:      true,
		OnlyModels:  []string{"deepseek-flash"},
	})
	if err != nil {
		t.Fatalf("ApplyCassetteLayout() error = %v", err)
	}
	if report.Selected != 1 || report.Planned != 3 {
		t.Fatalf("report = %+v, want 1 of 3 selected", report)
	}

	limited, err := ApplyCassetteLayout(context.Background(), ApplyOptions{
		Root:        root,
		DBPrefix:    "/vault",
		PostgresDSN: "postgres://test",
		Moves:       moves,
		Workers:     1,
		DryRun:      true,
		Limit:       2,
	})
	if err != nil {
		t.Fatalf("ApplyCassetteLayout() error = %v", err)
	}
	if limited.Selected != 2 || limited.Remaining != 1 {
		t.Fatalf("report = %+v, want 2 selected and 1 remaining", limited)
	}
}

func TestApplyCassetteLayoutRequiresDatabase(t *testing.T) {
	root := t.TempDir()
	writeLayoutCassette(t, root, "gpt-5.5/2026/01/02/a.http", recordfile.FileMagic, "gpt-5.5")

	_, err := ApplyCassetteLayout(context.Background(), ApplyOptions{
		Root:            root,
		Moves:           []LayoutMove{planMove("gpt-5.5/2026/01/02/a.http", "unknown-site/gpt-5.5/2026/01/02/a.http", "gpt-5.5")},
		Workers:         1,
		RequireDatabase: true,
	})
	if err == nil || !strings.Contains(err.Error(), "no Postgres DSN") {
		t.Fatalf("error = %v, want a missing database refusal", err)
	}
}

func TestApplyCassetteLayoutReportsCassettesWithoutIndexRows(t *testing.T) {
	root := t.TempDir()
	writeLayoutCassette(t, root, "gpt-5.5/2026/01/02/a.http", recordfile.FileMagic, "gpt-5.5")
	index := newFakePathIndex()
	withFakeIndex(t, index)

	report, err := ApplyCassetteLayout(context.Background(), ApplyOptions{
		Root:        root,
		DBPrefix:    "/vault",
		PostgresDSN: "postgres://test",
		Moves:       []LayoutMove{planMove("gpt-5.5/2026/01/02/a.http", "unknown-site/gpt-5.5/2026/01/02/a.http", "gpt-5.5")},
		Workers:     1,
	})
	if err != nil {
		t.Fatalf("ApplyCassetteLayout() error = %v", err)
	}
	if report.Moved != 1 || report.CassettesWithoutIndex != 1 {
		t.Fatalf("report = %+v, want one moved cassette without index rows", report)
	}
}
