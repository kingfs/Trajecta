package channel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/modelprobe"
	"github.com/kingfs/Trajecta/internal/netproxy"
	"github.com/kingfs/Trajecta/internal/providerprobe"
	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/internal/upstream"
)

type Store interface {
	LoadAppSettingJSON(context.Context, string, any) (bool, error)
	SaveAppSettingJSON(context.Context, string, any) error
	DeleteAppSetting(context.Context, string) (bool, error)
	ListChannelConfigs() ([]store.ChannelConfigRecord, error)
	GetChannelConfig(channelID string) (store.ChannelConfigRecord, error)
	UpsertChannelConfig(store.ChannelConfigRecord) (store.ChannelConfigRecord, error)
	UpdateChannelProbeStatus(channelID string, probedAt time.Time, status string, errorText string) error
	ListChannelModels(channelID string, enabledOnly bool) ([]store.ChannelModelRecord, error)
	ListModelAliases(alias string, enabledOnly bool) ([]store.ModelAliasRecord, error)
	ReplaceChannelModels(channelID string, records []store.ChannelModelRecord) error
	UpsertChannelModel(channelID string, record store.ChannelModelRecord) (store.ChannelModelRecord, error)
	UpsertChannelModels(channelID string, records []store.ChannelModelRecord) ([]store.ChannelModelRecord, error)
	UpsertModelCatalog(store.ModelCatalogRecord) error
	CreateChannelProbeRun(store.ChannelProbeRunRecord) (store.ChannelProbeRunRecord, error)
	ReplaceUpstreamModels(upstreamID string, records []store.UpstreamModelRecord) error
}

type Service struct {
	store      Store
	httpClient *http.Client
}

func NewService(st Store) *Service {
	return &Service{store: st}
}

// WithHTTPClient returns a copy of the service that probes with client.
//
// Every With* helper returns a derived service and leaves the receiver
// untouched, so a service shared by several callers (for example the one built
// for the Monitor) can be specialised without mutating it in place.
func (s *Service) WithHTTPClient(client *http.Client) *Service {
	next := *s
	next.httpClient = client
	return &next
}

// WithStore preserves probe options when applying a configuration transaction.
func (s *Service) WithStore(st Store) *Service {
	next := *s
	next.store = st
	return &next
}

const configurationInitializedKey = "channels.initialized"

// HasConfiguration distinguishes an intentionally empty database from first startup.
func (s *Service) HasConfiguration() (bool, error) {
	var initialized bool
	if _, err := s.store.LoadAppSettingJSON(context.Background(), configurationInitializedKey, &initialized); err != nil {
		return false, err
	}
	if initialized {
		return true, nil
	}
	channels, err := s.store.ListChannelConfigs()
	return len(channels) > 0, err
}

func (s *Service) MarkConfigurationInitialized() error {
	return s.store.SaveAppSettingJSON(context.Background(), configurationInitializedKey, true)
}

// ResetConfigurationInitialized clears the explicit bootstrap marker so the next
// start may import YAML again. It deliberately does not delete channels: a
// database that still holds channel configuration keeps counting as
// initialized, because HasConfiguration also falls back to the stored channels.
func (s *Service) ResetConfigurationInitialized() error {
	_, err := s.store.DeleteAppSetting(context.Background(), configurationInitializedKey)
	return err
}

type ProbeResult struct {
	ChannelID       string
	Status          string
	FailureReason   string
	RetryHint       string
	Models          []string
	DiscoveredCount int
	EnabledCount    int
	// AddedCount/RemovedCount describe what one sync changed relative to the
	// stored rows: models the provider now lists, and discovered models it no
	// longer lists. Operator-declared (manual or static) rows are never counted
	// as removed, and a failed sync reports 0/0 and keeps the previous snapshot.
	AddedCount     int
	RemovedCount   int
	Endpoint       string
	ErrorText      string
	ProviderReport providerprobe.Report
	StartedAt      time.Time
	CompletedAt    time.Time
	DurationMs     int64
}

type ProbeOptions struct {
	EnableDiscovered *bool
	DetectProvider   bool
}

type ProviderProbeReportOptions struct {
	ChannelID string
}

type ProviderProbeApplyResult struct {
	Report  providerprobe.BatchReport `json:"report"`
	Applied []ProviderProbeApplyItem  `json:"applied"`
}

type ProviderProbeApplyItem struct {
	ChannelID     string   `json:"channel_id"`
	Status        string   `json:"status"`
	Applied       bool     `json:"applied"`
	AppliedFields []string `json:"applied_fields,omitempty"`
	SkippedReason string   `json:"skipped_reason,omitempty"`
}

