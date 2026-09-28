package legacymigrate

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// timeValue is the timestamp used by the cassette fixtures.
func timeValue() time.Time {
	return time.Date(2026, 1, 2, 3, 4, 5, 123456789, time.UTC)
}

// buildLargeV3Cassette renders a cassette whose payload is far larger than the
// 512 byte block readFirstLine uses and whose body contains lines that look
// like prelude lines. A reader that resumes at the wrong offset fails here.
func buildLargeV3Cassette(t *testing.T) []byte {
	t.Helper()
	_, _, _, resBody := cassetteParts()
	noise := strings.Repeat("# meta: {not json}\n# event: garbage\nContent-Type: application/json\r\n", 120)
	return buildV3CassetteWithBody(t, recordfile.FileMagic, append(resBody, []byte(noise)...))
}

func checkOnce(t *testing.T, dir string, opts CheckOptions) *CheckReport {
	t.Helper()
	opts.Root = dir
	if opts.Workers == 0 {
		opts.Workers = 4
	}
	report, err := CheckCassettes(context.Background(), opts)
	if err != nil {
		t.Fatalf("CheckCassettes() error = %v", err)
	}
	return report
}

func issueCodes(report *CheckReport) []string {
	codes := make([]string, 0, len(report.Issues))
	for _, issue := range report.Issues {
		codes = append(codes, issue.Code)
	}
	return codes
}

func hasCode(report *CheckReport, code string) bool {
	for _, issue := range report.Issues {
		if issue.Code == code {
			return true
		}
	}
	return false
}

func TestCheckCassettesAcceptsValidV3(t *testing.T) {
	dir := t.TempDir()
	writeCassette(t, dir, "current.http", buildV3Cassette(t, recordfile.FileMagic))
	writeCassette(t, dir, "legacy.http", buildV3Cassette(t, recordfile.LegacyFileMagic))
	writeCassette(t, dir, "large.http", buildLargeV3Cassette(t))
	writeCassette(t, dir, "ignored.txt", []byte("not a cassette\n"))

	report := checkOnce(t, dir, CheckOptions{})
	if report.Scanned != 3 {
		t.Fatalf("Scanned = %d, want 3", report.Scanned)
	}
	if report.Errors != 0 {
		t.Fatalf("Errors = %d (%v), want 0", report.Errors, report.Issues)
	}
	if report.Failed() {
		t.Error("Failed() = true, want false")
	}
	if report.Legacy != 1 || report.Current != 2 {
		t.Errorf("Current = %d, Legacy = %d, want 2 and 1", report.Current, report.Legacy)
	}
	if report.OK != 2 {
		t.Errorf("OK = %d, want 2 (the legacy file carries a warning)", report.OK)
	}
	if report.Warnings != 1 || !hasCode(report, "legacy_magic") {
		t.Errorf("Warnings = %d, codes = %v, want a single legacy_magic warning", report.Warnings, issueCodes(report))
	}
}

func TestCheckCassettesAcceptsLargePreludeWithEvents(t *testing.T) {
	dir := t.TempDir()
	reqHeader, reqBody, resHeader, resBody := cassetteParts()
	header := recordfile.RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta: recordfile.MetaData{
			RequestID:  "req-events",
			Time:       timeValue(),
			Model:      "gpt-x",
			URL:        "http://example.com/v1/models",
			Method:     "GET",
			StatusCode: 200,
		},
		Layout: recordfile.LayoutInfo{
			ReqHeaderLen: int64(len(reqHeader)), ReqBodyLen: int64(len(reqBody)),
			ResHeaderLen: int64(len(resHeader)), ResBodyLen: int64(len(resBody)),
		},
	}
	prelude, err := recordfile.MarshalPrelude(header, recordfile.BuildEvents(header))
	if err != nil {
		t.Fatalf("MarshalPrelude() error = %v", err)
	}
	content := append(prelude, reqHeader...)
	content = append(content, reqBody...)
	content = append(content, '\n')
	content = append(content, resHeader...)
	content = append(content, resBody...)
	writeCassette(t, dir, "events.http", content)

	report := checkOnce(t, dir, CheckOptions{})
	if report.Errors != 0 || report.Warnings != 0 {
		t.Fatalf("Errors = %d, Warnings = %d (%v), want a clean file", report.Errors, report.Warnings, report.Issues)
	}
	if report.OK != 1 {
		t.Errorf("OK = %d, want 1", report.OK)
	}
}

