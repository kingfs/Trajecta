// Credential masking audits: one row per value the proxy replaced with a
// format-preserving stand-in, plus the notification the event feed raises for
// the rules that fired.
//
// The cassette prelude is still the durable record of the exchange and still
// says a replacement happened; these rows are the part an auditor reads the pair
// out of. Read the migration of the same name for why the two are separate and
// what "storage_mode" means.

package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// CredentialMaskAudit states whose values are already decrypted, which is the
// form the list and detail readers hand back. Writes use CredentialMaskAuditRecord.
type CredentialMaskAudit struct {
	ID          string    `json:"id"`
	TraceID     string    `json:"trace_id"`
	EventIndex  int       `json:"event_index"`
	Rule        string    `json:"rule"`
	Vendor      string    `json:"vendor"`
	Kind        string    `json:"kind"`
	Confidence  string    `json:"confidence"`
	Score       int       `json:"score"`
	Reasons     string    `json:"reasons"`
	Start       int       `json:"start"`
	End         int       `json:"end"`
	ValueLength int       `json:"value_length"`
	Original    string    `json:"original"`
	StandIn     string    `json:"stand_in"`
	StorageMode string    `json:"storage_mode"`
	DetectedAt  time.Time `json:"detected_at"`
}

// CredentialMaskAuditRecord is one replacement as the write path knows it. The
// values are the real credential and the stand-in that left the machine, in the
// clear; UpsertCredentialMaskAudits encrypts them.
type CredentialMaskAuditRecord struct {
	TraceID    string
	EventIndex int
	Rule       string
	Vendor     string
	Kind       string
	Confidence string
	Score      int
	Reasons    string
	Start      int
	End        int
	Original   string
	StandIn    string
	DetectedAt time.Time
}

// CredentialMaskAuditFilter selects audit rows. The monitor page and the MCP
// tool both read through it, so it is the one shape of query the table serves.
type CredentialMaskAuditFilter struct {
	TraceID  string
	Rule     string
	Since    time.Time
	Until    time.Time
	Page     int
	PageSize int
}

type CredentialMaskAuditPage struct {
	Items      []CredentialMaskAudit `json:"items"`
	Total      int                   `json:"total"`
	Page       int                   `json:"page"`
	PageSize   int                   `json:"page_size"`
	TotalPages int                   `json:"total_pages"`
}