func (s *Service) RuntimeTargets() ([]config.UpstreamTargetConfig, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("channel service store is required")
	}
	channels, err := s.store.ListChannelConfigs()
	if err != nil {
		return nil, err
	}
	aliases, err := s.store.ListModelAliases("", true)
	if err != nil {
		return nil, err
	}
	// Every channel's models come back in one query ordered by channel id, which
	// is the same order and the same rows the per-channel lookups returned; the
	// per-channel form ran one query per enabled channel on every routing reload.
	allModels, err := s.store.ListChannelModels("", false)
	if err != nil {
		return nil, err
	}
	modelsByChannel := make(map[string][]store.ChannelModelRecord, len(channels))
	for _, model := range allModels {
		modelsByChannel[model.ChannelID] = append(modelsByChannel[model.ChannelID], model)
	}
	targets := make([]config.UpstreamTargetConfig, 0, len(channels))
	for _, channel := range channels {
		if !channel.Enabled {
			continue
		}
		models := modelsByChannel[channel.ID]
		var enabledModels []store.ChannelModelRecord
		var disabledModels []string
		upstreamModelNames := make([]string, 0, len(models))
		for _, model := range models {
			upstreamModel := strings.TrimSpace(model.UpstreamModel)
			if modelRoutable(channel, model) {
				enabledModels = append(enabledModels, model)
				if upstreamModel != "" {
					upstreamModelNames = append(upstreamModelNames, upstreamModel)
				}
				upstreamModelNames = append(upstreamModelNames, channelModelProtocolWireNames(model)...)
			} else {
				disabledModels = append(disabledModels, model.Model)
				if upstreamModel != "" {
					disabledModels = append(disabledModels, upstreamModel)
				}
			}
		}
		models = enabledModels
		protocolAliases := channelModelProtocolAliases(models)
		headers := map[string]string{}
		if strings.TrimSpace(channel.HeadersJSON) != "" {
			if err := json.Unmarshal([]byte(channel.HeadersJSON), &headers); err != nil {
				return nil, fmt.Errorf("decode headers for channel %q: %w", channel.ID, err)
			}
		}
		capabilities, err := unmarshalCapabilities(channel.CapabilitiesJSON)
		if err != nil {
			return nil, fmt.Errorf("decode capabilities for channel %q: %w", channel.ID, err)
		}
		proxySettings, err := ChannelProxySettings(channel)
		if err != nil {
			return nil, fmt.Errorf("decode proxy for channel %q: %w", channel.ID, err)
		}
		enabled := true
		target := config.UpstreamTargetConfig{
			ID:                   channel.ID,
			Name:                 channel.Name,
			Enabled:              &enabled,
			Priority:             channel.Priority,
			Weight:               channel.Weight,
			CapacityHint:         channel.CapacityHint,
			ModelDiscovery:       channel.ModelDiscovery,
			StaticModels:         channelModelNamesWithAliases(models, aliases, channel.ID),
			ModelAliases:         channelModelAliases(models, aliases, channel.ID),
			ProtocolModelAliases: protocolAliases,
			ModelWireNames:       channelModelWireNames(models),
			ConfiguredModelsOnly: true,
			DisabledModels:       disabledModels,
			UpstreamModelNames:   upstreamModelNames,
			Upstream: config.UpstreamConfig{
				BaseURL:           channel.BaseURL,
				ApiKey:            string(channel.APIKeyCiphertext),
				ProviderPreset:    channel.ProviderPreset,
				APIType:           channel.APIType,
				Mode:              channel.Mode,
				Capabilities:      capabilities,
				ModelCapabilities: ChannelModelCapabilities(models, aliases, channel.ID),
				ProtocolFamily:    channel.ProtocolFamily,
				RoutingProfile:    channel.RoutingProfile,
				APIVersion:        channel.APIVersion,
				Deployment:        channel.Deployment,
				Project:           channel.Project,
				Location:          channel.Location,
				ModelResource:     channel.ModelResource,
				Headers:           headers,
				Proxy:             proxySettings,
			},
		}
		targets = append(targets, target)
	}
	return targets, nil
}

func (s *Service) ProviderProbeReport(ctx context.Context, options ProviderProbeReportOptions) (providerprobe.BatchReport, error) {
	targets, err := s.ProviderProbeTargets(options.ChannelID)
	if err != nil {
		return providerprobe.BatchReport{}, err
	}
	return providerprobe.ProbeBatch(ctx, targets, s.httpClient), nil
}

func (s *Service) ApplyProviderProbeReport(ctx context.Context, options ProviderProbeReportOptions) (ProviderProbeApplyResult, error) {
	report, err := s.ProviderProbeReport(ctx, options)
	if err != nil {
		return ProviderProbeApplyResult{}, err
	}
	channels, err := s.store.ListChannelConfigs()
	if err != nil {
		return ProviderProbeApplyResult{}, err
	}
	byID := make(map[string]store.ChannelConfigRecord, len(channels))
	for _, channel := range channels {
		byID[channel.ID] = channel
	}
	result := ProviderProbeApplyResult{
		Report:  report,
		Applied: make([]ProviderProbeApplyItem, 0, len(report.Reports)),
	}
	for _, probeReport := range report.Reports {
		item := ProviderProbeApplyItem{
			ChannelID: strings.TrimSpace(probeReport.ProviderID),
			Status:    probeReport.Status,
		}
		if probeReport.Status != providerprobe.StatusDetected {
			item.SkippedReason = "probe status is not detected"
			result.Applied = append(result.Applied, item)
			continue
		}
		channel, ok := byID[item.ChannelID]
		if !ok {
			item.SkippedReason = "channel not found"
			result.Applied = append(result.Applied, item)
			continue
		}
		updated, fields, err := applyProviderProbeSuggestions(channel, probeReport)
		if err != nil {
			return ProviderProbeApplyResult{}, err
		}
		if len(fields) == 0 {
			item.SkippedReason = "no missing fields to apply"
			result.Applied = append(result.Applied, item)
			continue
		}
		if _, err := s.store.UpsertChannelConfig(updated); err != nil {
			return ProviderProbeApplyResult{}, err
		}
		item.Applied = true
		item.AppliedFields = fields
		result.Applied = append(result.Applied, item)
	}
	return result, nil
}

func (s *Service) ProviderProbeTargets(channelID string) ([]providerprobe.ProbeTarget, error) {
	if s == nil || s.store == nil {
		return nil, errors.New("channel service store is required")
	}
	channelID = strings.TrimSpace(channelID)
	channels, err := s.store.ListChannelConfigs()
	if err != nil {
		return nil, err
	}
	targets := make([]providerprobe.ProbeTarget, 0, len(channels))
	for _, channel := range channels {
		if !channel.Enabled {
			continue
		}
		if channelID != "" && channel.ID != channelID {
			continue
		}
		target := providerProbeTargetFromChannel(channel)
		target.TargetSource = "channel"
		if strings.TrimSpace(target.BaseURL) == "" {
			continue
		}
		targets = append(targets, target)
	}
	if channelID != "" && len(targets) == 0 {
		return nil, fmt.Errorf("channel %q was not found, is disabled, or has no base_url", channelID)
	}
	if len(targets) == 0 {
		return nil, errors.New("no enabled channel with base_url is configured")
	}
	return targets, nil
}

func marshalCapabilities(capabilities config.UpstreamCapabilitiesConfig) (string, error) {
	data, err := json.Marshal(capabilities)
	if err != nil {
		return "", err
	}
	if string(data) == "null" || string(data) == "" {
		return "{}", nil
	}
	return string(data), nil
}

func unmarshalCapabilities(raw string) (config.UpstreamCapabilitiesConfig, error) {
	var capabilities config.UpstreamCapabilitiesConfig
	if strings.TrimSpace(raw) == "" {
		return capabilities, nil
	}
	if err := json.Unmarshal([]byte(raw), &capabilities); err != nil {
		return config.UpstreamCapabilitiesConfig{}, err
	}
	return capabilities, nil
}

