package cli

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/recordfile"
	_ "modernc.org/sqlite"
)

func writeDBCleanupCLIConfig(t *testing.T, dir string) string {
	t.Helper()
	configPath := filepath.Join(dir, "config.yaml")
	body := `
trace:
  output_dir: "` + dir + `"
database:
  driver: sqlite
  dsn: "` + filepath.Join(dir, "trace_index.sqlite3") + `"
  auto_migrate: true
`
	if err := os.WriteFile(configPath, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile(config) error = %v", err)
	}
	return configPath
}

func writeDBCleanupCLILog(t *testing.T, st *store.Store, dir string, name string, sessionID string, statusCode int, at time.Time) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("cassette:"+name), 0o644); err != nil {
		t.Fatalf("write cassette %s: %v", name, err)
	}
	header := recordfile.RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta: recordfile.MetaData{
			RequestID:  name,
			Time:       at.UTC(),
			Model:      "gpt-5",
			Provider:   "openai_compatible",
			Endpoint:   "/v1/chat/completions",
			URL:        "/v1/chat/completions",
			Method:     http.MethodPost,
			StatusCode: statusCode,
		},
	}
	if err := st.UpsertLogWithGrouping(path, header, store.GroupingInfo{SessionID: sessionID, SessionSource: "test"}); err != nil {
		t.Fatalf("upsert log %s: %v", name, err)
	}
	return path
}

