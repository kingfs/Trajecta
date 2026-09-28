package legacymigrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// MagicCensusOptions configures CensusCassetteMagic.
type MagicCensusOptions struct {
	Root     string
	Workers  int
	Progress ProgressFunc
}

// MagicCensusReport counts the prelude format of every cassette. It reads only
// the first bytes of each file, so it is fast even on vaults with hundreds of
// thousands of cassettes.
type MagicCensusReport struct {
	Root       string        `json:"root"`
	Scanned    int64         `json:"scanned"`
	Current    int64         `json:"current_magic"`
	Legacy     int64         `json:"legacy_magic"`
	V2         int64         `json:"v2_records"`
	Unknown    int64         `json:"unknown_format"`
	Errors     int64         `json:"errors"`
	DurationMS int64         `json:"duration_ms"`
	Failures   []FileFailure `json:"failures,omitempty"`
	TotalBytes int64         `json:"total_bytes,omitempty"`
}

// CensusCassetteMagic counts cassette formats without validating them.
func CensusCassetteMagic(ctx context.Context, opts MagicCensusOptions) (*MagicCensusReport, error) {
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		return nil, errors.New("cassette root is required")
	}
	if info, err := os.Stat(root); err != nil {
		return nil, fmt.Errorf("cassette root %s: %w", root, err)
	} else if !info.IsDir() {
		return nil, fmt.Errorf("cassette root %s is not a directory", root)
	}

	report := &MagicCensusReport{Root: root}
	started := time.Now()

	var (
		scanned     atomic.Int64
		current     atomic.Int64
		legacy      atomic.Int64
		v2Count     atomic.Int64
		unknown     atomic.Int64
		errorsCount atomic.Int64
		totalBytes  atomic.Int64
		failureMu   sync.Mutex
	)
	progress := newProgressReporter(opts.Progress, 2*time.Second)

	err := walkCassettes(ctx, root, func(path string) {
		kind, size, classifyErr := classifyCassette(path)
		if classifyErr != nil {
			errorsCount.Add(1)
			failureMu.Lock()
			if len(report.Failures) < maxReportedFailures {
				report.Failures = append(report.Failures, FileFailure{Path: path, Message: classifyErr.Error()})
			}
			failureMu.Unlock()
		} else {
			totalBytes.Add(size)
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
		}
		done := scanned.Add(1)
		progress.report(done, func() Progress {
			return Progress{
				Phase:   "census",
				Scanned: done,
				Current: current.Load(),
				Other:   legacy.Load() + v2Count.Load() + unknown.Load(),
				Failed:  errorsCount.Load(),
			}
		})
	}, normalizeWorkers(opts.Workers))

	report.Scanned = scanned.Load()
	report.Current = current.Load()
	report.Legacy = legacy.Load()
	report.V2 = v2Count.Load()
	report.Unknown = unknown.Load()
	report.Errors = errorsCount.Load()
	report.TotalBytes = totalBytes.Load()
	report.DurationMS = time.Since(started).Milliseconds()
	return report, err
}

// classifyCassette reads the first line of a cassette and returns its format.
func classifyCassette(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", 0, err
	}
	if info.Size() == 0 {
		return "unknown", 0, nil
	}
	line, err := readFirstLine(file)
	if err != nil {
		return "unknown", 0, err
	}
	magic, _ := splitMagicLine(line)
	switch magic {
	case recordfile.FileMagic:
		return "current", info.Size(), nil
	case recordfile.LegacyFileMagic:
		return "legacy", info.Size(), nil
	}
	head := make([]byte, recordfile.LegacyHeaderLen)
	if _, seekErr := file.Seek(0, 0); seekErr != nil {
		return "unknown", info.Size(), nil
	}
	read, readErr := file.Read(head)
	if readErr != nil && read == 0 {
		return "unknown", info.Size(), nil
	}
	if _, parseErr := recordfile.ParsePrelude(head[:read]); parseErr == nil {
		return "v2", info.Size(), nil
	}
	return "unknown", info.Size(), nil
}
