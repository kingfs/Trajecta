package config

import "testing"

func TestValidateServeTLSRequiresPair(t *testing.T) {
	for _, cert := range []string{"", "cert.pem", " "} {
		for _, key := range []string{"", "key.pem", " "} {
			cfg := &Config{}
			cfg.Server.TLSCert, cfg.Server.TLSKey = cert, key
			wantError := (cert == "cert.pem") != (key == "key.pem")
			if err := ValidateServe(cfg); (err != nil) != wantError {
				t.Errorf("cert=%q key=%q: error=%v, wantError=%v", cert, key, err, wantError)
			}
		}
	}
}

func TestTLSEnvironmentOverrides(t *testing.T) {
	t.Setenv("TRAJECTA_SERVER_TLS_CERT", " cert.pem ")
	t.Setenv("TRAJECTA_SERVER_TLS_KEY", " key.pem ")
	cfg := &Config{}
	applyEnvOverrides(cfg)
	if cfg.ServerScheme() != "https://" || cfg.ValidateTLS() != nil {
		t.Fatalf("TLS environment not applied: cert=%q key=%q", cfg.Server.TLSCert, cfg.Server.TLSKey)
	}
}
