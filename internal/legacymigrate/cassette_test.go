package legacymigrate

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// cassetteParts returns a realistic payload: CRLF HTTP header blocks and a
// JSON body.
func cassetteParts() (reqHeader, reqBody, resHeader, resBody []byte) {
	reqHeader = []byte("GET /v1/models HTTP/1.1\r\nHost: example.com\r\nContent-Type: application/json\r\nAccept: */*\r\n\r\n")
	resHeader = []byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nX-Request-Id: abc\r\n\r\n")
	resBody = []byte(`{"object":"list","data":[{"id":"gpt-x"}]}`)
	return
}

// buildV3Cassette renders a structurally valid V3 cassette carrying the given
// magic line.
func buildV3Cassette(t *testing.T, magic string) []byte {
	t.Helper()
	_, _, _, resBody := cassetteParts()
	return buildV3CassetteWithBody(t, magic, resBody)
}

// buildV3CassetteWithBody renders a structurally valid V3 cassette with an
// explicit response body, so the declared layout always matches the file size.
func buildV3CassetteWithBody(t *testing.T, magic string, resBody []byte) []byte {
	t.Helper()
	reqHeader, reqBody, resHeader, _ := cassetteParts()
	header := recordfile.RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta: recordfile.MetaData{
			RequestID:  "req-test",
			Time:       time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC),
			Model:      "gpt-x",
			URL:        "http://example.com/v1/models",
			Method:     "GET",
			StatusCode: 200,
			DurationMs: 12,
			ClientIP:   "127.0.0.1",
		},
		Layout: recordfile.LayoutInfo{
			ReqHeaderLen: int64(len(reqHeader)),
			ReqBodyLen:   int64(len(reqBody)),
			ResHeaderLen: int64(len(resHeader)),
			ResBodyLen:   int64(len(resBody)),
		},
		Usage: recordfile.UsageInfo{PromptTokens: 1, CompletionTokens: 2, TotalTokens: 3},
	}
	prelude, err := recordfile.MarshalPrelude(header, nil)
	if err != nil {
		t.Fatalf("MarshalPrelude() error = %v", err)
	}
	payload := make([]byte, 0, len(reqHeader)+len(reqBody)+len(resHeader)+len(resBody)+1)
	payload = append(payload, reqHeader...)
	payload = append(payload, reqBody...)
	payload = append(payload, '\n')
	payload = append(payload, resHeader...)
	payload = append(payload, resBody...)

	lineEnd := bytes.IndexByte(prelude, '\n')
	if lineEnd < 0 {
		t.Fatal("MarshalPrelude() produced no first line")
	}
	out := append([]byte(magic), prelude[lineEnd:]...)
	return append(out, payload...)
}

func writeCassette(t *testing.T, dir, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", name, err)
	}
	return path
}

