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
	"sort"
	"strings"
	"time"
)

// ProjectStatus is whether a trace declared a project or only carried a
// declaration-shaped value that was rejected.
type ProjectStatus string

const (
	// ProjectDeclared means a usable project root was extracted.
	ProjectDeclared ProjectStatus = "declared"
	// ProjectRejected means a declaration block was found but produced no
	// usable root. The reason is kept so a harness that changed its prompt
	// format is visible instead of looking like traffic with no declaration.
	ProjectRejected ProjectStatus = "rejected"
)

// ProjectRecord is what one trace says about its project, as the extractor
// found it: at most one effective root, or the reason a candidate was rejected.
type ProjectRecord struct {
	Root string
	// RootKey is the comparison form of Root (case-folded Windows paths, POSIX
	// kept verbatim). Two declarations with the same key are one project.
	RootKey string
	Source  string
	// MultipleRoots reports that the request declared more than one distinct
	// root; Root holds the effective (last) one.
	MultipleRoots bool
	// Rejected marks a record with no usable root (Root and RootKey empty).
	Rejected bool
	// Reason is the closed rejection vocabulary (placeholder, relative, ...).
	Reason string
	// Evidence is the rejected value or the block that carried no value.
	Evidence string
}

// Status reports how the record should be stored.
func (r ProjectRecord) Status() ProjectStatus {
	if r.Rejected {
		return ProjectRejected
	}
	return ProjectDeclared
}

