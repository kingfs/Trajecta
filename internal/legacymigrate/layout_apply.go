package legacymigrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Layout apply statuses reported for one planned move.
const (
	// ApplyMoved means the cassette was renamed to its planned target (in a dry
	// run: the file would be renamed).
	ApplyMoved = "moved"
	// ApplyResumed means the file was already at the target path but the trace
	// index still pointed at the source, so only the index was repointed. This
	// is the state left behind by an interrupted apply.
	ApplyResumed = "resumed"
	// ApplyAlreadyApplied means source and target already reflect the plan.
	ApplyAlreadyApplied = "already-applied"
	// ApplyMissingSource means the planned source does not exist and neither
	// does the target.
	ApplyMissingSource = "missing-source"
	// ApplyTargetExists means source and target both exist; nothing is
	// overwritten.
	ApplyTargetExists = "target-exists"
	// ApplyFailed means the move was refused or rolled back.
	ApplyFailed = "failed"
)

// ApplyOptions configures ApplyCassetteLayout.
type ApplyOptions struct {
	// Root is the cassette vault root on this filesystem.
	Root string
	// DBPrefix is the prefix under which the database stores cassette paths.
	// Empty means "the same path as Root". It exists because a server running in
	// a container stores "/app/data/traces/..." while the vault lives elsewhere
	// on the host.
	DBPrefix string
	// PostgresDSN is the trace index database. Empty disables index updates.
	PostgresDSN string
	// RequireDatabase refuses an apply run without a database, because moving
	// files without repointing the index leaves every trace detail view broken.
	RequireDatabase bool
	// Moves is the reviewed plan, with paths relative to Root and slash
	// separated.
	Moves []LayoutMove
	// Workers is the number of concurrent move+index pairs.
	Workers int
	// Progress receives throttled progress observations.
	Progress ProgressFunc
	// DryRun reports what would happen and touches nothing.
	DryRun bool
	// VerifyModel re-reads each cassette prelude and refuses to move a file
	// whose recorded model no longer matches the plan.
	VerifyModel bool
	// OnlyModels restricts the plan to these recorded model names.
	OnlyModels []string
	// Limit caps how many moves are applied; the rest is reported as remaining.
	Limit int
	// Sample caps how many applied moves are kept in the report.
	Sample int
}

// PathRefs counts the index rows that store one cassette path.
type PathRefs struct {
	Logs      int64 `json:"logs,omitempty"`
	Exchanges int64 `json:"upstream_exchanges,omitempty"`
}

// Rows is the total number of index rows that reference the cassette.
func (r PathRefs) Rows() int64 { return r.Logs + r.Exchanges }

// ApplyMoveResult is the outcome of one planned move.
type ApplyMoveResult struct {
	From   string   `json:"from"`
	To     string   `json:"to"`
	Model  string   `json:"model,omitempty"`
	Status string   `json:"status"`
	Bytes  int64    `json:"bytes,omitempty"`
	Refs   PathRefs `json:"database_rows,omitempty"`
}

// ApplyFailure is a planned move that needs attention.
type ApplyFailure struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Stage   string `json:"stage"`
	Message string `json:"message"`
}

// LayoutApplyReport is the result of an apply run.
type LayoutApplyReport struct {
	Root      string `json:"root"`
	DBPrefix  string `json:"db_prefix"`
	DryRun    bool   `json:"dry_run"`
	Database  bool   `json:"database"`
	Planned   int64  `json:"planned"`
	Selected  int64  `json:"selected"`
	Remaining int64  `json:"remaining"`

	Moved          int64 `json:"moved"`
	Resumed        int64 `json:"resumed"`
	AlreadyApplied int64 `json:"already_applied"`
	MissingSource  int64 `json:"missing_source"`
	TargetExists   int64 `json:"target_exists"`
	MovedBytes     int64 `json:"moved_bytes"`

	// DatabaseRows counts index rows repointed (in a dry run: that would be
	// repointed). CassettesWithoutIndex counts moved cassettes that had no
	// logs row at all, which is expected for files recorded after the last
	// index sync.
	DatabaseRows          int64 `json:"database_rows"`
	CassettesWithoutIndex int64 `json:"cassettes_without_index"`

	FailedMoves int64             `json:"failed_moves"`
	Samples     []ApplyMoveResult `json:"samples,omitempty"`
	Failures    []ApplyFailure    `json:"failures,omitempty"`
	DurationMS  int64             `json:"duration_ms"`
	Note        string            `json:"note,omitempty"`
}

