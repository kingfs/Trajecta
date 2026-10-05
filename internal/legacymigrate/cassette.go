package legacymigrate

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// tempCassettePrefix marks in-flight temporary files created while rewriting a
// cassette. Files with this prefix are never treated as cassettes.
const tempCassettePrefix = ".trajecta-magic-"

// copyBufferSize is the streaming buffer used when a cassette must be rewritten.
const copyBufferSize = 1 << 20

// FileFailure records a per-file error.
type FileFailure struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// MagicRewriteOptions configures RewriteCassetteMagic.
type MagicRewriteOptions struct {
	Root        string
	Workers     int
	DryRun      bool
	VerifyAfter bool
	Progress    ProgressFunc
}

// MagicRewriteReport summarizes a cassette magic rewrite pass.
type MagicRewriteReport struct {
	Root        string        `json:"root"`
	DryRun      bool          `json:"dry_run"`
	Scanned     int64         `json:"scanned"`
	Rewritten   int64         `json:"rewritten"`
	Current     int64         `json:"already_current"`
	Other       int64         `json:"other_format"`
	Errors      int64         `json:"errors"`
	BytesCopied int64         `json:"bytes_copied"`
	DurationMS  int64         `json:"duration_ms"`
	Failures    []FileFailure `json:"failures,omitempty"`
}

// RewriteCassetteMagic replaces the first line of every cassette whose prelude
// still carries the pre-rename magic "# llm-tracelab/v3" with the current
// "# trajecta/v3" magic. Nothing else in the file is inspected or modified:
// the payload is copied byte for byte.
//
// Each file is rewritten through a temporary file in the same directory and
// atomically renamed into place, so an interrupted run can never leave a
// half-written cassette behind. File mode and modification time are preserved.
func RewriteCassetteMagic(ctx context.Context, opts MagicRewriteOptions) (*MagicRewriteReport, error) {
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

	report := &MagicRewriteReport{Root: root, DryRun: opts.DryRun}
	started := time.Now()

	var (
		scanned     atomic.Int64
		rewritten   atomic.Int64
		current     atomic.Int64
		otherFormat atomic.Int64
		bytesCopied atomic.Int64
		errorsCount atomic.Int64
		failureMu   sync.Mutex
	)

	workers := normalizeWorkers(opts.Workers)
	progress := newProgressReporter(opts.Progress, 2*time.Second)

	err = walkCassettes(ctx, root, func(path string) {
		workerErr := rewriteOneCassette(ctx, path, opts.DryRun, opts.VerifyAfter, &bytesCopied)
		// workerErr is nil when nothing had to change.
		if workerErr != nil {
			if errors.Is(workerErr, errNotMultiplexed) {
				otherFormat.Add(1)
			} else if errors.Is(workerErr, errAlreadyCurrent) {
				current.Add(1)
			} else {
				errorsCount.Add(1)
				failureMu.Lock()
				if len(report.Failures) < maxReportedFailures {
					report.Failures = append(report.Failures, FileFailure{Path: path, Message: workerErr.Error()})
				}
				failureMu.Unlock()
			}
		} else {
			rewritten.Add(1)
		}
		done := scanned.Add(1)
		progress.report(done, func() Progress {
			return Progress{
				Phase:     "rewrite",
				Scanned:   done,
				Rewritten: rewritten.Load(),
				Current:   current.Load(),
				Other:     otherFormat.Load(),
				Failed:    errorsCount.Load(),
			}
		})
	}, workers)

	report.Scanned = scanned.Load()
	report.Rewritten = rewritten.Load()
	report.Current = current.Load()
	report.Other = otherFormat.Load()
	report.Errors = errorsCount.Load()
	report.BytesCopied = bytesCopied.Load()
	report.DurationMS = time.Since(started).Milliseconds()
	return report, err
}

const maxReportedFailures = 50

var (
	// errNotMultiplexed marks a cassette that does not use a V3 prelude at all.
	errNotMultiplexed = errors.New("cassette does not use the v3 prelude")
	// errAlreadyCurrent marks a cassette that already carries the current magic.
	errAlreadyCurrent = errors.New("cassette already uses the current magic")
)

// rewriteOneCassette inspects only the first line of a cassette and rewrites it
// when it carries the legacy magic. It reports errAlreadyCurrent or
// errNotMultiplexed when the file needs no change.
func rewriteOneCassette(ctx context.Context, path string, dryRun, verifyAfter bool, bytesCopied *atomic.Int64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	header, err := readFirstLine(file)
	if err != nil {
		file.Close()
		return err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}

	magic, lineLength := splitMagicLine(header)
	switch magic {
	case recordfile.FileMagic:
		file.Close()
		return errAlreadyCurrent
	case recordfile.LegacyFileMagic:
		// fall through: this file needs to be rewritten
	default:
		file.Close()
		return errNotMultiplexed
	}
	if dryRun {
		file.Close()
		return nil
	}

	if err := replaceFirstLine(file, path, info, header, lineLength); err != nil {
		file.Close()
		return err
	}
	file.Close()
	bytesCopied.Add(info.Size())

	if verifyAfter {
		if err := verifyCassetteMagic(path); err != nil {
			return err
		}
	}
	return nil
}

