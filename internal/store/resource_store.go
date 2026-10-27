// Resource index: the distinct local filesystem paths and URLs the model's
// tool calls in a recorded exchange named.
//
// The cassette remains the source of truth; this is a derived index over it. It
// is deliberately two tables rather than one row per occurrence:
//
//   - resources is the global catalogue, one row per (kind, value) however many
//     traces saw it. This is what keeps a path that an agent reads on every run
//     from growing the database with traffic.
//   - trace_resources is the relation back to one trace, one row per distinct
//     value in that trace. The per-trace list and the "which traces" count read
//     from here, and no file content is ever stored.
//
// Counts are computed on read from trace_resources instead of being maintained
// as counters: a reparse is then an idempotent replace of the trace's rows, with
// no delta arithmetic that a concurrent writer or a deleted trace could corrupt.

package store

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

// ResourceAccess is one distinct value of a trace as the extractor knows it.
type ResourceAccess struct {
	Kind string
	// Value is the path or URL. It is never file content.
	Value string
	// Count is the number of tool calls that named it.
	Count int
	// Position is the order of first appearance within the trace.
	Position int
}

// TraceResource is one resource of one trace, as the trace detail view reads it.
type TraceResource struct {
	ID          int64     `json:"id"`
	Kind        string    `json:"kind"`
	Value       string    `json:"value"`
	AccessCount int       `json:"access_count"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
}

// ResourceSummary is one catalogue row with its aggregate access counts.
type ResourceSummary struct {
	ID          int64     `json:"id"`
	Kind        string    `json:"kind"`
	Value       string    `json:"value"`
	LastTraceID string    `json:"last_trace_id"`
	FirstSeenAt time.Time `json:"first_seen_at"`
	LastSeenAt  time.Time `json:"last_seen_at"`
	TraceCount  int       `json:"trace_count"`
	AccessCount int64     `json:"access_count"`
}

// ResourceFilter selects catalogue rows for the global resources page.
type ResourceFilter struct {
	Kind     string
	Query    string
	Since    time.Time
	Page     int
	PageSize int
}

type ResourcePage struct {
	Items      []ResourceSummary `json:"items"`
	Total      int               `json:"total"`
	Page       int               `json:"page"`
	PageSize   int               `json:"page_size"`
	TotalPages int               `json:"total_pages"`
}

// SaveTraceResources replaces the resource index of one trace.
//
// Replacing rather than appending is what makes a reparse idempotent: the same
// trace indexed twice lands on one row per value, and a value the new parse no
// longer sees disappears. The catalogue row itself is not deleted, because
// other traces may share it and the aggregate counts are computed from the
// relations that remain.
func (s *Store) SaveTraceResources(traceID string, accesses []ResourceAccess) error {
	traceID = strings.TrimSpace(traceID)
	if traceID == "" {
		return errors.New("save trace resources: trace id is required")
	}
	if s == nil || s.db == nil {
		return errors.New("save trace resources: database is not configured")
	}
	// The trace's own timestamp, not the time of the reparse, so re-indexing an
	// old trace cannot move the catalogue's last_seen to today.
	seenAt := s.traceRecordedAt(traceID)
	now := time.Now().UTC()

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	// The old relation is read before it is replaced so a value that the new
	// parse no longer sees can drop its catalogue row when this trace was its
	// last user. Without this, a parser or extractor change would leave values
	// that no page can ever show.
	oldIDs, err := s.resourceIDsTx(tx, traceID)
	if err != nil {
		return err
	}

	type link struct {
		resourceID int64
		access     ResourceAccess
	}
	links := make([]link, 0, len(accesses))
	seen := make(map[string]struct{}, len(accesses))
	for _, access := range accesses {
		kind := strings.TrimSpace(access.Kind)
		if kind == "" || access.Value == "" {
			continue
		}
		key := kind + "\x00" + access.Value
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		var resourceID int64
		if err := s.queryRowTx(tx, `
			INSERT INTO resources (kind, value, value_hash, last_trace_id, first_seen_at, last_seen_at)
			VALUES (?, ?, ?, ?, ?, ?)
			ON CONFLICT (kind, value_hash) DO UPDATE SET
				value = excluded.value,
				last_trace_id = CASE
					WHEN excluded.last_seen_at >= resources.last_seen_at THEN excluded.last_trace_id
					ELSE resources.last_trace_id
				END,
				first_seen_at = CASE
					WHEN excluded.first_seen_at < resources.first_seen_at THEN excluded.first_seen_at
					ELSE resources.first_seen_at
				END,
				last_seen_at = CASE
					WHEN excluded.last_seen_at > resources.last_seen_at THEN excluded.last_seen_at
					ELSE resources.last_seen_at
				END
			RETURNING id
		`, kind, access.Value, resourceValueHash(kind, access.Value), traceID, seenAt, seenAt).Scan(&resourceID); err != nil {
			return fmt.Errorf("upsert resource: %w", err)
		}
		links = append(links, link{resourceID: resourceID, access: access})
	}

	// Replace the relation wholesale. It is a handful of rows per trace, and it
	// removes values the new parse no longer sees without diffing old and new.
	if _, err := s.execTx(tx, `DELETE FROM trace_resources WHERE trace_id = ?`, traceID); err != nil {
		return fmt.Errorf("clear trace resources: %w", err)
	}
	newIDs := make(map[int64]struct{}, len(links))
	for _, item := range links {
		newIDs[item.resourceID] = struct{}{}
		if _, err := s.execTx(tx, `
			INSERT INTO trace_resources (trace_id, resource_id, access_count, first_position, created_at)
			VALUES (?, ?, ?, ?, ?)
		`, traceID, item.resourceID, item.access.Count, item.access.Position, now); err != nil {
			return fmt.Errorf("insert trace resource: %w", err)
		}
	}
	// Drop catalogue rows this trace was the last user of. A value another
	// trace still links stays; the NOT EXISTS carries that decision.
	for _, resourceID := range oldIDs {
		if _, ok := newIDs[resourceID]; ok {
			continue
		}
		if _, err := s.execTx(tx, `
			DELETE FROM resources
			WHERE id = ? AND NOT EXISTS (SELECT 1 FROM trace_resources WHERE resource_id = ?)
		`, resourceID, resourceID); err != nil {
			return fmt.Errorf("delete orphan resource: %w", err)
		}
	}
	return tx.Commit()
}

// ListTraceResources returns the resources of one trace in first-appearance
// order.
func (s *Store) ListTraceResources(traceID string) ([]TraceResource, error) {
	rows, err := s.db.Query(`
		SELECT r.id, r.kind, r.value, tr.access_count, r.first_seen_at, r.last_seen_at
		FROM trace_resources tr
		JOIN resources r ON r.id = tr.resource_id
		WHERE tr.trace_id = ?
		ORDER BY tr.first_position ASC, r.id ASC
	`, strings.TrimSpace(traceID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]TraceResource, 0, 8)
	for rows.Next() {
		var (
			item      TraceResource
			firstSeen any
			lastSeen  any
		)
		if err := rows.Scan(&item.ID, &item.Kind, &item.Value, &item.AccessCount, &firstSeen, &lastSeen); err != nil {
			return nil, err
		}
		if item.FirstSeenAt, err = timeParseValue(firstSeen); err != nil {
			return nil, err
		}
		if item.LastSeenAt, err = timeParseValue(lastSeen); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

// CountTraceResources returns the number of distinct resources indexed for one
// trace.
func (s *Store) CountTraceResources(traceID string) (int, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM trace_resources WHERE trace_id = ?`, strings.TrimSpace(traceID)).Scan(&count)
	return count, err
}