// Failed reports whether any planned move needs human attention.
func (r *LayoutApplyReport) Failed() bool { return r != nil && r.FailedMoves > 0 }

// cassettePathIndex is the part of the trace index that stores cassette paths.
type cassettePathIndex interface {
	CountRefs(ctx context.Context, cassettePath string) (PathRefs, error)
	MovePath(ctx context.Context, oldPath, newPath string) (PathRefs, error)
	Close() error
}

// openCassettePathIndex is a seam for tests, which substitute an in-memory
// index instead of a real Postgres.
var openCassettePathIndex = func(ctx context.Context, dsn string) (cassettePathIndex, error) {
	return openPostgresPathIndex(ctx, dsn)
}

// postgresPathIndex repoints the index columns that hold a cassette path.
// logs.path is the primary key of the trace index and upstream_exchanges holds an
// optional copy. Nothing else in the schema stores a cassette path:
// parse_jobs/analysis_jobs reference traces by UUID, and request_audits.path is
// an HTTP path. The Overview's hourly bucket table used to be repointed here too;
// it is no longer written or read, and a database that has already dropped it
// must not make `layout apply` fail, so it is left alone.
type postgresPathIndex struct{ db *sql.DB }

func openPostgresPathIndex(ctx context.Context, dsn string) (*postgresPathIndex, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	db.SetMaxOpenConns(clampInt(defaultWorkers(), 2, 16))
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("connect postgres: %w", err)
	}
	if err := requireApplicationSchema(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &postgresPathIndex{db: db}, nil
}

func (p *postgresPathIndex) Close() error { return p.db.Close() }

func (p *postgresPathIndex) CountRefs(ctx context.Context, cassettePath string) (PathRefs, error) {
	var refs PathRefs
	err := p.db.QueryRowContext(ctx,
		`SELECT (SELECT count(*) FROM logs WHERE path = $1),
		        (SELECT count(*) FROM upstream_exchanges WHERE cassette_path = $1)`,
		cassettePath).Scan(&refs.Logs, &refs.Exchanges)
	if err != nil {
		return PathRefs{}, fmt.Errorf("count index rows for %s: %w", cassettePath, err)
	}
	return refs, nil
}