func applyProviderProbeSuggestions(channel store.ChannelConfigRecord, report providerprobe.Report) (store.ChannelConfigRecord, []string, error) {
	updated := channel
	var fields []string
	if strings.TrimSpace(updated.APIType) == "" && strings.TrimSpace(report.SuggestedAPIType) != "" {
		updated.APIType = strings.TrimSpace(report.SuggestedAPIType)
		fields = append(fields, "api_type")
	}
	if strings.TrimSpace(updated.ProtocolFamily) == "" && strings.TrimSpace(report.SuggestedProtocolFamily) != "" {
		updated.ProtocolFamily = strings.TrimSpace(report.SuggestedProtocolFamily)
		fields = append(fields, "protocol_family")
	}
	capabilities, err := unmarshalCapabilities(updated.CapabilitiesJSON)
	if err != nil {
		return store.ChannelConfigRecord{}, nil, fmt.Errorf("decode capabilities for channel %q: %w", channel.ID, err)
	}
	// The provider probe only checks routes: it POSTs an empty body and treats a 400
	// "missing model" as proof that the endpoint exists. That is not enough to grant the
	// channel-level Responses capability - a Chat Completions channel would then be offered
	// a native `/v1/responses` pass-through it cannot honour (NVIDIA NIM lists the model but
	// answers plain-text `404 page not found` on `/v1/responses`). Only apply Responses when
	// the channel is Responses-typed, or when it explicitly has no Chat Completions backend
	// (so Responses can only mean the native surface). The HTTP client's per-model probe is
	// what is allowed to grant Responses after it actually reached the endpoint.
	responsesAllowed := responsesCapabilityAllowed(updated.APIType, capabilities)
	for _, capability := range report.Capabilities {
		if isResponsesCapability(capability) && !responsesAllowed {
			continue
		}
		if upstream.SetCapabilityIfUnset(&capabilities, capability, true) {
			if field := upstream.CapabilityFieldName(capability); field != "" {
				fields = append(fields, field)
			}
		}
	}
	if len(fields) == 0 {
		return updated, nil, nil
	}
	capabilitiesJSON, err := marshalCapabilities(capabilities)
	if err != nil {
		return store.ChannelConfigRecord{}, nil, err
	}
	updated.CapabilitiesJSON = capabilitiesJSON
	return updated, fields, nil
}

func (s *Service) Probe(channelID string) (ProbeResult, error) {
	return s.ProbeWithOptions(channelID, ProbeOptions{})
}

func (s *Service) ProbeWithOptions(channelID string, options ProbeOptions) (ProbeResult, error) {
	if s == nil || s.store == nil {
		return ProbeResult{}, errors.New("channel service store is required")
	}
	channelID = strings.TrimSpace(channelID)
	if channelID == "" {
		return ProbeResult{}, errors.New("channel id is required")
	}
	startedAt := time.Now().UTC()
	result := ProbeResult{ChannelID: channelID, StartedAt: startedAt}

	channel, err := s.store.GetChannelConfig(channelID)
	if err != nil {
		return result, err
	}
	probeClient, err := s.httpClientForChannel(channel)
	if err != nil {
		return result, err
	}
	if options.DetectProvider {
		providerReport, providerProbeErr := providerprobe.Probe(context.Background(), providerProbeTargetFromChannel(channel), probeClient)
		if providerProbeErr == nil {
			result.ProviderReport = providerReport
		}
	}
	resolved, err := upstream.Resolve(UpstreamConfigFromChannel(channel))
	if err != nil {
		return s.finishProbe(result, resolved, nil, err, options)
	}
	models, err := upstream.DiscoverModelsResolved(resolved, probeClient)
	return s.finishProbe(result, resolved, models, err, options)
}

func (s *Service) finishProbe(result ProbeResult, resolved upstream.ResolvedUpstream, models []string, probeErr error, options ProbeOptions) (ProbeResult, error) {
	completedAt := time.Now().UTC()
	result.CompletedAt = completedAt
	result.DurationMs = completedAt.Sub(result.StartedAt).Milliseconds()
	if diagnostics, err := resolved.StartupDiagnostics(); err == nil {
		result.Endpoint = diagnostics.ConnectivityEndpoint
	}
	result.Models = normalizeModels(models)
	result.DiscoveredCount = len(result.Models)
	result.Status = "success"
	if probeErr != nil {
		result.Status = "failed"
		result.ErrorText = probeErr.Error()
		result.FailureReason, result.RetryHint = classifyProbeFailure(probeErr)
	}

	channelModels := make([]store.ChannelModelRecord, 0, len(result.Models))
	upstreamModels := make([]store.UpstreamModelRecord, 0, len(result.Models))
	for _, model := range result.Models {
		channelModels = append(channelModels, store.ChannelModelRecord{
			ChannelID:   result.ChannelID,
			Model:       model,
			DisplayName: model,
			Source:      "discovered",
			Enabled:     true,
			FirstSeenAt: completedAt,
			LastSeenAt:  completedAt,
			LastProbeAt: completedAt,
		})
		upstreamModels = append(upstreamModels, store.UpstreamModelRecord{
			UpstreamID: result.ChannelID,
			Model:      model,
			Source:     "discovered",
			SeenAt:     completedAt,
		})
		if err := s.store.UpsertModelCatalog(store.ModelCatalogRecord{
			Model:       model,
			DisplayName: model,
			FirstSeenAt: completedAt,
			LastSeenAt:  completedAt,
		}); err != nil {
			return result, err
		}
	}
	if probeErr == nil {
		merged, added, removed, err := s.mergeDiscoveredChannelModels(result.ChannelID, channelModels, enableDiscoveredByDefault(options))
		if err != nil {
			return result, err
		}
		result.AddedCount = added
		result.RemovedCount = removed
		result.EnabledCount = countEnabledModels(merged)
		if err := s.store.ReplaceUpstreamModels(result.ChannelID, upstreamModels); err != nil {
			return result, err
		}
	}
	// The channel's last_probe_at is the availability anchor: ModelAvailability
	// compares channel_models.last_probe_at against it. A failed probe records
	// its status and error but must not move the anchor, so the latest
	// successful probe stays authoritative and a failure never widens or
	// narrows routability. The zero time makes the store leave last_probe_at
	// untouched; the attempt itself is still recorded in channel_probe_runs.
	probeAnchor := completedAt
	if probeErr != nil {
		probeAnchor = time.Time{}
	}
	if err := s.store.UpdateChannelProbeStatus(result.ChannelID, probeAnchor, result.Status, result.ErrorText); err != nil {
		return result, err
	}
	if _, err := s.store.CreateChannelProbeRun(store.ChannelProbeRunRecord{
		ChannelID:       result.ChannelID,
		Status:          result.Status,
		StartedAt:       result.StartedAt,
		CompletedAt:     result.CompletedAt,
		DurationMs:      result.DurationMs,
		DiscoveredCount: result.DiscoveredCount,
		EnabledCount:    result.EnabledCount,
		Endpoint:        result.Endpoint,
		ErrorText:       result.ErrorText,
		RequestMetaJSON: probeRequestMetaJSON(result),
	}); err != nil {
		return result, err
	}
	return result, probeErr
}

