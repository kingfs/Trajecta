package legacymigrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/lib/pq"
	_ "modernc.org/sqlite"
)

// legacySQLiteNames are the application database file names used by Trajecta
// and by its pre-rename predecessors, newest naming convention first.
var legacySQLiteNames = []string{"trajecta.sqlite3", "llm_tracelab.sqlite3", "trace_index.sqlite3"}

// skippedSourceTables never get merged: they hold backend-local bookkeeping.
var skippedSourceTables = map[string]string{
	"schema_migrations": "migration bookkeeping of the source backend",
	"app_schema_status": "migration bookkeeping of the source backend",
}

// snapshotSourceTables hold runtime snapshots and compatibility projections
// rather than authoritative records: a running server rewrites them from the
// live provider configuration (see docs/ROUTING_AND_CREDENTIALS.md), so a
// legacy row can be absent from Postgres simply because the row it described
// was replaced or superseded by the current state. The archive gate must not
// excuse those keys silently, so operators opt in explicitly with
// TolerateSnapshotDrift and the excused count is reported.
var snapshotSourceTables = map[string]string{
	"upstream_targets": "runtime snapshot rewritten by configuration transactions and upstream refresh",
	"upstream_models":  "runtime snapshot replaced per upstream by probes and upstream refresh",
}

