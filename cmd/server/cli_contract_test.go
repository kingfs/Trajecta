package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// captureRunOutput runs the CLI with os.Stdout/os.Stderr redirected to pipes and
// returns the exit code plus both streams. run() is the only entry point that
// exercises the top-level error classification in root.go.
func captureRunOutput(t *testing.T, args []string) (int, string, string) {
	t.Helper()

	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe() error = %v", err)
	}
	originalStdout, originalStderr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = stdoutWriter, stderrWriter
	defer func() {
		os.Stdout, os.Stderr = originalStdout, originalStderr
	}()

	type stream struct {
		out string
		err string
	}
	done := make(chan stream, 1)
	go func() {
		var captured stream
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			data, _ := io.ReadAll(stdoutReader)
			captured.out = string(data)
		}()
		go func() {
			defer wg.Done()
			data, _ := io.ReadAll(stderrReader)
			captured.err = string(data)
		}()
		wg.Wait()
		done <- captured
	}()

	code := run(args)
	stdoutWriter.Close()
	stderrWriter.Close()
	captured := <-done
	return code, captured.out, captured.err
}

func TestRunUnknownCommandExitsWithUsageCode(t *testing.T) {
	code, _, stderr := captureRunOutput(t, []string{"nosuchcommand"})
	if code != exitCodeUsage {
		t.Fatalf("run(unknown command) = %d, want %d (stderr=%q)", code, exitCodeUsage, stderr)
	}
}

func TestRunUnknownCommandHonorsJSONFormat(t *testing.T) {
	code, _, stderr := captureRunOutput(t, []string{"--format", "json", "nosuchcommand"})
	if code != exitCodeUsage {
		t.Fatalf("run(--format json unknown command) = %d, want %d (stderr=%q)", code, exitCodeUsage, stderr)
	}
	envelope := decodeCLIEnvelope(t, stderr)
	if envelope.OK {
		t.Fatalf("envelope.OK = true, want false")
	}
	if envelope.Error == nil || envelope.Error.Code != "USAGE_ERROR" {
		t.Fatalf("envelope.Error = %+v, want USAGE_ERROR", envelope.Error)
	}
	if !strings.Contains(envelope.Error.Message, "unknown command") {
		t.Fatalf("envelope message = %q, want it to name the unknown command", envelope.Error.Message)
	}
}

func TestRunJSONEnvelopeCarriesTheLoggedCause(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	code, _, stderr := captureRunOutput(t, []string{"--format", "json", "-c", missing, "config", "inspect"})
	if code == 0 {
		t.Fatalf("run(missing config) = 0, want a failure (stderr=%q)", stderr)
	}
	envelope := decodeCLIEnvelope(t, stderr)
	if envelope.Error == nil {
		t.Fatalf("stderr = %q, want a JSON error envelope", stderr)
	}
	if !strings.Contains(envelope.Error.Message, "no such file") {
		t.Fatalf("envelope message = %q, want the real cause instead of %q", envelope.Error.Message, "command failed")
	}
}

func TestRunMigrateDownReportsTheUnsupportedMessageOnce(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	body := `
server:
  port: "19301"
monitor:
  port: "19302"
database:
  driver: "sqlite"
  dsn: "{{output_dir}}/trajecta.sqlite3"
trace:
  output_dir: "{{temp_dir}}"
`
	body = strings.ReplaceAll(body, "{{output_dir}}", filepath.Join(t.TempDir(), "traces"))
	body = strings.ReplaceAll(body, "{{temp_dir}}", t.TempDir())
	if err := os.WriteFile(configPath, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	code, _, stderr := captureRunOutput(t, []string{"-c", configPath, "db", "migrate", "down"})
	if code != exitCodeUsage {
		t.Fatalf("run(db migrate down) = %d, want %d (stderr=%q)", code, exitCodeUsage, stderr)
	}
	if count := strings.Count(stderr, appDBMigrateDownUnsupportedMessage); count != 1 {
		t.Fatalf("stderr mentions the unsupported message %d times, want 1:\n%s", count, stderr)
	}
}

func TestRunMigrateDownJSONStderrIsOnlyAnEnvelope(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	body := `
server:
  port: "19311"
monitor:
  port: "19312"
database:
  driver: "sqlite"
  dsn: "{{output_dir}}/trajecta.sqlite3"
trace:
  output_dir: "{{temp_dir}}"
`
	body = strings.ReplaceAll(body, "{{output_dir}}", filepath.Join(t.TempDir(), "traces"))
	body = strings.ReplaceAll(body, "{{temp_dir}}", t.TempDir())
	if err := os.WriteFile(configPath, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	code, _, stderr := captureRunOutput(t, []string{"-c", configPath, "--format", "json", "db", "migrate", "down"})
	if code != exitCodeUsage {
		t.Fatalf("run(db migrate down --format json) = %d, want %d (stderr=%q)", code, exitCodeUsage, stderr)
	}
	// db migrate down never loads the database, so stderr must be the envelope
	// alone: the bare text line used to be printed before it.
	envelope := decodeCLIEnvelope(t, strings.TrimSpace(stderr))
	if envelope.Error == nil || envelope.Error.Code != "UNSUPPORTED_DB_MIGRATE_DOWN" {
		t.Fatalf("envelope = %+v, want UNSUPPORTED_DB_MIGRATE_DOWN", envelope)
	}
}

func TestWriteSecretKeyFileTightensExistingPermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "export.key")
	if err := os.WriteFile(path, []byte("stale"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}

	if err := writeSecretKeyFile(path, []byte("tlsec:v1:secret")); err != nil {
		t.Fatalf("writeSecretKeyFile() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("mode = %o, want 600 even for a pre-existing file", mode)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if string(content) != "tlsec:v1:secret" {
		t.Fatalf("content = %q, want the exported key", content)
	}
}

func TestResultWarningsAreHoistedIntoTheEnvelope(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		result any
		want   int
	}{
		{name: "map", result: map[string]any{"warnings": []string{"a", "b"}}, want: 2},
		{name: "struct", result: struct{ Warnings []string }{Warnings: []string{"a"}}, want: 1},
		{name: "reports", result: struct {
			Reports []struct{ Warnings []string }
		}{Reports: []struct{ Warnings []string }{{Warnings: []string{"a", "b"}}, {Warnings: []string{"c"}}}}, want: 3},
		{name: "none", result: map[string]any{"ok": true}, want: 0},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := len(resultWarnings(tc.result)); got != tc.want {
				t.Fatalf("resultWarnings() = %d warnings, want %d", got, tc.want)
			}
		})
	}
}

type cliEnvelopeForTest struct {
	OK       bool            `json:"ok"`
	Warnings []string        `json:"warnings"`
	Error    *cliErrorForRun `json:"error"`
}

type cliErrorForRun struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// decodeCLIEnvelope reads the last JSON object printed on stderr, skipping the
// text log lines that precede it.
func decodeCLIEnvelope(t *testing.T, stderr string) cliEnvelopeForTest {
	t.Helper()

	index := strings.LastIndex(stderr, "\n{")
	if index < 0 {
		if strings.HasPrefix(strings.TrimSpace(stderr), "{") {
			index = -1
		} else {
			t.Fatalf("stderr = %q, want a JSON envelope", stderr)
		}
	}
	payload := stderr[index+1:]
	var envelope cliEnvelopeForTest
	if err := json.Unmarshal([]byte(payload), &envelope); err != nil {
		t.Fatalf("json.Unmarshal(%q) error = %v", payload, err)
	}
	return envelope
}
