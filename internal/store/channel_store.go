// Channel configuration, channel models and probe runs, with the per-channel usage and
// log cross-queries built on them. Secrets and the rotation machinery they use stay in
// store.go, so this file never touches a credential.

package store

import (
	"context"
	"database/sql"
	entsql "entgo.io/ent/dialect/sql"
	"errors"
	"fmt"
	"github.com/google/uuid"
	"github.com/kingfs/Trajecta/ent/dao"
	"github.com/kingfs/Trajecta/ent/dao/channelconfig"
	"github.com/kingfs/Trajecta/ent/dao/channelmodel"
	"github.com/kingfs/Trajecta/ent/dao/channelproberun"
	"github.com/kingfs/Trajecta/ent/dao/modelcatalog"
	"github.com/kingfs/Trajecta/ent/dao/predicate"
	"sort"
	"strings"
	"time"
)

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

func (s *Store) GetChannelUsageTrends(channelID string, since time.Time, bucketSize time.Duration, bucketCount int, loc *time.Location) ([]UsageTrendRecord, error) {
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return nil, errors.New("channel id is required")
	}
	return s.usageTrends("selected_upstream_id = ?", []any{channelID}, since, bucketSize, bucketCount, loc)
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

// GetChannelUsageSummaries returns the per-channel usage summary for the window
// in one grouped pass, keyed by channel id. The channel list page would
// otherwise ask for one summary per configured channel.
func (s *Store) GetChannelUsageSummaries(since time.Time) (map[string]UsageSummaryRecord, error) {
	return s.usageSummariesByGroupKey("selected_upstream_id", since)
}

// GetChannelUsageTrendsBatch computes the same series GetChannelUsageTrends
// computes for one channel, for every channel in one ordered pass. Each channel
// anchors its series at its own latest record, exactly like the single-key form,
// so the shared scan starts at the earliest of those windows and a row that
// falls outside its own channel's window is dropped while bucketing. A channel
// with no rows at all is absent from the result, because the scan cannot know
// the empty window such a channel would have.
func (s *Store) GetChannelUsageTrendsBatch(since time.Time, bucketSize time.Duration, bucketCount int, loc *time.Location) (map[string][]UsageTrendRecord, error) {
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
		// Same grid rule as usageTrends: both sides use bucketSlot.
		bucketStart := bucketSlot(referenceTime, bucketSize, loc).Add(-time.Duration(bucketCount-1) * bucketSize)
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
		item := buckets[bucketSlot(recordedTime, bucketSize, loc)]
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
