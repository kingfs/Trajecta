package config

import (
	"gopkg.in/yaml.v3"
	"testing"
)

func TestAuthCookieSecureDefaultsAndOverrides(t *testing.T) {
	for _, tc := range []struct {
		name, yaml, env string
		want            bool
	}{
		{"default", "", "", true},
		{"YAML development", "auth:\n  cookie_secure: false\n", "", false},
		{"environment development", "", "false", false},
		{"environment overrides YAML", "auth:\n  cookie_secure: false\n", "true", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TRAJECTA_AUTH_COOKIE_SECURE", tc.env)
			var cfg Config
			if err := yaml.Unmarshal([]byte(tc.yaml), &cfg); err != nil {
				t.Fatal(err)
			}
			applyEnvOverrides(&cfg)
			if got := cfg.AuthCookieSecure(); got != tc.want {
				t.Fatalf("secure=%v want=%v", got, tc.want)
			}
		})
	}
}
