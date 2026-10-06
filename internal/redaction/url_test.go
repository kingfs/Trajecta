package redaction

import (
	"strings"
	"testing"
)

func TestDisplayURLRedactsUserinfo(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "password",
			raw:  "https://user:secret@example.com/v1",
			want: "https://user:REDACTED@example.com/v1",
		},
		{
			name: "username only",
			raw:  "https://user@example.com/v1",
			want: "https://user:REDACTED@example.com/v1",
		},
		{
			name: "empty username",
			raw:  "https://:secret@example.com/v1",
			want: "https://REDACTED:REDACTED@example.com/v1",
		},
		{
			// A gateway configured with the key as the URL username would
			// otherwise have that key written into the cassette meta and the
			// logs table in plain text.
			name: "secret named in the username",
			raw:  "https://api_key_abc@example.com/v1",
			want: "https://REDACTED:REDACTED@example.com/v1",
		},
		{
			name: "token named in the username",
			raw:  "https://token@example.com/v1",
			want: "https://REDACTED:REDACTED@example.com/v1",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DisplayURL(tt.raw); got != tt.want {
				t.Fatalf("DisplayURL() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestSensitiveURLParamMarkersAreTheSharedList guards the marker list that both
// the recorder/proxy redaction and the CLI `config inspect` redaction consult.
// The CLI used to keep a private copy that had lost five of these markers.
func TestSensitiveURLParamMarkersAreTheSharedList(t *testing.T) {
	for _, key := range []string{
		"key", "api_key", "x-api-key", "token", "access_token", "refresh_token",
		"secret", "client_secret", "password", "passwd", "credential",
		"signature", "sig", "authorization", "Authorization", "auth",
		// A configuration key that names a DSN, which the CLI treated as sensitive
		// through a private list before it shared this one.
		"dsn", "TRAJECTA_DATABASE_DSN",
	} {
		if !IsSensitiveURLParam(key) {
			t.Errorf("IsSensitiveURLParam(%q) = false, want true", key)
		}
	}
	for _, key := range []string{"", "  ", "model", "sslmode", "application_name", "page", "limit", "host", "dbname"} {
		if IsSensitiveURLParam(key) {
			t.Errorf("IsSensitiveURLParam(%q) = true, want false", key)
		}
	}
}

func TestDisplayURLRedactsSensitiveQuery(t *testing.T) {
	raw := "https://example.com/v1?api_key=abc&token=def&access_token=ghi&client_secret=jkl&signature=mno&sig=pqr&password=stu&passwd=vwx&credential=yz&model=gpt-5"
	got := DisplayURL(raw)
	for _, leaked := range []string{"abc", "def", "ghi", "jkl", "mno", "pqr", "stu", "vwx", "yz"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("DisplayURL() leaked %q in %q", leaked, got)
		}
	}
	for _, want := range []string{
		"api_key=REDACTED",
		"token=REDACTED",
		"access_token=REDACTED",
		"client_secret=REDACTED",
		"signature=REDACTED",
		"sig=REDACTED",
		"password=REDACTED",
		"passwd=REDACTED",
		"credential=REDACTED",
		"model=gpt-5",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("DisplayURL() = %q, missing %q", got, want)
		}
	}
}

func TestDisplayURLPreservesNonURL(t *testing.T) {
	raw := "not a url ?api_key=abc"
	if got := DisplayURL(raw); got != raw {
		t.Fatalf("DisplayURL() = %q, want %q", got, raw)
	}
}

func TestDisplayURLRedactsRelativeQuery(t *testing.T) {
	got := DisplayURL("/v1/responses?access_token=abc&model=gpt-5")
	if strings.Contains(got, "abc") {
		t.Fatalf("DisplayURL() leaked relative query token: %q", got)
	}
	if !strings.Contains(got, "access_token=REDACTED") || !strings.Contains(got, "model=gpt-5") {
		t.Fatalf("DisplayURL() = %q, want redacted access_token and preserved model", got)
	}
}

func TestDisplayURLPreservesNonSensitiveQuery(t *testing.T) {
	raw := "https://example.com/v1?model=gpt-5&region=us"
	got := DisplayURL(raw)
	for _, want := range []string{"model=gpt-5", "region=us"} {
		if !strings.Contains(got, want) {
			t.Fatalf("DisplayURL() = %q, missing %q", got, want)
		}
	}
}

func TestMetadataTextRedactsCredentialMaterial(t *testing.T) {
	raw := `Authorization: Bearer sk-live-token api_key=abc123 refresh_token:"oauth-refresh" x-api-key: custom-secret {"type":"service_account","private_key":"-----BEGIN PRIVATE KEY-----\nabc"}`
	got := MetadataText(raw)
	for _, leaked := range []string{"sk-live-token", "abc123", "oauth-refresh", "custom-secret", "PRIVATE KEY"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("MetadataText() leaked %q in %q", leaked, got)
		}
	}
	if !strings.Contains(got, "REDACTED") {
		t.Fatalf("MetadataText() = %q, want redacted markers", got)
	}
}

func TestSafeCredentialHintRedactsSecretLikeHints(t *testing.T) {
	if got := SafeCredentialHint("Bearer sk-live-token"); got != "Bearer REDACTED" {
		t.Fatalf("SafeCredentialHint() = %q, want redacted bearer hint", got)
	}
	if got := SafeCredentialHint("acct-prod-east-1234567890"); got != "acct-prod-east-1234567890" {
		t.Fatalf("SafeCredentialHint() = %q, want safe hint unchanged", got)
	}
}
