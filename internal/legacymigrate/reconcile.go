package legacymigrate

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The legacy index stored the recorder exchange id in `logs.trace_id`, while the
// current server derives that column from the cassette itself. When a server
// index pass ran before the legacy database was merged, `logs` kept the derived
// value and the merge skipped the legacy row (the inserts rely on
// ON CONFLICT DO NOTHING), so the derived tables still carry ids that no `logs`
// row uses.
//
// ReconcileDerivedTraceIDs repairs that state without ever discarding the last
// copy of a row: for every derived table it maps the orphan ids back through
// the legacy database (`logs.trace_id` -> `logs.path`) and the current index
// (`logs.path` -> `logs.trace_id`), deletes rows whose identity already exists
// under the mapped id, and moves the remaining rows onto that id.

// ReconcileOptions configures ReconcileDerivedTraceIDs.
type ReconcileOptions struct {
	// SQLitePaths lists the legacy databases that still carry the old ids. The
	// archived files (`*.migrated`) are accepted; nothing is written to them.
	SQLitePaths []string
	PostgresDSN string
	// Apply performs the deletions and updates; without it the run only counts.
	Apply bool
	// Tables optionally restricts the run to the named derived tables.
	Tables []string
	// OpenMode chooses how the legacy databases are opened.
	OpenMode string
	// BatchSize is the number of id pairs per statement. Defaults to 200.
	BatchSize int
	// DataRoot is the local root the recorded cassette paths live under. When it
	// is set the run also repairs `logs` rows whose cassette moved away while the
	// same recording stayed indexed at a path that exists.
	DataRoot string
	// RecordedPrefix is the prefix the database recorded. It is replaced by
	// DataRoot, which is what a run outside the deployment container needs;
	// inside the container both are the same and RecordedPrefix defaults to
	// DataRoot.
	RecordedPrefix string
	// PruneSupersededIndexRows deletes a superseded `logs` row once no derived
	// row references its trace id any more. It only has an effect with Apply.
	PruneSupersededIndexRows bool
	// MaxSamples caps how many example paths the superseded-row report lists.
	// Zero or less means defaultMaxSamples, so an operator can raise it to
	// enumerate every path the repair refused to touch.
	MaxSamples int
}

// DerivedTableResult reports the reconciliation of one derived table.
type DerivedTableResult struct {
	Table          string   `json:"table"`
	OrphanIDs      int64    `json:"orphan_ids"`
	MappedIDs      int64    `json:"mapped_ids"`
	UnmappedIDs    int64    `json:"unmapped_ids"`
	DuplicateRows  int64    `json:"duplicate_rows"`
	RemappedRows   int64    `json:"remapped_rows"`
	UnresolvedRows int64    `json:"unresolved_rows,omitempty"`
	IdentityKeys   []string `json:"identity_columns,omitempty"`
	SampleUnmapped []string `json:"sample_unmapped_ids,omitempty"`
	Err            string   `json:"error,omitempty"`
}

// SupersededIndexResult reports the `logs` rows whose cassette moved away while
// the same recording stayed indexed at a path that exists.
type SupersededIndexResult struct {
	Checked           int64    `json:"checked"`
	MissingFiles      int64    `json:"missing_files"`
	Superseded        int64    `json:"superseded"`
	Ambiguous         int64    `json:"ambiguous"`
	OrphanFiles       int64    `json:"orphan_files"`
	DonorIDs          int64    `json:"donor_ids"`
	DerivedDuplicates int64    `json:"derived_duplicate_rows"`
	DerivedRemapped   int64    `json:"derived_remapped_rows"`
	DerivedUnresolved int64    `json:"derived_unresolved_rows"`
	Pruned            int64    `json:"pruned_index_rows"`
	PrunePending      int64    `json:"prune_pending_rows"`
	Err               string   `json:"error,omitempty"`
	SampleOrphanPaths []string `json:"sample_orphan_paths,omitempty"`
	SampleAmbiguous   []string `json:"sample_ambiguous_paths,omitempty"`
}

