package monitor

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kingfs/Trajecta/internal/channel"
	"github.com/kingfs/Trajecta/internal/router"
	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/internal/upstream"
)

const (
	// providerModelDetectMaxModels bounds one channel scan so a channel with a huge
	// model list cannot keep the HTTP request open indefinitely.
	providerModelDetectMaxModels = 100
	// providerModelDetectConcurrency is how many model detections may be in flight at
	// once. Each detection runs its protocols in parallel, so this is the outer bound.
	providerModelDetectConcurrency = 4
)

// providerModelDetectProtocolTimeout is the per-protocol budget handed to
// channel.DetectModelInterfaces. It is a variable only so tests can shorten it.
var providerModelDetectProtocolTimeout = channel.ModelDetectionProtocolTimeout

type providerModelDetectRequest struct {
	ChannelID string `json:"channel_id"`
	Model     string `json:"model"`
	Overwrite bool   `json:"overwrite"`
}

type providerModelDetectProtocol struct {
	Protocol   string `json:"protocol"`
	Status     string `json:"status"`
	HTTPStatus int    `json:"http_status"`
	LatencyMS  int64  `json:"latency_ms"`
	Error      string `json:"error"`
	Hint       string `json:"hint"`
}

type providerModelDetectApplied struct {
	SupportsChatCompletions *bool `json:"supports_chat_completions,omitempty"`
	SupportsResponses       *bool `json:"supports_responses,omitempty"`
}

type providerModelDetectItem struct {
	Model          string                        `json:"model"`
	WireModel      string                        `json:"wire_model"`
	ProtocolFamily string                        `json:"protocol_family"`
	Protocols      []providerModelDetectProtocol `json:"protocols"`
	Applied        providerModelDetectApplied    `json:"applied"`
	SkippedReason  string                        `json:"skipped_reason,omitempty"`
}

type providerModelDetectResponse struct {
	ChannelID  string                    `json:"channel_id"`
	Overwrite  bool                      `json:"overwrite"`
	DurationMS int64                     `json:"duration_ms"`
	Items      []providerModelDetectItem `json:"items"`
	Warnings   []string                  `json:"warnings"`
}

// providerModelDetectAPIHandler detects the real interfaces of one or all enabled
// models on a channel and persists the decisive capability values.
//
// Probing happens first, outside the configuration transaction: the upstream
// calls are the slow part, and they must not hold the management write lock. The
// decisive values are then applied in one ConfigurationTransaction and the router
// is reloaded only after the commit succeeds. Logical probe failures are a 200:
// the operator asked for an answer, and "the endpoint is not served" is an answer.
//
// This endpoint never writes last_probe_at, channel_probe_runs, cassettes or logs.
func providerModelDetectAPIHandler(st *store.Store, svc *channel.Service, rtr *router.Router) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}
		service := effectiveChannelService(st, svc)
		if service == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "channel service not configured"})
			return
		}
		var req providerModelDetectRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid provider model detect payload"})
			return
		}
		req.ChannelID = strings.TrimSpace(req.ChannelID)
		req.Model = strings.TrimSpace(req.Model)
		if req.ChannelID == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "channel_id is required"})
			return
		}
		if service.ReadOnly() {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "upstreams are managed by YAML credentials; edit YAML and restart instead"})
			return
		}

		started := time.Now()
		if _, err := st.GetChannelConfig(req.ChannelID); err != nil {
			writeChannelLookupError(w, err, "channel not found")
			return
		}
		models, err := st.ListChannelModels(req.ChannelID, false)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		selected, warnings := selectProviderModelDetectModels(models, req.Model)
		if req.Model != "" && len(selected) == 0 {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "model not found"})
			return
		}
		detections, detectionErr := probeProviderModelDetect(r.Context(), service, req.ChannelID, selected)
		if detectionErr != nil {
			writeChannelLookupError(w, detectionErr, "model not found")
			return
		}

		items := make([]providerModelDetectItem, len(selected))
		for i := range selected {
			items[i] = providerModelDetectItem{
				Model:          detections[i].Model,
				WireModel:      detections[i].WireModel,
				ProtocolFamily: detections[i].ProtocolFamily,
				Protocols:      providerModelDetectProtocols(detections[i].Protocols),
			}
		}
		applied, skippedReasons, applyErr := applyProviderModelDetect(r.Context(), st, req, detections)
		if applyErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "model detection not applied: " + applyErr.Error()})
			return
		}
		for i := range items {
			items[i].Applied = applied[i]
			items[i].SkippedReason = skippedReasons[i]
		}

		if err := reloadRouterFromChannels(rtr, service); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "reload router: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, providerModelDetectResponse{
			ChannelID:  req.ChannelID,
			Overwrite:  req.Overwrite,
			DurationMS: time.Since(started).Milliseconds(),
			Items:      items,
			Warnings:   warnings,
		})
	}
}