func TestCheckCassettesReportsLayoutMismatch(t *testing.T) {
	dir := t.TempDir()
	content := buildV3Cassette(t, recordfile.FileMagic)
	writeCassette(t, dir, "truncated.http", content[:len(content)-4])

	report := checkOnce(t, dir, CheckOptions{})
	if report.Errors != 1 {
		t.Fatalf("Errors = %d (%v), want 1", report.Errors, report.Issues)
	}
	if !hasCode(report, "layout_mismatch") {
		t.Errorf("codes = %v, want layout_mismatch", issueCodes(report))
	}
	if report.Failed() != true {
		t.Error("Failed() = false, want true")
	}

	tolerant := checkOnce(t, dir, CheckOptions{ToleratePartial: true})
	if tolerant.Errors != 0 || tolerant.Warnings != 1 {
		t.Errorf("Errors = %d, Warnings = %d, want 0 and 1 with ToleratePartial", tolerant.Errors, tolerant.Warnings)
	}
	if !hasCode(tolerant, "layout_mismatch_partial") {
		t.Errorf("codes = %v, want layout_mismatch_partial", issueCodes(tolerant))
	}
}

func TestCheckCassettesLegacyAndStrictSeverities(t *testing.T) {
	dir := t.TempDir()
	writeCassette(t, dir, "legacy.http", buildV3Cassette(t, recordfile.LegacyFileMagic))

	report := checkOnce(t, dir, CheckOptions{FailOnLegacy: true})
	if report.Errors != 1 || !report.Failed() {
		t.Errorf("Errors = %d, Failed = %v, want 1 and true with FailOnLegacy", report.Errors, report.Failed())
	}

	dir2 := t.TempDir()
	writeCassette(t, dir2, "no_meta.http", []byte(recordfile.FileMagic+"\n# meta: {}\n\npayload"))
	strict := checkOnce(t, dir2, CheckOptions{Strict: true})
	if strict.Errors == 0 || !strict.Failed() {
		t.Errorf("Errors = %d, want the strict mode to escalate warnings", strict.Errors)
	}
	relaxed := checkOnce(t, dir2, CheckOptions{})
	if relaxed.Errors != 0 || relaxed.Warnings == 0 {
		t.Errorf("Errors = %d, Warnings = %d, want warnings only", relaxed.Errors, relaxed.Warnings)
	}
	if !hasCode(relaxed, "missing_request_id") {
		t.Errorf("codes = %v, want missing_request_id", issueCodes(relaxed))
	}
}

func TestCheckCassettesClassifiesLegacyV2AndUnknown(t *testing.T) {
	dir := t.TempDir()
	header := recordfile.RecordHeader{
		Version: "LLM_PROXY_V2",
		Meta:    recordfile.MetaData{RequestID: "req-v2", Time: timeValue()},
		Layout:  recordfile.LayoutInfo{ReqHeaderLen: 10, ResHeaderLen: 10},
	}
	encoded, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	v2 := make([]byte, recordfile.LegacyHeaderLen)
	copy(v2, encoded)
	v2[len(encoded)] = '\n'
	writeCassette(t, dir, "v2.http", v2)
	writeCassette(t, dir, "garbage.http", []byte("GET / HTTP/1.1\r\nHost: x\r\n\r\nbody"))
	writeCassette(t, dir, "empty.http", nil)

	report := checkOnce(t, dir, CheckOptions{})
	if report.V2 != 1 {
		t.Errorf("V2 = %d, want 1", report.V2)
	}
	if !hasCode(report, "legacy_v2") {
		t.Errorf("codes = %v, want legacy_v2", issueCodes(report))
	}
	if !hasCode(report, "unrecognized_prelude") {
		t.Errorf("codes = %v, want unrecognized_prelude", issueCodes(report))
	}
	if !hasCode(report, "empty_file") {
		t.Errorf("codes = %v, want empty_file", issueCodes(report))
	}
	if report.Unknown != 2 {
		t.Errorf("Unknown = %d, want 2", report.Unknown)
	}
	if report.Errors != 1 {
		t.Errorf("Errors = %d, want 1 (only the garbage file)", report.Errors)
	}

	strictV2 := checkOnce(t, dir, CheckOptions{FailOnV2: true})
	if strictV2.Errors != 2 {
		t.Errorf("Errors = %d, want 2 with FailOnV2", strictV2.Errors)
	}
}

func TestCheckCassettesTruncatesIssues(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.http", "b.http", "c.http"} {
		content := buildV3Cassette(t, recordfile.FileMagic)
		writeCassette(t, dir, name, content[:len(content)-4])
	}
	report := checkOnce(t, dir, CheckOptions{MaxIssues: 2})
	if len(report.Issues) != 2 {
		t.Fatalf("Issues = %d, want 2", len(report.Issues))
	}
	if !report.IssuesTruncated {
		t.Error("IssuesTruncated = false, want true")
	}
	if report.Errors != 3 {
		t.Errorf("Errors = %d, want 3", report.Errors)
	}
}

