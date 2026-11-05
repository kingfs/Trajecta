package monitor

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/kingfs/Trajecta/internal/channel"
	"github.com/kingfs/Trajecta/internal/modelprobe"
	"github.com/kingfs/Trajecta/internal/store"
)

// providerModelTestRequest is the POST /api/provider-model-test payload. The
// frontend sends one channel model and optionally restricts the probe to one
// protocol; "auto" (or a missing field) probes every declared surface.
type providerModelTestRequest struct {
	ChannelID string `json:"channel_id"`
	Model     string `json:"model"`
	Protocol  string `json:"protocol"`
}

// providerModelTestAPIHandler answers the per-model connectivity test: it
// resolves the channel model through the same configuration projection the
// forwarding path uses, sends the smallest protocol-appropriate request with the
// decrypted credential and reports whether that model really works over that
// protocol.
//
// The handler is deliberately "did not run" oriented: only a malformed request,
// an unknown channel/model or a failed store/config read is a 4xx/5xx. A probe
// that ran and failed is a 200 with a logical status, because the operator asked
// for an answer, not for an HTTP error.
//
// This endpoint is read-only: it writes no probe run, channel timestamp or
// configuration row.
func providerModelTestAPIHandler(st *store.Store, svc *channel.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var req providerModelTestRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid provider model test payload"})
			return
		}
		req.ChannelID = strings.TrimSpace(req.ChannelID)
		req.Model = strings.TrimSpace(req.Model)
		if req.ChannelID == "" || req.Model == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "channel_id and model are required"})
			return
		}
		protocol, err := modelprobe.ParseProtocol(req.Protocol)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		service := effectiveChannelService(st, svc)
		if service == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "channel service not configured"})
			return
		}
		target, err := service.ModelProbeTarget(req.ChannelID, req.Model)
		if err != nil {
			switch {
			case errors.Is(err, sql.ErrNoRows), errors.Is(err, channel.ErrChannelModelNotFound):
				writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			case errors.Is(err, channel.ErrChannelConfigInvalid):
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			default:
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "resolve channel model: " + err.Error()})
			}
			return
		}
		report := modelprobe.Run(r.Context(), target, protocol, service.HTTPClient())
		writeJSON(w, http.StatusOK, report)
	}
}
