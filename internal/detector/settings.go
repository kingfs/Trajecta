package detector

import (
	"context"
	"encoding/json"
	"sort"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
)

// SettingsKey names the app_settings row holding the detection-plugin enablement
// list and per-plugin settings. It is the detection analogue of plugins.channels.
const SettingsKey = "plugins.detectors"

// Config is the persisted plugin state. Enabled is authoritative when a row
// exists; a missing row falls back to DefaultConfig so the binary keeps the
// behaviour it had before pluginisation (masking and the builtin analysis
// detectors on). Settings carries opaque per-plugin configuration keyed by ID.
type Config struct {
	Enabled   []string                   `json:"enabled"`
	Settings  map[string]json.RawMessage `json:"settings,omitempty"`
	UpdatedAt time.Time                  `json:"updated_at,omitempty"`
}

// DefaultConfig enables every builtin plugin whose DefaultEnabled is set. It is
// the state used before an operator has written a row.
func DefaultConfig() Config {
	var enabled []string
	for _, p := range Plugins() {
		if p.DefaultEnabled {
			enabled = append(enabled, p.ID)
		}
	}
	sort.Strings(enabled)
	return Config{Enabled: enabled}
}

// LoadConfig reads the plugin state, falling back to DefaultConfig when no row
// exists. Stored IDs are filtered against the compiled catalog so a row written
// by a newer or older build cannot resurrect an unknown plugin, mirroring the
// channel-plugin loader.
func LoadConfig(ctx context.Context, st *store.Store) (Config, error) {
	if st == nil {
		return DefaultConfig(), nil
	}
	var stored Config
	found, err := st.LoadAppSettingJSON(ctx, SettingsKey, &stored)
	if err != nil {
		return Config{}, err
	}
	if !found {
		return DefaultConfig(), nil
	}
	cfg := Config{UpdatedAt: stored.UpdatedAt}
	seen := make(map[string]bool, len(stored.Enabled))
	for _, id := range stored.Enabled {
		if _, ok := Lookup(id); !ok || seen[id] {
			continue
		}
		seen[id] = true
		cfg.Enabled = append(cfg.Enabled, id)
	}
	sort.Strings(cfg.Enabled)
	if len(stored.Settings) > 0 {
		cfg.Settings = make(map[string]json.RawMessage, len(stored.Settings))
		for id, s := range stored.Settings {
			if _, ok := Lookup(id); ok {
				cfg.Settings[id] = s
			}
		}
	}
	return cfg, nil
}

// SaveConfig persists the plugin state with a fresh timestamp.
func SaveConfig(ctx context.Context, st *store.Store, cfg Config) error {
	cfg.UpdatedAt = time.Now().UTC()
	return st.SaveAppSettingJSON(ctx, SettingsKey, cfg)
}

// IsEnabled reports whether id is in the enablement list.
func (c Config) IsEnabled(id string) bool {
	for _, e := range c.Enabled {
		if e == id {
			return true
		}
	}
	return false
}

// SettingsFor returns the opaque settings for id, or nil.
func (c Config) SettingsFor(id string) Settings {
	if c.Settings == nil {
		return nil
	}
	return c.Settings[id]
}

func (c Config) enabledSet() map[string]bool {
	set := make(map[string]bool, len(c.Enabled))
	for _, id := range c.Enabled {
		set[id] = true
	}
	return set
}
