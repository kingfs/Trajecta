package legacymigrate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// defaultMaxPreludeBytes bounds how much of a cassette the structural checker
// is willing to read. The payload is never read.
const defaultMaxPreludeBytes = 1 << 20

// Issue severities.
const (
	SeverityError   = "error"
	SeverityWarning = "warning"
)

// Issue is one structural problem found in a cassette.
type Issue struct {
	Path     string `json:"path"`
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
}

// CheckOptions configures CheckCassettes.
type CheckOptions struct {
	Root            string
	Workers         int
	Progress        ProgressFunc
	FailOnLegacy    bool
	FailOnV2        bool
	Strict          bool
	ToleratePartial bool
	MaxIssues       int
	MaxPreludeBytes int
}

// CheckReport summarizes a structural validation pass.
type CheckReport struct {
	Root            string  `json:"root"`
	Scanned         int64   `json:"scanned"`
	OK              int64   `json:"ok"`
	Current         int64   `json:"current_magic"`
	Legacy          int64   `json:"legacy_magic"`
	V2              int64   `json:"v2_records"`
	Unknown         int64   `json:"unknown_format"`
	Warnings        int64   `json:"warnings"`
	Errors          int64   `json:"errors"`
	DurationMS      int64   `json:"duration_ms"`
	Issues          []Issue `json:"issues,omitempty"`
	IssuesTruncated bool    `json:"issues_truncated,omitempty"`
}

// Failed reports whether the validation pass found blocking problems.
func (r *CheckReport) Failed() bool { return r != nil && r.Errors > 0 }

var errPreludeUnterminated = errors.New("prelude is not terminated by a blank line")

// issueCollector accumulates issues for a single cassette.
type issueCollector struct {
	path   string
	issues []Issue
}

func (c *issueCollector) add(severity, code, format string, args ...any) {
	c.issues = append(c.issues, Issue{
		Path:     c.path,
		Severity: severity,
		Code:     code,
		Message:  fmt.Sprintf(format, args...),
	})
}

// CheckCassettes validates the structural format of every cassette below root.
// It reads only the prelude of each file and never inspects the recorded HTTP
// payload, so it stays fast on large vaults.
func CheckCassettes(ctx context.Context, opts CheckOptions) (*CheckReport, error) {
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		return nil, errors.New("cassette root is required")
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("cassette root %s: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("cassette root %s is not a directory", root)
	}

	if opts.MaxPreludeBytes <= 0 {
		opts.MaxPreludeBytes = defaultMaxPreludeBytes
	}
	maxIssues := opts.MaxIssues
	if maxIssues <= 0 {
		maxIssues = 200
	}

	report := &CheckReport{Root: root}
	started := time.Now()

	var (
		scanned   atomic.Int64
		okCount   atomic.Int64
		current   atomic.Int64
		legacy    atomic.Int64
		v2Count   atomic.Int64
		unknown   atomic.Int64
		warnings  atomic.Int64
		errorsCnt atomic.Int64
		issueMu   sync.Mutex
		truncated atomic.Bool
	)

	record := func(issues []Issue) {
		for _, issue := range issues {
			if issue.Severity == SeverityError {
				errorsCnt.Add(1)
			} else {
				warnings.Add(1)
			}
			issueMu.Lock()
			if len(report.Issues) < maxIssues {
				report.Issues = append(report.Issues, issue)
			} else {
				truncated.Store(true)
			}
			issueMu.Unlock()
		}
	}

	progress := newProgressReporter(opts.Progress, 2*time.Second)

	err = walkCassettes(ctx, root, func(path string) {
		kind, issues := ValidateCassetteFile(path, opts)
		switch kind {
		case "current":
			current.Add(1)
		case "legacy":
			legacy.Add(1)
		case "v2":
			v2Count.Add(1)
		default:
			unknown.Add(1)
		}
		record(issues)
		if len(issues) == 0 {
			okCount.Add(1)
		}
		done := scanned.Add(1)
		progress.report(done, func() Progress {
			return Progress{
				Phase:   "check",
				Scanned: done,
				Checked: done,
				Current: current.Load(),
				Other:   legacy.Load() + v2Count.Load() + unknown.Load(),
				Failed:  errorsCnt.Load(),
			}
		})
	}, normalizeWorkers(opts.Workers))

	report.Scanned = scanned.Load()
	report.OK = okCount.Load()
	report.Current = current.Load()
	report.Legacy = legacy.Load()
	report.V2 = v2Count.Load()
	report.Unknown = unknown.Load()
	report.Warnings = warnings.Load()
	report.Errors = errorsCnt.Load()
	report.IssuesTruncated = truncated.Load()
	report.DurationMS = time.Since(started).Milliseconds()

	if opts.Strict {
		report.Errors += report.Warnings
		report.Warnings = 0
		for i := range report.Issues {
			report.Issues[i].Severity = SeverityError
		}
	}
	return report, err
}