// SnapshotDriftTables returns the legacy tables whose missing keys the archive
// gate may excuse when TolerateSnapshotDrift is set, sorted by name.
func SnapshotDriftTables() []string {
	names := make([]string, 0, len(snapshotSourceTables))
	for name := range snapshotSourceTables {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// legacyTableDependencies is the complete set of cross-table foreign keys in
// the application schema; parents are copied before children.
var legacyTableDependencies = map[string][]string{
	"api_tokens": {"users"},
}

// SQLite file open modes.
const (
	// OpenModeAuto tries read-only first and falls back to immutable when the
	// filesystem refuses a read-only open.
	OpenModeAuto = "auto"
	// OpenModeReadOnly forces a read-only open.
	OpenModeReadOnly = "ro"
	// OpenModeImmutable forces SQLite's immutable mode, which ignores locking
	// and journals.
	OpenModeImmutable = "immutable"
	// OpenModeReadWrite opens the database read-write; it may recover a hot
	// journal, so it is opt-in.
	OpenModeReadWrite = "rw"
)

// CopyOptions configures MergeSQLiteIntoPostgres.
type CopyOptions struct {
	SQLitePaths []string
	PostgresDSN string
	BatchSize   int
	Workers     int
	Tables      []string
	SkipTables  []string
	FillMissing bool
	Analyze     bool
	VerifyKeys  bool
	DryRun      bool
	// VerifyOnly checks that every legacy key already exists in Postgres
	// without writing anything. It is the gate used before archiving a legacy
	// database file.
	VerifyOnly bool
	// TolerateSnapshotDrift excuses missing keys in the snapshotSourceTables
	// (see SnapshotDriftTables) instead of refusing the archive. Only those
	// tables are excused, and the excused key count is reported separately, so
	// every authoritative table is still verified strictly.
	TolerateSnapshotDrift bool
	OpenMode              string
	Progress              ProgressFunc
}

// toleratesMissingKeys reports whether a table's missing keys are excused.
func (o CopyOptions) toleratesMissingKeys(table string) bool {
	if !o.TolerateSnapshotDrift {
		return false
	}
	_, ok := snapshotSourceTables[table]
	return ok
}

// SnapshotDriftReason describes why a table's missing keys may be excused.
func SnapshotDriftReason(table string) string {
	return snapshotSourceTables[table]
}

// TableResult reports the outcome of one table.
type TableResult struct {
	Table             string   `json:"table"`
	Status            string   `json:"status"`
	Reason            string   `json:"reason,omitempty"`
	SourceRows        int64    `json:"source_rows"`
	Copied            int64    `json:"copied"`
	Duplicate         int64    `json:"duplicate"`
	Failed            int64    `json:"failed"`
	Columns           []string `json:"columns,omitempty"`
	MissingRequired   []string `json:"missing_required_columns,omitempty"`
	SampleFailures    []string `json:"sample_failures,omitempty"`
	MissingKeys       int64    `json:"missing_keys,omitempty"`
	SampleMissingKeys []string `json:"sample_missing_keys,omitempty"`
	// ToleratedKeys counts keys that are missing in Postgres but belong to a
	// snapshot table the operator explicitly excused with
	// TolerateSnapshotDrift. They never block an archive and are reported apart
	// from MissingKeys.
	ToleratedKeys int64  `json:"tolerated_keys,omitempty"`
	Sequence      string `json:"sequence,omitempty"`
	DurationMS    int64  `json:"duration_ms"`
}

// DatabaseResult reports the outcome of one legacy SQLite database.
type DatabaseResult struct {
	Path       string        `json:"path"`
	OpenMode   string        `json:"open_mode,omitempty"`
	SizeBytes  int64         `json:"size_bytes,omitempty"`
	Tables     []TableResult `json:"tables"`
	DurationMS int64         `json:"duration_ms"`
	Failed     int64         `json:"failed_tables,omitempty"`
	Err        string        `json:"error,omitempty"`
}

// CopyReport aggregates a full merge run.
type CopyReport struct {
	DryRun    bool             `json:"dry_run"`
	Databases []DatabaseResult `json:"databases"`
	Warnings  []string         `json:"warnings,omitempty"`
	Copied    int64            `json:"copied"`
	Duplicate int64            `json:"duplicate"`
	Failed    int64            `json:"failed"`
	Missing   int64            `json:"missing_keys"`
	// Tolerated sums the missing keys that TolerateSnapshotDrift excused. It is
	// reported so an operator can see exactly how much drift was accepted.
	Tolerated  int64 `json:"tolerated_keys,omitempty"`
	TablesDone int   `json:"tables_completed"`
	TablesSkip int   `json:"tables_skipped"`
	DurationMS int64 `json:"duration_ms"`
}

// OK reports whether every table was merged and verified without problems.
func (r *CopyReport) OK() bool {
	return r != nil && r.Failed == 0 && r.Missing == 0 && r.TablesSkip == 0
}

// SQLiteCandidate is a discovered legacy database file.
type SQLiteCandidate struct {
	Path    string    `json:"path"`
	Size    int64     `json:"size_bytes"`
	ModTime time.Time `json:"mod_time"`
}

// DiscoverLegacySQLiteFiles finds legacy application databases in dirs. Files
// are returned newest first so that the most recent database wins when two
// vintages contain a row with the same primary key.
func DiscoverLegacySQLiteFiles(dirs []string) []SQLiteCandidate {
	seen := map[string]bool{}
	var found []SQLiteCandidate
	for _, dir := range dirs {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		for _, name := range legacySQLiteNames {
			path := filepath.Join(dir, name)
			if seen[path] {
				continue
			}
			info, err := os.Stat(path)
			if err != nil || info.IsDir() {
				continue
			}
			seen[path] = true
			found = append(found, SQLiteCandidate{Path: path, Size: info.Size(), ModTime: info.ModTime()})
		}
	}
	sort.SliceStable(found, func(i, j int) bool {
		if found[i].ModTime.Equal(found[j].ModTime) {
			return found[i].Path < found[j].Path
		}
		return found[i].ModTime.After(found[j].ModTime)
	})
	return found
}

// MergeSQLiteIntoPostgres copies every row of the given legacy SQLite
// application databases into Postgres. Rows whose primary key (or any other
// unique key) already exists are skipped, so the merge is additive and
// idempotent.
func MergeSQLiteIntoPostgres(ctx context.Context, opts CopyOptions) (*CopyReport, error) {
	if strings.TrimSpace(opts.PostgresDSN) == "" {
		return nil, errors.New("a Postgres DSN is required to merge the legacy SQLite databases")
	}
	if len(opts.SQLitePaths) == 0 {
		return nil, errors.New("no SQLite database was selected")
	}
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = 500
	}
	workers := opts.Workers
	if workers <= 0 {
		workers = DefaultDatabaseWorkers()
	}
	openMode := opts.OpenMode
	if openMode == "" {
		openMode = OpenModeAuto
	}

	report := &CopyReport{DryRun: opts.DryRun}
	started := time.Now()

	pg, err := sql.Open("postgres", opts.PostgresDSN)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	defer pg.Close()
	pg.SetMaxOpenConns(workers + 2)
	pg.SetMaxIdleConns(workers + 2)
	if err := pg.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	if err := requireApplicationSchema(ctx, pg); err != nil {
		return nil, err
	}

	for _, path := range opts.SQLitePaths {
		result := DatabaseResult{Path: path}
		if info, statErr := os.Stat(path); statErr == nil {
			result.SizeBytes = info.Size()
		}
		dbStart := time.Now()
		source, mode, openErr := openLegacySQLite(ctx, path, openMode)
		if openErr != nil {
			result.Err = openErr.Error()
			result.DurationMS = time.Since(dbStart).Milliseconds()
			report.Databases = append(report.Databases, result)
			continue
		}
		result.OpenMode = mode
		tables, copyErr := copyDatabase(ctx, source, pg, path, batchSize, workers, opts, &result)
		source.Close()
		if copyErr != nil {
			result.Err = copyErr.Error()
		}
		result.Tables = tables
		for _, table := range tables {
			if table.Status == "failed" || table.Status == "blocked" {
				result.Failed++
			}
		}
		result.DurationMS = time.Since(dbStart).Milliseconds()
		report.Databases = append(report.Databases, result)
	}

	for _, database := range report.Databases {
		for _, table := range database.Tables {
			report.Copied += table.Copied
			report.Duplicate += table.Duplicate
			report.Failed += table.Failed
			report.Missing += table.MissingKeys
			report.Tolerated += table.ToleratedKeys
			switch table.Status {
			case "copied", "verified", "planned", "empty", "skipped":
				report.TablesDone++
			default:
				report.TablesSkip++
			}
		}
		if database.Err != "" {
			report.Warnings = append(report.Warnings, fmt.Sprintf("%s: %s", database.Path, database.Err))
		}
	}
	report.DurationMS = time.Since(started).Milliseconds()
	return report, nil
}

// requireApplicationSchema refuses to merge into a database that has no
// Trajecta application schema.
func requireApplicationSchema(ctx context.Context, pg *sql.DB) error {
	var exists bool
	err := pg.QueryRowContext(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = current_schema() AND table_name = 'logs')`).Scan(&exists)
	if err != nil {
		return fmt.Errorf("inspect postgres schema: %w", err)
	}
	if !exists {
		return errors.New("the Postgres database has no Trajecta application schema; run `server db migrate up` first")
	}
	return nil
}

// openLegacySQLite opens a legacy database read-only when possible. Some
// filesystems refuse a read-only open of a database that has no write-ahead
// log, so immutable mode is used as a fallback. Immutable mode is only safe
// when no journal is pending, which is verified first.
func openLegacySQLite(ctx context.Context, path, mode string) (*sql.DB, string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, "", err
	}
	query := url.Values{}
	query.Add("_pragma", "busy_timeout(5000)")

	var candidates []string
	switch mode {
	case OpenModeReadOnly:
		candidates = []string{"ro"}
	case OpenModeImmutable:
		candidates = []string{"immutable"}
	case OpenModeReadWrite:
		candidates = []string{"rw"}
	default:
		candidates = []string{"ro", "immutable"}
	}

	var lastErr error
	for _, candidate := range candidates {
		values := url.Values{}
		for key, list := range query {
			for _, value := range list {
				values.Add(key, value)
			}
		}
		switch candidate {
		case "ro":
			values.Set("mode", "ro")
		case "immutable":
			if err := requireNoPendingJournal(path); err != nil {
				lastErr = err
				continue
			}
			values.Set("immutable", "1")
		case "rw":
			values.Set("mode", "rw")
		}
		dsn := url.URL{Scheme: "file", Path: absolute, RawQuery: values.Encode()}
		db, err := sql.Open("sqlite", dsn.String())
		if err != nil {
			lastErr = err
			continue
		}
		db.SetMaxOpenConns(DefaultDatabaseWorkers())
		db.SetMaxIdleConns(DefaultDatabaseWorkers())
		if err := db.PingContext(ctx); err == nil {
			return db, candidate, nil
		} else {
			lastErr = err
		}
		db.Close()
	}
	if lastErr == nil {
		lastErr = errors.New("no usable SQLite open mode")
	}
	return nil, "", fmt.Errorf("open %s read-only: %w (stop the process that holds the database open, or pass --sqlite-open=rw to let SQLite recover it)", path, lastErr)
}

// requireNoPendingJournal refuses immutable mode when a journal may still hold
// committed data.
func requireNoPendingJournal(path string) error {
	for _, suffix := range []string{"-wal", "-journal"} {
		info, err := os.Stat(path + suffix)
		if err != nil {
			continue
		}
		if info.Size() > 0 {
			return fmt.Errorf("found a pending %s file (%d bytes); open the database once with the gateway or a SQLite client so it can be recovered before migrating",
				suffix, info.Size())
		}
	}
	return nil
}

// copyDatabase merges every table of one legacy database.
func copyDatabase(ctx context.Context, source, pg *sql.DB, path string, batchSize, workers int, opts CopyOptions, result *DatabaseResult) ([]TableResult, error) {
	tables, err := listSourceTables(ctx, source)
	if err != nil {
		return nil, err
	}
	targetColumns, err := targetTableColumns(ctx, pg)
	if err != nil {
		return nil, err
	}

	selected := make([]string, 0, len(tables))
	for _, table := range tables {
		if len(opts.Tables) > 0 && !containsFold(opts.Tables, table) {
			continue
		}
		if containsFold(opts.SkipTables, table) {
			continue
		}
		selected = append(selected, table)
	}
	if len(selected) == 0 {
		return nil, errors.New("no matching table found in the legacy database")
	}

	waves := scheduleWaves(selected, legacyTableDependencies)
	results := make([]TableResult, len(selected))
	index := make(map[string]int, len(selected))
	for i, table := range selected {
		index[table] = i
	}

	progressMu := &sync.Mutex{}
	emit := func(progress Progress) {
		if opts.Progress == nil {
			return
		}
		progressMu.Lock()
		defer progressMu.Unlock()
		opts.Progress(progress)
	}

	for _, wave := range waves {
		var wg sync.WaitGroup
		semaphore := make(chan struct{}, workers)
		for _, table := range wave {
			wg.Add(1)
			go func(table string) {
				defer wg.Done()
				semaphore <- struct{}{}
				defer func() { <-semaphore }()
				results[index[table]] = copyTable(ctx, source, pg, table, targetColumns[table], batchSize, opts, emit)
			}(table)
		}
		wg.Wait()
	}
	return results, nil
}

// scheduleWaves groups tables so that every table is copied after the tables it
// references.
func scheduleWaves(tables []string, dependencies map[string][]string) [][]string {
	remaining := make(map[string]bool, len(tables))
	for _, table := range tables {
		remaining[table] = true
	}
	var waves [][]string
	for len(remaining) > 0 {
		var wave []string
		for _, table := range tables {
			if !remaining[table] {
				continue
			}
			ready := true
			for _, dependency := range dependencies[table] {
				if remaining[dependency] {
					ready = false
					break
				}
			}
			if ready {
				wave = append(wave, table)
			}
		}
		if len(wave) == 0 {
			// dependency cycle: copy the rest in one final wave
			for _, table := range tables {
				if remaining[table] {
					wave = append(wave, table)
				}
			}
			for _, table := range wave {
				delete(remaining, table)
			}
			waves = append(waves, wave)
			break
		}
		for _, table := range wave {
			delete(remaining, table)
		}
		waves = append(waves, wave)
	}
	return waves
}

// copyTable merges one table.
func copyTable(ctx context.Context, source, pg *sql.DB, table string, target []Column, batchSize int, opts CopyOptions, emit func(Progress)) TableResult {
	started := time.Now()
	result := TableResult{Table: table, Status: "copied"}

	reason, skip := skippedSourceTables[table]
	if skip {
		result.Status = "skipped"
		result.Reason = reason
		result.DurationMS = time.Since(started).Milliseconds()
		return result
	}
	if len(target) == 0 {
		result.Status = "skipped"
		result.Reason = "target table does not exist in Postgres"
		result.DurationMS = time.Since(started).Milliseconds()
		return result
	}

	sourceColumns, err := sourceTableColumns(ctx, source, table)
	if err != nil {
		result.Status = "failed"
		result.Reason = err.Error()
		result.DurationMS = time.Since(started).Milliseconds()
		return result
	}

	targetByName := make(map[string]Column, len(target))
	for _, column := range target {
		targetByName[column.Name] = column
	}

	insertColumns := make([]Column, 0, len(sourceColumns)+4)
	var fillers []Column
	present := make(map[string]bool, len(sourceColumns))
	for _, column := range sourceColumns {
		targetColumn, ok := targetByName[column.Name]
		if !ok {
			continue
		}
		insertColumns = append(insertColumns, targetColumn)
		present[column.Name] = true
	}
	for _, column := range target {
		if present[column.Name] || !column.Required() {
			continue
		}
		result.MissingRequired = append(result.MissingRequired, column.Name)
		if opts.FillMissing {
			fillers = append(fillers, column)
		}
	}
	if len(insertColumns) == 0 {
		result.Status = "skipped"
		result.Reason = "no shared column between the legacy table and Postgres"
		result.MissingRequired = nil
		result.DurationMS = time.Since(started).Milliseconds()
		return result
	}
	if len(result.MissingRequired) > 0 && !opts.FillMissing {
		result.Status = "blocked"
		result.Reason = fmt.Sprintf("required Postgres columns are missing from the legacy table: %s", strings.Join(result.MissingRequired, ", "))
		result.DurationMS = time.Since(started).Milliseconds()
		return result
	}

	names := make([]string, 0, len(insertColumns)+len(fillers))
	for _, column := range insertColumns {
		names = append(names, column.Name)
	}
	for _, column := range fillers {
		names = append(names, column.Name)
	}
	result.Columns = append(result.Columns, names...)

	sourceRows, err := countRows(ctx, source, table)
	if err != nil {
		result.Status = "failed"
		result.Reason = err.Error()
		result.DurationMS = time.Since(started).Milliseconds()
		return result
	}
	result.SourceRows = sourceRows
	if sourceRows == 0 {
		result.Status = "empty"
		result.DurationMS = time.Since(started).Milliseconds()
		return result
	}
	if opts.DryRun {
		result.Status = "planned"
		result.DurationMS = time.Since(started).Milliseconds()
		return result
	}
	if opts.VerifyOnly {
		missing, samples, verifyErr := verifySourceKeys(ctx, source, pg, table, target)
		result.SourceRows = sourceRows
		if verifyErr != nil {
			result.Status = "failed"
			result.Reason = verifyErr.Error()
		} else {
			result.MissingKeys = missing
			result.SampleMissingKeys = samples
			result.Status = "verified"
			if missing > 0 {
				if opts.toleratesMissingKeys(table) {
					// The rows were replaced by the running server rather than
					// lost; keep the samples for the report but do not let the
					// table block the archive.
					result.ToleratedKeys = missing
					result.MissingKeys = 0
					result.Reason = snapshotSourceTables[table]
				} else {
					result.Status = "partial"
				}
			}
		}
		result.DurationMS = time.Since(started).Milliseconds()
		return result
	}

	effectiveBatch := batchSize
	if maxRows := 65535 / len(names); effectiveBatch > maxRows && maxRows > 0 {
		effectiveBatch = maxRows
	}
	if effectiveBatch < 1 {
		effectiveBatch = 1
	}

	copied, duplicate, failed, failures, err := streamTable(ctx, source, pg, table, insertColumns, fillers, effectiveBatch, sourceRows, emit)
	result.Copied = copied
	result.Duplicate = duplicate
	result.Failed = failed
	result.SampleFailures = failures
	if err != nil {
		result.Status = "failed"
		result.Reason = err.Error()
		result.DurationMS = time.Since(started).Milliseconds()
		return result
	}
	if failed > 0 {
		result.Status = "partial"
	}

	if columns, seqErr := targetIdentityColumns(ctx, pg, table); seqErr == nil && len(columns) > 0 {
		actions, fixErr := fixSequences(ctx, pg, table, columns)
		if fixErr != nil {
			result.Sequence = fixErr.Error()
		} else {
			result.Sequence = strings.Join(actions, "; ")
		}
	}

	if opts.VerifyKeys {
		missing, samples, verifyErr := verifySourceKeys(ctx, source, pg, table, target)
		if verifyErr != nil {
			result.Status = "partial"
			result.SampleFailures = append(result.SampleFailures, "verify: "+verifyErr.Error())
		} else {
			result.MissingKeys = missing
			result.SampleMissingKeys = samples
			if missing > 0 {
				result.Status = "partial"
			}
		}
	}

	if opts.Analyze && result.Status != "failed" {
		if _, analyzeErr := pg.ExecContext(ctx, `ANALYZE `+quoteIdent(table)); analyzeErr != nil {
			// analyzing is best effort, it never invalidates a completed copy
			result.SampleFailures = append(result.SampleFailures, "analyze: "+analyzeErr.Error())
		}
	}

	if result.Status == "copied" && result.SourceRows == result.Copied+result.Duplicate {
		result.Status = "verified"
	}
	result.DurationMS = time.Since(started).Milliseconds()
	return result
}

// streamTable reads the legacy table once and inserts it in batches.
func streamTable(ctx context.Context, source, pg *sql.DB, table string, insertColumns, fillers []Column, batchSize int, total int64, emit func(Progress)) (int64, int64, int64, []string, error) {
	selectColumns := make([]string, 0, len(insertColumns))
	for _, column := range insertColumns {
		selectColumns = append(selectColumns, quoteIdent(column.Name))
	}
	query := fmt.Sprintf("SELECT %s FROM %s", strings.Join(selectColumns, ", "), quoteIdent(table))
	rows, err := source.QueryContext(ctx, query)
	if err != nil {
		return 0, 0, 0, nil, fmt.Errorf("read %s: %w", table, err)
	}
	defer rows.Close()

	fillerValues := make([]any, len(fillers))
	for i, column := range fillers {
		value, zeroErr := ZeroValue(column)
		if zeroErr != nil {
			return 0, 0, 0, nil, zeroErr
		}
		fillerValues[i] = value
	}

	names := make([]string, 0, len(insertColumns)+len(fillers))
	for _, column := range insertColumns {
		names = append(names, column.Name)
	}
	for _, column := range fillers {
		names = append(names, column.Name)
	}

	var (
		copied    int64
		duplicate int64
		failed    int64
		processed int64
		failures  []string
		batch     [][]any
		progress  = newProgressReporter(func(p Progress) {
			emit(p)
		}, 2*time.Second)
	)

	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		batchCopied, batchDuplicate, batchFailed, batchFailures, flushErr := insertBatch(ctx, pg, table, names, batch)
		copied += batchCopied
		duplicate += batchDuplicate
		failed += batchFailed
		for _, failure := range batchFailures {
			if len(failures) < maxReportedFailures {
				failures = append(failures, failure)
			}
		}
		batch = batch[:0]
		if flushErr != nil {
			return flushErr
		}
		progress.report(processed, func() Progress {
			return Progress{Phase: "copy", Table: table, Processed: processed, Total: total, Copied: copied, Duplicate: duplicate, Failed: failed}
		})
		return nil
	}

	for rows.Next() {
		if err := ctx.Err(); err != nil {
			return copied, duplicate, failed, failures, err
		}
		values := make([]any, len(insertColumns))
		pointers := make([]any, len(insertColumns))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return copied, duplicate, failed, failures, fmt.Errorf("scan %s: %w", table, err)
		}
		processed++
		converted := make([]any, 0, len(names))
		bad := false
		for i, column := range insertColumns {
			value, convErr := ConvertValue(column, values[i])
			if convErr != nil {
				failed++
				if len(failures) < maxReportedFailures {
					failures = append(failures, fmt.Sprintf("row %d column %s: %v", processed, column.Name, convErr))
				}
				bad = true
				break
			}
			converted = append(converted, cloneValue(value))
		}
		if bad {
			continue
		}
		converted = append(converted, fillerValues...)
		batch = append(batch, converted)
		if len(batch) >= batchSize {
			if err := flush(); err != nil {
				return copied, duplicate, failed, failures, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return copied, duplicate, failed, failures, fmt.Errorf("read %s: %w", table, err)
	}
	if err := flush(); err != nil {
		return copied, duplicate, failed, failures, err
	}
	return copied, duplicate, failed, failures, nil
}

// insertBatch inserts one batch with ON CONFLICT DO NOTHING. When Postgres
// rejects the batch because of a single bad row, the rows are retried
// individually so the failure can be reported precisely.
func insertBatch(ctx context.Context, pg *sql.DB, table string, names []string, batch [][]any) (int64, int64, int64, []string, error) {
	if len(batch) == 0 {
		return 0, 0, 0, nil, nil
	}
	statement := buildInsert(table, names, len(batch))
	args := make([]any, 0, len(batch)*len(names))
	for _, row := range batch {
		args = append(args, row...)
	}
	result, err := pg.ExecContext(ctx, statement, args...)
	if err == nil {
		affected, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return 0, 0, 0, nil, rowsErr
		}
		return affected, int64(len(batch)) - affected, 0, nil, nil
	}
	if !isRecoverableRowError(err) {
		return 0, 0, 0, nil, fmt.Errorf("insert into %s: %w", table, err)
	}
	if len(batch) == 1 {
		if isUniqueViolation(err) {
			return 0, 1, 0, nil, nil
		}
		return 0, 0, 1, []string{fmt.Sprintf("row: %v", err)}, nil
	}

	// isolate the offending rows
	single := buildInsert(table, names, 1)
	var (
		copied    int64
		duplicate int64
		failed    int64
		failures  []string
	)
	for _, row := range batch {
		result, rowErr := pg.ExecContext(ctx, single, row...)
		if rowErr == nil {
			affected, rowsErr := result.RowsAffected()
			if rowsErr != nil {
				return copied, duplicate, failed, failures, rowsErr
			}
			copied += affected
			duplicate += 1 - affected
			continue
		}
		if isUniqueViolation(rowErr) {
			duplicate++
			continue
		}
		failed++
		if len(failures) < maxReportedFailures {
			failures = append(failures, fmt.Sprintf("%v (row %v)", rowErr, rowKey(names, row)))
		}
	}
	return copied, duplicate, failed, failures, nil
}

// rowKey renders the primary-key-ish prefix of a row for failure reports.
func rowKey(names []string, row []any) string {
	limit := len(names)
	if limit > 3 {
		limit = 3
	}
	parts := make([]string, 0, limit)
	for i := 0; i < limit; i++ {
		parts = append(parts, fmt.Sprintf("%s=%v", names[i], row[i]))
	}
	return strings.Join(parts, ",")
}

func buildInsert(table string, names []string, rows int) string {
	quoted := make([]string, len(names))
	for i, name := range names {
		quoted[i] = quoteIdent(name)
	}
	placeholders := make([]string, 0, rows)
	index := 1
	for row := 0; row < rows; row++ {
		values := make([]string, len(names))
		for col := range names {
			values[col] = fmt.Sprintf("$%d", index)
			index++
		}
		placeholders = append(placeholders, "("+strings.Join(values, ",")+")")
	}
	return fmt.Sprintf("INSERT INTO %s (%s) VALUES %s ON CONFLICT DO NOTHING",
		quoteIdent(table), strings.Join(quoted, ","), strings.Join(placeholders, ","))
}

// isRecoverableRowError reports whether a failed batch should be retried row by
// row. Data exceptions and integrity violations are row-local; anything else
// (a lost connection, for example) aborts the table.
func isRecoverableRowError(err error) bool {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		code := string(pqErr.Code)
		if len(code) < 2 {
			return false
		}
		class := code[:2]
		return class == "22" || class == "23"
	}
	return false
}

func isUniqueViolation(err error) bool {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return pqErr.Code == "23505"
	}
	return false
}

// cloneValue copies byte slices so buffered rows do not alias driver memory.
func cloneValue(value any) any {
	if data, ok := value.([]byte); ok && data != nil {
		cloned := make([]byte, len(data))
		copy(cloned, data)
		return cloned
	}
	return value
}

// ---- introspection -------------------------------------------------------

func listSourceTables(ctx context.Context, source *sql.DB) ([]string, error) {
	rows, err := source.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list legacy tables: %w", err)
	}
	defer rows.Close()
	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		tables = append(tables, name)
	}
	return tables, rows.Err()
}

func sourceTableColumns(ctx context.Context, source *sql.DB, table string) ([]Column, error) {
	rows, err := source.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return nil, fmt.Errorf("read legacy columns of %s: %w", table, err)
	}
	defer rows.Close()
	var columns []Column
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		columns = append(columns, Column{Name: name})
	}
	return columns, rows.Err()
}

func countRows(ctx context.Context, source *sql.DB, table string) (int64, error) {
	var count int64
	if err := source.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoteIdent(table)).Scan(&count); err != nil {
		return 0, fmt.Errorf("count %s: %w", table, err)
	}
	return count, nil
}

// targetTableColumns maps every table of the target schema to its columns.
func targetTableColumns(ctx context.Context, pg *sql.DB) (map[string][]Column, error) {
	rows, err := pg.QueryContext(ctx, `
		SELECT table_name, column_name, data_type, udt_name, is_nullable, column_default, is_identity
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		ORDER BY table_name, ordinal_position`)
	if err != nil {
		return nil, fmt.Errorf("inspect postgres columns: %w", err)
	}
	defer rows.Close()
	result := map[string][]Column{}
	for rows.Next() {
		var (
			table      string
			column     string
			dataType   string
			udtName    string
			isNullable string
			defaultVal sql.NullString
			isIdentity string
		)
		if err := rows.Scan(&table, &column, &dataType, &udtName, &isNullable, &defaultVal, &isIdentity); err != nil {
			return nil, err
		}
		result[table] = append(result[table], Column{
			Name:       column,
			DataType:   dataType,
			UDTName:    udtName,
			Nullable:   strings.EqualFold(isNullable, "YES"),
			HasDefault: defaultVal.Valid && strings.TrimSpace(defaultVal.String) != "",
			IsIdentity: strings.EqualFold(strings.TrimSpace(isIdentity), "YES") ||
				(defaultVal.Valid && strings.HasPrefix(defaultVal.String, "nextval(")),
		})
	}
	return result, rows.Err()
}

// targetIdentityColumns returns the identity/serial columns of a target table.
func targetIdentityColumns(ctx context.Context, pg *sql.DB, table string) ([]string, error) {
	var columns []string
	err := func() error {
		rows, err := pg.QueryContext(ctx, `
			SELECT column_name
			FROM information_schema.columns
			WHERE table_schema = current_schema() AND table_name = $1
			  AND (is_identity = 'YES' OR column_default LIKE 'nextval(%')
			ORDER BY ordinal_position`, table)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var name string
			if err := rows.Scan(&name); err != nil {
				return err
			}
			columns = append(columns, name)
		}
		return rows.Err()
	}()
	return columns, err
}

// fixSequences advances identity sequences past the ids copied from the legacy
// database. A sequence is never moved backwards, so the START offsets chosen by
// the schema keep protecting freshly generated ids.
func fixSequences(ctx context.Context, pg *sql.DB, table string, columns []string) ([]string, error) {
	var actions []string
	for _, column := range columns {
		var sequence sql.NullString
		if err := pg.QueryRowContext(ctx, `SELECT pg_get_serial_sequence($1, $2)`, table, column).Scan(&sequence); err != nil {
			return actions, fmt.Errorf("resolve sequence for %s.%s: %w", table, column, err)
		}
		if !sequence.Valid || strings.TrimSpace(sequence.String) == "" {
			continue
		}
		var maxID sql.NullInt64
		if err := pg.QueryRowContext(ctx, fmt.Sprintf(`SELECT MAX(%s) FROM %s`, quoteIdent(column), quoteIdent(table))).Scan(&maxID); err != nil {
			return actions, fmt.Errorf("read max %s.%s: %w", table, column, err)
		}
		if !maxID.Valid {
			continue
		}
		var lastValue int64
		if err := pg.QueryRowContext(ctx, `SELECT last_value FROM `+quoteQualified(sequence.String)).Scan(&lastValue); err != nil {
			return actions, fmt.Errorf("read sequence %s: %w", sequence.String, err)
		}
		if maxID.Int64 <= lastValue {
			continue
		}
		if _, err := pg.ExecContext(ctx, `SELECT setval($1::regclass, $2, true)`, sequence.String, maxID.Int64); err != nil {
			return actions, fmt.Errorf("advance sequence %s: %w", sequence.String, err)
		}
		actions = append(actions, fmt.Sprintf("%s set to %d", sequence.String, maxID.Int64))
	}
	return actions, nil
}

// verifySourceKeys checks that every primary key of the legacy table exists in
// the target table. This is the gate used before the SQLite files are archived.
func verifySourceKeys(ctx context.Context, source, pg *sql.DB, table string, target []Column) (int64, []string, error) {
	targetByName := make(map[string]Column, len(target))
	for _, column := range target {
		targetByName[column.Name] = column
	}
	sourceColumns, err := sourceTableColumns(ctx, source, table)
	if err != nil {
		return 0, nil, err
	}
	var keyNames []string
	for _, column := range sourceColumns {
		if _, ok := targetByName[column.Name]; !ok {
			continue
		}
		if isTargetPrimaryKey(ctx, pg, table, column.Name) {
			keyNames = append(keyNames, column.Name)
		}
	}
	if len(keyNames) == 0 {
		// No usable key: the row accounting of the copy itself is the gate.
		return 0, nil, nil
	}

	selectKey := make([]string, len(keyNames))
	for i, name := range keyNames {
		selectKey[i] = quoteIdent(name)
	}
	rows, err := source.QueryContext(ctx, fmt.Sprintf("SELECT %s FROM %s", strings.Join(selectKey, ", "), quoteIdent(table)))
	if err != nil {
		return 0, nil, err
	}
	defer rows.Close()

	const batchSize = 400
	var (
		missing int64
		samples []string
		batch   [][]any
	)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		found, queryErr := existingKeys(ctx, pg, table, keyNames, batch)
		if queryErr != nil {
			return queryErr
		}
		for _, key := range batch {
			rendered := renderKey(keyNames, key)
			if !found[rendered] {
				missing++
				if len(samples) < maxReportedFailures {
					samples = append(samples, rendered)
				}
			}
		}
		batch = batch[:0]
		return nil
	}
	for rows.Next() {
		values := make([]any, len(keyNames))
		pointers := make([]any, len(keyNames))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return missing, samples, err
		}
		key := make([]any, 0, len(keyNames))
		for _, value := range values {
			converted, convErr := convertKeyValue(value)
			if convErr != nil {
				key = append(key, value)
				continue
			}
			key = append(key, converted)
		}
		batch = append(batch, key)
		if len(batch) >= batchSize {
			if err := flush(); err != nil {
				return missing, samples, err
			}
		}
	}
	if err := rows.Err(); err != nil {
		return missing, samples, err
	}
	if err := flush(); err != nil {
		return missing, samples, err
	}
	return missing, samples, nil
}

func existingKeys(ctx context.Context, pg *sql.DB, table string, keyNames []string, keys [][]any) (map[string]bool, error) {
	found := make(map[string]bool, len(keys))
	quoted := make([]string, len(keyNames))
	for i, name := range keyNames {
		quoted[i] = quoteIdent(name)
	}
	projection := strings.Join(quoted, ", ")
	if len(keyNames) == 1 {
		projection = quoted[0]
	}

	args := make([]any, 0, len(keys)*len(keyNames))
	placeholders := make([]string, 0, len(keys))
	index := 1
	for _, key := range keys {
		values := make([]string, len(key))
		for i := range key {
			values[i] = fmt.Sprintf("$%d", index)
			index++
		}
		if len(key) == 1 {
			placeholders = append(placeholders, values[0])
		} else {
			placeholders = append(placeholders, "("+strings.Join(values, ",")+")")
		}
		args = append(args, key...)
	}

	statement := fmt.Sprintf("SELECT %s FROM %s WHERE ", projection, quoteIdent(table))
	if len(keyNames) == 1 {
		statement += fmt.Sprintf("%s IN (%s)", quoted[0], strings.Join(placeholders, ","))
	} else {
		statement += fmt.Sprintf("(%s) IN (%s)", projection, strings.Join(placeholders, ","))
	}

	rows, err := pg.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		values := make([]any, len(keyNames))
		pointers := make([]any, len(keyNames))
		for i := range values {
			pointers[i] = &values[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		key := make([]any, 0, len(keyNames))
		for _, value := range values {
			key = append(key, normalizeKeyValue(value))
		}
		found[renderKey(keyNames, key)] = true
	}
	return found, rows.Err()
}

// convertKeyValue normalizes a legacy key value for comparison with Postgres.
func convertKeyValue(value any) (any, error) {
	switch typed := value.(type) {
	case []byte:
		return string(typed), nil
	case int64:
		return typed, nil
	case float64:
		return typed, nil
	case string:
		return typed, nil
	case nil:
		return nil, nil
	default:
		return typed, nil
	}
}

func normalizeKeyValue(value any) any {
	switch typed := value.(type) {
	case []byte:
		return string(typed)
	case int64:
		return typed
	case string:
		return typed
	case nil:
		return nil
	default:
		return typed
	}
}

// renderKey builds a comparable text form of a key tuple.
func renderKey(names []string, values []any) string {
	parts := make([]string, 0, len(values))
	for i, value := range values {
		name := ""
		if i < len(names) {
			name = names[i]
		}
		parts = append(parts, fmt.Sprintf("%s=%v", name, normalizeKeyValue(value)))
	}
	return strings.Join(parts, "|")
}

func isTargetPrimaryKey(ctx context.Context, pg *sql.DB, table, column string) bool {
	var exists bool
	err := pg.QueryRowContext(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM information_schema.table_constraints tc
			JOIN information_schema.key_column_usage kcu
			  ON tc.constraint_name = kcu.constraint_name AND tc.table_schema = kcu.table_schema
			WHERE tc.table_schema = current_schema()
			  AND tc.table_name = $1
			  AND tc.constraint_type = 'PRIMARY KEY'
			  AND kcu.column_name = $2
		)`, table, column).Scan(&exists)
	if err != nil {
		return false
	}
	return exists
}

// ---- identifiers ---------------------------------------------------------

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

// quoteQualified quotes every part of a possibly schema-qualified identifier.
func quoteQualified(name string) string {
	parts := strings.Split(name, ".")
	for i, part := range parts {
		parts[i] = quoteIdent(part)
	}
	return strings.Join(parts, ".")
}

func containsFold(values []string, target string) bool {
	for _, value := range values {
		if strings.EqualFold(strings.TrimSpace(value), target) {
			return true
		}
	}
	return false
}