// ListResources returns the global catalogue newest-seen first, with the number
// of traces that referenced each value and the total node count across them.
func (s *Store) ListResources(filter ResourceFilter) (ResourcePage, error) {
	page, pageSize := normalizePage(filter.Page, filter.PageSize)

	where := []string{"1 = 1"}
	args := []any{}
	if kind := strings.ToLower(strings.TrimSpace(filter.Kind)); kind != "" {
		where = append(where, "r.kind = ?")
		args = append(args, kind)
	}
	if query := strings.TrimSpace(filter.Query); query != "" {
		where = append(where, "LOWER(r.value) LIKE ?")
		args = append(args, "%"+escapeLike(strings.ToLower(query))+"%")
	}
	if !filter.Since.IsZero() {
		where = append(where, "r.last_seen_at >= ?")
		args = append(args, filter.Since.UTC())
	}
	whereSQL := strings.Join(where, " AND ")

	// Only values still referenced by at least one trace are listed. A
	// catalogue row whose last trace was re-parsed away keeps its identity but
	// has nothing left to link to, and showing it as "0 traces" reads as a bug.
	visible := ` AND EXISTS (SELECT 1 FROM trace_resources trx WHERE trx.resource_id = r.id)`
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM resources r WHERE `+whereSQL+visible, args...).Scan(&total); err != nil {
		return ResourcePage{}, err
	}

	queryArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	rows, err := s.db.Query(`
		SELECT r.id, r.kind, r.value, r.last_trace_id, r.first_seen_at, r.last_seen_at,
			COUNT(tr.trace_id), COALESCE(SUM(tr.access_count), 0)
		FROM resources r
		JOIN trace_resources tr ON tr.resource_id = r.id
		WHERE `+whereSQL+`
		GROUP BY r.id, r.kind, r.value, r.last_trace_id, r.first_seen_at, r.last_seen_at
		ORDER BY r.last_seen_at DESC, r.id DESC
		LIMIT ? OFFSET ?
	`, queryArgs...)
	if err != nil {
		return ResourcePage{}, err
	}
	defer rows.Close()

	items := make([]ResourceSummary, 0, pageSize)
	for rows.Next() {
		var (
			item      ResourceSummary
			firstSeen any
			lastSeen  any
			traceCnt  int64
		)
		if err := rows.Scan(&item.ID, &item.Kind, &item.Value, &item.LastTraceID, &firstSeen, &lastSeen, &traceCnt, &item.AccessCount); err != nil {
			return ResourcePage{}, err
		}
		item.TraceCount = int(traceCnt)
		if item.FirstSeenAt, err = timeParseValue(firstSeen); err != nil {
			return ResourcePage{}, err
		}
		if item.LastSeenAt, err = timeParseValue(lastSeen); err != nil {
			return ResourcePage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return ResourcePage{}, err
	}
	return ResourcePage{
		Items:      items,
		Total:      total,
		Page:       page,
		PageSize:   pageSize,
		TotalPages: totalPages(total, pageSize),
	}, nil
}

// traceRecordedAt reads the trace's own timestamp, falling back to now when the
// log row is gone (a cassette indexed without an entry, or a deleted trace).
func (s *Store) traceRecordedAt(traceID string) time.Time {
	var raw any
	if err := s.db.QueryRow(`SELECT recorded_at FROM logs WHERE trace_id = ?`, traceID).Scan(&raw); err != nil {
		return time.Now().UTC()
	}
	at, err := timeParseValue(raw)
	if err != nil || at.IsZero() {
		return time.Now().UTC()
	}
	return at.UTC()
}

// resourceValueHash is the identity half of the catalogue key. The unique index
// is on the hash rather than on the value so a very long URL cannot exceed the
// database's index key limit; collisions are a 256-bit non-event, and the full
// value is still stored for display.
func resourceValueHash(kind string, value string) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + value))
	return hex.EncodeToString(sum[:])
}

// ensureResourcesSchema creates the resource tables on Postgres. SQLite gets the
// same shapes from sqliteSchemaStatements; the two are kept in step by
// TestSQLiteAndPostgresSchemasAgree.
func (s *Store) ensureResourcesSchema() error {
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS "resources" (
		"id" bigint GENERATED BY DEFAULT AS IDENTITY,
		"kind" character varying NOT NULL DEFAULT '',
		"value" text NOT NULL DEFAULT '',
		"value_hash" character varying NOT NULL DEFAULT '',
		"last_trace_id" character varying NOT NULL DEFAULT '',
		"first_seen_at" timestamptz NOT NULL,
		"last_seen_at" timestamptz NOT NULL,
		PRIMARY KEY ("id")
	);`); err != nil {
		return err
	}
	for _, stmt := range []string{
		`CREATE UNIQUE INDEX IF NOT EXISTS resource_kind_value_hash ON resources(kind, value_hash)`,
		`CREATE INDEX IF NOT EXISTS resource_last_seen_at_id ON resources(last_seen_at DESC, id DESC)`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS "trace_resources" (
		"trace_id" character varying NOT NULL,
		"resource_id" bigint NOT NULL,
		"access_count" bigint NOT NULL DEFAULT 0,
		"first_position" bigint NOT NULL DEFAULT 0,
		"created_at" timestamptz NOT NULL,
		PRIMARY KEY ("trace_id", "resource_id")
	);`); err != nil {
		return err
	}
	if _, err := s.db.Exec(`CREATE INDEX IF NOT EXISTS traceresource_resource_id_trace_id ON trace_resources(resource_id, trace_id)`); err != nil {
		return err
	}
	return nil
}

// EnqueueResourceBackfill queues a parse job for the most recent traces that
// have no resource rows yet. The parse worker is where extraction runs, so this
// is the one-shot way to fill the index for traffic recorded before it existed;
// running it again is safe because enqueueing a trace resets its existing job
// rather than adding a second one.
//
// A trace that genuinely carries no path or URL never gets a row and therefore
// remains a candidate. That is deliberate: a marker row would cost one row per
// trace forever just to remember "nothing here", and this action is manual and
// bounded by limit.
func (s *Store) EnqueueResourceBackfill(since time.Time, limit int) (int, error) {
	if limit <= 0 {
		limit = 500
	}
	if limit > 5000 {
		limit = 5000
	}
	where := "1 = 1"
	args := []any{}
	if !since.IsZero() {
		where = "l.recorded_at >= ?"
		args = append(args, since.UTC())
	}
	args = append(args, limit)
	rows, err := s.db.Query(`
		SELECT l.trace_id, l.path
		FROM logs l
		WHERE `+where+`
			AND NOT EXISTS (SELECT 1 FROM trace_resources tr WHERE tr.trace_id = l.trace_id)
		ORDER BY l.recorded_at DESC, l.trace_id DESC
		LIMIT ?
	`, args...)
	if err != nil {
		return 0, err
	}
	type candidate struct{ traceID, path string }
	var candidates []candidate
	for rows.Next() {
		var item candidate
		if err := rows.Scan(&item.traceID, &item.path); err != nil {
			rows.Close()
			return 0, err
		}
		candidates = append(candidates, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return 0, err
	}
	queued := 0
	for _, item := range candidates {
		// A stale log row whose cassette is gone can only fail its parse job;
		// queueing it would fill the analysis list with failures the operator
		// cannot act on. The row itself is a separate index-hygiene issue.
		if item.path != "" {
			if _, err := os.Stat(item.path); err != nil {
				continue
			}
		}
		if err := s.EnqueueParseJob(item.traceID); err != nil {
			return queued, err
		}
		queued++
	}
	return queued, nil
}

// resourceIDsTx loads the resource ids currently linked to one trace.
func (s *Store) resourceIDsTx(tx *sql.Tx, traceID string) ([]int64, error) {
	rows, err := s.queryTx(tx, `SELECT resource_id FROM trace_resources WHERE trace_id = ?`, traceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
