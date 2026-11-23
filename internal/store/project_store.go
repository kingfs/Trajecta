// Project attribution: the project directory a client harness declared about
// itself, indexed per trace and rolled up per session.
//
// This is a derived index over the cassettes, exactly like trace_resources: the
// raw .http file stays the source of truth for what the client said, and the
// rows here only answer "which project was this request/session working in".
// Nothing is inferred from paths a model touched - a project is a root the
// harness named, not a file the model read - so the values are one labelled
// declaration each (see internal/projectid).
//
// Two tables, for the same reason resources has two: trace_projects is the
// per-request fact, and session_projects is the session-level rollup the pages
// aggregate. The rollup is stored rather than computed per page because the
// session list is read on every page load and a per-session "first declaration"
// query is the expensive half of it; it is recomputed from trace_projects
// whenever a trace is (re)parsed, so it cannot drift from the facts it derives.

package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ProjectDeclaration is one trace's declared project, as the extractor found it.
type ProjectDeclaration struct {
	Root string
	// RootKey is the comparison form of Root (case-folded Windows paths, POSIX
	// kept verbatim). Two declarations with the same key are one project.
	RootKey string
	Source  string
	// Conflicting reports that the request declared more than one distinct
	// root; Root holds the effective (last) one.
	Conflicting bool
}

// TraceProject is the project index row of one trace.
type TraceProject struct {
	TraceID     string
	SessionID   string
	Root        string
	RootKey     string
	Source      string
	Conflicting bool
}

// SessionProject is the project rollup of one session.
type SessionProject struct {
	SessionID string
	// Root/RootKey/Source are the session's first declaration: the project the
	// session started in, which is what "this session belongs to project X"
	// means even if the agent later changed directory.
	Root    string
	RootKey string
	Source  string
	// LatestRoot is the last declaration seen, when it differs from Root.
	LatestRoot    string
	LatestRootKey string
	// SwitchCount is how many times the declaration changed across the
	// session's traces in time order. It is a property of the session, not an
	// error: an agent that cd's into a worktree is a real event.
	SwitchCount    int
	FirstTraceID   string
	FirstSeen      time.Time
	LastDeclaredAt time.Time
}

// ProjectFilter selects the project list page.
type ProjectFilter struct {
	Query    string
	Since    time.Time
	Page     int
	PageSize int
}

// ProjectSummary is one project row: a root plus the session traffic attributed
// to it.
type ProjectSummary struct {
	Root         string
	RootKey      string
	SessionCount int
	RequestCount int
	TotalTokens  int
	FirstSeen    time.Time
	LastSeen     time.Time
	Sources      []string
}

// ProjectPage is one page of the project list, plus the coverage numbers the
// page reports instead of hiding: a session with no declaration belongs to no
// project, and "0" must not be mistaken for "no traffic".
type ProjectPage struct {
	Items              []ProjectSummary
	Total              int
	Page               int
	PageSize           int
	TotalPages         int
	IdentifiedSessions int
	TotalSessions      int
}

