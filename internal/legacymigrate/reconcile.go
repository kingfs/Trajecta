package legacymigrate

import (
	"context"
	"database/sql"
	"fmt"
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

// ReconcileReport aggregates a reconciliation run.
type ReconcileReport struct {
	DryRun     bool                 `json:"dry_run"`
	LegacyIDs  int64                `json:"legacy_ids"`
	MappedIDs  int64                `json:"mapped_ids"`
	Tables     []DerivedTableResult `json:"tables"`
	Warnings   []string             `json:"warnings,omitempty"`
	DurationMS int64                `json:"duration_ms"`
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
	if len(opts.SQLitePaths) == 0 {
		return nil, fmt.Errorf("at least one legacy sqlite path is required to map the old trace ids")
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

	for _, table := range tables {
		result := reconcileTable(ctx, pg, table, orphans[table], mapping, batchSize, opts.Apply)
		report.Tables = append(report.Tables, result)
	}
	if report.Unresolved() > 0 {
		report.Warnings = append(report.Warnings, fmt.Sprintf(
			"%d orphan rows reference no legacy trace and were left untouched; the rows below the missing ids are still readable through /api", report.Unresolved()))
	}
	return report, nil
}

// derivedTraceTables lists the tables that carry a trace id and are derived from
// the recorded cassettes, excluding the index itself.
func derivedTraceTables(ctx context.Context, pg *sql.DB) ([]string, error) {
	columns, err := targetTableColumns(ctx, pg)
	if err != nil {
		return nil, err
	}
	tables := make([]string, 0, len(columns))
	for table, tableColumns := range columns {
		if table == "logs" {
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
func orphanTraceIDs(ctx context.Context, pg *sql.DB, table string) ([]string, error) {
	rows, err := pg.QueryContext(ctx, fmt.Sprintf(`
		SELECT DISTINCT d.trace_id
		FROM %s d
		WHERE d.trace_id IS NOT NULL AND d.trace_id <> ''
		  AND NOT EXISTS (SELECT 1 FROM logs l WHERE l.trace_id = d.trace_id)
		ORDER BY d.trace_id`, quoteIdent(table)))
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
	if len(ids) == 0 {
		return mapping, nil
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
		for start := 0; start < len(ids); start += lookupBatch {
			end := start + lookupBatch
			if end > len(ids) {
				end = len(ids)
			}
			chunk := ids[start:end]
			placeholders := make([]string, len(chunk))
			args := make([]any, len(chunk))
			for i, id := range chunk {
				placeholders[i] = "?"
				args[i] = id
			}
			statement := fmt.Sprintf(`SELECT trace_id, path FROM logs WHERE trace_id IN (%s)`, strings.Join(placeholders, ","))
			rows, err := source.QueryContext(ctx, statement, args...)
			if err != nil {
				source.Close()
				return nil, fmt.Errorf("look up legacy trace ids in %s (%s): %w", path, mode, err)
			}
			for rows.Next() {
				var id, cassettePath string
				if err := rows.Scan(&id, &cassettePath); err != nil {
					rows.Close()
					source.Close()
					return nil, err
				}
				legacyPath[id] = cassettePath
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				source.Close()
				return nil, err
			}
			rows.Close()
		}
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