func selectProviderModelDetectModels(models []store.ChannelModelRecord, requested string) ([]store.ChannelModelRecord, []string) {
	warnings := []string{}
	if requested != "" {
		needle := strings.ToLower(requested)
		for _, record := range models {
			if strings.ToLower(record.Model) == needle {
				return []store.ChannelModelRecord{record}, warnings
			}
		}
		return nil, warnings
	}
	selected := make([]store.ChannelModelRecord, 0, len(models))
	for _, record := range models {
		if record.Enabled {
			selected = append(selected, record)
		}
	}
	if len(selected) > providerModelDetectMaxModels {
		selected = selected[:providerModelDetectMaxModels]
		warnings = append(warnings, fmt.Sprintf("detection is limited to the first %d enabled models", providerModelDetectMaxModels))
	}
	if len(selected) == 0 {
		warnings = append(warnings, "channel has no enabled models to detect")
	}
	return selected, warnings
}

// probeProviderModelDetect runs the model detections with a bounded number in
// flight and keeps the request's model order. It returns the first fatal error.
func probeProviderModelDetect(ctx context.Context, service *channel.Service, channelID string, models []store.ChannelModelRecord) ([]channel.ModelInterfaceDetection, error) {
	detections := make([]channel.ModelInterfaceDetection, len(models))
	errs := make([]error, len(models))
	semaphore := make(chan struct{}, providerModelDetectConcurrency)
	var wg sync.WaitGroup
	for i := range models {
		wg.Add(1)
		semaphore <- struct{}{}
		go func(index int) {
			defer wg.Done()
			defer func() { <-semaphore }()
			detections[index], errs[index] = service.DetectModelInterfaces(ctx, channelID, models[index].Model, providerModelDetectProtocolTimeout)
		}(i)
	}
	wg.Wait()
	for _, err := range errs {
		if err == nil {
			continue
		}
		return nil, err
	}
	return detections, nil
}

func providerModelDetectProtocols(reports []channel.ModelInterfaceProtocolReport) []providerModelDetectProtocol {
	out := make([]providerModelDetectProtocol, 0, len(reports))
	for _, report := range reports {
		out = append(out, providerModelDetectProtocol{
			Protocol:   report.Protocol,
			Status:     report.Status,
			HTTPStatus: report.HTTPStatus,
			LatencyMS:  report.LatencyMS,
			Error:      report.Error,
			Hint:       report.Hint,
		})
	}
	return out
}

// applyProviderModelDetect persists the decisive values in one configuration
// transaction. overwrite=false only fills a NULL column; overwrite=true replaces
// an explicit value too. Models whose protocol family has no capability columns
// (messages/generate_content) are report-only.
func applyProviderModelDetect(ctx context.Context, st *store.Store, req providerModelDetectRequest, detections []channel.ModelInterfaceDetection) ([]providerModelDetectApplied, []string, error) {
	applied := make([]providerModelDetectApplied, len(detections))
	skipped := make([]string, len(detections))
	err := st.ConfigurationTransaction(ctx, func(tx *store.Store, commit func() error) error {
		currentModels, err := tx.ListChannelModels(req.ChannelID, false)
		if err != nil {
			return err
		}
		currentByModel := make(map[string]store.ChannelModelRecord, len(currentModels))
		for _, record := range currentModels {
			currentByModel[record.Model] = record
		}
		for i, detection := range detections {
			current, ok := currentByModel[detection.Model]
			if !ok {
				skipped[i] = "model is no longer configured"
				continue
			}
			if detection.ProtocolFamily != upstream.ProtocolFamilyOpenAICompatible {
				skipped[i] = "protocol family " + detection.ProtocolFamily + " has only report-only interfaces"
				continue
			}
			reasons := []string{}
			patch := store.ChannelModelProfilePatch{}
			value := providerModelDetectApplied{}
			if detection.SupportsChatCompletions != nil {
				if req.Overwrite || current.SupportsChatCompletions == nil {
					detected := *detection.SupportsChatCompletions
					patch.SupportsChatCompletions = &detected
					appliedValue := detected
					value.SupportsChatCompletions = &appliedValue
				} else {
					reasons = append(reasons, "supports_chat_completions is already configured")
				}
			}
			if detection.SupportsResponses != nil {
				if req.Overwrite || current.SupportsResponses == nil {
					detected := *detection.SupportsResponses
					patch.SupportsResponses = &detected
					appliedValue := detected
					value.SupportsResponses = &appliedValue
				} else {
					reasons = append(reasons, "supports_responses is already configured")
				}
			}
			if patch.SupportsChatCompletions == nil && patch.SupportsResponses == nil {
				if len(reasons) == 0 {
					reasons = append(reasons, "probe results were inconclusive")
				}
				skipped[i] = strings.Join(reasons, "; ")
				continue
			}
			if _, err := tx.UpdateChannelModelProfile(req.ChannelID, detection.Model, patch); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					skipped[i] = "model is no longer configured"
					continue
				}
				return err
			}
			applied[i] = value
		}
		return commit()
	})
	if err != nil {
		return nil, nil, err
	}
	return applied, skipped, nil
}

func writeChannelLookupError(w http.ResponseWriter, err error, notFoundMessage string) {
	switch {
	case errors.Is(err, sql.ErrNoRows), errors.Is(err, channel.ErrChannelModelNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": notFoundMessage})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
	}
}