// ModelState classifies a channel model against the provider's latest
// successful sync.
type ModelState string

const (
	// ModelStateEnabled is a model the operator enabled and the latest sync saw.
	ModelStateEnabled ModelState = "enabled"
	// ModelStateDisabled is a model the operator has switched off.
	ModelStateDisabled ModelState = "disabled"
	// ModelStateRemoved is a model the latest successful sync no longer lists.
	// The operator's switch is kept, but routing excludes it.
	ModelStateRemoved ModelState = "removed"
	// ModelStateUnverified is an operator-declared model, or any model on a
	// channel that has never synced successfully. It is routable but flagged.
	ModelStateUnverified ModelState = "unverified"
)

// isOperatorDeclaredModel reports whether a channel_models row was declared by
// the operator rather than discovered from the provider's listing: a manually
// added model, or one imported from YAML static_models during bootstrap.
//
// A discovery that does not return such a row is not evidence that the provider
// dropped it, so availability never classifies it removed.
func isOperatorDeclaredModel(model store.ChannelModelRecord) bool {
	switch source := strings.TrimSpace(model.Source); {
	case strings.EqualFold(source, "manual"), strings.EqualFold(source, "static"):
		return true
	default:
		return false
	}
}

// ModelAvailability classifies one channel model against the latest successful
// sync. A model the latest successful sync did not return is "removed" and is
// not routed; a model without a successful sync to classify it - the channel
// never synced successfully, or the operator declared the row - is "unverified"
// and stays routable.
//
// channel.LastProbeAt is the anchor and only advances on a successful probe, so
// a failed probe changes neither direction of the classification: the previous
// successful snapshot stays authoritative until the next success.
func ModelAvailability(channel store.ChannelConfigRecord, model store.ChannelModelRecord) ModelState {
	if !model.Enabled {
		return ModelStateDisabled
	}
	if isOperatorDeclaredModel(model) {
		return ModelStateUnverified
	}
	if channel.LastProbeAt.IsZero() {
		return ModelStateUnverified
	}
	if model.LastProbeAt.IsZero() || model.LastProbeAt.Before(channel.LastProbeAt) {
		return ModelStateRemoved
	}
	return ModelStateEnabled
}

// ModelRoutable reports whether the model may be selected for a request.
func ModelRoutable(channel store.ChannelConfigRecord, model store.ChannelModelRecord) bool {
	switch ModelAvailability(channel, model) {
	case ModelStateEnabled, ModelStateUnverified:
		return true
	default:
		return false
	}
}

func modelRoutable(channel store.ChannelConfigRecord, model store.ChannelModelRecord) bool {
	return ModelRoutable(channel, model)
}

func (s *Service) mergeDiscoveredChannelModels(channelID string, discovered []store.ChannelModelRecord, enableNew bool) ([]store.ChannelModelRecord, int, int, error) {
	existing, err := s.store.ListChannelModels(channelID, false)
	if err != nil {
		return nil, 0, 0, err
	}
	existingByModel := make(map[string]store.ChannelModelRecord, len(existing))
	for _, record := range existing {
		existingByModel[record.Model] = record
	}
	discoveredSet := make(map[string]struct{}, len(discovered))
	added := 0
	records := make([]store.ChannelModelRecord, 0, len(discovered))
	for _, record := range discovered {
		model := strings.ToLower(strings.TrimSpace(record.Model))
		if model == "" {
			continue
		}
		discoveredSet[model] = struct{}{}
		record.Enabled = enableNew
		if previous, ok := existingByModel[model]; ok {
			// Re-discovery updates observation timestamps, not operator choices
			// or an adopted model profile.
			lastSeen, lastProbe := record.LastSeenAt, record.LastProbeAt
			record = previous
			record.LastSeenAt = lastSeen
			record.LastProbeAt = lastProbe
		} else {
			added++
		}
		records = append(records, record)
	}
	removed := 0
	for model, record := range existingByModel {
		if _, ok := discoveredSet[model]; ok {
			continue
		}
		// A provider no longer listing an operator-declared model (manual or
		// YAML static) does not remove it: the operator declared it on purpose.
		// Any other row stays in the table (so its switch survives) and is
		// marked removed by the sync timestamp.
		if isOperatorDeclaredModel(record) {
			continue
		}
		removed++
	}
	// One grouped upsert for the whole discovery replaces the upsert, catalog
	// write and read back the loop used to run per model.
	merged, err := s.store.UpsertChannelModels(channelID, records)
	if err != nil {
		return nil, 0, 0, err
	}
	return merged, added, removed, nil
}

func enableDiscoveredByDefault(options ProbeOptions) bool {
	if options.EnableDiscovered == nil {
		return false
	}
	return *options.EnableDiscovered
}

func countEnabledModels(records []store.ChannelModelRecord) int {
	count := 0
	for _, record := range records {
		if record.Enabled {
			count++
		}
	}
	return count
}

func probeRequestMetaJSON(result ProbeResult) string {
	meta := map[string]any{}
	if result.FailureReason != "" {
		meta["failure_reason"] = result.FailureReason
	}
	if result.RetryHint != "" {
		meta["retry_hint"] = result.RetryHint
	}
	if result.ProviderReport.Status != "" {
		meta["provider_probe"] = result.ProviderReport
	}
	if len(meta) == 0 {
		return "{}"
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return "{}"
	}
	return string(data)
}

func classifyProbeFailure(err error) (string, string) {
	if err == nil {
		return "", ""
	}
	text := strings.ToLower(err.Error())
	switch {
	case strings.Contains(text, "upstream status: 401") || strings.Contains(text, "upstream status: 403") ||
		strings.Contains(text, "unauthorized") || strings.Contains(text, "forbidden") || strings.Contains(text, "api key"):
		return "auth_error", "Verify the API key and custom authorization headers for this channel."
	case strings.Contains(text, "upstream status: 404"):
		return "not_found", "Check the base URL, provider preset, deployment, API version, and model discovery endpoint."
	case strings.Contains(text, "invalid character") || strings.Contains(text, "cannot unmarshal") || strings.Contains(text, "unexpected end of json"):
		return "invalid_json", "Confirm the model discovery endpoint returns a supported JSON models list."
	case isNetworkProbeError(err, text):
		return "network_error", "Check network reachability, DNS, proxy settings, and whether the upstream service is listening."
	case strings.Contains(text, "upstream status: 429") || strings.Contains(text, "rate limit"):
		return "rate_limited", "Retry later or adjust this channel's upstream quota, priority, or weight."
	case strings.Contains(text, "upstream status: 5"):
		return "upstream_error", "Retry after the provider recovers or temporarily disable this channel."
	case strings.Contains(text, "build check url") || strings.Contains(text, "unsupported") || strings.Contains(text, "missing"):
		return "configuration_error", "Review the channel base URL, provider preset, protocol family, and discovery settings."
	default:
		return "probe_error", "Inspect the upstream response and channel configuration, then retry the probe."
	}
}

