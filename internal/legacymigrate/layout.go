package legacymigrate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// DefaultUnknownSite is the directory segment used for recordings that never
// resolved an upstream, so they carry no site host. Their prelude URL is
// relative (for example "/v1/responses"), which means the real upstream host
// cannot be recovered from the file.
const DefaultUnknownSite = "unknown-site"

// layoutHeadBytes is the first read attempt per cassette. A prelude is normally
// one or two kilobytes, and on a cold vault the bytes dominate the scan cost, so
// the larger bounds are only used when the smaller one turns out to be truncated.
const layoutHeadBytes = 4 << 10

// layoutGrowBytes and layoutMaxBytes are the follow-up read attempts. A streaming
// recording appends one "# event:" line per chunk, so a long stream really does
// produce a prelude of several hundred kilobytes.
const (
	layoutGrowBytes = 512 << 10
	layoutMaxBytes  = 8 << 20
)

// Layout decisions for one cassette.
const (
	// LayoutCanonical means the path already is "<site>/<model>/<date>".
	LayoutCanonical = "canonical"
	// LayoutSiteMissing means the path is "<model>/<date>": the recording never
	// had a site segment.
	LayoutSiteMissing = "site_missing"
	// LayoutModelPrefix means the path only holds a leading part of the model
	// name ("feature" of "feature/gpt-5.6-luna") and no site segment.
	LayoutModelPrefix = "model_prefix_only"
	// LayoutAmbiguous means path and recorded model disagree in a way that
	// cannot be resolved mechanically, so the file needs a human decision.
	LayoutAmbiguous = "ambiguous"
	// LayoutUnreadable means the prelude or the model name could not be read.
	LayoutUnreadable = "unreadable"
)

// LayoutOptions configures a layout planning pass.
type LayoutOptions struct {
	Root        string
	UnknownSite string
	Workers     int
	Progress    ProgressFunc
	// KeepMoves caps how many planned moves are retained for the report. The
	// counters stay exact. Zero keeps every move.
	KeepMoves int
}

// LayoutMove is one planned relocation. From and To are relative to the root and
// slash separated.
type LayoutMove struct {
	From  string `json:"from"`
	To    string `json:"to"`
	Model string `json:"model"`
	Site  string `json:"site,omitempty"`
}

// LayoutReport is the read-only result of a layout planning pass. Planning never
// writes to the filesystem.
type LayoutReport struct {
	Root         string `json:"root"`
	UnknownSite  string `json:"unknown_site"`
	Scanned      int64  `json:"scanned"`
	Canonical    int64  `json:"canonical"`
	SiteMissing  int64  `json:"site_missing"`
	ModelPrefix  int64  `json:"model_prefix_only"`
	Ambiguous    int64  `json:"ambiguous"`
	Unreadable   int64  `json:"unreadable"`
	CurrentMagic int64  `json:"current_magic"`
	LegacyMagic  int64  `json:"legacy_magic"`
	OtherMagic   int64  `json:"other_magic"`
	TotalBytes   int64  `json:"total_bytes"`
	// MovesPlanned counts every planned relocation; Moves retains at most
	// KeepMoves of them and MovesTruncated reports whether the list was cut.
	MovesPlanned   int64        `json:"moves_planned"`
	MovesBytes     int64        `json:"moves_bytes"`
	Moves          []LayoutMove `json:"moves,omitempty"`
	MovesTruncated bool         `json:"moves_truncated,omitempty"`
	// AmbiguousPaths samples the files that need a manual decision.
	AmbiguousPaths []LayoutMove  `json:"ambiguous_paths,omitempty"`
	Failures       []FileFailure `json:"failures,omitempty"`
	DurationMS     int64         `json:"duration_ms"`
}

// Failed reports whether the pass hit cassettes it could not classify. Such
// files are never silently rewritten; callers decide whether that is fatal.
func (r *LayoutReport) Failed() bool { return r != nil && r.Unreadable > 0 }