// ValidateCassetteFile performs the structural checks for one cassette and
// returns its detected format plus the issues found.
func ValidateCassetteFile(path string, opts CheckOptions) (string, []Issue) {
	collector := &issueCollector{path: path}

	file, err := os.Open(path)
	if err != nil {
		collector.add(SeverityError, "open_failed", "%v", err)
		return "unknown", collector.issues
	}
	defer file.Close()

	stat, err := file.Stat()
	if err != nil {
		collector.add(SeverityError, "stat_failed", "%v", err)
		return "unknown", collector.issues
	}
	size := stat.Size()
	if size == 0 {
		collector.add(SeverityWarning, "empty_file", "file is empty (the indexer treats it as an incomplete recording)")
		return "unknown", collector.issues
	}

	firstLine, err := readFirstLine(file)
	if err != nil {
		collector.add(SeverityError, "prelude_invalid", "%v", err)
		return "unknown", collector.issues
	}
	magic, _ := splitMagicLine(firstLine)

	// kind is assigned by both non-default branches below; the default branch
	// returns through the V2 validator.
	var kind string
	switch magic {
	case recordfile.FileMagic:
		kind = "current"
	case recordfile.LegacyFileMagic:
		kind = "legacy"
		severity, code := SeverityWarning, "legacy_magic"
		message := fmt.Sprintf("prelude still uses the pre-rename magic %q; readers accept it, `cassettes rewrite` can normalize it", recordfile.LegacyFileMagic)
		if opts.FailOnLegacy {
			severity, code = SeverityError, "legacy_magic"
		}
		collector.add(severity, code, "%s", message)
	default:
		return validateLegacyV2(file, size, collector, opts), collector.issues
	}

	maxPrelude := opts.MaxPreludeBytes
	if maxPrelude <= 0 {
		maxPrelude = defaultMaxPreludeBytes
	}
	prelude, err := readV3Prelude(file, firstLine, maxPrelude)
	if err != nil {
		if errors.Is(err, errPreludeUnterminated) {
			collector.add(SeverityError, "prelude_unterminated", "prelude has no blank line before the payload")
		} else {
			collector.add(SeverityError, "prelude_unreadable", "%v", err)
		}
		return kind, collector.issues
	}
	if int64(len(prelude)) > size {
		collector.add(SeverityError, "prelude_exceeds_file", "prelude is %d bytes but the file is %d bytes", len(prelude), size)
		return kind, collector.issues
	}

	parsed, err := recordfile.ParsePrelude(prelude)
	if err != nil {
		collector.add(SeverityError, "prelude_invalid", "%v", err)
		return kind, collector.issues
	}
	checkHeaderStructure(parsed, int64(len(prelude)), size, collector, opts)
	return kind, collector.issues
}

