// Channel configuration and channel model endpoints: the handlers the monitor UI writes
// through, and the helpers that enrich a stored channel or model row with the analytics
// and effective-service facts the UI displays.

package monitor

import (
	"database/sql"
	"encoding/json"
	"errors"
	"github.com/kingfs/Trajecta/internal/channel"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/store"
	"io"
	"net/http"
	"strings"
	"time"
)

func decodeChannelProbeRequest(r *http.Request) (channelProbeRequest, error) {
	var req channelProbeRequest
	if r == nil || r.Body == nil || r.Body == http.NoBody {
		return req, nil
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return channelProbeRequest{}, errors.New("invalid probe payload")
	}
	return req, nil
}

func handleChannelConfig(w http.ResponseWriter, r *http.Request, st *store.Store, rtr *router.Router, channelService *channel.Service, channelID string) {
	switch r.Method {
	case http.MethodGet:
		record, err := st.GetChannelConfig(channelID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "channel not found"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		models, err := st.ListChannelModels(channelID, false)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		item := channelItemFromRecord(st, record, len(models), enabledChannelModelCount(models, channelID))
		windowLabel, since := parseAnalyticsWindow(r.URL.Query().Get("window"))
		bucketSize, bucketCount := analyticsBucketSpec(windowLabel)
		enrichChannelItemAnalytics(st, &item, channelID, since, bucketSize, bucketCount, true)
		writeJSON(w, http.StatusOK, item)
	case http.MethodPatch:
		existing, err := st.GetChannelConfig(channelID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "channel not found"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		var req channelUpsertRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid channel payload"})
			return
		}
		req.ID = channelID
		record, err := st.UpsertChannelConfig(channelRecordFromRequest(req, existing))
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := reloadRouterFromChannels(rtr, effectiveChannelService(st, channelService)); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reload router: " + err.Error()})
			return
		}
		models, _ := st.ListChannelModels(channelID, false)
		writeJSON(w, http.StatusOK, channelItemFromRecord(st, record, len(models), enabledChannelModelCount(models, channelID)))
	case http.MethodDelete:
		if err := st.DeleteChannelConfig(channelID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "channel not found"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if err := reloadRouterFromChannels(rtr, effectiveChannelService(st, channelService)); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reload router: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	default:
		http.NotFound(w, r)
	}
}

func handleChannelModels(w http.ResponseWriter, r *http.Request, st *store.Store, rtr *router.Router, channelService *channel.Service, channelID string) {
	switch r.Method {
	case http.MethodGet:
		models, err := st.ListChannelModels(channelID, false)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		items := make([]channelModelItem, 0, len(models))
		for _, model := range models {
			items = append(items, channelModelItemFromRecord(model))
		}
		writeJSON(w, http.StatusOK, channelModelsResponse{Items: items, RefreshedAt: time.Now().UTC()})
	case http.MethodPost:
		var req channelModelCreateRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid model payload"})
			return
		}
		enabled := true
		if req.Enabled != nil {
			enabled = *req.Enabled
		}
		record, err := st.UpsertChannelModel(channelID, store.ChannelModelRecord{
			Model:       req.Model,
			DisplayName: req.DisplayName,
			Source:      "manual",
			Enabled:     enabled,
		})
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := reloadRouterFromChannels(rtr, effectiveChannelService(st, channelService)); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reload router: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, channelModelItemFromRecord(record))
	default:
		http.NotFound(w, r)
	}
}

func handleChannelModelsBatch(w http.ResponseWriter, r *http.Request, st *store.Store, rtr *router.Router, channelService *channel.Service, channelID string) {
	if r.Method != http.MethodPatch {
		http.NotFound(w, r)
		return
	}
	var req channelModelBatchPatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid model payload"})
		return
	}
	models := normalizeModelList(req.Models)
	if len(models) == 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "models are required"})
		return
	}
	for _, model := range models {
		if err := st.SetChannelModelEnabled(channelID, model, req.Enabled); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
	}
	if err := reloadRouterFromChannels(rtr, effectiveChannelService(st, channelService)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reload router: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, channelModelBatchPatchResponse{Updated: len(models), Models: models, Enabled: req.Enabled})
}