// MovePath repoints every index row of one cassette inside a single
// transaction. A unique violation on logs.path means another row already claims
// the target path; the caller then moves the file back.
func (p *postgresPathIndex) MovePath(ctx context.Context, oldPath, newPath string) (PathRefs, error) {
	var refs PathRefs
	tx, err := p.db.BeginTx(ctx, nil)
	if err != nil {
		return refs, fmt.Errorf("begin index transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	steps := []struct {
		label  string
		target *int64
		query  string
	}{
		{"logs.path", &refs.Logs, `UPDATE logs SET path = $1 WHERE path = $2`},
		{"upstream_exchanges.cassette_path", &refs.Exchanges, `UPDATE upstream_exchanges SET cassette_path = $1 WHERE cassette_path = $2`},
	}
	for _, step := range steps {
		result, err := tx.ExecContext(ctx, step.query, newPath, oldPath)
		if err != nil {
			if isUniqueViolation(err) {
				return PathRefs{}, fmt.Errorf("%s already has a row for %s: %w", step.label, newPath, err)
			}
			return PathRefs{}, fmt.Errorf("update %s: %w", step.label, err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return PathRefs{}, fmt.Errorf("count updated %s rows: %w", step.label, err)
		}
		*step.target = affected
	}
	if err := tx.Commit(); err != nil {
		return PathRefs{}, fmt.Errorf("commit index transaction: %w", err)
	}
	return refs, nil
}

// layoutApplyTask is one validated plan entry.
type layoutApplyTask struct {
	move   LayoutMove
	from   string
	to     string
	dbFrom string
	dbTo   string
}

// ApplyCassetteLayout executes a reviewed move plan: it renames each cassette
// and repoints the trace index in the same step, so the filesystem and the
// index never disagree. Every per-file failure rolls the file move back and is
// reported instead of being silently skipped.
func ApplyCassetteLayout(ctx context.Context, opts ApplyOptions) (*LayoutApplyReport, error) {
	root, err := resolveLayoutRoot(opts.Root)
	if err != nil {
		return nil, err
	}
	dsn := strings.TrimSpace(opts.PostgresDSN)
	if !opts.DryRun && opts.RequireDatabase && dsn == "" {
		return nil, errors.New("no Postgres DSN resolved; moving cassettes without repointing the trace index would break every detail view - pass --no-db to move files without updating the index")
	}
	dbPrefix := strings.TrimRight(filepath.ToSlash(strings.TrimSpace(opts.DBPrefix)), "/")
	if dbPrefix == "" {
		dbPrefix = strings.TrimRight(filepath.ToSlash(root), "/")
	}

	tasks, remaining, err := buildLayoutApplyTasks(root, dbPrefix, opts)
	if err != nil {
		return nil, err
	}

	report := &LayoutApplyReport{
		Root:      root,
		DBPrefix:  dbPrefix,
		DryRun:    opts.DryRun,
		Database:  dsn != "",
		Planned:   int64(len(opts.Moves)),
		Selected:  int64(len(tasks)),
		Remaining: remaining,
	}
	started := time.Now()

	var index cassettePathIndex
	if dsn != "" {
		index, err = openCassettePathIndex(ctx, dsn)
		if err != nil {
			return nil, err
		}
		defer index.Close()
	}

	var (
		moved, resumed, already, missing, conflict, failed atomic.Int64
		movedBytes, dbRows, dbMissing                      atomic.Int64
	)
	sampleCap := opts.Sample
	if sampleCap <= 0 {
		sampleCap = 10
	}
	var mu sync.Mutex
	keepSample := func(result ApplyMoveResult) {
		mu.Lock()
		defer mu.Unlock()
		if len(report.Samples) < sampleCap {
			report.Samples = append(report.Samples, result)
		}
	}
	keepFailure := func(failure ApplyFailure) {
		mu.Lock()
		defer mu.Unlock()
		if len(report.Failures) < maxReportedFailures {
			report.Failures = append(report.Failures, failure)
		}
	}

	var processed atomic.Int64
	progress := newProgressReporter(opts.Progress, 2*time.Second)
	workers := clampInt(opts.Workers, 1, 32)
	work := make(chan layoutApplyTask)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for task := range work {
				result, refs, failure := applyOneLayoutMove(ctx, index, opts, task)
				switch result.Status {
				case ApplyMoved:
					moved.Add(1)
					movedBytes.Add(result.Bytes)
				case ApplyResumed:
					resumed.Add(1)
				case ApplyAlreadyApplied:
					already.Add(1)
				case ApplyMissingSource:
					missing.Add(1)
				case ApplyTargetExists:
					conflict.Add(1)
				}
				if failure != nil {
					failed.Add(1)
					keepFailure(*failure)
				}
				if index != nil && (result.Status == ApplyMoved || result.Status == ApplyResumed) {
					dbRows.Add(refs.Rows())
					if result.Status == ApplyMoved && refs.Logs == 0 {
						dbMissing.Add(1)
					}
				}
				if result.Status == ApplyMoved || result.Status == ApplyResumed {
					keepSample(result)
				}
				done := processed.Add(1)
				progress.report(done, func() Progress {
					return Progress{
						Phase:     "layout-apply",
						Processed: done,
						Total:     report.Selected,
						Copied:    moved.Load(),
						Failed:    failed.Load(),
					}
				})
			}
		}()
	}

	var cancelled error
feed:
	for _, task := range tasks {
		select {
		case <-ctx.Done():
			cancelled = ctx.Err()
			break feed
		case work <- task:
		}
	}
	close(work)
	wg.Wait()

	report.Moved = moved.Load()
	report.Resumed = resumed.Load()
	report.AlreadyApplied = already.Load()
	report.MissingSource = missing.Load()
	report.TargetExists = conflict.Load()
	report.MovedBytes = movedBytes.Load()
	report.DatabaseRows = dbRows.Load()
	report.CassettesWithoutIndex = dbMissing.Load()
	report.FailedMoves = failed.Load()
	report.DurationMS = time.Since(started).Milliseconds()
	if opts.DryRun {
		report.Note = "dry run: nothing was created, moved, deleted or written to the database"
	}
	if cancelled != nil {
		return report, cancelled
	}
	return report, nil
}

