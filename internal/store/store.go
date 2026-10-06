package store

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/kingfs/Trajecta/ent/dao"
	"github.com/kingfs/Trajecta/ent/dao/channelconfig"
	"github.com/kingfs/Trajecta/ent/dao/dataset"
	"github.com/kingfs/Trajecta/ent/dao/datasetexample"
	"github.com/kingfs/Trajecta/ent/dao/evalrun"
	"github.com/kingfs/Trajecta/ent/dao/experimentrun"
	"github.com/kingfs/Trajecta/ent/dao/modelcatalog"
	"github.com/kingfs/Trajecta/ent/dao/predicate"
	"github.com/kingfs/Trajecta/ent/dao/score"
	"github.com/kingfs/Trajecta/ent/dao/tracelog"
	"github.com/kingfs/Trajecta/ent/dao/upstreammodel"
	"github.com/kingfs/Trajecta/ent/dao/upstreamtarget"
	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/pkg/llm"
	"github.com/kingfs/Trajecta/pkg/observe"
	"github.com/kingfs/Trajecta/pkg/recordfile"
	_ "github.com/lib/pq"
	_ "modernc.org/sqlite"
)

type LogEntry struct {
	ID              string
	Header          recordfile.RecordHeader
	LogPath         string
	SessionID       string
	SessionSource   string
	WindowID        string
	ClientRequestID string
	Observation     ObservationMetadata
}

type Stats struct {
	TotalRequest   int
	AvgTTFT        int
	TotalTokens    int
	SuccessRequest int
	FailedRequest  int
	SuccessRate    float64
}

const overviewMetricBucketSize = time.Hour

type Store struct {
	db                    *rebindingDB
	client                *dao.Client
	outputDir             string
	dbPath                string
	driver                string
	secrets               *secretBox
	useSessionSummaryRead bool
	shared                *storeShared
	// closed makes Close idempotent, so an explicit close next to a deferred one does
	// not flush or close the handle twice.
	closed atomic.Bool
}

// storeShared keeps the state that every Store view of the same database must
// share. ConfigurationTransaction builds a view whose db/client are rebound to
// one transaction; if the writer locks or the live system-event subscribers were
// copied instead of shared, such a view would silently diverge from the store it
// was derived from.
type storeShared struct {
	configMu   sync.Mutex
	upstreamMu sync.Mutex
	syncMu     sync.Mutex
	eventMu    sync.Mutex
	eventSeq   uint64
	eventSubs  map[chan SystemEventNotification]struct{}

	// claimMu serializes the SQLite task claims. Postgres takes its rows with
	// FOR UPDATE SKIP LOCKED, so two claimers cannot overlap there; SQLite has no
	// row locks, and this lock keeps two goroutines in one process from reading
	// the same queued rows before either UPDATE lands.
	claimMu sync.Mutex

	// derivedMu guards the deferred derived-table queues; derivedFlushMu
	// serializes the flushes themselves. See markDerivedRefreshForPath.
	derivedMu       sync.Mutex
	derivedFlushMu  sync.Mutex
	derivedPaths    map[string]struct{}
	derivedTraces   map[string]struct{}
	derivedSessions map[string]struct{}
	// derivedLimit overrides derivedQueueLimit when positive; tests use it to
	// observe the bound without writing the default number of recordings.
	derivedLimit int

	// statsMu guards the one-entry request-statistics cache. The monitor list
	// page asks for the same aggregate on every page it renders, and a log write
	// clears the entry, so a cached value is either current or at most
	// statsCacheTTL old. See Stats.
	statsMu    sync.Mutex
	statsKey   string
	statsValue Stats
	statsAt    time.Time
}

// statsCacheTTL bounds how long Stats may serve a cached aggregate. Log writes
// clear the entry, so the bound only matters for writes the store does not see.
const statsCacheTTL = 15 * time.Second

type DatabaseOptions struct {
	AutoMigrate           bool
	UseSessionSummaryRead bool
}

type rebindingDB struct {
	*sql.DB
	tx     *sql.Tx
	driver string
}

// ErrNestedTransaction reports an attempt to open a second, independent
// transaction from a store that already runs inside one. database/sql has no
// nested transactions, and the promoted *sql.DB.Begin/BeginTx would silently
// borrow a pooled connection and escape the outer transaction.
var ErrNestedTransaction = errors.New("store: nested transaction on a transaction-scoped store")

func (db *rebindingDB) Begin() (*sql.Tx, error) {
	return db.BeginTx(context.Background(), nil)
}

func (db *rebindingDB) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	if db == nil || db.DB == nil {
		return nil, errors.New("store: database is not configured")
	}
	if db.tx != nil {
		return nil, ErrNestedTransaction
	}
	return db.DB.BeginTx(ctx, opts)
}

func (db *rebindingDB) Exec(query string, args ...any) (sql.Result, error) {
	return db.ExecContext(context.Background(), query, args...)
}

func (db *rebindingDB) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if db.tx != nil {
		return db.tx.ExecContext(ctx, db.rebind(query), args...)
	}
	return db.DB.ExecContext(ctx, db.rebind(query), args...)
}

func (db *rebindingDB) Query(query string, args ...any) (*sql.Rows, error) {
	return db.QueryContext(context.Background(), query, args...)
}

func (db *rebindingDB) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if db.tx != nil {
		return db.tx.QueryContext(ctx, db.rebind(query), args...)
	}
	return db.DB.QueryContext(ctx, db.rebind(query), args...)
}

func (db *rebindingDB) QueryRow(query string, args ...any) *sql.Row {
	return db.QueryRowContext(context.Background(), query, args...)
}

func (db *rebindingDB) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	if db.tx != nil {
		return db.tx.QueryRowContext(ctx, db.rebind(query), args...)
	}
	return db.DB.QueryRowContext(ctx, db.rebind(query), args...)
}

func (db *rebindingDB) rebind(query string) string {
	if db == nil || db.driver != "postgres" {
		return query
	}
	return rebindPostgresPlaceholders(query)
}

func rebindPostgresPlaceholders(query string) string {
	var b strings.Builder
	b.Grow(len(query) + 8)
	arg := 1
	inSingleQuote := false
	for i := 0; i < len(query); i++ {
		ch := query[i]
		if ch == '\'' {
			b.WriteByte(ch)
			if inSingleQuote && i+1 < len(query) && query[i+1] == '\'' {
				i++
				b.WriteByte(query[i])
				continue
			}
			inSingleQuote = !inSingleQuote
			continue
		}
		if ch == '?' && !inSingleQuote {
			b.WriteByte('$')
			fmt.Fprint(&b, arg)
			arg++
			continue
		}
		b.WriteByte(ch)
	}
	return b.String()
}

const (
	localSecretKeyFile = "trace_index.secret"
	secretEnvelopeV1   = "tlsec:v1:"
)

type secretBox struct {
	aead        cipher.AEAD
	mode        string
	keyPath     string
	fingerprint string
}

type SecretStatus struct {
	Mode        string `json:"mode"`
	KeyPath     string `json:"key_path"`
	Exists      bool   `json:"exists"`
	Readable    bool   `json:"readable"`
	Fingerprint string `json:"fingerprint,omitempty"`
	Error       string `json:"error,omitempty"`
}

type SecretRotationResult struct {
	Mode           string `json:"mode"`
	KeyPath        string `json:"key_path"`
	BackupPath     string `json:"backup_path,omitempty"`
	OldFingerprint string `json:"old_fingerprint"`
	NewFingerprint string `json:"new_fingerprint"`
	ChannelCount   int    `json:"channel_count"`
	APIKeyCount    int    `json:"api_key_count"`
	HeaderCount    int    `json:"header_count"`
}

type rotatedChannelSecret struct {
	id             string
	apiKey         []byte
	hasAPIKey      bool
	headersJSON    string
	secretKeyCount int
}

type ListPageResult struct {
	Items      []LogEntry
	Total      int
	Page       int
	PageSize   int
	TotalPages int
}

type ListFilter struct {
	Query             string
	Provider          string
	Model             string
	Endpoint          string
	SelectedUpstream  string
	Status            string
	ObservationStatus string
	MissingUsage      bool
	MinDurationMs     int64
	MaxDurationMs     int64
	MinTTFTMs         int64
	MaxTTFTMs         int64
	MinTokens         int
	MaxTokens         int
}

type GroupingInfo struct {
	SessionID       string
	SessionSource   string
	WindowID        string
	ClientRequestID string
}

type UpstreamTargetRecord struct {
	ID                string
	BaseURL           string
	ProviderPreset    string
	ProtocolFamily    string
	RoutingProfile    string
	Enabled           bool
	Priority          int
	Weight            float64
	CapacityHint      float64
	LastRefreshAt     time.Time
	LastRefreshStatus string
	LastRefreshError  string
}

type UpstreamModelRecord struct {
	UpstreamID string
	Model      string
	Source     string
	SeenAt     time.Time
}

type ModelAliasRecord struct {
	ID          string
	Alias       string
	TargetModel string
	ChannelID   string
	Enabled     bool
	Description string
	Source      string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

var ErrModelAliasConflict = errors.New("model alias conflict")

type ModelCatalogRecord struct {
	Model       string
	DisplayName string
	Family      string
	Vendor      string
	Description string
	TagsJSON    string
	FirstSeenAt time.Time
	LastSeenAt  time.Time
	LastUsedAt  time.Time
}

type UpstreamFailureRecord struct {
	TraceID    string
	Model      string
	Endpoint   string
	StatusCode int
	RecordedAt time.Time
	Reason     string
	ErrorText  string
}

type UpstreamDetail struct {
	Analytics      UpstreamAnalyticsRecord
	Traces         []LogEntry
	Models         []CountItem
	Endpoints      []CountItem
	FailureReasons []CountItem
	Timeline       []TimeCountItem
}

type CountItem struct {
	Label string
	Count int
}

type RoutingFailureRecord struct {
	TraceID    string
	Model      string
	Endpoint   string
	RecordedAt time.Time
	Reason     string
	ErrorText  string
	StatusCode int
}

type AnalysisRunRecord struct {
	ID              int64
	TraceID         string
	SessionID       string
	Kind            string
	Analyzer        string
	AnalyzerVersion string
	Model           string
	InputRef        string
	OutputJSON      string
	Status          string
	CreatedAt       time.Time
}

type TimeCountItem struct {
	Time  time.Time
	Count int
}

type UsageSummaryRecord struct {
	RequestCount     int
	SuccessRequest   int
	FailedRequest    int
	SuccessRate      float64
	MissingUsage     int
	TotalTokens      int
	PromptTokens     int
	CompletionTokens int
	CachedTokens     int
	AvgTTFT          int
	AvgDurationMs    int64
	LastSeen         time.Time
}

type UsageTrendRecord struct {
	Time          time.Time
	RequestCount  int
	FailedRequest int
	MissingUsage  int
	TotalTokens   int
	ModelCount    int
}

type DatasetRecord struct {
	ID           string
	Name         string
	Description  string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	ExampleCount int
}

type DatasetExampleRecord struct {
	DatasetID  string
	TraceID    string
	Position   int
	AddedAt    time.Time
	SourceType string
	SourceID   string
	Note       string
	Trace      LogEntry
}

type EvalRunRecord struct {
	ID           string
	DatasetID    string
	SourceType   string
	SourceID     string
	EvaluatorSet string
	CreatedAt    time.Time
	CompletedAt  time.Time
	TraceCount   int
	ScoreCount   int
	PassCount    int
	FailCount    int
}

type ScoreRecord struct {
	ID           string
	TraceID      string
	SessionID    string
	DatasetID    string
	EvalRunID    string
	EvaluatorKey string
	Value        float64
	Status       string
	Label        string
	Explanation  string
	CreatedAt    time.Time
}

type ExperimentRunRecord struct {
	ID                  string
	Name                string
	Description         string
	BaselineEvalRunID   string
	CandidateEvalRunID  string
	CreatedAt           time.Time
	BaselineScoreCount  int
	CandidateScoreCount int
	BaselinePassRate    float64
	CandidatePassRate   float64
	PassRateDelta       float64
	MatchedScoreCount   int
	ImprovementCount    int
	RegressionCount     int
}

type ScoreFilter struct {
	TraceID   string
	SessionID string
	DatasetID string
	EvalRunID string
}

func (s *Store) ListUpstreamTargets() ([]UpstreamTargetRecord, error) {
	rows, err := s.client.UpstreamTarget.Query().
		Order(upstreamtarget.ByPriority(entsql.OrderDesc()), upstreamtarget.ByID()).
		All(context.Background())
	if err != nil {
		return nil, err
	}

	out := make([]UpstreamTargetRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, upstreamTargetRecordFromEnt(row))
	}
	return out, nil
}

func (s *Store) ListUpstreamModels() ([]UpstreamModelRecord, error) {
	rows, err := s.client.UpstreamModel.Query().
		Order(upstreammodel.ByUpstreamID(), upstreammodel.ByModel()).
		All(context.Background())
	if err != nil {
		return nil, err
	}

	out := make([]UpstreamModelRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, upstreamModelRecordFromEnt(row))
	}
	return out, nil
}