func isNetworkProbeError(err error, text string) bool {
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		return true
	}
	return strings.Contains(text, "connection refused") ||
		strings.Contains(text, "no such host") ||
		strings.Contains(text, "i/o timeout") ||
		strings.Contains(text, "context deadline exceeded") ||
		strings.Contains(text, "tls handshake")
}

// UpstreamConfigFromChannel maps a stored channel record onto the configuration the upstream
// resolver reads. It is exported so the Monitor inspector resolves a channel exactly the way
// the forwarding path does instead of re-deriving its capabilities from the raw api_type.
func UpstreamConfigFromChannel(channel store.ChannelConfigRecord) config.UpstreamConfig {
	headers := map[string]string{}
	if strings.TrimSpace(channel.HeadersJSON) != "" {
		_ = json.Unmarshal([]byte(channel.HeadersJSON), &headers)
	}
	capabilities, _ := unmarshalCapabilities(channel.CapabilitiesJSON)
	proxySettings, _ := ChannelProxySettings(channel)
	return config.UpstreamConfig{
		BaseURL:        channel.BaseURL,
		ApiKey:         string(channel.APIKeyCiphertext),
		ProviderPreset: channel.ProviderPreset,
		APIType:        channel.APIType,
		Mode:           channel.Mode,
		Capabilities:   capabilities,
		ProtocolFamily: channel.ProtocolFamily,
		RoutingProfile: channel.RoutingProfile,
		APIVersion:     channel.APIVersion,
		Deployment:     channel.Deployment,
		Project:        channel.Project,
		Location:       channel.Location,
		ModelResource:  channel.ModelResource,
		Headers:        headers,
		Proxy:          proxySettings,
	}
}

// ChannelProxySettings decodes the channel-level outbound proxy policy stored in
// channel_configs.proxy_json. A missing or empty value is the default (disabled)
// policy, which makes the channel follow the process-wide system policy.
func ChannelProxySettings(channel store.ChannelConfigRecord) (netproxy.Settings, error) {
	if strings.TrimSpace(channel.ProxyJSON) == "" {
		return netproxy.DefaultSettings(), nil
	}
	var settings netproxy.Settings
	if err := json.Unmarshal([]byte(channel.ProxyJSON), &settings); err != nil {
		return netproxy.DefaultSettings(), err
	}
	return settings.Normalize(), nil
}

// ChannelProxyResolver compiles the channel-level policy. A disabled policy
// returns a disabled resolver rather than nil, so callers can distinguish
// "configured but off" from a decode error.
func ChannelProxyResolver(channel store.ChannelConfigRecord) (*netproxy.Resolver, error) {
	settings, err := ChannelProxySettings(channel)
	if err != nil {
		return nil, err
	}
	return netproxy.Compile(settings)
}

// httpClientForChannel returns the client a probe of this channel must use. A
// channel with its own proxy gets a client whose transport applies that policy;
// every other channel keeps the service client unchanged.
func (s *Service) httpClientForChannel(channel store.ChannelConfigRecord) (*http.Client, error) {
	resolver, err := ChannelProxyResolver(channel)
	if err != nil {
		return nil, err
	}
	if !resolver.Enabled() {
		return s.httpClient, nil
	}
	base := s.httpClient
	if base == nil {
		base = &http.Client{Timeout: 10 * time.Second}
	}
	return netproxy.ClientWith(base, resolver), nil
}

func providerProbeTargetFromChannel(channel store.ChannelConfigRecord) providerprobe.ProbeTarget {
	upstreamCfg := UpstreamConfigFromChannel(channel)
	return providerprobe.ProbeTarget{
		ProviderID:              channel.ID,
		BaseURL:                 upstreamCfg.BaseURL,
		APIKey:                  upstreamCfg.ApiKey,
		Headers:                 upstreamCfg.Headers,
		SpecifiedAPIType:        upstreamCfg.APIType,
		SpecifiedProtocolFamily: upstreamCfg.ProtocolFamily,
		Proxy:                   upstreamCfg.Proxy,
	}
}

func normalizeModels(models []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(models))
	for _, model := range models {
		model = strings.ToLower(strings.TrimSpace(model))
		if model == "" {
			continue
		}
		if _, ok := seen[model]; ok {
			continue
		}
		seen[model] = struct{}{}
		out = append(out, model)
	}
	slices.Sort(out)
	return out
}

// channelModelCapabilities maps each client-facing model name, including its
// aliases, to the per-model capability overrides declared on the channel model
// profile. Models whose profile declares no capability stay out of the map so
// that they keep falling back to the channel-level api_type and capabilities.
// ChannelModelCapabilities is the projection the forwarding path reads: it maps every
// client-facing model name on the channel, its aliases included, to the per-model capability
// overrides declared on the channel model profile. An alias name carries the capabilities of
// its target row, and models whose profile declares no capability stay out of the map so that
// they keep falling back to the channel-level api_type and capabilities.
//
// It is exported because the Monitor routing inspector has to describe a request the way the
// forwarding path will act on it: the router keys these overrides by the model name in the
// client request, so an alias that shadows a declared model name has to be resolved here
// rather than looked up under the shadowed row's own declaration.
func ChannelModelCapabilities(models []store.ChannelModelRecord, aliases []store.ModelAliasRecord, channelID string) map[string]config.UpstreamCapabilitiesConfig {
	if len(models) == 0 {
		return nil
	}
	out := make(map[string]config.UpstreamCapabilitiesConfig, len(models))
	for _, model := range models {
		caps, ok := channelModelCapabilityConfig(model)
		if !ok {
			continue
		}
		name := strings.ToLower(strings.TrimSpace(model.Model))
		if name == "" {
			continue
		}
		out[name] = caps
	}
	if len(out) == 0 {
		return nil
	}
	for _, alias := range aliases {
		aliasName := strings.ToLower(strings.TrimSpace(alias.Alias))
		targetModel := strings.ToLower(strings.TrimSpace(alias.TargetModel))
		aliasChannelID := strings.TrimSpace(alias.ChannelID)
		if aliasName == "" || targetModel == "" {
			continue
		}
		if aliasChannelID != "" && aliasChannelID != channelID {
			continue
		}
		if caps, ok := out[targetModel]; ok {
			out[aliasName] = caps
		}
	}
	return out
}

