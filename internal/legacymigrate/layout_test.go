package legacymigrate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// writeLayoutCassette writes a structurally valid V3 cassette with an explicit
// prelude magic and model name.
func writeLayoutCassette(t *testing.T, root, rel, magic, model string) string {
	t.Helper()
	reqHeader, reqBody, resHeader := []byte("POST /v1/responses HTTP/1.1\r\nHost: example.com\r\n\r\n"), []byte("{}"), []byte("HTTP/1.1 200 OK\r\n\r\n")
	resBody := []byte("{}")
	header := recordfile.RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta: recordfile.MetaData{
			RequestID:  "req-layout",
			Time:       time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
			Model:      model,
			URL:        "/v1/responses",
			Method:     "POST",
			StatusCode: 200,
		},
		Layout: recordfile.LayoutInfo{
			ReqHeaderLen: int64(len(reqHeader)),
			ReqBodyLen:   int64(len(reqBody)),
			ResHeaderLen: int64(len(resHeader)),
			ResBodyLen:   int64(len(resBody)),
		},
	}
	prelude, err := recordfile.MarshalPrelude(header, nil)
	if err != nil {
		t.Fatalf("MarshalPrelude() error = %v", err)
	}
	lineEnd := strings.IndexByte(string(prelude), '\n')
	if lineEnd < 0 {
		t.Fatal("MarshalPrelude() produced no first line")
	}
	content := append([]byte(magic+"\n"), prelude[lineEnd+1:]...)
	content = append(content, reqHeader...)
	content = append(content, reqBody...)
	content = append(content, resHeader...)
	content = append(content, resBody...)

	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%s) error = %v", rel, err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", rel, err)
	}
	return path
}

func TestPlanCassetteLayoutClassifiesRecordedVaultShapes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()

	writeLayoutCassette(t, root, "ai-api-gateway.app.baizhi.cloud/gpt-5.4/2026/04/16/a.http", recordfile.FileMagic, "gpt-5.4")
	writeLayoutCassette(t, root, "ai-api-gateway.app.baizhi.cloud/feature/gpt-5.6-sol/2026/04/16/b.http", recordfile.FileMagic, "feature/gpt-5.6-sol")
	writeLayoutCassette(t, root, "deepseek-chat/2026/04/17/c.http", recordfile.LegacyFileMagic, "deepseek-chat")
	writeLayoutCassette(t, root, "feature/gpt-5.6-luna/2026/04/17/d.http", recordfile.LegacyFileMagic, "feature/gpt-5.6-luna")
	writeLayoutCassette(t, root, "feature/2026/04/17/e.http", recordfile.LegacyFileMagic, "feature/gpt-5.6-luna")
	writeLayoutCassette(t, root, "gw/2026/04/17/f.http", recordfile.LegacyFileMagic, "deepseek-flash")
	if err := os.WriteFile(filepath.Join(root, "broken.http"), nil, 0o644); err != nil {
		t.Fatalf("WriteFile(broken.http) error = %v", err)
	}

	report, err := PlanCassetteLayout(context.Background(), LayoutOptions{Root: root, Workers: 2})
	if err != nil {
		t.Fatalf("PlanCassetteLayout() error = %v", err)
	}

	if report.Scanned != 7 {
		t.Fatalf("Scanned = %d, want 7", report.Scanned)
	}
	if report.Canonical != 2 {
		t.Errorf("Canonical = %d, want 2", report.Canonical)
	}
	if report.SiteMissing != 2 {
		t.Errorf("SiteMissing = %d, want 2", report.SiteMissing)
	}
	if report.ModelPrefix != 1 {
		t.Errorf("ModelPrefix = %d, want 1", report.ModelPrefix)
	}
	if report.Ambiguous != 1 {
		t.Errorf("Ambiguous = %d, want 1", report.Ambiguous)
	}
	// broken.http is empty and broken.http is not <site>/<model>/YYYY/MM/DD.
	if report.Unreadable != 1 {
		t.Errorf("Unreadable = %d, want 1", report.Unreadable)
	}
	if !report.Failed() {
		t.Error("Failed() = false, want true for an unreadable cassette")
	}
	if report.LegacyMagic != 4 || report.CurrentMagic != 2 {
		t.Errorf("magic counts = current %d / legacy %d, want 2 / 4", report.CurrentMagic, report.LegacyMagic)
	}
	if report.MovesPlanned != 3 {
		t.Fatalf("MovesPlanned = %d, want 3 (site missing 2 + model prefix 1)", report.MovesPlanned)
	}
	if report.MovesBytes <= 0 {
		t.Errorf("MovesBytes = %d, want > 0", report.MovesBytes)
	}

	want := map[string]string{
		"deepseek-chat/2026/04/17/c.http":        "unknown-site/deepseek-chat/2026/04/17/c.http",
		"feature/gpt-5.6-luna/2026/04/17/d.http": "unknown-site/feature/gpt-5.6-luna/2026/04/17/d.http",
		"feature/2026/04/17/e.http":              "unknown-site/feature/gpt-5.6-luna/2026/04/17/e.http",
	}
	got := map[string]string{}
	for _, move := range report.Moves {
		got[move.From] = move.To
		if move.To == move.From {
			t.Errorf("move %s targets itself", move.From)
		}
	}
	for from, to := range want {
		if got[from] != to {
			t.Errorf("move %s -> %s, want %s", from, got[from], to)
		}
	}
	if len(report.AmbiguousPaths) != 1 || report.AmbiguousPaths[0].From != "gw/2026/04/17/f.http" {
		t.Errorf("AmbiguousPaths = %+v, want only gw/2026/04/17/f.http", report.AmbiguousPaths)
	}
	if report.Root != root {
		t.Errorf("Root = %q, want %q", report.Root, root)
	}
}