func TestRewriteCassetteMagicRewritesLegacyMagicOnly(t *testing.T) {
	dir := t.TempDir()
	legacyPath := writeCassette(t, dir, "legacy.http", buildV3Cassette(t, recordfile.LegacyFileMagic))
	currentPath := writeCassette(t, dir, "current.http", buildV3Cassette(t, recordfile.FileMagic))
	untouchedPath := writeCassette(t, dir, "notes.txt", []byte("not a cassette\n"))
	otherPath := writeCassette(t, dir, "other.http", []byte("GET / HTTP/1.1\r\n\r\n"))

	modTime := time.Date(2025, 6, 1, 2, 3, 4, 0, time.UTC)
	if err := os.Chtimes(legacyPath, modTime, modTime); err != nil {
		t.Fatalf("Chtimes() error = %v", err)
	}
	legacyBefore, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	currentBefore, err := os.ReadFile(currentPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	report, err := RewriteCassetteMagic(context.Background(), MagicRewriteOptions{
		Root: dir, Workers: 4, VerifyAfter: true,
	})
	if err != nil {
		t.Fatalf("RewriteCassetteMagic() error = %v", err)
	}
	if report.Scanned != 3 {
		t.Errorf("Scanned = %d, want 3 (only .http files)", report.Scanned)
	}
	if report.Rewritten != 1 {
		t.Errorf("Rewritten = %d, want 1", report.Rewritten)
	}
	if report.Current != 1 {
		t.Errorf("Current = %d, want 1", report.Current)
	}
	if report.Other != 1 {
		t.Errorf("Other = %d, want 1", report.Other)
	}
	if report.Errors != 0 {
		t.Fatalf("Errors = %d (%v), want 0", report.Errors, report.Failures)
	}
	if report.BytesCopied != int64(len(legacyBefore)) {
		t.Errorf("BytesCopied = %d, want %d", report.BytesCopied, len(legacyBefore))
	}
	if report.DryRun {
		t.Error("DryRun = true, want false")
	}

	legacyAfter, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatalf("ReadFile(legacy) error = %v", err)
	}
	if !bytes.HasPrefix(legacyAfter, []byte(recordfile.FileMagic+"\n")) {
		t.Errorf("legacy cassette still starts with %q", firstLineOf(legacyAfter))
	}
	want := buildV3Cassette(t, recordfile.FileMagic)
	if !bytes.Equal(legacyAfter, want) {
		t.Errorf("rewritten cassette differs from a freshly written one (%d vs %d bytes)", len(legacyAfter), len(want))
	}
	if len(legacyBefore)-len(legacyAfter) != len(recordfile.LegacyFileMagic)-len(recordfile.FileMagic) {
		t.Errorf("size delta = %d, want %d", len(legacyBefore)-len(legacyAfter),
			len(recordfile.LegacyFileMagic)-len(recordfile.FileMagic))
	}

	info, err := os.Stat(legacyPath)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if !info.ModTime().Equal(modTime) {
		t.Errorf("modification time = %s, want %s", info.ModTime(), modTime)
	}
	if got := info.Mode().Perm(); got != 0o644 {
		t.Errorf("permission = %o, want 644", got)
	}

	currentAfter, err := os.ReadFile(currentPath)
	if err != nil {
		t.Fatalf("ReadFile(current) error = %v", err)
	}
	if !bytes.Equal(currentBefore, currentAfter) {
		t.Error("a cassette that already uses the current magic was modified")
	}
	if content, err := os.ReadFile(untouchedPath); err != nil || string(content) != "not a cassette\n" {
		t.Errorf("a non-.http file was modified: %q (%v)", content, err)
	}
	if content, err := os.ReadFile(otherPath); err != nil || !bytes.Equal(content, []byte("GET / HTTP/1.1\r\n\r\n")) {
		t.Errorf("an unrecognized .http file was modified: %q (%v)", content, err)
	}

	leftovers, err := filepath.Glob(filepath.Join(dir, tempCassettePrefix+"*"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(leftovers) != 0 {
		t.Errorf("temporary files left behind: %v", leftovers)
	}
}

func TestRewriteCassetteMagicDryRun(t *testing.T) {
	dir := t.TempDir()
	path := writeCassette(t, dir, "legacy.http", buildV3Cassette(t, recordfile.LegacyFileMagic))
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	report, err := RewriteCassetteMagic(context.Background(), MagicRewriteOptions{Root: dir, DryRun: true})
	if err != nil {
		t.Fatalf("RewriteCassetteMagic() error = %v", err)
	}
	if report.Rewritten != 1 || !report.DryRun {
		t.Errorf("report = %+v, want one planned rewrite in dry run mode", report)
	}
	if report.BytesCopied != 0 {
		t.Errorf("BytesCopied = %d, want 0 in a dry run", report.BytesCopied)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Error("a dry run modified the cassette")
	}
}

func TestRewriteCassetteMagicKeepsCRLFPrelude(t *testing.T) {
	dir := t.TempDir()
	cassette := buildV3Cassette(t, recordfile.LegacyFileMagic)
	// Rewrite every prelude line ending to CRLF; the payload already uses CRLF.
	lineEnd := bytes.Index(cassette, []byte("\n\n"))
	if lineEnd < 0 {
		t.Fatal("fixture has no prelude terminator")
	}
	prelude := bytes.ReplaceAll(cassette[:lineEnd+2], []byte("\n"), []byte("\r\n"))
	crlf := append(prelude, cassette[lineEnd+2:]...)
	path := writeCassette(t, dir, "crlf.http", crlf)

	report, err := RewriteCassetteMagic(context.Background(), MagicRewriteOptions{Root: dir, VerifyAfter: true})
	if err != nil {
		t.Fatalf("RewriteCassetteMagic() error = %v", err)
	}
	if report.Rewritten != 1 || report.Errors != 0 {
		t.Fatalf("report = %+v, want one rewrite and no errors", report)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !bytes.HasPrefix(after, []byte(recordfile.FileMagic+"\r\n")) {
		t.Errorf("CRLF prelude lost its carriage return: %q", firstLineOf(after))
	}
	expected := append([]byte(recordfile.FileMagic+"\r\n"), crlf[len(recordfile.LegacyFileMagic)+2:]...)
	if !bytes.Equal(after, expected) {
		t.Error("the CRLF cassette changed beyond its magic line")
	}
}

func TestRewriteCassetteMagicMissingRoot(t *testing.T) {
	if _, err := RewriteCassetteMagic(context.Background(), MagicRewriteOptions{Root: filepath.Join(t.TempDir(), "absent")}); err == nil {
		t.Fatal("RewriteCassetteMagic() error = nil, want an error for a missing root")
	}
	if _, err := RewriteCassetteMagic(context.Background(), MagicRewriteOptions{}); err == nil {
		t.Fatal("RewriteCassetteMagic() error = nil, want an error for an empty root")
	}
}

func TestCleanupStaleTempFiles(t *testing.T) {
	dir := t.TempDir()
	stale := writeCassette(t, dir, tempCassettePrefix+"1234.tmp", []byte("partial"))
	keep := writeCassette(t, dir, "keep.http", buildV3Cassette(t, recordfile.FileMagic))
	keepTemp := writeCassette(t, dir, "unrelated.tmp", []byte("x"))

	removed, err := CleanupStaleTempFiles(context.Background(), dir)
	if err != nil {
		t.Fatalf("CleanupStaleTempFiles() error = %v", err)
	}
	if removed != 1 {
		t.Errorf("removed = %d, want 1", removed)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("stale temporary file still exists (err = %v)", err)
	}
	for _, path := range []string{keep, keepTemp} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("CleanupStaleTempFiles() removed %s: %v", path, err)
		}
	}
}

func TestArchiveSQLiteDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "llm_tracelab.sqlite3")
	if err := os.WriteFile(dbPath, []byte("sqlite"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	journal := dbPath + "-journal"
	if err := os.WriteFile(journal, []byte("journal"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	moved, err := ArchiveSQLiteDatabase(dbPath, ".migrated")
	if err != nil {
		t.Fatalf("ArchiveSQLiteDatabase() error = %v", err)
	}
	if len(moved) != 2 {
		t.Errorf("moved = %v, want the database and its journal", moved)
	}
	for _, path := range []string{dbPath + ".migrated", dbPath + ".migrated-journal"} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("archived file %s is missing: %v", path, err)
		}
	}
	if _, err := os.Stat(dbPath); !os.IsNotExist(err) {
		t.Errorf("the original database still exists (err = %v)", err)
	}

	// A second archive must not overwrite the first one.
	if err := os.WriteFile(dbPath, []byte("sqlite2"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	moved, err = ArchiveSQLiteDatabase(dbPath, ".migrated")
	if err != nil {
		t.Fatalf("ArchiveSQLiteDatabase(second) error = %v", err)
	}
	if len(moved) != 1 {
		t.Fatalf("moved = %v, want a single rename", moved)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir() error = %v", err)
	}
	archived := 0
	for _, entry := range entries {
		if bytes.HasPrefix([]byte(entry.Name()), []byte("llm_tracelab.sqlite3.migrated")) {
			archived++
		}
	}
	if archived != 3 {
		t.Errorf("archived files = %d, want 3 (two databases plus one journal)", archived)
	}

	if _, err := ArchiveSQLiteDatabase(filepath.Join(dir, "absent.sqlite3"), ".migrated"); err == nil {
		t.Error("ArchiveSQLiteDatabase(absent) error = nil, want an error")
	}
	if _, err := ArchiveSQLiteDatabase("", ".migrated"); err == nil {
		t.Error("ArchiveSQLiteDatabase(\"\") error = nil, want an error")
	}
}

func firstLineOf(content []byte) string {
	if idx := bytes.IndexByte(content, '\n'); idx >= 0 {
		return string(content[:idx])
	}
	return string(content)
}