func channelModelCapabilityConfig(model store.ChannelModelRecord) (config.UpstreamCapabilitiesConfig, bool) {
	caps := config.UpstreamCapabilitiesConfig{
		Responses:       capabilityIntToBool(model.SupportsResponses),
		ChatCompletions: capabilityIntToBool(model.SupportsChatCompletions),
		Embeddings:      capabilityIntToBool(model.SupportsEmbeddings),
	}
	if protocols := decodeChannelModelProtocols(model.ProtocolsJSON); len(protocols) > 0 {
		if entry, ok := protocols["chat_completions"]; ok {
			enabled := entry.Enabled
			caps.ChatCompletions = &enabled
		}
		if entry, ok := protocols["responses"]; ok {
			enabled := entry.Enabled
			caps.Responses = &enabled
		}
		if entry, ok := protocols["messages"]; ok {
			enabled := entry.Enabled
			caps.Messages = &enabled
		}
		if entry, ok := protocols["gemini"]; ok {
			enabled := entry.Enabled
			caps.GenerateContent = &enabled
		}
	}
	if caps.Responses == nil && caps.ChatCompletions == nil && caps.Embeddings == nil && caps.Messages == nil && caps.GenerateContent == nil {
		return config.UpstreamCapabilitiesConfig{}, false
	}
	return caps, true
}

func capabilityIntToBool(value *int) *bool {
	if value == nil {
		return nil
	}
	enabled := *value != 0
	return &enabled
}

func channelModelNames(models []store.ChannelModelRecord) []string {
	out := make([]string, 0, len(models))
	for _, model := range models {
		out = append(out, model.Model)
	}
	return normalizeModels(out)
}

func channelModelNamesWithAliases(models []store.ChannelModelRecord, aliases []store.ModelAliasRecord, channelID string) []string {
	modelNames := channelModelNames(models)
	if len(aliases) == 0 || len(modelNames) == 0 {
		return modelNames
	}
	supported := make(map[string]struct{}, len(modelNames))
	for _, model := range modelNames {
		supported[model] = struct{}{}
	}
	out := append([]string(nil), modelNames...)
	for _, alias := range aliases {
		aliasName := strings.ToLower(strings.TrimSpace(alias.Alias))
		targetModel := strings.ToLower(strings.TrimSpace(alias.TargetModel))
		aliasChannelID := strings.TrimSpace(alias.ChannelID)
		if aliasName == "" || targetModel == "" {
			continue
		}
		if aliasChannelID != "" && aliasChannelID != channelID {
			continue
		}
		if _, ok := supported[targetModel]; !ok {
			continue
		}
		out = append(out, aliasName)
	}
	return normalizeModels(out)
}

type channelModelProtocol struct {
	Enabled   bool   `json:"enabled"`
	WireModel string `json:"wire_model"`
}

func decodeChannelModelProtocols(raw string) map[string]channelModelProtocol {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "{}" {
		return nil
	}
	out := map[string]channelModelProtocol{}
	if json.Unmarshal([]byte(raw), &out) != nil {
		return nil
	}
	return out
}

