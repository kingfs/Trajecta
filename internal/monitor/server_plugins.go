// Channel plugin endpoints: the compiled pkg/upstream catalog grouped into
// vendor plugins, plus the runtime enable/disable state stored in app_settings.
// Plugins gate the official provider catalog only; they never change routing.
// Disabling a plugin with enabled channels requires an explicit force after the
// caller has seen the affected channels.

package monitor

import (
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
	pkgupstream "github.com/kingfs/Trajecta/pkg/upstream"
)

// pluginsSettingsKey names the app_settings row holding the enabled channel
// plugins. A missing row means every plugin is disabled: the official catalog
// starts empty and the operator opts in from the plugins page.
const pluginsSettingsKey = "plugins.channels"

type channelPluginSettings struct {
	Enabled   []string  `json:"enabled"`
	UpdatedAt time.Time `json:"updated_at,omitempty"`
}

func loadChannelPluginSettings(ctx context.Context, st *store.Store) (channelPluginSettings, error) {
	settings := channelPluginSettings{Enabled: []string{}}
	if st == nil {
		return settings, nil
	}
	var stored channelPluginSettings
	found, err := st.LoadAppSettingJSON(ctx, pluginsSettingsKey, &stored)
	if err != nil {
		return settings, err
	}
	if !found {
		return settings, nil
	}
	// Keep only known plugin IDs, trimmed and deduplicated: a stale row from a
	// newer/older build must not resurrect unknown plugins.
	seen := make(map[string]bool, len(stored.Enabled))
	for _, id := range stored.Enabled {
		plugin, ok := pkgupstream.LookupPlugin(id)
		if !ok || seen[plugin.ID] {
			continue
		}
		seen[plugin.ID] = true
		settings.Enabled = append(settings.Enabled, plugin.ID)
	}
	sort.Strings(settings.Enabled)
	settings.UpdatedAt = stored.UpdatedAt
	return settings, nil
}

func enabledPluginSet(ctx context.Context, st *store.Store) (map[string]bool, error) {
	settings, err := loadChannelPluginSettings(ctx, st)
	if err != nil {
		return nil, err
	}
	enabled := make(map[string]bool, len(settings.Enabled))
	for _, id := range settings.Enabled {
		enabled[id] = true
	}
	return enabled, nil
}

func saveChannelPluginSettings(ctx context.Context, st *store.Store, settings channelPluginSettings) error {
	settings.UpdatedAt = time.Now().UTC()
	return st.SaveAppSettingJSON(ctx, pluginsSettingsKey, settings)
}

// pluginChannelRef is one channel that belongs to a plugin, as reported by the
// disable check. It carries no credential material.
type pluginChannelRef struct {
	ID                string `json:"id"`
	Name              string `json:"name"`
	Enabled           bool   `json:"enabled"`
	EnabledModelCount int    `json:"enabled_model_count"`
}

// pluginItem is one channel plugin with its catalog payload and usage facts.
type pluginItem struct {
	ID                  string              `json:"id"`
	Name                string              `json:"name"`
	NameZh              string              `json:"name_zh,omitempty"`
	Region              string              `json:"region,omitempty"`
	Description         string              `json:"description,omitempty"`
	DescriptionZh       string              `json:"description_zh,omitempty"`
	DocsURL             string              `json:"docs_url,omitempty"`
	KeysURL             string              `json:"keys_url,omitempty"`
	Enabled             bool                `json:"enabled"`
	ChannelCount        int                 `json:"channel_count"`
	EnabledChannelCount int                 `json:"enabled_channel_count"`
	EnabledModelCount   int                 `json:"enabled_model_count"`
	Preset              providerCatalogItem `json:"preset"`
}

type pluginsResponse struct {
	Items     []pluginItem `json:"items"`
	UpdatedAt time.Time    `json:"updated_at,omitempty"`
}

// pluginChannelStats collects the channels owned by one plugin (including
// channels created from hidden legacy presets) and their enabled model counts.
func pluginChannelStats(st *store.Store, pluginID string) (refs []pluginChannelRef, enabledChannels int, enabledModels int) {
	if st == nil {
		return nil, 0, 0
	}
	presetIDs := pkgupstream.PresetIDsForPlugin(pluginID)
	if len(presetIDs) == 0 {
		return nil, 0, 0
	}
	owned := make(map[string]bool, len(presetIDs))
	for _, id := range presetIDs {
		owned[strings.ToLower(strings.TrimSpace(id))] = true
	}
	channels, err := st.ListChannelConfigMetadata()
	if err != nil {
		return nil, 0, 0
	}
	var channelIDs []string
	for _, channel := range channels {
		if !owned[strings.ToLower(strings.TrimSpace(channel.ProviderPreset))] {
			continue
		}
		channelIDs = append(channelIDs, channel.ID)
		refs = append(refs, pluginChannelRef{ID: channel.ID, Name: channel.Name, Enabled: channel.Enabled})
		if channel.Enabled {
			enabledChannels++
		}
	}
	if len(channelIDs) == 0 {
		return refs, 0, 0
	}
	byChannel := make(map[string]int, len(refs))
	models, err := st.ListChannelModels("", true)
	if err == nil {
		for _, model := range models {
			byChannel[model.ChannelID]++
		}
	}
	for i := range refs {
		refs[i].EnabledModelCount = byChannel[refs[i].ID]
		if refs[i].Enabled {
			enabledModels += refs[i].EnabledModelCount
		}
	}
	return refs, enabledChannels, enabledModels
}