// ReconcileReport aggregates a reconciliation run.
type ReconcileReport struct {
	DryRun     bool                   `json:"dry_run"`
	LegacyIDs  int64                  `json:"legacy_ids"`
	MappedIDs  int64                  `json:"mapped_ids"`
	Tables     []DerivedTableResult   `json:"tables"`
	Superseded *SupersededIndexResult `json:"superseded_index,omitempty"`
	Warnings   []string               `json:"warnings,omitempty"`
	DurationMS int64                  `json:"duration_ms"`
}

// Deleted returns the number of duplicate rows the run removed (or would).
func (r *ReconcileReport) Deleted() int64 {
	var total int64
	for _, table := range r.Tables {
		total += table.DuplicateRows
	}
	return total
}

// Remapped returns the number of rows the run moved onto the current ids.
func (r *ReconcileReport) Remapped() int64 {
	var total int64
	for _, table := range r.Tables {
		total += table.RemappedRows
	}
	return total
}

// Unresolved returns the number of orphan rows that referenced no reachable
// legacy trace and were therefore left untouched.
func (r *ReconcileReport) Unresolved() int64 {
	var total int64
	for _, table := range r.Tables {
		total += table.UnresolvedRows
	}
	return total
}

// OK reports whether every table was reconciled without an error and without
// leaving rows that reference no legacy trace behind.
func (r *ReconcileReport) OK() bool {
	if r.Unresolved() != 0 {
		return false
	}
	for _, table := range r.Tables {
		if table.Err != "" {
			return false
		}
	}
	return true
}