func channelModelProtocolAliases(models []store.ChannelModelRecord) map[string]map[string]string {
	out := map[string]map[string]string{}
	for _, model := range models {
		name := strings.ToLower(strings.TrimSpace(model.Model))
		if name == "" {
			continue
		}
		for protocol, entry := range decodeChannelModelProtocols(model.ProtocolsJSON) {
			protocol = strings.ToLower(strings.TrimSpace(protocol))
			wire := strings.TrimSpace(entry.WireModel)
			if protocol == "" || !entry.Enabled || wire == "" {
				continue
			}
			if out[protocol] == nil {
				out[protocol] = map[string]string{}
			}
			out[protocol][name] = wire
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// channelModelWireNames maps each enabled model to its channel row's
// upstream_model. It is the protocol-agnostic wire fallback the router applies
// after a per-protocol wire_model and before the client-facing model name.
func channelModelWireNames(models []store.ChannelModelRecord) map[string]string {
	out := make(map[string]string, len(models))
	for _, model := range models {
		name := strings.ToLower(strings.TrimSpace(model.Model))
		wire := strings.TrimSpace(model.UpstreamModel)
		if name != "" && wire != "" {
			out[name] = wire
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func channelModelProtocolWireNames(model store.ChannelModelRecord) []string {
	var out []string
	for _, entry := range decodeChannelModelProtocols(model.ProtocolsJSON) {
		if entry.Enabled && strings.TrimSpace(entry.WireModel) != "" {
			out = append(out, strings.TrimSpace(entry.WireModel))
		}
	}
	return out
}

func channelModelAliases(models []store.ChannelModelRecord, aliases []store.ModelAliasRecord, channelID string) map[string]string {
	modelNames := channelModelNames(models)
	if len(aliases) == 0 || len(modelNames) == 0 {
		return nil
	}
	supported := make(map[string]struct{}, len(modelNames))
	for _, model := range modelNames {
		supported[model] = struct{}{}
	}
	out := map[string]string{}
	for _, alias := range aliases {
		aliasName := strings.ToLower(strings.TrimSpace(alias.Alias))
		targetModel := strings.ToLower(strings.TrimSpace(alias.TargetModel))
		aliasChannelID := strings.TrimSpace(alias.ChannelID)
		if aliasName == "" || targetModel == "" {
			continue
		}
		if aliasChannelID != "" && aliasChannelID != channelID {
			continue
		}
		if _, ok := supported[targetModel]; !ok {
			continue
		}
		out[aliasName] = targetModel
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// responsesCapabilityAllowed reports whether a route-only provider probe may grant the
// channel-level Responses capability.
//
// It may when the channel is already Responses-typed, or when Chat Completions is explicitly
// disabled (without a local Chat Completions backend the only possible Responses surface is
// native). Everywhere else the probe saw a route, not a working model.
func responsesCapabilityAllowed(apiType string, capabilities config.UpstreamCapabilitiesConfig) bool {
	normalized := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(apiType)), "-", "_")
	if normalized == upstream.APITypeResponses || normalized == upstream.APITypeResponsesNative {
		return true
	}
	return capabilities.ChatCompletions != nil && !*capabilities.ChatCompletions
}

func isResponsesCapability(capability string) bool {
	normalized := strings.ReplaceAll(strings.ToLower(strings.TrimSpace(capability)), "-", "_")
	return normalized == upstream.CapabilityResponses
}

// ErrChannelModelNotFound is returned by ModelProbeTarget when the requested model is not one
// of the channel's channel_models rows. The Monitor API answers it with 404 rather than a
// logical probe failure, because there is nothing to probe.
var ErrChannelModelNotFound = errors.New("channel model not found")

// ErrChannelConfigInvalid is returned when a stored channel cannot be resolved into a usable
// upstream (for example a missing base_url). It lets the HTTP layer answer 400 for a
// configuration problem instead of 500 for a store failure.
var ErrChannelConfigInvalid = errors.New("channel configuration cannot be resolved")

// HTTPClient returns the HTTP client the service probes with, if one was overridden. The
// Monitor per-model probe passes it to modelprobe.Run so it uses the same client as the
// channel probe instead of silently creating a second one.
func (s *Service) HTTPClient() *http.Client {
	if s == nil {
		return nil
	}
	return s.httpClient
}

// ModelProbeTarget resolves one channel model into the minimal target the per-model probe
// needs: the decrypted upstream (base URL, credentials and routing profile), the wire model
// name and the protocol surfaces the forwarding path would actually use.
//
// It only reads. No probe run, model timestamp or configuration row is written. The returned
// error is sql.ErrNoRows when the channel does not exist, ErrChannelModelNotFound when the
// model is not a channel_models row, and ErrChannelConfigInvalid when the channel resolves to
// an unusable upstream.
func (s *Service) ModelProbeTarget(channelID, model string) (modelprobe.Target, error) {
	if s == nil || s.store == nil {
		return modelprobe.Target{}, errors.New("channel service store is required")
	}
	channelID = strings.TrimSpace(channelID)
	model = strings.TrimSpace(model)
	if channelID == "" {
		return modelprobe.Target{}, errors.New("channel id is required")
	}
	if model == "" {
		return modelprobe.Target{}, errors.New("model is required")
	}
	channel, err := s.store.GetChannelConfig(channelID)
	if err != nil {
		return modelprobe.Target{}, err
	}
	models, err := s.store.ListChannelModels(channelID, false)
	if err != nil {
		return modelprobe.Target{}, err
	}
	record, ok := findChannelModel(models, model)
	if !ok {
		return modelprobe.Target{}, fmt.Errorf("%w: %q on channel %q", ErrChannelModelNotFound, model, channelID)
	}
	aliases, err := s.store.ListModelAliases("", true)
	if err != nil {
		return modelprobe.Target{}, err
	}
	cfg := UpstreamConfigFromChannel(channel)
	// The forwarding path reads per-model overrides, so the probe has to resolve the target
	// with the same projection: a model that declares its own protocol surface must be probed
	// over that surface, not over the channel-level one.
	cfg.ModelCapabilities = ChannelModelCapabilities(models, aliases, channelID)
	resolved, err := upstream.Resolve(cfg)
	if err != nil {
		return modelprobe.Target{}, fmt.Errorf("%w: %v", ErrChannelConfigInvalid, err)
	}
	wireModel := strings.TrimSpace(record.UpstreamModel)
	if wireModel == "" {
		wireModel = record.Model
	}
	declared := map[modelprobe.Protocol]bool{
		modelprobe.ProtocolChatCompletions: resolved.SupportsChatCompletionsAPIForModel(record.Model),
		// "Declared" is deliberately broader than native pass-through eligibility: the
		// operator needs the test to tell them that a declared Responses surface is not
		// actually served for this model, which is exactly the NVIDIA NIM incident.
		modelprobe.ProtocolResponses:       responsesDeclared(resolved, record.Model),
		modelprobe.ProtocolMessages:        false,
		modelprobe.ProtocolGenerateContent: false,
	}
	switch resolved.ProtocolFamily {
	case upstream.ProtocolFamilyAnthropicMessages:
		declared[modelprobe.ProtocolMessages] = true
	case upstream.ProtocolFamilyGoogleGenAI, upstream.ProtocolFamilyVertexNative:
		declared[modelprobe.ProtocolGenerateContent] = true
	}
	return modelprobe.Target{
		ChannelID:       channel.ID,
		Model:           record.Model,
		WireModel:       wireModel,
		ProtocolFamily:  resolved.ProtocolFamily,
		ChannelEnabled:  channel.Enabled,
		ModelEnabled:    record.Enabled,
		ModelState:      string(ModelAvailability(channel, record)),
		Routable:        ModelRoutable(channel, record),
		Declared:        declared,
		NativeResponses: resolved.SupportsNativeResponsesPath(record.Model),
		Resolved:        resolved,
	}, nil
}

// responsesDeclared reports Responses support as declared by the stored channel and model
// configuration, independent of whether the upstream can serve `/v1/responses` natively.
//
// The order is the operator's declaration order: a per-model `supports_responses` value wins
// (true or false); otherwise a channel-level `capabilities.responses: true` or a Responses
// `api_type` counts. This is what the per-model probe must test, because a channel can declare
// Responses while its upstream only serves it through a route that is missing for one model.
func responsesDeclared(resolved upstream.ResolvedUpstream, model string) bool {
	if enabled, configured := resolved.ModelCapability(model, upstream.CapabilityResponses); configured {
		return enabled
	}
	if resolved.Capabilities.Responses != nil && *resolved.Capabilities.Responses {
		return true
	}
	switch resolved.APIType {
	case upstream.APITypeResponses, upstream.APITypeResponsesNative:
		return true
	}
	return false
}

func findChannelModel(models []store.ChannelModelRecord, model string) (store.ChannelModelRecord, bool) {
	needle := strings.ToLower(strings.TrimSpace(model))
	for _, record := range models {
		if strings.ToLower(strings.TrimSpace(record.Model)) == needle {
			return record, true
		}
	}
	return store.ChannelModelRecord{}, false
}

// ModelDetectionProtocolTimeout is the per-protocol budget for interface detection.
// A detection probes every protocol of a model in parallel, so one dead endpoint
// cannot stretch a whole channel scan past this bound.
const ModelDetectionProtocolTimeout = 8 * time.Second

// ModelInterfaceProtocolReport is one forced protocol probe run for interface detection.
type ModelInterfaceProtocolReport struct {
	Protocol   string
	Status     string
	HTTPStatus int
	LatencyMS  int64
	Error      string
	Hint       string
}

// ModelInterfaceDetection is the outcome of forced protocol probing for one channel model.
//
// SupportsChatCompletions and SupportsResponses carry the value detection is confident
// enough to persist: true for a working protocol, false when the upstream says the
// endpoint or model is not served there, and nil when the probe was inconclusive.
type ModelInterfaceDetection struct {
	ChannelID               string
	Model                   string
	WireModel               string
	ProtocolFamily          string
	Protocols               []ModelInterfaceProtocolReport
	SupportsChatCompletions *bool
	SupportsResponses       *bool
}

// ModelInterfaces is the effective API surface of one stored channel model, resolved with
// the same model-override-over-channel rules as the forwarding path.
type ModelInterfaces struct {
	ChatCompletions  bool `json:"chat_completions"`
	NativeResponses  bool `json:"native_responses"`
	ResponsesViaChat bool `json:"responses_via_chat"`
	Messages         bool `json:"messages"`
	GenerateContent  bool `json:"generate_content"`
}

// DetectModelInterfaces probes one channel model's real protocol surfaces with forced
// probes that ignore the currently declared capabilities. It reuses ModelProbeTarget, so
// the detection uses the same decrypted credential and per-model projection as the
// forwarding path, and it writes nothing.
//
// openai_compatible models are probed for Chat Completions and Responses in parallel;
// anthropic_messages models for Messages; google_genai/vertex models for generateContent.
// perProtocolTimeout defaults to ModelDetectionProtocolTimeout when non-positive.
func (s *Service) DetectModelInterfaces(ctx context.Context, channelID, model string, perProtocolTimeout time.Duration) (ModelInterfaceDetection, error) {
	target, err := s.ModelProbeTarget(channelID, model)
	if err != nil {
		return ModelInterfaceDetection{}, err
	}
	if perProtocolTimeout <= 0 {
		perProtocolTimeout = ModelDetectionProtocolTimeout
	}
	protocols := detectionProtocolsForFamily(target.ProtocolFamily)
	results := make([]modelprobe.Result, len(protocols))
	var wg sync.WaitGroup
	for i, protocol := range protocols {
		wg.Add(1)
		go func(index int, probeProtocol modelprobe.Protocol) {
			defer wg.Done()
			probeCtx, cancel := context.WithTimeout(ctx, perProtocolTimeout)
			defer cancel()
			report := modelprobe.RunForced(probeCtx, target, []modelprobe.Protocol{probeProtocol}, s.HTTPClient())
			if len(report.Results) > 0 {
				results[index] = report.Results[0]
			}
		}(i, protocol)
	}
	wg.Wait()

	detection := ModelInterfaceDetection{
		ChannelID:      target.ChannelID,
		Model:          target.Model,
		WireModel:      target.WireModel,
		ProtocolFamily: target.ProtocolFamily,
		Protocols:      make([]ModelInterfaceProtocolReport, 0, len(protocols)),
	}
	for i, protocol := range protocols {
		result := results[i]
		detection.Protocols = append(detection.Protocols, ModelInterfaceProtocolReport{
			Protocol:   string(protocol),
			Status:     result.Status,
			HTTPStatus: result.HTTPStatus,
			LatencyMS:  result.LatencyMS,
			Error:      result.Error,
			Hint:       result.Hint,
		})
		switch protocol {
		case modelprobe.ProtocolChatCompletions:
			detection.SupportsChatCompletions = decisiveCapability(result.Status)
		case modelprobe.ProtocolResponses:
			detection.SupportsResponses = decisiveCapability(result.Status)
		}
	}
	return detection, nil
}

// EffectiveModelInterfaces resolves the interfaces one stored channel model can actually
// serve. A model-level supports_* value wins over the channel capability/api_type, which is
// the same precedence the router uses; responses_via_chat means the Responses entrypoint is
// answered by the local runtime instead of native pass-through.
func EffectiveModelInterfaces(channel store.ChannelConfigRecord, model store.ChannelModelRecord) ModelInterfaces {
	cfg := UpstreamConfigFromChannel(channel)
	overrides := config.UpstreamCapabilitiesConfig{
		Responses:       capabilityIntToBool(model.SupportsResponses),
		ChatCompletions: capabilityIntToBool(model.SupportsChatCompletions),
	}
	if protocols := decodeChannelModelProtocols(model.ProtocolsJSON); len(protocols) > 0 {
		if entry, ok := protocols["chat_completions"]; ok {
			enabled := entry.Enabled
			overrides.ChatCompletions = &enabled
		}
		if entry, ok := protocols["responses"]; ok {
			enabled := entry.Enabled
			overrides.Responses = &enabled
		}
		if entry, ok := protocols["messages"]; ok {
			enabled := entry.Enabled
			overrides.Messages = &enabled
		}
		if entry, ok := protocols["gemini"]; ok {
			enabled := entry.Enabled
			overrides.GenerateContent = &enabled
		}
	}
	if overrides.Responses != nil || overrides.ChatCompletions != nil || overrides.Messages != nil || overrides.GenerateContent != nil {
		cfg.ModelCapabilities = map[string]config.UpstreamCapabilitiesConfig{
			strings.ToLower(strings.TrimSpace(model.Model)): overrides,
		}
	}
	out := ModelInterfaces{}
	resolved, err := upstream.Resolve(cfg)
	if err != nil {
		// An unresolvable channel still has to render in the Monitor; fall back to
		// the raw family so interface badges degrade instead of breaking the page.
		switch normalizeProtocolFamily(channel.ProtocolFamily) {
		case upstream.ProtocolFamilyAnthropicMessages:
			out.Messages = true
		case upstream.ProtocolFamilyGoogleGenAI, upstream.ProtocolFamilyVertexNative:
			out.GenerateContent = true
		}
		return out
	}
	out.ChatCompletions = resolved.SupportsChatCompletionsAPIForModel(model.Model)
	out.NativeResponses = resolved.SupportsNativeResponsesPath(model.Model)
	out.ResponsesViaChat = out.ChatCompletions && !out.NativeResponses
	out.Messages = resolved.SupportsMessagesAPIForModel(model.Model)
	out.GenerateContent = resolved.SupportsGenerateContentAPIForModel(model.Model)
	return out
}

func detectionProtocolsForFamily(family string) []modelprobe.Protocol {
	switch normalizeProtocolFamily(family) {
	case upstream.ProtocolFamilyAnthropicMessages:
		return []modelprobe.Protocol{modelprobe.ProtocolMessages}
	case upstream.ProtocolFamilyGoogleGenAI, upstream.ProtocolFamilyVertexNative:
		return []modelprobe.Protocol{modelprobe.ProtocolGenerateContent}
	default:
		return []modelprobe.Protocol{modelprobe.ProtocolChatCompletions, modelprobe.ProtocolResponses}
	}
}

func decisiveCapability(status string) *bool {
	value := false
	switch status {
	case modelprobe.StatusOK:
		value = true
	case modelprobe.StatusEndpointNotSupported, modelprobe.StatusModelNotFound:
		value = false
	default:
		return nil
	}
	return &value
}

func normalizeProtocolFamily(value string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(value)), "-", "_")
}