// TraceProject is the project index row of one trace.
type TraceProject struct {
	TraceID   string
	SessionID string
	Status    ProjectStatus
	Root      string
	RootKey   string
	Source    string
	Reason    string
	Evidence  string
	// MultipleRoots reports that the request declared more than one root.
	MultipleRoots bool
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
	// CanonicalRoot/CanonicalKey are Root/RootKey after operator aliases: when
	// several paths were merged, this is the project the session is counted in.
	CanonicalRoot string
	CanonicalKey  string
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
	// PathCount is how many raw project paths the row merges: more than one
	// means operator aliases folded several directories into this project.
	PathCount int
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

// SaveTraceProject replaces the project record of one trace. record == nil
// removes any previous row, which is what a reparse that no longer finds a
// declaration must do.
func (s *Store) SaveTraceProject(traceID string, sessionID string, record *ProjectRecord) error {
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
	if record != nil {
		root := strings.TrimSpace(record.Root)
		rootKey := strings.TrimSpace(record.RootKey)
		reason := strings.TrimSpace(record.Reason)
		status := record.Status()
		if status == ProjectDeclared && (root == "" || rootKey == "") {
			return errors.New("save trace project: root and root key are required")
		}
		if status == ProjectRejected && reason == "" {
			return errors.New("save trace project: a rejected record requires a reason")
		}
		if _, err := s.execTx(tx, `
			INSERT INTO trace_projects (
				trace_id, session_id, status, root, root_key, source, reason, evidence, multiple_roots, created_at
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, traceID, strings.TrimSpace(sessionID), string(status), root, rootKey,
			strings.TrimSpace(record.Source), reason, strings.TrimSpace(record.Evidence),
			record.MultipleRoots, time.Now().UTC()); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// GetTraceProject returns the project declaration of one trace, if any.
func (s *Store) GetTraceProject(traceID string) (TraceProject, bool, error) {
	var (
		item   TraceProject
		status string
	)
	err := s.db.QueryRow(`
		SELECT trace_id, session_id, status, root, root_key, source, reason, evidence, multiple_roots
		FROM trace_projects
		WHERE trace_id = ?
	`, strings.TrimSpace(traceID)).Scan(
		&item.TraceID, &item.SessionID, &status, &item.Root, &item.RootKey, &item.Source,
		&item.Reason, &item.Evidence, &item.MultipleRoots,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return TraceProject{}, false, nil
	}
	if err != nil {
		return TraceProject{}, false, err
	}
	item.Status = ProjectStatus(status)
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
		WHERE l.session_id = ? AND tp.status = 'declared' AND `+clientVisibleLogClause("l")+`
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
	aliases, err := s.ProjectAliasMap()
	if err != nil {
		return SessionProject{}, false, err
	}
	item.CanonicalKey, item.CanonicalRoot = resolveProjectAlias(aliases, item.RootKey, item.Root)
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
	aliases, err := s.ProjectAliasMap()
	if err != nil {
		return nil, err
	}
	for sessionID, item := range out {
		item.CanonicalKey, item.CanonicalRoot = resolveProjectAlias(aliases, item.RootKey, item.Root)
		out[sessionID] = item
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

	// Fold operator aliases into one row per project. The SQL groups by raw
	// path; the merge happens here so the same alias table shapes the page, the
	// session filter and the session views without a join in every query.
	aliases, err := s.ProjectAliasMap()
	if err != nil {
		return ProjectPage{}, err
	}
	all = mergeProjectSummaries(all, aliases)

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
func (s *Store) EnqueueProjectBackfill(since time.Time, limit int) (int, error) {
	_, queued, err := s.enqueueProjectBackfill(since, time.Time{}, "", nil, limit, ParsePriorityBackfill)
	return queued.Queued, err
}

// ProjectBackfillPlan is one sweep step: what the selector found and what it
// managed to queue.
//
// Candidates and Queued are reported separately because they answer different
// questions. "No candidates" means the sweep reached the oldest trace and is
// done; "candidates but nothing queued" is a failure to retry. Collapsing the
// two would either mark a failed pass complete or advance the cursor past traces
// that were never queued.
type ProjectBackfillPlan struct {
	Candidates int
	Queued     int
	// Oldest and OldestTraceID locate the last queued trace, which becomes the
	// next cursor. They are only set when Queued > 0.
	Oldest        time.Time
	OldestTraceID string
}

// EnqueueProjectBackfillThrough queues the newest un-attributed traces recorded
// before the cursor, and reports where the sweep should resume.
//
// The cursor is the (recorded_at, trace_id) of the last trace the previous pass
// queued. The trace id breaks ties: a burst can record many requests inside one
// microsecond, and a timestamp-only cursor would jump past the ones that did not
// fit in the chunk - they would never be swept.
// harnesses, when non-empty, restricts the sweep to those client_harness ids.
// A historical sweep of every trace would re-parse hundreds of thousands of
// plain SDK calls to find the few clients that declare a working directory, so
// the caller can scope it to the harnesses that can. An empty list means "every
// trace", which is what the manual backfill and an explicit `all` scope use.
func (s *Store) EnqueueProjectBackfillThrough(through time.Time, throughTraceID string, harnesses []string, limit int) (ProjectBackfillPlan, error) {
	candidates, queued, err := s.enqueueProjectBackfill(time.Time{}, through, throughTraceID, harnesses, limit, ParsePriorityBackfill)
	return ProjectBackfillPlan{Candidates: candidates, Queued: queued.Queued, Oldest: queued.Oldest, OldestTraceID: queued.OldestTraceID}, err
}

type backfillQueued struct {
	Queued        int
	Oldest        time.Time
	OldestTraceID string
}

func (s *Store) enqueueProjectBackfill(since, through time.Time, throughTraceID string, harnesses []string, limit int, lane string) (int, backfillQueued, error) {
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
		args = append(args, since.UTC().Format(timeLayout))
	}
	if !through.IsZero() {
		// Strictly before the cursor. The bound is formatted exactly like the
		// column is written: recorded_at is a timeLayout string, so passing a
		// time.Time would be compared as text in a different layout by SQLite and
		// silently match nothing.
		bound := through.UTC().Format(timeLayout)
		if throughTraceID != "" {
			// Same timestamp, smaller trace id: the tie-break keeps a burst that
			// straddles a chunk boundary from being skipped.
			where += " AND (l.recorded_at < ? OR (l.recorded_at = ? AND l.trace_id < ?))"
			args = append(args, bound, bound, throughTraceID)
		} else {
			where += " AND l.recorded_at < ?"
			args = append(args, bound)
		}
	}
	if len(harnesses) > 0 {
		placeholders := make([]string, len(harnesses))
		for i, id := range harnesses {
			placeholders[i] = "?"
			args = append(args, id)
		}
		where += " AND l.client_harness IN (" + strings.Join(placeholders, ",") + ")"
	}
	args = append(args, limit)
	rows, err := s.db.Query(`
		SELECT l.trace_id, l.recorded_at
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
		return 0, backfillQueued{}, err
	}
	type candidate struct {
		traceID  string
		recorded time.Time
	}
	candidates := make([]candidate, 0, limit)
	for rows.Next() {
		var traceID string
		var recorded any
		if err := rows.Scan(&traceID, &recorded); err != nil {
			rows.Close()
			return 0, backfillQueued{}, err
		}
		at, err := timeParseValue(recorded)
		if err != nil {
			continue
		}
		candidates = append(candidates, candidate{traceID: traceID, recorded: at})
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, backfillQueued{}, err
	}
	traceIDs := make([]string, 0, len(candidates))
	for _, item := range candidates {
		traceIDs = append(traceIDs, item.traceID)
	}
	// Historical work goes to its own lane: a backfill of a hundred thousand
	// traces must never delay the trace the proxy recorded a second ago.
	queued, err := s.EnqueueParseJobsInLane(traceIDs, lane)
	if err != nil {
		return len(candidates), backfillQueued{}, err
	}
	result := backfillQueued{Queued: queued}
	// The cursor may only pass traces that are actually queued. Advancing it over
	// a failed insert would skip that trace for good, so when a batch statement
	// fails the caller gets the error and retries the same window.
	if queued > 0 {
		oldest := candidates[len(candidates)-1]
		result.Oldest = oldest.recorded
		result.OldestTraceID = oldest.traceID
	}
	return len(candidates), result, nil
}

// ensureProjectSchema creates the project tables on Postgres. SQLite gets the
// same shapes from sqliteSchemaStatements; the two are kept in step by
// TestSQLiteAndPostgresSchemasAgree.
func (s *Store) ensureProjectSchema() error {
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS "trace_projects" (
			"trace_id" character varying NOT NULL,
			"session_id" character varying NOT NULL DEFAULT '',
			"status" character varying NOT NULL DEFAULT 'declared',
			"root" text NOT NULL DEFAULT '',
			"root_key" text NOT NULL DEFAULT '',
			"source" character varying NOT NULL DEFAULT '',
			"reason" character varying NOT NULL DEFAULT '',
			"evidence" text NOT NULL DEFAULT '',
			"multiple_roots" boolean NOT NULL DEFAULT false,
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
		`CREATE TABLE IF NOT EXISTS "project_aliases" (
			"alias_key" character varying NOT NULL,
			"canonical_key" character varying NOT NULL DEFAULT '',
			"canonical_root" text NOT NULL DEFAULT '',
			"note" text NOT NULL DEFAULT '',
			"created_at" timestamptz NOT NULL,
			"updated_at" timestamptz NOT NULL,
			PRIMARY KEY ("alias_key")
		);`,
		`CREATE INDEX IF NOT EXISTS projectalias_canonical_key ON project_aliases(canonical_key)`,
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

// ProjectDetectionCount is one labelled count in the detection-health report.
type ProjectDetectionCount struct {
	Label string
	Count int
}

// ProjectDetectionHarness splits the detection counts per client harness, which
// is how a harness that changed its prompt format becomes visible: one harness'
// rejected count rises while the others stay flat.
type ProjectDetectionHarness struct {
	Harness  string
	Declared int
	Rejected int
}

// ProjectRejection is one recent trace whose declaration was rejected.
type ProjectRejection struct {
	TraceID    string
	SessionID  string
	Source     string
	Reason     string
	Evidence   string
	Harness    string
	RecordedAt time.Time
}

// ProjectDetection is the detection-health report of one window.
type ProjectDetection struct {
	Declared  int
	Rejected  int
	ByReason  []ProjectDetectionCount
	ByHarness []ProjectDetectionHarness
	Recent    []ProjectRejection
}

// ProjectDetectionReport aggregates how declaration extraction went over a
// window: how many traces declared a project, how many carried a
// declaration-shaped value that was rejected and why, and which harnesses those
// rejections belong to.
//
// A rejected trace is the only evidence that a harness changed its prompt
// format: without this report the extraction simply stops producing projects
// for that client, which is indistinguishable from traffic that never declared
// one.
func (s *Store) ProjectDetectionReport(filter ProjectDetectionFilter) (ProjectDetection, error) {
	window := []string{clientVisibleLogClause("l")}
	args := []any{}
	if !filter.Since.IsZero() {
		window = append(window, "l.recorded_at >= ?")
		args = append(args, filter.Since.UTC())
	}
	windowSQL := strings.Join(window, " AND ")
	report := ProjectDetection{}

	rows, err := s.db.Query(`
		SELECT COALESCE(l.client_harness, '') AS harness,
			COALESCE(SUM(CASE WHEN tp.status = 'declared' THEN 1 ELSE 0 END), 0) AS declared,
			COALESCE(SUM(CASE WHEN tp.status = 'rejected' THEN 1 ELSE 0 END), 0) AS rejected
		FROM trace_projects tp
		JOIN logs l ON l.trace_id = tp.trace_id
		WHERE `+windowSQL+`
		GROUP BY COALESCE(l.client_harness, '')
		ORDER BY rejected DESC, declared DESC, harness ASC
	`, args...)
	if err != nil {
		return ProjectDetection{}, err
	}
	for rows.Next() {
		var item ProjectDetectionHarness
		if err := rows.Scan(&item.Harness, &item.Declared, &item.Rejected); err != nil {
			rows.Close()
			return ProjectDetection{}, err
		}
		report.Declared += item.Declared
		report.Rejected += item.Rejected
		report.ByHarness = append(report.ByHarness, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return ProjectDetection{}, err
	}

	rows, err = s.db.Query(`
		SELECT tp.reason, COUNT(*) AS count
		FROM trace_projects tp
		JOIN logs l ON l.trace_id = tp.trace_id
		WHERE tp.status = 'rejected' AND `+windowSQL+`
		GROUP BY tp.reason
		ORDER BY count DESC, tp.reason ASC
	`, args...)
	if err != nil {
		return ProjectDetection{}, err
	}
	for rows.Next() {
		var item ProjectDetectionCount
		if err := rows.Scan(&item.Label, &item.Count); err != nil {
			rows.Close()
			return ProjectDetection{}, err
		}
		report.ByReason = append(report.ByReason, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return ProjectDetection{}, err
	}

	rows, err = s.db.Query(`
		SELECT tp.trace_id, tp.session_id, tp.source, tp.reason, tp.evidence,
			COALESCE(l.client_harness, ''), l.recorded_at
		FROM trace_projects tp
		JOIN logs l ON l.trace_id = tp.trace_id
		WHERE tp.status = 'rejected' AND `+windowSQL+`
		ORDER BY l.recorded_at DESC, tp.trace_id DESC
		LIMIT 20
	`, args...)
	if err != nil {
		return ProjectDetection{}, err
	}
	for rows.Next() {
		var (
			item     ProjectRejection
			recorded any
		)
		if err := rows.Scan(&item.TraceID, &item.SessionID, &item.Source, &item.Reason,
			&item.Evidence, &item.Harness, &recorded); err != nil {
			rows.Close()
			return ProjectDetection{}, err
		}
		if item.RecordedAt, err = timeParseValue(recorded); err != nil {
			rows.Close()
			return ProjectDetection{}, err
		}
		report.Recent = append(report.Recent, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return ProjectDetection{}, err
	}
	return report, nil
}

// ProjectDetectionFilter bounds the detection-health window.
type ProjectDetectionFilter struct {
	Since time.Time
}

// ErrProjectAliasRequired / ErrProjectAliasSelf / ErrProjectAliasChain are the
// operator errors a merge can produce. They are sentinels so the HTTP layer can
// answer a precise 400 instead of a generic database failure.
var (
	ErrProjectAliasRequired = errors.New("project alias: alias key and canonical key are required")
	ErrProjectAliasSelf     = errors.New("project alias: a project cannot be merged into itself")
	ErrProjectAliasChain    = errors.New("project alias: aliases cannot chain; point the alias at the final project")
)

// ProjectAlias maps one raw project root key onto the project it belongs to.
//
// Aliases are applied when reading, never by rewriting session_projects: a
// merge is a view decision (the same repository seen through a container path,
// a worktree or a different mount), so it must be reversible and must not
// touch derived rows a reparse would rebuild anyway.
type ProjectAlias struct {
	AliasKey      string
	CanonicalKey  string
	CanonicalRoot string
	Note          string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// ListProjectAliases returns every alias, oldest first.
func (s *Store) ListProjectAliases() ([]ProjectAlias, error) {
	rows, err := s.db.Query(`
		SELECT alias_key, canonical_key, canonical_root, note, created_at, updated_at
		FROM project_aliases
		ORDER BY created_at ASC, alias_key ASC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ProjectAlias, 0, 8)
	for rows.Next() {
		var (
			item                 ProjectAlias
			createdAt, updatedAt any
		)
		if err := rows.Scan(&item.AliasKey, &item.CanonicalKey, &item.CanonicalRoot, &item.Note, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		if item.CreatedAt, err = timeParseValue(createdAt); err != nil {
			return nil, err
		}
		if item.UpdatedAt, err = timeParseValue(updatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// ProjectAliasMap returns the aliases keyed by alias key for read-time
// resolution.
func (s *Store) ProjectAliasMap() (map[string]ProjectAlias, error) {
	items, err := s.ListProjectAliases()
	if err != nil {
		return nil, err
	}
	out := make(map[string]ProjectAlias, len(items))
	for _, item := range items {
		out[item.AliasKey] = item
	}
	return out, nil
}

// SaveProjectAlias creates or updates one alias after checking that it cannot
// form a chain: the target must not itself be an alias, and the source must not
// be a merge target of another alias. One hop is all the reader applies, so a
// chain would silently drop part of a merge.
func (s *Store) SaveProjectAlias(alias ProjectAlias) error {
	alias.AliasKey = strings.TrimSpace(alias.AliasKey)
	alias.CanonicalKey = strings.TrimSpace(alias.CanonicalKey)
	alias.CanonicalRoot = strings.TrimSpace(alias.CanonicalRoot)
	alias.Note = strings.TrimSpace(alias.Note)
	if alias.AliasKey == "" || alias.CanonicalKey == "" {
		return ErrProjectAliasRequired
	}
	if alias.AliasKey == alias.CanonicalKey {
		return ErrProjectAliasSelf
	}
	if alias.CanonicalRoot == "" {
		alias.CanonicalRoot = alias.CanonicalKey
	}
	var chains int
	if err := s.db.QueryRow(`
		SELECT COUNT(*) FROM project_aliases
		WHERE alias_key = ? OR canonical_key = ?
	`, alias.CanonicalKey, alias.AliasKey).Scan(&chains); err != nil {
		return err
	}
	if chains > 0 {
		return ErrProjectAliasChain
	}
	now := time.Now().UTC()
	_, err := s.db.Exec(`
		INSERT INTO project_aliases (alias_key, canonical_key, canonical_root, note, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (alias_key) DO UPDATE SET
			canonical_key = excluded.canonical_key,
			canonical_root = excluded.canonical_root,
			note = excluded.note,
			updated_at = excluded.updated_at
	`, alias.AliasKey, alias.CanonicalKey, alias.CanonicalRoot, alias.Note, now, now)
	return err
}

// DeleteProjectAlias removes one alias, undoing its merge.
func (s *Store) DeleteProjectAlias(aliasKey string) error {
	_, err := s.db.Exec(`DELETE FROM project_aliases WHERE alias_key = ?`, strings.TrimSpace(aliasKey))
	return err
}

// resolveProjectAlias applies one alias hop to a raw root key.
func resolveProjectAlias(aliases map[string]ProjectAlias, rootKey string, root string) (canonicalKey string, canonicalRoot string) {
	canonicalKey, canonicalRoot = rootKey, root
	if alias, ok := aliases[rootKey]; ok {
		if alias.CanonicalKey != "" {
			canonicalKey = alias.CanonicalKey
		}
		if alias.CanonicalRoot != "" {
			canonicalRoot = alias.CanonicalRoot
		}
	}
	return canonicalKey, canonicalRoot
}

// projectFilterKeys expands one requested project key into every raw root key
// of its merged group: the key itself, its canonical key, and each alias
// pointing at that canonical key. Filtering by either the canonical path or one
// of the merged paths therefore lists the whole project.
func (s *Store) projectFilterKeys(key string) ([]string, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, nil
	}
	aliases, err := s.ProjectAliasMap()
	if err != nil {
		return nil, err
	}
	canonical := key
	if alias, ok := aliases[key]; ok && alias.CanonicalKey != "" {
		canonical = alias.CanonicalKey
	}
	keys := []string{canonical}
	seen := map[string]struct{}{canonical: {}}
	add := func(candidate string) {
		candidate = strings.TrimSpace(candidate)
		if candidate == "" {
			return
		}
		if _, duplicate := seen[candidate]; duplicate {
			return
		}
		seen[candidate] = struct{}{}
		keys = append(keys, candidate)
	}
	add(key)
	for aliasKey, alias := range aliases {
		if alias.CanonicalKey == canonical {
			add(aliasKey)
		}
	}
	return keys, nil
}

// mergeProjectSummaries folds alias-resolved rows into one row per project.
func mergeProjectSummaries(rows []ProjectSummary, aliases map[string]ProjectAlias) []ProjectSummary {
	type merged struct {
		summary ProjectSummary
		paths   map[string]struct{}
		sources map[string]struct{}
	}
	byKey := make(map[string]*merged, len(rows))
	order := make([]string, 0, len(rows))
	for _, row := range rows {
		key, root := resolveProjectAlias(aliases, row.RootKey, row.Root)
		entry, ok := byKey[key]
		if !ok {
			entry = &merged{
				summary: ProjectSummary{RootKey: key, Root: root, FirstSeen: row.FirstSeen, LastSeen: row.LastSeen},
				paths:   map[string]struct{}{},
				sources: map[string]struct{}{},
			}
			byKey[key] = entry
			order = append(order, key)
		}
		if row.Root < entry.summary.Root {
			entry.summary.Root = row.Root
		}
		entry.summary.SessionCount += row.SessionCount
		entry.summary.RequestCount += row.RequestCount
		entry.summary.TotalTokens += row.TotalTokens
		if row.FirstSeen.Before(entry.summary.FirstSeen) {
			entry.summary.FirstSeen = row.FirstSeen
		}
		if row.LastSeen.After(entry.summary.LastSeen) {
			entry.summary.LastSeen = row.LastSeen
		}
		entry.paths[row.RootKey] = struct{}{}
		for _, source := range row.Sources {
			entry.sources[source] = struct{}{}
		}
	}
	out := make([]ProjectSummary, 0, len(order))
	for _, key := range order {
		entry := byKey[key]
		entry.summary.PathCount = len(entry.paths)
		entry.summary.Sources = sortedStringSet(entry.sources)
		out = append(out, entry.summary)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].SessionCount != out[j].SessionCount {
			return out[i].SessionCount > out[j].SessionCount
		}
		if !out[i].LastSeen.Equal(out[j].LastSeen) {
			return out[i].LastSeen.After(out[j].LastSeen)
		}
		return out[i].RootKey < out[j].RootKey
	})
	return out
}

func sortedStringSet(values map[string]struct{}) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

// projectFilterKeys returns the resolved project keys of a filter, falling back
// to the unexpanded key for a caller that never went through ListSessionPage.
func projectFilterKeys(filter ListFilter) []string {
	if len(filter.ProjectKeys) > 0 {
		return filter.ProjectKeys
	}
	if key := strings.TrimSpace(filter.Project); key != "" {
		return []string{key}
	}
	return nil
}

// sessionProjectClause builds the session-level project predicate over a set of
// raw root keys.
func sessionProjectClause(keys []string, alias string) (string, []any) {
	prefix := ""
	if alias != "" {
		prefix = alias + "."
	}
	args := make([]any, 0, len(keys))
	for _, key := range keys {
		args = append(args, key)
	}
	return `EXISTS (SELECT 1 FROM session_projects spj WHERE spj.session_id = ` + prefix +
		`session_id AND spj.root_key IN (` + placeholders(len(keys)) + `))`, args
}

// CountDeclaredTraceProjects reports how many traces are attributed to a
// declared project. It is the cheap progress figure the queue panel shows: the
// total number of traces is a log-index scan the projects page already pays for.
func (s *Store) CountDeclaredTraceProjects() (int, error) {
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM trace_projects WHERE status = 'declared'`).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}