func handleChannelModel(w http.ResponseWriter, r *http.Request, st *store.Store, rtr *router.Router, channelService *channel.Service, channelID string, model string) {
	if r.Method != http.MethodPatch && r.Method != http.MethodDelete {
		http.NotFound(w, r)
		return
	}
	if r.Method == http.MethodDelete {
		if err := st.DeleteChannelModel(channelID, model); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": "model not found"})
				return
			}
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		if err := reloadRouterFromChannels(rtr, effectiveChannelService(st, channelService)); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reload router: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid model payload"})
		return
	}
	var req channelModelPatchRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid model payload"})
		return
	}
	// The capability columns are tri-state (unset = inherit). An explicit JSON
	// null means "clear back to inherit", which a *bool alone cannot express,
	// so presence of the key is checked separately.
	var rawFields map[string]json.RawMessage
	_ = json.Unmarshal(bodyBytes, &rawFields)
	explicitNull := func(key string) bool {
		value, ok := rawFields[key]
		return ok && strings.TrimSpace(string(value)) == "null"
	}
	record, err := st.UpdateChannelModelProfile(channelID, model, store.ChannelModelProfilePatch{
		DisplayName:                  req.DisplayName,
		Enabled:                      req.Enabled,
		SupportsResponses:            req.SupportsResponses,
		ClearSupportsResponses:       explicitNull("supports_responses"),
		SupportsChatCompletions:      req.SupportsChatCompletions,
		ClearSupportsChatCompletions: explicitNull("supports_chat_completions"),
		SupportsEmbeddings:           req.SupportsEmbeddings,
		ClearSupportsEmbeddings:      explicitNull("supports_embeddings"),
		ContextWindow:                req.ContextWindow,
		MaxOutputTokens:              req.MaxOutputTokens,
		CompactHistoryItemThreshold:  req.CompactHistoryItemThreshold,
		UpstreamModel:                req.UpstreamModel,
		ProfileSource:                req.ProfileSource,
		ProfileAdoptionStatus:        req.ProfileAdoptionStatus,
	})
	if errors.Is(err, sql.ErrNoRows) {
		record, err = st.UpsertChannelModel(channelID, channelModelRecordFromPatch(model, req))
	}
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if err := reloadRouterFromChannels(rtr, effectiveChannelService(st, channelService)); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reload router: " + err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, channelModelItemFromRecord(record))
}

func reloadRouterFromChannels(rtr *router.Router, channelService *channel.Service) error {
	if rtr == nil {
		return nil
	}
	if channelService == nil {
		return errors.New("channel service not configured")
	}
	targets, err := channelService.RuntimeTargets()
	if err != nil {
		return err
	}
	return rtr.Reload(targets)
}

func effectiveChannelService(st *store.Store, channelService *channel.Service) *channel.Service {
	if channelService != nil {
		return channelService
	}
	if st == nil {
		return nil
	}
	return channel.NewService(st)
}

func enrichModelChannelItemFromRecord(item *modelChannelItem, record store.ChannelModelRecord) {
	if item == nil {
		return
	}
	item.DisplayName = record.DisplayName
	item.Enabled = record.Enabled
	item.Source = record.Source
	item.SupportsResponses = capabilityBool(record.SupportsResponses)
	item.SupportsChatCompletions = capabilityBool(record.SupportsChatCompletions)
	item.SupportsEmbeddings = capabilityBool(record.SupportsEmbeddings)
	item.ContextWindow = record.ContextWindow
	item.MaxOutputTokens = record.MaxOutputTokens
	item.CompactHistoryItemThreshold = record.CompactHistoryItemThreshold
	item.UpstreamModel = record.UpstreamModel
	item.ProfileSource = record.ProfileSource
	item.ProfileAdoptionStatus = record.ProfileAdoptionStatus
	item.InputModalities = decodeStringList(record.InputModalitiesJSON)
	item.OutputModalities = decodeStringList(record.OutputModalitiesJSON)
}

func enrichChannelItemAnalytics(st *store.Store, item *channelItem, channelID string, since time.Time, bucketSize time.Duration, bucketCount int, includeDetail bool) {
	if st == nil || item == nil {
		return
	}
	if summary, err := st.GetChannelUsageSummary(channelID, since); err == nil {
		item.Summary = usageSummaryViewFromRecord(summary)
	}
	if trends, err := st.GetChannelUsageTrends(channelID, since, bucketSize, bucketCount, DisplayLocation()); err == nil {
		item.Trends = usageTrendViews(trends)
	}
	if !includeDetail {
		return
	}
	if models, err := st.GetChannelModelUsage(channelID, since); err == nil {
		item.ModelsUsage = make([]modelChannelItem, 0, len(models))
		for _, model := range models {
			item.ModelsUsage = append(item.ModelsUsage, modelChannelItem{
				ChannelID: model.ChannelID,
				Model:     model.Model,
				Enabled:   model.Enabled,
				Source:    model.Source,
				Summary:   usageSummaryViewFromRecord(model.Summary),
			})
		}
	}
	if failures, err := st.GetChannelRecentFailures(channelID, since, 10); err == nil {
		item.RecentFailures = toUpstreamFailureItems(failures)
	}
	if probeRuns, err := st.ListChannelProbeRuns(channelID, 8); err == nil {
		item.RecentProbeRuns = channelProbeRunItems(probeRuns)
	}
}

func enabledChannelModelCount(models []store.ChannelModelRecord, channelID string) int {
	count := 0
	for _, model := range models {
		if model.ChannelID == channelID && model.Enabled {
			count++
		}
	}
	return count
}