// TestPlanCassetteLayoutReadsLongPreludes covers streaming recordings: the
// recorder appends one "# event:" line per chunk, so a long stream produces a
// prelude of several hundred kilobytes.
func TestPlanCassetteLayoutReadsLongPreludes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	path := filepath.Join(root, "site.example", "gpt-5.4", "2026", "01", "02", "long.http")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	header := recordfile.RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta:    recordfile.MetaData{RequestID: "req-long", Model: "gpt-5.4", URL: "/v1/responses"},
	}
	events := make([]recordfile.RecordEvent, 0, 4000)
	for i := 0; i < 4000; i++ {
		events = append(events, recordfile.RecordEvent{Type: "message", Message: strings.Repeat("chunk", 24)})
	}
	prelude, err := recordfile.MarshalPrelude(header, events)
	if err != nil {
		t.Fatalf("MarshalPrelude() error = %v", err)
	}
	if len(prelude) < 512<<10 {
		t.Fatalf("prelude = %d bytes, want at least 512 KiB for this test", len(prelude))
	}
	content := append([]byte(recordfile.LegacyFileMagic+"\n"), prelude[strings.IndexByte(string(prelude), '\n')+1:]...)
	content = append(content, []byte("POST /v1/responses HTTP/1.1\r\n\r\n")...)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	report, err := PlanCassetteLayout(context.Background(), LayoutOptions{Root: root, Workers: 1})
	if err != nil {
		t.Fatalf("PlanCassetteLayout() error = %v", err)
	}
	if report.Unreadable != 0 {
		t.Fatalf("Unreadable = %d (%+v), want 0 for a long but valid prelude", report.Unreadable, report.Failures)
	}
	if report.Canonical != 1 || report.Scanned != 1 {
		t.Fatalf("Canonical = %d Scanned = %d, want 1 and 1", report.Canonical, report.Scanned)
	}
}

func TestPlanCassetteLayoutHonoursUnknownSiteAndKeepMoves(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeLayoutCassette(t, root, "deepseek-chat/2026/04/17/c.http", recordfile.LegacyFileMagic, "deepseek-chat")
	writeLayoutCassette(t, root, "gpt-5.5/2026/04/17/d.http", recordfile.LegacyFileMagic, "gpt-5.5")

	report, err := PlanCassetteLayout(context.Background(), LayoutOptions{
		Root:        root,
		Workers:     1,
		UnknownSite: "_no-site",
		KeepMoves:   1,
	})
	if err != nil {
		t.Fatalf("PlanCassetteLayout() error = %v", err)
	}
	if report.UnknownSite != "_no-site" {
		t.Errorf("UnknownSite = %q, want _no-site", report.UnknownSite)
	}
	if report.MovesPlanned != 2 {
		t.Errorf("MovesPlanned = %d, want 2", report.MovesPlanned)
	}
	if len(report.Moves) != 1 || !report.MovesTruncated {
		t.Fatalf("Moves = %d truncated=%v, want 1 truncated", len(report.Moves), report.MovesTruncated)
	}
	if !strings.HasPrefix(report.Moves[0].To, "_no-site/") {
		t.Errorf("move target = %q, want the _no-site prefix", report.Moves[0].To)
	}
}