// PlanCassetteLayout walks the cassette tree, reads each prelude (bounded to
// layoutPrefixBytes) and decides where the file belongs under the current
// "<site host>/<model>/YYYY/MM/DD" layout. Nothing is written.
func PlanCassetteLayout(ctx context.Context, opts LayoutOptions) (*LayoutReport, error) {
	root, err := resolveLayoutRoot(opts.Root)
	if err != nil {
		return nil, err
	}
	unknownSite := strings.Trim(strings.TrimSpace(opts.UnknownSite), "/")
	if unknownSite == "" {
		unknownSite = DefaultUnknownSite
	}
	if strings.Contains(unknownSite, "/") {
		return nil, fmt.Errorf("unknown site segment %q must not contain %q", unknownSite, "/")
	}

	report := &LayoutReport{Root: root, UnknownSite: unknownSite}
	started := time.Now()

	var (
		scanned      atomic.Int64
		canonical    atomic.Int64
		siteMissing  atomic.Int64
		modelPrefix  atomic.Int64
		ambiguous    atomic.Int64
		unreadable   atomic.Int64
		currentMagic atomic.Int64
		legacyMagic  atomic.Int64
		otherMagic   atomic.Int64
		movesPlanned atomic.Int64
		movesBytes   atomic.Int64
		totalBytes   atomic.Int64
	)
	var mu sync.Mutex
	keepMove := func(move LayoutMove) {
		mu.Lock()
		defer mu.Unlock()
		if opts.KeepMoves > 0 && len(report.Moves) >= opts.KeepMoves {
			report.MovesTruncated = true
			return
		}
		report.Moves = append(report.Moves, move)
	}
	keepAmbiguous := func(move LayoutMove) {
		mu.Lock()
		defer mu.Unlock()
		if len(report.AmbiguousPaths) < maxReportedFailures {
			report.AmbiguousPaths = append(report.AmbiguousPaths, move)
		}
	}
	keepFailure := func(path string, err error) {
		mu.Lock()
		defer mu.Unlock()
		if len(report.Failures) < maxReportedFailures {
			report.Failures = append(report.Failures, FileFailure{Path: path, Message: err.Error()})
		}
	}

	progress := newProgressReporter(opts.Progress, 2*time.Second)
	walkErr := walkCassettes(ctx, root, func(path string) {
		rel := relativeCassettePath(root, path)
		magic, model, size, inspectErr := inspectLayout(path)
		if inspectErr != nil {
			unreadable.Add(1)
			keepFailure(rel, inspectErr)
		} else {
			switch magic {
			case recordfile.FileMagic:
				currentMagic.Add(1)
			case recordfile.LegacyFileMagic:
				legacyMagic.Add(1)
			default:
				otherMagic.Add(1)
			}
			totalBytes.Add(size)
			decision, move, decisionErr := decideLayout(rel, model, unknownSite)
			switch {
			case decisionErr != nil:
				unreadable.Add(1)
				keepFailure(rel, decisionErr)
			case decision == LayoutCanonical:
				canonical.Add(1)
			case decision == LayoutAmbiguous:
				ambiguous.Add(1)
				keepAmbiguous(move)
			default:
				switch decision {
				case LayoutSiteMissing:
					siteMissing.Add(1)
				case LayoutModelPrefix:
					modelPrefix.Add(1)
				}
				movesPlanned.Add(1)
				movesBytes.Add(size)
				keepMove(move)
			}
		}

		done := scanned.Add(1)
		progress.report(done, func() Progress {
			return Progress{
				Phase:   "layout",
				Scanned: done,
				Current: canonical.Load(),
				Other:   siteMissing.Load() + modelPrefix.Load() + ambiguous.Load() + unreadable.Load(),
			}
		})
	}, normalizeWorkers(opts.Workers))
	if walkErr != nil {
		return nil, walkErr
	}

	report.Scanned = scanned.Load()
	if report.Scanned > 0 && scanned.Load() == unreadable.Load() {
		return nil, fmt.Errorf(
			"none of the %d cassettes under %s is in <site>/<model>/YYYY/MM/DD; --root must be the cassette vault root (the directory holding the site directories)",
			report.Scanned, root)
	}
	report.Canonical = canonical.Load()
	report.SiteMissing = siteMissing.Load()
	report.ModelPrefix = modelPrefix.Load()
	report.Ambiguous = ambiguous.Load()
	report.Unreadable = unreadable.Load()
	report.CurrentMagic = currentMagic.Load()
	report.LegacyMagic = legacyMagic.Load()
	report.OtherMagic = otherMagic.Load()
	report.TotalBytes = totalBytes.Load()
	report.MovesPlanned = movesPlanned.Load()
	report.MovesBytes = movesBytes.Load()
	report.DurationMS = time.Since(started).Milliseconds()
	sort.Slice(report.Moves, func(i, j int) bool { return report.Moves[i].From < report.Moves[j].From })
	sort.Slice(report.AmbiguousPaths, func(i, j int) bool {
		return report.AmbiguousPaths[i].From < report.AmbiguousPaths[j].From
	})
	return report, nil
}

// inspectLayout reads a bounded prefix of a cassette and returns its prelude
// magic, its recorded model name and its file size. The prelude of a healthy
// cassette is a few kilobytes, so the large bound is only used when the small
// one turns out to be truncated.
func inspectLayout(path string) (magic string, model string, size int64, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", "", 0, err
	}
	defer file.Close()

	info, statErr := file.Stat()
	if statErr != nil {
		return "", "", 0, statErr
	}
	size = info.Size()
	if size == 0 {
		return "", "", 0, errors.New("cassette is empty")
	}

	var (
		head     []byte
		parsed   *recordfile.ParsedPrelude
		parseErr error
	)
	for _, limit := range []int64{layoutHeadBytes, layoutGrowBytes, layoutMaxBytes} {
		if limit > size {
			limit = size
		}
		head, err = readLayoutHead(file, limit)
		if err != nil {
			return "", "", size, err
		}
		if parsed, parseErr = recordfile.ParsePrelude(head); parseErr == nil {
			break
		}
	}
	magic = layoutMagic(head)
	if parseErr != nil {
		return magic, "", size, fmt.Errorf("parse prelude: %w", parseErr)
	}
	model = strings.TrimSpace(parsed.Header.Meta.Model)
	if model == "" {
		return magic, "", size, errors.New("prelude records no model name")
	}
	return magic, model, size, nil
}