func (s *Store) ListModelAliases(alias string, enabledOnly bool) ([]ModelAliasRecord, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("store is not initialized")
	}
	var args []any
	where := "1=1"
	if alias = strings.ToLower(strings.TrimSpace(alias)); alias != "" {
		where += " AND alias = ?"
		args = append(args, alias)
	}
	if enabledOnly {
		where += " AND enabled = ?"
		args = append(args, true)
	}
	rows, err := s.db.Query(`SELECT id, alias, target_model, channel_id, enabled, description, source, created_at, updated_at
		FROM model_aliases WHERE `+where+` ORDER BY alias, channel_id, target_model, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ModelAliasRecord{}
	for rows.Next() {
		record, err := scanModelAlias(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, rows.Err()
}

func (s *Store) UpsertModelAlias(record ModelAliasRecord) (ModelAliasRecord, error) {
	if s == nil || s.db == nil {
		return ModelAliasRecord{}, errors.New("store is not initialized")
	}
	record = normalizeModelAliasRecord(record)
	if err := s.ValidateModelAlias(record); err != nil {
		return ModelAliasRecord{}, err
	}
	if record.ID == "" {
		record.ID = stableModelAliasID(record)
	}
	now := time.Now().UTC()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = now
	}
	if record.Source == "" {
		record.Source = "manual"
	}
	_, err := s.db.Exec(`INSERT INTO model_aliases (id, alias, target_model, channel_id, enabled, description, source, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			alias = excluded.alias,
			target_model = excluded.target_model,
			channel_id = excluded.channel_id,
			enabled = excluded.enabled,
			description = excluded.description,
			source = excluded.source,
			updated_at = excluded.updated_at`,
		record.ID,
		record.Alias,
		record.TargetModel,
		record.ChannelID,
		record.Enabled,
		record.Description,
		record.Source,
		record.CreatedAt,
		record.UpdatedAt,
	)
	if err != nil {
		return ModelAliasRecord{}, err
	}
	return s.GetModelAlias(record.ID)
}

func (s *Store) ValidateModelAlias(record ModelAliasRecord) error {
	if s == nil || s.db == nil {
		return errors.New("store is not initialized")
	}
	record = normalizeModelAliasRecord(record)
	if record.Alias == "" {
		return errors.New("alias is required")
	}
	if record.TargetModel == "" {
		return errors.New("target model is required")
	}
	if record.Alias == record.TargetModel {
		return errors.New("model alias cannot target itself")
	}
	if record.ID == "" {
		record.ID = stableModelAliasID(record)
	}
	if err := s.ensureNoDirectAliasCycle(record); err != nil {
		return err
	}
	if record.Enabled {
		if err := s.ensureNoDuplicateActiveModelAlias(record); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ensureNoDirectAliasCycle(record ModelAliasRecord) error {
	var existingID string
	err := s.db.QueryRow(`SELECT id FROM model_aliases
		WHERE alias = ? AND target_model = ? AND id <> ? LIMIT 1`, record.TargetModel, record.Alias, record.ID).Scan(&existingID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("direct model alias cycle with %s", existingID)
}

func (s *Store) ensureNoDuplicateActiveModelAlias(record ModelAliasRecord) error {
	var existingID string
	err := s.db.QueryRow(`SELECT id FROM model_aliases
		WHERE alias = ? AND target_model = ? AND channel_id = ? AND enabled = ? AND id <> ? LIMIT 1`, record.Alias, record.TargetModel, record.ChannelID, true, record.ID).Scan(&existingID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf("%w: active alias %q already targets %q on channel %q", ErrModelAliasConflict, record.Alias, record.TargetModel, record.ChannelID)
}

func (s *Store) GetModelAlias(id string) (ModelAliasRecord, error) {
	if s == nil || s.db == nil {
		return ModelAliasRecord{}, errors.New("store is not initialized")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return ModelAliasRecord{}, errors.New("model alias id is required")
	}
	row := s.db.QueryRow(`SELECT id, alias, target_model, channel_id, enabled, description, source, created_at, updated_at
		FROM model_aliases WHERE id = ?`, id)
	return scanModelAlias(row)
}

func (s *Store) SetModelAliasEnabled(id string, enabled bool) error {
	if s == nil || s.db == nil {
		return errors.New("store is not initialized")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("model alias id is required")
	}
	result, err := s.db.Exec(`UPDATE model_aliases SET enabled = ?, updated_at = ? WHERE id = ?`, enabled, time.Now().UTC(), id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) DeleteModelAlias(id string) error {
	if s == nil || s.db == nil {
		return errors.New("store is not initialized")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("model alias id is required")
	}
	result, err := s.db.Exec(`DELETE FROM model_aliases WHERE id = ?`, id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

type modelAliasScanner interface {
	Scan(dest ...any) error
}

func scanModelAlias(scanner modelAliasScanner) (ModelAliasRecord, error) {
	var record ModelAliasRecord
	if err := scanner.Scan(
		&record.ID,
		&record.Alias,
		&record.TargetModel,
		&record.ChannelID,
		&record.Enabled,
		&record.Description,
		&record.Source,
		&record.CreatedAt,
		&record.UpdatedAt,
	); err != nil {
		return ModelAliasRecord{}, err
	}
	return record, nil
}

func normalizeModelAliasRecord(record ModelAliasRecord) ModelAliasRecord {
	record.ID = strings.TrimSpace(record.ID)
	record.Alias = strings.ToLower(strings.TrimSpace(record.Alias))
	record.TargetModel = strings.ToLower(strings.TrimSpace(record.TargetModel))
	record.ChannelID = strings.TrimSpace(record.ChannelID)
	record.Description = strings.TrimSpace(record.Description)
	record.Source = strings.TrimSpace(record.Source)
	return record
}

func stableModelAliasID(record ModelAliasRecord) string {
	key := record.Alias + "\x00" + record.ChannelID + "\x00" + record.TargetModel
	sum := sha256.Sum256([]byte(key))
	return "mal_" + hex.EncodeToString(sum[:8])
}

func boolToCapabilityInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (s *Store) UpsertModelCatalog(record ModelCatalogRecord) error {
	model := strings.ToLower(strings.TrimSpace(record.Model))
	if model == "" {
		return errors.New("model is required")
	}
	now := time.Now().UTC()
	if record.FirstSeenAt.IsZero() {
		record.FirstSeenAt = now
	}
	if record.LastSeenAt.IsZero() {
		record.LastSeenAt = now
	}
	create := s.client.ModelCatalog.Create().
		SetID(model).
		SetDisplayName(strings.TrimSpace(record.DisplayName)).
		SetFamily(strings.TrimSpace(record.Family)).
		SetVendor(strings.TrimSpace(record.Vendor)).
		SetDescription(strings.TrimSpace(record.Description)).
		SetTagsJSON(defaultJSON(record.TagsJSON, "[]")).
		SetFirstSeenAt(record.FirstSeenAt.UTC()).
		SetLastSeenAt(record.LastSeenAt.UTC())
	if !record.LastUsedAt.IsZero() {
		create.SetLastUsedAt(record.LastUsedAt.UTC())
	}
	return create.
		OnConflictColumns(modelcatalog.FieldID).
		UpdateNewValues().
		Exec(context.Background())
}

func (s *Store) GetModelCatalog(model string) (ModelCatalogRecord, error) {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return ModelCatalogRecord{}, errors.New("model is required")
	}
	row, err := s.client.ModelCatalog.Get(context.Background(), model)
	if err != nil {
		if dao.IsNotFound(err) {
			return ModelCatalogRecord{}, sql.ErrNoRows
		}
		return ModelCatalogRecord{}, err
	}
	return modelCatalogRecordFromEnt(row), nil
}

func (s *Store) GetUpstreamDetail(upstreamID string, since time.Time, modelFilter string, traceLimit int, bucketSize time.Duration, bucketCount int) (UpstreamDetail, error) {
	if traceLimit <= 0 {
		traceLimit = 50
	}
	if bucketSize <= 0 {
		bucketSize = 2 * time.Hour
	}
	if bucketCount <= 0 {
		bucketCount = 12
	}
	analytics, err := s.ListUpstreamAnalytics(8, 5, since, modelFilter)
	if err != nil {
		return UpstreamDetail{}, err
	}
	var detail UpstreamDetail
	for _, item := range analytics {
		if item.UpstreamID == upstreamID {
			detail.Analytics = item
			break
		}
	}
	if detail.Analytics.UpstreamID == "" {
		return UpstreamDetail{}, sql.ErrNoRows
	}

	whereSQL, whereArgs := buildUpstreamAnalyticsWhere(since, modelFilter)
	queryArgs := append([]any{upstreamID}, whereArgs...)
	queryArgs = append(queryArgs, traceLimit)
	rows, err := s.db.Query(`
		SELECT
			trace_id, path, version, request_id, recorded_at, model, provider, operation, endpoint, url, method, status_code,
			duration_ms, ttft_ms, client_ip, content_length, error_text,
			prompt_tokens, completion_tokens, total_tokens, cached_tokens,
			req_header_len, req_body_len, res_header_len, res_body_len, is_stream,
			session_id, session_source, window_id, client_request_id,
			request_audit_id, response_id,
			exchange_id, exchange_kind, exchange_role, parent_exchange_id, sequence_index,
			selected_upstream_id, selected_upstream_base_url, selected_upstream_provider_preset,
			routing_policy, routing_score, routing_candidate_count, routing_failure_reason
		FROM logs
		WHERE selected_upstream_id = ?`+whereSQL+`
		ORDER BY recorded_at DESC, trace_id DESC
		LIMIT ?
	`, queryArgs...)
	if err != nil {
		return UpstreamDetail{}, err
	}
	defer rows.Close()

	modelCounts := map[string]int{}
	endpointCounts := map[string]int{}
	for rows.Next() {
		entry, err := scanEntry(rows)
		if err != nil {
			return UpstreamDetail{}, err
		}
		detail.Traces = append(detail.Traces, entry)
		if entry.Header.Meta.Model != "" {
			modelCounts[entry.Header.Meta.Model]++
		}
		if entry.Header.Meta.Endpoint != "" {
			endpointCounts[entry.Header.Meta.Endpoint]++
		}
	}
	if err := rows.Err(); err != nil {
		return UpstreamDetail{}, err
	}
	detail.Models = sortedCountItems(modelCounts)
	detail.Endpoints = sortedCountItems(endpointCounts)
	detail.FailureReasons, err = s.upstreamFailureReasons(upstreamID, 5, since, modelFilter)
	if err != nil {
		return UpstreamDetail{}, err
	}

	timelineArgs := append([]any{upstreamID}, whereArgs...)
	var latestRecordedAt any
	err = s.db.QueryRow(`
			SELECT MAX(recorded_at)
			FROM logs
			WHERE selected_upstream_id = ? AND status_code >= 400`+whereSQL,
		timelineArgs...,
	).Scan(&latestRecordedAt)
	if err != nil {
		return UpstreamDetail{}, err
	}
	referenceTime := time.Now().UTC()
	if latestTime, err := timeParseNullableValue(latestRecordedAt); err != nil {
		return UpstreamDetail{}, err
	} else if !latestTime.IsZero() {
		referenceTime = latestTime
	}
	bucketStart := referenceTime.Truncate(bucketSize).Add(-time.Duration(bucketCount-1) * bucketSize)
	buckets := make(map[time.Time]int, bucketCount)
	failureTimelineArgs := append([]any{upstreamID}, whereArgs...)
	timelineRows, err := s.db.Query(`
		SELECT recorded_at
		FROM logs
		WHERE selected_upstream_id = ? AND status_code >= 400`+whereSQL+`
		ORDER BY recorded_at ASC
	`, failureTimelineArgs...)
	if err != nil {
		return UpstreamDetail{}, err
	}
	defer timelineRows.Close()
	for timelineRows.Next() {
		var recordedAt string
		if err := timelineRows.Scan(&recordedAt); err != nil {
			return UpstreamDetail{}, err
		}
		recordedTime, err := timeParse(recordedAt)
		if err != nil {
			return UpstreamDetail{}, err
		}
		slot := recordedTime.UTC().Truncate(bucketSize)
		if slot.Before(bucketStart) {
			continue
		}
		if slot.After(referenceTime.Truncate(bucketSize)) {
			continue
		}
		if _, ok := buckets[slot]; ok {
			buckets[slot]++
			continue
		}
		buckets[slot] = 1
	}
	if err := timelineRows.Err(); err != nil {
		return UpstreamDetail{}, err
	}
	for index := 0; index < bucketCount; index++ {
		slot := bucketStart.Add(time.Duration(index) * bucketSize)
		detail.Timeline = append(detail.Timeline, TimeCountItem{
			Time:  slot,
			Count: buckets[slot],
		})
	}
	return detail, nil
}

func (s *Store) upstreamFailureReasons(upstreamID string, limit int, since time.Time, modelFilter string) ([]CountItem, error) {
	if limit <= 0 {
		limit = 5
	}
	whereSQL, whereArgs := buildUpstreamAnalyticsWhere(since, modelFilter)
	args := append([]any{upstreamID}, whereArgs...)
	rows, err := s.db.Query(`
		SELECT status_code, error_text
		FROM logs
		WHERE selected_upstream_id = ?
		  `+whereSQL+`
		  AND (status_code NOT BETWEEN 200 AND 299 OR error_text <> '')
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	counts := map[string]int{}
	for rows.Next() {
		var (
			statusCode int
			errorText  string
		)
		if err := rows.Scan(&statusCode, &errorText); err != nil {
			return nil, err
		}
		counts[classifyUpstreamFailure(statusCode, errorText)]++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	items := sortedCountItems(counts)
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

func classifyUpstreamFailure(statusCode int, errorText string) string {
	text := strings.ToLower(strings.TrimSpace(errorText))
	switch {
	case strings.Contains(text, "retry wait queue saturated") || strings.Contains(text, "retry_queue_saturated"):
		return "retry_queue_saturated"
	case statusCode == 408 || statusCode == 504 || strings.Contains(text, "timeout") || strings.Contains(text, "timed out") || strings.Contains(text, "deadline exceeded") || strings.Contains(text, "context deadline exceeded"):
		return "timeout"
	case statusCode == 429 || strings.Contains(text, "rate limit") || strings.Contains(text, "too many requests"):
		return "rate_limited"
	case statusCode == 401 || statusCode == 403 || strings.Contains(text, "unauthorized") || strings.Contains(text, "forbidden") || strings.Contains(text, "invalid api key") || strings.Contains(text, "authentication"):
		return "auth_denied"
	case statusCode == 503 || strings.Contains(text, "overloaded") || strings.Contains(text, "overload") || strings.Contains(text, "capacity") || strings.Contains(text, "unavailable"):
		return "upstream_overloaded"
	case statusCode >= 500:
		return "upstream_error"
	case statusCode >= 400:
		return "request_rejected"
	case text != "":
		return "transport_error"
	default:
		return "unknown_failure"
	}
}

func (s *Store) boolCountCaseSQL(column string) string {
	if s != nil && s.driver == "postgres" {
		return "CASE WHEN " + column + " THEN 1 ELSE 0 END"
	}
	return "CASE WHEN " + column + " = 1 THEN 1 ELSE 0 END"
}

func (s *Store) listLogModels(since time.Time) ([]string, error) {
	where := "model <> '' AND LOWER(model) <> 'list_models'"
	args := []any{}
	if !since.IsZero() {
		where += " AND recorded_at >= ?"
		args = append(args, since.UTC().Format(timeLayout))
	}
	rows, err := s.db.Query(`SELECT DISTINCT LOWER(model) FROM logs WHERE `+where+` ORDER BY LOWER(model)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var model string
		if err := rows.Scan(&model); err != nil {
			return nil, err
		}
		if model = strings.TrimSpace(model); model != "" {
			out = append(out, model)
		}
	}
	return out, rows.Err()
}

// usageSummaryAggregateColumns is the aggregate projection behind a usage
// summary. The single-key and the grouped form below share it so that one can
// stand in for the other without drifting.
const usageSummaryAggregateColumns = `
		COUNT(*) AS request_count,
		COALESCE(SUM(CASE WHEN status_code BETWEEN 200 AND 299 THEN 1 ELSE 0 END), 0) AS success_request,
		COALESCE(SUM(CASE WHEN status_code NOT BETWEEN 200 AND 299 THEN 1 ELSE 0 END), 0) AS failed_request,
		CASE WHEN COUNT(*) = 0 THEN 0 ELSE 100.0 * SUM(CASE WHEN status_code BETWEEN 200 AND 299 THEN 1 ELSE 0 END) / COUNT(*) END AS success_rate,
		COALESCE(SUM(CASE WHEN status_code BETWEEN 200 AND 299 AND total_tokens = 0 AND prompt_tokens = 0 AND completion_tokens = 0 THEN 1 ELSE 0 END), 0) AS missing_usage_request,
		COALESCE(SUM(total_tokens), 0) AS total_tokens,
		COALESCE(SUM(prompt_tokens), 0) AS prompt_tokens,
		COALESCE(SUM(completion_tokens), 0) AS completion_tokens,
		COALESCE(SUM(cached_tokens), 0) AS cached_tokens,
		COALESCE(AVG(ttft_ms), 0) AS avg_ttft,
		COALESCE(AVG(duration_ms), 0) AS avg_duration_ms,
		MAX(recorded_at) AS last_seen`

// usageSummaryRecordFromAggregate finishes one aggregate row. A group the query
// did not return and a single-key query that matched nothing both stay zero.
func usageSummaryRecordFromAggregate(record UsageSummaryRecord, successRate float64, avgTTFT float64, avgDuration float64, lastSeenValue any) (UsageSummaryRecord, error) {
	record.SuccessRate = successRate
	record.AvgTTFT = int(math.Round(avgTTFT))
	record.AvgDurationMs = int64(math.Round(avgDuration))
	if lastSeen, err := timeParseNullableValue(lastSeenValue); err != nil {
		return UsageSummaryRecord{}, err
	} else if !lastSeen.IsZero() {
		record.LastSeen = lastSeen
	}
	return record, nil
}

func (s *Store) usageSummary(baseWhere string, baseArgs []any, since time.Time) (UsageSummaryRecord, error) {
	where := strings.TrimSpace(baseWhere)
	if where == "" {
		where = "1=1"
	}
	args := append([]any(nil), baseArgs...)
	if !since.IsZero() {
		where += " AND recorded_at >= ?"
		args = append(args, since.UTC().Format(timeLayout))
	}
	var (
		record        UsageSummaryRecord
		successRate   float64
		avgTTFT       float64
		avgDuration   float64
		lastSeenValue any
	)
	if err := s.db.QueryRow(`
		SELECT`+usageSummaryAggregateColumns+`
		FROM logs
		WHERE `+where, args...).Scan(
		&record.RequestCount,
		&record.SuccessRequest,
		&record.FailedRequest,
		&successRate,
		&record.MissingUsage,
		&record.TotalTokens,
		&record.PromptTokens,
		&record.CompletionTokens,
		&record.CachedTokens,
		&avgTTFT,
		&avgDuration,
		&lastSeenValue,
	); err != nil {
		return UsageSummaryRecord{}, err
	}
	return usageSummaryRecordFromAggregate(record, successRate, avgTTFT, avgDuration, lastSeenValue)
}

// usageSummaryGroupKeys is the closed set of columns a grouped usage summary may
// group by. The key is interpolated into the query text, so it is validated
// against this map instead of being taken from a caller.
var usageSummaryGroupKeys = map[string]struct{}{
	"model":                {},
	"selected_upstream_id": {},
}

// usageSummariesByGroupKey computes the same aggregate as usageSummary for one
// equality clause on groupKey, for every group in one pass, keyed by the raw
// column value. The model catalog and the channel list otherwise run this
// aggregate once per entry (twice per model, once per channel); a group the map
// does not contain is the zero summary a no-match single-key query returned.
func (s *Store) usageSummariesByGroupKey(groupKey string, since time.Time) (map[string]UsageSummaryRecord, error) {
	if _, ok := usageSummaryGroupKeys[groupKey]; !ok {
		return nil, fmt.Errorf("unsupported usage summary group key %q", groupKey)
	}
	where := "1=1"
	var args []any
	if !since.IsZero() {
		where = "recorded_at >= ?"
		args = append(args, since.UTC().Format(timeLayout))
	}
	rows, err := s.db.Query(`
		SELECT `+groupKey+`,`+usageSummaryAggregateColumns+`
		FROM logs
		WHERE `+where+`
		GROUP BY `+groupKey+`
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	summaries := map[string]UsageSummaryRecord{}
	for rows.Next() {
		var (
			group         string
			record        UsageSummaryRecord
			successRate   float64
			avgTTFT       float64
			avgDuration   float64
			lastSeenValue any
		)
		if err := rows.Scan(
			&group,
			&record.RequestCount,
			&record.SuccessRequest,
			&record.FailedRequest,
			&successRate,
			&record.MissingUsage,
			&record.TotalTokens,
			&record.PromptTokens,
			&record.CompletionTokens,
			&record.CachedTokens,
			&avgTTFT,
			&avgDuration,
			&lastSeenValue,
		); err != nil {
			return nil, err
		}
		converted, err := usageSummaryRecordFromAggregate(record, successRate, avgTTFT, avgDuration, lastSeenValue)
		if err != nil {
			return nil, err
		}
		summaries[group] = converted
	}
	return summaries, rows.Err()
}

func (s *Store) usageSummariesByModel(since time.Time) (map[string]UsageSummaryRecord, error) {
	return s.usageSummariesByGroupKey("model", since)
}

func (s *Store) usageTrends(baseWhere string, baseArgs []any, since time.Time, bucketSize time.Duration, bucketCount int) ([]UsageTrendRecord, error) {
	if bucketSize <= 0 {
		bucketSize = 24 * time.Hour
	}
	if bucketCount <= 0 {
		bucketCount = 7
	}
	where := strings.TrimSpace(baseWhere)
	if where == "" {
		where = "1=1"
	}
	args := append([]any(nil), baseArgs...)
	referenceTime := time.Now().UTC()
	var latestRecordedAt any
	if err := s.db.QueryRow(`SELECT MAX(recorded_at) FROM logs WHERE `+where, args...).Scan(&latestRecordedAt); err != nil {
		return nil, err
	}
	if latestTime, err := timeParseNullableValue(latestRecordedAt); err != nil {
		return nil, err
	} else if !latestTime.IsZero() {
		referenceTime = latestTime.UTC()
	}
	if !since.IsZero() {
		where += " AND recorded_at >= ?"
		args = append(args, since.UTC().Format(timeLayout))
	}
	bucketStart := referenceTime.Truncate(bucketSize).Add(-time.Duration(bucketCount-1) * bucketSize)
	queryArgs := append([]any(nil), args...)
	queryArgs = append(queryArgs, bucketStart.Format(timeLayout))
	rows, err := s.db.Query(`
		SELECT recorded_at, status_code, total_tokens, prompt_tokens, completion_tokens, model
		FROM logs
		WHERE `+where+` AND recorded_at >= ?
		ORDER BY recorded_at ASC
	`, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type bucket struct {
		requests int
		failed   int
		missing  int
		tokens   int
		models   map[string]struct{}
	}
	buckets := make(map[time.Time]*bucket, bucketCount)
	for index := 0; index < bucketCount; index++ {
		slot := bucketStart.Add(time.Duration(index) * bucketSize)
		buckets[slot] = &bucket{models: map[string]struct{}{}}
	}
	for rows.Next() {
		var (
			recordedAt  string
			statusCode  int
			totalTokens int
			prompt      int
			completion  int
			model       string
		)
		if err := rows.Scan(&recordedAt, &statusCode, &totalTokens, &prompt, &completion, &model); err != nil {
			return nil, err
		}
		recordedTime, err := timeParse(recordedAt)
		if err != nil {
			return nil, err
		}
		slot := recordedTime.UTC().Truncate(bucketSize)
		item := buckets[slot]
		if item == nil {
			continue
		}
		item.requests++
		if statusCode < 200 || statusCode >= 300 {
			item.failed++
		}
		if statusCode >= 200 && statusCode < 300 && totalTokens == 0 && prompt == 0 && completion == 0 {
			item.missing++
		}
		item.tokens += totalTokens
		if model = strings.TrimSpace(model); isUsageModelName(model) {
			item.models[strings.ToLower(model)] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]UsageTrendRecord, 0, bucketCount)
	for index := 0; index < bucketCount; index++ {
		slot := bucketStart.Add(time.Duration(index) * bucketSize)
		item := buckets[slot]
		out = append(out, UsageTrendRecord{
			Time:          slot,
			RequestCount:  item.requests,
			FailedRequest: item.failed,
			MissingUsage:  item.missing,
			TotalTokens:   item.tokens,
			ModelCount:    len(item.models),
		})
	}
	return out, nil
}

func sortedKeys[V any](values map[string]V) []string {
	out := make([]string, 0, len(values))
	for value := range values {
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func isUsageModelName(model string) bool {
	model = strings.ToLower(strings.TrimSpace(model))
	return model != "" && model != "list_models"
}

func sortedCountItems(counts map[string]int) []CountItem {
	items := make([]CountItem, 0, len(counts))
	for label, count := range counts {
		items = append(items, CountItem{Label: label, Count: count})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Count != items[j].Count {
			return items[i].Count > items[j].Count
		}
		return items[i].Label < items[j].Label
	})
	return items
}

func New(outputDir string) (*Store, error) {
	return NewWithDatabase(outputDir, "sqlite", filepath.Join(outputDir, "trace_index.sqlite3"), 4, 4)
}

func NewWithDatabase(outputDir string, driver string, dsn string, maxOpenConns int, maxIdleConns int) (*Store, error) {
	return NewWithDatabaseOptions(outputDir, driver, dsn, maxOpenConns, maxIdleConns, DatabaseOptions{AutoMigrate: true})
}

func NewWithDatabaseOptions(outputDir string, driver string, dsn string, maxOpenConns int, maxIdleConns int, opts DatabaseOptions) (*Store, error) {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return nil, err
	}
	secrets, err := newLocalSecretBox(outputDir)
	if err != nil {
		return nil, err
	}
	driver = normalizeDatabaseDriver(driver)
	if driver != "sqlite" && driver != "postgres" {
		return nil, fmt.Errorf("store driver %q is not supported yet", driver)
	}

	db, dbPath, entDialect, err := openStoreDatabase(outputDir, driver, dsn)
	if err != nil {
		return nil, err
	}
	if maxOpenConns > 0 {
		db.SetMaxOpenConns(maxOpenConns)
	}
	if maxIdleConns > 0 {
		db.SetMaxIdleConns(maxIdleConns)
	}

	st := &Store{
		db:                    &rebindingDB{DB: db, driver: driver},
		client:                dao.NewClient(dao.Driver(entsql.OpenDB(entDialect, db))),
		outputDir:             outputDir,
		dbPath:                dbPath,
		driver:                driver,
		shared:                &storeShared{},
		secrets:               secrets,
		useSessionSummaryRead: opts.UseSessionSummaryRead,
	}
	if opts.AutoMigrate {
		if err := st.initSchema(); err != nil {
			_ = st.Close()
			return nil, err
		}
	}

	if !opts.AutoMigrate {
		if err := st.ping(); err != nil {
			_ = st.Close()
			return nil, err
		}
		if driver == "postgres" {
			if err := st.requirePostgresApplicationMigrations(); err != nil {
				_ = st.Close()
				return nil, err
			}
		}
	}

	return st, nil
}

func (s *Store) requirePostgresApplicationMigrations() error {
	if s.driver != "postgres" {
		return nil
	}
	var migrationTableExists bool
	if err := s.db.QueryRow(`SELECT EXISTS (
		SELECT 1
		FROM information_schema.tables
		WHERE table_schema = current_schema() AND table_name = 'schema_migrations'
	)`).Scan(&migrationTableExists); err != nil {
		return fmt.Errorf("check postgres application migrations: %w", err)
	}
	if !migrationTableExists {
		return errors.New("postgres application schema is not initialized: schema_migrations table is missing; run `server db migrate up` with the same config before starting with database.auto_migrate=false")
	}
	for _, table := range []string{"session_summaries", "overview_metric_buckets", "overview_metric_bucket_members"} {
		var exists bool
		if err := s.db.QueryRow(`SELECT EXISTS (
			SELECT 1
			FROM information_schema.tables
			WHERE table_schema = current_schema() AND table_name = ?
		)`, table).Scan(&exists); err != nil {
			return fmt.Errorf("check postgres %s migration: %w", table, err)
		}
		if !exists {
			return fmt.Errorf("postgres application schema is missing %s; run `server db migrate up` to apply ent/postgres-migrations before enabling service traffic", table)
		}
	}
	return nil
}

func (s *Store) ping() error {
	if s.db == nil {
		return nil
	}
	return s.db.Ping()
}

func normalizeDatabaseDriver(driver string) string {
	driver = strings.ToLower(strings.TrimSpace(driver))
	switch driver {
	case "":
		// The driver is never chosen implicitly as SQLite: an unset driver means
		// Postgres, and a missing DSN then fails loudly instead of creating a
		// local database file.
		return "postgres"
	case "postgresql":
		return "postgres"
	default:
		return driver
	}
}

func openStoreDatabase(outputDir string, driver string, dsn string) (*sql.DB, string, string, error) {
	switch driver {
	case "sqlite":
		dbPath := config.SQLitePathFromDSN(dsn)
		if strings.TrimSpace(dbPath) == "" {
			dbPath = config.ResolveDefaultSQLitePath(outputDir)
		}
		if dbPath != ":memory:" {
			if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
				return nil, "", "", err
			}
		}
		db, err := sql.Open("sqlite", sqliteDSN(dbPath))
		if err != nil {
			return nil, "", "", err
		}
		return db, dbPath, dialect.SQLite, nil
	case "postgres":
		if strings.TrimSpace(dsn) == "" {
			return nil, "", "", errors.New("postgres store dsn is required")
		}
		db, err := sql.Open("postgres", dsn)
		if err != nil {
			return nil, "", "", err
		}
		return db, dsn, dialect.Postgres, nil
	default:
		return nil, "", "", fmt.Errorf("store driver %q is not supported yet", driver)
	}
}

func newLocalSecretBox(outputDir string) (*secretBox, error) {
	if strings.TrimSpace(outputDir) == "" {
		outputDir = "."
	}
	keyPath := filepath.Join(outputDir, localSecretKeyFile)
	key, err := readOrCreateLocalSecretKey(keyPath)
	if err != nil {
		return nil, err
	}
	box, err := secretBoxFromKey(key, keyPath)
	if err != nil {
		return nil, err
	}
	return box, nil
}

func readOrCreateLocalSecretKey(keyPath string) ([]byte, error) {
	key, err := os.ReadFile(keyPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		key = make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, key); err != nil {
			return nil, err
		}
		encoded := []byte(base64.RawStdEncoding.EncodeToString(key) + "\n")
		if err := os.WriteFile(keyPath, encoded, 0o600); err != nil {
			return nil, err
		}
	} else {
		decoded, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(string(key)))
		if err != nil {
			return nil, fmt.Errorf("decode local secret key: %w", err)
		}
		key = decoded
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("local secret key must be 32 bytes, got %d", len(key))
	}
	return key, nil
}

func secretBoxFromKey(key []byte, keyPath string) (*secretBox, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("local secret key must be 32 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(key)
	return &secretBox{
		aead:        aead,
		mode:        "encrypted-local",
		keyPath:     keyPath,
		fingerprint: hex.EncodeToString(sum[:8]),
	}, nil
}

func (b *secretBox) encryptSecretBytes(plaintext []byte) ([]byte, error) {
	if len(plaintext) == 0 {
		return nil, nil
	}
	if bytes.HasPrefix(plaintext, []byte(secretEnvelopeV1)) {
		return append([]byte(nil), plaintext...), nil
	}
	if b == nil || b.aead == nil {
		return append([]byte(nil), plaintext...), nil
	}
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	sealed := b.aead.Seal(nil, nonce, plaintext, nil)
	payload := append(nonce, sealed...)
	out := secretEnvelopeV1 + base64.RawStdEncoding.EncodeToString(payload)
	return []byte(out), nil
}

func (b *secretBox) decryptSecretBytes(value []byte) ([]byte, error) {
	if len(value) == 0 {
		return nil, nil
	}
	if !bytes.HasPrefix(value, []byte(secretEnvelopeV1)) {
		return append([]byte(nil), value...), nil
	}
	if b == nil || b.aead == nil {
		return nil, errors.New("encrypted local secret cannot be decrypted without a local key")
	}
	encoded := strings.TrimPrefix(string(value), secretEnvelopeV1)
	payload, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, err
	}
	nonceSize := b.aead.NonceSize()
	if len(payload) < nonceSize {
		return nil, errors.New("encrypted local secret payload is too short")
	}
	nonce, ciphertext := payload[:nonceSize], payload[nonceSize:]
	return b.aead.Open(nil, nonce, ciphertext, nil)
}

func (s *Store) SecretStorageMode() string {
	if s == nil || s.secrets == nil || s.secrets.aead == nil {
		return "plaintext-local"
	}
	return s.secrets.mode
}

func (s *Store) SecretStatus() SecretStatus {
	status := SecretStatus{
		Mode: s.SecretStorageMode(),
	}
	if s == nil || s.secrets == nil {
		status.Error = "secret box is not configured"
		return status
	}
	status.KeyPath = s.secrets.keyPath
	status.Fingerprint = s.secrets.fingerprint
	if strings.TrimSpace(status.KeyPath) == "" {
		status.Readable = s.secrets.aead != nil
		return status
	}
	key, err := readLocalSecretKey(status.KeyPath)
	if err != nil {
		status.Error = err.Error()
		return status
	}
	status.Exists = true
	status.Readable = true
	sum := sha256.Sum256(key)
	status.Fingerprint = hex.EncodeToString(sum[:8])
	return status
}

func (s *Store) ExportLocalSecretKey() ([]byte, SecretStatus, error) {
	status := s.SecretStatus()
	if strings.TrimSpace(status.KeyPath) == "" {
		return nil, status, errors.New("local secret key path is not configured")
	}
	key, err := readLocalSecretKey(status.KeyPath)
	if err != nil {
		return nil, status, err
	}
	encoded := []byte(base64.RawStdEncoding.EncodeToString(key) + "\n")
	status.Exists = true
	status.Readable = true
	sum := sha256.Sum256(key)
	status.Fingerprint = hex.EncodeToString(sum[:8])
	return encoded, status, nil
}

func (s *Store) RotateLocalSecretKey() (SecretRotationResult, error) {
	if s == nil || s.secrets == nil || s.secrets.aead == nil {
		return SecretRotationResult{}, errors.New("local secret encryption is not configured")
	}
	if strings.TrimSpace(s.secrets.keyPath) == "" {
		return SecretRotationResult{}, errors.New("local secret key path is not configured")
	}
	oldKey, err := readLocalSecretKey(s.secrets.keyPath)
	if err != nil {
		return SecretRotationResult{}, err
	}
	oldBox, err := secretBoxFromKey(oldKey, s.secrets.keyPath)
	if err != nil {
		return SecretRotationResult{}, err
	}
	newKey := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, newKey); err != nil {
		return SecretRotationResult{}, err
	}
	newBox, err := secretBoxFromKey(newKey, s.secrets.keyPath)
	if err != nil {
		return SecretRotationResult{}, err
	}

	rows, err := s.client.ChannelConfig.Query().
		Order(channelconfig.ByID()).
		All(context.Background())
	if err != nil {
		return SecretRotationResult{}, err
	}
	rotated := make([]rotatedChannelSecret, 0, len(rows))
	result := SecretRotationResult{
		Mode:           newBox.mode,
		KeyPath:        s.secrets.keyPath,
		OldFingerprint: oldBox.fingerprint,
		NewFingerprint: newBox.fingerprint,
		ChannelCount:   len(rows),
	}
	for _, row := range rows {
		item := rotatedChannelSecret{id: row.ID}
		if row.APIKeyCiphertext != nil && len(*row.APIKeyCiphertext) > 0 {
			plaintext, err := oldBox.decryptSecretBytes(*row.APIKeyCiphertext)
			if err != nil {
				return SecretRotationResult{}, fmt.Errorf("decrypt channel %s api key: %w", row.ID, err)
			}
			encrypted, err := newBox.encryptSecretBytes(plaintext)
			if err != nil {
				return SecretRotationResult{}, fmt.Errorf("encrypt channel %s api key: %w", row.ID, err)
			}
			item.apiKey = encrypted
			item.hasAPIKey = true
			result.APIKeyCount++
		}
		headersJSON, secretKeyCount, err := reencryptHeadersJSONWithBoxes(oldBox, newBox, row.HeadersJSON)
		if err != nil {
			return SecretRotationResult{}, fmt.Errorf("reencrypt channel %s headers: %w", row.ID, err)
		}
		item.headersJSON = headersJSON
		item.secretKeyCount = secretKeyCount
		result.HeaderCount += secretKeyCount
		rotated = append(rotated, item)
	}

	encodedNewKey := []byte(base64.RawStdEncoding.EncodeToString(newKey) + "\n")
	backupPath := s.secrets.keyPath + ".bak." + time.Now().UTC().Format("20060102T150405Z")
	if err := os.WriteFile(backupPath, []byte(base64.RawStdEncoding.EncodeToString(oldKey)+"\n"), 0o600); err != nil {
		return SecretRotationResult{}, err
	}
	if err := os.WriteFile(s.secrets.keyPath, encodedNewKey, 0o600); err != nil {
		_ = os.Remove(backupPath)
		return SecretRotationResult{}, err
	}
	if err := s.applyRotatedChannelSecrets(rotated); err != nil {
		_ = os.WriteFile(s.secrets.keyPath, []byte(base64.RawStdEncoding.EncodeToString(oldKey)+"\n"), 0o600)
		return SecretRotationResult{}, err
	}
	s.secrets = newBox
	result.BackupPath = backupPath
	return result, nil
}

func (s *Store) applyRotatedChannelSecrets(items []rotatedChannelSecret) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, item := range items {
		if item.hasAPIKey {
			if _, err := s.execTx(tx, `UPDATE channel_configs SET api_key_ciphertext = ?, headers_json = ?, updated_at = ? WHERE id = ?`, item.apiKey, item.headersJSON, time.Now().UTC().Format(timeLayout), item.id); err != nil {
				return err
			}
			continue
		}
		if _, err := s.execTx(tx, `UPDATE channel_configs SET headers_json = ?, updated_at = ? WHERE id = ?`, item.headersJSON, time.Now().UTC().Format(timeLayout), item.id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func readLocalSecretKey(keyPath string) ([]byte, error) {
	keyData, err := os.ReadFile(keyPath)
	if err != nil {
		return nil, err
	}
	key, err := base64.RawStdEncoding.DecodeString(strings.TrimSpace(string(keyData)))
	if err != nil {
		return nil, fmt.Errorf("decode local secret key: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("local secret key must be 32 bytes, got %d", len(key))
	}
	return key, nil
}

func (s *Store) encryptSecretBytes(plaintext []byte) ([]byte, error) {
	if s == nil {
		return append([]byte(nil), plaintext...), nil
	}
	return s.secrets.encryptSecretBytes(plaintext)
}

func (s *Store) decryptSecretBytes(value []byte) ([]byte, error) {
	if s == nil {
		return append([]byte(nil), value...), nil
	}
	return s.secrets.decryptSecretBytes(value)
}

func (s *Store) encryptHeadersJSON(headersJSON string) (string, error) {
	headersJSON = strings.TrimSpace(headersJSON)
	if headersJSON == "" {
		return "{}", nil
	}
	headers := map[string]string{}
	if err := json.Unmarshal([]byte(headersJSON), &headers); err != nil {
		return "", err
	}
	for key, value := range headers {
		if !isSecretChannelHeader(key) || strings.TrimSpace(value) == "" {
			continue
		}
		encrypted, err := s.encryptSecretBytes([]byte(value))
		if err != nil {
			return "", err
		}
		headers[key] = string(encrypted)
	}
	data, err := json.Marshal(headers)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (s *Store) decryptHeadersJSON(headersJSON string) (string, error) {
	headersJSON = strings.TrimSpace(headersJSON)
	if headersJSON == "" {
		return "{}", nil
	}
	headers := map[string]string{}
	if err := json.Unmarshal([]byte(headersJSON), &headers); err != nil {
		return "", err
	}
	for key, value := range headers {
		if !isSecretChannelHeader(key) || !strings.HasPrefix(value, secretEnvelopeV1) {
			continue
		}
		plaintext, err := s.decryptSecretBytes([]byte(value))
		if err != nil {
			return "", err
		}
		headers[key] = string(plaintext)
	}
	data, err := json.Marshal(headers)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func reencryptHeadersJSONWithBoxes(oldBox *secretBox, newBox *secretBox, headersJSON string) (string, int, error) {
	headersJSON = strings.TrimSpace(headersJSON)
	if headersJSON == "" {
		return "{}", 0, nil
	}
	headers := map[string]string{}
	if err := json.Unmarshal([]byte(headersJSON), &headers); err != nil {
		return "", 0, err
	}
	count := 0
	for key, value := range headers {
		if !isSecretChannelHeader(key) || strings.TrimSpace(value) == "" {
			continue
		}
		plaintext, err := oldBox.decryptSecretBytes([]byte(value))
		if err != nil {
			return "", 0, err
		}
		encrypted, err := newBox.encryptSecretBytes(plaintext)
		if err != nil {
			return "", 0, err
		}
		headers[key] = string(encrypted)
		count++
	}
	data, err := json.Marshal(headers)
	if err != nil {
		return "", 0, err
	}
	return string(data), count, nil
}

func isSecretChannelHeader(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	return key == "authorization" || strings.Contains(key, "api-key") || strings.Contains(key, "apikey") || strings.Contains(key, "token")
}

func sqliteDSN(dbPath string) string {
	dbPath = normalizeSQLiteFilePath(dbPath)
	values := url.Values{}
	for _, pragma := range []string{
		"journal_mode(WAL)",
		"synchronous(NORMAL)",
		"busy_timeout(5000)",
		"wal_autocheckpoint(1000)",
	} {
		values.Add("_pragma", pragma)
	}
	u := url.URL{
		Scheme:   "file",
		Path:     dbPath,
		RawQuery: values.Encode(),
	}
	return u.String()
}

func normalizeSQLiteFilePath(dbPath string) string {
	dbPath = strings.TrimSpace(dbPath)
	if dbPath == "" || dbPath == ":memory:" || filepath.IsAbs(dbPath) {
		return dbPath
	}
	if abs, err := filepath.Abs(dbPath); err == nil {
		return abs
	}
	return dbPath
}

// Close settles the derived read models and then closes the database handle.
//
// The deferred derived queues are process-local: once the handle is gone, whatever is still
// queued can only come back by rebuilding the derived tables from the index, and nothing
// calls that rebuild in normal operation. `serve` flushes explicitly on its shutdown path,
// but every other caller of this store (the analyze and db commands) only closes it, and a
// command that repaired usage or re-indexed a trace would otherwise commit the index row and
// leave the overview buckets and session summaries showing the old numbers forever. Flushing
// here covers all of them and is a no-op when the queue is empty.
//
// Close is idempotent: the second call does nothing, so a defer next to an explicit close
// does not flush a closed handle.
func (s *Store) Close() error {
	if s == nil || !s.closed.CompareAndSwap(false, true) {
		return nil
	}
	s.flushDerivedRefresh()
	if s.client != nil {
		return s.client.Close()
	}
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// EntClient returns the generated ent client backing this store.
func (s *Store) EntClient() *dao.Client {
	return s.client
}

func (s *Store) execTx(tx *sql.Tx, query string, args ...any) (sql.Result, error) {
	if s == nil || s.db == nil {
		return tx.Exec(query, args...)
	}
	return tx.Exec(s.db.rebind(query), args...)
}

func (s *Store) hasColumn(table string, column string) (bool, error) {
	typ, err := s.columnType(table, column)
	if err != nil {
		return false, err
	}
	return typ != "", nil
}

func (s *Store) columnType(table string, column string) (string, error) {
	if s != nil && s.driver == "postgres" {
		var typ string
		err := s.db.QueryRow(`
			SELECT data_type
			FROM information_schema.columns
			WHERE table_schema = current_schema()
				AND table_name = ?
				AND column_name = ?
		`, table, column).Scan(&typ)
		if errors.Is(err, sql.ErrNoRows) {
			return "", nil
		}
		if err != nil {
			return "", err
		}
		return typ, nil
	}

	rows, err := s.db.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var (
		cid        int
		name       string
		typ        string
		notNull    int
		defaultVal sql.NullString
		pk         int
	)
	for rows.Next() {
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultVal, &pk); err != nil {
			return "", err
		}
		if name == column {
			return typ, rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	return "", nil
}

func (s *Store) backfillTraceIDs() error {
	rows, err := s.db.Query(`SELECT path FROM logs WHERE trace_id = '' OR trace_id IS NULL`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return err
		}
		paths = append(paths, path)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, path := range paths {
		if _, err := s.db.Exec(`UPDATE logs SET trace_id = ? WHERE path = ?`, uuid.NewString(), path); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) backfillSemantics() error {
	rows, err := s.db.Query(`SELECT path, url, provider, operation, endpoint FROM logs`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type rowData struct {
		path      string
		url       string
		provider  string
		operation string
		endpoint  string
	}
	var updates []rowData
	for rows.Next() {
		var row rowData
		if err := rows.Scan(&row.path, &row.url, &row.provider, &row.operation, &row.endpoint); err != nil {
			return err
		}
		if row.provider != "" && row.operation != "" && row.endpoint != "" {
			continue
		}
		semantics := llm.ClassifyPath(row.url, "")
		row.provider = semantics.Provider
		row.operation = semantics.Operation
		row.endpoint = semantics.Endpoint
		updates = append(updates, row)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, update := range updates {
		if _, err := s.db.Exec(
			`UPDATE logs SET provider = ?, operation = ?, endpoint = ? WHERE path = ?`,
			update.provider,
			update.operation,
			update.endpoint,
			update.path,
		); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) UpsertLog(path string, header recordfile.RecordHeader) error {
	return s.UpsertLogWithGrouping(path, header, GroupingInfo{})
}

func (s *Store) UpsertUpstreamTarget(record UpstreamTargetRecord) error {
	create := s.client.UpstreamTarget.Create().
		SetID(record.ID).
		SetBaseURL(record.BaseURL).
		SetProviderPreset(record.ProviderPreset).
		SetProtocolFamily(record.ProtocolFamily).
		SetRoutingProfile(record.RoutingProfile).
		SetEnabled(record.Enabled).
		SetPriority(record.Priority).
		SetWeight(record.Weight).
		SetCapacityHint(record.CapacityHint).
		SetLastRefreshStatus(record.LastRefreshStatus).
		SetLastRefreshError(record.LastRefreshError)
	if !record.LastRefreshAt.IsZero() {
		create.SetLastRefreshAt(record.LastRefreshAt.UTC())
	}
	return create.
		OnConflictColumns(upstreamtarget.FieldID).
		UpdateNewValues().
		Exec(context.Background())
}

func (s *Store) CreateDataset(name string, description string) (DatasetRecord, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return DatasetRecord{}, errors.New("dataset name is required")
	}
	now := time.Now().UTC()
	record := DatasetRecord{
		ID:          uuid.NewString(),
		Name:        name,
		Description: strings.TrimSpace(description),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.client.Dataset.Create().
		SetID(record.ID).
		SetName(record.Name).
		SetDescription(record.Description).
		SetCreatedAt(record.CreatedAt).
		SetUpdatedAt(record.UpdatedAt).
		Exec(context.Background()); err != nil {
		return DatasetRecord{}, err
	}
	return record, nil
}

func (s *Store) ListDatasets() ([]DatasetRecord, error) {
	ctx := context.Background()
	rows, err := s.client.Dataset.Query().
		Order(dataset.ByUpdatedAt(entsql.OrderDesc()), dataset.ByID(entsql.OrderDesc())).
		All(ctx)
	if err != nil {
		return nil, err
	}

	// One grouped count for every dataset replaces the per-dataset COUNT this
	// loop used to run.
	counts := map[string]int{}
	countRows, err := s.db.Query(`SELECT dataset_id, COUNT(*) FROM dataset_examples GROUP BY dataset_id`)
	if err != nil {
		return nil, err
	}
	for countRows.Next() {
		var (
			datasetID string
			count     int
		)
		if err := countRows.Scan(&datasetID, &count); err != nil {
			countRows.Close()
			return nil, err
		}
		counts[datasetID] = count
	}
	err = countRows.Err()
	countRows.Close()
	if err != nil {
		return nil, err
	}

	out := make([]DatasetRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, datasetRecordFromEnt(row, counts[row.ID]))
	}
	return out, nil
}

func (s *Store) GetDataset(datasetID string) (DatasetRecord, error) {
	ctx := context.Background()
	row, err := s.client.Dataset.Get(ctx, datasetID)
	if err != nil {
		if dao.IsNotFound(err) {
			return DatasetRecord{}, sql.ErrNoRows
		}
		return DatasetRecord{}, err
	}
	count, err := s.client.DatasetExample.Query().
		Where(datasetexample.DatasetIDEQ(row.ID)).
		Count(ctx)
	if err != nil {
		return DatasetRecord{}, err
	}
	return datasetRecordFromEnt(row, count), nil
}

func (s *Store) AppendDatasetExamples(datasetID string, traceIDs []string, sourceType string, sourceID string, note string) (int, int, error) {
	datasetID = strings.TrimSpace(datasetID)
	if datasetID == "" {
		return 0, 0, errors.New("dataset id is required")
	}
	if _, err := s.GetDataset(datasetID); err != nil {
		return 0, 0, err
	}

	seen := map[string]struct{}{}
	ordered := make([]string, 0, len(traceIDs))
	for _, traceID := range traceIDs {
		traceID = strings.TrimSpace(traceID)
		if traceID == "" {
			continue
		}
		if _, ok := seen[traceID]; ok {
			continue
		}
		seen[traceID] = struct{}{}
		ordered = append(ordered, traceID)
	}
	if len(ordered) == 0 {
		return 0, 0, nil
	}

	ctx := context.Background()
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()

	nextPosition := 0
	existingCount, err := tx.DatasetExample.Query().
		Where(datasetexample.DatasetIDEQ(datasetID)).
		Count(ctx)
	if err != nil {
		return 0, 0, err
	}
	if existingCount > 0 {
		nextPosition, err = tx.DatasetExample.Query().
			Where(datasetexample.DatasetIDEQ(datasetID)).
			Aggregate(dao.Max(datasetexample.FieldPosition)).
			Int(ctx)
		if err != nil {
			return 0, 0, err
		}
	}
	// Both checks below used to be one query per candidate trace: a read of the
	// trace row and an existence count of the dataset example.
	knownTraces, err := s.knownTraceIDs(ctx, ordered)
	if err != nil {
		return 0, 0, err
	}
	presentExamples, err := datasetExampleTraceIDs(ctx, tx, datasetID, ordered)
	if err != nil {
		return 0, 0, err
	}

	now := time.Now().UTC()
	added := 0
	skipped := 0
	creates := make([]*dao.DatasetExampleCreate, 0, len(ordered))
	for _, traceID := range ordered {
		if _, ok := knownTraces[traceID]; !ok {
			// Re-read the missing trace for the error the per-trace read raised,
			// which is sql.ErrNoRows for an unknown trace id.
			if _, err := s.GetByID(traceID); err != nil {
				return 0, 0, err
			}
			return 0, 0, fmt.Errorf("trace %q not found", traceID)
		}
		if _, ok := presentExamples[traceID]; ok {
			skipped++
			continue
		}
		nextPosition++
		added++
		creates = append(creates, tx.DatasetExample.Create().
			SetDatasetID(datasetID).
			SetTraceID(traceID).
			SetPosition(nextPosition).
			SetAddedAt(now).
			SetSourceType(strings.TrimSpace(sourceType)).
			SetSourceID(strings.TrimSpace(sourceID)).
			SetNote(strings.TrimSpace(note)))
	}
	if len(creates) > 0 {
		if err := tx.DatasetExample.CreateBulk(creates...).
			OnConflictColumns(datasetexample.FieldDatasetID, datasetexample.FieldTraceID).
			DoNothing().
			Exec(ctx); err != nil {
			return 0, 0, err
		}
	}
	if err := tx.Dataset.UpdateOneID(datasetID).SetUpdatedAt(now).Exec(ctx); err != nil {
		return 0, 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, err
	}
	return added, skipped, nil
}

func (s *Store) GetDatasetExamples(datasetID string) ([]DatasetExampleRecord, error) {
	rows, err := s.client.DatasetExample.Query().
		Where(datasetexample.DatasetIDEQ(datasetID)).
		Order(datasetexample.ByPosition(), datasetexample.ByTraceID()).
		All(context.Background())
	if err != nil {
		return nil, err
	}

	// One query per chunk of trace ids replaces the read of the trace row per
	// example, which a dataset detail view paid for every example.
	traceIDs := make([]string, 0, len(rows))
	for _, row := range rows {
		traceIDs = append(traceIDs, row.TraceID)
	}
	traces, err := s.traceLogsByTraceID(context.Background(), traceIDs)
	if err != nil {
		return nil, err
	}

	out := make([]DatasetExampleRecord, 0, len(rows))
	for _, row := range rows {
		trace, ok := traces[row.TraceID]
		if !ok {
			// The example outlived the trace it points at.
			continue
		}
		out = append(out, DatasetExampleRecord{
			DatasetID:  row.DatasetID,
			TraceID:    row.TraceID,
			Position:   row.Position,
			AddedAt:    row.AddedAt,
			SourceType: row.SourceType,
			SourceID:   row.SourceID,
			Note:       row.Note,
			Trace:      trace,
		})
	}
	return out, nil
}

func (s *Store) CreateEvalRun(datasetID string, sourceType string, sourceID string, evaluatorSet string, traceCount int) (EvalRunRecord, error) {
	evaluatorSet = strings.TrimSpace(evaluatorSet)
	if evaluatorSet == "" {
		return EvalRunRecord{}, errors.New("evaluator set is required")
	}
	now := time.Now().UTC()
	record := EvalRunRecord{
		ID:           uuid.NewString(),
		DatasetID:    strings.TrimSpace(datasetID),
		SourceType:   strings.TrimSpace(sourceType),
		SourceID:     strings.TrimSpace(sourceID),
		EvaluatorSet: evaluatorSet,
		CreatedAt:    now,
		CompletedAt:  now,
		TraceCount:   traceCount,
	}
	if err := s.client.EvalRun.Create().
		SetID(record.ID).
		SetDatasetID(record.DatasetID).
		SetSourceType(record.SourceType).
		SetSourceID(record.SourceID).
		SetEvaluatorSet(record.EvaluatorSet).
		SetCreatedAt(record.CreatedAt).
		SetCompletedAt(record.CompletedAt).
		SetTraceCount(record.TraceCount).
		SetScoreCount(0).
		SetPassCount(0).
		SetFailCount(0).
		Exec(context.Background()); err != nil {
		return EvalRunRecord{}, err
	}
	return record, nil
}

func (s *Store) FinalizeEvalRun(evalRunID string, scoreCount int, passCount int, failCount int) error {
	_, err := s.client.EvalRun.Update().
		Where(evalrun.IDEQ(evalRunID)).
		SetCompletedAt(time.Now().UTC()).
		SetScoreCount(scoreCount).
		SetPassCount(passCount).
		SetFailCount(failCount).
		Save(context.Background())
	return err
}

func (s *Store) AddScore(record ScoreRecord) (ScoreRecord, error) {
	if strings.TrimSpace(record.TraceID) == "" {
		return ScoreRecord{}, errors.New("trace id is required")
	}
	if strings.TrimSpace(record.EvaluatorKey) == "" {
		return ScoreRecord{}, errors.New("evaluator key is required")
	}
	now := time.Now().UTC()
	if record.ID == "" {
		record.ID = uuid.NewString()
	}
	record.CreatedAt = now
	if err := s.client.Score.Create().
		SetID(record.ID).
		SetTraceID(record.TraceID).
		SetSessionID(record.SessionID).
		SetDatasetID(record.DatasetID).
		SetEvalRunID(record.EvalRunID).
		SetEvaluatorKey(record.EvaluatorKey).
		SetValue(record.Value).
		SetStatus(record.Status).
		SetLabel(record.Label).
		SetExplanation(record.Explanation).
		SetCreatedAt(record.CreatedAt).
		Exec(context.Background()); err != nil {
		return ScoreRecord{}, err
	}
	return record, nil
}

func (s *Store) GetEvalRun(evalRunID string) (EvalRunRecord, error) {
	row, err := s.client.EvalRun.Get(context.Background(), evalRunID)
	if err != nil {
		if dao.IsNotFound(err) {
			return EvalRunRecord{}, sql.ErrNoRows
		}
		return EvalRunRecord{}, err
	}
	return evalRunRecordFromEnt(row), nil
}

func (s *Store) ListEvalRuns(limit int) ([]EvalRunRecord, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.client.EvalRun.Query().
		Order(evalrun.ByCreatedAt(entsql.OrderDesc()), evalrun.ByID(entsql.OrderDesc())).
		Limit(limit).
		All(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]EvalRunRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, evalRunRecordFromEnt(row))
	}
	return out, nil
}

func (s *Store) ListScores(filter ScoreFilter, limit int) ([]ScoreRecord, error) {
	if limit <= 0 {
		limit = 200
	}
	var predicates []predicate.Score
	if traceID := strings.TrimSpace(filter.TraceID); traceID != "" {
		predicates = append(predicates, score.TraceIDEQ(traceID))
	}
	if sessionID := strings.TrimSpace(filter.SessionID); sessionID != "" {
		predicates = append(predicates, score.SessionIDEQ(sessionID))
	}
	if datasetID := strings.TrimSpace(filter.DatasetID); datasetID != "" {
		predicates = append(predicates, score.DatasetIDEQ(datasetID))
	}
	if evalRunID := strings.TrimSpace(filter.EvalRunID); evalRunID != "" {
		predicates = append(predicates, score.EvalRunIDEQ(evalRunID))
	}
	rows, err := s.client.Score.Query().
		Where(predicates...).
		Order(score.ByCreatedAt(entsql.OrderDesc()), score.ByID(entsql.OrderDesc())).
		Limit(limit).
		All(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]ScoreRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, scoreRecordFromEnt(row))
	}
	return out, nil
}

func (s *Store) CreateExperimentRun(record ExperimentRunRecord) (ExperimentRunRecord, error) {
	if strings.TrimSpace(record.BaselineEvalRunID) == "" {
		return ExperimentRunRecord{}, errors.New("baseline eval run id is required")
	}
	if strings.TrimSpace(record.CandidateEvalRunID) == "" {
		return ExperimentRunRecord{}, errors.New("candidate eval run id is required")
	}
	record.ID = uuid.NewString()
	record.CreatedAt = time.Now().UTC()
	record.Name = strings.TrimSpace(record.Name)
	record.Description = strings.TrimSpace(record.Description)
	record.BaselineEvalRunID = strings.TrimSpace(record.BaselineEvalRunID)
	record.CandidateEvalRunID = strings.TrimSpace(record.CandidateEvalRunID)
	if err := s.client.ExperimentRun.Create().
		SetID(record.ID).
		SetName(record.Name).
		SetDescription(record.Description).
		SetBaselineEvalRunID(record.BaselineEvalRunID).
		SetCandidateEvalRunID(record.CandidateEvalRunID).
		SetCreatedAt(record.CreatedAt).
		SetBaselineScoreCount(record.BaselineScoreCount).
		SetCandidateScoreCount(record.CandidateScoreCount).
		SetBaselinePassRate(record.BaselinePassRate).
		SetCandidatePassRate(record.CandidatePassRate).
		SetPassRateDelta(record.PassRateDelta).
		SetMatchedScoreCount(record.MatchedScoreCount).
		SetImprovementCount(record.ImprovementCount).
		SetRegressionCount(record.RegressionCount).
		Exec(context.Background()); err != nil {
		return ExperimentRunRecord{}, err
	}
	return record, nil
}

func (s *Store) GetExperimentRun(experimentRunID string) (ExperimentRunRecord, error) {
	row, err := s.client.ExperimentRun.Get(context.Background(), experimentRunID)
	if err != nil {
		if dao.IsNotFound(err) {
			return ExperimentRunRecord{}, sql.ErrNoRows
		}
		return ExperimentRunRecord{}, err
	}
	return experimentRunRecordFromEnt(row), nil
}

func (s *Store) ListExperimentRuns(limit int) ([]ExperimentRunRecord, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.client.ExperimentRun.Query().
		Order(experimentrun.ByCreatedAt(entsql.OrderDesc()), experimentrun.ByID(entsql.OrderDesc())).
		Limit(limit).
		All(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]ExperimentRunRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, experimentRunRecordFromEnt(row))
	}
	return out, nil
}

func (s *Store) ReplaceUpstreamModels(upstreamID string, records []UpstreamModelRecord) error {
	ctx := context.Background()
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.UpstreamModel.Delete().Where(upstreammodel.UpstreamIDEQ(upstreamID)).Exec(ctx); err != nil {
		return err
	}
	creates := make([]*dao.UpstreamModelCreate, 0, len(records))
	for _, record := range records {
		seenAt := record.SeenAt
		if seenAt.IsZero() {
			seenAt = time.Now().UTC()
		}
		creates = append(creates, tx.UpstreamModel.Create().
			SetUpstreamID(upstreamID).
			SetModel(record.Model).
			SetSource(record.Source).
			SetSeenAt(seenAt.UTC()))
	}
	if len(creates) > 0 {
		if err := tx.UpstreamModel.CreateBulk(creates...).
			OnConflictColumns(upstreammodel.FieldUpstreamID, upstreammodel.FieldModel).
			UpdateNewValues().
			Exec(ctx); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// UpsertLogWithGrouping indexes one cassette. The derived read models
// (session_summaries and the overview metric buckets) are deferred; see
// markDerivedRefreshForPath.
func (s *Store) UpsertLogWithGrouping(path string, header recordfile.RecordHeader, grouping GroupingInfo) error {
	return s.upsertLogWithGrouping(path, header, grouping)
}

func (s *Store) upsertLogWithGrouping(path string, header recordfile.RecordHeader, grouping GroupingInfo) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	traceID, err := s.lookupOrCreateTraceID(path)
	if err != nil {
		return err
	}
	previousSessionID := s.sessionIDForPath(path)

	cachedTokens := 0
	if header.Usage.PromptTokenDetails != nil {
		cachedTokens = header.Usage.PromptTokenDetails.CachedTokens
	}
	if header.Meta.Provider == "" || header.Meta.Operation == "" || header.Meta.Endpoint == "" {
		semantics := llm.ClassifyPath(header.Meta.URL, "")
		if header.Meta.Provider == "" {
			header.Meta.Provider = semantics.Provider
		}
		if header.Meta.Operation == "" {
			header.Meta.Operation = semantics.Operation
		}
		if header.Meta.Endpoint == "" {
			header.Meta.Endpoint = semantics.Endpoint
		}
	}

	_, err = s.db.Exec(`
		INSERT INTO logs (
			path, trace_id, mod_time_ns, file_size, version, request_id, recorded_at, model, provider, operation, endpoint, url, method,
			status_code, duration_ms, ttft_ms, client_ip, content_length, error_text,
			prompt_tokens, completion_tokens, total_tokens, cached_tokens,
			req_header_len, req_body_len, res_header_len, res_body_len, is_stream,
			session_id, session_source, window_id, client_request_id,
			request_audit_id, response_id,
			exchange_id, exchange_kind, exchange_role, parent_exchange_id, sequence_index,
			selected_upstream_id, selected_upstream_base_url, selected_upstream_provider_preset,
			routing_policy, routing_score, routing_candidate_count, routing_failure_reason
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(path) DO UPDATE SET
			trace_id=CASE WHEN logs.trace_id = '' THEN excluded.trace_id ELSE logs.trace_id END,
			mod_time_ns=excluded.mod_time_ns,
			file_size=excluded.file_size,
			version=excluded.version,
			request_id=excluded.request_id,
			recorded_at=excluded.recorded_at,
			model=excluded.model,
			provider=excluded.provider,
			operation=excluded.operation,
			endpoint=excluded.endpoint,
			url=excluded.url,
			method=excluded.method,
			status_code=excluded.status_code,
			duration_ms=excluded.duration_ms,
			ttft_ms=excluded.ttft_ms,
			client_ip=excluded.client_ip,
			content_length=excluded.content_length,
			error_text=excluded.error_text,
			prompt_tokens=excluded.prompt_tokens,
			completion_tokens=excluded.completion_tokens,
			total_tokens=excluded.total_tokens,
			cached_tokens=excluded.cached_tokens,
			req_header_len=excluded.req_header_len,
			req_body_len=excluded.req_body_len,
			res_header_len=excluded.res_header_len,
			res_body_len=excluded.res_body_len,
			is_stream=excluded.is_stream,
			session_id=excluded.session_id,
			session_source=excluded.session_source,
			window_id=excluded.window_id,
			client_request_id=excluded.client_request_id,
			request_audit_id=excluded.request_audit_id,
			response_id=excluded.response_id,
			exchange_id=excluded.exchange_id,
			exchange_kind=excluded.exchange_kind,
			exchange_role=excluded.exchange_role,
			parent_exchange_id=excluded.parent_exchange_id,
			sequence_index=excluded.sequence_index,
			selected_upstream_id=excluded.selected_upstream_id,
			selected_upstream_base_url=excluded.selected_upstream_base_url,
			selected_upstream_provider_preset=excluded.selected_upstream_provider_preset,
			routing_policy=excluded.routing_policy,
			routing_score=excluded.routing_score,
			routing_candidate_count=excluded.routing_candidate_count,
			routing_failure_reason=excluded.routing_failure_reason
	`,
		sanitizeDBText(path),
		sanitizeDBText(traceID),
		info.ModTime().UnixNano(),
		info.Size(),
		sanitizeDBText(header.Version),
		sanitizeDBText(header.Meta.RequestID),
		header.Meta.Time.UTC().Format(timeLayout),
		sanitizeDBText(header.Meta.Model),
		sanitizeDBText(header.Meta.Provider),
		sanitizeDBText(header.Meta.Operation),
		sanitizeDBText(header.Meta.Endpoint),
		sanitizeDBText(header.Meta.URL),
		sanitizeDBText(header.Meta.Method),
		header.Meta.StatusCode,
		header.Meta.DurationMs,
		header.Meta.TTFTMs,
		sanitizeDBText(header.Meta.ClientIP),
		header.Meta.ContentLength,
		sanitizeDBText(header.Meta.Error),
		header.Usage.PromptTokens,
		header.Usage.CompletionTokens,
		header.Usage.TotalTokens,
		cachedTokens,
		header.Layout.ReqHeaderLen,
		header.Layout.ReqBodyLen,
		header.Layout.ResHeaderLen,
		header.Layout.ResBodyLen,
		header.Layout.IsStream,
		sanitizeDBText(grouping.SessionID),
		sanitizeDBText(grouping.SessionSource),
		sanitizeDBText(grouping.WindowID),
		sanitizeDBText(grouping.ClientRequestID),
		sanitizeDBText(header.Meta.RequestAuditID),
		sanitizeDBText(header.Meta.ResponseID),
		sanitizeDBText(header.Meta.ExchangeID),
		sanitizeDBText(header.Meta.ExchangeKind),
		sanitizeDBText(header.Meta.ExchangeRole),
		sanitizeDBText(header.Meta.ParentExchangeID),
		header.Meta.SequenceIndex,
		sanitizeDBText(header.Meta.SelectedUpstreamID),
		sanitizeDBText(header.Meta.SelectedUpstreamBaseURL),
		sanitizeDBText(header.Meta.SelectedUpstreamProviderPreset),
		sanitizeDBText(header.Meta.RoutingPolicy),
		header.Meta.RoutingScore,
		header.Meta.RoutingCandidateCount,
		sanitizeDBText(header.Meta.RoutingFailureReason),
	)

	if err != nil {
		return err
	}
	s.invalidateStatsCache()
	s.markDerivedRefreshForPath(path, previousSessionID, grouping.SessionID)
	return s.upsertSystemEventsForLog(traceID, header, grouping)
}

func (s *Store) UpdateLogUsage(traceID string, usage recordfile.UsageInfo) error {
	traceID = strings.TrimSpace(traceID)
	if traceID == "" {
		return errors.New("update log usage: trace id is required")
	}
	cachedTokens := 0
	if usage.PromptTokenDetails != nil {
		cachedTokens = usage.PromptTokenDetails.CachedTokens
	}
	_, err := s.db.Exec(`
		UPDATE logs
		SET prompt_tokens = ?, completion_tokens = ?, total_tokens = ?, cached_tokens = ?
		WHERE trace_id = ?
	`, usage.PromptTokens, usage.CompletionTokens, usage.TotalTokens, cachedTokens, traceID)
	if err != nil {
		return err
	}
	s.markDerivedRefreshForTrace(traceID)
	return nil
}

// derivedQueueLimit bounds how many paths, traces and sessions one process
// keeps deferred before it applies them. A long-running writer must not grow the
// queue without bound, and a smaller bound also keeps the staleness window short.
const derivedQueueLimit = 256

func (shared *storeShared) derivedQueueLimitLocked() int {
	if shared.derivedLimit > 0 {
		return shared.derivedLimit
	}
	return derivedQueueLimit
}

// markDerivedRefreshForPath defers the derived-table work for one indexed
// recording. Both derived tables are rebuilt from whole sessions and hour
// buckets, and they are read models: doing that work inside the recording path
// cost about as much as the rest of the write (measured on the SQLite harness)
// and it repeated the whole-session aggregate once per request of that session.
// The work is applied by flushDerivedRefresh, which every reader calls first.
func (s *Store) markDerivedRefreshForPath(path string, sessionIDs ...string) {
	if s == nil || s.shared == nil {
		return
	}
	path = strings.TrimSpace(path)
	shared := s.shared
	shared.derivedMu.Lock()
	ensureDerivedQueuesLocked(shared)
	if path != "" {
		shared.derivedPaths[path] = struct{}{}
	}
	for _, sessionID := range sessionIDs {
		if sessionID = strings.TrimSpace(sessionID); sessionID != "" {
			shared.derivedSessions[sessionID] = struct{}{}
		}
	}
	over := shared.derivedQueueSizeLocked() >= shared.derivedQueueLimitLocked()
	shared.derivedMu.Unlock()
	if over {
		s.flushDerivedRefresh()
	}
}

// markDerivedRefreshForTrace defers the derived-table work for a trace whose
// metric columns changed. Resolving the trace to its path and session is part of
// the flush, so an update no longer pays two extra point queries per call.
func (s *Store) markDerivedRefreshForTrace(traceID string) {
	if s == nil || s.shared == nil {
		return
	}
	traceID = strings.TrimSpace(traceID)
	if traceID == "" {
		return
	}
	shared := s.shared
	shared.derivedMu.Lock()
	ensureDerivedQueuesLocked(shared)
	shared.derivedTraces[traceID] = struct{}{}
	over := shared.derivedQueueSizeLocked() >= shared.derivedQueueLimitLocked()
	shared.derivedMu.Unlock()
	if over {
		s.flushDerivedRefresh()
	}
}

func ensureDerivedQueuesLocked(shared *storeShared) {
	if shared.derivedPaths == nil {
		shared.derivedPaths = map[string]struct{}{}
	}
	if shared.derivedTraces == nil {
		shared.derivedTraces = map[string]struct{}{}
	}
	if shared.derivedSessions == nil {
		shared.derivedSessions = map[string]struct{}{}
	}
}

func (shared *storeShared) derivedQueueSizeLocked() int {
	return len(shared.derivedPaths) + len(shared.derivedTraces) + len(shared.derivedSessions)
}

// FlushDerivedRefresh applies every deferred derived-table refresh now. Every
// reader of the derived tables already does this before it answers; the entry
// point exists for callers that want the write side settled without reading, such
// as a shutdown path or a tool that reports on the derived tables.
func (s *Store) FlushDerivedRefresh() {
	s.flushDerivedRefresh()
}

// flushDerivedRefresh applies every deferred derived-table refresh, best-effort:
// the index rows are already committed, so a failure is reported and never
// returned, exactly like the per-file refresh it replaces. Callers that read the
// derived tables call this first, which is what keeps the read models consistent
// for every consumer while the write path stays cheap.
func (s *Store) flushDerivedRefresh() {
	if s == nil || s.shared == nil || s.db == nil {
		return
	}
	if s.TransactionScoped() {
		// A transaction-scoped view shares the queue but not the transaction:
		// applying the work here would join somebody else's transaction.
		return
	}
	shared := s.shared
	// One flush at a time, and the lock is taken before the queue is read: a
	// reader that arrives while a flush is running waits for it and then finds an
	// empty queue, so the barrier really is complete. It also keeps two rebuilds
	// of the same session from committing out of order.
	shared.derivedFlushMu.Lock()
	defer shared.derivedFlushMu.Unlock()

	shared.derivedMu.Lock()
	paths := sortedKeys(shared.derivedPaths)
	traces := sortedKeys(shared.derivedTraces)
	sessions := sortedKeys(shared.derivedSessions)
	shared.derivedPaths = map[string]struct{}{}
	shared.derivedTraces = map[string]struct{}{}
	shared.derivedSessions = map[string]struct{}{}
	shared.derivedMu.Unlock()
	if len(paths) == 0 && len(traces) == 0 && len(sessions) == 0 {
		return
	}

	if len(traces) > 0 {
		resolvedPaths, resolvedSessions, err := s.resolveDeferredTraceRefs(traces)
		if err != nil {
			// The traces could not be resolved to paths and sessions, so their refresh is
			// still owed: put them back rather than dropping them.
			fmt.Fprintf(os.Stderr, "trajecta: resolve deferred derived refresh failed: %v\n", err)
			s.requeueDerivedTraces(traces)
		} else {
			paths = dedupeNonEmptyStrings(append(paths, resolvedPaths...))
			sessions = dedupeNonEmptyStrings(append(sessions, resolvedSessions...))
		}
	}
	if !s.refreshOverviewMetricBucketsBestEffort(paths) {
		s.requeueDerivedPaths(paths)
	}
	if failed := s.refreshSessionSummariesBestEffort(sessions...); len(failed) > 0 {
		s.requeueDerivedSessions(failed)
	}
}

// requeueDerived* put work back on the deferred queue after a failed refresh. The work is
// keyed by path/session in a set, so a concurrent writer that already re-queued the same
// entry costs nothing, and the next flush (a reader, or a write that crosses the queue limit)
// retries it. Losing the entry instead would leave the derived table permanently wrong,
// because the queue is the only record that the aggregate is owed an update.
func (s *Store) requeueDerivedPaths(paths []string) {
	s.requeueDerived(func(shared *storeShared) {
		ensureDerivedQueuesLocked(shared)
		for _, path := range paths {
			if path = strings.TrimSpace(path); path != "" {
				shared.derivedPaths[path] = struct{}{}
			}
		}
	})
}

func (s *Store) requeueDerivedTraces(traceIDs []string) {
	s.requeueDerived(func(shared *storeShared) {
		ensureDerivedQueuesLocked(shared)
		for _, traceID := range traceIDs {
			if traceID = strings.TrimSpace(traceID); traceID != "" {
				shared.derivedTraces[traceID] = struct{}{}
			}
		}
	})
}

func (s *Store) requeueDerivedSessions(sessionIDs []string) {
	s.requeueDerived(func(shared *storeShared) {
		ensureDerivedQueuesLocked(shared)
		for _, sessionID := range sessionIDs {
			if sessionID = strings.TrimSpace(sessionID); sessionID != "" {
				shared.derivedSessions[sessionID] = struct{}{}
			}
		}
	})
}

func (s *Store) requeueDerived(apply func(shared *storeShared)) {
	if s == nil || s.shared == nil {
		return
	}
	// A transaction-scoped view shares the queue, and the queue is process state rather than
	// transaction state, so requeueing through such a view is safe.
	shared := s.shared
	shared.derivedMu.Lock()
	apply(shared)
	shared.derivedMu.Unlock()
}

func (s *Store) resolveDeferredTraceRefs(traceIDs []string) ([]string, []string, error) {
	paths := make([]string, 0, len(traceIDs))
	sessions := make([]string, 0, len(traceIDs))
	for _, chunk := range chunkStrings(traceIDs, storeSQLParamChunk) {
		rows, err := s.db.Query(`
			SELECT path, session_id
			FROM logs
			WHERE trace_id IN (`+placeholders(len(chunk))+`)
		`, stringArgs(chunk)...)
		if err != nil {
			return nil, nil, err
		}
		for rows.Next() {
			var path, sessionID string
			if err := rows.Scan(&path, &sessionID); err != nil {
				rows.Close()
				return nil, nil, err
			}
			paths = append(paths, path)
			sessions = append(sessions, sessionID)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, nil, err
		}
		rows.Close()
	}
	return paths, sessions, nil
}

func (s *Store) refreshOverviewMetricBuckets(paths []string) error {
	paths = dedupeNonEmptyStrings(paths)
	if len(paths) == 0 {
		return nil
	}

	contributions := map[string]overviewMetricContribution{}
	previous := map[string]overviewMetricContribution{}
	for _, chunk := range chunkStrings(paths, storeSQLParamChunk) {
		args := stringArgs(chunk)

		rows, err := s.db.Query(`
			SELECT path, recorded_at, status_code, error_text, total_tokens, ttft_ms, duration_ms, is_stream
			FROM logs
			WHERE path IN (`+placeholders(len(chunk))+`) AND `+clientVisibleLogClause("")+`
		`, args...)
		if err != nil {
			return err
		}
		for rows.Next() {
			contribution, err := s.scanOverviewMetricContribution(rows)
			if err != nil {
				rows.Close()
				return err
			}
			contributions[contribution.Path] = contribution
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()

		rows, err = s.db.Query(`
			SELECT path, bucket_start, bucket_size_seconds, request_count, success_request, failed_request,
				total_tokens, ttft_sum, ttft_count, duration_sum, duration_count, stream_count
			FROM overview_metric_bucket_members
			WHERE path IN (`+placeholders(len(chunk))+`)
		`, args...)
		if err != nil {
			return err
		}
		var bucketStart any
		for rows.Next() {
			var member overviewMetricContribution
			if err := rows.Scan(
				&member.Path,
				&bucketStart,
				&member.BucketSizeSeconds,
				&member.RequestCount,
				&member.SuccessRequest,
				&member.FailedRequest,
				&member.TotalTokens,
				&member.TTFTSum,
				&member.TTFTCount,
				&member.DurationSum,
				&member.DurationCount,
				&member.StreamCount,
			); err != nil {
				rows.Close()
				return err
			}
			parsed, err := timeParseValue(bucketStart)
			if err != nil {
				rows.Close()
				return err
			}
			member.BucketStart = parsed.UTC()
			previous[member.Path] = member
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	deltas := map[string]overviewMetricContribution{}
	for _, member := range previous {
		member.RequestCount = -member.RequestCount
		member.SuccessRequest = -member.SuccessRequest
		member.FailedRequest = -member.FailedRequest
		member.TotalTokens = -member.TotalTokens
		member.TTFTSum = -member.TTFTSum
		member.TTFTCount = -member.TTFTCount
		member.DurationSum = -member.DurationSum
		member.DurationCount = -member.DurationCount
		member.StreamCount = -member.StreamCount
		addOverviewMetricContributionDelta(deltas, member)
	}
	for _, contribution := range contributions {
		addOverviewMetricContributionDelta(deltas, contribution)
	}
	// Deterministic write order keeps concurrent rebuilds and test failures
	// reproducible; the deltas are independent additions either way.
	for _, key := range sortedKeys(deltas) {
		if err := s.addOverviewMetricBucketTx(tx, deltas[key]); err != nil {
			return err
		}
	}
	for _, chunk := range chunkStrings(paths, storeSQLParamChunk) {
		if _, err := s.execTx(tx, `DELETE FROM overview_metric_bucket_members WHERE path IN (`+placeholders(len(chunk))+`)`, stringArgs(chunk)...); err != nil {
			return err
		}
	}
	if err := s.insertOverviewMetricMembersTx(tx, contributions); err != nil {
		return err
	}
	return tx.Commit()
}

func addOverviewMetricContributionDelta(deltas map[string]overviewMetricContribution, contribution overviewMetricContribution) {
	key := overviewMetricBucketKey(contribution)
	current, ok := deltas[key]
	if !ok {
		current = overviewMetricContribution{
			BucketStart:       contribution.BucketStart.UTC(),
			BucketSizeSeconds: contribution.BucketSizeSeconds,
		}
	}
	current.RequestCount += contribution.RequestCount
	current.SuccessRequest += contribution.SuccessRequest
	current.FailedRequest += contribution.FailedRequest
	current.TotalTokens += contribution.TotalTokens
	current.TTFTSum += contribution.TTFTSum
	current.TTFTCount += contribution.TTFTCount
	current.DurationSum += contribution.DurationSum
	current.DurationCount += contribution.DurationCount
	current.StreamCount += contribution.StreamCount
	deltas[key] = current
}

func dedupeNonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func chunkStrings(values []string, size int) [][]string {
	if size <= 0 || len(values) == 0 {
		return nil
	}
	out := make([][]string, 0, (len(values)+size-1)/size)
	for start := 0; start < len(values); start += size {
		end := start + size
		if end > len(values) {
			end = len(values)
		}
		out = append(out, values[start:end])
	}
	return out
}

func stringArgs(values []string) []any {
	args := make([]any, 0, len(values))
	for _, value := range values {
		args = append(args, value)
	}
	return args
}

const timeLayout = "2006-01-02T15:04:05.999999999Z07:00"

func (s *Store) Sync() error {
	s.shared.syncMu.Lock()
	defer s.shared.syncMu.Unlock()

	freshness, err := s.loadFreshness()
	if err != nil {
		return err
	}

	walkErr := filepath.Walk(s.outputDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if path == s.dbPath || strings.HasSuffix(path, "-wal") || strings.HasSuffix(path, "-shm") {
			return nil
		}
		if !strings.HasSuffix(info.Name(), ".http") {
			return nil
		}

		if record, ok := freshness[path]; ok && record.modTimeNs == info.ModTime().UnixNano() && record.fileSize == info.Size() {
			return nil
		}

		inputs, err := readCassetteIndexInputs(path)
		if err != nil {
			if cassetteIsFragment(path, err) {
				return nil
			}
			return fmt.Errorf("index %s: %w", path, err)
		}

		return s.upsertLogWithGrouping(path, inputs.Parsed.Header, inputs.Grouping)
	})
	// The index rows written before a walk failure are already committed, so the
	// deferred derived refreshes run either way; a derived-table failure is
	// reported and never masks the walk error.
	s.flushDerivedRefresh()
	return walkErr
}

type freshnessRecord struct {
	modTimeNs int64
	fileSize  int64
}

func (s *Store) loadFreshness() (map[string]freshnessRecord, error) {
	rows, err := s.db.Query(`SELECT path, mod_time_ns, file_size FROM logs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	freshness := map[string]freshnessRecord{}
	for rows.Next() {
		var (
			path      string
			modTimeNs int64
			fileSize  int64
		)
		if err := rows.Scan(&path, &modTimeNs, &fileSize); err != nil {
			return nil, err
		}
		freshness[path] = freshnessRecord{modTimeNs: modTimeNs, fileSize: fileSize}
	}
	return freshness, rows.Err()
}

// httpRequestMethodPrefixes are the request lines a cassette that was written before
// its prelude can start with. Such a file is an in-progress recording, not a corrupt
// one, so indexers skip it instead of failing the walk.
var httpRequestMethodPrefixes = [][]byte{
	[]byte("GET "),
	[]byte("POST "),
	[]byte("PUT "),
	[]byte("PATCH "),
	[]byte("DELETE "),
	[]byte("HEAD "),
	[]byte("OPTIONS "),
}

// maxRequestHeaderBlock bounds the request header block a reader will allocate from
// the file's own metadata. A real header block is a few hundred bytes; the bound stops
// a corrupt ReqHeaderLen claiming an arbitrary size.
const maxRequestHeaderBlock = 1 << 20

// cassetteIndexInputs is what indexing one cassette needs from it: the prelude and the
// grouping identifiers derived from the request header block.
type cassetteIndexInputs struct {
	Parsed   *recordfile.ParsedPrelude
	Grouping GroupingInfo
}

// readCassetteIndexInputs reads the prelude of the cassette at path and the request
// header block the grouping identifiers come from, without reading the record.
//
// Both index entry points used to read the whole file for this. A cassette is as large
// as the response body it holds, so on a corpus of recordings that made indexing cost
// grow with the bytes stored rather than with the number of traces.
//
// Grouping deliberately comes from the request header block alone, which is what the
// recorder uses when it indexes a recording as it finalises it. Deriving it from the
// whole request instead made the result depend on which path indexed the row, because
// the header parser keeps scanning past the blank line and will read a body line as a
// header if it looks like one.
func readCassetteIndexInputs(path string) (cassetteIndexInputs, error) {
	parsed, err := recordfile.ReadPreludeFile(path)
	if err != nil {
		return cassetteIndexInputs{}, err
	}

	headerLen := parsed.Header.Layout.ReqHeaderLen
	if headerLen <= 0 {
		return cassetteIndexInputs{Parsed: parsed}, nil
	}
	if headerLen > maxRequestHeaderBlock {
		headerLen = maxRequestHeaderBlock
	}

	f, err := os.Open(path)
	if err != nil {
		return cassetteIndexInputs{}, err
	}
	defer f.Close()
	if _, err := f.Seek(parsed.PayloadOffset, io.SeekStart); err != nil {
		return cassetteIndexInputs{}, err
	}

	// A record that is cut short is indexed from the bytes that are there, which is
	// what extracting a section and clamping it to the file length used to do.
	header := make([]byte, headerLen)
	n, readErr := io.ReadFull(f, header)
	if readErr != nil && readErr != io.ErrUnexpectedEOF && readErr != io.EOF {
		return cassetteIndexInputs{}, readErr
	}

	grouping, err := ExtractGroupingInfoFromRequestHeaders(header[:n])
	if err != nil {
		return cassetteIndexInputs{}, err
	}
	return cassetteIndexInputs{Parsed: parsed, Grouping: grouping}, nil
}

// cassetteIsFragment reports whether a cassette that failed to parse is a fragment an
// indexer skips rather than an error: a blank file, a bare HTTP record whose prelude has
// not been written yet, or a prelude recordfile has already classified as unusable. It
// reads a bounded prefix for that decision rather than the whole file.
func cassetteIsFragment(path string, cause error) bool {
	if cause == nil {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	head := make([]byte, 1024)
	n, _ := f.Read(head)
	if n <= 0 {
		return true
	}
	trimmed := bytes.TrimSpace(head[:n])
	if len(trimmed) == 0 {
		return true
	}

	if errors.Is(cause, recordfile.ErrUnusablePrelude) {
		return true
	}

	for _, method := range httpRequestMethodPrefixes {
		if bytes.HasPrefix(trimmed, method) {
			return true
		}
	}
	return false
}

func (s *Store) Reset() error {
	_, err := s.client.TraceLog.Delete().Exec(context.Background())
	return err
}

func (s *Store) Rebuild() (int, error) {
	if err := s.Reset(); err != nil {
		return 0, err
	}
	if err := s.Sync(); err != nil {
		return 0, err
	}

	count, err := s.client.TraceLog.Query().Count(context.Background())
	if err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) lookupOrCreateTraceID(path string) (string, error) {
	traceID, err := s.client.TraceLog.Query().
		Where(tracelog.IDEQ(path)).
		Select(tracelog.FieldTraceID).
		String(context.Background())
	switch {
	case err == nil && traceID != "":
		return traceID, nil
	case err == nil:
		return uuid.NewString(), nil
	case dao.IsNotFound(err):
		return uuid.NewString(), nil
	default:
		return "", err
	}
}

func (s *Store) ListRecent(limit int) ([]LogEntry, error) {
	rows, err := s.client.TraceLog.Query().
		Order(tracelog.ByRecordedAt(entsql.OrderDesc())).
		Limit(limit).
		All(context.Background())
	if err != nil {
		return nil, err
	}

	entries := make([]LogEntry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, logEntryFromTraceLog(row))
	}
	return entries, nil
}

func (s *Store) ListPage(page int, pageSize int, filter ListFilter) (ListPageResult, error) {
	return s.listPage(page, pageSize, filter, true)
}

func (s *Store) ListRoutingPage(page int, pageSize int, filter ListFilter) (ListPageResult, error) {
	return s.listPage(page, pageSize, filter, false)
}

func (s *Store) listPage(page int, pageSize int, filter ListFilter, clientVisibleOnly bool) (ListPageResult, error) {
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}

	ctx := context.Background()
	predicates := buildTraceLogPredicates(filter)
	if clientVisibleOnly {
		predicates = append(predicates, clientVisibleTraceLogPredicate())
	}
	total, err := s.client.TraceLog.Query().Where(predicates...).Count(ctx)
	if err != nil {
		return ListPageResult{}, err
	}

	offset := (page - 1) * pageSize
	rows, err := s.client.TraceLog.Query().
		Where(predicates...).
		Order(tracelog.ByRecordedAt(entsql.OrderDesc())).
		Limit(pageSize).
		Offset(offset).
		All(ctx)
	if err != nil {
		return ListPageResult{}, err
	}

	result := ListPageResult{
		Page:     page,
		PageSize: pageSize,
		Total:    total,
	}
	for _, row := range rows {
		result.Items = append(result.Items, logEntryFromTraceLog(row))
	}
	if err := s.populateObservationMetadata(result.Items); err != nil {
		return ListPageResult{}, err
	}
	if total == 0 {
		result.TotalPages = 0
		return result, nil
	}
	result.TotalPages = int(math.Ceil(float64(total) / float64(pageSize)))
	return result, nil
}

func (s *Store) ListTraceIDs(filter ListFilter, limit int) ([]string, error) {
	whereSQL, whereArgs := buildLogFilterClause(filter, "")
	whereSQL = andSQL(whereSQL, clientVisibleLogClause(""))
	if whereSQL == "" {
		whereSQL = "1 = 1"
	}
	query := `
		SELECT trace_id
		FROM logs
		WHERE ` + whereSQL + `
		ORDER BY recorded_at DESC, trace_id DESC
	`
	args := whereArgs
	if limit > 0 {
		query += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
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

// knownTraceIDs reports which of the given trace ids the index holds, in chunks
// the database accepts.
func (s *Store) knownTraceIDs(ctx context.Context, traceIDs []string) (map[string]struct{}, error) {
	known := make(map[string]struct{}, len(traceIDs))
	for _, chunk := range chunkStrings(traceIDs, storeSQLParamChunk) {
		rows, err := s.db.QueryContext(ctx, `
			SELECT trace_id
			FROM logs
			WHERE trace_id IN (`+placeholders(len(chunk))+`)
		`, stringArgs(chunk)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var traceID string
			if err := rows.Scan(&traceID); err != nil {
				rows.Close()
				return nil, err
			}
			known[traceID] = struct{}{}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return known, nil
}

// datasetExampleTraceIDs reports which of the given traces the dataset already
// holds, in chunks the database accepts.
func datasetExampleTraceIDs(ctx context.Context, tx *dao.Tx, datasetID string, traceIDs []string) (map[string]struct{}, error) {
	present := make(map[string]struct{}, len(traceIDs))
	for _, chunk := range chunkStrings(traceIDs, storeSQLParamChunk) {
		rows, err := tx.DatasetExample.Query().
			Where(datasetexample.DatasetIDEQ(datasetID), datasetexample.TraceIDIn(chunk...)).
			Select(datasetexample.FieldTraceID).
			All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			present[row.TraceID] = struct{}{}
		}
	}
	return present, nil
}

func (s *Store) GetByID(traceID string) (LogEntry, error) {
	row, err := s.client.TraceLog.Query().
		Where(tracelog.TraceIDEQ(traceID)).
		Only(context.Background())
	if err != nil {
		if dao.IsNotFound(err) {
			return LogEntry{}, sql.ErrNoRows
		}
		return LogEntry{}, err
	}
	return logEntryFromTraceLog(row), nil
}

// traceLogsByTraceID loads the log rows with the given trace ids in chunks the
// database accepts, keyed by trace id.
func (s *Store) traceLogsByTraceID(ctx context.Context, traceIDs []string) (map[string]LogEntry, error) {
	traces := make(map[string]LogEntry, len(traceIDs))
	for _, chunk := range chunkStrings(dedupeNonEmptyStrings(traceIDs), storeSQLParamChunk) {
		rows, err := s.client.TraceLog.Query().
			Where(tracelog.TraceIDIn(chunk...)).
			All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			traces[row.TraceID] = logEntryFromTraceLog(row)
		}
	}
	return traces, nil
}

// GetByRequestIDs resolves the given request ids to their newest log row in
// chunks the database accepts, keyed by request id. A request id that no row
// carries is absent from the map. The batch form replaces one query per request
// id; the rows are ordered so the first row of a request id is its newest one,
// the same row GetByRequestID returns.
func (s *Store) GetByRequestIDs(requestIDs []string) (map[string]LogEntry, error) {
	out := make(map[string]LogEntry, len(requestIDs))
	for _, chunk := range chunkStrings(dedupeNonEmptyStrings(requestIDs), storeSQLParamChunk) {
		rows, err := s.client.TraceLog.Query().
			Where(tracelog.RequestIDIn(chunk...)).
			Order(
				tracelog.ByRequestID(),
				tracelog.ByRecordedAt(entsql.OrderDesc()),
				tracelog.ByTraceID(entsql.OrderDesc()),
			).
			All(context.Background())
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if _, ok := out[row.RequestID]; ok {
				continue
			}
			out[row.RequestID] = logEntryFromTraceLog(row)
		}
	}
	return out, nil
}

func (s *Store) GetByRequestID(requestID string) (LogEntry, error) {
	row, err := s.client.TraceLog.Query().
		Where(tracelog.RequestIDEQ(requestID)).
		Order(tracelog.ByRecordedAt(entsql.OrderDesc()), tracelog.ByTraceID(entsql.OrderDesc())).
		First(context.Background())
	if err != nil {
		if dao.IsNotFound(err) {
			return LogEntry{}, sql.ErrNoRows
		}
		return LogEntry{}, err
	}
	return logEntryFromTraceLog(row), nil
}

// insertSemanticNodesTx writes a trace's semantic nodes in parameter-bounded
// multi-row statements. The row-by-row loop it replaces issued one INSERT per
// node, and a session reanalysis flattens hundreds of them into one call.
func (s *Store) insertSemanticNodesTx(tx *sql.Tx, traceID string, nodes []observe.FlatSemanticNode, now time.Time) error {
	if len(nodes) == 0 {
		return nil
	}
	// (trace_id, node_id) is unique, so a batch that repeats a node id would fail
	// the whole statement. observationFlatNodes already folds the non-empty ids
	// first-wins, so this is the safety net for the empty ids it keeps: the last
	// one wins and the batch always sends one row per key.
	unique := make([]observe.FlatSemanticNode, 0, len(nodes))
	at := make(map[string]int, len(nodes))
	for _, row := range nodes {
		if index, ok := at[row.Node.ID]; ok {
			unique[index] = row
			continue
		}
		at[row.Node.ID] = len(unique)
		unique = append(unique, row)
	}
	safeTraceID := sqlSafeText(traceID)
	const nodeColumns = 14
	rowsPerStatement := storeSQLParamChunk / nodeColumns
	valueRow := "(" + placeholders(nodeColumns) + ")"
	for start := 0; start < len(unique); start += rowsPerStatement {
		end := start + rowsPerStatement
		if end > len(unique) {
			end = len(unique)
		}
		chunk := unique[start:end]
		values := make([]string, 0, len(chunk))
		args := make([]any, 0, len(chunk)*nodeColumns)
		for _, row := range chunk {
			nodeJSON, err := json.Marshal(row.Node.JSON)
			if err != nil {
				return err
			}
			rawJSON, err := json.Marshal(row.Node.Raw)
			if err != nil {
				return err
			}
			values = append(values, valueRow)
			args = append(args, safeTraceID, sqlSafeText(row.Node.ID), sqlSafeText(row.ParentID), sqlSafeText(row.Node.ProviderType), string(row.Node.NormalizedType), sqlSafeText(row.Node.Role),
				sqlSafeText(row.Node.Path), row.Node.Index, row.Depth, textPreview(row.Node.Text, 240), string(sqlSafeBytes(nodeJSON)), string(sqlSafeBytes(rawJSON)), "", now)
		}
		if _, err := s.execTx(tx, `
			INSERT INTO semantic_nodes (
				trace_id, node_id, parent_node_id, provider_type, normalized_type, role,
				path, node_index, depth, text_preview, json, raw, raw_ref, created_at
			) VALUES `+strings.Join(values, ", "), args...); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ListSemanticNodes(traceID string) ([]observe.FlatSemanticNode, error) {
	rows, err := s.db.Query(`
		SELECT node_id, parent_node_id, provider_type, normalized_type, role, path,
			node_index, depth, text_preview, json, raw
		FROM semantic_nodes
		WHERE trace_id = ?
		ORDER BY depth ASC, node_index ASC, id ASC
	`, traceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []observe.FlatSemanticNode
	for rows.Next() {
		var row observe.FlatSemanticNode
		var normalized string
		var nodeJSON, rawJSON string
		if err := rows.Scan(
			&row.Node.ID,
			&row.ParentID,
			&row.Node.ProviderType,
			&normalized,
			&row.Node.Role,
			&row.Node.Path,
			&row.Node.Index,
			&row.Depth,
			&row.Node.Text,
			&nodeJSON,
			&rawJSON,
		); err != nil {
			return nil, err
		}
		row.Node.ParentID = row.ParentID
		row.Node.NormalizedType = observe.NormalizedType(normalized)
		if nodeJSON != "" && nodeJSON != "null" {
			row.Node.JSON = json.RawMessage(nodeJSON)
		}
		if rawJSON != "" && rawJSON != "null" {
			row.Node.Raw = json.RawMessage(rawJSON)
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func (s *Store) GetTraceExchangeMetadata(traceID string) (recordfile.MetaData, error) {
	var meta recordfile.MetaData
	var logKind, logRole, logParent string
	var logSeq int
	err := s.db.QueryRow(`
		SELECT exchange_kind, exchange_role, parent_exchange_id, sequence_index
		FROM logs
		WHERE trace_id = ?
	`, traceID).Scan(&logKind, &logRole, &logParent, &logSeq)
	if err != nil && err != sql.ErrNoRows {
		return recordfile.MetaData{}, err
	}
	meta.ExchangeKind = logKind
	meta.ExchangeRole = logRole
	meta.ParentExchangeID = logParent
	meta.SequenceIndex = logSeq

	var requestAuditID, responseID, upstreamKind, upstreamRole, upstreamParent sql.NullString
	var upstreamSeq sql.NullInt64
	err = s.db.QueryRow(`
		SELECT request_audit_id, response_id, exchange_kind, exchange_role, parent_exchange_id, sequence_index
		FROM upstream_exchanges
		WHERE trace_id = ?
		ORDER BY started_at DESC, id DESC
		LIMIT 1
	`, traceID).Scan(&requestAuditID, &responseID, &upstreamKind, &upstreamRole, &upstreamParent, &upstreamSeq)
	if err != nil && err != sql.ErrNoRows {
		return recordfile.MetaData{}, err
	}
	if meta.ExchangeKind == "" {
		meta.ExchangeKind = upstreamKind.String
	}
	if meta.ExchangeRole == "" {
		meta.ExchangeRole = upstreamRole.String
	}
	if meta.ParentExchangeID == "" {
		meta.ParentExchangeID = upstreamParent.String
	}
	if meta.SequenceIndex == 0 && upstreamSeq.Valid {
		meta.SequenceIndex = int(upstreamSeq.Int64)
	}
	if requestAuditID.Valid {
		meta.RequestAuditID = requestAuditID.String
	}
	if responseID.Valid {
		meta.ResponseID = responseID.String
	}
	return meta, nil
}

func (s *Store) SaveAnalysisRun(run AnalysisRunRecord) (int64, error) {
	if strings.TrimSpace(run.Kind) == "" {
		return 0, errors.New("save analysis run: kind is required")
	}
	if strings.TrimSpace(run.Analyzer) == "" {
		return 0, errors.New("save analysis run: analyzer is required")
	}
	if strings.TrimSpace(run.Status) == "" {
		run.Status = "completed"
	}
	if run.CreatedAt.IsZero() {
		run.CreatedAt = time.Now().UTC()
	}
	if s.driver == "postgres" {
		var id int64
		err := s.db.QueryRow(`
			INSERT INTO analysis_runs (
				trace_id, session_id, kind, analyzer, analyzer_version, model, input_ref, output_json, status, created_at
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			RETURNING id
		`, run.TraceID, run.SessionID, run.Kind, run.Analyzer, run.AnalyzerVersion, run.Model, run.InputRef, run.OutputJSON, run.Status, run.CreatedAt).Scan(&id)
		if err != nil {
			return 0, err
		}
		run.ID = id
		if isAnalysisRunFailure(run.Status) {
			if _, err := s.UpsertSystemEvent(systemEventForAnalysisFailure(run)); err != nil {
				return 0, err
			}
		}
		return id, nil
	}
	result, err := s.db.Exec(`
		INSERT INTO analysis_runs (
			trace_id, session_id, kind, analyzer, analyzer_version, model, input_ref, output_json, status, created_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, run.TraceID, run.SessionID, run.Kind, run.Analyzer, run.AnalyzerVersion, run.Model, run.InputRef, run.OutputJSON, run.Status, run.CreatedAt)
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	run.ID = id
	if isAnalysisRunFailure(run.Status) {
		if _, err := s.UpsertSystemEvent(systemEventForAnalysisFailure(run)); err != nil {
			return 0, err
		}
	}
	return id, nil
}

func (s *Store) ListAnalysisRuns(sessionID string, traceID string, kind string, limit int) ([]AnalysisRunRecord, error) {
	if limit <= 0 {
		limit = 20
	}
	query := `
		SELECT id, trace_id, session_id, kind, analyzer, analyzer_version, model, input_ref, output_json, status, created_at
		FROM analysis_runs
		WHERE 1 = 1
	`
	var args []any
	if sessionID = strings.TrimSpace(sessionID); sessionID != "" {
		query += ` AND session_id = ?`
		args = append(args, sessionID)
	}
	if traceID = strings.TrimSpace(traceID); traceID != "" {
		query += ` AND trace_id = ?`
		args = append(args, traceID)
	}
	if kind = strings.TrimSpace(kind); kind != "" {
		query += ` AND kind = ?`
		args = append(args, kind)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []AnalysisRunRecord
	for rows.Next() {
		var run AnalysisRunRecord
		var createdAt any
		if err := rows.Scan(&run.ID, &run.TraceID, &run.SessionID, &run.Kind, &run.Analyzer, &run.AnalyzerVersion,
			&run.Model, &run.InputRef, &run.OutputJSON, &run.Status, &createdAt); err != nil {
			return nil, err
		}
		if run.CreatedAt, err = timeParseValue(createdAt); err != nil {
			return nil, err
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

func (s *Store) ListChildExchangesForEntries(parents []LogEntry) (map[string][]LogEntry, error) {
	out := make(map[string][]LogEntry, len(parents))
	if len(parents) == 0 {
		return out, nil
	}

	parentByAudit := make(map[string]string, len(parents))
	parentByExchange := make(map[string]string, len(parents))
	parentByResponse := make(map[string]string, len(parents))
	var auditArgs []any
	var exchangeArgs []any
	var responseArgs []any
	for _, parent := range parents {
		if auditID := strings.TrimSpace(parent.Header.Meta.RequestAuditID); auditID != "" {
			parentByAudit[auditID] = parent.ID
			auditArgs = append(auditArgs, auditID)
		}
		if responseID := strings.TrimSpace(parent.Header.Meta.ResponseID); responseID != "" {
			parentByResponse[responseID] = parent.ID
			responseArgs = append(responseArgs, responseID)
		}
		if exchangeID := strings.TrimSpace(parent.Header.Meta.ExchangeID); exchangeID != "" {
			parentByExchange[exchangeID] = parent.ID
			exchangeArgs = append(exchangeArgs, exchangeID)
		}
	}
	if len(auditArgs) == 0 && len(exchangeArgs) == 0 && len(responseArgs) == 0 {
		return out, nil
	}

	var clauses []string
	var args []any
	if len(auditArgs) > 0 {
		clauses = append(clauses, `request_audit_id IN (`+placeholders(len(auditArgs))+`)`)
		args = append(args, auditArgs...)
	}
	if len(exchangeArgs) > 0 {
		clauses = append(clauses, `parent_exchange_id IN (`+placeholders(len(exchangeArgs))+`)`)
		args = append(args, exchangeArgs...)
	}
	if len(responseArgs) > 0 {
		clauses = append(clauses, `response_id IN (`+placeholders(len(responseArgs))+`)`)
		args = append(args, responseArgs...)
	}
	rows, err := s.db.Query(`
		SELECT
			trace_id, path, version, request_id, recorded_at, model, provider, operation, endpoint, url, method, status_code,
			duration_ms, ttft_ms, client_ip, content_length, error_text,
			prompt_tokens, completion_tokens, total_tokens, cached_tokens,
			req_header_len, req_body_len, res_header_len, res_body_len, is_stream,
			session_id, session_source, window_id, client_request_id,
			request_audit_id, response_id,
			exchange_id, exchange_kind, exchange_role, parent_exchange_id, sequence_index,
			selected_upstream_id, selected_upstream_base_url, selected_upstream_provider_preset,
			routing_policy, routing_score, routing_candidate_count, routing_failure_reason
		FROM logs
		WHERE exchange_kind = 'model' AND (`+strings.Join(clauses, " OR ")+`)
		ORDER BY sequence_index ASC, recorded_at ASC, trace_id ASC
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		entry, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		parentID := parentByAudit[strings.TrimSpace(entry.Header.Meta.RequestAuditID)]
		if parentID == "" {
			parentID = parentByResponse[strings.TrimSpace(entry.Header.Meta.ResponseID)]
		}
		if parentID == "" {
			parentID = parentByExchange[strings.TrimSpace(entry.Header.Meta.ParentExchangeID)]
		}
		if parentID == "" || parentID == entry.ID {
			continue
		}
		out[parentID] = append(out[parentID], entry)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, children := range out {
		if err := s.populateObservationMetadata(children); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Stats summarizes the traces the filter selects, the same rows ListPage and
// ListTraceIDs return for that filter, so the monitor can show one set of
// numbers next to its list.
//
// It is a single aggregate pass. The monitor list page asks for it on every
// render, so the previous four separate aggregates (total, success, mean TTFT,
// token sum) over the same rows were four times the scan cost, and the result is
// cached per filter for statsCacheTTL.
func (s *Store) Stats(filter ListFilter) (Stats, error) {
	whereSQL, whereArgs := buildLogFilterClause(filter, "")
	whereSQL = andSQL(whereSQL, clientVisibleLogClause(""))
	if whereSQL == "" {
		whereSQL = "1 = 1"
	}
	cacheKey := fmt.Sprintf("%+v", filter)
	if cached, ok := s.cachedStats(cacheKey); ok {
		return cached, nil
	}
	var (
		total        int
		successCount int
		avgTTFT      float64
		totalTokens  int
	)
	if err := s.db.QueryRow(`
		SELECT
			COUNT(*) AS total_request,
			COALESCE(SUM(CASE WHEN status_code >= 200 AND status_code < 300 THEN 1 ELSE 0 END), 0) AS success_request,
			COALESCE(AVG(CASE WHEN status_code >= 200 AND status_code < 300 THEN ttft_ms END), 0) AS avg_ttft,
			COALESCE(SUM(CASE WHEN status_code >= 200 AND status_code < 300 THEN total_tokens ELSE 0 END), 0) AS total_tokens
		FROM logs
		WHERE `+whereSQL, whereArgs...).Scan(&total, &successCount, &avgTTFT, &totalTokens); err != nil {
		return Stats{}, err
	}

	stats := Stats{
		TotalRequest:   total,
		SuccessRequest: successCount,
		FailedRequest:  total - successCount,
	}
	if total > 0 {
		stats.SuccessRate = 100.0 * float64(successCount) / float64(total)
	}
	if successCount > 0 {
		stats.AvgTTFT = int(math.Round(avgTTFT))
		stats.TotalTokens = totalTokens
	}
	s.storeCachedStats(cacheKey, stats)
	return stats, nil
}

func (s *Store) cachedStats(key string) (Stats, bool) {
	if s == nil || s.shared == nil {
		return Stats{}, false
	}
	shared := s.shared
	shared.statsMu.Lock()
	defer shared.statsMu.Unlock()
	if shared.statsKey != key || time.Since(shared.statsAt) >= statsCacheTTL {
		return Stats{}, false
	}
	return shared.statsValue, true
}

func (s *Store) storeCachedStats(key string, stats Stats) {
	if s == nil || s.shared == nil {
		return
	}
	shared := s.shared
	shared.statsMu.Lock()
	shared.statsKey = key
	shared.statsValue = stats
	shared.statsAt = time.Now()
	shared.statsMu.Unlock()
}

// invalidateStatsCache drops the cached aggregate after a log write, so the
// monitor never shows numbers from before the write.
func (s *Store) invalidateStatsCache() {
	if s == nil || s.shared == nil {
		return
	}
	shared := s.shared
	shared.statsMu.Lock()
	shared.statsKey = ""
	shared.statsValue = Stats{}
	shared.statsAt = time.Time{}
	shared.statsMu.Unlock()
}

func (s *Store) overviewPercentile(column string, whereSQL string, whereArgs []any, percentile float64, count int) (int64, error) {
	if count == 0 {
		return 0, nil
	}
	offset := int(math.Ceil(float64(count)*percentile)) - 1
	if offset < 0 {
		offset = 0
	}
	queryArgs := append([]any{}, whereArgs...)
	queryArgs = append(queryArgs, offset)
	var value int64
	if err := s.db.QueryRow(`
		SELECT `+column+`
		FROM logs
		WHERE `+whereSQL+` AND `+column+` > 0
		ORDER BY `+column+` ASC
		LIMIT 1 OFFSET ?
	`, queryArgs...).Scan(&value); err != nil {
		return 0, err
	}
	return value, nil
}

func logEntryFromTraceLog(row *dao.TraceLog) LogEntry {
	entry := LogEntry{
		ID:              row.TraceID,
		LogPath:         row.ID,
		SessionID:       row.SessionID,
		SessionSource:   row.SessionSource,
		WindowID:        row.WindowID,
		ClientRequestID: row.ClientRequestID,
	}
	entry.Header.Version = row.Version
	entry.Header.Meta.RequestID = row.RequestID
	entry.Header.Meta.Time = row.RecordedAt
	entry.Header.Meta.Model = row.Model
	entry.Header.Meta.Provider = row.Provider
	entry.Header.Meta.Operation = row.Operation
	entry.Header.Meta.Endpoint = row.Endpoint
	entry.Header.Meta.URL = row.URL
	entry.Header.Meta.Method = row.Method
	entry.Header.Meta.StatusCode = row.StatusCode
	entry.Header.Meta.DurationMs = row.DurationMs
	entry.Header.Meta.TTFTMs = row.TtftMs
	entry.Header.Meta.ClientIP = row.ClientIP
	entry.Header.Meta.ContentLength = row.ContentLength
	entry.Header.Meta.Error = row.ErrorText
	entry.Header.Meta.RequestAuditID = row.RequestAuditID
	entry.Header.Meta.ResponseID = row.ResponseID
	entry.Header.Meta.ExchangeID = row.ExchangeID
	entry.Header.Meta.ExchangeKind = row.ExchangeKind
	entry.Header.Meta.ExchangeRole = row.ExchangeRole
	entry.Header.Meta.ParentExchangeID = row.ParentExchangeID
	entry.Header.Meta.SequenceIndex = row.SequenceIndex
	entry.Header.Meta.SelectedUpstreamID = row.SelectedUpstreamID
	entry.Header.Meta.SelectedUpstreamBaseURL = row.SelectedUpstreamBaseURL
	entry.Header.Meta.SelectedUpstreamProviderPreset = row.SelectedUpstreamProviderPreset
	entry.Header.Meta.RoutingPolicy = row.RoutingPolicy
	entry.Header.Meta.RoutingScore = row.RoutingScore
	entry.Header.Meta.RoutingCandidateCount = row.RoutingCandidateCount
	entry.Header.Meta.RoutingFailureReason = row.RoutingFailureReason
	entry.Header.Usage.PromptTokens = row.PromptTokens
	entry.Header.Usage.CompletionTokens = row.CompletionTokens
	entry.Header.Usage.TotalTokens = row.TotalTokens
	if row.CachedTokens > 0 {
		entry.Header.Usage.PromptTokenDetails = &recordfile.PromptTokenDetails{CachedTokens: row.CachedTokens}
	}
	entry.Header.Layout.ReqHeaderLen = row.ReqHeaderLen
	entry.Header.Layout.ReqBodyLen = row.ReqBodyLen
	entry.Header.Layout.ResHeaderLen = row.ResHeaderLen
	entry.Header.Layout.ResBodyLen = row.ResBodyLen
	entry.Header.Layout.IsStream = row.IsStream
	return entry
}

func upstreamTargetRecordFromEnt(row *dao.UpstreamTarget) UpstreamTargetRecord {
	record := UpstreamTargetRecord{
		ID:                row.ID,
		BaseURL:           row.BaseURL,
		ProviderPreset:    row.ProviderPreset,
		ProtocolFamily:    row.ProtocolFamily,
		RoutingProfile:    row.RoutingProfile,
		Enabled:           row.Enabled,
		Priority:          row.Priority,
		Weight:            row.Weight,
		CapacityHint:      row.CapacityHint,
		LastRefreshStatus: row.LastRefreshStatus,
		LastRefreshError:  row.LastRefreshError,
	}
	if row.LastRefreshAt != nil {
		record.LastRefreshAt = *row.LastRefreshAt
	}
	return record
}

func upstreamModelRecordFromEnt(row *dao.UpstreamModel) UpstreamModelRecord {
	return UpstreamModelRecord{
		UpstreamID: row.UpstreamID,
		Model:      row.Model,
		Source:     row.Source,
		SeenAt:     row.SeenAt,
	}
}

func modelCatalogRecordFromEnt(row *dao.ModelCatalog) ModelCatalogRecord {
	record := ModelCatalogRecord{
		Model:       row.ID,
		DisplayName: row.DisplayName,
		Family:      row.Family,
		Vendor:      row.Vendor,
		Description: row.Description,
		TagsJSON:    row.TagsJSON,
		FirstSeenAt: row.FirstSeenAt,
		LastSeenAt:  row.LastSeenAt,
	}
	if row.LastUsedAt != nil {
		record.LastUsedAt = *row.LastUsedAt
	}
	return record
}

func defaultJSON(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}

func datasetRecordFromEnt(row *dao.Dataset, exampleCount int) DatasetRecord {
	return DatasetRecord{
		ID:           row.ID,
		Name:         row.Name,
		Description:  row.Description,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
		ExampleCount: exampleCount,
	}
}

func evalRunRecordFromEnt(row *dao.EvalRun) EvalRunRecord {
	return EvalRunRecord{
		ID:           row.ID,
		DatasetID:    row.DatasetID,
		SourceType:   row.SourceType,
		SourceID:     row.SourceID,
		EvaluatorSet: row.EvaluatorSet,
		CreatedAt:    row.CreatedAt,
		CompletedAt:  row.CompletedAt,
		TraceCount:   row.TraceCount,
		ScoreCount:   row.ScoreCount,
		PassCount:    row.PassCount,
		FailCount:    row.FailCount,
	}
}

func scoreRecordFromEnt(row *dao.Score) ScoreRecord {
	return ScoreRecord{
		ID:           row.ID,
		TraceID:      row.TraceID,
		SessionID:    row.SessionID,
		DatasetID:    row.DatasetID,
		EvalRunID:    row.EvalRunID,
		EvaluatorKey: row.EvaluatorKey,
		Value:        row.Value,
		Status:       row.Status,
		Label:        row.Label,
		Explanation:  row.Explanation,
		CreatedAt:    row.CreatedAt,
	}
}

func experimentRunRecordFromEnt(row *dao.ExperimentRun) ExperimentRunRecord {
	return ExperimentRunRecord{
		ID:                  row.ID,
		Name:                row.Name,
		Description:         row.Description,
		BaselineEvalRunID:   row.BaselineEvalRunID,
		CandidateEvalRunID:  row.CandidateEvalRunID,
		CreatedAt:           row.CreatedAt,
		BaselineScoreCount:  row.BaselineScoreCount,
		CandidateScoreCount: row.CandidateScoreCount,
		BaselinePassRate:    row.BaselinePassRate,
		CandidatePassRate:   row.CandidatePassRate,
		PassRateDelta:       row.PassRateDelta,
		MatchedScoreCount:   row.MatchedScoreCount,
		ImprovementCount:    row.ImprovementCount,
		RegressionCount:     row.RegressionCount,
	}
}

func scanEntry(scanner interface {
	Scan(dest ...any) error
}) (LogEntry, error) {
	var (
		entry        LogEntry
		recordedAt   any
		errorText    string
		cached       int
		isStream     any
		routingScore float64
	)

	err := scanner.Scan(
		&entry.ID,
		&entry.LogPath,
		&entry.Header.Version,
		&entry.Header.Meta.RequestID,
		&recordedAt,
		&entry.Header.Meta.Model,
		&entry.Header.Meta.Provider,
		&entry.Header.Meta.Operation,
		&entry.Header.Meta.Endpoint,
		&entry.Header.Meta.URL,
		&entry.Header.Meta.Method,
		&entry.Header.Meta.StatusCode,
		&entry.Header.Meta.DurationMs,
		&entry.Header.Meta.TTFTMs,
		&entry.Header.Meta.ClientIP,
		&entry.Header.Meta.ContentLength,
		&errorText,
		&entry.Header.Usage.PromptTokens,
		&entry.Header.Usage.CompletionTokens,
		&entry.Header.Usage.TotalTokens,
		&cached,
		&entry.Header.Layout.ReqHeaderLen,
		&entry.Header.Layout.ReqBodyLen,
		&entry.Header.Layout.ResHeaderLen,
		&entry.Header.Layout.ResBodyLen,
		&isStream,
		&entry.SessionID,
		&entry.SessionSource,
		&entry.WindowID,
		&entry.ClientRequestID,
		&entry.Header.Meta.RequestAuditID,
		&entry.Header.Meta.ResponseID,
		&entry.Header.Meta.ExchangeID,
		&entry.Header.Meta.ExchangeKind,
		&entry.Header.Meta.ExchangeRole,
		&entry.Header.Meta.ParentExchangeID,
		&entry.Header.Meta.SequenceIndex,
		&entry.Header.Meta.SelectedUpstreamID,
		&entry.Header.Meta.SelectedUpstreamBaseURL,
		&entry.Header.Meta.SelectedUpstreamProviderPreset,
		&entry.Header.Meta.RoutingPolicy,
		&routingScore,
		&entry.Header.Meta.RoutingCandidateCount,
		&entry.Header.Meta.RoutingFailureReason,
	)
	if err != nil {
		return LogEntry{}, err
	}

	entry.Header.Meta.Time, err = timeParseValue(recordedAt)
	if err != nil {
		return LogEntry{}, err
	}
	entry.Header.Meta.Error = errorText
	entry.Header.Meta.RoutingScore = routingScore
	entry.Header.Layout.IsStream = boolValue(isStream)
	if cached > 0 {
		entry.Header.Usage.PromptTokenDetails = &recordfile.PromptTokenDetails{CachedTokens: cached}
	}

	return entry, nil
}

func timeParse(v string) (time.Time, error) {
	for _, layout := range []string{
		timeLayout,
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999 -0700 MST",
		"2006-01-02 15:04:05 -0700 MST",
	} {
		if parsed, err := time.Parse(layout, v); err == nil {
			return parsed, nil
		}
	}
	return time.Parse(timeLayout, v)
}

func timeParseValue(v any) (time.Time, error) {
	switch value := v.(type) {
	case time.Time:
		return value, nil
	case sql.NullTime:
		if !value.Valid {
			return time.Time{}, nil
		}
		return value.Time, nil
	case sql.NullString:
		if !value.Valid || strings.TrimSpace(value.String) == "" {
			return time.Time{}, nil
		}
		return timeParse(value.String)
	case string:
		return timeParse(value)
	case []byte:
		return timeParse(string(value))
	case nil:
		return time.Time{}, nil
	default:
		return timeParse(fmt.Sprint(value))
	}
}

func timeParseNullableValue(v any) (time.Time, error) {
	switch value := v.(type) {
	case nil:
		return time.Time{}, nil
	case sql.NullTime:
		if !value.Valid {
			return time.Time{}, nil
		}
		return value.Time, nil
	case sql.NullString:
		if !value.Valid || strings.TrimSpace(value.String) == "" {
			return time.Time{}, nil
		}
		return timeParse(value.String)
	case string:
		if strings.TrimSpace(value) == "" {
			return time.Time{}, nil
		}
		return timeParse(value)
	case []byte:
		if strings.TrimSpace(string(value)) == "" {
			return time.Time{}, nil
		}
		return timeParse(string(value))
	default:
		return timeParseValue(v)
	}
}

func nullableTime(v time.Time) any {
	if v.IsZero() {
		return nil
	}
	return v
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func boolValue(v any) bool {
	switch value := v.(type) {
	case bool:
		return value
	case int:
		return value != 0
	case int64:
		return value != 0
	case int32:
		return value != 0
	case []byte:
		return boolValue(string(value))
	case string:
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "1", "t", "true", "yes":
			return true
		default:
			return false
		}
	default:
		return false
	}
}

// ExtractGroupingInfoFromRequestHeaders derives the grouping identifiers from the
// raw request header block alone - the bytes the request line and headers occupy,
// ending at the blank line before the body. The recorder uses it while finalising a
// cassette so it never has to hold a whole recording in memory to compute them.
func ExtractGroupingInfoFromRequestHeaders(headers []byte) (GroupingInfo, error) {
	if len(headers) == 0 {
		return GroupingInfo{}, nil
	}
	return extractGroupingInfoFromRequest(headers)
}

// sanitizeDBText drops bytes that Postgres rejects in a text column. A cassette
// is a byte-for-byte copy of what the upstream and the client exchanged, so a
// recorded header value can contain a NUL byte (0x00) or invalid UTF-8; storing
// it verbatim fails the whole statement, and one such recording must not abort
// the entire index sync. NUL has no valid use in text, and invalid sequences
// carry no information that survives the database, so both are removed.
func sanitizeDBText(value string) string {
	if utf8.ValidString(value) && !strings.ContainsRune(value, 0) {
		return value
	}
	return strings.ToValidUTF8(strings.ReplaceAll(value, "\x00", ""), "")
}

// extractGroupingInfoFromRequest sanitizes the grouping identifiers it derives
// from request headers: they are stored as text and are used as grouping keys.
func extractGroupingInfoFromRequest(reqFull []byte) (GroupingInfo, error) {
	info, err := extractGroupingInfoFromRequestRaw(reqFull)
	if err != nil {
		return info, err
	}
	info.SessionID = sanitizeDBText(info.SessionID)
	info.SessionSource = sanitizeDBText(info.SessionSource)
	info.WindowID = sanitizeDBText(info.WindowID)
	info.ClientRequestID = sanitizeDBText(info.ClientRequestID)
	return info, nil
}

func extractGroupingInfoFromRequestRaw(reqFull []byte) (GroupingInfo, error) {
	headers := parseRawRequestHeaders(reqFull)
	info := GroupingInfo{
		WindowID:        strings.TrimSpace(headers.Get("X-Codex-Window-Id")),
		ClientRequestID: strings.TrimSpace(headers.Get("X-Client-Request-Id")),
	}
	if sessionID := firstHeaderValue(headers, "Session-Id", "Session_id"); sessionID != "" {
		info.SessionID = sessionID
		info.SessionSource = "header.session_id"
		return info, nil
	}

	if sessionID := strings.TrimSpace(headers.Get("X-Claude-Code-Session-Id")); sessionID != "" {
		info.SessionID = sessionID
		info.SessionSource = "header.x_claude_code_session_id"
		return info, nil
	}

	if rawMetadata := strings.TrimSpace(headers.Get("X-Codex-Turn-Metadata")); rawMetadata != "" {
		var metadata struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal([]byte(rawMetadata), &metadata); err == nil && strings.TrimSpace(metadata.SessionID) != "" {
			info.SessionID = strings.TrimSpace(metadata.SessionID)
			info.SessionSource = "header.x_codex_turn_metadata.session_id"
			return info, nil
		}
	}

	if info.WindowID != "" {
		info.SessionID = normalizeWindowSessionID(info.WindowID)
		if info.SessionID != "" {
			info.SessionSource = "header.x_codex_window_id"
			return info, nil
		}
	}

	info.SessionSource = "none"
	return info, nil
}

func parseRawRequestHeaders(reqFull []byte) textproto.MIMEHeader {
	headers := make(textproto.MIMEHeader)
	lines := strings.Split(string(reqFull), "\r\n")
	for idx, line := range lines {
		if idx == 0 || line == "" {
			continue
		}
		name, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		headers.Add(textproto.CanonicalMIMEHeaderKey(strings.TrimSpace(name)), strings.TrimSpace(value))
	}
	return headers
}

func firstHeaderValue(headers textproto.MIMEHeader, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(headers.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func splitProviders(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	seen := map[string]struct{}{}
	var providers []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		providers = append(providers, part)
	}
	sort.Strings(providers)
	return providers
}

func buildTraceLogPredicates(filter ListFilter) []predicate.TraceLog {
	var predicates []predicate.TraceLog
	if provider := strings.TrimSpace(filter.Provider); provider != "" {
		predicates = append(predicates, tracelog.ProviderEqualFold(provider))
	}
	if model := strings.TrimSpace(filter.Model); model != "" {
		predicates = append(predicates, tracelog.ModelContainsFold(model))
	}
	if endpoint := strings.TrimSpace(filter.Endpoint); endpoint != "" {
		predicates = append(predicates, tracelog.EndpointContainsFold(endpoint))
	}
	if upstream := strings.TrimSpace(filter.SelectedUpstream); upstream != "" {
		predicates = append(predicates, tracelog.SelectedUpstreamIDContainsFold(upstream))
	}
	switch strings.ToLower(strings.TrimSpace(filter.ObservationStatus)) {
	case "parsed", "failed", "queued", "running", "unsupported":
		status := strings.ToLower(strings.TrimSpace(filter.ObservationStatus))
		predicates = append(predicates, predicate.TraceLog(func(s *entsql.Selector) {
			obs := entsql.Table("trace_observations")
			job := entsql.Table("parse_jobs")
			obsStatus := entsql.Select(obs.C("trace_id")).
				From(obs).
				Where(entsql.And(
					entsql.ColumnsEQ(obs.C("trace_id"), s.C(tracelog.FieldTraceID)),
					entsql.EQ(obs.C("status"), status),
				))
			jobStatus := entsql.Select(job.C("trace_id")).
				From(job).
				Where(entsql.And(
					entsql.ColumnsEQ(job.C("trace_id"), s.C(tracelog.FieldTraceID)),
					entsql.EQ(job.C("status"), status),
				))
			s.Where(entsql.Or(entsql.Exists(obsStatus), entsql.Exists(jobStatus)))
		}))
	case "unparsed":
		predicates = append(predicates, predicate.TraceLog(func(s *entsql.Selector) {
			obs := entsql.Table("trace_observations")
			sub := entsql.Select(obs.C("trace_id")).
				From(obs).
				Where(entsql.ColumnsEQ(obs.C("trace_id"), s.C(tracelog.FieldTraceID)))
			s.Where(entsql.NotExists(sub))
		}))
	}
	if filter.MissingUsage {
		predicates = append(predicates, tracelog.TotalTokensEQ(0), tracelog.StatusCodeGTE(200), tracelog.StatusCodeLT(300))
	}
	switch strings.ToLower(strings.TrimSpace(filter.Status)) {
	case "success":
		predicates = append(predicates, tracelog.StatusCodeGTE(200), tracelog.StatusCodeLT(300), tracelog.ErrorTextEQ(""))
	case "error":
		predicates = append(predicates, tracelog.Or(tracelog.StatusCodeLT(200), tracelog.StatusCodeGTE(300), tracelog.ErrorTextNEQ("")))
	}
	if filter.MinDurationMs > 0 {
		predicates = append(predicates, tracelog.DurationMsGTE(filter.MinDurationMs))
	}
	if filter.MaxDurationMs > 0 {
		predicates = append(predicates, tracelog.DurationMsLTE(filter.MaxDurationMs))
	}
	if filter.MinTTFTMs > 0 {
		predicates = append(predicates, tracelog.TtftMsGTE(filter.MinTTFTMs))
	}
	if filter.MaxTTFTMs > 0 {
		predicates = append(predicates, tracelog.TtftMsLTE(filter.MaxTTFTMs))
	}
	if filter.MinTokens > 0 {
		predicates = append(predicates, tracelog.TotalTokensGTE(filter.MinTokens))
	}
	if filter.MaxTokens > 0 {
		predicates = append(predicates, tracelog.TotalTokensLTE(filter.MaxTokens))
	}
	if query := strings.TrimSpace(filter.Query); query != "" {
		predicates = append(predicates, tracelog.Or(
			tracelog.SessionIDContainsFold(query),
			tracelog.TraceIDContainsFold(query),
			tracelog.ModelContainsFold(query),
			tracelog.ProviderContainsFold(query),
			tracelog.SelectedUpstreamIDContainsFold(query),
			tracelog.EndpointContainsFold(query),
			tracelog.URLContainsFold(query),
		))
	}
	return predicates
}

func clientVisibleTraceLogPredicate() predicate.TraceLog {
	return predicate.TraceLog(func(s *entsql.Selector) {
		s.Where(entsql.ExprP(`COALESCE(` + s.C(tracelog.FieldExchangeKind) + `, '') IN ('', 'entry', 'proxy')`))
	})
}

func clientVisibleLogClause(alias string) string {
	column := "exchange_kind"
	if alias != "" {
		column = alias + "." + column
	}
	return `COALESCE(` + column + `, '') IN ('', 'entry', 'proxy')`
}

func andSQL(left string, right string) string {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	switch {
	case left == "":
		return right
	case right == "":
		return left
	default:
		return "(" + left + ") AND (" + right + ")"
	}
}

func placeholders(count int) string {
	if count <= 0 {
		return ""
	}
	return strings.TrimRight(strings.Repeat("?,", count), ",")
}

// claimRowLockSQL is the locking clause of a task claim's row-selecting subquery.
// Postgres takes the selected rows with FOR UPDATE SKIP LOCKED, which is what
// makes two claimers pick disjoint jobs; SQLite has no row locks, so its claim
// relies on the database write lock and the status predicate in the outer UPDATE.
func (s *Store) claimRowLockSQL() string {
	if s != nil && s.driver == "postgres" {
		return "\n\t\t\tFOR UPDATE SKIP LOCKED"
	}
	return ""
}

func scanCountItems(rows *sql.Rows) ([]CountItem, error) {
	var out []CountItem
	for rows.Next() {
		var item CountItem
		if err := rows.Scan(&item.Label, &item.Count); err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func dedupeFlatSemanticNodes(nodes []observe.FlatSemanticNode) []observe.FlatSemanticNode {
	if len(nodes) < 2 {
		return nodes
	}
	out := make([]observe.FlatSemanticNode, 0, len(nodes))
	seen := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		if node.Node.ID == "" {
			out = append(out, node)
			continue
		}
		if _, ok := seen[node.Node.ID]; ok {
			continue
		}
		seen[node.Node.ID] = struct{}{}
		out = append(out, node)
	}
	return out
}

func textPreview(text string, limit int) string {
	text = sqlSafeText(text)
	if limit <= 0 || len(text) <= limit {
		return text
	}
	cut := 0
	for idx := range text {
		if idx > limit {
			break
		}
		cut = idx
	}
	if cut == 0 {
		return ""
	}
	return text[:cut]
}

// sqlSafeText makes a value storable in a Postgres text column.
//
// Invalid UTF-8 is replaced with U+FFFD, and so is a NUL byte, because Postgres
// refuses to store one at all: `INSERT ... VALUES (E'a\000b')` fails with
// `invalid byte sequence for encoding "UTF8": 0x00`. A request body or an
// upstream error body can carry a NUL, and the values derived from them - the
// model, the error text, a semantic node's text - then failed the insert that
// records the trace, so the trace was lost rather than stored with one odd
// character.
func sqlSafeText(text string) string {
	text = strings.ToValidUTF8(text, "\uFFFD")
	if strings.IndexByte(text, 0) < 0 {
		return text
	}
	return strings.ReplaceAll(text, "\x00", "\uFFFD")
}

// sqlSafeBytes makes a JSON document storable in a Postgres jsonb column.
//
// A raw NUL is replaced first, then the JSON escape json.Marshal emits for one:
// Postgres rejects that too (`unsupported Unicode escape sequence: \u0000 cannot
// be converted to text`), and replacing only the raw byte missed every NUL that
// reached a jsonb column, because encoding a Go string turns it into `\u0000`
// before the store ever sees the bytes. A literal backslash is escaped as `\\`,
// so a `u0000` is a NUL escape only when the run of backslashes before it is
// odd; an even run means the text after it is literal.
func sqlSafeBytes(data []byte) []byte {
	if len(data) == 0 {
		return data
	}
	data = []byte(sqlSafeText(string(data)))
	out := make([]byte, 0, len(data))
	for i := 0; i < len(data); {
		if data[i] != '\\' {
			out = append(out, data[i])
			i++
			continue
		}
		run := 0
		for i+run < len(data) && data[i+run] == '\\' {
			run++
		}
		if run%2 == 1 && i+run+5 <= len(data) && string(data[i+run:i+run+5]) == "u0000" {
			out = append(out, data[i:i+run]...)
			out = append(out, "uFFFD"...)
			i += run + 5
			continue
		}
		out = append(out, data[i:i+run]...)
		i += run
	}
	return out
}

func buildLogFilterClause(filter ListFilter, alias string) (string, []any) {
	column := func(name string) string {
		if alias == "" {
			return name
		}
		return alias + "." + name
	}
	outerTraceColumn := column("trace_id")
	if alias == "" {
		outerTraceColumn = "logs.trace_id"
	}

	var (
		clauses []string
		args    []any
	)

	if provider := strings.TrimSpace(filter.Provider); provider != "" {
		clauses = append(clauses, `LOWER(`+column("provider")+`) = LOWER(?)`)
		args = append(args, provider)
	}
	if model := strings.TrimSpace(filter.Model); model != "" {
		clauses = append(clauses, `LOWER(`+column("model")+`) LIKE LOWER(?)`)
		args = append(args, "%"+escapeLike(model)+"%")
	}
	if endpoint := strings.TrimSpace(filter.Endpoint); endpoint != "" {
		clauses = append(clauses, `LOWER(`+column("endpoint")+`) LIKE LOWER(?)`)
		args = append(args, "%"+escapeLike(endpoint)+"%")
	}
	if upstream := strings.TrimSpace(filter.SelectedUpstream); upstream != "" {
		clauses = append(clauses, `LOWER(`+column("selected_upstream_id")+`) LIKE LOWER(?)`)
		args = append(args, "%"+escapeLike(upstream)+"%")
	}
	switch strings.ToLower(strings.TrimSpace(filter.ObservationStatus)) {
	case "parsed", "failed", "queued", "running", "unsupported":
		clauses = append(clauses, `(
			EXISTS (SELECT 1 FROM trace_observations o WHERE o.trace_id = `+outerTraceColumn+` AND o.status = ?) OR
			EXISTS (SELECT 1 FROM parse_jobs p WHERE p.trace_id = `+outerTraceColumn+` AND p.status = ?)
		)`)
		status := strings.ToLower(strings.TrimSpace(filter.ObservationStatus))
		args = append(args, status, status)
	case "unparsed":
		clauses = append(clauses, `NOT EXISTS (SELECT 1 FROM trace_observations o WHERE o.trace_id = `+outerTraceColumn+`)`)
	}
	if filter.MissingUsage {
		clauses = append(clauses, `(`+column("total_tokens")+` = 0 AND `+column("status_code")+` >= 200 AND `+column("status_code")+` < 300)`)
	}
	switch strings.ToLower(strings.TrimSpace(filter.Status)) {
	case "success":
		clauses = append(clauses, `(`+column("status_code")+` >= 200 AND `+column("status_code")+` < 300 AND `+column("error_text")+` = '')`)
	case "error":
		clauses = append(clauses, `(`+column("status_code")+` < 200 OR `+column("status_code")+` >= 300 OR `+column("error_text")+` != '')`)
	}
	if filter.MinDurationMs > 0 {
		clauses = append(clauses, column("duration_ms")+` >= ?`)
		args = append(args, filter.MinDurationMs)
	}
	if filter.MaxDurationMs > 0 {
		clauses = append(clauses, column("duration_ms")+` <= ?`)
		args = append(args, filter.MaxDurationMs)
	}
	if filter.MinTTFTMs > 0 {
		clauses = append(clauses, column("ttft_ms")+` >= ?`)
		args = append(args, filter.MinTTFTMs)
	}
	if filter.MaxTTFTMs > 0 {
		clauses = append(clauses, column("ttft_ms")+` <= ?`)
		args = append(args, filter.MaxTTFTMs)
	}
	if filter.MinTokens > 0 {
		clauses = append(clauses, column("total_tokens")+` >= ?`)
		args = append(args, filter.MinTokens)
	}
	if filter.MaxTokens > 0 {
		clauses = append(clauses, column("total_tokens")+` <= ?`)
		args = append(args, filter.MaxTokens)
	}
	if query := strings.TrimSpace(filter.Query); query != "" {
		pattern := "%" + escapeLike(query) + "%"
		clauses = append(clauses, `(
			LOWER(`+column("session_id")+`) LIKE LOWER(?) ESCAPE '\' OR
			LOWER(`+column("trace_id")+`) LIKE LOWER(?) ESCAPE '\' OR
			LOWER(`+column("model")+`) LIKE LOWER(?) ESCAPE '\' OR
			LOWER(`+column("provider")+`) LIKE LOWER(?) ESCAPE '\' OR
			LOWER(`+column("selected_upstream_id")+`) LIKE LOWER(?) ESCAPE '\' OR
			LOWER(`+column("endpoint")+`) LIKE LOWER(?) ESCAPE '\' OR
			LOWER(`+column("url")+`) LIKE LOWER(?) ESCAPE '\'
		)`)
		for range 7 {
			args = append(args, pattern)
		}
	}

	return strings.Join(clauses, " AND "), args
}

func isAnalysisRunFailure(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "completed", "success":
		return false
	default:
		return true
	}
}

func classifySystemTransportError(errorText string, statusCode int) string {
	text := strings.ToLower(strings.TrimSpace(errorText))
	switch {
	case strings.Contains(text, "broken pipe") || strings.Contains(text, "client disconnected") || strings.Contains(text, "connection reset by peer"):
		return "client_disconnect"
	case strings.Contains(text, "timeout") || strings.Contains(text, "timed out") || strings.Contains(text, "deadline exceeded"):
		return "timeout"
	case strings.Contains(text, "goaway"):
		return "http2_goaway"
	case statusCode >= 500:
		return "upstream_5xx"
	default:
		return "transport_error"
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func escapeLike(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	value = strings.ReplaceAll(value, `_`, `\_`)
	return value
}

func normalizePage(page int, pageSize int) (int, int) {
	if page <= 0 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	if pageSize > 200 {
		pageSize = 200
	}
	return page, pageSize
}

func totalPages(total int, pageSize int) int {
	if total <= 0 || pageSize <= 0 {
		return 0
	}
	return int(math.Ceil(float64(total) / float64(pageSize)))
}

func (s *Store) backfillGrouping() error {
	rows, err := s.db.Query(`SELECT path FROM logs WHERE session_source = '' OR session_source = 'none'`)
	if err != nil {
		return err
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return err
		}
		paths = append(paths, path)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, path := range paths {
		inputs, err := readCassetteIndexInputs(path)
		if err != nil {
			if os.IsNotExist(err) || cassetteIsFragment(path, err) {
				continue
			}
			return err
		}
		grouping := inputs.Grouping
		if _, err := s.db.Exec(
			`UPDATE logs SET session_id = ?, session_source = ?, window_id = ?, client_request_id = ? WHERE path = ?`,
			grouping.SessionID,
			grouping.SessionSource,
			grouping.WindowID,
			grouping.ClientRequestID,
			path,
		); err != nil {
			return err
		}
	}
	return nil
}
