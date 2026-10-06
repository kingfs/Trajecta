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
	"time"
	"unicode/utf8"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/kingfs/Trajecta/ent/dao"
	"github.com/kingfs/Trajecta/ent/dao/channelconfig"
	"github.com/kingfs/Trajecta/ent/dao/channelmodel"
	"github.com/kingfs/Trajecta/ent/dao/channelproberun"
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

type SessionSummary struct {
	SessionID      string
	SessionSource  string
	RequestCount   int
	FirstSeen      time.Time
	LastSeen       time.Time
	LastModel      string
	Providers      []string
	SuccessRequest int
	FailedRequest  int
	SuccessRate    float64
	TotalTokens    int
	AvgTTFT        int
	TotalDuration  int64
	StreamCount    int
}

type SessionPageResult struct {
	Items      []SessionSummary
	Total      int
	Page       int
	PageSize   int
	TotalPages int
}

type SessionSummaryRebuildStats struct {
	SessionID      string `json:"session_id,omitempty"`
	CandidateCount int    `json:"candidate_count"`
	ExistingCount  int    `json:"existing_count"`
	WouldDeleteAll bool   `json:"would_delete_all"`
	WouldDeleteOne bool   `json:"would_delete_one"`
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

type ChannelConfigRecord struct {
	ID                 string
	Name               string
	Description        string
	Source             string
	BaseURL            string
	ProviderPreset     string
	APIType            string
	Mode               string
	CapabilitiesJSON   string
	ProtocolFamily     string
	RoutingProfile     string
	APIVersion         string
	Deployment         string
	Project            string
	Location           string
	ModelResource      string
	APIKeyCiphertext   []byte
	APIKeyHint         string
	HeadersJSON        string
	Enabled            bool
	Priority           int
	Weight             float64
	CapacityHint       float64
	ModelDiscovery     string
	AllowUnknownModels bool
	CreatedAt          time.Time
	UpdatedAt          time.Time
	LastProbeAt        time.Time
	LastProbeStatus    string
	LastProbeError     string
}

type ChannelModelRecord struct {
	ChannelID                   string
	Model                       string
	DisplayName                 string
	Source                      string
	Enabled                     bool
	SupportsResponses           *int
	SupportsChatCompletions     *int
	SupportsEmbeddings          *int
	ContextWindow               *int
	MaxOutputTokens             *int
	CompactHistoryItemThreshold *int
	UpstreamModel               string
	ProfileSource               string
	ProfileAdoptionStatus       string
	InputModalitiesJSON         string
	OutputModalitiesJSON        string
	RawModelJSON                string
	FirstSeenAt                 time.Time
	LastSeenAt                  time.Time
	LastProbeAt                 time.Time
}

// ChannelModelProfilePatch describes a partial update of one channel model.
//
// The capability columns are tri-state: a nil pointer leaves the stored value
// untouched, a non-nil pointer pins it, and the matching Clear* flag resets it
// back to "inherit the channel-level capability".
type ChannelModelProfilePatch struct {
	DisplayName                  *string
	Enabled                      *bool
	SupportsResponses            *bool
	ClearSupportsResponses       bool
	SupportsChatCompletions      *bool
	ClearSupportsChatCompletions bool
	SupportsEmbeddings           *bool
	ClearSupportsEmbeddings      bool
	ContextWindow                *int
	MaxOutputTokens              *int
	CompactHistoryItemThreshold  *int
	UpstreamModel                *string
	ProfileSource                *string
	ProfileAdoptionStatus        *string
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

type ChannelProbeRunRecord struct {
	ID                 string
	ChannelID          string
	Status             string
	StartedAt          time.Time
	CompletedAt        time.Time
	DurationMs         int64
	DiscoveredCount    int
	EnabledCount       int
	Endpoint           string
	StatusCode         int
	ErrorText          string
	RequestMetaJSON    string
	ResponseSampleJSON string
}

type UpstreamAnalyticsRecord struct {
	UpstreamID     string
	RequestCount   int
	SuccessRequest int
	FailedRequest  int
	SuccessRate    float64
	TotalTokens    int
	AvgTTFT        int
	LastSeen       time.Time
	Models         []string
	LastModel      string
	RecentErrors   []string
	RecentFailures []UpstreamFailureRecord
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

type SystemEvent struct {
	ID              string
	Fingerprint     string
	Source          string
	Category        string
	Severity        string
	Status          string
	Title           string
	Message         string
	DetailsJSON     json.RawMessage
	TraceID         string
	SessionID       string
	JobID           string
	UpstreamID      string
	Model           string
	OccurrenceCount int
	FirstSeenAt     time.Time
	LastSeenAt      time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
	ReadAt          time.Time
	ResolvedAt      time.Time
}

type SystemEventFilter struct {
	Status   string
	Severity string
	Source   string
	Category string
	Query    string
	Since    time.Time
	After    string
	Page     int
	PageSize int
}

type SystemEventPageResult struct {
	Items      []SystemEvent
	Total      int
	Page       int
	PageSize   int
	TotalPages int
	NextCursor string
	HasMore    bool
}

const systemEventCursorVersion = 1

type systemEventCursor struct {
	Version    int    `json:"v"`
	LastSeenAt string `json:"last_seen_at"`
	ID         string `json:"id"`
}

type systemEventCursorPosition struct {
	LastSeenAt time.Time
	ID         string
}

type SystemEventSummary struct {
	Total      int
	Unread     int
	Critical   int
	Error      int
	Warning    int
	LastSeenAt time.Time
	BySource   []CountItem
	ByCategory []CountItem
}

type SystemEventNotification struct {
	Sequence uint64
	EventID  string
	Status   string
	Severity string
	Source   string
	Category string
	At       time.Time
}

type RoutingFailureAnalytics struct {
	Total    int
	Reasons  []CountItem
	Recent   []RoutingFailureRecord
	Timeline []TimeCountItem
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

type AnalysisJobRecord struct {
	ID          int64
	JobType     string
	TargetType  string
	TargetID    string
	Status      string
	StepsJSON   string
	RequestJSON string
	ResultJSON  string
	LastError   string
	Attempts    int
	CreatedAt   time.Time
	UpdatedAt   time.Time
	StartedAt   time.Time
	FinishedAt  time.Time
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

type ModelCatalogAnalyticsRecord struct {
	Model               string
	DisplayName         string
	ProviderCount       int
	ChannelCount        int
	EnabledChannelCount int
	Summary             UsageSummaryRecord
	Today               UsageSummaryRecord
	Channels            []string
}

type ModelDetailAnalyticsRecord struct {
	Model    ModelCatalogAnalyticsRecord
	Trends   []UsageTrendRecord
	Channels []ChannelModelAnalyticsRecord
}

type ChannelModelAnalyticsRecord struct {
	ChannelID string
	Model     string
	Enabled   bool
	Source    string
	Summary   UsageSummaryRecord
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

type ObservationSummary struct {
	TraceID          string
	Parser           string
	ParserVersion    string
	Status           string
	Provider         string
	Operation        string
	Model            string
	ExchangeKind     string
	ExchangeRole     string
	ParentExchangeID string
	SequenceIndex    int
	RequestAuditID   string
	ResponseID       string
	SummaryJSON      string
	WarningsJSON     string
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type ObservationMetadata struct {
	Status        string
	Parser        string
	ParserVersion string
	UpdatedAt     time.Time
}

type ParseJobRecord struct {
	ID        int64
	TraceID   string
	Status    string
	Attempts  int
	LastError string
	CreatedAt time.Time
	UpdatedAt time.Time
}

type FindingFilter struct {
	Category string
	Severity string
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

func (s *Store) ListChannelConfigs() ([]ChannelConfigRecord, error) {
	rows, err := s.client.ChannelConfig.Query().
		Order(channelconfig.ByPriority(entsql.OrderDesc()), channelconfig.ByID()).
		All(context.Background())
	if err != nil {
		return nil, err
	}

	out := make([]ChannelConfigRecord, 0, len(rows))
	for _, row := range rows {
		record, err := s.channelConfigRecordFromEnt(row)
		if err != nil {
			return nil, err
		}
		out = append(out, record)
	}
	return out, nil
}

func (s *Store) GetChannelConfig(channelID string) (ChannelConfigRecord, error) {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return ChannelConfigRecord{}, errors.New("channel id is required")
	}
	row, err := s.client.ChannelConfig.Get(context.Background(), channelID)
	if err != nil {
		if dao.IsNotFound(err) {
			return ChannelConfigRecord{}, sql.ErrNoRows
		}
		return ChannelConfigRecord{}, err
	}
	return s.channelConfigRecordFromEnt(row)
}

func (s *Store) DeleteChannelConfig(channelID string) error {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return errors.New("channel id is required")
	}

	ctx := context.Background()
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ChannelModel.Delete().Where(channelmodel.ChannelIDEQ(channelID)).Exec(ctx); err != nil {
		return err
	}
	if err := tx.ChannelConfig.DeleteOneID(channelID).Exec(ctx); err != nil {
		if dao.IsNotFound(err) {
			return sql.ErrNoRows
		}
		return err
	}
	return tx.Commit()
}

func (s *Store) UpsertChannelConfig(record ChannelConfigRecord) (ChannelConfigRecord, error) {
	record.ID = strings.TrimSpace(record.ID)
	record.Name = strings.TrimSpace(record.Name)
	record.BaseURL = strings.TrimSpace(record.BaseURL)
	if record.ID == "" {
		record.ID = uuid.NewString()
	}
	if record.Name == "" {
		return ChannelConfigRecord{}, errors.New("channel name is required")
	}
	if record.BaseURL == "" {
		return ChannelConfigRecord{}, errors.New("channel base_url is required")
	}
	if strings.TrimSpace(record.HeadersJSON) == "" {
		record.HeadersJSON = "{}"
	}
	if strings.TrimSpace(record.CapabilitiesJSON) == "" {
		record.CapabilitiesJSON = "{}"
	}
	if strings.TrimSpace(record.ModelDiscovery) == "" {
		record.ModelDiscovery = "list_models"
	}
	if strings.TrimSpace(record.Source) == "" {
		record.Source = "manual"
	}
	if record.Weight == 0 {
		record.Weight = 1
	}
	if record.CapacityHint == 0 {
		record.CapacityHint = 1
	}
	now := time.Now().UTC()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	record.UpdatedAt = now
	storedAPIKey, err := s.encryptSecretBytes(record.APIKeyCiphertext)
	if err != nil {
		return ChannelConfigRecord{}, err
	}
	storedHeaders, err := s.encryptHeadersJSON(record.HeadersJSON)
	if err != nil {
		return ChannelConfigRecord{}, err
	}

	create := s.client.ChannelConfig.Create().
		SetID(record.ID).
		SetName(record.Name).
		SetDescription(strings.TrimSpace(record.Description)).
		SetSource(strings.TrimSpace(record.Source)).
		SetBaseURL(record.BaseURL).
		SetProviderPreset(strings.TrimSpace(record.ProviderPreset)).
		SetAPIType(strings.TrimSpace(record.APIType)).
		SetMode(strings.TrimSpace(record.Mode)).
		SetCapabilitiesJSON(strings.TrimSpace(record.CapabilitiesJSON)).
		SetProtocolFamily(strings.TrimSpace(record.ProtocolFamily)).
		SetRoutingProfile(strings.TrimSpace(record.RoutingProfile)).
		SetAPIVersion(strings.TrimSpace(record.APIVersion)).
		SetDeployment(strings.TrimSpace(record.Deployment)).
		SetProject(strings.TrimSpace(record.Project)).
		SetLocation(strings.TrimSpace(record.Location)).
		SetModelResource(strings.TrimSpace(record.ModelResource)).
		SetAPIKeyHint(strings.TrimSpace(record.APIKeyHint)).
		SetHeadersJSON(storedHeaders).
		SetEnabled(record.Enabled).
		SetPriority(record.Priority).
		SetWeight(record.Weight).
		SetCapacityHint(record.CapacityHint).
		SetModelDiscovery(record.ModelDiscovery).
		SetAllowUnknownModels(record.AllowUnknownModels).
		SetCreatedAt(record.CreatedAt).
		SetUpdatedAt(record.UpdatedAt).
		SetLastProbeStatus(strings.TrimSpace(record.LastProbeStatus)).
		SetLastProbeError(strings.TrimSpace(record.LastProbeError))
	if len(storedAPIKey) > 0 {
		create.SetAPIKeyCiphertext(storedAPIKey)
	}
	if !record.LastProbeAt.IsZero() {
		create.SetLastProbeAt(record.LastProbeAt.UTC())
	}
	if err := create.
		OnConflictColumns(channelconfig.FieldID).
		UpdateNewValues().
		Exec(context.Background()); err != nil {
		return ChannelConfigRecord{}, err
	}
	return s.GetChannelConfig(record.ID)
}

func (s *Store) UpdateChannelProbeStatus(channelID string, probedAt time.Time, status string, errorText string) error {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return errors.New("channel id is required")
	}
	update := s.client.ChannelConfig.UpdateOneID(channelID).
		SetUpdatedAt(time.Now().UTC()).
		SetLastProbeStatus(strings.TrimSpace(status)).
		SetLastProbeError(strings.TrimSpace(errorText))
	if !probedAt.IsZero() {
		update.SetLastProbeAt(probedAt.UTC())
	}
	return update.Exec(context.Background())
}

func (s *Store) ListChannelModels(channelID string, enabledOnly bool) ([]ChannelModelRecord, error) {
	query := s.client.ChannelModel.Query()
	var predicates []predicate.ChannelModel
	if channelID = strings.TrimSpace(channelID); channelID != "" {
		predicates = append(predicates, channelmodel.ChannelIDEQ(channelID))
	}
	if enabledOnly {
		predicates = append(predicates, channelmodel.EnabledEQ(true))
	}
	if len(predicates) > 0 {
		query = query.Where(predicates...)
	}
	rows, err := query.Order(channelmodel.ByChannelID(), channelmodel.ByModel()).All(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]ChannelModelRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, channelModelRecordFromEnt(row))
	}
	return out, nil
}

func (s *Store) ListAdoptedChannelModelProfiles() ([]ChannelModelRecord, error) {
	channelRows, err := s.client.ChannelConfig.Query().
		Where(channelconfig.EnabledEQ(true)).
		All(context.Background())
	if err != nil {
		return nil, err
	}
	enabledChannels := make(map[string]struct{}, len(channelRows))
	for _, row := range channelRows {
		enabledChannels[row.ID] = struct{}{}
	}
	if len(enabledChannels) == 0 {
		return nil, nil
	}

	rows, err := s.client.ChannelModel.Query().
		Where(
			channelmodel.EnabledEQ(true),
			channelmodel.ProfileAdoptionStatusEQ("adopted"),
		).
		Order(channelmodel.ByModel(), channelmodel.ByChannelID()).
		All(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]ChannelModelRecord, 0, len(rows))
	for _, row := range rows {
		if _, ok := enabledChannels[row.ChannelID]; !ok {
			continue
		}
		if row.SupportsChatCompletions != nil && *row.SupportsChatCompletions == 0 {
			continue
		}
		out = append(out, channelModelRecordFromEnt(row))
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

func (s *Store) ReplaceChannelModels(channelID string, records []ChannelModelRecord) error {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return errors.New("channel id is required")
	}

	ctx := context.Background()
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ChannelModel.Delete().Where(channelmodel.ChannelIDEQ(channelID)).Exec(ctx); err != nil {
		return err
	}
	creates := make([]*dao.ChannelModelCreate, 0, len(records))
	now := time.Now().UTC()
	for _, record := range records {
		model := strings.ToLower(strings.TrimSpace(record.Model))
		if model == "" {
			continue
		}
		firstSeenAt := record.FirstSeenAt
		if firstSeenAt.IsZero() {
			firstSeenAt = now
		}
		lastSeenAt := record.LastSeenAt
		if lastSeenAt.IsZero() {
			lastSeenAt = now
		}
		enabled := record.Enabled
		create := tx.ChannelModel.Create().
			SetChannelID(channelID).
			SetModel(model).
			SetDisplayName(strings.TrimSpace(record.DisplayName)).
			SetSource(strings.TrimSpace(record.Source)).
			SetEnabled(enabled).
			SetNillableSupportsResponses(record.SupportsResponses).
			SetNillableSupportsChatCompletions(record.SupportsChatCompletions).
			SetNillableSupportsEmbeddings(record.SupportsEmbeddings).
			SetNillableContextWindow(record.ContextWindow).
			SetNillableMaxOutputTokens(record.MaxOutputTokens).
			SetNillableCompactHistoryItemThreshold(record.CompactHistoryItemThreshold).
			SetUpstreamModel(strings.TrimSpace(record.UpstreamModel)).
			SetProfileSource(strings.TrimSpace(record.ProfileSource)).
			SetProfileAdoptionStatus(strings.TrimSpace(record.ProfileAdoptionStatus)).
			SetInputModalitiesJSON(defaultJSON(record.InputModalitiesJSON, "[]")).
			SetOutputModalitiesJSON(defaultJSON(record.OutputModalitiesJSON, "[]")).
			SetRawModelJSON(defaultJSON(record.RawModelJSON, "{}")).
			SetFirstSeenAt(firstSeenAt.UTC()).
			SetLastSeenAt(lastSeenAt.UTC())
		if !record.LastProbeAt.IsZero() {
			create.SetLastProbeAt(record.LastProbeAt.UTC())
		}
		creates = append(creates, create)
	}
	if len(creates) > 0 {
		if err := tx.ChannelModel.CreateBulk(creates...).
			OnConflictColumns(channelmodel.FieldChannelID, channelmodel.FieldModel).
			UpdateNewValues().
			Exec(ctx); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) UpsertChannelModel(channelID string, record ChannelModelRecord) (ChannelModelRecord, error) {
	channelID = strings.TrimSpace(channelID)
	model := strings.ToLower(strings.TrimSpace(record.Model))
	if channelID == "" {
		return ChannelModelRecord{}, errors.New("channel id is required")
	}
	if model == "" {
		return ChannelModelRecord{}, errors.New("model is required")
	}
	now := time.Now().UTC()
	if record.FirstSeenAt.IsZero() {
		record.FirstSeenAt = now
	}
	if record.LastSeenAt.IsZero() {
		record.LastSeenAt = now
	}
	source := strings.TrimSpace(record.Source)
	if source == "" {
		source = "manual"
	}
	create := s.client.ChannelModel.Create().
		SetChannelID(channelID).
		SetModel(model).
		SetDisplayName(strings.TrimSpace(record.DisplayName)).
		SetSource(source).
		SetEnabled(record.Enabled).
		SetNillableSupportsResponses(record.SupportsResponses).
		SetNillableSupportsChatCompletions(record.SupportsChatCompletions).
		SetNillableSupportsEmbeddings(record.SupportsEmbeddings).
		SetNillableContextWindow(record.ContextWindow).
		SetNillableMaxOutputTokens(record.MaxOutputTokens).
		SetNillableCompactHistoryItemThreshold(record.CompactHistoryItemThreshold).
		SetUpstreamModel(strings.TrimSpace(record.UpstreamModel)).
		SetProfileSource(strings.TrimSpace(record.ProfileSource)).
		SetProfileAdoptionStatus(strings.TrimSpace(record.ProfileAdoptionStatus)).
		SetInputModalitiesJSON(defaultJSON(record.InputModalitiesJSON, "[]")).
		SetOutputModalitiesJSON(defaultJSON(record.OutputModalitiesJSON, "[]")).
		SetRawModelJSON(defaultJSON(record.RawModelJSON, "{}")).
		SetFirstSeenAt(record.FirstSeenAt.UTC()).
		SetLastSeenAt(record.LastSeenAt.UTC())
	if !record.LastProbeAt.IsZero() {
		create.SetLastProbeAt(record.LastProbeAt.UTC())
	}
	if err := create.
		OnConflictColumns(channelmodel.FieldChannelID, channelmodel.FieldModel).
		UpdateNewValues().
		Exec(context.Background()); err != nil {
		return ChannelModelRecord{}, err
	}
	if err := s.UpsertModelCatalog(ModelCatalogRecord{
		Model:       model,
		DisplayName: strings.TrimSpace(record.DisplayName),
		FirstSeenAt: record.FirstSeenAt,
		LastSeenAt:  record.LastSeenAt,
	}); err != nil {
		return ChannelModelRecord{}, err
	}
	row, err := s.client.ChannelModel.Query().
		Where(channelmodel.ChannelIDEQ(channelID), channelmodel.ModelEQ(model)).
		Only(context.Background())
	if err != nil {
		return ChannelModelRecord{}, err
	}
	return channelModelRecordFromEnt(row), nil
}

// channelModelUpsertChunk bounds how many models one grouped upsert carries, so
// the statement stays inside the parameter limits of both dialects.
const channelModelUpsertChunk = 100

// UpsertChannelModels stores the given models of one channel in grouped
// statements. UpsertChannelModel runs an upsert of the channel model, an upsert
// of the catalog entry and a read of the stored row per model, which the model
// discovery paid once per discovered model.
//
// Rows with the same model are collapsed to the last occurrence, because a single
// INSERT with two rows of the same conflict target fails on Postgres, and the
// returned slice still holds one record per input record, in input order.
func (s *Store) UpsertChannelModels(channelID string, records []ChannelModelRecord) ([]ChannelModelRecord, error) {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return nil, errors.New("channel id is required")
	}
	if len(records) == 0 {
		return nil, nil
	}
	now := time.Now().UTC()
	normalized := make([]ChannelModelRecord, 0, len(records))
	for _, record := range records {
		model := strings.ToLower(strings.TrimSpace(record.Model))
		if model == "" {
			return nil, errors.New("model is required")
		}
		record.Model = model
		if record.FirstSeenAt.IsZero() {
			record.FirstSeenAt = now
		}
		if record.LastSeenAt.IsZero() {
			record.LastSeenAt = now
		}
		if source := strings.TrimSpace(record.Source); source == "" {
			record.Source = "manual"
		} else {
			record.Source = source
		}
		normalized = append(normalized, record)
	}
	unique := make([]ChannelModelRecord, 0, len(normalized))
	at := make(map[string]int, len(normalized))
	for _, record := range normalized {
		if index, ok := at[record.Model]; ok {
			unique[index] = record
			continue
		}
		at[record.Model] = len(unique)
		unique = append(unique, record)
	}

	ctx := context.Background()
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	client := tx.Client()

	for start := 0; start < len(unique); start += channelModelUpsertChunk {
		end := start + channelModelUpsertChunk
		if end > len(unique) {
			end = len(unique)
		}
		chunk := unique[start:end]
		// The single-row upsert leaves last_probe_at out of the statement when
		// the record carries no probe time, so an existing probe time survives the
		// conflict update. A batch has one column set for all of its rows, so the
		// records without a probe time go in their own statement.
		withProbe := make([]*dao.ChannelModelCreate, 0, len(chunk))
		withoutProbe := make([]*dao.ChannelModelCreate, 0, len(chunk))
		catalogs := make([]*dao.ModelCatalogCreate, 0, len(chunk))
		for _, record := range chunk {
			builder := channelModelCreateBuilder(client, channelID, record)
			if record.LastProbeAt.IsZero() {
				withoutProbe = append(withoutProbe, builder)
			} else {
				withProbe = append(withProbe, builder.SetLastProbeAt(record.LastProbeAt.UTC()))
			}
			// The same field set UpsertModelCatalog writes for UpsertChannelModel,
			// which also resets the catalog columns it does not carry.
			catalogs = append(catalogs, client.ModelCatalog.Create().
				SetID(record.Model).
				SetDisplayName(strings.TrimSpace(record.DisplayName)).
				SetFamily("").
				SetVendor("").
				SetDescription("").
				SetTagsJSON(defaultJSON("", "[]")).
				SetFirstSeenAt(record.FirstSeenAt.UTC()).
				SetLastSeenAt(record.LastSeenAt.UTC()))
		}
		for _, group := range [][]*dao.ChannelModelCreate{withProbe, withoutProbe} {
			if len(group) == 0 {
				continue
			}
			if err := client.ChannelModel.CreateBulk(group...).
				OnConflictColumns(channelmodel.FieldChannelID, channelmodel.FieldModel).
				UpdateNewValues().
				Exec(ctx); err != nil {
				return nil, err
			}
		}
		if err := client.ModelCatalog.CreateBulk(catalogs...).
			OnConflictColumns(modelcatalog.FieldID).
			UpdateNewValues().
			Exec(ctx); err != nil {
			return nil, err
		}
	}

	stored := make(map[string]ChannelModelRecord, len(unique))
	for start := 0; start < len(unique); start += storeSQLParamChunk {
		end := start + storeSQLParamChunk
		if end > len(unique) {
			end = len(unique)
		}
		chunk := unique[start:end]
		names := make([]string, 0, len(chunk))
		for _, record := range chunk {
			names = append(names, record.Model)
		}
		rows, err := client.ChannelModel.Query().
			Where(channelmodel.ChannelIDEQ(channelID), channelmodel.ModelIn(names...)).
			All(ctx)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			stored[row.Model] = channelModelRecordFromEnt(row)
		}
	}

	out := make([]ChannelModelRecord, 0, len(normalized))
	for _, record := range normalized {
		saved, ok := stored[record.Model]
		if !ok {
			return nil, fmt.Errorf("channel model %q was not stored", record.Model)
		}
		out = append(out, saved)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

// channelModelCreateBuilder builds the insert of one channel model row, with the
// same field set UpsertChannelModel writes. The caller adds last_probe_at.
func channelModelCreateBuilder(client *dao.Client, channelID string, record ChannelModelRecord) *dao.ChannelModelCreate {
	create := client.ChannelModel.Create().
		SetChannelID(channelID).
		SetModel(record.Model).
		SetDisplayName(strings.TrimSpace(record.DisplayName)).
		SetSource(record.Source).
		SetEnabled(record.Enabled).
		SetNillableSupportsResponses(record.SupportsResponses).
		SetNillableSupportsChatCompletions(record.SupportsChatCompletions).
		SetNillableSupportsEmbeddings(record.SupportsEmbeddings).
		SetNillableContextWindow(record.ContextWindow).
		SetNillableMaxOutputTokens(record.MaxOutputTokens).
		SetNillableCompactHistoryItemThreshold(record.CompactHistoryItemThreshold).
		SetUpstreamModel(strings.TrimSpace(record.UpstreamModel)).
		SetProfileSource(strings.TrimSpace(record.ProfileSource)).
		SetProfileAdoptionStatus(strings.TrimSpace(record.ProfileAdoptionStatus)).
		SetInputModalitiesJSON(defaultJSON(record.InputModalitiesJSON, "[]")).
		SetOutputModalitiesJSON(defaultJSON(record.OutputModalitiesJSON, "[]")).
		SetRawModelJSON(defaultJSON(record.RawModelJSON, "{}")).
		SetFirstSeenAt(record.FirstSeenAt.UTC()).
		SetLastSeenAt(record.LastSeenAt.UTC())
	return create
}

func (s *Store) SetChannelModelEnabled(channelID string, model string, enabled bool) error {
	channelID = strings.TrimSpace(channelID)
	model = strings.ToLower(strings.TrimSpace(model))
	if channelID == "" {
		return errors.New("channel id is required")
	}
	if model == "" {
		return errors.New("model is required")
	}
	affected, err := s.client.ChannelModel.Update().
		Where(channelmodel.ChannelIDEQ(channelID), channelmodel.ModelEQ(model)).
		SetEnabled(enabled).
		SetLastSeenAt(time.Now().UTC()).
		Save(context.Background())
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (s *Store) UpdateChannelModelProfile(channelID string, model string, patch ChannelModelProfilePatch) (ChannelModelRecord, error) {
	channelID = strings.TrimSpace(channelID)
	model = strings.ToLower(strings.TrimSpace(model))
	if channelID == "" {
		return ChannelModelRecord{}, errors.New("channel id is required")
	}
	if model == "" {
		return ChannelModelRecord{}, errors.New("model is required")
	}
	update := s.client.ChannelModel.Update().
		Where(channelmodel.ChannelIDEQ(channelID), channelmodel.ModelEQ(model)).
		SetLastSeenAt(time.Now().UTC())
	if patch.DisplayName != nil {
		update.SetDisplayName(strings.TrimSpace(*patch.DisplayName))
	}
	if patch.Enabled != nil {
		update.SetEnabled(*patch.Enabled)
	}
	switch {
	case patch.ClearSupportsResponses:
		update.ClearSupportsResponses()
	case patch.SupportsResponses != nil:
		update.SetSupportsResponses(boolToCapabilityInt(*patch.SupportsResponses))
	}
	switch {
	case patch.ClearSupportsChatCompletions:
		update.ClearSupportsChatCompletions()
	case patch.SupportsChatCompletions != nil:
		update.SetSupportsChatCompletions(boolToCapabilityInt(*patch.SupportsChatCompletions))
	}
	switch {
	case patch.ClearSupportsEmbeddings:
		update.ClearSupportsEmbeddings()
	case patch.SupportsEmbeddings != nil:
		update.SetSupportsEmbeddings(boolToCapabilityInt(*patch.SupportsEmbeddings))
	}
	if patch.ContextWindow != nil {
		update.SetContextWindow(*patch.ContextWindow)
	}
	if patch.MaxOutputTokens != nil {
		update.SetMaxOutputTokens(*patch.MaxOutputTokens)
	}
	if patch.CompactHistoryItemThreshold != nil {
		update.SetCompactHistoryItemThreshold(*patch.CompactHistoryItemThreshold)
	}
	if patch.UpstreamModel != nil {
		update.SetUpstreamModel(strings.TrimSpace(*patch.UpstreamModel))
	}
	if patch.ProfileSource != nil {
		update.SetProfileSource(strings.TrimSpace(*patch.ProfileSource))
	}
	if patch.ProfileAdoptionStatus != nil {
		update.SetProfileAdoptionStatus(strings.TrimSpace(*patch.ProfileAdoptionStatus))
	}
	if _, err := update.Save(context.Background()); err != nil {
		return ChannelModelRecord{}, err
	}
	row, err := s.client.ChannelModel.Query().
		Where(channelmodel.ChannelIDEQ(channelID), channelmodel.ModelEQ(model)).
		Only(context.Background())
	if err != nil {
		if dao.IsNotFound(err) {
			return ChannelModelRecord{}, sql.ErrNoRows
		}
		return ChannelModelRecord{}, err
	}
	record := channelModelRecordFromEnt(row)
	if record.DisplayName != "" {
		if err := s.UpsertModelCatalog(ModelCatalogRecord{
			Model:       record.Model,
			DisplayName: record.DisplayName,
			FirstSeenAt: record.FirstSeenAt,
			LastSeenAt:  record.LastSeenAt,
		}); err != nil {
			return ChannelModelRecord{}, err
		}
	}
	return record, nil
}

func boolToCapabilityInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func (s *Store) DeleteChannelModel(channelID string, model string) error {
	channelID = strings.TrimSpace(channelID)
	model = strings.ToLower(strings.TrimSpace(model))
	if channelID == "" {
		return errors.New("channel id is required")
	}
	if model == "" {
		return errors.New("model is required")
	}
	affected, err := s.client.ChannelModel.Delete().
		Where(channelmodel.ChannelIDEQ(channelID), channelmodel.ModelEQ(model)).
		Exec(context.Background())
	if err != nil {
		return err
	}
	if affected == 0 {
		return sql.ErrNoRows
	}
	return nil
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

func (s *Store) CreateChannelProbeRun(record ChannelProbeRunRecord) (ChannelProbeRunRecord, error) {
	record.ID = strings.TrimSpace(record.ID)
	record.ChannelID = strings.TrimSpace(record.ChannelID)
	record.Status = strings.TrimSpace(record.Status)
	if record.ID == "" {
		record.ID = uuid.NewString()
	}
	if record.ChannelID == "" {
		return ChannelProbeRunRecord{}, errors.New("channel id is required")
	}
	if record.Status == "" {
		return ChannelProbeRunRecord{}, errors.New("probe status is required")
	}
	if record.StartedAt.IsZero() {
		record.StartedAt = time.Now().UTC()
	}
	create := s.client.ChannelProbeRun.Create().
		SetID(record.ID).
		SetChannelID(record.ChannelID).
		SetStatus(record.Status).
		SetStartedAt(record.StartedAt.UTC()).
		SetDurationMs(record.DurationMs).
		SetDiscoveredCount(record.DiscoveredCount).
		SetEnabledCount(record.EnabledCount).
		SetEndpoint(strings.TrimSpace(record.Endpoint)).
		SetStatusCode(record.StatusCode).
		SetErrorText(strings.TrimSpace(record.ErrorText)).
		SetRequestMetaJSON(defaultJSON(record.RequestMetaJSON, "{}")).
		SetResponseSampleJSON(defaultJSON(record.ResponseSampleJSON, "{}"))
	if !record.CompletedAt.IsZero() {
		create.SetCompletedAt(record.CompletedAt.UTC())
	}
	if err := create.Exec(context.Background()); err != nil {
		return ChannelProbeRunRecord{}, err
	}
	return record, nil
}

func (s *Store) ListChannelProbeRuns(channelID string, limit int) ([]ChannelProbeRunRecord, error) {
	query := s.client.ChannelProbeRun.Query()
	if channelID = strings.TrimSpace(channelID); channelID != "" {
		query = query.Where(channelproberun.ChannelIDEQ(channelID))
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := query.
		Order(channelproberun.ByStartedAt(entsql.OrderDesc()), channelproberun.ByID(entsql.OrderDesc())).
		Limit(limit).
		All(context.Background())
	if err != nil {
		return nil, err
	}
	out := make([]ChannelProbeRunRecord, 0, len(rows))
	for _, row := range rows {
		out = append(out, channelProbeRunRecordFromEnt(row))
	}
	return out, nil
}

func (s *Store) ListModelCatalogAnalytics(since time.Time, todaySince time.Time) ([]ModelCatalogAnalyticsRecord, error) {
	modelSet := map[string]*ModelCatalogAnalyticsRecord{}
	channelModels, err := s.ListChannelModels("", false)
	if err != nil {
		return nil, err
	}
	providersByModel := map[string]map[string]struct{}{}
	channelsByModel := map[string]map[string]struct{}{}
	enabledChannelsByModel := map[string]map[string]struct{}{}
	for _, channelModel := range channelModels {
		model := strings.ToLower(strings.TrimSpace(channelModel.Model))
		if !isUsageModelName(model) {
			continue
		}
		record := modelSet[model]
		if record == nil {
			record = &ModelCatalogAnalyticsRecord{Model: model, DisplayName: channelModel.DisplayName}
			modelSet[model] = record
		}
		if providersByModel[model] == nil {
			providersByModel[model] = map[string]struct{}{}
			channelsByModel[model] = map[string]struct{}{}
			enabledChannelsByModel[model] = map[string]struct{}{}
		}
		channelsByModel[model][channelModel.ChannelID] = struct{}{}
		if channelModel.Enabled {
			enabledChannelsByModel[model][channelModel.ChannelID] = struct{}{}
		}
	}

	channels, err := s.ListChannelConfigs()
	if err != nil {
		return nil, err
	}
	providerByChannel := map[string]string{}
	for _, channel := range channels {
		providerByChannel[channel.ID] = channel.ProviderPreset
	}
	for model, channelIDs := range channelsByModel {
		for channelID := range channelIDs {
			if provider := providerByChannel[channelID]; provider != "" {
				providersByModel[model][provider] = struct{}{}
			}
		}
	}

	logModels, err := s.listLogModels(since)
	if err != nil {
		return nil, err
	}
	logModelChannels, err := s.logModelChannels(since)
	if err != nil {
		return nil, err
	}
	for _, model := range logModels {
		if !isUsageModelName(model) {
			continue
		}
		if modelSet[model] == nil {
			modelSet[model] = &ModelCatalogAnalyticsRecord{Model: model}
		}
		if providersByModel[model] == nil {
			providersByModel[model] = map[string]struct{}{}
			channelsByModel[model] = map[string]struct{}{}
			enabledChannelsByModel[model] = map[string]struct{}{}
		}
		for channelID := range logModelChannels[model] {
			channelsByModel[model][channelID] = struct{}{}
			if provider := providerByChannel[channelID]; provider != "" {
				providersByModel[model][provider] = struct{}{}
			}
		}
	}
	summariesByModel, err := s.usageSummariesByModel(since)
	if err != nil {
		return nil, err
	}
	todayByModel, err := s.usageSummariesByModel(todaySince)
	if err != nil {
		return nil, err
	}
	for _, record := range modelSet {
		record.Summary = summariesByModel[record.Model]
		record.Today = todayByModel[record.Model]
		record.ProviderCount = len(providersByModel[record.Model])
		record.ChannelCount = len(channelsByModel[record.Model])
		record.EnabledChannelCount = len(enabledChannelsByModel[record.Model])
		record.Channels = sortedKeys(channelsByModel[record.Model])
	}

	out := make([]ModelCatalogAnalyticsRecord, 0, len(modelSet))
	for _, record := range modelSet {
		out = append(out, *record)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Summary.RequestCount != out[j].Summary.RequestCount {
			return out[i].Summary.RequestCount > out[j].Summary.RequestCount
		}
		return out[i].Model < out[j].Model
	})
	return out, nil
}

func (s *Store) GetModelDetailAnalytics(model string, since time.Time, todaySince time.Time, bucketSize time.Duration, bucketCount int) (ModelDetailAnalyticsRecord, error) {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return ModelDetailAnalyticsRecord{}, errors.New("model is required")
	}
	all, err := s.ListModelCatalogAnalytics(since, todaySince)
	if err != nil {
		return ModelDetailAnalyticsRecord{}, err
	}
	var detail ModelDetailAnalyticsRecord
	for _, item := range all {
		if item.Model == model {
			detail.Model = item
			break
		}
	}
	if detail.Model.Model == "" {
		return ModelDetailAnalyticsRecord{}, sql.ErrNoRows
	}
	trends, err := s.usageTrends("model = ?", []any{model}, since, bucketSize, bucketCount)
	if err != nil {
		return ModelDetailAnalyticsRecord{}, err
	}
	detail.Trends = trends

	channelModels, err := s.ListChannelModels("", false)
	if err != nil {
		return ModelDetailAnalyticsRecord{}, err
	}
	seenChannels := map[string]struct{}{}
	for _, channelModel := range channelModels {
		if strings.ToLower(channelModel.Model) != model || !isUsageModelName(model) {
			continue
		}
		seenChannels[channelModel.ChannelID] = struct{}{}
		summary, err := s.usageSummary("model = ? AND selected_upstream_id = ?", []any{model, channelModel.ChannelID}, since)
		if err != nil {
			return ModelDetailAnalyticsRecord{}, err
		}
		detail.Channels = append(detail.Channels, ChannelModelAnalyticsRecord{
			ChannelID: channelModel.ChannelID,
			Model:     model,
			Enabled:   channelModel.Enabled,
			Source:    channelModel.Source,
			Summary:   summary,
		})
	}
	logChannels, err := s.modelLogChannels(model, since)
	if err != nil {
		return ModelDetailAnalyticsRecord{}, err
	}
	for _, channelID := range logChannels {
		if _, ok := seenChannels[channelID]; ok {
			continue
		}
		summary, err := s.usageSummary("model = ? AND selected_upstream_id = ?", []any{model, channelID}, since)
		if err != nil {
			return ModelDetailAnalyticsRecord{}, err
		}
		detail.Channels = append(detail.Channels, ChannelModelAnalyticsRecord{
			ChannelID: channelID,
			Model:     model,
			Source:    "trace",
			Summary:   summary,
		})
	}
	sort.Slice(detail.Channels, func(i, j int) bool {
		if detail.Channels[i].Summary.RequestCount != detail.Channels[j].Summary.RequestCount {
			return detail.Channels[i].Summary.RequestCount > detail.Channels[j].Summary.RequestCount
		}
		return detail.Channels[i].ChannelID < detail.Channels[j].ChannelID
	})
	return detail, nil
}

func (s *Store) GetChannelUsageTrends(channelID string, since time.Time, bucketSize time.Duration, bucketCount int) ([]UsageTrendRecord, error) {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return nil, errors.New("channel id is required")
	}
	return s.usageTrends("selected_upstream_id = ?", []any{channelID}, since, bucketSize, bucketCount)
}

func (s *Store) GetChannelUsageSummary(channelID string, since time.Time) (UsageSummaryRecord, error) {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return UsageSummaryRecord{}, errors.New("channel id is required")
	}
	return s.usageSummary("selected_upstream_id = ?", []any{channelID}, since)
}

func (s *Store) GetChannelModelUsage(channelID string, since time.Time) ([]ChannelModelAnalyticsRecord, error) {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return nil, errors.New("channel id is required")
	}
	channelModels, err := s.ListChannelModels(channelID, false)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	out := make([]ChannelModelAnalyticsRecord, 0, len(channelModels))
	for _, channelModel := range channelModels {
		model := strings.ToLower(strings.TrimSpace(channelModel.Model))
		if !isUsageModelName(model) {
			continue
		}
		seen[model] = struct{}{}
		summary, err := s.usageSummary("selected_upstream_id = ? AND model = ?", []any{channelID, model}, since)
		if err != nil {
			return nil, err
		}
		out = append(out, ChannelModelAnalyticsRecord{
			ChannelID: channelID,
			Model:     model,
			Enabled:   channelModel.Enabled,
			Source:    channelModel.Source,
			Summary:   summary,
		})
	}

	logModels, err := s.channelLogModels(channelID, since)
	if err != nil {
		return nil, err
	}
	for _, model := range logModels {
		if !isUsageModelName(model) {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		summary, err := s.usageSummary("selected_upstream_id = ? AND model = ?", []any{channelID, model}, since)
		if err != nil {
			return nil, err
		}
		out = append(out, ChannelModelAnalyticsRecord{
			ChannelID: channelID,
			Model:     model,
			Source:    "trace",
			Summary:   summary,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Enabled != out[j].Enabled {
			return out[i].Enabled
		}
		if out[i].Summary.TotalTokens != out[j].Summary.TotalTokens {
			return out[i].Summary.TotalTokens > out[j].Summary.TotalTokens
		}
		if out[i].Summary.RequestCount != out[j].Summary.RequestCount {
			return out[i].Summary.RequestCount > out[j].Summary.RequestCount
		}
		return out[i].Model < out[j].Model
	})
	return out, nil
}

func (s *Store) GetChannelRecentFailures(channelID string, since time.Time, limit int) ([]UpstreamFailureRecord, error) {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return nil, errors.New("channel id is required")
	}
	return s.upstreamRecentFailures(channelID, limit, since, "")
}

func (s *Store) ListUpstreamAnalytics(limitModels int, limitErrors int, since time.Time, modelFilter string) ([]UpstreamAnalyticsRecord, error) {
	whereSQL, whereArgs := buildUpstreamAnalyticsWhere(since, modelFilter)
	rows, err := s.db.Query(`
		SELECT
			selected_upstream_id,
			COUNT(*) AS request_count,
			COALESCE(SUM(CASE WHEN status_code BETWEEN 200 AND 299 THEN 1 ELSE 0 END), 0) AS success_request,
			COALESCE(SUM(CASE WHEN status_code NOT BETWEEN 200 AND 299 THEN 1 ELSE 0 END), 0) AS failed_request,
			CASE WHEN COUNT(*) = 0 THEN 0 ELSE
				100.0 * SUM(CASE WHEN status_code BETWEEN 200 AND 299 THEN 1 ELSE 0 END) / COUNT(*)
			END AS success_rate,
			COALESCE(SUM(CASE WHEN status_code BETWEEN 200 AND 299 THEN total_tokens ELSE 0 END), 0) AS total_tokens,
			COALESCE(AVG(CASE WHEN status_code BETWEEN 200 AND 299 THEN ttft_ms END), 0) AS avg_ttft,
			MAX(recorded_at) AS last_seen
		FROM logs
		WHERE selected_upstream_id <> ''`+whereSQL+`
		GROUP BY selected_upstream_id
		ORDER BY request_count DESC, selected_upstream_id ASC
	`, whereArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// The per-upstream lists come from three grouped queries for the whole page
	// instead of four per upstream, which is what the loop below used to run.
	coverages, err := s.upstreamModelCoverageAll(limitModels, since, modelFilter)
	if err != nil {
		return nil, err
	}
	errorsByUpstream, err := s.upstreamRecentErrorsAll(limitErrors, since, modelFilter)
	if err != nil {
		return nil, err
	}
	failuresByUpstream, err := s.upstreamRecentFailuresAll(limitErrors, since, modelFilter)
	if err != nil {
		return nil, err
	}

	var out []UpstreamAnalyticsRecord
	for rows.Next() {
		var (
			record   UpstreamAnalyticsRecord
			lastSeen string
			avgTTFT  float64
		)
		if err := rows.Scan(
			&record.UpstreamID,
			&record.RequestCount,
			&record.SuccessRequest,
			&record.FailedRequest,
			&record.SuccessRate,
			&record.TotalTokens,
			&avgTTFT,
			&lastSeen,
		); err != nil {
			return nil, err
		}
		record.AvgTTFT = int(math.Round(avgTTFT))
		record.LastSeen, err = timeParse(lastSeen)
		if err != nil {
			return nil, err
		}
		coverage := coverages[record.UpstreamID]
		record.Models = coverage.Models
		record.LastModel = coverage.LastModel
		record.RecentErrors = errorsByUpstream[record.UpstreamID]
		record.RecentFailures = failuresByUpstream[record.UpstreamID]
		out = append(out, record)
	}
	return out, rows.Err()
}

func (s *Store) GetRoutingFailureAnalytics(since time.Time, modelFilter string, limitReasons int, limitRecent int, bucketSize time.Duration, bucketCount int) (RoutingFailureAnalytics, error) {
	if limitReasons <= 0 {
		limitReasons = 5
	}
	if limitRecent <= 0 {
		limitRecent = 5
	}
	if bucketSize <= 0 {
		bucketSize = time.Hour
	}
	if bucketCount <= 0 {
		bucketCount = 12
	}

	whereSQL, whereArgs := buildUpstreamAnalyticsWhere(since, modelFilter)
	baseWhere := `routing_failure_reason <> ''`
	if strings.TrimSpace(whereSQL) != "" {
		baseWhere += whereSQL
	}

	var analytics RoutingFailureAnalytics
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM logs WHERE `+baseWhere, whereArgs...).Scan(&analytics.Total); err != nil {
		return RoutingFailureAnalytics{}, err
	}

	reasonArgs := append([]any{}, whereArgs...)
	reasonArgs = append(reasonArgs, limitReasons)
	reasonRows, err := s.db.Query(`
		SELECT routing_failure_reason, COUNT(*) AS count
		FROM logs
		WHERE `+baseWhere+`
		GROUP BY routing_failure_reason
		ORDER BY count DESC, routing_failure_reason ASC
		LIMIT ?
	`, reasonArgs...)
	if err != nil {
		return RoutingFailureAnalytics{}, err
	}
	defer reasonRows.Close()
	for reasonRows.Next() {
		var item CountItem
		if err := reasonRows.Scan(&item.Label, &item.Count); err != nil {
			return RoutingFailureAnalytics{}, err
		}
		analytics.Reasons = append(analytics.Reasons, item)
	}
	if err := reasonRows.Err(); err != nil {
		return RoutingFailureAnalytics{}, err
	}

	recentArgs := append([]any{}, whereArgs...)
	recentArgs = append(recentArgs, limitRecent)
	recentRows, err := s.db.Query(`
		SELECT trace_id, model, endpoint, recorded_at, routing_failure_reason, error_text, status_code
		FROM logs
		WHERE `+baseWhere+`
		ORDER BY recorded_at DESC, trace_id DESC
		LIMIT ?
	`, recentArgs...)
	if err != nil {
		return RoutingFailureAnalytics{}, err
	}
	defer recentRows.Close()
	for recentRows.Next() {
		var (
			item       RoutingFailureRecord
			recordedAt string
		)
		if err := recentRows.Scan(&item.TraceID, &item.Model, &item.Endpoint, &recordedAt, &item.Reason, &item.ErrorText, &item.StatusCode); err != nil {
			return RoutingFailureAnalytics{}, err
		}
		item.RecordedAt, err = timeParse(recordedAt)
		if err != nil {
			return RoutingFailureAnalytics{}, err
		}
		analytics.Recent = append(analytics.Recent, item)
	}
	if err := recentRows.Err(); err != nil {
		return RoutingFailureAnalytics{}, err
	}

	referenceTime := time.Now().UTC()
	var latestRecordedAt any
	if err := s.db.QueryRow(`SELECT MAX(recorded_at) FROM logs WHERE `+baseWhere, whereArgs...).Scan(&latestRecordedAt); err != nil {
		return RoutingFailureAnalytics{}, err
	}
	if latestTime, err := timeParseNullableValue(latestRecordedAt); err != nil {
		return RoutingFailureAnalytics{}, err
	} else if !latestTime.IsZero() {
		referenceTime = latestTime
	}
	bucketStart := referenceTime.UTC().Truncate(bucketSize).Add(-time.Duration(bucketCount-1) * bucketSize)
	timelineArgs := append([]any{bucketStart.Format(timeLayout)}, whereArgs...)
	timelineRows, err := s.db.Query(`
		SELECT recorded_at
		FROM logs
		WHERE recorded_at >= ? AND `+baseWhere+`
		ORDER BY recorded_at ASC
	`, timelineArgs...)
	if err != nil {
		return RoutingFailureAnalytics{}, err
	}
	defer timelineRows.Close()

	buckets := make(map[time.Time]int, bucketCount)
	for index := 0; index < bucketCount; index++ {
		slot := bucketStart.Add(time.Duration(index) * bucketSize)
		buckets[slot] = 0
	}
	for timelineRows.Next() {
		var recordedAt string
		if err := timelineRows.Scan(&recordedAt); err != nil {
			return RoutingFailureAnalytics{}, err
		}
		recordedTime, err := timeParse(recordedAt)
		if err != nil {
			return RoutingFailureAnalytics{}, err
		}
		slot := recordedTime.UTC().Truncate(bucketSize)
		if slot.Before(bucketStart) {
			continue
		}
		if _, ok := buckets[slot]; ok {
			buckets[slot]++
		}
	}
	if err := timelineRows.Err(); err != nil {
		return RoutingFailureAnalytics{}, err
	}
	for index := 0; index < bucketCount; index++ {
		slot := bucketStart.Add(time.Duration(index) * bucketSize)
		analytics.Timeline = append(analytics.Timeline, TimeCountItem{
			Time:  slot,
			Count: buckets[slot],
		})
	}

	return analytics, nil
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

func (s *Store) upstreamModelCoverage(upstreamID string, limit int, since time.Time, modelFilter string) ([]string, string, error) {
	if limit <= 0 {
		limit = 5
	}
	whereSQL, whereArgs := buildUpstreamAnalyticsWhere(since, modelFilter)
	args := append([]any{upstreamID}, whereArgs...)
	args = append(args, limit)
	rows, err := s.db.Query(`
		SELECT model, COUNT(*) AS count
		FROM logs
		WHERE selected_upstream_id = ? AND model <> ''`+whereSQL+`
		GROUP BY model
		ORDER BY count DESC, model ASC
		LIMIT ?
	`, args...)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()

	var models []string
	for rows.Next() {
		var model string
		var count int
		if err := rows.Scan(&model, &count); err != nil {
			return nil, "", err
		}
		models = append(models, model)
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}

	var lastModel string
	lastModelArgs := append([]any{upstreamID}, whereArgs...)
	if err := s.db.QueryRow(`
		SELECT model
		FROM logs
		WHERE selected_upstream_id = ? AND model <> ''`+whereSQL+`
		ORDER BY recorded_at DESC, trace_id DESC
		LIMIT 1
	`, lastModelArgs...).Scan(&lastModel); err != nil && err != sql.ErrNoRows {
		return nil, "", err
	}

	return models, lastModel, nil
}

func (s *Store) upstreamRecentErrors(upstreamID string, limit int, since time.Time, modelFilter string) ([]string, error) {
	if limit <= 0 {
		limit = 3
	}
	whereSQL, whereArgs := buildUpstreamAnalyticsWhere(since, modelFilter)
	args := append([]any{upstreamID}, whereArgs...)
	args = append(args, limit)
	rows, err := s.db.Query(`
		SELECT error_text, status_code, endpoint
		FROM logs
		WHERE selected_upstream_id = ?
		  `+whereSQL+`
		  AND (status_code NOT BETWEEN 200 AND 299 OR error_text <> '')
		ORDER BY recorded_at DESC, trace_id DESC
		LIMIT ?
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var (
			errorText  string
			statusCode int
			endpoint   string
		)
		if err := rows.Scan(&errorText, &statusCode, &endpoint); err != nil {
			return nil, err
		}
		switch {
		case strings.TrimSpace(errorText) != "":
			out = append(out, errorText)
		case strings.TrimSpace(endpoint) != "":
			out = append(out, fmt.Sprintf("%s HTTP %d", endpoint, statusCode))
		default:
			out = append(out, fmt.Sprintf("HTTP %d", statusCode))
		}
	}
	return out, rows.Err()
}

func (s *Store) upstreamRecentFailures(upstreamID string, limit int, since time.Time, modelFilter string) ([]UpstreamFailureRecord, error) {
	if limit <= 0 {
		limit = 3
	}
	whereSQL, whereArgs := buildUpstreamAnalyticsWhere(since, modelFilter)
	args := append([]any{upstreamID}, whereArgs...)
	args = append(args, limit)
	rows, err := s.db.Query(`
		SELECT trace_id, model, endpoint, status_code, recorded_at, error_text
		FROM logs
		WHERE selected_upstream_id = ?
		  `+whereSQL+`
		  AND (status_code NOT BETWEEN 200 AND 299 OR error_text <> '')
		ORDER BY recorded_at DESC, trace_id DESC
		LIMIT ?
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []UpstreamFailureRecord
	for rows.Next() {
		var (
			record     UpstreamFailureRecord
			recordedAt string
		)
		if err := rows.Scan(&record.TraceID, &record.Model, &record.Endpoint, &record.StatusCode, &recordedAt, &record.ErrorText); err != nil {
			return nil, err
		}
		record.RecordedAt, err = timeParse(recordedAt)
		if err != nil {
			return nil, err
		}
		record.Reason = classifyUpstreamFailure(record.StatusCode, record.ErrorText)
		out = append(out, record)
	}
	return out, rows.Err()
}

// upstreamModelCoverageRecord is the per-upstream result of the batched model
// coverage query.
type upstreamModelCoverageRecord struct {
	Models    []string
	LastModel string
}

// upstreamModelCoverageAll computes upstreamModelCoverage for every upstream in
// two queries. The per-upstream form ran both of them once per upstream, and the
// analytics page calls it for every upstream it lists. Both queries rank inside
// the database so the model order and the top-N cut keep the collation the
// per-upstream query used.
func (s *Store) upstreamModelCoverageAll(limit int, since time.Time, modelFilter string) (map[string]upstreamModelCoverageRecord, error) {
	if limit <= 0 {
		limit = 5
	}
	whereSQL, whereArgs := buildUpstreamAnalyticsWhere(since, modelFilter)
	out := map[string]upstreamModelCoverageRecord{}

	rankedArgs := append([]any(nil), whereArgs...)
	rankedArgs = append(rankedArgs, limit)
	rows, err := s.db.Query(`
		SELECT selected_upstream_id, model
		FROM (
			SELECT
				selected_upstream_id,
				model,
				ROW_NUMBER() OVER (
					PARTITION BY selected_upstream_id
					ORDER BY COUNT(*) DESC, model ASC
				) AS model_rank
			FROM logs
			WHERE selected_upstream_id <> '' AND model <> ''`+whereSQL+`
			GROUP BY selected_upstream_id, model
		) ranked
		WHERE model_rank <= ?
		ORDER BY selected_upstream_id, model_rank
	`, rankedArgs...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var (
			upstreamID string
			model      string
		)
		if err := rows.Scan(&upstreamID, &model); err != nil {
			rows.Close()
			return nil, err
		}
		record := out[upstreamID]
		record.Models = append(record.Models, model)
		out[upstreamID] = record
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}

	lastRows, err := s.db.Query(`
		SELECT selected_upstream_id, model
		FROM (
			SELECT
				selected_upstream_id,
				model,
				ROW_NUMBER() OVER (
					PARTITION BY selected_upstream_id
					ORDER BY recorded_at DESC, trace_id DESC
				) AS row_number
			FROM logs
			WHERE selected_upstream_id <> '' AND model <> ''`+whereSQL+`
		) ranked
		WHERE row_number = 1
	`, whereArgs...)
	if err != nil {
		return nil, err
	}
	defer lastRows.Close()
	for lastRows.Next() {
		var (
			upstreamID string
			model      string
		)
		if err := lastRows.Scan(&upstreamID, &model); err != nil {
			return nil, err
		}
		record := out[upstreamID]
		record.LastModel = model
		out[upstreamID] = record
	}
	return out, lastRows.Err()
}

// upstreamRecentErrorsAll computes upstreamRecentErrors for every upstream in one
// query, ranking the last limit rows of each upstream inside the database.
func (s *Store) upstreamRecentErrorsAll(limit int, since time.Time, modelFilter string) (map[string][]string, error) {
	if limit <= 0 {
		limit = 3
	}
	whereSQL, whereArgs := buildUpstreamAnalyticsWhere(since, modelFilter)
	args := append([]any(nil), whereArgs...)
	args = append(args, limit)
	rows, err := s.db.Query(`
		SELECT selected_upstream_id, error_text, status_code, endpoint
		FROM (
			SELECT
				selected_upstream_id,
				error_text,
				status_code,
				endpoint,
				ROW_NUMBER() OVER (
					PARTITION BY selected_upstream_id
					ORDER BY recorded_at DESC, trace_id DESC
				) AS row_number
			FROM logs
			WHERE selected_upstream_id <> ''`+whereSQL+`
			  AND (status_code NOT BETWEEN 200 AND 299 OR error_text <> '')
		) ranked
		WHERE row_number <= ?
		ORDER BY selected_upstream_id, row_number
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string][]string{}
	for rows.Next() {
		var (
			upstreamID string
			errorText  string
			statusCode int
			endpoint   string
		)
		if err := rows.Scan(&upstreamID, &errorText, &statusCode, &endpoint); err != nil {
			return nil, err
		}
		switch {
		case strings.TrimSpace(errorText) != "":
			out[upstreamID] = append(out[upstreamID], errorText)
		case strings.TrimSpace(endpoint) != "":
			out[upstreamID] = append(out[upstreamID], fmt.Sprintf("%s HTTP %d", endpoint, statusCode))
		default:
			out[upstreamID] = append(out[upstreamID], fmt.Sprintf("HTTP %d", statusCode))
		}
	}
	return out, rows.Err()
}

// upstreamRecentFailuresAll computes upstreamRecentFailures for every upstream
// in one query, ranking the last limit rows of each upstream inside the database.
func (s *Store) upstreamRecentFailuresAll(limit int, since time.Time, modelFilter string) (map[string][]UpstreamFailureRecord, error) {
	if limit <= 0 {
		limit = 3
	}
	whereSQL, whereArgs := buildUpstreamAnalyticsWhere(since, modelFilter)
	args := append([]any(nil), whereArgs...)
	args = append(args, limit)
	rows, err := s.db.Query(`
		SELECT selected_upstream_id, trace_id, model, endpoint, status_code, recorded_at, error_text
		FROM (
			SELECT
				selected_upstream_id,
				trace_id,
				model,
				endpoint,
				status_code,
				recorded_at,
				error_text,
				ROW_NUMBER() OVER (
					PARTITION BY selected_upstream_id
					ORDER BY recorded_at DESC, trace_id DESC
				) AS row_number
			FROM logs
			WHERE selected_upstream_id <> ''`+whereSQL+`
			  AND (status_code NOT BETWEEN 200 AND 299 OR error_text <> '')
		) ranked
		WHERE row_number <= ?
		ORDER BY selected_upstream_id, row_number
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string][]UpstreamFailureRecord{}
	for rows.Next() {
		var (
			upstreamID string
			record     UpstreamFailureRecord
			recordedAt string
		)
		if err := rows.Scan(&upstreamID, &record.TraceID, &record.Model, &record.Endpoint, &record.StatusCode, &recordedAt, &record.ErrorText); err != nil {
			return nil, err
		}
		parsed, err := timeParse(recordedAt)
		if err != nil {
			return nil, err
		}
		record.RecordedAt = parsed
		record.Reason = classifyUpstreamFailure(record.StatusCode, record.ErrorText)
		out[upstreamID] = append(out[upstreamID], record)
	}
	return out, rows.Err()
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

func (s *Store) sessionProvidersAggregateSQL() string {
	if s != nil && s.driver == "postgres" {
		return "COALESCE(string_agg(DISTINCT CASE WHEN s.provider <> '' THEN s.provider END, ','), '')"
	}
	return "COALESCE(GROUP_CONCAT(DISTINCT CASE WHEN s.provider <> '' THEN s.provider END), '')"
}

func buildUpstreamAnalyticsWhere(since time.Time, modelFilter string) (string, []any) {
	var (
		clauses []string
		args    []any
	)
	if !since.IsZero() {
		clauses = append(clauses, `recorded_at >= ?`)
		args = append(args, since.UTC().Format(timeLayout))
	}
	if model := strings.TrimSpace(modelFilter); model != "" {
		clauses = append(clauses, `LOWER(model) LIKE LOWER(?) ESCAPE '\'`)
		args = append(args, "%"+escapeLike(model)+"%")
	}
	if len(clauses) == 0 {
		return "", nil
	}
	return " AND " + strings.Join(clauses, " AND "), args
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

func (s *Store) channelLogModels(channelID string, since time.Time) ([]string, error) {
	where := "selected_upstream_id = ? AND model <> '' AND LOWER(model) <> 'list_models'"
	args := []any{channelID}
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

func (s *Store) modelLogChannels(model string, since time.Time) ([]string, error) {
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return nil, errors.New("model is required")
	}
	where := "LOWER(model) = ? AND selected_upstream_id <> ''"
	args := []any{model}
	if !since.IsZero() {
		where += " AND recorded_at >= ?"
		args = append(args, since.UTC().Format(timeLayout))
	}
	rows, err := s.db.Query(`SELECT DISTINCT selected_upstream_id FROM logs WHERE `+where+` ORDER BY selected_upstream_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var channelID string
		if err := rows.Scan(&channelID); err != nil {
			return nil, err
		}
		if channelID = strings.TrimSpace(channelID); channelID != "" {
			out = append(out, channelID)
		}
	}
	return out, rows.Err()
}

func (s *Store) logModelChannels(since time.Time) (map[string]map[string]struct{}, error) {
	where := "model <> '' AND LOWER(model) <> 'list_models' AND selected_upstream_id <> ''"
	args := []any{}
	if !since.IsZero() {
		where += " AND recorded_at >= ?"
		args = append(args, since.UTC().Format(timeLayout))
	}
	rows, err := s.db.Query(`SELECT DISTINCT LOWER(model), selected_upstream_id FROM logs WHERE `+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]map[string]struct{}{}
	for rows.Next() {
		var model, channelID string
		if err := rows.Scan(&model, &channelID); err != nil {
			return nil, err
		}
		model = strings.TrimSpace(model)
		channelID = strings.TrimSpace(channelID)
		if model == "" || channelID == "" {
			continue
		}
		if out[model] == nil {
			out[model] = map[string]struct{}{}
		}
		out[model][channelID] = struct{}{}
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

// GetChannelUsageSummaries returns the per-channel usage summary for the window
// in one grouped pass, keyed by channel id. The channel list page would
// otherwise ask for one summary per configured channel.
func (s *Store) GetChannelUsageSummaries(since time.Time) (map[string]UsageSummaryRecord, error) {
	return s.usageSummariesByGroupKey("selected_upstream_id", since)
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

// GetChannelUsageTrendsBatch computes the same series GetChannelUsageTrends
// computes for one channel, for every channel in one ordered pass. Each channel
// anchors its series at its own latest record, exactly like the single-key form,
// so the shared scan starts at the earliest of those windows and a row that
// falls outside its own channel's window is dropped while bucketing. A channel
// with no rows at all is absent from the result, because the scan cannot know
// the empty window such a channel would have.
func (s *Store) GetChannelUsageTrendsBatch(since time.Time, bucketSize time.Duration, bucketCount int) (map[string][]UsageTrendRecord, error) {
	if bucketSize <= 0 {
		bucketSize = 24 * time.Hour
	}
	if bucketCount <= 0 {
		bucketCount = 7
	}
	// The reference time of a channel is its latest record over all time, not
	// just the window, which is what the single-key form reads too.
	referenceRows, err := s.db.Query(`
		SELECT selected_upstream_id, MAX(recorded_at)
		FROM logs
		WHERE selected_upstream_id <> ''
		GROUP BY selected_upstream_id
	`)
	if err != nil {
		return nil, err
	}
	references := map[string]time.Time{}
	for referenceRows.Next() {
		var (
			channelID    string
			latestRecord any
		)
		if err := referenceRows.Scan(&channelID, &latestRecord); err != nil {
			referenceRows.Close()
			return nil, err
		}
		latestTime, err := timeParseNullableValue(latestRecord)
		if err != nil {
			referenceRows.Close()
			return nil, err
		}
		if !latestTime.IsZero() {
			references[channelID] = latestTime.UTC()
		}
	}
	err = referenceRows.Err()
	referenceRows.Close()
	if err != nil {
		return nil, err
	}
	if len(references) == 0 {
		return map[string][]UsageTrendRecord{}, nil
	}

	type bucket struct {
		requests int
		failed   int
		missing  int
		tokens   int
		models   map[string]struct{}
	}
	where := "selected_upstream_id <> ''"
	var args []any
	if !since.IsZero() {
		where += " AND recorded_at >= ?"
		args = append(args, since.UTC().Format(timeLayout))
	}
	var scanStart time.Time
	slotsByChannel := make(map[string][]time.Time, len(references))
	bucketsByChannel := make(map[string]map[time.Time]*bucket, len(references))
	for channelID, referenceTime := range references {
		bucketStart := referenceTime.Truncate(bucketSize).Add(-time.Duration(bucketCount-1) * bucketSize)
		slots := make([]time.Time, 0, bucketCount)
		buckets := make(map[time.Time]*bucket, bucketCount)
		for index := 0; index < bucketCount; index++ {
			slot := bucketStart.Add(time.Duration(index) * bucketSize)
			slots = append(slots, slot)
			buckets[slot] = &bucket{models: map[string]struct{}{}}
		}
		slotsByChannel[channelID] = slots
		bucketsByChannel[channelID] = buckets
		if scanStart.IsZero() || bucketStart.Before(scanStart) {
			scanStart = bucketStart
		}
	}

	queryArgs := append([]any(nil), args...)
	queryArgs = append(queryArgs, scanStart.Format(timeLayout))
	rows, err := s.db.Query(`
		SELECT selected_upstream_id, recorded_at, status_code, total_tokens, prompt_tokens, completion_tokens, model
		FROM logs
		WHERE `+where+` AND recorded_at >= ?
		ORDER BY selected_upstream_id, recorded_at ASC
	`, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var (
			channelID   string
			recordedAt  string
			statusCode  int
			totalTokens int
			prompt      int
			completion  int
			model       string
		)
		if err := rows.Scan(&channelID, &recordedAt, &statusCode, &totalTokens, &prompt, &completion, &model); err != nil {
			return nil, err
		}
		buckets := bucketsByChannel[channelID]
		if buckets == nil {
			continue
		}
		recordedTime, err := timeParse(recordedAt)
		if err != nil {
			return nil, err
		}
		item := buckets[recordedTime.UTC().Truncate(bucketSize)]
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

	out := make(map[string][]UsageTrendRecord, len(references))
	for channelID, slots := range slotsByChannel {
		buckets := bucketsByChannel[channelID]
		series := make([]UsageTrendRecord, 0, len(slots))
		for _, slot := range slots {
			item := buckets[slot]
			series = append(series, UsageTrendRecord{
				Time:          slot,
				RequestCount:  item.requests,
				FailedRequest: item.failed,
				MissingUsage:  item.missing,
				TotalTokens:   item.tokens,
				ModelCount:    len(item.models),
			})
		}
		out[channelID] = series
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

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
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

func (s *Store) initSchema() error {
	if s.driver == "postgres" {
		if err := s.client.Schema.Create(context.Background()); err != nil {
			return err
		}
		if _, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS "app_settings" (
			"setting_key" character varying NOT NULL,
			"value_json" character varying NOT NULL,
			"updated_at" timestamptz NOT NULL,
			PRIMARY KEY ("setting_key")
		);`); err != nil {
			return err
		}
		if err := s.ensureSessionSummariesSchema(); err != nil {
			return err
		}
		if err := s.ensureModelAliasesSchema(); err != nil {
			return err
		}
		if err := s.ensureLogExchangeColumns(); err != nil {
			return err
		}
		if err := s.ensureOverviewMetricBucketsSchema(); err != nil {
			return err
		}
		return nil
	}
	stmts := sqliteSchemaStatements

	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return s.applySQLiteSchemaUpgrades()
}

func (s *Store) ensureSessionSummariesSchema() error {
	timeType := "datetime"
	if s.driver == "postgres" {
		timeType = "timestamptz"
	}
	if _, err := s.db.Exec(fmt.Sprintf(`CREATE TABLE IF NOT EXISTS session_summaries (
		session_id TEXT PRIMARY KEY,
		session_source TEXT NOT NULL DEFAULT '',
		request_count INTEGER NOT NULL DEFAULT 0,
		first_seen %s NOT NULL,
		last_seen %s NOT NULL,
		last_model TEXT NOT NULL DEFAULT '',
		providers TEXT NOT NULL DEFAULT '',
		success_request INTEGER NOT NULL DEFAULT 0,
		failed_request INTEGER NOT NULL DEFAULT 0,
		success_rate REAL NOT NULL DEFAULT 0,
		total_tokens INTEGER NOT NULL DEFAULT 0,
		avg_ttft REAL NOT NULL DEFAULT 0,
		total_duration INTEGER NOT NULL DEFAULT 0,
		stream_count INTEGER NOT NULL DEFAULT 0,
		updated_at %s NOT NULL
	)`, timeType, timeType, timeType)); err != nil {
		return err
	}
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_session_summaries_last_seen ON session_summaries(last_seen DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_session_summaries_last_model ON session_summaries(last_model)`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ensureEntCompatibleTables() error {
	if err := s.ensureUpstreamTargetsEntTable(); err != nil {
		return err
	}
	if err := s.ensureAutoIDTable(
		"upstream_models",
		`CREATE TABLE upstream_models (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			upstream_id TEXT NOT NULL,
			model TEXT NOT NULL,
			source TEXT NOT NULL,
			seen_at datetime NOT NULL
		)`,
		`INSERT INTO upstream_models (upstream_id, model, source, seen_at)
		 SELECT upstream_id, model, source, seen_at FROM upstream_models_old`,
		[]string{
			`CREATE UNIQUE INDEX IF NOT EXISTS upstreammodel_upstream_id_model ON upstream_models(upstream_id, model)`,
			`CREATE INDEX IF NOT EXISTS idx_upstream_models_model ON upstream_models(model)`,
		},
	); err != nil {
		return err
	}
	return s.ensureAutoIDTable(
		"dataset_examples",
		`CREATE TABLE dataset_examples (
			id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
			dataset_id TEXT NOT NULL,
			trace_id TEXT NOT NULL,
			position INTEGER NOT NULL,
			added_at TEXT NOT NULL,
			source_type TEXT NOT NULL DEFAULT '',
			source_id TEXT NOT NULL DEFAULT '',
			note TEXT NOT NULL DEFAULT ''
		)`,
		`INSERT INTO dataset_examples (dataset_id, trace_id, position, added_at, source_type, source_id, note)
		 SELECT dataset_id, trace_id, position, added_at, source_type, source_id, note FROM dataset_examples_old`,
		[]string{
			`CREATE UNIQUE INDEX IF NOT EXISTS datasetexample_dataset_id_trace_id ON dataset_examples(dataset_id, trace_id)`,
			`CREATE INDEX IF NOT EXISTS idx_dataset_examples_dataset_position ON dataset_examples(dataset_id, position ASC)`,
		},
	)
}

func (s *Store) ensureUpstreamTargetsEntTable() error {
	enabledType, err := s.columnType("upstream_targets", "enabled")
	if err != nil {
		return err
	}
	lastRefreshAtType, err := s.columnType("upstream_targets", "last_refresh_at")
	if err != nil {
		return err
	}
	if strings.EqualFold(enabledType, "bool") && strings.EqualFold(lastRefreshAtType, "datetime") {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`ALTER TABLE upstream_targets RENAME TO upstream_targets_old`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE TABLE upstream_targets (
		id TEXT PRIMARY KEY,
		base_url TEXT NOT NULL DEFAULT '',
		provider_preset TEXT NOT NULL DEFAULT '',
		protocol_family TEXT NOT NULL DEFAULT '',
		routing_profile TEXT NOT NULL DEFAULT '',
		enabled bool NOT NULL DEFAULT true,
		priority INTEGER NOT NULL DEFAULT 0,
		weight REAL NOT NULL DEFAULT 0,
		capacity_hint REAL NOT NULL DEFAULT 0,
		last_refresh_at datetime NULL,
		last_refresh_status TEXT NOT NULL DEFAULT '',
		last_refresh_error TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO upstream_targets (
		id, base_url, provider_preset, protocol_family, routing_profile, enabled,
		priority, weight, capacity_hint, last_refresh_at, last_refresh_status, last_refresh_error
	)
	SELECT
		id, base_url, provider_preset, protocol_family, routing_profile,
		CASE WHEN enabled IN (1, '1', 'true', 'TRUE') THEN true ELSE false END,
		priority, weight, capacity_hint,
		CASE WHEN last_refresh_at IS NULL OR TRIM(CAST(last_refresh_at AS text)) = '' THEN NULL ELSE last_refresh_at END,
		last_refresh_status, last_refresh_error
	FROM upstream_targets_old`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE upstream_targets_old`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ensureLogsDatetimeTable() error {
	recordedAtType, err := s.columnType("logs", "recorded_at")
	if err != nil {
		return err
	}
	isStreamType, err := s.columnType("logs", "is_stream")
	if err != nil {
		return err
	}
	if strings.EqualFold(recordedAtType, "datetime") && strings.EqualFold(isStreamType, "bool") {
		return nil
	}

	for _, stmt := range []string{
		`DROP INDEX IF EXISTS idx_logs_recorded_at`,
		`DROP INDEX IF EXISTS idx_logs_model_recorded_at`,
		`DROP INDEX IF EXISTS idx_logs_trace_id`,
		`DROP INDEX IF EXISTS idx_logs_session_id_recorded_at`,
		`DROP INDEX IF EXISTS logs_trace_id_key`,
		`DROP INDEX IF EXISTS tracelog_recorded_at`,
		`DROP INDEX IF EXISTS tracelog_model_recorded_at`,
		`DROP INDEX IF EXISTS tracelog_session_id_recorded_at`,
		`DROP INDEX IF EXISTS tracelog_request_id`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`ALTER TABLE logs RENAME TO logs_old`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE TABLE logs (
		path TEXT PRIMARY KEY,
		trace_id TEXT NOT NULL DEFAULT '',
		mod_time_ns INTEGER NOT NULL,
		file_size INTEGER NOT NULL,
		version TEXT NOT NULL,
		request_id TEXT NOT NULL DEFAULT '',
		recorded_at datetime NOT NULL,
		model TEXT NOT NULL DEFAULT '',
		provider TEXT NOT NULL DEFAULT '',
		operation TEXT NOT NULL DEFAULT '',
		endpoint TEXT NOT NULL DEFAULT '',
		url TEXT NOT NULL DEFAULT '',
		method TEXT NOT NULL DEFAULT '',
		status_code INTEGER NOT NULL DEFAULT 0,
		duration_ms INTEGER NOT NULL DEFAULT 0,
		ttft_ms INTEGER NOT NULL DEFAULT 0,
		client_ip TEXT NOT NULL DEFAULT '',
		content_length INTEGER NOT NULL DEFAULT 0,
		error_text TEXT NOT NULL DEFAULT '',
		prompt_tokens INTEGER NOT NULL DEFAULT 0,
		completion_tokens INTEGER NOT NULL DEFAULT 0,
		total_tokens INTEGER NOT NULL DEFAULT 0,
		cached_tokens INTEGER NOT NULL DEFAULT 0,
		req_header_len INTEGER NOT NULL DEFAULT 0,
		req_body_len INTEGER NOT NULL DEFAULT 0,
		res_header_len INTEGER NOT NULL DEFAULT 0,
		res_body_len INTEGER NOT NULL DEFAULT 0,
		is_stream bool NOT NULL DEFAULT false,
		session_id TEXT NOT NULL DEFAULT '',
		session_source TEXT NOT NULL DEFAULT '',
		window_id TEXT NOT NULL DEFAULT '',
		client_request_id TEXT NOT NULL DEFAULT '',
		request_audit_id TEXT NOT NULL DEFAULT '',
		response_id TEXT NOT NULL DEFAULT '',
		exchange_id TEXT NOT NULL DEFAULT '',
		exchange_kind TEXT NOT NULL DEFAULT '',
		exchange_role TEXT NOT NULL DEFAULT '',
		parent_exchange_id TEXT NOT NULL DEFAULT '',
		sequence_index INTEGER NOT NULL DEFAULT 0,
		selected_upstream_id TEXT NOT NULL DEFAULT '',
		selected_upstream_base_url TEXT NOT NULL DEFAULT '',
		selected_upstream_provider_preset TEXT NOT NULL DEFAULT '',
		routing_policy TEXT NOT NULL DEFAULT '',
		routing_score REAL NOT NULL DEFAULT 0,
		routing_candidate_count INTEGER NOT NULL DEFAULT 0,
		routing_failure_reason TEXT NOT NULL DEFAULT ''
	)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO logs (
		path, trace_id, mod_time_ns, file_size, version, request_id, recorded_at, model, provider, operation, endpoint, url, method,
		status_code, duration_ms, ttft_ms, client_ip, content_length, error_text,
		prompt_tokens, completion_tokens, total_tokens, cached_tokens,
		req_header_len, req_body_len, res_header_len, res_body_len, is_stream,
		session_id, session_source, window_id, client_request_id,
		request_audit_id, response_id,
		exchange_id, exchange_kind, exchange_role, parent_exchange_id, sequence_index,
		selected_upstream_id, selected_upstream_base_url, selected_upstream_provider_preset,
		routing_policy, routing_score, routing_candidate_count, routing_failure_reason
	)
	SELECT
		path, trace_id, mod_time_ns, file_size, version, request_id,
		CASE WHEN recorded_at IS NULL OR TRIM(CAST(recorded_at AS text)) = '' THEN '1970-01-01T00:00:00Z' ELSE recorded_at END,
		model, provider, operation, endpoint, url, method,
		status_code, duration_ms, ttft_ms, client_ip, content_length, error_text,
		prompt_tokens, completion_tokens, total_tokens, cached_tokens,
		req_header_len, req_body_len, res_header_len, res_body_len,
		CASE WHEN is_stream IN (1, '1', 'true', 'TRUE') THEN true ELSE false END,
		session_id, session_source, window_id, client_request_id,
		'', '',
		'', '', '', '', 0,
		selected_upstream_id, selected_upstream_base_url, selected_upstream_provider_preset,
		routing_policy, routing_score, routing_candidate_count, routing_failure_reason
	FROM logs_old`); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE logs_old`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ensureAutoIDTable(table string, createSQL string, copySQL string, indexes []string) error {
	hasID, err := s.hasColumn(table, "id")
	if err != nil {
		return err
	}
	if hasID {
		return nil
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`ALTER TABLE ` + table + ` RENAME TO ` + table + `_old`); err != nil {
		return err
	}
	if _, err := tx.Exec(createSQL); err != nil {
		return err
	}
	if _, err := tx.Exec(copySQL); err != nil {
		return err
	}
	if _, err := tx.Exec(`DROP TABLE ` + table + `_old`); err != nil {
		return err
	}
	for _, stmt := range indexes {
		if _, err := tx.Exec(stmt); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) ensureColumn(table string, column string, definition string) error {
	exists, err := s.hasColumn(table, column)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}

	_, err = s.db.Exec(`ALTER TABLE ` + table + ` ADD COLUMN ` + column + ` ` + definition)
	return err
}

func (s *Store) ensureModelAliasesSchema() error {
	if s == nil || s.db == nil {
		return nil
	}
	createdAtType := "datetime"
	updatedAtType := "datetime"
	if s.driver == "postgres" {
		createdAtType = "timestamptz"
		updatedAtType = "timestamptz"
	}
	stmts := []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS model_aliases (
			id TEXT PRIMARY KEY,
			alias TEXT NOT NULL,
			target_model TEXT NOT NULL,
			channel_id TEXT NOT NULL DEFAULT '',
			enabled bool NOT NULL DEFAULT true,
			description TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT 'manual',
			created_at %s NOT NULL,
			updated_at %s NOT NULL
		);`, createdAtType, updatedAtType),
		`CREATE INDEX IF NOT EXISTS idx_model_aliases_alias ON model_aliases(alias);`,
		`CREATE INDEX IF NOT EXISTS idx_model_aliases_target ON model_aliases(target_model);`,
		`CREATE INDEX IF NOT EXISTS idx_model_aliases_channel ON model_aliases(channel_id);`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ensureLogExchangeColumns() error {
	if err := s.ensureColumn("logs", "request_audit_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("logs", "response_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("logs", "exchange_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("logs", "exchange_kind", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("logs", "exchange_role", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("logs", "parent_exchange_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := s.ensureColumn("logs", "sequence_index", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS tracelog_request_audit_id_recorded_at ON logs(request_audit_id, recorded_at)`,
		`CREATE INDEX IF NOT EXISTS tracelog_exchange_kind_recorded_at ON logs(exchange_kind, recorded_at)`,
		`CREATE INDEX IF NOT EXISTS tracelog_parent_exchange_id ON logs(parent_exchange_id)`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ensureHotpathIndexes() error {
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_logs_trace_id_hotpath ON logs(trace_id)`,
		`CREATE INDEX IF NOT EXISTS tracelog_session_recorded_trace ON logs(session_id, recorded_at DESC, trace_id DESC) WHERE session_id <> ''`,
		`CREATE INDEX IF NOT EXISTS idx_parse_jobs_status_trace ON parse_jobs(status, trace_id)`,
		`CREATE INDEX IF NOT EXISTS idx_system_events_last_seen_id ON system_events(last_seen_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_system_events_status_last_seen_id ON system_events(status, last_seen_at DESC, id DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_system_events_source_category_last_seen_id ON system_events(source, category, last_seen_at DESC, id DESC)`,
		// The same five indexes the versioned Postgres migrations add, so the
		// two schemas keep serving the same access paths.
		`CREATE INDEX IF NOT EXISTS tracelog_selected_upstream_id_recorded_at ON logs(selected_upstream_id, recorded_at)`,
		`CREATE INDEX IF NOT EXISTS requestaudit_created_at_id ON request_audits(created_at, id)`,
		`CREATE INDEX IF NOT EXISTS toolcallaudit_created_at_id ON tool_call_audits(created_at, id)`,
		`CREATE INDEX IF NOT EXISTS analysisrun_created_at_id ON analysis_runs(created_at, id)`,
		`CREATE INDEX IF NOT EXISTS tracefinding_severity_created_at ON trace_findings(severity, created_at)`,
	} {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	return nil
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

func (s *Store) sessionIDForPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	var sessionID string
	if err := s.db.QueryRow(`SELECT session_id FROM logs WHERE path = ?`, path).Scan(&sessionID); err != nil {
		return ""
	}
	return sessionID
}

func (s *Store) refreshSessionSummariesBestEffort(sessionIDs ...string) {
	seen := map[string]struct{}{}
	for _, sessionID := range sessionIDs {
		sessionID = strings.TrimSpace(sessionID)
		if sessionID == "" {
			continue
		}
		if _, ok := seen[sessionID]; ok {
			continue
		}
		seen[sessionID] = struct{}{}
		if err := s.RebuildSessionSummary(sessionID); err != nil {
			fmt.Fprintf(os.Stderr, "trajecta: refresh session summary %q failed: %v\n", sessionID, err)
		}
	}
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
			fmt.Fprintf(os.Stderr, "trajecta: resolve deferred derived refresh failed: %v\n", err)
		} else {
			paths = dedupeNonEmptyStrings(append(paths, resolvedPaths...))
			sessions = dedupeNonEmptyStrings(append(sessions, resolvedSessions...))
		}
	}
	s.refreshOverviewMetricBucketsBestEffort(paths)
	s.refreshSessionSummariesBestEffort(sessions...)
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

func (s *Store) RebuildSessionSummary(sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := s.execTx(tx, `DELETE FROM session_summaries WHERE session_id = ?`, sessionID); err != nil {
		return err
	}
	if _, err := s.execTx(tx, s.insertSessionSummaryFromLogsSQL(`s.session_id = ? AND `+clientVisibleLogClause("s")), sessionID); err != nil {
		return err
	}
	return tx.Commit()
}

// SessionSummaryRebuildStats reports the summary table as it is, without
// applying anything the write path deferred: it is the dry-run input of
// `db summary rebuild sessions`, and the operator needs to see existing drift
// rather than have it repaired before it is counted.
func (s *Store) SessionSummaryRebuildStats(sessionID string) (SessionSummaryRebuildStats, error) {
	sessionID = strings.TrimSpace(sessionID)
	stats := SessionSummaryRebuildStats{SessionID: sessionID}
	if sessionID == "" {
		stats.WouldDeleteAll = true
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM session_summaries`).Scan(&stats.ExistingCount); err != nil {
			return stats, err
		}
		if err := s.db.QueryRow(`SELECT COUNT(DISTINCT session_id) FROM logs s WHERE s.session_id <> '' AND ` + clientVisibleLogClause("s")).Scan(&stats.CandidateCount); err != nil {
			return stats, err
		}
		return stats, nil
	}
	stats.WouldDeleteOne = true
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM session_summaries WHERE session_id = ?`, sessionID).Scan(&stats.ExistingCount); err != nil {
		return stats, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(DISTINCT session_id) FROM logs s WHERE s.session_id = ? AND `+clientVisibleLogClause("s"), sessionID).Scan(&stats.CandidateCount); err != nil {
		return stats, err
	}
	return stats, nil
}

func (s *Store) RebuildSessionSummaries() error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := s.execTx(tx, `DELETE FROM session_summaries`); err != nil {
		return err
	}
	if _, err := s.execTx(tx, s.insertSessionSummaryFromLogsSQL(clientVisibleLogClause("s"))); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) insertSessionSummaryFromLogsSQL(whereSQL string) string {
	whereSQL = andSQL(`s.session_id <> ''`, whereSQL)
	return `
		INSERT INTO session_summaries (
			session_id, session_source, request_count, first_seen, last_seen, last_model, providers,
			success_request, failed_request, success_rate, total_tokens, avg_ttft, total_duration, stream_count, updated_at
		)
		SELECT
			s.session_id,
			MIN(s.session_source) AS session_source,
			COUNT(*) AS request_count,
			MIN(s.recorded_at) AS first_seen,
			MAX(s.recorded_at) AS last_seen,
			COALESCE((
				SELECT model FROM logs l2
				WHERE l2.session_id = s.session_id
					AND ` + clientVisibleLogClause("l2") + `
				ORDER BY l2.recorded_at DESC, l2.trace_id DESC
				LIMIT 1
			), '') AS last_model,
			` + s.sessionProvidersAggregateSQL() + ` AS providers,
			COALESCE(SUM(CASE WHEN s.status_code BETWEEN 200 AND 299 THEN 1 ELSE 0 END), 0) AS success_request,
			COALESCE(SUM(CASE WHEN s.status_code NOT BETWEEN 200 AND 299 THEN 1 ELSE 0 END), 0) AS failed_request,
			CASE WHEN COUNT(*) = 0 THEN 0 ELSE
				100.0 * SUM(CASE WHEN s.status_code BETWEEN 200 AND 299 THEN 1 ELSE 0 END) / COUNT(*)
			END AS success_rate,
			COALESCE(SUM(CASE WHEN s.status_code BETWEEN 200 AND 299 THEN s.total_tokens ELSE 0 END), 0) AS total_tokens,
			COALESCE(AVG(CASE WHEN s.status_code BETWEEN 200 AND 299 THEN s.ttft_ms END), 0) AS avg_ttft,
			COALESCE(SUM(s.duration_ms), 0) AS total_duration,
			COALESCE(SUM(` + s.boolCountCaseSQL("s.is_stream") + `), 0) AS stream_count,
			CURRENT_TIMESTAMP AS updated_at
		FROM logs s
		WHERE ` + whereSQL + `
		GROUP BY s.session_id
	`
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

		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		parsed, err := recordfile.ParsePrelude(content)
		if err != nil {
			if shouldSkipIncompleteRecord(content, err) {
				return nil
			}
			return fmt.Errorf("parse %s: %w", path, err)
		}

		grouping, err := ExtractGroupingInfo(content, parsed)
		if err != nil {
			return fmt.Errorf("extract grouping %s: %w", path, err)
		}

		return s.upsertLogWithGrouping(path, parsed.Header, grouping)
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

func shouldSkipIncompleteRecord(content []byte, err error) bool {
	if err == nil {
		return false
	}

	trimmed := bytes.TrimSpace(content)
	if len(trimmed) == 0 {
		return true
	}

	if recordfile.HasFileMagic(trimmed) {
		errText := err.Error()
		return strings.Contains(errText, "failed to read prelude") ||
			strings.Contains(errText, "missing v3 meta line") ||
			strings.Contains(errText, "invalid v3")
	}

	httpMethods := [][]byte{
		[]byte("GET "),
		[]byte("POST "),
		[]byte("PUT "),
		[]byte("PATCH "),
		[]byte("DELETE "),
		[]byte("HEAD "),
		[]byte("OPTIONS "),
	}
	for _, method := range httpMethods {
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

func (s *Store) populateObservationMetadata(entries []LogEntry) error {
	if len(entries) == 0 {
		return nil
	}
	traceIDs := make([]string, 0, len(entries))
	byTraceID := make(map[string]int, len(entries))
	for idx := range entries {
		entries[idx].Observation.Status = "unparsed"
		traceIDs = append(traceIDs, entries[idx].ID)
		byTraceID[entries[idx].ID] = idx
	}
	meta, err := s.LoadObservationMetadata(traceIDs)
	if err != nil {
		return err
	}
	for traceID, observation := range meta {
		idx, ok := byTraceID[traceID]
		if !ok {
			continue
		}
		entries[idx].Observation = observation
	}
	return nil
}

func (s *Store) LoadObservationMetadata(traceIDs []string) (map[string]ObservationMetadata, error) {
	out := make(map[string]ObservationMetadata, len(traceIDs))
	if len(traceIDs) == 0 {
		return out, nil
	}
	args := make([]any, 0, len(traceIDs))
	for _, traceID := range traceIDs {
		traceID = strings.TrimSpace(traceID)
		if traceID == "" {
			continue
		}
		out[traceID] = ObservationMetadata{Status: "unparsed"}
		args = append(args, traceID)
	}
	if len(args) == 0 {
		return out, nil
	}
	placeholders := strings.TrimRight(strings.Repeat("?,", len(args)), ",")
	rows, err := s.db.Query(`
		SELECT trace_id, parser, parser_version, status, updated_at
		FROM trace_observations
		WHERE trace_id IN (`+placeholders+`)
	`, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var (
			traceID   string
			meta      ObservationMetadata
			updatedAt any
		)
		if err := rows.Scan(&traceID, &meta.Parser, &meta.ParserVersion, &meta.Status, &updatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		if meta.UpdatedAt, err = timeParseValue(updatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out[traceID] = meta
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	missingArgs := make([]any, 0, len(out))
	for traceID, meta := range out {
		if strings.TrimSpace(meta.Parser) == "" {
			missingArgs = append(missingArgs, traceID)
		}
	}
	if len(missingArgs) == 0 {
		return out, nil
	}
	missingPlaceholders := strings.TrimRight(strings.Repeat("?,", len(missingArgs)), ",")
	rows, err = s.db.Query(`
		SELECT p.trace_id, p.status, p.updated_at
		FROM parse_jobs p
		INNER JOIN (
			SELECT trace_id, MAX(id) AS id
			FROM parse_jobs
			WHERE trace_id IN (`+missingPlaceholders+`)
			GROUP BY trace_id
		) latest ON latest.id = p.id
	`, missingArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			traceID   string
			meta      ObservationMetadata
			updatedAt any
		)
		if err := rows.Scan(&traceID, &meta.Status, &updatedAt); err != nil {
			return nil, err
		}
		if meta.UpdatedAt, err = timeParseValue(updatedAt); err != nil {
			return nil, err
		}
		if strings.TrimSpace(meta.Status) == "" {
			meta.Status = "unparsed"
		}
		out[traceID] = meta
	}
	return out, rows.Err()
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

func (s *Store) SaveObservation(obs observe.TraceObservation) error {
	if obs.TraceID == "" {
		return errors.New("save observation: trace id is required")
	}
	now := time.Now().UTC()
	warningsJSON, err := json.Marshal(obs.Warnings)
	if err != nil {
		return err
	}
	summaryJSON, err := json.Marshal(observationSummaryJSON(obs))
	if err != nil {
		return err
	}
	nodes := observationFlatNodes(obs)

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := s.execTx(tx, `
		INSERT INTO parser_versions (parser, version, created_at)
		VALUES (?, ?, ?)
		ON CONFLICT(parser, version) DO NOTHING
	`, obs.Parser, obs.ParserVersion, now); err != nil {
		return err
	}
	if _, err := s.execTx(tx, `
		INSERT INTO trace_observations (
			trace_id, parser, parser_version, status, provider, operation, model,
			exchange_kind, exchange_role, parent_exchange_id, sequence_index, request_audit_id, response_id,
			summary_json, warnings_json, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(trace_id) DO UPDATE SET
			parser=excluded.parser,
			parser_version=excluded.parser_version,
			status=excluded.status,
			provider=excluded.provider,
			operation=excluded.operation,
			model=excluded.model,
			exchange_kind=excluded.exchange_kind,
			exchange_role=excluded.exchange_role,
			parent_exchange_id=excluded.parent_exchange_id,
			sequence_index=excluded.sequence_index,
			request_audit_id=excluded.request_audit_id,
			response_id=excluded.response_id,
			summary_json=excluded.summary_json,
			warnings_json=excluded.warnings_json,
			updated_at=excluded.updated_at
	`, sqlSafeText(obs.TraceID), sqlSafeText(obs.Parser), sqlSafeText(obs.ParserVersion), string(obs.Status), sqlSafeText(obs.Provider), sqlSafeText(obs.Operation), sqlSafeText(obs.Model),
		sqlSafeText(obs.ExchangeKind), sqlSafeText(obs.ExchangeRole), sqlSafeText(obs.ParentExchangeID), obs.SequenceIndex, sqlSafeText(obs.RequestAuditID), sqlSafeText(obs.ResponseID),
		sqlSafeBytes(summaryJSON), sqlSafeBytes(warningsJSON), now, now); err != nil {
		return err
	}
	if _, err := s.execTx(tx, `DELETE FROM semantic_nodes WHERE trace_id = ?`, obs.TraceID); err != nil {
		return err
	}
	if err := s.insertSemanticNodesTx(tx, obs.TraceID, nodes, now); err != nil {
		return err
	}
	// One parse job per trace: the observation result updates the job that
	// EnqueueParseJob queued (or creates it when a trace was parsed without
	// one). Inserting a second row here made every parsed trace appear twice in
	// parse_jobs and doubled the queue table with rows no worker ever claimed.
	if _, err := s.execTx(tx, `
		UPDATE parse_jobs
		SET status = ?, attempts = CASE WHEN attempts = 0 THEN 1 ELSE attempts END, last_error = '', updated_at = ?
		WHERE trace_id = ?
	`, string(obs.Status), now, obs.TraceID); err != nil {
		return err
	}
	if _, err := s.execTx(tx, `
		INSERT INTO parse_jobs (trace_id, status, attempts, created_at, updated_at)
		SELECT ?, ?, 1, ?, ?
		WHERE NOT EXISTS (SELECT 1 FROM parse_jobs WHERE trace_id = ?)
	`, obs.TraceID, string(obs.Status), now, now, obs.TraceID); err != nil {
		return err
	}
	return tx.Commit()
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

func (s *Store) GetObservationSummary(traceID string) (ObservationSummary, error) {
	var summary ObservationSummary
	var createdAt, updatedAt any
	err := s.db.QueryRow(`
		SELECT trace_id, parser, parser_version, status, provider, operation, model,
			exchange_kind, exchange_role, parent_exchange_id, sequence_index, request_audit_id, response_id,
			summary_json, warnings_json, created_at, updated_at
		FROM trace_observations
		WHERE trace_id = ?
	`, traceID).Scan(
		&summary.TraceID,
		&summary.Parser,
		&summary.ParserVersion,
		&summary.Status,
		&summary.Provider,
		&summary.Operation,
		&summary.Model,
		&summary.ExchangeKind,
		&summary.ExchangeRole,
		&summary.ParentExchangeID,
		&summary.SequenceIndex,
		&summary.RequestAuditID,
		&summary.ResponseID,
		&summary.SummaryJSON,
		&summary.WarningsJSON,
		&createdAt,
		&updatedAt,
	)
	if err != nil {
		return ObservationSummary{}, err
	}
	if summary.CreatedAt, err = timeParseValue(createdAt); err != nil {
		return ObservationSummary{}, err
	}
	if summary.UpdatedAt, err = timeParseValue(updatedAt); err != nil {
		return ObservationSummary{}, err
	}
	return summary, nil
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

func (s *Store) GetObservation(traceID string) (observe.TraceObservation, error) {
	summary, err := s.GetObservationSummary(traceID)
	if err != nil {
		return observe.TraceObservation{}, err
	}
	nodes, err := s.ListSemanticNodes(traceID)
	if err != nil {
		return observe.TraceObservation{}, err
	}
	var warnings []observe.ParseWarning
	if strings.TrimSpace(summary.WarningsJSON) != "" {
		_ = json.Unmarshal([]byte(summary.WarningsJSON), &warnings)
	}
	return observe.TraceObservation{
		TraceID:          summary.TraceID,
		Provider:         summary.Provider,
		Operation:        summary.Operation,
		Model:            summary.Model,
		ExchangeKind:     summary.ExchangeKind,
		ExchangeRole:     summary.ExchangeRole,
		ParentExchangeID: summary.ParentExchangeID,
		SequenceIndex:    summary.SequenceIndex,
		RequestAuditID:   summary.RequestAuditID,
		ResponseID:       summary.ResponseID,
		Parser:           summary.Parser,
		ParserVersion:    summary.ParserVersion,
		Status:           observe.ParseStatus(summary.Status),
		Warnings:         warnings,
		Response: observe.ObservationResponse{
			Nodes: observe.RebuildNodeTree(nodes),
		},
	}, nil
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

func (s *Store) EnqueueParseJob(traceID string) error {
	if strings.TrimSpace(traceID) == "" {
		return errors.New("enqueue parse job: trace id is required")
	}
	now := time.Now().UTC()
	// `(trace_id)` is unique, so re-enqueueing an existing trace resets that
	// job to queued instead of piling up a second row.
	_, err := s.db.Exec(`
		INSERT INTO parse_jobs (trace_id, status, attempts, created_at, updated_at)
		VALUES (?, 'queued', 0, ?, ?)
		ON CONFLICT (trace_id) DO UPDATE
		SET status = 'queued', attempts = 0, last_error = '', updated_at = excluded.updated_at
	`, traceID, now, now)
	return err
}

func (s *Store) ListParseJobs(status string, limit int) ([]ParseJobRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`
		SELECT id, trace_id, status, attempts, last_error, created_at, updated_at
		FROM parse_jobs
		WHERE status = ?
		ORDER BY updated_at ASC, id ASC
		LIMIT ?
	`, status, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanParseJobs(rows)
}

func (s *Store) getParseJob(id int64) (ParseJobRecord, error) {
	row := s.db.QueryRow(`
		SELECT id, trace_id, status, attempts, last_error, created_at, updated_at
		FROM parse_jobs
		WHERE id = ?
	`, id)
	var job ParseJobRecord
	var createdAt, updatedAt any
	if err := row.Scan(&job.ID, &job.TraceID, &job.Status, &job.Attempts, &job.LastError, &createdAt, &updatedAt); err != nil {
		return ParseJobRecord{}, err
	}
	var err error
	if job.CreatedAt, err = timeParseValue(createdAt); err != nil {
		return ParseJobRecord{}, err
	}
	if job.UpdatedAt, err = timeParseValue(updatedAt); err != nil {
		return ParseJobRecord{}, err
	}
	return job, nil
}

func (s *Store) MarkParseJobRunning(id int64) error {
	_, err := s.db.Exec(`
		UPDATE parse_jobs
		SET status = 'running', attempts = attempts + 1, updated_at = ?
		WHERE id = ?
	`, time.Now().UTC(), id)
	return err
}

// ClaimParseJobs moves up to limit queued parse jobs to running in one statement
// and returns them. The worker used to list queued jobs and then mark each one
// running, which let a second process (another `trajecta serve`, or a CLI run)
// pick up the same job in between. The claim is a single UPDATE with RETURNING:
// Postgres takes the oldest queued rows with FOR UPDATE SKIP LOCKED so concurrent
// claimers step over each other's rows, SQLite has no row locks so its statement
// relies on the database write lock plus claimMu and gives up the row when the
// outer status predicate no longer matches.
func (s *Store) ClaimParseJobs(limit int) ([]ParseJobRecord, error) {
	if limit <= 0 {
		limit = 10
	}
	now := time.Now().UTC()
	s.shared.claimMu.Lock()
	defer s.shared.claimMu.Unlock()
	rows, err := s.db.Query(`
		UPDATE parse_jobs
		SET status = 'running', attempts = attempts + 1, updated_at = ?
		WHERE status = 'queued' AND id IN (
			SELECT id FROM parse_jobs
			WHERE status = 'queued'
			ORDER BY updated_at ASC, id ASC
			LIMIT ?`+s.claimRowLockSQL()+`
		)
		RETURNING id, trace_id, status, attempts, last_error, created_at, updated_at
	`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanParseJobs(rows)
}

func (s *Store) MarkParseJobDone(id int64) error {
	_, err := s.db.Exec(`
		UPDATE parse_jobs
		SET status = 'parsed', last_error = '', updated_at = ?
		WHERE id = ?
	`, time.Now().UTC(), id)
	return err
}

func (s *Store) MarkParseJobFailed(id int64, lastError string) error {
	_, err := s.db.Exec(`
		UPDATE parse_jobs
		SET status = 'failed', last_error = ?, updated_at = ?
		WHERE id = ?
	`, textPreview(lastError, 2000), time.Now().UTC(), id)
	if err != nil {
		return err
	}
	job, err := s.getParseJob(id)
	if err != nil {
		return err
	}
	_, err = s.UpsertSystemEvent(systemEventForParseFailure(job))
	return err
}

func (s *Store) UpsertSystemEvent(event SystemEvent) (SystemEvent, error) {
	event.Fingerprint = strings.TrimSpace(event.Fingerprint)
	if event.Fingerprint == "" {
		return SystemEvent{}, errors.New("upsert system event: fingerprint is required")
	}
	event.Source = strings.TrimSpace(event.Source)
	if event.Source == "" {
		return SystemEvent{}, errors.New("upsert system event: source is required")
	}
	event.Category = strings.TrimSpace(event.Category)
	if event.Category == "" {
		return SystemEvent{}, errors.New("upsert system event: category is required")
	}
	event.Severity = strings.TrimSpace(event.Severity)
	if event.Severity == "" {
		event.Severity = "error"
	}
	now := time.Now().UTC()
	if event.ID == "" {
		event.ID = uuid.NewString()
	}
	if event.FirstSeenAt.IsZero() {
		event.FirstSeenAt = now
	}
	if event.LastSeenAt.IsZero() {
		event.LastSeenAt = now
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = now
	}
	event.UpdatedAt = now
	if event.Status == "" {
		event.Status = SystemEventStatusUnread
	}
	if event.OccurrenceCount <= 0 {
		event.OccurrenceCount = 1
	}
	detailsJSON := strings.TrimSpace(string(event.DetailsJSON))
	if detailsJSON == "" {
		detailsJSON = "{}"
	}

	_, err := s.db.Exec(`
		INSERT INTO system_events (
			id, fingerprint, source, category, severity, status, title, message, details_json,
			trace_id, session_id, job_id, upstream_id, model, occurrence_count,
			first_seen_at, last_seen_at, created_at, updated_at, read_at, resolved_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL)
		ON CONFLICT(fingerprint) DO UPDATE SET
			source=excluded.source,
			category=excluded.category,
			severity=excluded.severity,
			status=CASE
				WHEN system_events.status = 'ignored' THEN system_events.status
				ELSE 'unread'
			END,
			title=excluded.title,
			message=excluded.message,
			details_json=excluded.details_json,
			trace_id=excluded.trace_id,
			session_id=excluded.session_id,
			job_id=excluded.job_id,
			upstream_id=excluded.upstream_id,
			model=excluded.model,
			occurrence_count=system_events.occurrence_count + 1,
			last_seen_at=excluded.last_seen_at,
			updated_at=excluded.updated_at,
			read_at=CASE
				WHEN system_events.status = 'ignored' THEN system_events.read_at
				ELSE NULL
			END,
			resolved_at=CASE
				WHEN system_events.status = 'ignored' THEN system_events.resolved_at
				ELSE NULL
			END
	`, sanitizeDBText(event.ID), sanitizeDBText(event.Fingerprint), sanitizeDBText(event.Source),
		sanitizeDBText(event.Category), sanitizeDBText(event.Severity), sanitizeDBText(event.Status),
		sanitizeDBText(textPreview(event.Title, 300)), sanitizeDBText(textPreview(event.Message, 2000)),
		sanitizeDBText(detailsJSON),
		sanitizeDBText(event.TraceID), sanitizeDBText(event.SessionID), sanitizeDBText(event.JobID),
		sanitizeDBText(event.UpstreamID), sanitizeDBText(event.Model), event.OccurrenceCount,
		event.FirstSeenAt, event.LastSeenAt, event.CreatedAt, event.UpdatedAt)
	if err != nil {
		return SystemEvent{}, err
	}
	saved, err := s.GetSystemEventByFingerprint(event.Fingerprint)
	if err != nil {
		return SystemEvent{}, err
	}
	s.notifySystemEventChanged(saved)
	return saved, nil
}

func (s *Store) GetSystemEvent(id string) (SystemEvent, error) {
	return s.getSystemEvent(`id = ?`, strings.TrimSpace(id))
}

func (s *Store) GetSystemEventByFingerprint(fingerprint string) (SystemEvent, error) {
	return s.getSystemEvent(`fingerprint = ?`, strings.TrimSpace(fingerprint))
}

func (s *Store) ListSystemEvents(filter SystemEventFilter) (SystemEventPageResult, error) {
	page, pageSize := normalizePage(filter.Page, filter.PageSize)
	whereSQL, args := buildSystemEventFilterClause(filter)
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM system_events WHERE `+whereSQL, args...).Scan(&total); err != nil {
		return SystemEventPageResult{}, err
	}
	queryArgs := append([]any{}, args...)
	offsetSQL := ` OFFSET ?`
	if strings.TrimSpace(filter.After) != "" {
		cursor, err := decodeSystemEventCursor(filter.After)
		if err != nil {
			return SystemEventPageResult{}, err
		}
		whereSQL += ` AND (last_seen_at < ? OR (last_seen_at = ? AND id < ?))`
		cursorLastSeen := cursor.LastSeenAt.UTC()
		queryArgs = append(queryArgs, cursorLastSeen, cursorLastSeen, cursor.ID)
		offsetSQL = ``
	}
	queryArgs = append(queryArgs, pageSize+1)
	if offsetSQL != "" {
		queryArgs = append(queryArgs, (page-1)*pageSize)
	}
	rows, err := s.db.Query(`
		SELECT id, fingerprint, source, category, severity, status, title, message, details_json,
			trace_id, session_id, job_id, upstream_id, model, occurrence_count,
			first_seen_at, last_seen_at, created_at, updated_at, read_at, resolved_at
		FROM system_events
		WHERE `+whereSQL+`
		ORDER BY last_seen_at DESC, id DESC
		LIMIT ?`+offsetSQL+`
	`, queryArgs...)
	if err != nil {
		return SystemEventPageResult{}, err
	}
	defer rows.Close()
	items, err := scanSystemEvents(rows)
	if err != nil {
		return SystemEventPageResult{}, err
	}
	hasMore := len(items) > pageSize
	if hasMore {
		items = items[:pageSize]
	}
	nextCursor := ""
	if hasMore && len(items) > 0 {
		nextCursor, err = encodeSystemEventCursor(items[len(items)-1])
		if err != nil {
			return SystemEventPageResult{}, err
		}
	}
	return SystemEventPageResult{
		Items:      items,
		Total:      total,
		Page:       page,
		PageSize:   pageSize,
		TotalPages: totalPages(total, pageSize),
		NextCursor: nextCursor,
		HasMore:    hasMore,
	}, nil
}

func (s *Store) SystemEventSummary(since time.Time) (SystemEventSummary, error) {
	whereSQL := `1 = 1`
	var args []any
	if !since.IsZero() {
		whereSQL = `last_seen_at >= ?`
		args = append(args, since.UTC().Format(timeLayout))
	}
	var summary SystemEventSummary
	var lastSeen any
	if err := s.db.QueryRow(`
		SELECT
			COUNT(*) AS total,
			COALESCE(SUM(CASE WHEN status = 'unread' THEN 1 ELSE 0 END), 0) AS unread,
			COALESCE(SUM(CASE WHEN severity = 'critical' AND status = 'unread' THEN 1 ELSE 0 END), 0) AS critical,
			COALESCE(SUM(CASE WHEN severity = 'error' AND status = 'unread' THEN 1 ELSE 0 END), 0) AS error,
			COALESCE(SUM(CASE WHEN severity = 'warning' AND status = 'unread' THEN 1 ELSE 0 END), 0) AS warning,
			MAX(last_seen_at) AS last_seen_at
		FROM system_events
		WHERE `+whereSQL, args...).Scan(&summary.Total, &summary.Unread, &summary.Critical, &summary.Error, &summary.Warning, &lastSeen); err != nil {
		return SystemEventSummary{}, err
	}
	var err error
	summary.LastSeenAt, err = timeParseNullableValue(lastSeen)
	if err != nil {
		return SystemEventSummary{}, err
	}
	summary.BySource, err = s.systemEventCountBy("source", whereSQL, args, 10)
	if err != nil {
		return SystemEventSummary{}, err
	}
	summary.ByCategory, err = s.systemEventCountBy("category", whereSQL, args, 10)
	if err != nil {
		return SystemEventSummary{}, err
	}
	return summary, nil
}

func (s *Store) MarkSystemEventRead(id string) error {
	now := time.Now().UTC()
	_, err := s.db.Exec(`
		UPDATE system_events
		SET status = 'read', read_at = ?, updated_at = ?
		WHERE id = ? AND status != 'ignored'
	`, now, now, strings.TrimSpace(id))
	if err == nil {
		s.notifySystemEventIDChanged(id)
	}
	return err
}

func (s *Store) MarkAllSystemEventsRead(filter SystemEventFilter) (int, error) {
	whereSQL, args := buildSystemEventFilterClause(filter)
	now := time.Now().UTC()
	args = append([]any{now, now}, args...)
	result, err := s.db.Exec(`
		UPDATE system_events
		SET status = 'read', read_at = ?, updated_at = ?
		WHERE status != 'ignored' AND `+whereSQL, args...)
	if err != nil {
		return 0, err
	}
	count, err := result.RowsAffected()
	if err == nil && count > 0 {
		s.notifySystemEventChanged(SystemEvent{Status: SystemEventStatusRead})
	}
	return int(count), err
}

func (s *Store) ResolveSystemEvent(id string) error {
	now := time.Now().UTC()
	_, err := s.db.Exec(`
		UPDATE system_events
		SET status = 'resolved', resolved_at = ?, updated_at = ?
		WHERE id = ?
	`, now, now, strings.TrimSpace(id))
	if err == nil {
		s.notifySystemEventIDChanged(id)
	}
	return err
}

func (s *Store) IgnoreSystemEvent(id string) error {
	_, err := s.db.Exec(`
		UPDATE system_events
		SET status = 'ignored', updated_at = ?
		WHERE id = ?
	`, time.Now().UTC(), strings.TrimSpace(id))
	if err == nil {
		s.notifySystemEventIDChanged(id)
	}
	return err
}

func (s *Store) SubscribeSystemEvents(buffer int) (<-chan SystemEventNotification, func()) {
	if buffer <= 0 {
		buffer = 8
	}
	ch := make(chan SystemEventNotification, buffer)
	s.shared.eventMu.Lock()
	if s.shared.eventSubs == nil {
		s.shared.eventSubs = map[chan SystemEventNotification]struct{}{}
	}
	s.shared.eventSubs[ch] = struct{}{}
	s.shared.eventMu.Unlock()
	return ch, func() {
		s.shared.eventMu.Lock()
		if _, ok := s.shared.eventSubs[ch]; ok {
			delete(s.shared.eventSubs, ch)
			close(ch)
		}
		s.shared.eventMu.Unlock()
	}
}

func (s *Store) SaveFindings(traceID string, findings []observe.Finding) error {
	if strings.TrimSpace(traceID) == "" {
		return errors.New("save findings: trace id is required")
	}
	now := time.Now().UTC()
	normalized := make([]observe.Finding, 0, len(findings))
	for _, finding := range findings {
		if finding.ID == "" {
			return errors.New("save findings: finding id is required")
		}
		if finding.CreatedAt.IsZero() {
			finding.CreatedAt = now
		}
		if finding.TraceID == "" {
			finding.TraceID = traceID
		}
		normalized = append(normalized, finding)
	}
	// A multi-row INSERT cannot name the same (trace_id, finding_id) twice:
	// Postgres fails the whole statement, and the row-by-row form it replaces
	// failed on the second row anyway. Fold duplicates first, last one wins, so a
	// reanalysis that emits the same finding twice still writes one row.
	unique := dedupeFindingsByKey(normalized)

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := s.execTx(tx, `DELETE FROM trace_findings WHERE trace_id = ?`, traceID); err != nil {
		return err
	}
	// One statement per parameter-bounded chunk of findings instead of one per
	// finding; a session reanalysis used to issue hundreds of inserts.
	const findingColumns = 13
	rowsPerStatement := storeSQLParamChunk / findingColumns
	valueRow := "(" + placeholders(findingColumns) + ")"
	for start := 0; start < len(unique); start += rowsPerStatement {
		end := start + rowsPerStatement
		if end > len(unique) {
			end = len(unique)
		}
		chunk := unique[start:end]
		values := make([]string, 0, len(chunk))
		args := make([]any, 0, len(chunk)*findingColumns)
		for _, finding := range chunk {
			values = append(values, valueRow)
			args = append(args, finding.TraceID, finding.ID, finding.Category, string(finding.Severity), finding.Confidence,
				finding.Title, finding.Description, finding.EvidencePath, textPreview(finding.EvidenceExcerpt, 500),
				finding.NodeID, finding.Detector, finding.DetectorVersion, finding.CreatedAt)
		}
		if _, err := s.execTx(tx, `
			INSERT INTO trace_findings (
				trace_id, finding_id, category, severity, confidence, title, description,
				evidence_path, evidence_excerpt, node_id, detector, detector_version, created_at
			) VALUES `+strings.Join(values, ", "), args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// dedupeFindingsByKey folds findings that share the (trace_id, finding_id)
// unique key, keeping the last occurrence in input order.
func dedupeFindingsByKey(findings []observe.Finding) []observe.Finding {
	if len(findings) < 2 {
		return findings
	}
	out := make([]observe.Finding, 0, len(findings))
	at := make(map[string]int, len(findings))
	for _, finding := range findings {
		key := finding.TraceID + "\x00" + finding.ID
		if index, ok := at[key]; ok {
			out[index] = finding
			continue
		}
		at[key] = len(out)
		out = append(out, finding)
	}
	return out
}

func (s *Store) ListFindings(traceID string, filter FindingFilter) ([]observe.Finding, error) {
	if strings.TrimSpace(traceID) == "" {
		return nil, errors.New("list findings: trace id is required")
	}
	query := `
		SELECT finding_id, trace_id, category, severity, confidence, title, description,
			evidence_path, evidence_excerpt, node_id, detector, detector_version, created_at
		FROM trace_findings
		WHERE trace_id = ?
	`
	args := []any{traceID}
	if category := strings.TrimSpace(filter.Category); category != "" {
		query += ` AND category = ?`
		args = append(args, category)
	}
	if severity := strings.TrimSpace(filter.Severity); severity != "" {
		query += ` AND severity = ?`
		args = append(args, severity)
	}
	query += ` ORDER BY id ASC`

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []observe.Finding
	for rows.Next() {
		finding, err := scanFindingRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, finding)
	}
	return out, rows.Err()
}

// ListFindingsByTraceIDs groups the findings of the given traces by trace id, in
// chunks the database accepts. The batch reanalysis used to call ListFindings
// once per trace of the session.
func (s *Store) ListFindingsByTraceIDs(traceIDs []string) (map[string][]observe.Finding, error) {
	out := make(map[string][]observe.Finding, len(traceIDs))
	for _, chunk := range chunkStrings(dedupeNonEmptyStrings(traceIDs), storeSQLParamChunk) {
		rows, err := s.db.Query(`
			SELECT finding_id, trace_id, category, severity, confidence, title, description,
				evidence_path, evidence_excerpt, node_id, detector, detector_version, created_at
			FROM trace_findings
			WHERE trace_id IN (`+placeholders(len(chunk))+`)
			ORDER BY id ASC
		`, stringArgs(chunk)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			finding, err := scanFindingRow(rows)
			if err != nil {
				rows.Close()
				return nil, err
			}
			out[finding.TraceID] = append(out[finding.TraceID], finding)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// scanFindingRow reads one trace_findings row from the shared column list.
func scanFindingRow(rows *sql.Rows) (observe.Finding, error) {
	var finding observe.Finding
	var severity string
	var createdAt any
	if err := rows.Scan(&finding.ID, &finding.TraceID, &finding.Category, &severity, &finding.Confidence, &finding.Title,
		&finding.Description, &finding.EvidencePath, &finding.EvidenceExcerpt, &finding.NodeID,
		&finding.Detector, &finding.DetectorVersion, &createdAt); err != nil {
		return observe.Finding{}, err
	}
	finding.Severity = observe.Severity(severity)
	parsed, err := timeParseValue(createdAt)
	if err != nil {
		return observe.Finding{}, err
	}
	finding.CreatedAt = parsed
	return finding, nil
}

func (s *Store) ListAllFindings(filter FindingFilter, limit int) ([]observe.Finding, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `
		SELECT finding_id, trace_id, category, severity, confidence, title, description,
			evidence_path, evidence_excerpt, node_id, detector, detector_version, created_at
		FROM trace_findings
		WHERE 1 = 1
	`
	var args []any
	if category := strings.TrimSpace(filter.Category); category != "" {
		query += ` AND category = ?`
		args = append(args, category)
	}
	if severity := strings.TrimSpace(filter.Severity); severity != "" {
		query += ` AND severity = ?`
		args = append(args, severity)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanFindings(rows)
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

func (s *Store) CreateAnalysisJob(job AnalysisJobRecord) (AnalysisJobRecord, error) {
	if strings.TrimSpace(job.JobType) == "" {
		return AnalysisJobRecord{}, errors.New("create analysis job: job type is required")
	}
	if strings.TrimSpace(job.TargetType) == "" {
		return AnalysisJobRecord{}, errors.New("create analysis job: target type is required")
	}
	if strings.TrimSpace(job.TargetID) == "" {
		return AnalysisJobRecord{}, errors.New("create analysis job: target id is required")
	}
	if strings.TrimSpace(job.Status) == "" {
		job.Status = "queued"
	}
	if strings.TrimSpace(job.StepsJSON) == "" {
		job.StepsJSON = "[]"
	}
	if strings.TrimSpace(job.RequestJSON) == "" {
		job.RequestJSON = "{}"
	}
	if strings.TrimSpace(job.ResultJSON) == "" {
		job.ResultJSON = "{}"
	}
	now := time.Now().UTC()
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	if job.UpdatedAt.IsZero() {
		job.UpdatedAt = job.CreatedAt
	}
	if s.driver == "postgres" {
		err := s.db.QueryRow(`
			INSERT INTO analysis_jobs (
				job_type, target_type, target_id, status, steps_json, request_json, result_json, last_error,
				attempts, created_at, updated_at, started_at, finished_at
			)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			RETURNING id
		`, job.JobType, job.TargetType, job.TargetID, job.Status, job.StepsJSON, job.RequestJSON, job.ResultJSON, job.LastError,
			job.Attempts, job.CreatedAt, job.UpdatedAt, nullableTime(job.StartedAt), nullableTime(job.FinishedAt)).Scan(&job.ID)
		if err != nil {
			return AnalysisJobRecord{}, err
		}
		return job, nil
	}
	result, err := s.db.Exec(`
		INSERT INTO analysis_jobs (
			job_type, target_type, target_id, status, steps_json, request_json, result_json, last_error,
			attempts, created_at, updated_at, started_at, finished_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, job.JobType, job.TargetType, job.TargetID, job.Status, job.StepsJSON, job.RequestJSON, job.ResultJSON, job.LastError,
		job.Attempts, job.CreatedAt, job.UpdatedAt, nullableTime(job.StartedAt), nullableTime(job.FinishedAt))
	if err != nil {
		return AnalysisJobRecord{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return AnalysisJobRecord{}, err
	}
	job.ID = id
	return job, nil
}

func (s *Store) GetAnalysisJob(id int64) (AnalysisJobRecord, error) {
	row := s.db.QueryRow(`
		SELECT id, job_type, target_type, target_id, status, steps_json, request_json, result_json, last_error,
			attempts, created_at, updated_at, started_at, finished_at
		FROM analysis_jobs
		WHERE id = ?
	`, id)
	return scanAnalysisJob(row)
}

func (s *Store) ListAnalysisJobs(status string, targetType string, targetID string, limit int) ([]AnalysisJobRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	query := `
		SELECT id, job_type, target_type, target_id, status, steps_json, request_json, result_json, last_error,
			attempts, created_at, updated_at, started_at, finished_at
		FROM analysis_jobs
		WHERE 1 = 1
	`
	var args []any
	if status = strings.TrimSpace(status); status != "" {
		query += ` AND status = ?`
		args = append(args, status)
	}
	if targetType = strings.TrimSpace(targetType); targetType != "" {
		query += ` AND target_type = ?`
		args = append(args, targetType)
	}
	if targetID = strings.TrimSpace(targetID); targetID != "" {
		query += ` AND target_id = ?`
		args = append(args, targetID)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAnalysisJobs(rows)
}

func (s *Store) ListAnalysisJobsForWorker(limit int) ([]AnalysisJobRecord, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := s.db.Query(`
		SELECT id, job_type, target_type, target_id, status, steps_json, request_json, result_json, last_error,
			attempts, created_at, updated_at, started_at, finished_at
		FROM analysis_jobs
		WHERE status = 'queued'
		ORDER BY updated_at ASC, id ASC
		LIMIT ?
	`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAnalysisJobs(rows)
}

func (s *Store) MarkAnalysisJobRunning(id int64) error {
	_, err := s.db.Exec(`
		UPDATE analysis_jobs
		SET status = 'running', attempts = attempts + 1, started_at = COALESCE(started_at, ?), updated_at = ?
		WHERE id = ?
	`, time.Now().UTC(), time.Now().UTC(), id)
	return err
}

// ClaimAnalysisJobsForWorker is the analysis-job counterpart of ClaimParseJobs:
// it flips up to limit queued jobs to running in one UPDATE ... RETURNING so the
// server worker and a second process cannot execute the same job twice. The
// reanalysis service no longer marks a job running itself; the claim already
// incremented attempts and stamped started_at.
func (s *Store) ClaimAnalysisJobsForWorker(limit int) ([]AnalysisJobRecord, error) {
	if limit <= 0 {
		limit = 10
	}
	now := time.Now().UTC()
	s.shared.claimMu.Lock()
	defer s.shared.claimMu.Unlock()
	rows, err := s.db.Query(`
		UPDATE analysis_jobs
		SET status = 'running', attempts = attempts + 1,
			started_at = COALESCE(started_at, ?), updated_at = ?
		WHERE status = 'queued' AND id IN (
			SELECT id FROM analysis_jobs
			WHERE status = 'queued'
			ORDER BY updated_at ASC, id ASC
			LIMIT ?`+s.claimRowLockSQL()+`
		)
		RETURNING id, job_type, target_type, target_id, status, steps_json, request_json, result_json, last_error,
			attempts, created_at, updated_at, started_at, finished_at
	`, now, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAnalysisJobs(rows)
}

func (s *Store) MarkAnalysisJobCompleted(id int64, resultJSON string) error {
	if strings.TrimSpace(resultJSON) == "" {
		resultJSON = "{}"
	}
	now := time.Now().UTC()
	_, err := s.db.Exec(`
		UPDATE analysis_jobs
		SET status = 'completed', result_json = ?, last_error = '', finished_at = ?, updated_at = ?
		WHERE id = ?
	`, resultJSON, now, now, id)
	return err
}

func (s *Store) MarkAnalysisJobFailed(id int64, lastError string) error {
	now := time.Now().UTC()
	_, err := s.db.Exec(`
		UPDATE analysis_jobs
		SET status = 'failed', last_error = ?, finished_at = ?, updated_at = ?
		WHERE id = ?
	`, textPreview(lastError, 2000), now, now, id)
	if err != nil {
		return err
	}
	job, err := s.GetAnalysisJob(id)
	if err != nil {
		return err
	}
	_, err = s.UpsertSystemEvent(systemEventForAnalysisJobFailure(job))
	return err
}

func (s *Store) MarkAnalysisJobCanceled(id int64) error {
	now := time.Now().UTC()
	_, err := s.db.Exec(`
		UPDATE analysis_jobs
		SET status = 'canceled', finished_at = ?, updated_at = ?
		WHERE id = ? AND status IN ('queued', 'running')
	`, now, now, id)
	return err
}

func sessionSummaryFilterSupported(filter ListFilter) bool {
	return strings.TrimSpace(filter.Endpoint) == "" &&
		strings.TrimSpace(filter.SelectedUpstream) == "" &&
		strings.TrimSpace(filter.ObservationStatus) == "" &&
		!filter.MissingUsage &&
		filter.MinDurationMs == 0 &&
		filter.MaxDurationMs == 0 &&
		filter.MinTTFTMs == 0 &&
		filter.MaxTTFTMs == 0 &&
		filter.MinTokens == 0 &&
		filter.MaxTokens == 0
}

func (s *Store) listSessionPageFromSummaries(page int, pageSize int, filter ListFilter) (SessionPageResult, bool, error) {
	var summaryRows int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM session_summaries`).Scan(&summaryRows); err != nil {
		return SessionPageResult{}, false, err
	}
	if summaryRows == 0 {
		return SessionPageResult{}, false, nil
	}

	whereSQL, whereArgs := buildSessionSummaryFilterClause(filter)
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM session_summaries s WHERE `+whereSQL, whereArgs...).Scan(&total); err != nil {
		return SessionPageResult{}, false, err
	}
	offset := (page - 1) * pageSize
	queryArgs := append([]any{}, whereArgs...)
	queryArgs = append(queryArgs, pageSize, offset)
	rows, err := s.db.Query(`
		SELECT
			s.session_id,
			s.session_source,
			s.request_count,
			s.first_seen,
			s.last_seen,
			s.last_model,
			s.providers,
			s.success_request,
			s.failed_request,
			s.success_rate,
			s.total_tokens,
			s.avg_ttft,
			s.total_duration,
			s.stream_count
		FROM session_summaries s
		WHERE `+whereSQL+`
		ORDER BY s.last_seen DESC, s.session_id DESC
		LIMIT ? OFFSET ?
	`, queryArgs...)
	if err != nil {
		return SessionPageResult{}, false, err
	}
	defer rows.Close()

	result := SessionPageResult{
		Page:     page,
		PageSize: pageSize,
		Total:    total,
	}
	for rows.Next() {
		summary, err := scanSessionSummary(rows)
		if err != nil {
			return SessionPageResult{}, false, err
		}
		result.Items = append(result.Items, summary)
	}
	if err := rows.Err(); err != nil {
		return SessionPageResult{}, false, err
	}
	if total > 0 {
		result.TotalPages = int(math.Ceil(float64(total) / float64(pageSize)))
	}
	return result, true, nil
}

func (s *Store) getSessionFromSummary(sessionID string) (SessionSummary, error) {
	row := s.db.QueryRow(`
		SELECT
			session_id,
			session_source,
			request_count,
			first_seen,
			last_seen,
			last_model,
			providers,
			success_request,
			failed_request,
			success_rate,
			total_tokens,
			avg_ttft,
			total_duration,
			stream_count
		FROM session_summaries
		WHERE session_id = ?
	`, sessionID)
	return scanSessionSummary(row)
}

func buildSessionSummaryFilterClause(filter ListFilter) (string, []any) {
	var clauses []string
	var args []any
	if query := strings.TrimSpace(filter.Query); query != "" {
		like := "%" + escapeLike(query) + "%"
		clauses = append(clauses, `(LOWER(s.session_id) LIKE LOWER(?) ESCAPE '\' OR LOWER(s.last_model) LIKE LOWER(?) ESCAPE '\' OR LOWER(s.providers) LIKE LOWER(?) ESCAPE '\')`)
		args = append(args, like, like, like)
	}
	if provider := strings.TrimSpace(filter.Provider); provider != "" {
		like := "%" + escapeLike(provider) + "%"
		clauses = append(clauses, `LOWER(s.providers) LIKE LOWER(?) ESCAPE '\'`)
		args = append(args, like)
	}
	if model := strings.TrimSpace(filter.Model); model != "" {
		clauses = append(clauses, `LOWER(s.last_model) LIKE LOWER(?) ESCAPE '\'`)
		args = append(args, "%"+escapeLike(model)+"%")
	}
	switch strings.ToLower(strings.TrimSpace(filter.Status)) {
	case "success":
		clauses = append(clauses, `s.failed_request = 0`)
	case "failed", "error":
		clauses = append(clauses, `s.failed_request > 0`)
	}
	if len(clauses) == 0 {
		return "1=1", nil
	}
	return strings.Join(clauses, " AND "), args
}

func (s *Store) ListSessionPage(page int, pageSize int, filter ListFilter) (SessionPageResult, error) {
	// The summary read path is a derived read model, so it observes every
	// deferred write before it answers; the log-derived fallback below does not
	// need it but is cheap to keep behind the same barrier.
	s.flushDerivedRefresh()
	if page < 1 {
		page = 1
	}
	if pageSize <= 0 {
		pageSize = 50
	}
	if s.useSessionSummaryRead && sessionSummaryFilterSupported(filter) {
		result, ok, err := s.listSessionPageFromSummaries(page, pageSize, filter)
		if err == nil && ok {
			return result, nil
		}
	}

	whereSQL, whereArgs := buildLogFilterClause(filter, "s")
	sessionWhere := andSQL(`s.session_id <> ''`, clientVisibleLogClause("s"))
	sessionWhere = andSQL(sessionWhere, whereSQL)
	sessionIDs, total, err := s.listSessionPageIDs(sessionWhere, whereArgs, page, pageSize)
	if err != nil {
		return SessionPageResult{}, err
	}

	result := SessionPageResult{
		Page:     page,
		PageSize: pageSize,
		Total:    total,
	}
	if total == 0 {
		return result, nil
	}
	result.TotalPages = totalPages(total, pageSize)
	if len(sessionIDs) == 0 {
		return result, nil
	}

	queryArgs := append([]any{}, whereArgs...)
	for _, sessionID := range sessionIDs {
		queryArgs = append(queryArgs, sessionID)
	}
	listSQL := `
		SELECT
			s.session_id,
			MIN(s.session_source) AS session_source,
			COUNT(*) AS request_count,
			MIN(s.recorded_at) AS first_seen,
			MAX(s.recorded_at) AS last_seen,
			COALESCE((
				SELECT model FROM logs l2
				WHERE l2.session_id = s.session_id
					AND ` + clientVisibleLogClause("l2") + `
				ORDER BY l2.recorded_at DESC, l2.trace_id DESC
				LIMIT 1
			), '') AS last_model,
			` + s.sessionProvidersAggregateSQL() + ` AS providers,
			COALESCE(SUM(CASE WHEN s.status_code BETWEEN 200 AND 299 THEN 1 ELSE 0 END), 0) AS success_request,
			COALESCE(SUM(CASE WHEN s.status_code NOT BETWEEN 200 AND 299 THEN 1 ELSE 0 END), 0) AS failed_request,
			CASE WHEN COUNT(*) = 0 THEN 0 ELSE
				100.0 * SUM(CASE WHEN s.status_code BETWEEN 200 AND 299 THEN 1 ELSE 0 END) / COUNT(*)
			END AS success_rate,
			COALESCE(SUM(CASE WHEN s.status_code BETWEEN 200 AND 299 THEN s.total_tokens ELSE 0 END), 0) AS total_tokens,
			COALESCE(AVG(CASE WHEN s.status_code BETWEEN 200 AND 299 THEN s.ttft_ms END), 0) AS avg_ttft,
			COALESCE(SUM(s.duration_ms), 0) AS total_duration,
			COALESCE(SUM(` + s.boolCountCaseSQL("s.is_stream") + `), 0) AS stream_count
		FROM logs s
		WHERE ` + sessionWhere + ` AND s.session_id IN (` + placeholders(len(sessionIDs)) + `)
		GROUP BY s.session_id
	`
	rows, err := s.db.Query(listSQL, queryArgs...)
	if err != nil {
		return SessionPageResult{}, err
	}
	defer rows.Close()

	bySessionID := make(map[string]SessionSummary, len(sessionIDs))
	for rows.Next() {
		summary, err := scanSessionSummary(rows)
		if err != nil {
			return SessionPageResult{}, err
		}
		bySessionID[summary.SessionID] = summary
	}
	if err := rows.Err(); err != nil {
		return SessionPageResult{}, err
	}
	for _, sessionID := range sessionIDs {
		if summary, ok := bySessionID[sessionID]; ok {
			result.Items = append(result.Items, summary)
		}
	}
	return result, nil
}

func (s *Store) listSessionPageIDs(sessionWhere string, whereArgs []any, page int, pageSize int) ([]string, int, error) {
	var total int
	if err := s.db.QueryRow(`SELECT COUNT(DISTINCT s.session_id) FROM logs s WHERE `+sessionWhere, whereArgs...).Scan(&total); err != nil {
		return nil, 0, err
	}
	if total == 0 {
		return nil, 0, nil
	}
	offset := (page - 1) * pageSize
	queryArgs := append([]any{}, whereArgs...)
	queryArgs = append(queryArgs, pageSize, offset)
	rows, err := s.db.Query(`
		SELECT s.session_id
		FROM logs s
		WHERE `+sessionWhere+`
		GROUP BY s.session_id
		ORDER BY MAX(s.recorded_at) DESC
		LIMIT ? OFFSET ?
	`, queryArgs...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var sessionIDs []string
	for rows.Next() {
		var sessionID string
		if err := rows.Scan(&sessionID); err != nil {
			return nil, 0, err
		}
		sessionIDs = append(sessionIDs, sessionID)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return sessionIDs, total, nil
}

func (s *Store) GetSession(sessionID string) (SessionSummary, error) {
	s.flushDerivedRefresh()
	if s.useSessionSummaryRead {
		summary, err := s.getSessionFromSummary(sessionID)
		if err == nil {
			return summary, nil
		}
	}
	row := s.db.QueryRow(`
		SELECT
			s.session_id,
			MIN(s.session_source) AS session_source,
			COUNT(*) AS request_count,
			MIN(s.recorded_at) AS first_seen,
			MAX(s.recorded_at) AS last_seen,
			COALESCE((
				SELECT model FROM logs l2
				WHERE l2.session_id = s.session_id
					AND `+clientVisibleLogClause("l2")+`
				ORDER BY l2.recorded_at DESC, l2.trace_id DESC
				LIMIT 1
			), '') AS last_model,
			`+s.sessionProvidersAggregateSQL()+` AS providers,
			COALESCE(SUM(CASE WHEN s.status_code BETWEEN 200 AND 299 THEN 1 ELSE 0 END), 0) AS success_request,
			COALESCE(SUM(CASE WHEN s.status_code NOT BETWEEN 200 AND 299 THEN 1 ELSE 0 END), 0) AS failed_request,
			CASE WHEN COUNT(*) = 0 THEN 0 ELSE
				100.0 * SUM(CASE WHEN s.status_code BETWEEN 200 AND 299 THEN 1 ELSE 0 END) / COUNT(*)
			END AS success_rate,
			COALESCE(SUM(CASE WHEN s.status_code BETWEEN 200 AND 299 THEN s.total_tokens ELSE 0 END), 0) AS total_tokens,
			COALESCE(AVG(CASE WHEN s.status_code BETWEEN 200 AND 299 THEN s.ttft_ms END), 0) AS avg_ttft,
			COALESCE(SUM(s.duration_ms), 0) AS total_duration,
			COALESCE(SUM(`+s.boolCountCaseSQL("s.is_stream")+`), 0) AS stream_count
		FROM logs s
		WHERE s.session_id = ? AND `+clientVisibleLogClause("s")+`
		GROUP BY s.session_id
	`, sessionID)
	return scanSessionSummary(row)
}

func (s *Store) ListTracesBySession(sessionID string) ([]LogEntry, error) {
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
		WHERE session_id = ? AND `+clientVisibleLogClause("")+`
		ORDER BY recorded_at DESC, trace_id DESC
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []LogEntry
	for rows.Next() {
		entry, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	if err := s.populateObservationMetadata(entries); err != nil {
		return nil, err
	}
	return entries, rows.Err()
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

func channelConfigRecordFromEnt(row *dao.ChannelConfig) ChannelConfigRecord {
	record := ChannelConfigRecord{
		ID:                 row.ID,
		Name:               row.Name,
		Description:        row.Description,
		Source:             row.Source,
		BaseURL:            row.BaseURL,
		ProviderPreset:     row.ProviderPreset,
		APIType:            row.APIType,
		Mode:               row.Mode,
		CapabilitiesJSON:   row.CapabilitiesJSON,
		ProtocolFamily:     row.ProtocolFamily,
		RoutingProfile:     row.RoutingProfile,
		APIVersion:         row.APIVersion,
		Deployment:         row.Deployment,
		Project:            row.Project,
		Location:           row.Location,
		ModelResource:      row.ModelResource,
		APIKeyHint:         row.APIKeyHint,
		HeadersJSON:        row.HeadersJSON,
		Enabled:            row.Enabled,
		Priority:           row.Priority,
		Weight:             row.Weight,
		CapacityHint:       row.CapacityHint,
		ModelDiscovery:     row.ModelDiscovery,
		AllowUnknownModels: row.AllowUnknownModels,
		CreatedAt:          row.CreatedAt,
		UpdatedAt:          row.UpdatedAt,
		LastProbeStatus:    row.LastProbeStatus,
		LastProbeError:     row.LastProbeError,
	}
	if row.APIKeyCiphertext != nil {
		record.APIKeyCiphertext = append([]byte(nil), (*row.APIKeyCiphertext)...)
	}
	if row.LastProbeAt != nil {
		record.LastProbeAt = *row.LastProbeAt
	}
	return record
}

func (s *Store) channelConfigRecordFromEnt(row *dao.ChannelConfig) (ChannelConfigRecord, error) {
	record := channelConfigRecordFromEnt(row)
	if len(record.APIKeyCiphertext) > 0 {
		plaintext, err := s.decryptSecretBytes(record.APIKeyCiphertext)
		if err != nil {
			return ChannelConfigRecord{}, fmt.Errorf("decrypt channel %s api key: %w", row.ID, err)
		}
		record.APIKeyCiphertext = plaintext
	}
	if strings.TrimSpace(record.HeadersJSON) != "" {
		headers, err := s.decryptHeadersJSON(record.HeadersJSON)
		if err != nil {
			return ChannelConfigRecord{}, fmt.Errorf("decrypt channel %s headers: %w", row.ID, err)
		}
		record.HeadersJSON = headers
	}
	return record, nil
}

func channelModelRecordFromEnt(row *dao.ChannelModel) ChannelModelRecord {
	record := ChannelModelRecord{
		ChannelID:             row.ChannelID,
		Model:                 row.Model,
		DisplayName:           row.DisplayName,
		Source:                row.Source,
		Enabled:               row.Enabled,
		UpstreamModel:         row.UpstreamModel,
		ProfileSource:         row.ProfileSource,
		ProfileAdoptionStatus: row.ProfileAdoptionStatus,
		InputModalitiesJSON:   row.InputModalitiesJSON,
		OutputModalitiesJSON:  row.OutputModalitiesJSON,
		RawModelJSON:          row.RawModelJSON,
		FirstSeenAt:           row.FirstSeenAt,
		LastSeenAt:            row.LastSeenAt,
	}
	if row.SupportsResponses != nil {
		value := *row.SupportsResponses
		record.SupportsResponses = &value
	}
	if row.SupportsChatCompletions != nil {
		value := *row.SupportsChatCompletions
		record.SupportsChatCompletions = &value
	}
	if row.SupportsEmbeddings != nil {
		value := *row.SupportsEmbeddings
		record.SupportsEmbeddings = &value
	}
	if row.ContextWindow != nil {
		value := *row.ContextWindow
		record.ContextWindow = &value
	}
	if row.MaxOutputTokens != nil {
		value := *row.MaxOutputTokens
		record.MaxOutputTokens = &value
	}
	if row.CompactHistoryItemThreshold != nil {
		value := *row.CompactHistoryItemThreshold
		record.CompactHistoryItemThreshold = &value
	}
	if row.LastProbeAt != nil {
		record.LastProbeAt = *row.LastProbeAt
	}
	return record
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

func channelProbeRunRecordFromEnt(row *dao.ChannelProbeRun) ChannelProbeRunRecord {
	record := ChannelProbeRunRecord{
		ID:                 row.ID,
		ChannelID:          row.ChannelID,
		Status:             row.Status,
		StartedAt:          row.StartedAt,
		DurationMs:         row.DurationMs,
		DiscoveredCount:    row.DiscoveredCount,
		EnabledCount:       row.EnabledCount,
		Endpoint:           row.Endpoint,
		StatusCode:         row.StatusCode,
		ErrorText:          row.ErrorText,
		RequestMetaJSON:    row.RequestMetaJSON,
		ResponseSampleJSON: row.ResponseSampleJSON,
	}
	if row.CompletedAt != nil {
		record.CompletedAt = *row.CompletedAt
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

func scanSessionSummary(scanner interface {
	Scan(dest ...any) error
}) (SessionSummary, error) {
	var (
		summary      SessionSummary
		firstSeen    any
		lastSeen     any
		providersCSV string
		avgTTFT      float64
	)
	err := scanner.Scan(
		&summary.SessionID,
		&summary.SessionSource,
		&summary.RequestCount,
		&firstSeen,
		&lastSeen,
		&summary.LastModel,
		&providersCSV,
		&summary.SuccessRequest,
		&summary.FailedRequest,
		&summary.SuccessRate,
		&summary.TotalTokens,
		&avgTTFT,
		&summary.TotalDuration,
		&summary.StreamCount,
	)
	if err != nil {
		return SessionSummary{}, err
	}
	summary.FirstSeen, err = timeParseValue(firstSeen)
	if err != nil {
		return SessionSummary{}, err
	}
	summary.LastSeen, err = timeParseValue(lastSeen)
	if err != nil {
		return SessionSummary{}, err
	}
	summary.AvgTTFT = int(math.Round(avgTTFT))
	summary.Providers = splitProviders(providersCSV)
	return summary, nil
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

func ExtractGroupingInfo(content []byte, parsed *recordfile.ParsedPrelude) (GroupingInfo, error) {
	reqFull, _, _, _ := recordfile.ExtractSections(content, parsed)
	return extractGroupingInfoFromRequest(reqFull)
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

func normalizeWindowSessionID(windowID string) string {
	windowID = strings.TrimSpace(windowID)
	if windowID == "" {
		return ""
	}
	sessionID, _, found := strings.Cut(windowID, ":")
	if !found {
		return windowID
	}
	return strings.TrimSpace(sessionID)
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

func scanFindings(rows *sql.Rows) ([]observe.Finding, error) {
	var out []observe.Finding
	for rows.Next() {
		var finding observe.Finding
		var severity string
		var createdAt any
		if err := rows.Scan(&finding.ID, &finding.TraceID, &finding.Category, &severity, &finding.Confidence, &finding.Title,
			&finding.Description, &finding.EvidencePath, &finding.EvidenceExcerpt, &finding.NodeID,
			&finding.Detector, &finding.DetectorVersion, &createdAt); err != nil {
			return nil, err
		}
		finding.Severity = observe.Severity(severity)
		var err error
		finding.CreatedAt, err = timeParseValue(createdAt)
		if err != nil {
			return nil, err
		}
		out = append(out, finding)
	}
	return out, rows.Err()
}

func scanParseJobs(rows *sql.Rows) ([]ParseJobRecord, error) {
	var out []ParseJobRecord
	for rows.Next() {
		var job ParseJobRecord
		var createdAt, updatedAt any
		if err := rows.Scan(&job.ID, &job.TraceID, &job.Status, &job.Attempts, &job.LastError, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		var err error
		if job.CreatedAt, err = timeParseValue(createdAt); err != nil {
			return nil, err
		}
		if job.UpdatedAt, err = timeParseValue(updatedAt); err != nil {
			return nil, err
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

type analysisJobScanner interface {
	Scan(dest ...any) error
}

func scanAnalysisJob(row analysisJobScanner) (AnalysisJobRecord, error) {
	var job AnalysisJobRecord
	var createdAt, updatedAt, startedAt, finishedAt any
	if err := row.Scan(&job.ID, &job.JobType, &job.TargetType, &job.TargetID, &job.Status, &job.StepsJSON, &job.RequestJSON, &job.ResultJSON, &job.LastError,
		&job.Attempts, &createdAt, &updatedAt, &startedAt, &finishedAt); err != nil {
		return AnalysisJobRecord{}, err
	}
	var err error
	if job.CreatedAt, err = timeParseValue(createdAt); err != nil {
		return AnalysisJobRecord{}, err
	}
	if job.UpdatedAt, err = timeParseValue(updatedAt); err != nil {
		return AnalysisJobRecord{}, err
	}
	if job.StartedAt, err = timeParseNullableValue(startedAt); err != nil {
		return AnalysisJobRecord{}, err
	}
	if job.FinishedAt, err = timeParseNullableValue(finishedAt); err != nil {
		return AnalysisJobRecord{}, err
	}
	return job, nil
}

func scanAnalysisJobs(rows *sql.Rows) ([]AnalysisJobRecord, error) {
	var out []AnalysisJobRecord
	for rows.Next() {
		job, err := scanAnalysisJob(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, job)
	}
	return out, rows.Err()
}

func scanSystemEvents(rows *sql.Rows) ([]SystemEvent, error) {
	var out []SystemEvent
	for rows.Next() {
		event, err := scanSystemEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, event)
	}
	return out, rows.Err()
}

type systemEventScanner interface {
	Scan(dest ...any) error
}

func scanSystemEvent(row systemEventScanner) (SystemEvent, error) {
	var event SystemEvent
	var detailsJSON string
	var firstSeenAt, lastSeenAt, createdAt, updatedAt, readAt, resolvedAt any
	if err := row.Scan(
		&event.ID,
		&event.Fingerprint,
		&event.Source,
		&event.Category,
		&event.Severity,
		&event.Status,
		&event.Title,
		&event.Message,
		&detailsJSON,
		&event.TraceID,
		&event.SessionID,
		&event.JobID,
		&event.UpstreamID,
		&event.Model,
		&event.OccurrenceCount,
		&firstSeenAt,
		&lastSeenAt,
		&createdAt,
		&updatedAt,
		&readAt,
		&resolvedAt,
	); err != nil {
		return SystemEvent{}, err
	}
	event.DetailsJSON = json.RawMessage(detailsJSON)
	var err error
	if event.FirstSeenAt, err = timeParseValue(firstSeenAt); err != nil {
		return SystemEvent{}, err
	}
	if event.LastSeenAt, err = timeParseValue(lastSeenAt); err != nil {
		return SystemEvent{}, err
	}
	if event.CreatedAt, err = timeParseValue(createdAt); err != nil {
		return SystemEvent{}, err
	}
	if event.UpdatedAt, err = timeParseValue(updatedAt); err != nil {
		return SystemEvent{}, err
	}
	if event.ReadAt, err = timeParseNullableValue(readAt); err != nil {
		return SystemEvent{}, err
	}
	if event.ResolvedAt, err = timeParseNullableValue(resolvedAt); err != nil {
		return SystemEvent{}, err
	}
	return event, nil
}

func observationSummaryJSON(obs observe.TraceObservation) map[string]any {
	return map[string]any{
		"request_nodes":  len(obs.Request.Nodes),
		"response_nodes": len(obs.Response.Nodes),
		"stream_events":  len(obs.Stream.Events),
		"tool_calls":     len(obs.Tools.Calls),
		"tool_results":   len(obs.Tools.Results),
		"findings":       len(obs.Findings),
		"exchange_kind":  obs.ExchangeKind,
		"exchange_role":  obs.ExchangeRole,
	}
}

func observationFlatNodes(obs observe.TraceObservation) []observe.FlatSemanticNode {
	var roots []observe.SemanticNode
	roots = append(roots, obs.Request.Nodes...)
	roots = append(roots, obs.Response.Nodes...)
	roots = append(roots, obs.Stream.AccumulatedToolCalls...)
	return dedupeFlatSemanticNodes(observe.FlattenNodes(roots))
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

func buildSystemEventFilterClause(filter SystemEventFilter) (string, []any) {
	var clauses []string
	var args []any
	if status := normalizedSystemEventFilterValue(filter.Status); status != "" {
		clauses = append(clauses, `status = ?`)
		args = append(args, status)
	}
	if severity := normalizedSystemEventFilterValue(filter.Severity); severity != "" {
		clauses = append(clauses, `severity = ?`)
		args = append(args, severity)
	}
	if source := normalizedSystemEventFilterValue(filter.Source); source != "" {
		clauses = append(clauses, `source = ?`)
		args = append(args, source)
	}
	if category := normalizedSystemEventFilterValue(filter.Category); category != "" {
		clauses = append(clauses, `category = ?`)
		args = append(args, category)
	}
	if !filter.Since.IsZero() {
		clauses = append(clauses, `last_seen_at >= ?`)
		args = append(args, filter.Since.UTC().Format(timeLayout))
	}
	if query := strings.TrimSpace(filter.Query); query != "" {
		pattern := "%" + escapeLike(query) + "%"
		clauses = append(clauses, `(
			LOWER(fingerprint) LIKE LOWER(?) ESCAPE '\' OR
			LOWER(title) LIKE LOWER(?) ESCAPE '\' OR
			LOWER(message) LIKE LOWER(?) ESCAPE '\' OR
			LOWER(trace_id) LIKE LOWER(?) ESCAPE '\' OR
			LOWER(session_id) LIKE LOWER(?) ESCAPE '\' OR
			LOWER(upstream_id) LIKE LOWER(?) ESCAPE '\' OR
			LOWER(model) LIKE LOWER(?) ESCAPE '\'
		)`)
		for range 7 {
			args = append(args, pattern)
		}
	}
	if len(clauses) == 0 {
		return "1 = 1", nil
	}
	return strings.Join(clauses, " AND "), args
}

func normalizedSystemEventFilterValue(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || value == "all" {
		return ""
	}
	return value
}

func (s *Store) getSystemEvent(whereSQL string, arg any) (SystemEvent, error) {
	var event SystemEvent
	row := s.db.QueryRow(`
		SELECT id, fingerprint, source, category, severity, status, title, message, details_json,
			trace_id, session_id, job_id, upstream_id, model, occurrence_count,
			first_seen_at, last_seen_at, created_at, updated_at, read_at, resolved_at
		FROM system_events
		WHERE `+whereSQL+`
	`, arg)
	event, err := scanSystemEvent(row)
	if err != nil {
		return SystemEvent{}, err
	}
	return event, nil
}

func (s *Store) systemEventCountBy(column string, whereSQL string, args []any, limit int) ([]CountItem, error) {
	switch column {
	case "source", "category", "severity", "status":
	default:
		return nil, fmt.Errorf("unsupported system event count column %q", column)
	}
	queryArgs := append([]any{}, args...)
	queryArgs = append(queryArgs, limit)
	rows, err := s.db.Query(`
		SELECT `+column+`, COUNT(*) AS count
		FROM system_events
		WHERE `+whereSQL+` AND `+column+` != ''
		GROUP BY `+column+`
		ORDER BY count DESC, `+column+` ASC
		LIMIT ?
	`, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanCountItems(rows)
}

func (s *Store) upsertSystemEventsForLog(traceID string, header recordfile.RecordHeader, grouping GroupingInfo) error {
	if reason := strings.TrimSpace(header.Meta.RoutingFailureReason); reason != "" {
		if _, err := s.UpsertSystemEvent(systemEventForRoutingFailure(traceID, header, grouping, reason)); err != nil {
			return err
		}
		return nil
	}
	if errorText := strings.TrimSpace(header.Meta.Error); errorText != "" {
		if _, err := s.UpsertSystemEvent(systemEventForTransportError(traceID, header, grouping, errorText)); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) notifySystemEventIDChanged(id string) {
	event, err := s.GetSystemEvent(strings.TrimSpace(id))
	if err != nil {
		return
	}
	s.notifySystemEventChanged(event)
}

func (s *Store) notifySystemEventChanged(event SystemEvent) {
	s.shared.eventMu.Lock()
	s.shared.eventSeq++
	notification := SystemEventNotification{
		Sequence: s.shared.eventSeq,
		EventID:  event.ID,
		Status:   event.Status,
		Severity: event.Severity,
		Source:   event.Source,
		Category: event.Category,
		At:       time.Now().UTC(),
	}
	// The sends happen while eventMu is still held, and the unsubscribe closure
	// closes the channel under that same mutex, so a send can never observe a
	// closed channel. Publishing from a snapshot taken before the unlock races
	// with close, and `select` treats a send on a closed channel as a ready
	// case, so the non-blocking `default` branch does not protect it. That panic
	// lands on the parse/reanalysis worker goroutines that call
	// UpsertSystemEvent, and nothing in the repository recovers, so it would
	// terminate the whole server. Each send is non-blocking, so holding the
	// mutex across the loop stays O(subscribers) and cannot block on a reader.
	for ch := range s.shared.eventSubs {
		select {
		case ch <- notification:
		default:
		}
	}
	s.shared.eventMu.Unlock()
}

func systemEventForParseFailure(job ParseJobRecord) SystemEvent {
	return SystemEvent{
		Fingerprint: "parser:parse_job:" + normalizeEventFingerprintPart(job.LastError),
		Source:      "parser",
		Category:    "parse_failure",
		Severity:    "error",
		Title:       "Observation parse job failed",
		Message:     job.LastError,
		TraceID:     job.TraceID,
		JobID:       fmt.Sprint(job.ID),
		DetailsJSON: mustMarshalSystemEventDetails(map[string]any{
			"attempts": job.Attempts,
			"status":   job.Status,
		}),
	}
}

func systemEventForAnalysisFailure(run AnalysisRunRecord) SystemEvent {
	return SystemEvent{
		Fingerprint: strings.Join([]string{
			"analyzer",
			normalizeEventFingerprintPart(run.Kind),
			normalizeEventFingerprintPart(run.Analyzer),
			normalizeEventFingerprintPart(run.Status),
			firstNonEmpty(run.TraceID, run.SessionID, "workspace"),
		}, ":"),
		Source:    "analyzer",
		Category:  "analysis_failure",
		Severity:  "error",
		Title:     "Analysis run failed",
		Message:   firstNonEmpty(run.OutputJSON, run.Status),
		TraceID:   run.TraceID,
		SessionID: run.SessionID,
		JobID:     fmt.Sprint(run.ID),
		Model:     run.Model,
		DetailsJSON: mustMarshalSystemEventDetails(map[string]any{
			"kind":             run.Kind,
			"analyzer":         run.Analyzer,
			"analyzer_version": run.AnalyzerVersion,
			"input_ref":        run.InputRef,
			"status":           run.Status,
		}),
	}
}

func systemEventForAnalysisJobFailure(job AnalysisJobRecord) SystemEvent {
	traceID := ""
	sessionID := ""
	switch job.TargetType {
	case "trace":
		traceID = job.TargetID
	case "session":
		sessionID = job.TargetID
	}
	return SystemEvent{
		Fingerprint: strings.Join([]string{
			"analysis_job",
			normalizeEventFingerprintPart(job.JobType),
			normalizeEventFingerprintPart(job.TargetType),
			normalizeEventFingerprintPart(job.TargetID),
			normalizeEventFingerprintPart(job.LastError),
		}, ":"),
		Source:    "analyzer",
		Category:  "analysis_job_failure",
		Severity:  "error",
		Title:     "Reanalysis job failed",
		Message:   job.LastError,
		TraceID:   traceID,
		SessionID: sessionID,
		JobID:     fmt.Sprint(job.ID),
		DetailsJSON: mustMarshalSystemEventDetails(map[string]any{
			"job_type":    job.JobType,
			"target_type": job.TargetType,
			"target_id":   job.TargetID,
			"status":      job.Status,
			"attempts":    job.Attempts,
			"steps":       job.StepsJSON,
		}),
	}
}

func systemEventForRoutingFailure(traceID string, header recordfile.RecordHeader, grouping GroupingInfo, reason string) SystemEvent {
	model := firstNonEmpty(header.Meta.Model, "unknown-model")
	return SystemEvent{
		Fingerprint: strings.Join([]string{
			"router",
			normalizeEventFingerprintPart(model),
			normalizeEventFingerprintPart(reason),
		}, ":"),
		Source:     "router",
		Category:   "routing_failure",
		Severity:   "error",
		Title:      "Routing failed",
		Message:    firstNonEmpty(header.Meta.Error, reason),
		TraceID:    traceID,
		SessionID:  grouping.SessionID,
		UpstreamID: header.Meta.SelectedUpstreamID,
		Model:      header.Meta.Model,
		DetailsJSON: mustMarshalSystemEventDetails(map[string]any{
			"endpoint":                 header.Meta.Endpoint,
			"routing_policy":           header.Meta.RoutingPolicy,
			"routing_failure_reason":   reason,
			"routing_candidate_count":  header.Meta.RoutingCandidateCount,
			"status_code":              header.Meta.StatusCode,
			"selected_upstream_id":     header.Meta.SelectedUpstreamID,
			"selected_upstream_preset": header.Meta.SelectedUpstreamProviderPreset,
		}),
	}
}

func systemEventForTransportError(traceID string, header recordfile.RecordHeader, grouping GroupingInfo, errorText string) SystemEvent {
	class := classifySystemTransportError(errorText, header.Meta.StatusCode)
	severity := "error"
	if class == "client_disconnect" {
		severity = "warning"
	}
	return SystemEvent{
		Fingerprint: strings.Join([]string{
			"upstream",
			normalizeEventFingerprintPart(firstNonEmpty(header.Meta.SelectedUpstreamID, "unknown-upstream")),
			normalizeEventFingerprintPart(header.Meta.Endpoint),
			class,
		}, ":"),
		Source:     "upstream",
		Category:   "transport_error",
		Severity:   severity,
		Title:      "Upstream transport error",
		Message:    errorText,
		TraceID:    traceID,
		SessionID:  grouping.SessionID,
		UpstreamID: header.Meta.SelectedUpstreamID,
		Model:      header.Meta.Model,
		DetailsJSON: mustMarshalSystemEventDetails(map[string]any{
			"endpoint":             header.Meta.Endpoint,
			"status_code":          header.Meta.StatusCode,
			"error_class":          class,
			"selected_upstream_id": header.Meta.SelectedUpstreamID,
			"is_stream":            header.Layout.IsStream,
		}),
	}
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

func normalizeEventFingerprintPart(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "unknown"
	}
	var b strings.Builder
	lastUnderscore := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "unknown"
	}
	if len(out) > 80 {
		out = out[:80]
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func mustMarshalSystemEventDetails(details map[string]any) json.RawMessage {
	data, err := json.Marshal(details)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(data)
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

func encodeSystemEventCursor(event SystemEvent) (string, error) {
	event.ID = strings.TrimSpace(event.ID)
	if event.ID == "" || event.LastSeenAt.IsZero() {
		return "", errors.New("encode system event cursor: missing sort key")
	}
	payload := systemEventCursor{
		Version:    systemEventCursorVersion,
		LastSeenAt: event.LastSeenAt.UTC().Format(timeLayout),
		ID:         event.ID,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func decodeSystemEventCursor(value string) (systemEventCursorPosition, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return systemEventCursorPosition{}, errors.New("decode system event cursor: cursor is empty")
	}
	data, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil {
		return systemEventCursorPosition{}, errors.New("decode system event cursor: invalid encoding")
	}
	var payload systemEventCursor
	if err := json.Unmarshal(data, &payload); err != nil {
		return systemEventCursorPosition{}, errors.New("decode system event cursor: invalid payload")
	}
	if payload.Version != systemEventCursorVersion {
		return systemEventCursorPosition{}, fmt.Errorf("decode system event cursor: unsupported version %d", payload.Version)
	}
	payload.ID = strings.TrimSpace(payload.ID)
	if payload.ID == "" {
		return systemEventCursorPosition{}, errors.New("decode system event cursor: id is required")
	}
	lastSeenAt, err := timeParse(payload.LastSeenAt)
	if err != nil || lastSeenAt.IsZero() {
		return systemEventCursorPosition{}, errors.New("decode system event cursor: invalid last_seen_at")
	}
	return systemEventCursorPosition{LastSeenAt: lastSeenAt.UTC(), ID: payload.ID}, nil
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
		content, err := os.ReadFile(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		parsed, err := recordfile.ParsePrelude(content)
		if err != nil {
			if shouldSkipIncompleteRecord(content, err) {
				continue
			}
			return err
		}
		grouping, err := ExtractGroupingInfo(content, parsed)
		if err != nil {
			return err
		}
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