// SaveTraceProject replaces the project declaration of one trace. decl == nil
// removes any previous row, which is what a reparse that no longer finds a
// declaration must do.
func (s *Store) SaveTraceProject(traceID string, sessionID string, decl *ProjectDeclaration) error {
	traceID = strings.TrimSpace(traceID)
	if traceID == "" {
		return errors.New("save trace project: trace id is required")
	}
	if s == nil || s.db == nil {
		return errors.New("save trace project: database is not configured")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := s.execTx(tx, `DELETE FROM trace_projects WHERE trace_id = ?`, traceID); err != nil {
		return err
	}
	if decl != nil {
		root := strings.TrimSpace(decl.Root)
		rootKey := strings.TrimSpace(decl.RootKey)
		if root == "" || rootKey == "" {
			return errors.New("save trace project: root and root key are required")
		}
		if _, err := s.execTx(tx, `
			INSERT INTO trace_projects (trace_id, session_id, root, root_key, source, conflicting, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, traceID, strings.TrimSpace(sessionID), root, rootKey, strings.TrimSpace(decl.Source), decl.Conflicting, time.Now().UTC()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetTraceProject returns the project declaration of one trace, if any.
func (s *Store) GetTraceProject(traceID string) (TraceProject, bool, error) {
	var item TraceProject
	err := s.db.QueryRow(`
		SELECT trace_id, session_id, root, root_key, source, conflicting
		FROM trace_projects
		WHERE trace_id = ?
	`, strings.TrimSpace(traceID)).Scan(
		&item.TraceID, &item.SessionID, &item.Root, &item.RootKey, &item.Source, &item.Conflicting,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return TraceProject{}, false, nil
	}
	if err != nil {
		return TraceProject{}, false, err
	}
	return item, true, nil
}

// RebuildSessionProject recomputes one session's rollup from trace_projects in
// trace time order. It is idempotent, and it is what keeps the rollup correct
// when parse jobs finish out of order: the ordering comes from logs.recorded_at,
// never from the order the rows were written.
func (s *Store) RebuildSessionProject(sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := s.rebuildSessionProjectTx(tx, sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

// rebuildSessionProjectTx is RebuildSessionProject inside a caller's
// transaction, so a trace cleanup can drop the rows and repair the rollups
// atomically.
func (s *Store) rebuildSessionProjectTx(tx *sql.Tx, sessionID string) error {
	// Claim this session's rollup row before reading the facts.
	//
	// Two rebuilders of the same session can run at once - the parse worker and
	// the reanalysis service are separate workers, and a backfill adds more
	// parses of the same session. Without a claim both read the same facts and
	// write their results in whatever order the scheduler picks, so the write
	// holding the *older* snapshot can land last and put a stale attribution
	// back. The insert creates the row when it is missing; the no-op update
	// takes its write lock in both drivers, so the second rebuilder blocks here
	// until the first commits and then reads the facts it committed.
	now := time.Now().UTC()
	if _, err := s.execTx(tx, `
		INSERT INTO session_projects (
			session_id, root, root_key, source, latest_root, latest_root_key,
			switch_count, first_trace_id, first_seen, last_declared_at, updated_at
		)
		VALUES (?, '', '', '', '', '', 0, '', ?, ?, ?)
		ON CONFLICT (session_id) DO NOTHING
	`, sessionID, now, now, now); err != nil {
		return err
	}
	if _, err := s.execTx(tx, `UPDATE session_projects SET updated_at = updated_at WHERE session_id = ?`, sessionID); err != nil {
		return err
	}

	facts, err := s.sessionProjectFactsTx(tx, sessionID)
	if err != nil {
		return err
	}
	if len(facts) == 0 {
		_, err := s.execTx(tx, `DELETE FROM session_projects WHERE session_id = ?`, sessionID)
		return err
	}
	return s.upsertSessionProjectTx(tx, sessionID, facts)
}

// projectFact is one declaration in session-time order.
type projectFact struct {
	traceID  string
	root     string
	rootKey  string
	source   string
	recorded time.Time
}

func (s *Store) sessionProjectFactsTx(tx *sql.Tx, sessionID string) ([]projectFact, error) {
	rows, err := s.queryTx(tx, `
		SELECT tp.trace_id, tp.root, tp.root_key, tp.source, l.recorded_at
		FROM trace_projects tp
		JOIN logs l ON l.trace_id = tp.trace_id
		WHERE l.session_id = ? AND `+clientVisibleLogClause("l")+`
		ORDER BY l.recorded_at ASC, tp.trace_id ASC
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var facts []projectFact
	for rows.Next() {
		var (
			fact     projectFact
			recorded any
		)
		if err := rows.Scan(&fact.traceID, &fact.root, &fact.rootKey, &fact.source, &recorded); err != nil {
			return nil, err
		}
		if fact.recorded, err = timeParseValue(recorded); err != nil {
			return nil, err
		}
		facts = append(facts, fact)
	}
	return facts, rows.Err()
}

// upsertSessionProjectTx writes one session rollup from facts in time order.
func (s *Store) upsertSessionProjectTx(tx *sql.Tx, sessionID string, facts []projectFact) error {
	first := facts[0]
	latest := facts[len(facts)-1]
	switches := 0
	for index := 1; index < len(facts); index++ {
		if facts[index].rootKey != facts[index-1].rootKey {
			switches++
		}
	}
	_, err := s.execTx(tx, `
		INSERT INTO session_projects (
			session_id, root, root_key, source, latest_root, latest_root_key,
			switch_count, first_trace_id, first_seen, last_declared_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (session_id) DO UPDATE SET
			root = excluded.root,
			root_key = excluded.root_key,
			source = excluded.source,
			latest_root = excluded.latest_root,
			latest_root_key = excluded.latest_root_key,
			switch_count = excluded.switch_count,
			first_trace_id = excluded.first_trace_id,
			first_seen = excluded.first_seen,
			last_declared_at = excluded.last_declared_at,
			updated_at = excluded.updated_at
	`, sessionID, first.root, first.rootKey, first.source, latest.root, latest.rootKey,
		switches, first.traceID, first.recorded, latest.recorded, time.Now().UTC())
	return err
}

// GetSessionProject returns the project rollup of one session, if any.
func (s *Store) GetSessionProject(sessionID string) (SessionProject, bool, error) {
	var (
		item           SessionProject
		firstSeen      any
		lastDeclaredAt any
	)
	err := s.db.QueryRow(`
		SELECT session_id, root, root_key, source, latest_root, latest_root_key,
			switch_count, first_trace_id, first_seen, last_declared_at
		FROM session_projects
		WHERE session_id = ?
	`, strings.TrimSpace(sessionID)).Scan(
		&item.SessionID, &item.Root, &item.RootKey, &item.Source, &item.LatestRoot, &item.LatestRootKey,
		&item.SwitchCount, &item.FirstTraceID, &firstSeen, &lastDeclaredAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionProject{}, false, nil
	}
	if err != nil {
		return SessionProject{}, false, err
	}
	if item.FirstSeen, err = timeParseValue(firstSeen); err != nil {
		return SessionProject{}, false, err
	}
	if item.LastDeclaredAt, err = timeParseValue(lastDeclaredAt); err != nil {
		return SessionProject{}, false, err
	}
	return item, true, nil
}

// ListSessionProjects returns the rollups of the given sessions, keyed by
// session id. Sessions without a declaration are absent from the map.
func (s *Store) ListSessionProjects(sessionIDs []string) (map[string]SessionProject, error) {
	out := make(map[string]SessionProject, len(sessionIDs))
	cleaned := make([]string, 0, len(sessionIDs))
	seen := make(map[string]struct{}, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			continue
		}
		if _, duplicate := seen[sessionID]; duplicate {
			continue
		}
		seen[sessionID] = struct{}{}
		cleaned = append(cleaned, sessionID)
	}
	// Chunked so a large page cannot exceed the driver's placeholder limit.
	const chunk = 400
	for start := 0; start < len(cleaned); start += chunk {
		end := start + chunk
		if end > len(cleaned) {
			end = len(cleaned)
		}
		args := make([]any, 0, end-start)
		for _, sessionID := range cleaned[start:end] {
			args = append(args, sessionID)
		}
		rows, err := s.db.Query(`
			SELECT session_id, root, root_key, source, latest_root, latest_root_key,
				switch_count, first_trace_id, first_seen, last_declared_at
			FROM session_projects
			WHERE session_id IN (`+placeholders(len(args))+`)
		`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var (
				item           SessionProject
				firstSeen      any
				lastDeclaredAt any
			)
			if err := rows.Scan(
				&item.SessionID, &item.Root, &item.RootKey, &item.Source, &item.LatestRoot, &item.LatestRootKey,
				&item.SwitchCount, &item.FirstTraceID, &firstSeen, &lastDeclaredAt,
			); err != nil {
				rows.Close()
				return nil, err
			}
			if item.FirstSeen, err = timeParseValue(firstSeen); err != nil {
				rows.Close()
				return nil, err
			}
			if item.LastDeclaredAt, err = timeParseValue(lastDeclaredAt); err != nil {
				rows.Close()
				return nil, err
			}
			out[item.SessionID] = item
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// ListProjects returns one page of the project list.
//
// The aggregate reads logs instead of session_summaries on purpose. The summary
// table is a derived read model, and a deployment that never backfilled it
// would see an empty project page - which reads as "no traffic", the one
// conclusion the page must not draw from missing data. The window test is also
// the sessions tab's: a session is in the window when it has a client-visible
// request at or after `since`, and a session's counts cover its whole history,
// so a project row and the session row it opens agree.
//
// The number of distinct project roots is bounded by what operators actually
// run, so the grouped rows are read in one query and the page is sliced here
// rather than re-evaluating the session set for a separate count.
func (s *Store) ListProjects(filter ProjectFilter) (ProjectPage, error) {
	page, pageSize := normalizePage(filter.Page, filter.PageSize)
	activeSQL, activeArgs := s.projectActiveSessionsSQL(filter.Since)

	pageResult := ProjectPage{Page: page, PageSize: pageSize}
	// Coverage over the same window: how many sessions are active, and how many
	// of those declared a project.
	if err := s.db.QueryRow(`
		SELECT COUNT(*), COUNT(sp.session_id)
		FROM (`+activeSQL+`) active
		LEFT JOIN session_projects sp ON sp.session_id = active.session_id
	`, activeArgs...).Scan(&pageResult.TotalSessions, &pageResult.IdentifiedSessions); err != nil {
		return ProjectPage{}, err
	}

	where := []string{clientVisibleLogClause("l")}
	args := append([]any{}, activeArgs...)
	if query := strings.TrimSpace(filter.Query); query != "" {
		where = append(where, `LOWER(sp.root) LIKE ?`)
		args = append(args, "%"+escapeLike(strings.ToLower(query))+"%")
	}
	rows, err := s.db.Query(`
		SELECT
			sp.root_key,
			MIN(sp.root) AS root,
			COUNT(DISTINCT active.session_id) AS session_count,
			COUNT(l.trace_id) AS request_count,
			COALESCE(SUM(CASE WHEN l.status_code BETWEEN 200 AND 299 THEN l.total_tokens ELSE 0 END), 0) AS total_tokens,
			MIN(l.recorded_at) AS first_seen,
			MAX(l.recorded_at) AS last_seen,
			`+s.distinctAggregateSQL("sp.source")+` AS sources
		FROM (`+activeSQL+`) active
		JOIN session_projects sp ON sp.session_id = active.session_id
		JOIN logs l ON l.session_id = active.session_id
		WHERE `+strings.Join(where, " AND ")+`
		GROUP BY sp.root_key
		ORDER BY COUNT(DISTINCT active.session_id) DESC, MAX(l.recorded_at) DESC, sp.root_key ASC
	`, args...)
	if err != nil {
		return ProjectPage{}, err
	}
	defer rows.Close()

	all := make([]ProjectSummary, 0, 32)
	for rows.Next() {
		var (
			item      ProjectSummary
			firstSeen any
			lastSeen  any
			sources   string
		)
		if err := rows.Scan(&item.RootKey, &item.Root, &item.SessionCount, &item.RequestCount,
			&item.TotalTokens, &firstSeen, &lastSeen, &sources); err != nil {
			return ProjectPage{}, err
		}
		if item.FirstSeen, err = timeParseValue(firstSeen); err != nil {
			return ProjectPage{}, err
		}
		if item.LastSeen, err = timeParseValue(lastSeen); err != nil {
			return ProjectPage{}, err
		}
		item.Sources = splitCommaList(sources)
		all = append(all, item)
	}
	if err := rows.Err(); err != nil {
		return ProjectPage{}, err
	}

	pageResult.Total = len(all)
	if pageResult.Total == 0 {
		return pageResult, nil
	}
	pageResult.TotalPages = int((pageResult.Total + pageSize - 1) / pageSize)
	offset := (page - 1) * pageSize
	if offset >= len(all) {
		return pageResult, nil
	}
	end := offset + pageSize
	if end > len(all) {
		end = len(all)
	}
	pageResult.Items = all[offset:end]
	return pageResult, nil
}

// projectActiveSessionsSQL returns the subquery naming every session active in
// the window: those with at least one client-visible request at or after
// `since`. A zero `since` means every session with a client-visible request.
func (s *Store) projectActiveSessionsSQL(since time.Time) (string, []any) {
	where := []string{"l.session_id <> ''", clientVisibleLogClause("l")}
	args := []any{}
	if !since.IsZero() {
		where = append(where, "l.recorded_at >= ?")
		args = append(args, since.UTC())
	}
	return `SELECT l.session_id AS session_id
		FROM logs l
		WHERE ` + strings.Join(where, " AND ") + `
		GROUP BY l.session_id`, args
}

// distinctAggregateSQL renders a GROUP_CONCAT/string_agg over distinct
// non-empty values for the active driver.
func (s *Store) distinctAggregateSQL(expr string) string {
	if s != nil && s.driver == "postgres" {
		return `COALESCE(string_agg(DISTINCT CASE WHEN ` + expr + ` <> '' THEN ` + expr + ` END, ','), '')`
	}
	return `COALESCE(GROUP_CONCAT(DISTINCT CASE WHEN ` + expr + ` <> '' THEN ` + expr + ` END), '')`
}

func splitCommaList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// EnqueueProjectBackfill queues a parse job for the most recent traces that have
// no project row yet. Parsing is where the declaration is extracted, so this is
// the one-shot way to fill the index for traffic recorded before it existed;
// running it again is safe because enqueueing a trace resets its existing job
// instead of adding a second one.
//
// A trace that declares no project never gets a row and therefore stays a
// candidate. That is the same trade-off the resource backfill makes: a marker
// row would cost one row per trace forever just to remember "nothing here", and
// this action is manual and bounded by limit.
func (s *Store) EnqueueProjectBackfill(since time.Time, limit int) (int, error) {
	if limit <= 0 {
		limit = 500
	}
	if limit > 5000 {
		limit = 5000
	}
	where := "l.trace_id <> ''"
	args := []any{}
	if !since.IsZero() {
		where += " AND l.recorded_at >= ?"
		args = append(args, since.UTC())
	}
	args = append(args, limit)
	rows, err := s.db.Query(`
		SELECT l.trace_id
		FROM logs l
		WHERE `+where+`
			AND NOT EXISTS (SELECT 1 FROM trace_projects tp WHERE tp.trace_id = l.trace_id)
			AND NOT EXISTS (
				SELECT 1 FROM parse_jobs p
				WHERE p.trace_id = l.trace_id AND p.status IN ('queued', 'running')
			)
			AND `+clientVisibleLogClause("l")+`
		ORDER BY l.recorded_at DESC, l.trace_id DESC
		LIMIT ?
	`, args...)
	if err != nil {
		return 0, err
	}
	var traceIDs []string
	for rows.Next() {
		var traceID string
		if err := rows.Scan(&traceID); err != nil {
			rows.Close()
			return 0, err
		}
		traceIDs = append(traceIDs, traceID)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	queued := 0
	for _, traceID := range traceIDs {
		if err := s.EnqueueParseJob(traceID); err != nil {
			continue
		}
		queued++
	}
	return queued, nil
}

// ensureProjectSchema creates the project tables on Postgres. SQLite gets the
// same shapes from sqliteSchemaStatements; the two are kept in step by
// TestSQLiteAndPostgresSchemasAgree.
func (s *Store) ensureProjectSchema() error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS "trace_projects" (
			"trace_id" character varying NOT NULL,
			"session_id" character varying NOT NULL DEFAULT '',
			"root" text NOT NULL DEFAULT '',
			"root_key" text NOT NULL DEFAULT '',
			"source" character varying NOT NULL DEFAULT '',
			"conflicting" boolean NOT NULL DEFAULT false,
			"created_at" timestamptz NOT NULL,
			PRIMARY KEY ("trace_id")
		);`,
		`CREATE INDEX IF NOT EXISTS traceproject_session_id ON trace_projects(session_id)`,
		`CREATE INDEX IF NOT EXISTS traceproject_root_key ON trace_projects(root_key)`,
		`CREATE TABLE IF NOT EXISTS "session_projects" (
			"session_id" character varying NOT NULL,
			"root" text NOT NULL DEFAULT '',
			"root_key" text NOT NULL DEFAULT '',
			"source" character varying NOT NULL DEFAULT '',
			"latest_root" text NOT NULL DEFAULT '',
			"latest_root_key" text NOT NULL DEFAULT '',
			"switch_count" bigint NOT NULL DEFAULT 0,
			"first_trace_id" character varying NOT NULL DEFAULT '',
			"first_seen" timestamptz NOT NULL,
			"last_declared_at" timestamptz NOT NULL,
			"updated_at" timestamptz NOT NULL,
			PRIMARY KEY ("session_id")
		);`,
		`CREATE INDEX IF NOT EXISTS sessionproject_root_key ON session_projects(root_key, last_declared_at DESC)`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			return fmt.Errorf("ensure project schema: %w", err)
		}
	}
	return nil
}

// SessionIDForTrace returns the session a trace was recorded under, or "" when
// the request carried no session signal.
func (s *Store) SessionIDForTrace(traceID string) (string, error) {
	var sessionID string
	err := s.db.QueryRow(`SELECT session_id FROM logs WHERE trace_id = ? LIMIT 1`, strings.TrimSpace(traceID)).Scan(&sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(sessionID), nil
}

// rebuildSessionProjectsTx repairs several rollups in a caller's transaction.
func (s *Store) rebuildSessionProjectsTx(tx *sql.Tx, sessionIDs []string) error {
	seen := make(map[string]struct{}, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			continue
		}
		if _, duplicate := seen[sessionID]; duplicate {
			continue
		}
		seen[sessionID] = struct{}{}
		if err := s.rebuildSessionProjectTx(tx, sessionID); err != nil {
			return err
		}
	}
	return nil
}