func pluginItemFrom(plugin pkgupstream.Plugin, enabled bool, st *store.Store) pluginItem {
	refs, enabledChannels, enabledModels := pluginChannelStats(st, plugin.ID)
	item := pluginItem{
		ID:                  plugin.ID,
		Name:                plugin.Name,
		NameZh:              plugin.NameZh,
		Region:              plugin.Region,
		Description:         plugin.Description,
		DescriptionZh:       plugin.DescriptionZh,
		DocsURL:             plugin.DocsURL,
		KeysURL:             plugin.KeysURL,
		Enabled:             enabled,
		ChannelCount:        len(refs),
		EnabledChannelCount: enabledChannels,
		EnabledModelCount:   enabledModels,
	}
	if preset, ok := pkgupstream.LookupPreset(plugin.PresetID); ok {
		item.Preset = providerCatalogItemFrom(preset)
	}
	return item
}

// pluginsAPIHandler serves GET /api/plugins: the full plugin catalog with the
// enabled state and per-plugin channel usage. Admin-only at registration.
func pluginsAPIHandler(st *store.Store) http.HandlerFunc {
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
		settings, err := loadChannelPluginSettings(r.Context(), st)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		enabled := make(map[string]bool, len(settings.Enabled))
		for _, id := range settings.Enabled {
			enabled[id] = true
		}
		plugins := pkgupstream.Plugins()
		items := make([]pluginItem, 0, len(plugins))
		for _, plugin := range plugins {
			items = append(items, pluginItemFrom(plugin, enabled[plugin.ID], st))
		}
		writeJSON(w, http.StatusOK, pluginsResponse{Items: items, UpdatedAt: settings.UpdatedAt})
	}
}

type pluginPatchRequest struct {
	Enabled *bool `json:"enabled"`
	// Force acknowledges the disable check: the caller has seen the enabled
	// channels and still wants to hide the plugin's catalog cards.
	Force bool `json:"force"`
}

// pluginDetailAPIHandler serves PATCH /api/plugins/{id}. Disabling a plugin
// never touches routing: existing channels keep serving traffic. When enabled
// channels still use the plugin the response is 409 with the affected channels
// unless force is set, so the UI can recommend disabling the channels first.
func pluginDetailAPIHandler(st *store.Store) http.HandlerFunc {
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
		id := strings.Trim(strings.TrimPrefix(pathClean(r.URL.Path), "/api/plugins/"), "/")
		plugin, ok := pkgupstream.LookupPlugin(id)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "plugin not found"})
			return
		}
		var req pluginPatchRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Enabled == nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid plugin payload: enabled is required"})
			return
		}
		settings, err := loadChannelPluginSettings(r.Context(), st)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if !*req.Enabled {
			refs, enabledChannels, enabledModels := pluginChannelStats(st, plugin.ID)
			if enabledChannels > 0 && !req.Force {
				active := make([]pluginChannelRef, 0, enabledChannels)
				for _, ref := range refs {
					if ref.Enabled {
						active = append(active, ref)
					}
				}
				writeJSON(w, http.StatusConflict, map[string]any{
					"error":                 "plugin_in_use",
					"message":               "plugin has enabled channels; disable the channels first or confirm with force",
					"enabled_channel_count": enabledChannels,
					"enabled_model_count":   enabledModels,
					"channels":              active,
				})
				return
			}
		}
		enabled := make(map[string]bool, len(settings.Enabled))
		for _, existing := range settings.Enabled {
			enabled[existing] = true
		}
		enabled[plugin.ID] = *req.Enabled
		settings.Enabled = settings.Enabled[:0]
		for id, on := range enabled {
			if on {
				settings.Enabled = append(settings.Enabled, id)
			}
		}
		sort.Strings(settings.Enabled)
		if err := saveChannelPluginSettings(r.Context(), st, settings); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, pluginItemFrom(plugin, *req.Enabled, st))
	}
}

// annotateChannelItemsPluginState marks each channel item with whether its
// plugin is enabled, so the UI can badge channels whose plugin was disabled.
// One settings load serves the whole list; a load failure fails open (the
// badge is cosmetic, routing never depends on it).
func annotateChannelItemsPluginState(ctx context.Context, st *store.Store, items []channelItem) {
	if st == nil || len(items) == 0 {
		return
	}
	enabled, err := enabledPluginSet(ctx, st)
	if err != nil {
		for i := range items {
			items[i].PluginEnabled = true
		}
		return
	}
	for i := range items {
		items[i].PluginEnabled = channelPluginEnabled(items[i].ProviderPreset, enabled)
	}
}

// channelPluginEnabled resolves a channel's preset (including hidden legacy
// presets) to its plugin and reports the enabled state. Custom channels (no
// preset) are always "enabled": they do not come from the catalog.
func channelPluginEnabled(presetID string, enabled map[string]bool) bool {
	if strings.TrimSpace(presetID) == "" {
		return true
	}
	plugin, ok := pkgupstream.PluginForPreset(presetID)
	if !ok {
		return true
	}
	return enabled[plugin.ID]
}

// annotateChannelItemPluginState is the single-item form of
// annotateChannelItemsPluginState.
func annotateChannelItemPluginState(ctx context.Context, st *store.Store, item *channelItem) {
	items := []channelItem{*item}
	annotateChannelItemsPluginState(ctx, st, items)
	*item = items[0]
}