// ReconcileDerivedTraceIDs repairs the trace ids of the derived tables.
func ReconcileDerivedTraceIDs(ctx context.Context, opts ReconcileOptions) (*ReconcileReport, error) {
	started := time.Now()
	report := &ReconcileReport{DryRun: !opts.Apply}
	defer func() { report.DurationMS = time.Since(started).Milliseconds() }()

	if strings.TrimSpace(opts.PostgresDSN) == "" {
		return nil, fmt.Errorf("postgres dsn must not be empty")
	}
	if len(opts.SQLitePaths) == 0 && strings.TrimSpace(opts.DataRoot) == "" {
		return nil, fmt.Errorf("at least one legacy sqlite path or a data root is required")
	}
	batchSize := opts.BatchSize
	if batchSize <= 0 {
		batchSize = 200
	}
	openMode := opts.OpenMode
	if openMode == "" {
		openMode = OpenModeAuto
	}

	pg, err := sql.Open("postgres", opts.PostgresDSN)
	if err != nil {
		return nil, fmt.Errorf("open postgres: %w", err)
	}
	defer pg.Close()
	if err := pg.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	if err := requireApplicationSchema(ctx, pg); err != nil {
		return nil, err
	}

	tables, err := derivedTraceTables(ctx, pg)
	if err != nil {
		return nil, err
	}
	if len(opts.Tables) > 0 {
		wanted := make(map[string]bool, len(opts.Tables))
		for _, name := range opts.Tables {
			wanted[name] = true
		}
		filtered := tables[:0]
		for _, name := range tables {
			if wanted[name] {
				filtered = append(filtered, name)
			}
		}
		tables = filtered
	}
	if len(tables) == 0 {
		return report, nil
	}

	// Orphan ids are collected per table first: the same id can appear in
	// several tables, and the legacy lookup is the expensive part.
	orphans := map[string][]string{}
	all := map[string]bool{}
	for _, table := range tables {
		ids, listErr := orphanTraceIDs(ctx, pg, table)
		if listErr != nil {
			return nil, listErr
		}
		orphans[table] = ids
		for _, id := range ids {
			all[id] = true
		}
	}
	ids := make([]string, 0, len(all))
	for id := range all {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	report.LegacyIDs = int64(len(ids))

	mapping, err := mapLegacyTraceIDs(ctx, pg, opts.SQLitePaths, openMode, ids)
	if err != nil {
		return nil, err
	}
	report.MappedIDs = int64(len(mapping))

	// A `logs` row whose cassette moved away is a donor: the recording stayed
	// indexed at the path it moved to, so the donor's derived rows belong to the
	// surviving trace id and the donor row itself becomes garbage.
	if strings.TrimSpace(opts.DataRoot) != "" {
		superseded, stats, discoverErr := discoverSupersededIndexRows(ctx, pg, opts)
		if discoverErr != nil {
			return nil, discoverErr
		}
		report.Superseded = stats
		if len(superseded) > 0 {
			donors := make([]string, 0, len(superseded))
			for _, row := range superseded {
				mapping[row.TraceID] = row.SurvivorID
				donors = append(donors, row.TraceID)
			}
			sort.Strings(donors)
			stats.DonorIDs = int64(len(donors))
			for _, table := range tables {
				result := reconcileTable(ctx, pg, table, donors, mapping, batchSize, opts.Apply)
				if result.Err != "" && stats.Err == "" {
					stats.Err = result.Err
				}
				stats.DerivedDuplicates += result.DuplicateRows
				stats.DerivedRemapped += result.RemappedRows
				stats.DerivedUnresolved += result.UnresolvedRows
			}
			if opts.Apply && opts.PruneSupersededIndexRows {
				pruned, pruneErr := pruneSupersededIndexRows(ctx, pg, tables, donors, batchSize)
				if pruneErr != nil {
					return nil, pruneErr
				}
				stats.Pruned = pruned
			} else {
				// Every donor loses its derived rows (they are duplicates of the
				// surviving id or move onto it), so the whole donor set is what a
				// prune targets. The apply run reports what it really deleted.
				stats.PrunePending = int64(len(donors))
			}
		}
	}

	for _, table := range tables {
		result := reconcileTable(ctx, pg, table, orphans[table], mapping, batchSize, opts.Apply)
		report.Tables = append(report.Tables, result)
	}
	if report.Unresolved() > 0 {
		report.Warnings = append(report.Warnings, fmt.Sprintf(
			"%d orphan rows reference no legacy trace and were left untouched; the rows below the missing ids are still readable through /api", report.Unresolved()))
	}
	if stats := report.Superseded; stats != nil && stats.OrphanFiles > 0 {
		report.Warnings = append(report.Warnings, fmt.Sprintf(
			"%d indexed cassettes are missing at the recorded path and exist nowhere under the data root; those metadata rows are kept as they are", stats.OrphanFiles))
	}
	return report, nil
}

// sharedTraceIDTables names the tables whose `trace_id` column does not hold a
// `logs.trace_id`, so remapping it through the legacy trace index would corrupt
// it:
//
//   - `logs` owns the id;
//   - `upstream_exchanges.trace_id` holds the recorder prelude `meta.request_id`
//     (docs/IMPLEMENTATION_STATUS.md:150), which is a different id space.
var sharedTraceIDTables = map[string]bool{
	"logs":               true,
	"upstream_exchanges": true,
}

// derivedTraceTables lists the tables that carry a `logs.trace_id` and are
// derived from the recorded cassettes.
func derivedTraceTables(ctx context.Context, pg *sql.DB) ([]string, error) {
	columns, err := targetTableColumns(ctx, pg)
	if err != nil {
		return nil, err
	}
	tables := make([]string, 0, len(columns))
	for table, tableColumns := range columns {
		if sharedTraceIDTables[table] {
			continue
		}
		for _, column := range tableColumns {
			if column.Name == "trace_id" {
				tables = append(tables, table)
				break
			}
		}
	}
	sort.Strings(tables)
	return tables, nil
}

// orphanTraceIDs returns the distinct trace ids of a table that no logs row uses.
//
// The distinct set is folded first and only then anti-joined against `logs`.
// Written the other way round the planner probes `logs` once per table row,
// which on the hundred-million-row `semantic_nodes` table means tens of
// millions of index probes instead of a few hundred thousand, and the join
// form lets Postgres pick a hash anti-join over the small `logs` side.
func orphanTraceIDs(ctx context.Context, pg *sql.DB, table string) ([]string, error) {
	rows, err := pg.QueryContext(ctx, fmt.Sprintf(`
		SELECT t.trace_id
		FROM (
			SELECT DISTINCT d.trace_id
			FROM %s d
			WHERE d.trace_id IS NOT NULL AND d.trace_id <> ''
		) t
		LEFT JOIN logs l ON l.trace_id = t.trace_id
		WHERE l.trace_id IS NULL
		ORDER BY t.trace_id`, quoteIdent(table)))
	if err != nil {
		return nil, fmt.Errorf("list orphan trace ids of %s: %w", table, err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// mapLegacyTraceIDs resolves old ids to the ids the current index uses, through
// the cassette path both indexes agree on.
func mapLegacyTraceIDs(ctx context.Context, pg *sql.DB, paths []string, openMode string, ids []string) (map[string]string, error) {
	mapping := map[string]string{}
	if len(ids) == 0 || len(paths) == 0 {
		return mapping, nil
	}
	// One sequential pass over the legacy index beats a random indexed probe per
	// orphan id: the real vault has ~2·10^5 orphan ids and the legacy database is
	// tens of gigabytes, so probing them individually costs minutes of seeks.
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	legacyPath := map[string]string{} // old id -> cassette path
	const lookupBatch = 400
	for _, path := range paths {
		source, mode, err := openLegacySQLite(ctx, path, openMode)
		if err != nil {
			return nil, err
		}
		tables, err := listSourceTables(ctx, source)
		if err != nil {
			source.Close()
			return nil, err
		}
		if !containsFold(tables, "logs") {
			source.Close()
			continue
		}
		columns, err := sourceTableColumns(ctx, source, "logs")
		if err != nil {
			source.Close()
			return nil, err
		}
		names := make([]string, 0, len(columns))
		for _, column := range columns {
			names = append(names, column.Name)
		}
		if !containsFold(names, "trace_id") || !containsFold(names, "path") {
			source.Close()
			continue
		}
		rows, err := source.QueryContext(ctx, `SELECT trace_id, path FROM logs`)
		if err != nil {
			source.Close()
			return nil, fmt.Errorf("read the legacy trace index of %s (%s): %w", path, mode, err)
		}
		for rows.Next() {
			var id, cassettePath string
			if err := rows.Scan(&id, &cassettePath); err != nil {
				rows.Close()
				source.Close()
				return nil, err
			}
			if wanted[id] {
				legacyPath[id] = cassettePath
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			source.Close()
			return nil, err
		}
		rows.Close()
		source.Close()
	}
	if len(legacyPath) == 0 {
		return mapping, nil
	}

	pathsToResolve := make([]string, 0, len(legacyPath))
	for _, cassettePath := range legacyPath {
		pathsToResolve = append(pathsToResolve, cassettePath)
	}
	sort.Strings(pathsToResolve)
	currentID := map[string]string{}
	for start := 0; start < len(pathsToResolve); start += lookupBatch {
		end := start + lookupBatch
		if end > len(pathsToResolve) {
			end = len(pathsToResolve)
		}
		chunk := pathsToResolve[start:end]
		found, err := lookupValues(ctx, pg, "logs", "path", []string{"trace_id"}, chunk)
		if err != nil {
			return nil, err
		}
		for path, values := range found {
			if len(values) > 0 {
				if id, ok := values[0].(string); ok && id != "" {
					currentID[path] = id
				}
			}
		}
	}
	for id, cassettePath := range legacyPath {
		if current, ok := currentID[cassettePath]; ok && current != id {
			mapping[id] = current
		}
	}
	return mapping, nil
}

// lookupValues resolves the wanted columns of rows identified by one key
// column. The result is keyed by that column's value.
func lookupValues(ctx context.Context, pg *sql.DB, table, keyName string, wanted []string, keys []string) (map[string][]any, error) {
	out := map[string][]any{}
	if len(keys) == 0 || keyName == "" {
		return out, nil
	}
	selected := make([]string, 0, len(wanted)+1)
	selected = append(selected, keyName)
	for _, name := range wanted {
		if name != keyName {
			selected = append(selected, name)
		}
	}
	quoted := make([]string, len(selected))
	for i, name := range selected {
		quoted[i] = quoteIdent(name)
	}
	statement := fmt.Sprintf("SELECT %s FROM %s WHERE %s = ANY($1)", strings.Join(quoted, ", "), quoteIdent(table), quoteIdent(keyName))
	rows, err := pg.QueryContext(ctx, statement, keys)
	if err != nil {
		return nil, fmt.Errorf("look up %s by %s: %w", table, keyName, err)
	}
	defer rows.Close()
	for rows.Next() {
		scanned := make([]any, len(selected))
		pointers := make([]any, len(selected))
		for i := range scanned {
			pointers[i] = &scanned[i]
		}
		if err := rows.Scan(pointers...); err != nil {
			return nil, err
		}
		byName := make(map[string]any, len(selected))
		for i, name := range selected {
			byName[name] = scanned[i]
		}
		key, ok := byName[keyName].(string)
		if !ok || key == "" {
			continue
		}
		values := make([]any, len(wanted))
		for i, name := range wanted {
			values[i] = byName[name]
		}
		out[key] = values
	}
	return out, rows.Err()
}

// reconcileTable deletes the superseded duplicates of one table and moves the
// remaining orphan rows onto the current ids.
func reconcileTable(ctx context.Context, pg *sql.DB, table string, orphans []string, mapping map[string]string, batchSize int, apply bool) DerivedTableResult {
	result := DerivedTableResult{Table: table, OrphanIDs: int64(len(orphans))}
	if len(orphans) == 0 {
		return result
	}
	identity, err := traceIdentityColumns(ctx, pg, table)
	if err != nil {
		result.Err = err.Error()
		return result
	}
	result.IdentityKeys = identity

	type pair struct{ legacy, current string }
	pairs := make([]pair, 0, len(orphans))
	for _, id := range orphans {
		current, ok := mapping[id]
		if !ok {
			result.UnmappedIDs++
			if len(result.SampleUnmapped) < 10 {
				result.SampleUnmapped = append(result.SampleUnmapped, id)
			}
			continue
		}
		pairs = append(pairs, pair{legacy: id, current: current})
	}
	result.MappedIDs = int64(len(pairs))
	if len(pairs) == 0 {
		result.UnresolvedRows = countRowsByTraceIDs(ctx, pg, table, orphans)
		return result
	}

	for start := 0; start < len(pairs); start += batchSize {
		end := start + batchSize
		if end > len(pairs) {
			end = len(pairs)
		}
		batch := pairs[start:end]
		legacy := make([]string, len(batch))
		current := make([]string, len(batch))
		for i, item := range batch {
			legacy[i] = item.legacy
			current[i] = item.current
		}
		duplicates, err := countDuplicateRows(ctx, pg, table, identity, legacy, current)
		if err != nil {
			result.Err = err.Error()
			return result
		}
		result.DuplicateRows += duplicates
		// Every orphan row of the batch is either a superseded duplicate or a
		// row that moves onto the current id.
		before := countRowsByTraceIDs(ctx, pg, table, legacy)
		if before > duplicates {
			result.RemappedRows += before - duplicates
		}
		if apply {
			if err := applyBatch(ctx, pg, table, identity, legacy, current); err != nil {
				result.Err = err.Error()
				return result
			}
		}
	}
	if result.UnmappedIDs > 0 {
		unmapped := make([]string, 0, len(result.SampleUnmapped))
		for _, id := range orphans {
			if _, ok := mapping[id]; !ok {
				unmapped = append(unmapped, id)
			}
		}
		result.UnresolvedRows = countRowsByTraceIDs(ctx, pg, table, unmapped)
	}
	return result
}

// traceIdentityColumns returns the columns that identify a row of a derived
// table independently of its trace id.
func traceIdentityColumns(ctx context.Context, pg *sql.DB, table string) ([]string, error) {
	sets, err := uniqueKeySets(ctx, pg, table)
	if err != nil {
		return nil, err
	}
	for _, set := range sets {
		hasTrace := false
		identity := make([]string, 0, len(set.Names))
		for _, name := range set.Names {
			if name == "trace_id" {
				hasTrace = true
				continue
			}
			identity = append(identity, name)
		}
		if hasTrace {
			return identity, nil
		}
	}
	return nil, nil
}

func countDuplicateRows(ctx context.Context, pg *sql.DB, table string, identity, legacy, current []string) (int64, error) {
	var count int64
	err := pg.QueryRowContext(ctx, duplicateDeleteStatement(table, identity, true),
		legacy, current).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count duplicate rows of %s: %w", table, err)
	}
	return count, nil
}

// duplicateDeleteStatement builds the statement that removes the orphan rows
// whose identity already exists under the mapped id. With countOnly it counts
// them instead, so a dry run reports exactly what an apply would delete.
func duplicateDeleteStatement(table string, identity []string, countOnly bool) string {
	match := "TRUE"
	if len(identity) > 0 {
		conditions := make([]string, 0, len(identity))
		for _, name := range identity {
			conditions = append(conditions, fmt.Sprintf("c.%s IS NOT DISTINCT FROM d.%s", quoteIdent(name), quoteIdent(name)))
		}
		match = strings.Join(conditions, " AND ")
	}
	if countOnly {
		return fmt.Sprintf(`SELECT count(*) FROM %s d JOIN unnest($1::text[], $2::text[]) AS m(legacy, current) ON d.trace_id = m.legacy WHERE EXISTS (SELECT 1 FROM %s c WHERE c.trace_id = m.current AND %s)`,
			quoteIdent(table), quoteIdent(table), match)
	}
	return fmt.Sprintf(`DELETE FROM %s d USING unnest($1::text[], $2::text[]) AS m(legacy, current) WHERE d.trace_id = m.legacy AND EXISTS (SELECT 1 FROM %s c WHERE c.trace_id = m.current AND %s)`,
		quoteIdent(table), quoteIdent(table), match)
}

// remapStatement moves the remaining orphan rows onto the mapped ids.
func remapStatement(table string) string {
	return fmt.Sprintf(`UPDATE %s d SET trace_id = m.current FROM unnest($1::text[], $2::text[]) AS m(legacy, current) WHERE d.trace_id = m.legacy`,
		quoteIdent(table))
}

// applyBatch deletes one batch of duplicates and moves the remaining rows. The
// two statements share a transaction, and a conflict during the update falls
// back to per-pair handling so a single stubborn row cannot stop the run.
func applyBatch(ctx context.Context, pg *sql.DB, table string, identity, legacy, current []string) error {
	tx, err := pg.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, duplicateDeleteStatement(table, identity, false), legacy, current); err != nil {
		tx.Rollback()
		return fmt.Errorf("delete duplicates of %s: %w", table, err)
	}
	if _, err := tx.ExecContext(ctx, remapStatement(table), legacy, current); err != nil {
		tx.Rollback()
		return remapOneByOne(ctx, pg, table, identity, legacy, current)
	}
	return tx.Commit()
}

// remapOneByOne retries a failing batch pair by pair, keeping the pairs that
// still conflict exactly as they are.
func remapOneByOne(ctx context.Context, pg *sql.DB, table string, identity, legacy, current []string) error {
	for i := range legacy {
		l := []string{legacy[i]}
		c := []string{current[i]}
		tx, err := pg.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, duplicateDeleteStatement(table, identity, false), l, c); err != nil {
			tx.Rollback()
			return fmt.Errorf("delete duplicates of %s for %s: %w", table, legacy[i], err)
		}
		if _, err := tx.ExecContext(ctx, remapStatement(table), l, c); err != nil {
			tx.Rollback()
			continue // the row keeps its old id and stays reported as unresolved
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func countRowsByTraceIDs(ctx context.Context, pg *sql.DB, table string, ids []string) int64 {
	if len(ids) == 0 {
		return 0
	}
	var total int64
	const batch = 500
	for start := 0; start < len(ids); start += batch {
		end := start + batch
		if end > len(ids) {
			end = len(ids)
		}
		var count int64
		statement := fmt.Sprintf("SELECT count(*) FROM %s WHERE trace_id = ANY($1)", quoteIdent(table))
		if err := pg.QueryRowContext(ctx, statement, ids[start:end]).Scan(&count); err != nil {
			return total
		}
		total += count
	}
	return total
}

// supersededIndexRow is a `logs` row whose cassette no longer exists at the
// recorded path while the same recording is indexed at a path that does.
type supersededIndexRow struct {
	Path         string
	TraceID      string
	SurvivorPath string
	SurvivorID   string
}

// discoverSupersededIndexRows classifies the `logs` rows whose cassette is
// missing. A row whose recording is indexed exactly once at a path that exists
// is superseded; a row with no candidate at all is counted and kept, because it
// may be the only trace of that request.
func discoverSupersededIndexRows(ctx context.Context, pg *sql.DB, opts ReconcileOptions) ([]supersededIndexRow, *SupersededIndexResult, error) {
	stats := &SupersededIndexResult{}
	rows, err := pg.QueryContext(ctx, `SELECT path, trace_id FROM logs ORDER BY path`)
	if err != nil {
		return nil, stats, fmt.Errorf("list the trace index: %w", err)
	}
	defer rows.Close()

	recordedPrefix := opts.RecordedPrefix
	if recordedPrefix == "" {
		recordedPrefix = opts.DataRoot
	}
	survivors := map[string][]supersededIndexRow{}
	type missingRow struct{ path, traceID string }
	var missing []missingRow
	for rows.Next() {
		var path, traceID string
		if err := rows.Scan(&path, &traceID); err != nil {
			return nil, stats, err
		}
		stats.Checked++
		if _, statErr := os.Stat(recordedToLocal(path, recordedPrefix, opts.DataRoot)); statErr == nil {
			base := filepath.Base(path)
			survivors[base] = append(survivors[base], supersededIndexRow{Path: path, TraceID: traceID})
			continue
		}
		missing = append(missing, missingRow{path: path, traceID: traceID})
	}
	if err := rows.Err(); err != nil {
		return nil, stats, err
	}
	stats.MissingFiles = int64(len(missing))

	var superseded []supersededIndexRow
	for _, row := range missing {
		hits := survivors[filepath.Base(row.path)]
		switch len(hits) {
		case 0:
			stats.OrphanFiles++
			if len(stats.SampleOrphanPaths) < sampleLimit(opts.MaxSamples) {
				stats.SampleOrphanPaths = append(stats.SampleOrphanPaths, row.path)
			}
		case 1:
			superseded = append(superseded, supersededIndexRow{
				Path:         row.path,
				TraceID:      row.traceID,
				SurvivorPath: hits[0].Path,
				SurvivorID:   hits[0].TraceID,
			})
		default:
			stats.Ambiguous++
			if len(stats.SampleAmbiguous) < sampleLimit(opts.MaxSamples) {
				stats.SampleAmbiguous = append(stats.SampleAmbiguous, row.path)
			}
		}
	}
	stats.Superseded = int64(len(superseded))
	return superseded, stats, nil
}

// defaultMaxSamples is how many example paths a report lists unless the caller
// asks for a different cap.
const defaultMaxSamples = 10

// sampleLimit resolves the per-list cap; a non-positive request means the default.
func sampleLimit(maxSamples int) int {
	if maxSamples <= 0 {
		return defaultMaxSamples
	}
	return maxSamples
}

// recordedToLocal maps a path recorded in the application database onto the local
// file system. Inside the deployment container the recorded prefix is the local
// one, so both arguments are equal and the path is returned unchanged.
func recordedToLocal(recorded, recordedPrefix, dataRoot string) string {
	if recordedPrefix == "" || dataRoot == "" {
		return recorded
	}
	tail, ok := strings.CutPrefix(recorded, recordedPrefix)
	if !ok {
		return recorded
	}
	return filepath.Join(dataRoot, tail)
}

// pruneSupersededIndexRows deletes the donor `logs` rows that no table derives
// from any more and reports how many it removed.
func pruneSupersededIndexRows(ctx context.Context, pg *sql.DB, tables, donors []string, batchSize int) (int64, error) {
	if len(donors) == 0 {
		return 0, nil
	}
	statement := supersededPruneStatement(tables)
	var total int64
	for start := 0; start < len(donors); start += batchSize {
		end := start + batchSize
		if end > len(donors) {
			end = len(donors)
		}
		result, err := pg.ExecContext(ctx, statement, donors[start:end])
		if err != nil {
			return total, fmt.Errorf("prune superseded index rows: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return total, err
		}
		total += affected
	}
	return total, nil
}

// supersededPruneStatement removes a `logs` row only when every table that stores
// a `logs.trace_id` stopped referencing it, so pruning a donor can never take
// derived rows with it. An empty table list means the caller has no evidence at
// all, so the statement is made a no-op instead of an unguarded delete.
func supersededPruneStatement(tables []string) string {
	conditions := make([]string, 0, len(tables)+1)
	for _, table := range tables {
		conditions = append(conditions, fmt.Sprintf(
			"NOT EXISTS (SELECT 1 FROM %s d WHERE d.trace_id = l.trace_id)", quoteIdent(table)))
	}
	if len(conditions) == 0 {
		conditions = append(conditions, "FALSE")
	}
	return "DELETE FROM logs l WHERE l.trace_id = ANY($1) AND " + strings.Join(conditions, " AND ")
}