// readLayoutHead reads at most limit bytes from the start of a cassette.
func readLayoutHead(file *os.File, limit int64) ([]byte, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(file, limit))
}

// layoutMagic classifies the prelude magic of a cassette head. A file whose
// first line is not a known magic is reported as LLM_PROXY_V2 when its fixed
// 2 KB header parses, and as "unknown" otherwise.
func layoutMagic(head []byte) string {
	magic, _ := splitMagicLine(firstLineBytes(head))
	switch magic {
	case recordfile.FileMagic, recordfile.LegacyFileMagic:
		return magic
	}
	if _, parseErr := recordfile.ParsePrelude(head[:min(len(head), recordfile.LegacyHeaderLen)]); parseErr == nil {
		return "v2"
	}
	return "unknown"
}

// decideLayout classifies one cassette path against its recorded model name.
//
// The writer emits "<site host>/<model>/YYYY/MM/DD/<file>.http" where the model
// name may itself contain "/" (for example "feature/gpt-5.6-sol"), so the model
// name recorded in the prelude is the only reliable separator between the site
// segment and the model path.
func decideLayout(rel, model, unknownSite string) (string, LayoutMove, error) {
	// A recorded model name occasionally carries a trailing separator ("gw/"),
	// so normalise it before it becomes a directory path.
	model = strings.Trim(strings.TrimSpace(model), "/")
	if model == "" {
		return LayoutUnreadable, LayoutMove{From: rel}, fmt.Errorf(
			"prelude records no model name, so the target directory is unknown")
	}
	if err := validateLayoutModel(model); err != nil {
		return LayoutUnreadable, LayoutMove{From: rel}, err
	}

	segments := strings.Split(rel, "/")
	if len(segments) < 5 {
		return LayoutUnreadable, LayoutMove{From: rel, Model: model}, fmt.Errorf(
			"%s is not <site>/<model>/YYYY/MM/DD/<file>; --root must be the cassette vault root", rel)
	}
	date := segments[len(segments)-4 : len(segments)-1]
	if !isLayoutDate(date) {
		return LayoutUnreadable, LayoutMove{From: rel, Model: model}, fmt.Errorf(
			"%s has no YYYY/MM/DD tail; --root must be the cassette vault root", rel)
	}
	identity := strings.Join(segments[:len(segments)-4], "/")
	datePath := strings.Join(append(append([]string{}, date...), segments[len(segments)-1]), "/")
	missingSite := LayoutMove{
		From:  rel,
		To:    unknownSite + "/" + model + "/" + datePath,
		Model: model,
	}

	switch {
	case identity == model:
		// No site segment at all: the recording resolved no upstream.
		return LayoutSiteMissing, missingSite, nil
	case strings.HasSuffix(identity, "/"+model):
		site := strings.TrimSuffix(identity, "/"+model)
		if site == "" {
			return LayoutSiteMissing, missingSite, nil
		}
		return LayoutCanonical, LayoutMove{From: rel, To: rel, Model: model, Site: site}, nil
	case strings.HasPrefix(model, identity+"/"):
		// The path only holds a prefix of the model name.
		return LayoutModelPrefix, missingSite, nil
	default:
		return LayoutAmbiguous, LayoutMove{From: rel, Model: model, Site: identity}, nil
	}
}

// validateLayoutModel rejects recorded model names that cannot be used as a
// relative directory path, so a plan can never escape the cassette root.
func validateLayoutModel(model string) error {
	for _, segment := range strings.Split(model, "/") {
		switch segment {
		case "", ".", "..":
			return fmt.Errorf("prelude model name %q is not a safe directory path", model)
		}
	}
	return nil
}

// firstLineBytes returns the first line, including its terminator.
func firstLineBytes(content []byte) []byte {
	if idx := indexByte(content, '\n'); idx >= 0 {
		return content[:idx+1]
	}
	return content
}

func isLayoutDate(segments []string) bool {
	if len(segments) != 3 {
		return false
	}
	for i, value := range segments {
		want := 4
		if i > 0 {
			want = 2
		}
		if len(value) != want {
			return false
		}
		for _, r := range value {
			if r < '0' || r > '9' {
				return false
			}
		}
	}
	return true
}

func relativeCassettePath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

// resolveLayoutRoot validates the cassette root directory.
func resolveLayoutRoot(root string) (string, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return "", errors.New("cassette root directory is required")
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", root)
	}
	return root, nil
}