func TestValidateCassetteFileReportsMissingFile(t *testing.T) {
	kind, issues := ValidateCassetteFile(filepath.Join(t.TempDir(), "absent.http"), CheckOptions{})
	if kind != "unknown" {
		t.Errorf("kind = %q, want unknown", kind)
	}
	if len(issues) != 1 || issues[0].Code != "open_failed" || issues[0].Severity != SeverityError {
		t.Errorf("issues = %+v, want one open_failed error", issues)
	}
}

func TestCheckCassettesMissingRoot(t *testing.T) {
	if _, err := CheckCassettes(context.Background(), CheckOptions{Root: filepath.Join(t.TempDir(), "absent")}); err == nil {
		t.Fatal("CheckCassettes() error = nil, want an error for a missing root")
	}
}

func TestCensusCassetteMagic(t *testing.T) {
	dir := t.TempDir()
	writeCassette(t, dir, "current.http", buildV3Cassette(t, recordfile.FileMagic))
	writeCassette(t, dir, "legacy.http", buildV3Cassette(t, recordfile.LegacyFileMagic))
	writeCassette(t, dir, "garbage.http", []byte("GET / HTTP/1.1\r\n\r\n"))
	writeCassette(t, dir, "empty.http", nil)

	report, err := CensusCassetteMagic(context.Background(), MagicCensusOptions{Root: dir, Workers: 4})
	if err != nil {
		t.Fatalf("CensusCassetteMagic() error = %v", err)
	}
	if report.Scanned != 4 {
		t.Errorf("Scanned = %d, want 4", report.Scanned)
	}
	if report.Current != 1 || report.Legacy != 1 {
		t.Errorf("Current = %d, Legacy = %d, want 1 and 1", report.Current, report.Legacy)
	}
	if report.Unknown != 2 {
		t.Errorf("Unknown = %d, want 2 (garbage and empty)", report.Unknown)
	}
	if report.V2 != 0 || report.Errors != 0 {
		t.Errorf("V2 = %d, Errors = %d, want 0 and 0", report.V2, report.Errors)
	}
	if report.TotalBytes <= 0 {
		t.Errorf("TotalBytes = %d, want a positive total", report.TotalBytes)
	}
}

func TestScanComposeEnvPrefix(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "docker-compose.yml")
	compose := `services:
  llm-tracelab:
    image: trajecta:local
    environment:
      LLM_TRACELAB_DATABASE_DRIVER: postgres
      LLM_TRACELAB_DATABASE_DSN: ${LLM_TRACELAB_DATABASE_DSN:-postgres://user:pw@postgres:5432/db}
      TRAJECTA_TOOLS_WEB_SEARCH_ENABLED: "true"
`
	if err := os.WriteFile(path, []byte(compose), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	scan := ScanComposeEnvPrefix(path)
	if !scan.UsesLegacyPrefix() {
		t.Fatal("UsesLegacyPrefix() = false, want true")
	}
	if len(scan.LegacyKeys) != 2 {
		t.Errorf("LegacyKeys = %v, want 2 entries", scan.LegacyKeys)
	}
	if len(scan.CurrentKeys) != 1 || scan.CurrentKeys[0] != "TRAJECTA_TOOLS_WEB_SEARCH_ENABLED" {
		t.Errorf("CurrentKeys = %v, want the TRAJECTA_* variable", scan.CurrentKeys)
	}
	if !strings.Contains(scan.Summary(), "mix") {
		t.Errorf("Summary() = %q, want it to mention mixed prefixes", scan.Summary())
	}

	missing := ScanComposeEnvPrefix(filepath.Join(dir, "absent.yml"))
	if missing.UsesLegacyPrefix() {
		t.Error("UsesLegacyPrefix() = true for a missing file")
	}
	if missing.ReadError == "" {
		t.Error("ReadError is empty for a missing file")
	}
}

func TestMaskSecret(t *testing.T) {
	cases := []struct {
		key   string
		value string
		want  string
	}{
		{"TRAJECTA_DATABASE_DSN", "postgres://user:pw@host:5432/db?sslmode=disable", "postgres://user:***@host:5432/db?sslmode=disable"},
		{"TRAJECTA_DATABASE_DSN", "postgres://host:5432/db", "postgres://host:5432/db"},
		{"LLM_TRACELAB_BOOTSTRAP_API_KEY", "sk-1234567890", "sk-1***90"},
		{"TRAJECTA_BOOTSTRAP_API_KEY", "short", "***"},
		{"TRAJECTA_TRACE_OUTPUT_DIR", "/app/data/traces", "/app/data/traces"},
		{"TRAJECTA_SECRET", "", ""},
	}
	for _, tc := range cases {
		if got := MaskSecret(tc.key, tc.value); got != tc.want {
			t.Errorf("MaskSecret(%q, %q) = %q, want %q", tc.key, tc.value, got, tc.want)
		}
	}
}
