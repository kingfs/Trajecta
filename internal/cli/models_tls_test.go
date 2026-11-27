package cli

import (
	"testing"

	"github.com/kingfs/Trajecta/internal/config"
)

func TestModelsCodexProviderUsesTLS(t *testing.T) {
	cfg := &config.Config{}
	cfg.Server.Port = "8080"
	cfg.Server.TLSCert = "cert.pem"
	cfg.Server.TLSKey = "key.pem"
	provider, _ := buildModelsCodexProviderConfig(cfg)
	if provider.BaseURL != "https://127.0.0.1:8080/v1" {
		t.Fatalf("base URL = %q", provider.BaseURL)
	}
}
