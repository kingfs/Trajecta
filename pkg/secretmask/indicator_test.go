package secretmask

import (
	"bytes"
	"strings"
	"testing"
)

// indicatorValues returns the matched bytes of every access indicator in src.
func indicatorValues(src string) []string {
	var out []string
	for _, f := range Indicators([]byte(src)) {
		out = append(out, string(f.Bytes([]byte(src))))
	}
	return out
}

// TestIndicatorsDetectURLsAndPaths pins the report-only detection contract: the
// machine finds the shape, the candidate checks decide whether it is a URL or a
// local path rather than an API route, a MIME type or a bare domain, and the
// sentence punctuation around a value is not part of it.
func TestIndicatorsDetectURLsAndPaths(t *testing.T) {
	cases := []struct {
		name string
		text string
		want []string
	}{
		{
			name: "https url",
			text: "fetched https://api.github.com/repos/example/project?page=2 and stopped",
			want: []string{"https://api.github.com/repos/example/project?page=2"},
		},
		{
			name: "http url with port",
			text: "curl http://localhost:8080/v1/health",
			want: []string{"http://localhost:8080/v1/health"},
		},
		{
			name: "ws url",
			text: "connect wss://gateway.example.com/socket",
			want: []string{"wss://gateway.example.com/socket"},
		},
		{
			name: "url in json",
			text: `{"url":"https://example.com/a/b","method":"GET"}`,
			want: []string{"https://example.com/a/b"},
		},
		{
			name: "url keeps userinfo and fragment",
			text: "https://user@example.com/path#section-2",
			want: []string{"https://user@example.com/path#section-2"},
		},
		{
			name: "sentence period is not part of the url",
			text: "see https://example.com/docs.",
			want: []string{"https://example.com/docs"},
		},
		{
			name: "markdown closer is not part of the url",
			text: "[docs](https://example.com/docs)",
			want: []string{"https://example.com/docs"},
		},
		{
			// A balanced bracket belongs to the URL; only the markdown closer
			// around it is stripped.
			name: "balanced parentheses stay in the url",
			text: "[wiki](https://en.wikipedia.org/wiki/Foo_(bar))",
			want: []string{"https://en.wikipedia.org/wiki/Foo_(bar)"},
		},
		{
			name: "unbalanced closing bracket is stripped",
			text: "see (https://example.com/a).",
			want: []string{"https://example.com/a"},
		},
		{
			name: "posix absolute path",
			text: "read /home/alice/project/main.go",
			want: []string{"/home/alice/project/main.go"},
		},
		{
			name: "posix directory without extension",
			text: "ls /project/src",
			want: []string{"/project/src"},
		},
		{
			name: "single known root",
			text: "cat /etc then /var/log/syslog",
			want: []string{"/etc", "/var/log/syslog"},
		},
		{
			name: "doubled sentence punctuation",
			text: "wrote to /tmp/out.json, then.",
			want: []string{"/tmp/out.json"},
		},
		{
			name: "home relative",
			text: "cat ~/notes/todo.md",
			want: []string{"~/notes/todo.md"},
		},
		{
			name: "dot relative",
			text: "run ./scripts/build.sh and ../README.md",
			want: []string{"./scripts/build.sh", "../README.md"},
		},
		{
			name: "relative file path",
			text: "changed src/main.go and internal/store/store.go",
			want: []string{"src/main.go", "internal/store/store.go"},
		},
		{
			name: "relative dot directory",
			text: "read .git/config",
			want: []string{".git/config"},
		},
		{
			name: "windows backslash path",
			text: `open C:\Users\alice\project\main.go`,
			want: []string{`C:\Users\alice\project\main.go`},
		},
		{
			name: "windows forward slash path",
			text: "open C:/Users/alice/main.go",
			want: []string{"C:/Users/alice/main.go"},
		},
		{
			name: "unc path",
			text: `open \\fileserver\share\dir\report.txt`,
			want: []string{`\\fileserver\share\dir\report.txt`},
		},
		{
			// Tool-call arguments are JSON text, so a real backslash arrives
			// doubled. The machine accepts both forms and the analyzer
			// normalizes the escaped one before recording it.
			name: "json escaped windows path",
			text: `{"file_path":"C:\\Users\\alice\\project\\main.go"}`,
			want: []string{`C:\\Users\\alice\\project\\main.go`},
		},
		{
			name: "quoted list is split into paths",
			text: `'explorer/a.npz','explorer/b.npz`,
			want: []string{"explorer/a.npz", "explorer/b.npz"},
		},
		{
			name: "json escaped unc path",
			text: `{"path":"\\\\fileserver\\share\\dir\\report.txt"}`,
			want: []string{`\\\\fileserver\\share\\dir\\report.txt`},
		},
		{
			name: "json escaped path",
			text: `{"arguments":"{\"file_path\":\"/home/alice/.env\"}"}`,
			want: []string{"/home/alice/.env"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := indicatorValues(tc.text)
			if len(got) != len(tc.want) {
				t.Fatalf("indicators = %q, want %q", got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("indicator %d = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestIndicatorsIgnoreNonLocalSlashText keeps the precision claim honest. These
// are the shapes a transcript is full of that look like a path or a URL but are
// an API route, a MIME type, an import path, a domain or prose.
func TestIndicatorsIgnoreNonLocalSlashText(t *testing.T) {
	cases := []string{
		"/v1/chat/completions",
		"/api/v1/models",
		"POST /v1/responses",
		"https://",                       // no host
		"http://.",                       // punctuation is not a host
		"application/vnd.api+json",       // MIME subtype, not a file
		"application/json",               // no extension
		"net/http",                       // import path
		"and/or",                         // prose
		"2024/01/summary",                // numeric date path without extension
		"example.com/path",               // domain-relative reference
		"foo.bar/baz",                    // domain first segment
		"README.md",                      // bare name has no path evidence
		"C:\\",                           // a drive with no path under it
		"#/definitions/Request",          // JSON pointer, not a filesystem path
		`$ref: "#/components/schemas/X"`, // the same, inside a schema
	}

	for _, text := range cases {
		if got := indicatorValues(text); len(got) != 0 {
			t.Errorf("%q produced indicators %q, want none", text, got)
		}
	}
}

// TestCredentialScanIgnoresAccessIndicators is the other half of the split: the
// credential machine must not start reporting paths and URLs, because a
// credential finding is scored and may be rewritten.
func TestCredentialScanIgnoresAccessIndicators(t *testing.T) {
	text := `GET https://api.example.com/repos/x?page=2 from C:\Users\alice\project and /home/alice/src`
	if got := Scan([]byte(text)); len(got) != 0 {
		t.Fatalf("credential scan reported %v, want none", got)
	}
}

// TestMaskStillRewritesACredentialInsideAURL is why the indicators live in a
// second machine. A Ragel scanner consumes the bytes it matched, so a URL rule
// in the credential machine would swallow a key in its query string and the
// masker would never see it. This test pins both halves: the URL is detected by
// the indicator machine, and the key inside it is still replaced on the wire.
func TestMaskStillRewritesACredentialInsideAURL(t *testing.T) {
	key := "sk-" + strings.Repeat("A", 20) + "T3BlbkFJ" + strings.Repeat("B", 20)
	body := []byte("GET https://api.example.com/v1/chat?key=" + key + " HTTP/1.1\n")

	if got := indicatorValues(string(body)); len(got) != 1 || !strings.HasPrefix(got[0], "https://api.example.com/v1/chat?key=") {
		t.Fatalf("indicators = %q, want the full URL", got)
	}

	found := testVault().det.Scan(body)
	if len(found) != 1 || found[0].ID != RuleOpenAILegacy {
		t.Fatalf("credential scan = %+v, want one openai.legacy_key", found)
	}

	masked, events := testVault().Mask(body)
	if len(events) != 1 {
		t.Fatalf("Mask events = %d, want 1", len(events))
	}
	if bytes.Contains(masked, []byte(key)) {
		t.Fatalf("the key inside the URL was not rewritten: %q", masked)
	}
	if !bytes.Contains(masked, []byte("https://api.example.com/v1/chat?key=")) {
		t.Fatalf("Mask changed the URL around the key: %q", masked)
	}
}

// TestReportOnlyFindingsAreNotMaskable keeps the rewrite guard independent of
// the score: even a finding with a conclusive score must be refused when its
// rule says the value is audited rather than replaced.
func TestReportOnlyFindingsAreNotMaskable(t *testing.T) {
	for _, id := range []RuleID{RuleURL, RuleLocalPath} {
		f := Finding{Span: Span{Start: 0, End: 8, ID: id}, Score: 100, Confidence: ConfidenceHigh}
		if f.Maskable() {
			t.Errorf("%s is maskable, want report-only", ruleTable[id].Name)
		}
	}
}

// TestPositiveSamplesDetectAsIndicators keeps the metadata sample and the
// machine in step for the report-only rules, the way TestStrengthCoverage does
// for the credential tiers.
func TestPositiveSamplesDetectAsIndicators(t *testing.T) {
	for _, tc := range positiveSamples {
		if !ruleTable[tc.rule].ReportOnly {
			continue
		}
		got := indicatorValues(tc.text)
		if len(got) != 1 || got[0] != tc.text {
			t.Errorf("%s sample %q produced %q", ruleTable[tc.rule].Name, tc.text, got)
		}
	}
}

// TestIndicatorSpansDoNotOverlap is the scanner invariant for the second
// machine: it consumes what it matched, so a buffer of mixed indicators yields
// disjoint spans.
func TestIndicatorSpansDoNotOverlap(t *testing.T) {
	text := "GET https://a.example.com/x /home/alice/p.go C:\\Users\\a\\p.txt src/main.go https://b.example.com/y"
	spans := Indicators([]byte(text))
	if len(spans) < 4 {
		t.Fatalf("spans = %d, want at least 4: %v", len(spans), spans)
	}
	for i := 1; i < len(spans); i++ {
		if spans[i].Start < spans[i-1].End {
			t.Fatalf("span %d %+v overlaps previous %+v", i, spans[i], spans[i-1])
		}
	}
}

// TestIndicatorsRejectEscapeDebris pins the false positives a real transcript
// produced. Each value is either the raw text as it arrived (doubled backslashes
// from JSON) or the value the index stored after normalizeValue collapsed the
// pair, so both forms are covered.
func TestIndicatorsRejectEscapeDebris(t *testing.T) {
	cases := []struct {
		name string
		text string
	}{
		// JSON/Go escape sequences read as a UNC share after the index
		// normalizes the doubled separator.
		{name: "escaped newline newline", text: `\\n\\n`},
		{name: "escaped tab tab", text: `\\n\\t\\t`},
		{name: "escaped tab return", text: `\\n\\treturn`},
		{name: "escaped tab id", text: `\\n\\tid`},
		{name: "escaped tab case", text: `\\t\\tcase`},
		{name: "stored newline newline", text: `\n\n`},
		{name: "stored tab return", text: `\n\treturn`},
		{name: "stored tab tab case", text: `\t\tcase`},
		// Regex source, both escaped and stored.
		{name: "escaped regex group", text: `\\d+)\\.(\\d+`},
		{name: "stored regex group", text: `\d+)\.(\d+`},
		{name: "escaped backreference", text: `\\1.\\2`},
		{name: "stored backreference", text: `\1.\2`},
		{name: "escaped regex dot group", text: `\\.(\\d+)`},
		{name: "stored regex dot group", text: `\.(\d+)`},
		// A printf format after a one-letter label looked like a drive path.
		{name: "printf format after label", text: `q:\n%s`},
		{name: "escaped printf format after label", text: `q:\\n%s`},
		{name: "escaped line number after drive", text: `z:\n244`},
		// Surface area with no evidence of an accessed file.
		{name: "bare parent", text: `../`},
		{name: "parent with one segment", text: `../a`},
		{name: "dot with shell metacharacter", text: `./$`},
		{name: "bare dot", text: `./`},
		{name: "home dir only", text: `~/`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := indicatorValues(tc.text); len(got) != 0 {
				t.Fatalf("%q produced indicators %q, want none", tc.text, got)
			}
		})
	}
}

// TestIndicatorsStripProsePrefixes keeps the value that follows an assignment or
// an opening parenthesis. The byte before a path is prose, not part of it: the
// machine must not consume "registry_file=" or "(" in the first place, because a
// candidate check can only drop a value, not rewrite it.
func TestIndicatorsStripProsePrefixes(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{text: `(modules/identity-tenancy.md`, want: "modules/identity-tenancy.md"},
		{text: `registry_file=internal/infrastructure/config/config.go`, want: "internal/infrastructure/config/config.go"},
		{text: `-file=./internal/store/store.go`, want: "./internal/store/store.go"},
		{text: `open(/home/alice/project/main.go)`, want: "/home/alice/project/main.go"},
	}
	for _, tc := range cases {
		got := indicatorValues(tc.text)
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("%q produced %q, want [%q]", tc.text, got, tc.want)
		}
	}
}
