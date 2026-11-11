package transfer

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
)

// ExportOptions controls one bundle export.
type ExportOptions struct {
	// Since/Until are the recorded_at bounds (inclusive) applied to logs and
	// to the cassettes they reference. All takes no window; exactly one of
	// the two shapes is legal.
	Since *time.Time
	Until *time.Time
	All   bool

	// Out is the bundle path. It must not exist unless Force is set.
	Out   string
	Force bool

	// Password encrypts the whole stream. When empty and AllowPlaintext is
	// false, a random password is generated and reported once via Result.Password.
	Password       string
	AllowPlaintext bool

	// Compression is "gzip" (default) or "none".
	Compression string
	// SourceName is a free-form label stored in the manifest for reporting
	// (e.g. "home" / "office"). It is not used for matching.
	SourceName string
	// Version is the trajecta binary version, recorded in the manifest.
	Version string

	Log *Logger
}

// ExportResult reports what the export did. Password is set only when a
// random password was generated; otherwise empty.
type ExportResult struct {
	Bundle    string
	Password  string
	Bytes     int64
	Tables    []TableCount
	Cassettes CassetteCount
	Duration  time.Duration
}

const (
	compressionGzip = "gzip"
	compressionNone = "none"
)

type exportWindow struct {
	Since *time.Time
	Until *time.Time
	All   bool
}

type cassetteRef struct {
	Entry   string // slash-separated path relative to the source output dir
	AbsPath string // resolved path on the source host
}

