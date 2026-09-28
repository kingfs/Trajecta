package legacymigrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMapLegacyEnvKey(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		renamed bool
	}{
		{"LLM_TRACELAB_DATABASE_DSN", "TRAJECTA_DATABASE_DSN", true},
		{"LLM_TRACELAB_TRACE_OUTPUT_DIR", "TRAJECTA_TRACE_OUTPUT_DIR", true},
		{"TRAJECTA_DATABASE_DSN", "TRAJECTA_DATABASE_DSN", false},
		{"PATH", "PATH", false},
		{"LLM_TRACELAB_", "TRAJECTA_", true},
	}
	for _, tc := range cases {
		got, renamed := MapLegacyEnvKey(tc.in)
		if got != tc.want || renamed != tc.renamed {
			t.Errorf("MapLegacyEnvKey(%q) = (%q, %v), want (%q, %v)", tc.in, got, renamed, tc.want, tc.renamed)
		}
	}
}

func TestParseEnvFileValueForms(t *testing.T) {
	data := []byte(`# leading comment
export LLM_TRACELAB_DATABASE_DRIVER=postgres
TRAJECTA_TRACE_OUTPUT_DIR="./data/traces"   # trailing comment
LLM_TRACELAB_BOOTSTRAP_API_KEY='sk-with # hash'
TRAJECTA_DATABASE_DSN="postgres://user:pw@host:5432/db?sslmode=disable"
EMPTY_VALUE=
WITH_ESCAPE="line1\nline2\t\"quoted\""
NO_EXPANSION='$HOME/${USER}'
`)
	entries, warnings := ParseEnvFile(data)
	if len(warnings) != 0 {
		t.Fatalf("ParseEnvFile() warnings = %v, want none", warnings)
	}
	want := []struct {
		source  string
		key     string
		value   string
		renamed bool
	}{
		{"LLM_TRACELAB_DATABASE_DRIVER", "TRAJECTA_DATABASE_DRIVER", "postgres", true},
		{"TRAJECTA_TRACE_OUTPUT_DIR", "TRAJECTA_TRACE_OUTPUT_DIR", "./data/traces", false},
		{"LLM_TRACELAB_BOOTSTRAP_API_KEY", "TRAJECTA_BOOTSTRAP_API_KEY", "sk-with # hash", true},
		{"TRAJECTA_DATABASE_DSN", "TRAJECTA_DATABASE_DSN", "postgres://user:pw@host:5432/db?sslmode=disable", false},
		{"EMPTY_VALUE", "EMPTY_VALUE", "", false},
		{"WITH_ESCAPE", "WITH_ESCAPE", "line1\nline2\t\"quoted\"", false},
		{"NO_EXPANSION", "NO_EXPANSION", "$HOME/${USER}", false},
	}
	if len(entries) != len(want) {
		t.Fatalf("ParseEnvFile() returned %d entries, want %d: %+v", len(entries), len(want), entries)
	}
	for i, expected := range want {
		got := entries[i]
		if got.SourceKey != expected.source || got.Key != expected.key || got.Value != expected.value || got.Renamed != expected.renamed {
			t.Errorf("entry %d = %+v, want {%s %s %s %v}", i, got, expected.source, expected.key, expected.value, expected.renamed)
		}
	}
}

func TestParseEnvFileWarnings(t *testing.T) {
	data := []byte("this line has no equals sign\n1INVALID=value\nTRAJECTA_OK=yes\nUNTERMINATED=\"abc\n")
	entries, warnings := ParseEnvFile(data)
	if len(entries) != 1 || entries[0].Key != "TRAJECTA_OK" {
		t.Fatalf("ParseEnvFile() entries = %+v, want only TRAJECTA_OK", entries)
	}
	if len(warnings) != 3 {
		t.Fatalf("ParseEnvFile() warnings = %v, want 3", warnings)
	}
	for _, want := range []string{"expected KEY=VALUE", "invalid variable name", "unterminated"} {
		found := false
		for _, warning := range warnings {
			if strings.Contains(warning, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("warnings %v do not mention %q", warnings, want)
		}
	}
}

func TestLoadEnvFilePrecedence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "LLM_TRACELAB_DATABASE_DSN=from-file\nTRAJECTA_MIGRATE_TEST_NEW=value\nTRAJECTA_MIGRATE_TEST_DUP=first\nTRAJECTA_MIGRATE_TEST_DUP=second\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(.env) error = %v", err)
	}
	for _, key := range []string{"TRAJECTA_DATABASE_DSN", "TRAJECTA_MIGRATE_TEST_NEW", "TRAJECTA_MIGRATE_TEST_DUP"} {
		t.Cleanup(func() { _ = os.Unsetenv(key) })
	}

	t.Setenv("TRAJECTA_DATABASE_DSN", "from-shell")
	result, err := LoadEnvFile(path, false)
	if err != nil {
		t.Fatalf("LoadEnvFile() error = %v", err)
	}
	if got := os.Getenv("TRAJECTA_DATABASE_DSN"); got != "from-shell" {
		t.Errorf("process environment value = %q, want from-shell", got)
	}
	if _, ok := os.LookupEnv("LLM_TRACELAB_DATABASE_DSN"); ok {
		t.Errorf("the legacy variable name must not be exported")
	}
	if got := os.Getenv("TRAJECTA_MIGRATE_TEST_NEW"); got != "value" {
		t.Errorf("TRAJECTA_MIGRATE_TEST_NEW = %q, want value", got)
	}
	if got := os.Getenv("TRAJECTA_MIGRATE_TEST_DUP"); got != "second" {
		t.Errorf("the last assignment must win, got %q", got)
	}
	if result.Kept != 1 {
		t.Errorf("Kept = %d, want 1", result.Kept)
	}
	if result.Applied != 2 {
		t.Errorf("Applied = %d, want 2", result.Applied)
	}
	if len(result.Warnings) != 1 || !strings.Contains(result.Warnings[0], "overridden") {
		t.Errorf("Warnings = %v, want one override warning", result.Warnings)
	}

	result, err = LoadEnvFile(path, true)
	if err != nil {
		t.Fatalf("LoadEnvFile(override) error = %v", err)
	}
	if got := os.Getenv("TRAJECTA_DATABASE_DSN"); got != "from-file" {
		t.Errorf("override value = %q, want from-file", got)
	}
	if result.Kept != 0 {
		t.Errorf("Kept = %d, want 0 with override", result.Kept)
	}
}

func TestLoadEnvFileMissingFile(t *testing.T) {
	if _, err := LoadEnvFile(filepath.Join(t.TempDir(), "absent.env"), false); err == nil {
		t.Fatal("LoadEnvFile() error = nil, want an error for a missing file")
	}
}

func TestEnvFileCandidate(t *testing.T) {
	if got, want := EnvFileCandidate("."), "."+string(os.PathSeparator)+".env"; got != want {
		t.Errorf("EnvFileCandidate(.) = %q, want %q", got, want)
	}
	if got, want := EnvFileCandidate("/data/gateway/"), "/data/gateway/.env"; got != want {
		t.Errorf("EnvFileCandidate(/data/gateway/) = %q, want %q", got, want)
	}
	if got, want := EnvFileCandidate(""), "."+string(os.PathSeparator)+".env"; got != want {
		t.Errorf("EnvFileCandidate(\"\") = %q, want %q", got, want)
	}
}
