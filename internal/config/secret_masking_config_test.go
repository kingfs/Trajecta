package config

import (
	"testing"

	"github.com/kingfs/Trajecta/pkg/secretmask"
)

// TestSecretMaskingMappingDefaults pins the persisted-mapping contract: it is on
// unless the configuration turns it off, and a restart loads every recorded
// pair by default.
func TestSecretMaskingMappingDefaults(t *testing.T) {
	for _, tc := range []struct {
		name      string
		body      string
		env       map[string]string
		enabled   bool
		path      string
		loadLimit int
	}{
		{
			name:      "key omitted",
			body:      "trace:\n  output_dir: /tmp/trajecta-mapping\n",
			enabled:   true,
			loadLimit: DefaultSecretMappingLoadLimit,
		},
		{
			name:      "empty section",
			body:      "secret_masking: {}\n",
			enabled:   true,
			loadLimit: DefaultSecretMappingLoadLimit,
		},
		{
			name:      "explicitly off",
			body:      "secret_masking:\n  mapping_store:\n    enabled: false\n",
			enabled:   false,
			loadLimit: DefaultSecretMappingLoadLimit,
		},
		{
			name:      "explicit path and limit",
			body:      "secret_masking:\n  mapping_store:\n    path: /var/lib/trajecta/mappings.db\n    load_limit: 25\n",
			enabled:   true,
			path:      "/var/lib/trajecta/mappings.db",
			loadLimit: 25,
		},
		{
			name: "env off",
			body: "secret_masking: {}\n",
			env: map[string]string{
				"TRAJECTA_SECRET_MASKING_MAPPING_STORE_ENABLED": "false",
			},
			enabled:   false,
			loadLimit: DefaultSecretMappingLoadLimit,
		},
		{
			name: "env path and limit override the file",
			body: "secret_masking:\n  mapping_store:\n    path: /from/file.db\n    load_limit: 25\n",
			env: map[string]string{
				"TRAJECTA_SECRET_MASKING_MAPPING_STORE_PATH":       "/from/env.db",
				"TRAJECTA_SECRET_MASKING_MAPPING_STORE_LOAD_LIMIT": "7",
			},
			enabled:   true,
			path:      "/from/env.db",
			loadLimit: 7,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for key, value := range tc.env {
				t.Setenv(key, value)
			}
			cfg, err := Load(writeTempConfig(t, tc.body))
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if cfg.SecretMasking.MappingStore.Enabled != tc.enabled {
				t.Fatalf("MappingStore.Enabled = %v, want %v", cfg.SecretMasking.MappingStore.Enabled, tc.enabled)
			}
			if cfg.SecretMasking.MappingStore.Path != tc.path {
				t.Fatalf("MappingStore.Path = %q, want %q", cfg.SecretMasking.MappingStore.Path, tc.path)
			}
			if cfg.SecretMasking.MappingStore.LoadLimit != tc.loadLimit {
				t.Fatalf("MappingStore.LoadLimit = %d, want %d", cfg.SecretMasking.MappingStore.LoadLimit, tc.loadLimit)
			}
		})
	}
}

// TestSecretMappingLoadLimitConstantsAgree pins that the configuration default
// and the engine fallback cannot drift. The configuration package deliberately
// does not import the rule engine, so the number is repeated there.
func TestSecretMappingLoadLimitConstantsAgree(t *testing.T) {
	if DefaultSecretMappingLoadLimit != secretmask.DefaultMappingLoadLimit {
		t.Fatalf("DefaultSecretMappingLoadLimit = %d, secretmask.DefaultMappingLoadLimit = %d",
			DefaultSecretMappingLoadLimit, secretmask.DefaultMappingLoadLimit)
	}
}