// Export writes one bundle. The source deployment runs with traffic stopped;
// the export takes its own consistent read snapshot and never writes.
func Export(ctx context.Context, st *store.Store, outputDir string, opts ExportOptions) (*ExportResult, error) {
	start := time.Now()
	if st == nil {
		return nil, errors.New("export requires a store")
	}
	window, err := normalizeWindow(opts.Since, opts.Until, opts.All)
	if err != nil {
		return nil, err
	}
	log := opts.Log
	if log == nil {
		log = NewDiscardLogger()
	}
	if err := checkOutPath(opts.Out, opts.Force); err != nil {
		return nil, err
	}

	password := opts.Password
	generatedPassword := false
	if password == "" && !opts.AllowPlaintext {
		password = generateExportPassword()
		generatedPassword = true
	}
	if password == "" && opts.AllowPlaintext {
		log.Warn("export is NOT encrypted (--allow-plaintext): the bundle contains decrypted channel credentials and must be kept safe")
	}

	compression := strings.TrimSpace(opts.Compression)
	if compression == "" {
		compression = compressionGzip
	}
	if compression != compressionGzip && compression != compressionNone {
		return nil, fmt.Errorf("unsupported compression %q (want gzip|none)", compression)
	}

	absOut := outputDir
	if !filepath.IsAbs(absOut) {
		if joined, err := filepath.Abs(absOut); err == nil {
			absOut = joined
		}
	}

	// Pre-pass under one read snapshot: manifest counts and the cassette
	// reference list are both known before a single byte is written.
	pre := exportPrepass{}
	err = st.TransferReadTx(ctx, func(view *store.Store) error {
		var err error
		pre, err = collectPrepass(ctx, view, window, absOut, log)
		return err
	})
	if err != nil {
		return nil, err
	}
	migrationsVersion := pre.MigrationsVersion

	file, err := os.OpenFile(opts.Out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create bundle %s: %w", opts.Out, err)
	}
	bundlePath := opts.Out
	fail := func(err error) (*ExportResult, error) {
		file.Close()
		_ = os.Remove(bundlePath)
		return nil, err
	}

	var encrypted *encryptedWriter
	pipe := io.Writer(file)
	if password != "" {
		enc, err := newEncryptedWriter(file, password)
		if err != nil {
			return fail(err)
		}
		encrypted = enc
		pipe = enc
	}
	var gz *gzip.Writer
	if compression == compressionGzip {
		gz = gzip.NewWriter(pipe)
		pipe = gz
	}
	bundle := bundleWriter{tw: tar.NewWriter(pipe)}

	manifest := Manifest{
		Format:            BundleFormat,
		CreatedAt:         time.Now().UTC(),
		TrajectaVersion:   opts.Version,
		MigrationsVersion: migrationsVersion,
		SourceName:        opts.SourceName,
		SourceOutputDir:   outputDir,
		Window:            Window{Since: window.Since, Until: window.Until, All: window.All},
		PasswordProtected: password != "",
		Compression:       compression,
		Tables:            pre.Tables,
		Cassettes:         pre.Cassettes,
	}
	if err := bundle.WriteEntry(entryManifest, mustJSON(manifest), manifest.CreatedAt); err != nil {
		return fail(err)
	}

	result := &ExportResult{Bundle: bundlePath, Tables: pre.Tables}
	var writeErr error
	writeErr = st.TransferReadTx(ctx, func(view *store.Store) error {
		counts := map[string]int64{}
		for _, table := range tableExportOrder {
			n, err := exportTable(ctx, view, table, window, bundle, log)
			if err != nil {
				return fmt.Errorf("export table %s: %w", table, err)
			}
			counts[table] = n
			log.Infof("table %s: exported %d rows", table, n)
		}
		for _, table := range pre.Tables {
			if counts[table.Name] != table.Rows {
				return fmt.Errorf("table %s row count changed during export (want %d, got %d); bundle aborted", table.Name, table.Rows, counts[table.Name])
			}
		}
		// Copy every referenced cassette. Missing files are reported, not fatal:
		// the index row is what the monitor reads, and the operator can restore
		// the file from backup later.
		copied := int64(0)
		var copiedBytes int64
		missing := int64(0)
		for _, ref := range pre.Refs {
			bytes, ok, err := copyCassetteToBundle(ref, bundle, log)
			if err != nil {
				return err
			}
			if !ok {
				missing++
				continue
			}
			copied++
			copiedBytes += bytes
		}
		for _, rel := range pre.Orphans {
			bytes, ok, err := copyCassetteToBundle(cassetteRef{Entry: rel, AbsPath: filepath.Join(absOut, filepath.FromSlash(rel))}, bundle, log)
			if err != nil {
				return err
			}
			if ok {
				copied++
				copiedBytes += bytes
			} else {
				missing++
			}
		}
		result.Cassettes = CassetteCount{
			Count:           copied,
			Bytes:           copiedBytes,
			ReferencedCount: pre.Cassettes.ReferencedCount,
			OrphanCount:     int64(len(pre.Orphans)),
			MissingFiles:    missing,
		}
		return nil
	})
	if writeErr != nil {
		_ = bundle.tw.Close()
		if gz != nil {
			_ = gz.Close()
		}
		if encrypted != nil {
			_ = encrypted.Close()
		}
		return fail(writeErr)
	}
	if err := bundle.tw.Close(); err != nil {
		return fail(fmt.Errorf("finalise bundle: %w", err))
	}
	if gz != nil {
		if err := gz.Close(); err != nil {
			return fail(err)
		}
	}
	if encrypted != nil {
		if err := encrypted.Close(); err != nil {
			return fail(err)
		}
	}
	if err := file.Close(); err != nil {
		return fail(err)
	}
	fi, err := os.Stat(bundlePath)
	if err != nil {
		return nil, err
	}
	result.Bytes = fi.Size()
	if generatedPassword {
		result.Password = password
	}
	result.Duration = time.Since(start)
	return result, nil
}

func normalizeWindow(since, until *time.Time, all bool) (exportWindow, error) {
	if all {
		if since != nil || until != nil {
			return exportWindow{}, errors.New("--all cannot be combined with --since/--until")
		}
		return exportWindow{All: true}, nil
	}
	if since == nil && until == nil {
		return exportWindow{}, errors.New("time window required: pass --since/--until, or --all for an unwindowed export")
	}
	if since != nil {
		s := since.UTC()
		since = &s
	}
	if until != nil {
		u := until.UTC()
		until = &u
	}
	if since != nil && until != nil && !since.Before(until.Add(time.Nanosecond)) {
		return exportWindow{}, errors.New("--since must be before --until")
	}
	return exportWindow{Since: since, Until: until}, nil
}