// buildLayoutApplyTasks validates the whole plan before anything is touched, so
// a malformed plan fails before the first rename.
func buildLayoutApplyTasks(root, dbPrefix string, opts ApplyOptions) ([]layoutApplyTask, int64, error) {
	only := make(map[string]bool)
	for _, value := range opts.OnlyModels {
		for _, part := range strings.Split(value, ",") {
			if model := normalizeLayoutModel(part); model != "" {
				only[model] = true
			}
		}
	}
	selected := make([]LayoutMove, 0, len(opts.Moves))
	for _, move := range opts.Moves {
		if len(only) > 0 && !only[normalizeLayoutModel(move.Model)] {
			continue
		}
		selected = append(selected, move)
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i].From < selected[j].From })

	remaining := int64(0)
	if opts.Limit > 0 && len(selected) > opts.Limit {
		remaining = int64(len(selected) - opts.Limit)
		selected = selected[:opts.Limit]
	}

	tasks := make([]layoutApplyTask, 0, len(selected))
	for _, move := range selected {
		from, err := cleanLayoutRelativePath(move.From)
		if err != nil {
			return nil, 0, fmt.Errorf("plan source %q: %w", move.From, err)
		}
		to, err := cleanLayoutRelativePath(move.To)
		if err != nil {
			return nil, 0, fmt.Errorf("plan target %q: %w", move.To, err)
		}
		if from == to {
			return nil, 0, fmt.Errorf("plan entry %q does not move anything", move.From)
		}
		tasks = append(tasks, layoutApplyTask{
			move:   move,
			from:   filepath.Join(root, filepath.FromSlash(from)),
			to:     filepath.Join(root, filepath.FromSlash(to)),
			dbFrom: joinCassetteDBPath(dbPrefix, from),
			dbTo:   joinCassetteDBPath(dbPrefix, to),
		})
	}
	return tasks, remaining, nil
}

