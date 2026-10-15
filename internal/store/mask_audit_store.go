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
	if err := tx.Commit(); err != nil {
		return err
	}

	for rule, record := range notified {
		if _, err := s.UpsertSystemEvent(systemEventForCredentialMasked(rule, record)); err != nil {
			return err
		}
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

// systemEventForCredentialMasked builds the event-feed entry for one rule that
// rewrote a credential in this request.
//
// The event deliberately does not repeat the values: the feed is served to every
// monitor session and kept as a notification row, so the pair belongs in the
// audit table and this row only points at it. "sample" carries the trace of the
// most recent occurrence so the UI can link straight to the recording.
func systemEventForCredentialMasked(rule string, record CredentialMaskAuditRecord) SystemEvent {
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
		Message:     fmt.Sprintf("rule %s replaced a %s credential; the original and the stand-in are in the credential mask audit", name, record.Vendor),
		TraceID:     record.TraceID,
		Model:       "",
		DetailsJSON: mustMarshalSystemEventDetails(map[string]any{
			"rule":       name,
			"vendor":     record.Vendor,
			"kind":       record.Kind,
			"confidence": record.Confidence,
			"score":      record.Score,
			"length":     len(record.Original),
			"trace_id":   record.TraceID,
			"offset":     record.Start,
			"audit":      "credential_mask_audits",
		}),
	}
}