func checkOutPath(out string, force bool) error {
	out = strings.TrimSpace(out)
	if out == "" {
		return errors.New("--out is required")
	}
	fi, err := os.Stat(out)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if fi.IsDir() {
		return fmt.Errorf("%s is a directory", out)
	}
	if !force {
		return fmt.Errorf("%s already exists (use --force to overwrite)", out)
	}
	return nil
}

func (w exportWindow) logWhere() (string, []any) {
	clauses := []string{}
	args := []any{}
	if w.Since != nil {
		clauses = append(clauses, `recorded_at >= ?`)
		args = append(args, w.Since.UTC().Format(exportTimeLayout))
	}
	if w.Until != nil {
		clauses = append(clauses, `recorded_at <= ?`)
		args = append(args, w.Until.UTC().Format(exportTimeLayout))
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return `WHERE ` + strings.Join(clauses, ` AND `), args
}

type exportPrepass struct {
	Tables            []TableCount
	Cassettes         CassetteCount
	Refs              []cassetteRef
	Orphans           []string
	MigrationsVersion int64
}

func collectPrepass(ctx context.Context, view *store.Store, window exportWindow, absOutDir string, log *Logger) (exportPrepass, error) {
	var pre exportPrepass
	for _, table := range tableExportOrder {
		var n int64
		query, args := countQueryFor(table, window)
		if err := view.TransferQueryRow(ctx, query, args...).Scan(&n); err != nil {
			return pre, fmt.Errorf("count table %s: %w", table, err)
		}
		pre.Tables = append(pre.Tables, TableCount{Name: table, Rows: n})
	}
	// Cassette references: one pass, used by the manifest, the copies and --all.
	refs, err := collectCassetteRefs(ctx, view, window, absOutDir)
	if err != nil {
		return pre, err
	}
	var referencedBytes int64
	var sum any
	sumQuery, args := cassetteSumQuery(window)
	if err := view.TransferQueryRow(ctx, sumQuery, args...).Scan(&sum); err != nil {
		return pre, fmt.Errorf("sum cassette bytes: %w", err)
	}
	referencedBytes = anyInt64(sum)
	pre.Refs = refs
	pre.Cassettes.ReferencedCount = int64(len(refs))
	pre.Cassettes.Bytes = referencedBytes
	pre.Cassettes.Count = int64(len(refs))
	if window.All {
		referenced := make(map[string]struct{}, len(refs))
		for _, ref := range refs {
			referenced[ref.Entry] = struct{}{}
		}
		orphans, err := collectOrphanCassettes(absOutDir, referenced)
		if err != nil {
			return pre, err
		}
		pre.Orphans = orphans
		pre.Cassettes.OrphanCount = int64(len(orphans))
		log.Infof("found %d referenced cassettes, %d orphan cassettes", len(refs), len(orphans))
	} else {
		log.Infof("found %d referenced cassettes", len(refs))
	}
	// Best effort: migrations version for operator diagnostics.
	var version any
	_ = view.TransferQueryRow(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version)
	pre.MigrationsVersion = anyInt64(version)
	return pre, nil
}

func countQueryFor(table string, window exportWindow) (string, []any) {
	if table == "logs" {
		where, args := window.logWhere()
		return `SELECT COUNT(*) FROM logs ` + where, args
	}
	return `SELECT COUNT(*) FROM ` + table, nil
}

func collectCassetteRefs(ctx context.Context, view *store.Store, window exportWindow, absOutDir string) ([]cassetteRef, error) {
	where, args := window.logWhere()
	rows, err := view.TransferQuery(ctx, `SELECT path FROM logs `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var refs []cassetteRef
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		abs, entry, err := resolveCassettePath(absOutDir, p)
		if err != nil {
			return nil, err
		}
		refs = append(refs, cassetteRef{Entry: entry, AbsPath: abs})
	}
	return refs, rows.Err()
}

func cassetteSumQuery(window exportWindow) (string, []any) {
	where, args := window.logWhere()
	return `SELECT COALESCE(SUM(file_size), 0) FROM logs ` + where, args
}

// resolveCassettePath maps a logs.path (absolute or relative) onto the source
// output directory. Entry is the slash-separated path relative to outputDir;
// it is what the bundle and the import dest use.
func resolveCassettePath(absOutDir, recorded string) (absPath, entry string, err error) {
	recorded = strings.TrimSpace(recorded)
	if recorded == "" {
		return "", "", errors.New("logs row with empty cassette path")
	}
	normOut := filepath.ToSlash(strings.TrimSuffix(absOutDir, string(filepath.Separator)))
	normRec := filepath.ToSlash(strings.TrimPrefix(recorded, "./"))
	// Happy path: the recorded path starts with the output dir, either because
	// it was stored absolute, or because output_dir was relative and the row
	// carries the same relative prefix.
	if rel := strings.TrimPrefix(normRec, normOut+"/"); rel != normRec && rel != "" {
		return filepath.Join(absOutDir, filepath.FromSlash(rel)), rel, nil
	}
	// Fallbacks for layouts that do not share the prefix (e.g. logs stored
	// relative to a different CWD): try CWD-resolution, then outputDir-join.
	candidates := []string{}
	if abs, err := filepath.Abs(recorded); err == nil {
		candidates = append(candidates, abs)
	}
	candidates = append(candidates, filepath.Join(absOutDir, filepath.FromSlash(normRec)))
	for _, candidate := range candidates {
		rel, err := filepath.Rel(absOutDir, candidate)
		if err != nil {
			continue
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			continue
		}
		if rel == "." {
			continue
		}
		return filepath.Join(absOutDir, rel), filepath.ToSlash(rel), nil
	}
	return "", "", fmt.Errorf("cassette path %q cannot be resolved under the output directory %q", recorded, absOutDir)
}

func copyCassetteToBundle(ref cassetteRef, bundle bundleWriter, log *Logger) (int64, bool, error) {
	f, err := os.Open(ref.AbsPath)
	if err != nil {
		// The index references a cassette that is no longer on disk. The row
		// still exports; the file does not, and the report says so.
		log.Warnf("cassette referenced by logs is missing on disk, skipped: %s (%v)", ref.AbsPath, err)
		return 0, false, nil
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0, false, err
	}
	hdr := &tar.Header{
		Name:     entryCassettesPrefix + ref.Entry,
		Mode:     0o600,
		Size:     fi.Size(),
		Typeflag: tar.TypeReg,
		ModTime:  fi.ModTime(),
	}
	if err := bundle.tw.WriteHeader(hdr); err != nil {
		return 0, false, err
	}
	n, err := io.Copy(bundle.tw, bufio.NewReaderSize(f, 1<<20))
	if err != nil {
		return n, false, err
	}
	log.Infof("cassette archived: %s (%d bytes)", ref.Entry, n)
	return n, true, nil
}

func collectOrphanCassettes(absOutDir string, referenced map[string]struct{}) ([]string, error) {
	var orphans []string
	walkErr := filepath.WalkDir(absOutDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".http") {
			return nil
		}
		rel, err := filepath.Rel(absOutDir, path)
		if err != nil {
			return err
		}
		slash := filepath.ToSlash(rel)
		if _, ok := referenced[slash]; ok {
			return nil
		}
		orphans = append(orphans, slash)
		return nil
	})
	return orphans, walkErr
}

func generateExportPassword() string {
	const alphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	var buf [32]byte
	if _, err := rand.Read(buf[:]); err != nil {
		for i := range buf {
			buf[i] = byte(time.Now().UnixNano() >> uint(i%8))
		}
	}
	groups := make([]string, 0, 4)
	for g := 0; g < 4; g++ {
		seg := make([]byte, 0, 8)
		for i := 0; i < 8; i++ {
			seg = append(seg, alphabet[int(buf[g*8+i])%len(alphabet)])
		}
		groups = append(groups, string(seg))
	}
	return strings.Join(groups, "-")
}

func mustJSON(v any) []byte {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		panic(err)
	}
	return data
}