// UpsertCredentialMaskAudits stores the replacements of one trace and raises the
// event-feed notification for the rules that fired.
//
// It is an upsert rather than an append because the same cassette can be indexed
// more than once - the write path indexes it, and the background sync re-indexes
// it whenever its mtime or size changes - and a second pass must land on the same
// rows. A record with no original value is skipped: that is the shape the sync
// path sees, because the prelude deliberately does not carry the pair, so only
// the request that actually masked something can write an audit row.
func (s *Store) UpsertCredentialMaskAudits(records []CredentialMaskAuditRecord) error {
	if s == nil || len(records) == 0 {
		return nil
	}
	prepared := make([]CredentialMaskAuditRecord, 0, len(records))
	for _, record := range records {
		record.TraceID = strings.TrimSpace(record.TraceID)
		if record.Original == "" || record.TraceID == "" {
			continue
		}
		if record.DetectedAt.IsZero() {
			record.DetectedAt = time.Now().UTC()
		}
		prepared = append(prepared, record)
	}
	if len(prepared) == 0 {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	notified := make(map[string]CredentialMaskAuditRecord, len(prepared))
	savedEvents := make([]SystemEvent, 0, len(prepared))
	for _, record := range prepared {
		original, err := s.encryptMaskAuditValue(record.Original)
		if err != nil {
			return fmt.Errorf("encrypt masked credential: %w", err)
		}
		standIn, err := s.encryptMaskAuditValue(record.StandIn)
		if err != nil {
			return fmt.Errorf("encrypt credential stand-in: %w", err)
		}
		mode := s.SecretStorageMode()
		// Through execTx rather than tx.Exec: the rebinding that turns "?" into
		// "$n" for lib/pq lives on the store, and a bare *sql.Tx from Begin does
		// not carry it, so a "?" statement run directly on the transaction reaches
		// Postgres as-is and fails to parse.
		if _, err := s.execTx(tx, `
			INSERT INTO credential_mask_audits (
				id, trace_id, event_index, rule, vendor, kind, confidence, score, reasons,
				start_offset, end_offset, value_length, original_value, stand_in_value,
				storage_mode, detected_at, created_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT (trace_id, event_index) DO UPDATE SET
				rule = excluded.rule,
				vendor = excluded.vendor,
				kind = excluded.kind,
				confidence = excluded.confidence,
				score = excluded.score,
				reasons = excluded.reasons,
				start_offset = excluded.start_offset,
				end_offset = excluded.end_offset,
				value_length = excluded.value_length,
				original_value = excluded.original_value,
				stand_in_value = excluded.stand_in_value,
				storage_mode = excluded.storage_mode,
				detected_at = excluded.detected_at
		`,
			uuid.NewString(),
			record.TraceID,
			record.EventIndex,
			record.Rule,
			record.Vendor,
			record.Kind,
			record.Confidence,
			record.Score,
			record.Reasons,
			record.Start,
			record.End,
			len(record.Original),
			original,
			standIn,
			mode,
			record.DetectedAt.UTC(),
			time.Now().UTC(),
		); err != nil {
			return fmt.Errorf("store credential mask audit: %w", err)
		}
		// One event per rule, not per value: the values are in the table above,
		// and a fingerprint that carried the trace would add a system_events row
		// for every trace that ever contained a credential.
		notified[record.Rule] = record
	}
	// The notification is written in the same transaction as the rows it
	// announces. If the event insert fails, the audit rows roll back with it and
	// the caller can retry the batch; committing the rows first is what allowed a
	// transient system_events failure to leave a trace that shows a masking in its
	// audit tab while the event feed knows nothing about it.
	for rule, record := range notified {
		event, err := s.persistSystemEvent(systemEventForCredentialMasked(rule, record), tx)
		if err != nil {
			return fmt.Errorf("store credential mask event: %w", err)
		}
		savedEvents = append(savedEvents, event)
	}
	if err := tx.Commit(); err != nil {
		return err
	}

	// Subscribers are told only after the commit: a refetch they trigger has to
	// see both the event and the audit row it points at.
	for _, event := range savedEvents {
		s.notifySystemEventChanged(event)
	}
	return nil
}

// encryptMaskAuditValue seals one value with the local secret key, or returns it
// unchanged when no key is configured. SecretStorageMode reports which happened,
// and every row stores that mode so a reader never has to guess.
func (s *Store) encryptMaskAuditValue(value string) (string, error) {
	if value == "" || s == nil || s.secrets == nil || s.secrets.aead == nil {
		return value, nil
	}
	sealed, err := s.secrets.encryptSecretBytes([]byte(value))
	if err != nil {
		return "", err
	}
	return string(sealed), nil
}

func (s *Store) decryptMaskAuditValue(value string) (string, error) {
	if value == "" || s == nil || s.secrets == nil || s.secrets.aead == nil {
		return value, nil
	}
	plain, err := s.secrets.decryptSecretBytes([]byte(value))
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

// ListCredentialMaskAudits returns the masked credentials matching the filter,
// newest first, with both values decrypted. A row whose original cannot be
// decrypted - the local key was rotated or lost - keeps its metadata and reports
// the original as empty rather than failing the whole page.
func (s *Store) ListCredentialMaskAudits(filter CredentialMaskAuditFilter) (CredentialMaskAuditPage, error) {
	page, pageSize := normalizePage(filter.Page, filter.PageSize)

	where := []string{"1 = 1"}
	args := []any{}
	if traceID := strings.TrimSpace(filter.TraceID); traceID != "" {
		where = append(where, "trace_id = ?")
		args = append(args, traceID)
	}
	if rule := strings.TrimSpace(filter.Rule); rule != "" {
		where = append(where, "rule = ?")
		args = append(args, rule)
	}
	if !filter.Since.IsZero() {
		where = append(where, "detected_at >= ?")
		args = append(args, filter.Since.UTC())
	}
	if !filter.Until.IsZero() {
		where = append(where, "detected_at <= ?")
		args = append(args, filter.Until.UTC())
	}
	whereSQL := strings.Join(where, " AND ")

	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM credential_mask_audits WHERE `+whereSQL, args...).Scan(&total); err != nil {
		return CredentialMaskAuditPage{}, err
	}

	queryArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	rows, err := s.db.Query(`
		SELECT id, trace_id, event_index, rule, vendor, kind, confidence, score, reasons,
			start_offset, end_offset, value_length, original_value, stand_in_value,
			storage_mode, detected_at
		FROM credential_mask_audits
		WHERE `+whereSQL+`
		ORDER BY detected_at DESC, id DESC
		LIMIT ? OFFSET ?
	`, queryArgs...)
	if err != nil {
		return CredentialMaskAuditPage{}, err
	}
	defer rows.Close()

	items := make([]CredentialMaskAudit, 0, pageSize)
	for rows.Next() {
		item, err := s.scanCredentialMaskAudit(rows)
		if err != nil {
			return CredentialMaskAuditPage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return CredentialMaskAuditPage{}, err
	}
	return CredentialMaskAuditPage{
		Items:      items,
		Total:      total,
		Page:       page,
		PageSize:   pageSize,
		TotalPages: totalPages(total, pageSize),
	}, nil
}

// MaskAuditRuleCount is one (rule, kind) tally of a trace's masking events.
type MaskAuditRuleCount struct {
	Rule  string
	Kind  string
	Count int
}

// CountCredentialMaskAuditsByTraceIDs groups the masking events of the given
// traces by rule and kind.
//
// Like CountFindingsByTraceIDs this exists for the trace list: the tag needs one
// number per trace, and listing the rows would decrypt every original value to
// count it.
func (s *Store) CountCredentialMaskAuditsByTraceIDs(traceIDs []string) (map[string][]MaskAuditRuleCount, error) {
	out := make(map[string][]MaskAuditRuleCount, len(traceIDs))
	for _, chunk := range chunkStrings(dedupeNonEmptyStrings(traceIDs), storeSQLParamChunk) {
		rows, err := s.db.Query(`
			SELECT trace_id, rule, kind, COUNT(*)
			FROM credential_mask_audits
			WHERE trace_id IN (`+placeholders(len(chunk))+`)
			GROUP BY trace_id, rule, kind
		`, stringArgs(chunk)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var (
				traceID string
				count   MaskAuditRuleCount
			)
			if err := rows.Scan(&traceID, &count.Rule, &count.Kind, &count.Count); err != nil {
				rows.Close()
				return nil, err
			}
			out[traceID] = append(out[traceID], count)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// GetCredentialMaskAudit returns one row by id.
func (s *Store) GetCredentialMaskAudit(id string) (CredentialMaskAudit, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return CredentialMaskAudit{}, errors.New("get credential mask audit: id is required")
	}
	row := s.db.QueryRow(`
		SELECT id, trace_id, event_index, rule, vendor, kind, confidence, score, reasons,
			start_offset, end_offset, value_length, original_value, stand_in_value,
			storage_mode, detected_at
		FROM credential_mask_audits
		WHERE id = ?
	`, id)
	return s.scanCredentialMaskAudit(row)
}

type credentialMaskAuditScanner interface {
	Scan(dest ...any) error
}

func (s *Store) scanCredentialMaskAudit(row credentialMaskAuditScanner) (CredentialMaskAudit, error) {
	var item CredentialMaskAudit
	var original, standIn string
	if err := row.Scan(
		&item.ID, &item.TraceID, &item.EventIndex, &item.Rule, &item.Vendor, &item.Kind,
		&item.Confidence, &item.Score, &item.Reasons, &item.Start, &item.End,
		&item.ValueLength, &original, &standIn, &item.StorageMode, &item.DetectedAt,
	); err != nil {
		return CredentialMaskAudit{}, err
	}
	decrypted, err := s.decryptMaskAuditValue(original)
	if err != nil {
		// A row whose key is gone is still worth listing: the rule, the trace and
		// the offsets are what an investigator triages on, and the cassette next to
		// it holds the bytes. Failing the page would hide the rest of the findings.
		decrypted = ""
	}
	item.Original = decrypted
	if stand, err := s.decryptMaskAuditValue(standIn); err == nil {
		item.StandIn = stand
	}
	return item, nil
}

// CredentialMaskEventSeed is the latest audit row of one rule, in the shape the
// event feed needs. A reconciliation pass builds it from the table rather than
// from a live request, so it carries the stored value length and never the
// original value itself.
type CredentialMaskEventSeed struct {
	Rule        string
	Vendor      string
	Kind        string
	Confidence  string
	Score       int
	ValueLength int
	Offset      int
	TraceID     string
	DetectedAt  time.Time
}

// CredentialMaskEventSeeds returns one seed per rule that has been rewritten but
// has no event yet. The distinct-rule query is an index-only scan on
// credentialmaskaudit_rule_detected_at, existing fingerprints are read once, and
// only a rule that needs healing pays for the latest-row query, so a
// reconciliation pass is bounded by the rule table rather than by the traffic.
func (s *Store) CredentialMaskEventSeeds() ([]CredentialMaskEventSeed, error) {
	existing, err := s.credentialMaskEventFingerprints()
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(`
		SELECT DISTINCT rule
		FROM credential_mask_audits
		WHERE rule <> ''
		ORDER BY rule
	`)
	if err != nil {
		return nil, err
	}
	var rules []string
	for rows.Next() {
		var rule string
		if err := rows.Scan(&rule); err != nil {
			rows.Close()
			return nil, err
		}
		if !existing["secretmask:rule:"+normalizeEventFingerprintPart(rule)] {
			rules = append(rules, rule)
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}

	seeds := make([]CredentialMaskEventSeed, 0, len(rules))
	for _, rule := range rules {
		var seed CredentialMaskEventSeed
		seed.Rule = rule
		err := s.db.QueryRow(`
			SELECT vendor, kind, confidence, score, value_length, start_offset, trace_id, detected_at
			FROM credential_mask_audits
			WHERE rule = ?
			ORDER BY detected_at DESC, id DESC
			LIMIT 1
		`, rule).Scan(&seed.Vendor, &seed.Kind, &seed.Confidence, &seed.Score,
			&seed.ValueLength, &seed.Offset, &seed.TraceID, &seed.DetectedAt)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		seeds = append(seeds, seed)
	}
	return seeds, nil
}

func (s *Store) credentialMaskEventFingerprints() (map[string]bool, error) {
	rows, err := s.db.Query(`
		SELECT fingerprint
		FROM system_events
		WHERE source = 'secretmask' AND category = 'credential_masked'
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	existing := map[string]bool{}
	for rows.Next() {
		var fingerprint string
		if err := rows.Scan(&fingerprint); err != nil {
			return nil, err
		}
		existing[fingerprint] = true
	}
	return existing, rows.Err()
}

// CredentialMaskTraceMatches resolves a trace-id search to the rules that trace
// carried, one fingerprint per rule.
//
// The event table keeps one row per rule, so it cannot store a trace id per
// occurrence without duplicating what credential_mask_audits already holds.
// This read is the join instead: the audit table's ("trace_id", "event_index")
// index answers the prefix lookup, and the returned fingerprints are ORed into
// the event search. The map value is the full trace id the prefix matched, which
// the monitor returns as matched_trace_id so the event detail reads that
// trace's pair rather than the rule's newest one.
//
// Only UUID-shaped input is treated as a trace query. A free-text search for a
// rule name must not scan the audit table for every keystroke.
func (s *Store) CredentialMaskTraceMatches(query string) (map[string]string, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	if !looksLikeTraceIDQuery(query) {
		return nil, nil
	}
	rows, err := s.db.Query(`
		SELECT trace_id, rule
		FROM credential_mask_audits
		WHERE trace_id LIKE ? ESCAPE '\'
		ORDER BY CASE WHEN trace_id = ? THEN 0 ELSE 1 END, detected_at DESC, id DESC
		LIMIT 100
	`, escapeLike(query)+"%", query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	matches := map[string]string{}
	for rows.Next() {
		var traceID, rule string
		if err := rows.Scan(&traceID, &rule); err != nil {
			return nil, err
		}
		fingerprint := "secretmask:rule:" + normalizeEventFingerprintPart(rule)
		if _, exists := matches[fingerprint]; !exists {
			matches[fingerprint] = traceID
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, nil
	}
	return matches, nil
}

// looksLikeTraceIDQuery accepts the UUID and UUID-prefix shape a reader pastes
// into the event search. The length floor keeps a short hex rule name from
// turning every keystroke into an audit-table prefix scan.
func looksLikeTraceIDQuery(value string) bool {
	if len(value) < 8 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || r == '-' {
			continue
		}
		return false
	}
	return true
}

// ReopenIgnoredCredentialMaskEvents returns every ignored credential_masked
// event to unread. It is the one-time repair for deployments that ignored the
// rule under the old permanent-mute semantics; after this build a new occurrence
// reopens the event by itself, so an operator can still dismiss one occurrence
// without silencing the next credential.
func (s *Store) ReopenIgnoredCredentialMaskEvents() (int64, error) {
	result, err := s.db.Exec(`
		UPDATE system_events
		SET status = 'unread', read_at = NULL, resolved_at = NULL, updated_at = ?
		WHERE source = 'secretmask' AND category = 'credential_masked' AND status = 'ignored'
	`, time.Now().UTC())
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	if count > 0 {
		s.notifySystemEventChanged(SystemEvent{Status: SystemEventStatusUnread})
	}
	return count, nil
}

// ReconcileCredentialMaskEvents recreates a credential_masked notification for
// every rule that has an audit row but no event. It inserts missing rows only,
// so running it on a timer is idempotent: an existing event keeps its occurrence
// count, its status and its latest trace pointer.
//
// This is the recovery path for the deploy that wrote the cassette and the audit
// table before this build - or for a process that died between an audit commit
// and its notification. The live write path already commits both together; this
// pass is what makes a historical gap heal by itself.
func (s *Store) ReconcileCredentialMaskEvents() (int, error) {
	seeds, err := s.CredentialMaskEventSeeds()
	if err != nil {
		return 0, err
	}
	inserted := 0
	for _, seed := range seeds {
		_, added, err := s.EnsureSystemEvent(systemEventForCredentialMaskSeed(seed))
		if err != nil {
			return inserted, err
		}
		if added {
			inserted++
		}
	}
	return inserted, nil
}

// systemEventForCredentialMasked builds the event-feed entry for one rule that
// rewrote a credential in this request.
//
// The event deliberately does not repeat the values: the feed is served to every
// monitor session and kept as a notification row, so the pair belongs in the
// audit table and this row only points at it. The trace id carries the most
// recent occurrence so the UI can link straight to the recording.
func systemEventForCredentialMasked(rule string, record CredentialMaskAuditRecord) SystemEvent {
	return systemEventForCredentialMask(rule, record.Vendor, record.Kind, record.Confidence,
		record.Score, record.TraceID, record.Start, len(record.Original))
}

func systemEventForCredentialMaskSeed(seed CredentialMaskEventSeed) SystemEvent {
	event := systemEventForCredentialMask(seed.Rule, seed.Vendor, seed.Kind, seed.Confidence,
		seed.Score, seed.TraceID, seed.Offset, seed.ValueLength)
	// A recovered event keeps the audit row's instant. It is not new information
	// and must not jump to the top of a recency-ordered feed as if it were; a
	// reader looking at a longer window still sees it because the page defaults to
	// the all-time window.
	if !seed.DetectedAt.IsZero() {
		event.FirstSeenAt = seed.DetectedAt
		event.LastSeenAt = seed.DetectedAt
		event.CreatedAt = seed.DetectedAt
	}
	return event
}

func systemEventForCredentialMask(rule, vendor, kind, confidence string, score int, traceID string, offset, valueLength int) SystemEvent {
	name := strings.TrimSpace(rule)
	if name == "" {
		name = "unknown"
	}
	return SystemEvent{
		Fingerprint: "secretmask:rule:" + normalizeEventFingerprintPart(name),
		Source:      "secretmask",
		Category:    "credential_masked",
		Severity:    "warning",
		Title:       "Sensitive credential replaced before leaving the machine",
		Message:     fmt.Sprintf("rule %s replaced a %s credential; the original and the stand-in are in the credential mask audit", name, vendor),
		TraceID:     traceID,
		Model:       "",
		DetailsJSON: mustMarshalSystemEventDetails(map[string]any{
			"rule":       name,
			"vendor":     vendor,
			"kind":       kind,
			"confidence": confidence,
			"score":      score,
			"length":     valueLength,
			"trace_id":   traceID,
			"offset":     offset,
			"audit":      "credential_mask_audits",
		}),
	}
}
