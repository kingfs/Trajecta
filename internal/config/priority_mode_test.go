package config

import "testing"

func TestValidateRouterPriorityMode(t *testing.T) {
	for _, mode := range []string{"", "compat", "strict", "COMPAT", " Strict "} {
		cfg := &Config{}
		cfg.Router.Selection.PriorityMode = mode
		if err := validateRouterEnums(cfg); err != nil {
			t.Fatalf("priority_mode %q rejected: %v", mode, err)
		}
	}
	cfg := &Config{}
	cfg.Router.Selection.PriorityMode = "tiered"
	if err := validateRouterEnums(cfg); err == nil {
		t.Fatal("unknown priority_mode accepted")
	}
}