// validateLegacyV2 classifies a file whose first line is not a V3 magic. A V2
// recording starts with the raw JSON header followed by a fixed 2048 byte
// header block.
func validateLegacyV2(file *os.File, size int64, collector *issueCollector, opts CheckOptions) string {
	head := make([]byte, recordfile.LegacyHeaderLen)
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		collector.add(SeverityError, "read_failed", "%v", err)
		return "unknown"
	}
	read, err := io.ReadFull(file, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		collector.add(SeverityError, "read_failed", "%v", err)
		return "unknown"
	}
	if _, parseErr := recordfile.ParsePrelude(head[:read]); parseErr != nil {
		collector.add(SeverityError, "unrecognized_prelude",
			"first line is neither %q nor %q and the header does not parse: %v",
			recordfile.FileMagic, recordfile.LegacyFileMagic, parseErr)
		return "unknown"
	}
	severity, code := SeverityWarning, "legacy_v2"
	if opts.FailOnV2 {
		severity, code = SeverityError, "legacy_v2"
	}
	collector.add(severity, code, "file uses the legacy LLM_PROXY_V2 format; `migrate --rewrite-v2` can convert it")
	if size < recordfile.LegacyHeaderLen {
		collector.add(SeverityWarning, "v2_truncated", "file is %d bytes, smaller than the %d byte V2 header", size, recordfile.LegacyHeaderLen)
	}
	return "v2"
}

// checkHeaderStructure validates the prelude layout against the recorded
// lengths. It intentionally ignores payload semantics.
func checkHeaderStructure(parsed *recordfile.ParsedPrelude, payloadOffset, size int64, collector *issueCollector, opts CheckOptions) {
	header := parsed.Header
	if header.Version != "LLM_PROXY_V3" {
		collector.add(SeverityWarning, "version_not_canonical", "meta version is %q, expected %q", header.Version, "LLM_PROXY_V3")
	}
	if strings.TrimSpace(header.Meta.RequestID) == "" {
		collector.add(SeverityWarning, "missing_request_id", "meta has no request_id")
	}
	if header.Meta.Time.IsZero() {
		collector.add(SeverityWarning, "missing_time", "meta has no time")
	}

	layout := header.Layout
	switch {
	case layout.ReqHeaderLen < 0 || layout.ReqBodyLen < 0 || layout.ResHeaderLen < 0 || layout.ResBodyLen < 0:
		collector.add(SeverityError, "negative_length", "layout contains a negative length: %+v", layout)
		return
	case layout.ReqHeaderLen == 0 && layout.ReqBodyLen == 0 && layout.ResHeaderLen == 0 && layout.ResBodyLen == 0:
		collector.add(SeverityWarning, "empty_layout", "layout declares no payload lengths")
		return
	}

	expected := payloadOffset + layout.ReqHeaderLen + layout.ReqBodyLen + 1 + layout.ResHeaderLen + layout.ResBodyLen
	if expected != size {
		severity, code := SeverityError, "layout_mismatch"
		if opts.ToleratePartial {
			severity, code = SeverityWarning, "layout_mismatch_partial"
		}
		collector.add(severity, code, "declared layout needs %d bytes but the file has %d (payload offset %d, layout %+v)",
			expected, size, payloadOffset, layout)
	}
}

// readV3Prelude reads the meta and event lines up to and including the blank
// line that terminates the prelude.
func readV3Prelude(file *os.File, firstLine []byte, maxBytes int) ([]byte, error) {
	var buffer bytes.Buffer
	buffer.Write(firstLine)
	// readFirstLine reads a fixed size block, so the file offset is not
	// necessarily at the end of the first line yet; position it explicitly.
	if _, err := file.Seek(int64(len(firstLine)), io.SeekStart); err != nil {
		return nil, err
	}
	reader := newBufferedReader(file)
	for {
		if buffer.Len() > maxBytes {
			return nil, fmt.Errorf("prelude exceeds %d bytes", maxBytes)
		}
		line, err := reader.ReadBytes('\n')
		if len(line) > 0 {
			buffer.Write(line)
			if isBlankLine(line) {
				return buffer.Bytes(), nil
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return buffer.Bytes(), errPreludeUnterminated
			}
			return buffer.Bytes(), err
		}
	}
}

func isBlankLine(line []byte) bool {
	trimmed := bytes.TrimRight(line, "\r\n")
	return len(trimmed) == 0
}