func TestPlanCassetteLayoutRejectsBadRoots(t *testing.T) {
	t.Parallel()
	if _, err := PlanCassetteLayout(context.Background(), LayoutOptions{}); err == nil {
		t.Error("PlanCassetteLayout() with an empty root = nil error, want failure")
	}
	file := filepath.Join(t.TempDir(), "cassette.http")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := PlanCassetteLayout(context.Background(), LayoutOptions{Root: file}); err == nil {
		t.Error("PlanCassetteLayout() with a file root = nil error, want failure")
	}
	if _, err := PlanCassetteLayout(context.Background(), LayoutOptions{Root: t.TempDir(), UnknownSite: "a/b"}); err == nil {
		t.Error("PlanCassetteLayout() with a nested unknown site = nil error, want failure")
	}
}

func TestDecideLayout(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		rel      string
		model    string
		want     string
		wantTo   string
		wantSite string
	}{
		{
			name: "canonical host and model", rel: "site.example/gpt-5.4/2026/04/16/a.http",
			model: "gpt-5.4", want: LayoutCanonical, wantTo: "site.example/gpt-5.4/2026/04/16/a.http", wantSite: "site.example",
		},
		{
			name: "canonical with a slash in the model", rel: "site.example/feature/gpt-5.6-sol/2026/04/16/a.http",
			model: "feature/gpt-5.6-sol", want: LayoutCanonical, wantTo: "site.example/feature/gpt-5.6-sol/2026/04/16/a.http", wantSite: "site.example",
		},
		{
			name: "host with a port", rel: "10.2.69.245:32080/dev/gpt-5.5/2026/04/16/a.http",
			model: "dev/gpt-5.5", want: LayoutCanonical, wantTo: "10.2.69.245:32080/dev/gpt-5.5/2026/04/16/a.http", wantSite: "10.2.69.245:32080",
		},
		{
			name: "no site segment", rel: "deepseek-chat/2026/04/17/c.http",
			model: "deepseek-chat", want: LayoutSiteMissing, wantTo: "unknown-site/deepseek-chat/2026/04/17/c.http",
		},
		{
			name: "no site segment with a slash in the model", rel: "feature/gpt-5.6-luna/2026/04/17/d.http",
			model: "feature/gpt-5.6-luna", want: LayoutSiteMissing, wantTo: "unknown-site/feature/gpt-5.6-luna/2026/04/17/d.http",
		},
		{
			name: "model truncated to its first segment", rel: "feature/2026/04/17/e.http",
			model: "feature/gpt-5.6-luna", want: LayoutModelPrefix, wantTo: "unknown-site/feature/gpt-5.6-luna/2026/04/17/e.http",
		},
		{
			name: "site without the model segment", rel: "gw/2026/04/17/f.http",
			model: "deepseek-flash", want: LayoutAmbiguous,
		},
		{
			name: "recorded model with a trailing separator", rel: "gw/2026/04/17/f.http",
			model: "gw/", want: LayoutSiteMissing, wantTo: "unknown-site/gw/2026/04/17/f.http",
		},
		{
			name: "recorded model that would escape the root", rel: "site.example/2026/04/17/f.http",
			model: "../../etc", want: LayoutUnreadable,
		},
		{
			name: "no date tail", rel: "site.example/gpt-5.4/a.http",
			model: "gpt-5.4", want: LayoutUnreadable,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decision, move, err := decideLayout(tc.rel, tc.model, DefaultUnknownSite)
			if tc.want == LayoutUnreadable {
				if err == nil {
					t.Fatalf("decideLayout(%q) error = nil, want a failure", tc.rel)
				}
				return
			}
			if err != nil {
				t.Fatalf("decideLayout(%q) error = %v", tc.rel, err)
			}
			if decision != tc.want {
				t.Errorf("decision = %q, want %q", decision, tc.want)
			}
			if tc.wantTo != "" && move.To != tc.wantTo {
				t.Errorf("To = %q, want %q", move.To, tc.wantTo)
			}
			if tc.wantSite != "" && move.Site != tc.wantSite {
				t.Errorf("Site = %q, want %q", move.Site, tc.wantSite)
			}
		})
	}
}

func TestFirstLineBytes(t *testing.T) {
	t.Parallel()
	if got := string(firstLineBytes([]byte("a\nb"))); got != "a\n" {
		t.Errorf("firstLineBytes = %q, want %q", got, "a\n")
	}
	if got := string(firstLineBytes([]byte("a"))); got != "a" {
		t.Errorf("firstLineBytes without a terminator = %q, want %q", got, "a")
	}
}
