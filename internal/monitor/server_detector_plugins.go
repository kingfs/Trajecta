// Detection-plugin endpoints: the compiled internal/detector catalog plus the
// runtime enable/disable state and per-plugin settings stored in app_settings
// under plugins.detectors. It mirrors the channel-plugin endpoints
// (server_plugins.go): an admin-only GET list and PATCH toggle. Toggling a
// detector republishes the runtime snapshot so the masking coordinator, the
// audit projection and the reanalysis runner pick up the change without a
// restart. Disabling a hot-lane masking plugin while stand-in mappings exist
// requires an explicit force, the detection analogue of plugin_in_use.

package monitor

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/internal/detector"
	"github.com/kingfs/Trajecta/internal/store"
)

type detectorPluginItem struct {
	detector.Plugin
	Enabled  bool            `json:"enabled"`
	Settings json.RawMessage `json:"settings,omitempty"`
}

type detectorPluginsResponse struct {
	Items     []detectorPluginItem `json:"items"`
	UpdatedAt time.Time            `json:"updated_at,omitempty"`
}

func detectorPluginItemFrom(p detector.Plugin, cfg detector.Config) detectorPluginItem {
	return detectorPluginItem{
		Plugin:   p,
		Enabled:  cfg.IsEnabled(p.ID),
		Settings: cfg.SettingsFor(p.ID),
	}
}

// detectorPluginsAPIHandler serves GET /api/detector-plugins: the full detection
// catalog with enablement and per-plugin settings. Admin-only at registration.
func detectorPluginsAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}
		cfg, err := detector.LoadConfig(r.Context(), st)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		plugins := detector.Plugins()
		items := make([]detectorPluginItem, 0, len(plugins))
		for _, p := range plugins {
			items = append(items, detectorPluginItemFrom(p, cfg))
		}
		writeJSON(w, http.StatusOK, detectorPluginsResponse{Items: items, UpdatedAt: cfg.UpdatedAt})
	}
}

type detectorPluginPatchRequest struct {
	Enabled *bool `json:"enabled"`
	// Settings replaces the plugin's opaque configuration when present. A nil
	// Settings leaves the existing value untouched.
	Settings json.RawMessage `json:"settings,omitempty"`
	// Force acknowledges the disable check: the caller has seen the mapping
	// count and still wants to stop the hot-lane detector.
	Force bool `json:"force"`
}

// detectorPluginDetailAPIHandler serves PATCH /api/detector-plugins/{id}. It
// updates enablement and/or settings, persists the row and republishes the
// runtime. Disabling a hot-lane plugin while the vault holds stand-in mappings
// answers 409 plugin_in_use unless force is set.
func detectorPluginDetailAPIHandler(st *store.Store, rt *detector.Runtime, mappings SecretMappingAdmin) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			w.Header().Set("Allow", http.MethodPatch)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}
		id := strings.Trim(strings.TrimPrefix(pathClean(r.URL.Path), "/api/detector-plugins/"), "/")
		reg, ok := detector.Lookup(id)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "plugin not found"})
			return
		}
		var req detectorPluginPatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid plugin payload"})
			return
		}
		if req.Enabled == nil && req.Settings == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "enabled or settings is required"})
			return
		}
		cfg, err := detector.LoadConfig(r.Context(), st)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}

		if req.Enabled != nil && !*req.Enabled && reg.Plugin.HasLane(detector.LaneHot) && !req.Force {
			if count := mappingCount(r.Context(), mappings); count > 0 {
				writeJSON(w, http.StatusConflict, map[string]any{
					"error":         "plugin_in_use",
					"message":       "plugin has stand-in mappings; disabling it stops masking new values. Confirm with force.",
					"mapping_count": count,
				})
				return
			}
		}

		if req.Enabled != nil {
			enabled := make(map[string]bool, len(cfg.Enabled))
			for _, existing := range cfg.Enabled {
				enabled[existing] = true
			}
			enabled[id] = *req.Enabled
			cfg.Enabled = cfg.Enabled[:0]
			for pluginID, on := range enabled {
				if on {
					cfg.Enabled = append(cfg.Enabled, pluginID)
				}
			}
			sort.Strings(cfg.Enabled)
		}
		if req.Settings != nil {
			if cfg.Settings == nil {
				cfg.Settings = make(map[string]json.RawMessage)
			}
			cfg.Settings[id] = req.Settings
		}

		if err := detector.SaveConfig(r.Context(), st, cfg); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if rt != nil {
			if err := rt.Republish(r.Context()); err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "plugin state saved but republish failed: " + err.Error()})
				return
			}
		}
		writeJSON(w, http.StatusOK, detectorPluginItemFrom(reg.Plugin, cfg))
	}
}

// mappingCount reads the live vault's mapping count for the disable check. A nil
// vault or a read failure reports zero so the check fails open (the badge is
// advisory; disabling still stops detection).
func mappingCount(ctx context.Context, mappings SecretMappingAdmin) int {
	if mappings == nil {
		return 0
	}
	stats, err := mappings.MappingStats()
	if err != nil {
		return 0
	}
	return stats.EntryCount
}