func dbCleanupConfigLogCount(t *testing.T, dbPath string) int {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM logs`).Scan(&count); err != nil {
		t.Fatalf("count logs error = %v", err)
	}
	return count
}

func dbCleanupEnvelopeResult(t *testing.T, out []byte) map[string]any {
	t.Helper()
	var envelope Envelope
	if err := json.Unmarshal(out, &envelope); err != nil {
		t.Fatalf("json.Unmarshal() error = %v; output=%s", err, string(out))
	}
	if !envelope.OK {
		t.Fatalf("envelope not ok: %s", string(out))
	}
	result, ok := envelope.Result.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v, want a map", envelope.Result)
	}
	return result
}

func TestDBCleanupRequestsSQLiteDryRunThenApply(t *testing.T) {
	dir := t.TempDir()
	configPath := writeDBCleanupCLIConfig(t, dir)
	dbPath := filepath.Join(dir, "trace_index.sqlite3")
	st, err := store.NewWithDatabase(dir, "sqlite", dbPath, 1, 1)
	if err != nil {
		t.Fatalf("store.NewWithDatabase() error = %v", err)
	}
	base := time.Date(2026, 11, 5, 10, 0, 0, 0, time.UTC)
	badPath := writeDBCleanupCLILog(t, st, dir, "cleanup-cli-404.http", "sess-cli", http.StatusNotFound, base)
	goodPath := writeDBCleanupCLILog(t, st, dir, "cleanup-cli-200.http", "sess-cli", http.StatusOK, base.Add(time.Minute))
	if err := st.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	var dryOut bytes.Buffer
	if code, _ := runDBCleanupRequestsWithOptions(dbCleanupRequestsOptions{
		configPath: configPath,
		format:     "json",
		stdout:     &dryOut,
	}, store.RequestCleanupFilter{StatusCodes: []int{http.StatusNotFound}}); code != 0 {
		t.Fatalf("dry run code = %d, output=%s", code, dryOut.String())
	}
	dry := dbCleanupEnvelopeResult(t, dryOut.Bytes())
	if dry["dry_run"] != true || dry["applied"] != false {
		t.Fatalf("dry run flags = %#v", dry)
	}
	if dry["matched_requests"].(float64) != 1 || dry["total_log_rows"].(float64) != 1 {
		t.Fatalf("dry run counts = %#v, want one match", dry)
	}
	if _, ok := dry["deleted_logs"]; ok {
		t.Fatal("dry run reported deleted_logs")
	}
	if !fileExists(badPath) || !fileExists(goodPath) {
		t.Fatal("dry run removed a cassette file")
	}
	if got := dbCleanupConfigLogCount(t, dbPath); got != 2 {
		t.Fatalf("logs after dry run = %d, want 2", got)
	}

	var applyOut bytes.Buffer
	if code, _ := runDBCleanupRequestsWithOptions(dbCleanupRequestsOptions{
		configPath: configPath,
		format:     "json",
		stdout:     &applyOut,
		apply:      true,
	}, store.RequestCleanupFilter{StatusCodes: []int{http.StatusNotFound}}); code != 0 {
		t.Fatalf("apply code = %d, output=%s", code, applyOut.String())
	}
	applied := dbCleanupEnvelopeResult(t, applyOut.Bytes())
	if applied["applied"] != true {
		t.Fatalf("apply flags = %#v", applied)
	}
	if applied["deleted_logs"].(float64) != 1 {
		t.Fatalf("deleted logs = %v, want 1", applied["deleted_logs"])
	}
	if applied["deleted_cassettes"].(float64) != 1 {
		t.Fatalf("deleted cassettes = %v, want 1", applied["deleted_cassettes"])
	}
	if fileExists(badPath) {
		t.Fatal("the 404 cassette survived an applied run")
	}
	if !fileExists(goodPath) {
		t.Fatal("the 200 cassette was removed")
	}
	if got := dbCleanupConfigLogCount(t, dbPath); got != 1 {
		t.Fatalf("logs after apply = %d, want 1", got)
	}
}

func TestDBCleanupRequestsKeepCassettes(t *testing.T) {
	dir := t.TempDir()
	configPath := writeDBCleanupCLIConfig(t, dir)
	dbPath := filepath.Join(dir, "trace_index.sqlite3")
	st, err := store.NewWithDatabase(dir, "sqlite", dbPath, 1, 1)
	if err != nil {
		t.Fatalf("store.NewWithDatabase() error = %v", err)
	}
	badPath := writeDBCleanupCLILog(t, st, dir, "cleanup-keep-404.http", "sess-keep", http.StatusNotFound, time.Date(2026, 11, 5, 11, 0, 0, 0, time.UTC))
	if err := st.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	var out bytes.Buffer
	if code, _ := runDBCleanupRequestsWithOptions(dbCleanupRequestsOptions{
		configPath:    configPath,
		format:        "json",
		stdout:        &out,
		apply:         true,
		keepCassettes: true,
	}, store.RequestCleanupFilter{StatusCodes: []int{http.StatusNotFound}}); code != 0 {
		t.Fatalf("keep-cassettes code = %d, output=%s", code, out.String())
	}
	result := dbCleanupEnvelopeResult(t, out.Bytes())
	if result["deleted_cassettes"].(float64) != 0 || result["kept_cassettes"].(float64) != 1 {
		t.Fatalf("keep-cassettes result = %#v", result)
	}
	if !fileExists(badPath) {
		t.Fatal("--keep-cassettes removed the cassette file")
	}
	if got := dbCleanupConfigLogCount(t, dbPath); got != 0 {
		t.Fatalf("logs after keep-cassettes apply = %d, want 0", got)
	}
}

func TestDBCleanupRequestsFilterValidation(t *testing.T) {
	if _, err := dbCleanupRequestsFilter(dbCleanupRequestsOptions{}); err == nil {
		t.Fatal("dbCleanupRequestsFilter() without a selector = nil error, want usage error")
	} else {
		var exit ExitError
		if !errors.As(err, &exit) || exit.code != ExitCodeUsage {
			t.Fatalf("selector error = %v, want an exit-3 usage error", err)
		}
	}
	filter, err := dbCleanupRequestsFilter(dbCleanupRequestsOptions{statuses: []string{"404,500", "502"}})
	if err != nil {
		t.Fatalf("dbCleanupRequestsFilter(statuses) error = %v", err)
	}
	if len(filter.StatusCodes) != 3 || filter.StatusCodes[0] != 404 || filter.StatusCodes[2] != 502 {
		t.Fatalf("status codes = %v, want [404 500 502]", filter.StatusCodes)
	}
	if _, err := dbCleanupRequestsFilter(dbCleanupRequestsOptions{statuses: []string{"99"}}); err == nil {
		t.Fatal("dbCleanupRequestsFilter(status 99) = nil error, want usage error")
	}
	if _, err := dbCleanupRequestsFilter(dbCleanupRequestsOptions{statuses: []string{"abc"}}); err == nil {
		t.Fatal("dbCleanupRequestsFilter(status abc) = nil error, want usage error")
	}
	if _, err := dbCleanupRequestsFilter(dbCleanupRequestsOptions{failed: true, limit: -1}); err == nil {
		t.Fatal("dbCleanupRequestsFilter(negative limit) = nil error, want usage error")
	}
	if _, err := dbCleanupRequestsFilter(dbCleanupRequestsOptions{failed: true, since: "not-a-time"}); err == nil {
		t.Fatal("dbCleanupRequestsFilter(bad since) = nil error, want usage error")
	}
	if _, err := dbCleanupRequestsFilter(dbCleanupRequestsOptions{statuses: []string{"404"}, until: "2026-01-01T00:00:00Z", since: "2026-02-01T00:00:00Z"}); err == nil {
		t.Fatal("dbCleanupRequestsFilter(until before since) = nil error, want usage error")
	}
}

func TestCleanupCassetteFilesRejectsUnsafePaths(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "inside.http")
	if err := os.WriteFile(inside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "outside.http")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	notHTTP := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(notHTTP, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	result := cleanupCassetteFiles(root, []string{inside, outside, notHTTP}, false)
	if result.deleted != 1 || result.freedBytes == 0 {
		t.Fatalf("delete result = %+v, want one deleted file", result)
	}
	if len(result.skipped) != 2 {
		t.Fatalf("skipped = %v, want the outside path and the non-.http file", result.skipped)
	}
	if fileExists(inside) {
		t.Fatal("the in-tree cassette survived")
	}
	if !fileExists(outside) || !fileExists(notHTTP) {
		t.Fatal("cleanup deleted a path outside the trace output dir or a non-cassette file")
	}
}

func TestDBCleanupRequestsCassetteFailureWritesOneEnvelope(t *testing.T) {
	dir := t.TempDir()
	configPath := writeDBCleanupCLIConfig(t, dir)
	dbPath := filepath.Join(dir, "trace_index.sqlite3")
	st, err := store.NewWithDatabase(dir, "sqlite", dbPath, 1, 1)
	if err != nil {
		t.Fatalf("store.NewWithDatabase() error = %v", err)
	}
	badPath := writeDBCleanupCLILog(t, st, dir, "cleanup-fail-404.http", "sess-fail", http.StatusNotFound, time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC))
	if err := st.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	// Replace the cassette with a non-empty directory: os.Remove cannot delete it,
	// so the applied run has to report a cassette failure.
	if err := os.Remove(badPath); err != nil {
		t.Fatalf("remove cassette: %v", err)
	}
	if err := os.Mkdir(badPath, 0o755); err != nil {
		t.Fatalf("mkdir cassette: %v", err)
	}
	if err := os.WriteFile(filepath.Join(badPath, "child"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write child: %v", err)
	}

	var out bytes.Buffer
	code, wrote := runDBCleanupRequestsWithOptions(dbCleanupRequestsOptions{
		configPath: configPath,
		format:     "json",
		stdout:     &out,
		apply:      true,
	}, store.RequestCleanupFilter{StatusCodes: []int{http.StatusNotFound}})
	if code != 1 || !wrote {
		t.Fatalf("code=%d wrote=%v, want 1 and true", code, wrote)
	}
	var envelope Envelope
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
		t.Fatalf("json.Unmarshal() error = %v; output=%s", err, out.String())
	}
	if envelope.OK {
		t.Fatal("envelope ok=true despite a cassette failure")
	}
	if len(envelope.Warnings) == 0 {
		t.Fatal("no warning about the cassette failure")
	}
	if count := bytes.Count(out.Bytes(), []byte(`"ok"`)); count != 1 {
		t.Fatalf("json documents = %d, want exactly one; output=%s", count, out.String())
	}
	// The database delete still committed: index rows are removed before the
	// filesystem step, and a failed file removal must not roll it back.
	if got := dbCleanupConfigLogCount(t, dbPath); got != 0 {
		t.Fatalf("logs = %d, want 0", got)
	}
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