// readFirstLine reads at most maxFirstLineBytes bytes and returns the first
// line including its terminator.
//
// It reads a fixed size block, so the caller must not assume that the file
// offset ends up at the end of the returned line: seek to len(line) before
// reading anything else from the file.
func readFirstLine(file *os.File) ([]byte, error) {
	const maxFirstLineBytes = 512
	buffer := make([]byte, maxFirstLineBytes)
	n, err := file.Read(buffer)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if n == 0 {
		return nil, errors.New("empty file")
	}
	content := buffer[:n]
	if idx := indexByte(content, '\n'); idx >= 0 {
		return content[:idx+1], nil
	}
	if n == maxFirstLineBytes {
		return nil, fmt.Errorf("first line exceeds %d bytes", maxFirstLineBytes)
	}
	return content, nil
}

// splitMagicLine returns the magic without its line terminator together with
// the number of bytes the whole first line occupies.
func splitMagicLine(line []byte) (string, int) {
	trimmed := line
	if len(trimmed) > 0 && trimmed[len(trimmed)-1] == '\n' {
		trimmed = trimmed[:len(trimmed)-1]
	}
	if len(trimmed) > 0 && trimmed[len(trimmed)-1] == '\r' {
		trimmed = trimmed[:len(trimmed)-1]
	}
	return string(trimmed), len(line)
}

// replaceFirstLine writes the current magic followed by the untouched remainder
// of the source file into a temporary file and renames it over the original.
func replaceFirstLine(source *os.File, path string, info os.FileInfo, firstLine []byte, lineLength int) error {
	carriageReturn := strings.HasSuffix(strings.TrimSuffix(string(firstLine), "\n"), "\r")
	newLine := recordfile.FileMagic + "\n"
	if carriageReturn {
		newLine = recordfile.FileMagic + "\r\n"
	}

	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, tempCassettePrefix+"*"+".tmp")
	if err != nil {
		return err
	}
	tempName := temp.Name()
	cleanup := func() {
		temp.Close()
		os.Remove(tempName)
	}

	if _, err := temp.WriteString(newLine); err != nil {
		cleanup()
		return err
	}
	if _, err := source.Seek(int64(lineLength), io.SeekStart); err != nil {
		cleanup()
		return err
	}
	buffer := make([]byte, copyBufferSize)
	if _, err := io.CopyBuffer(temp, source, buffer); err != nil {
		cleanup()
		return err
	}
	if err := temp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := temp.Chmod(info.Mode().Perm()); err != nil {
		cleanup()
		return err
	}
	if err := temp.Close(); err != nil {
		os.Remove(tempName)
		return err
	}
	if err := os.Chtimes(tempName, info.ModTime(), info.ModTime()); err != nil {
		os.Remove(tempName)
		return err
	}
	if err := os.Rename(tempName, path); err != nil {
		os.Remove(tempName)
		return err
	}
	return nil
}

func verifyCassetteMagic(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	line, err := readFirstLine(file)
	if err != nil {
		return err
	}
	magic, _ := splitMagicLine(line)
	if magic != recordfile.FileMagic {
		return fmt.Errorf("verification failed: first line is %q", magic)
	}
	return nil
}

// CleanupStaleTempFiles removes temporary files left behind by an interrupted
// rewrite. It only touches files this tool creates.
func CleanupStaleTempFiles(ctx context.Context, root string) (int, error) {
	removed := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.IsDir() {
			return nil
		}
		name := entry.Name()
		if strings.HasPrefix(name, tempCassettePrefix) && strings.HasSuffix(name, ".tmp") {
			if removeErr := os.Remove(path); removeErr == nil {
				removed++
			}
		}
		return nil
	})
	return removed, err
}

// walkCassettes walks every regular *.http file below root and calls handle for
// each one. Workers run handle concurrently; walk errors are reported through
// the returned error but never abort the walk.
func walkCassettes(ctx context.Context, root string, handle func(path string), workers int) error {
	paths := make(chan string, workers*4)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for path := range paths {
				if ctx.Err() != nil {
					continue
				}
				handle(path)
			}
		}()
	}

	var walkErr error
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			if walkErr == nil {
				walkErr = err
			}
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".http") {
			return nil
		}
		if strings.HasPrefix(entry.Name(), tempCassettePrefix) {
			return nil
		}
		select {
		case paths <- path:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	})
	close(paths)
	wg.Wait()
	if err != nil {
		return err
	}
	return walkErr
}

func indexByte(data []byte, target byte) int {
	for i, b := range data {
		if b == target {
			return i
		}
	}
	return -1
}

func normalizeWorkers(workers int) int {
	if workers <= 0 {
		workers = defaultWorkers()
	}
	if workers > maxWorkers {
		workers = maxWorkers
	}
	return workers
}

const maxWorkers = 64

// newBufferedReader is a small helper used by the structural validator.
func newBufferedReader(reader io.Reader) *bufio.Reader {
	return bufio.NewReaderSize(reader, 64*1024)
}
