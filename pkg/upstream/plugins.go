package upstream

import "sort"

// Plugin is the enable/disable unit of the channel catalog: one vendor site
// (vendor + region) represented by one canonical preset and its plan variants.
// Plugins are compiled into the binary as catalog data; only their enabled
// state is runtime configuration. There is no dynamic plugin loading.
//
// A plugin's ID is its canonical preset ID. Billing plans (Coding Plan, Token
// Plan, ...) and protocol surfaces (OpenAI-compatible, Anthropic Messages, ...)
// are never separate plugins: plans are PlanVariants of the preset and protocol
// surfaces are per-protocol Endpoints served by one channel with one key.
type Plugin struct {
	// ID is the canonical preset identifier of the plugin.
	ID string
	// Name is the short English display name.
	Name string
	// NameZh is the short Simplified Chinese display name ("" = use Name).
	NameZh string
	// Region distinguishes independent sites of one vendor ("cn", "intl", "").
	Region string
	// Description is the English description (the preset Notes).
	Description string
	// DescriptionZh is the Simplified Chinese description ("" = Description).
	DescriptionZh string
	// DocsURL links to the vendor's API documentation.
	DocsURL string
	// KeysURL links to the vendor's API-key management page.
	KeysURL string
	// PresetID is the canonical preset backing this plugin (equal to ID).
	PresetID string
}

// pluginFromPreset projects a visible preset onto its plugin view.
func pluginFromPreset(preset Preset) Plugin {
	return Plugin{
		ID:            preset.ID,
		Name:          preset.Name,
		NameZh:        preset.NameZh,
		Region:        preset.Region,
		Description:   preset.Notes,
		DescriptionZh: preset.NotesZh,
		DocsURL:       preset.DocsURL,
		KeysURL:       preset.KeysURL,
		PresetID:      preset.ID,
	}
}

// Plugins returns one plugin per visible (non-hidden) preset, sorted by ID.
// The returned values are copies; callers cannot mutate the catalog.
func Plugins() []Plugin {
	out := make([]Plugin, 0, len(builtinPresets))
	for _, preset := range builtinPresets {
		if preset.Hidden {
			continue
		}
		out = append(out, pluginFromPreset(preset))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// PluginIDs returns the canonical plugin IDs in sorted order.
func PluginIDs() []string {
	plugins := Plugins()
	ids := make([]string, 0, len(plugins))
	for _, plugin := range plugins {
		ids = append(ids, plugin.ID)
	}
	return ids
}

// LookupPlugin returns the plugin with the given canonical ID or alias. The
// lookup is case-insensitive and treats '-' and ' ' as '_'.
func LookupPlugin(id string) (Plugin, bool) {
	preset, ok := LookupPreset(id)
	if !ok || preset.Hidden {
		return Plugin{}, false
	}
	return pluginFromPreset(preset), true
}

// PluginForPreset resolves any preset ID — including aliases and hidden legacy
// presets — to the plugin that owns it. A hidden preset resolves through its
// Replacement mapping; a hidden preset without a replacement has no plugin.
func PluginForPreset(presetID string) (Plugin, bool) {
	preset, ok := LookupPreset(presetID)
	if !ok {
		return Plugin{}, false
	}
	if preset.Hidden {
		if preset.Replacement == nil {
			return Plugin{}, false
		}
		preset, ok = LookupPreset(preset.Replacement.PresetID)
		if !ok || preset.Hidden {
			return Plugin{}, false
		}
	}
	return pluginFromPreset(preset), true
}

// PresetIDsForPlugin returns every preset ID that belongs to the plugin: the
// canonical preset ID first, then each hidden legacy preset whose Replacement
// points at it, in sorted order. Channel ownership queries use this set so a
// channel created from a legacy preset still counts toward its plugin.
func PresetIDsForPlugin(pluginID string) []string {
	plugin, ok := LookupPlugin(pluginID)
	if !ok {
		return nil
	}
	ids := []string{plugin.PresetID}
	for _, preset := range builtinPresets {
		if !preset.Hidden || preset.Replacement == nil {
			continue
		}
		if normalizeKey(preset.Replacement.PresetID) == normalizeKey(plugin.PresetID) {
			ids = append(ids, preset.ID)
		}
	}
	sort.Strings(ids[1:])
	return ids
}

// VisiblePresets returns a deep copy of the non-hidden catalog entries, sorted
// by ID. The catalog API serves these; hidden legacy presets stay resolvable
// through LookupPreset but are never offered to new channels.
func VisiblePresets() []Preset {
	out := make([]Preset, 0, len(builtinPresets))
	for _, preset := range builtinPresets {
		if preset.Hidden {
			continue
		}
		out = append(out, clonePreset(preset))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}