// applyOneLayoutMove renames one cassette and repoints its index rows.
func applyOneLayoutMove(ctx context.Context, index cassettePathIndex, opts ApplyOptions, task layoutApplyTask) (ApplyMoveResult, PathRefs, *ApplyFailure) {
	result := ApplyMoveResult{From: task.move.From, To: task.move.To, Model: task.move.Model}
	failure := func(stage, message string) (ApplyMoveResult, PathRefs, *ApplyFailure) {
		result.Status = ApplyFailed
		return result, PathRefs{}, &ApplyFailure{From: task.move.From, To: task.move.To, Stage: stage, Message: message}
	}

	sourceInfo, sourceErr := os.Stat(task.from)
	_, targetErr := os.Stat(task.to)
	sourceExists := sourceErr == nil
	targetExists := targetErr == nil
	if sourceErr != nil && !errors.Is(sourceErr, os.ErrNotExist) {
		return failure("stat", fmt.Sprintf("inspect %s: %v", task.from, sourceErr))
	}
	if targetErr != nil && !errors.Is(targetErr, os.ErrNotExist) {
		return failure("stat", fmt.Sprintf("inspect %s: %v", task.to, targetErr))
	}

	if !sourceExists && !targetExists {
		result.Status = ApplyMissingSource
		return result, PathRefs{}, nil
	}
	if sourceExists && targetExists {
		// Both paths exist: never overwrite, and keep the conflict visible as its
		// own outcome instead of a rolled-back move.
		result.Status = ApplyTargetExists
		return result, PathRefs{}, &ApplyFailure{
			From: task.move.From, To: task.move.To, Stage: "conflict",
			Message: "both the source and the target exist; nothing was moved",
		}
	}

	if opts.VerifyModel && task.move.Model != "" {
		candidate := task.from
		if !sourceExists {
			candidate = task.to
		}
		if err := verifyLayoutModel(candidate, task.move.Model); err != nil {
			return failure("verify", err.Error())
		}
	}

	if !sourceExists && targetExists {
		// The file move already happened; finish the job for an interrupted run.
		if index == nil {
			result.Status = ApplyAlreadyApplied
			return result, PathRefs{}, nil
		}
		targetRefs, err := index.CountRefs(ctx, task.dbTo)
		if err != nil {
			return failure("database", err.Error())
		}
		if targetRefs.Rows() > 0 {
			result.Status = ApplyAlreadyApplied
			result.Refs = targetRefs
			return result, targetRefs, nil
		}
		sourceRefs, err := index.CountRefs(ctx, task.dbFrom)
		if err != nil {
			return failure("database", err.Error())
		}
		if sourceRefs.Rows() == 0 {
			result.Status = ApplyAlreadyApplied
			return result, PathRefs{}, nil
		}
		result.Status = ApplyResumed
		if opts.DryRun {
			result.Refs = sourceRefs
			return result, sourceRefs, nil
		}
		refs, err := index.MovePath(ctx, task.dbFrom, task.dbTo)
		if err != nil {
			return failure("database", fmt.Sprintf("index repair failed: %v", err))
		}
		result.Refs = refs
		return result, refs, nil
	}

	// Normal case: the source exists and the target does not.
	result.Bytes = sourceInfo.Size()
	result.Status = ApplyMoved
	if opts.DryRun {
		if index != nil {
			refs, err := index.CountRefs(ctx, task.dbFrom)
			if err != nil {
				return failure("database", err.Error())
			}
			result.Refs = refs
			return result, refs, nil
		}
		return result, PathRefs{}, nil
	}

	if err := os.MkdirAll(filepath.Dir(task.to), 0o755); err != nil {
		return failure("mkdir", fmt.Sprintf("create %s: %v", filepath.Dir(task.to), err))
	}
	if err := os.Rename(task.from, task.to); err != nil {
		return failure("move", fmt.Sprintf("rename %s -> %s: %v", task.from, task.to, err))
	}
	if index == nil {
		return result, PathRefs{}, nil
	}
	refs, err := index.MovePath(ctx, task.dbFrom, task.dbTo)
	if err != nil {
		result.Bytes = 0
		if revertErr := os.Rename(task.to, task.from); revertErr != nil {
			return failure("revert", fmt.Sprintf("index update failed (%v) and the cassette could not be moved back (%v); it now sits at %s while the index still points at %s", err, revertErr, task.to, task.dbFrom))
		}
		return failure("database", fmt.Sprintf("%v; the file move was rolled back", err))
	}
	result.Refs = refs
	return result, refs, nil
}

// verifyLayoutModel re-reads a prelude and checks the recorded model, so a stale
// or hand-edited plan cannot file a cassette under the wrong model.
func verifyLayoutModel(path, model string) error {
	_, recorded, _, err := inspectLayout(path)
	if err != nil {
		return fmt.Errorf("read prelude: %w", err)
	}
	if normalizeLayoutModel(recorded) != normalizeLayoutModel(model) {
		return fmt.Errorf("recorded model %q does not match the plan (%q)", recorded, model)
	}
	return nil
}

// cleanLayoutRelativePath rejects absolute paths and anything that would escape
// the cassette root.
func cleanLayoutRelativePath(value string) (string, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "", errors.New("empty path")
	}
	if strings.HasPrefix(trimmed, "/") || filepath.IsAbs(trimmed) {
		return "", errors.New("must be relative to the cassette root")
	}
	cleaned := path.Clean(filepath.ToSlash(trimmed))
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", errors.New("escapes the cassette root")
	}
	return cleaned, nil
}

// joinCassetteDBPath renders the path as the database stores it: slash
// separated, with the configured prefix.
func joinCassetteDBPath(prefix, relative string) string {
	if prefix == "" {
		return "/" + strings.TrimLeft(relative, "/")
	}
	return prefix + "/" + strings.TrimLeft(relative, "/")
}

func normalizeLayoutModel(value string) string {
	return strings.Trim(strings.TrimSpace(value), "/")
}
