package recorder

import (
	"bytes"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

func TestResponseBodyLimitTruncatesCassetteAndRepairsContentLength(t *testing.T) {
	dir := t.TempDir()
	rec := New(dir, false, nil)
	rec.SetResponseBodyLimitBytes(4)

	requestBody := []byte(`{"model":"gpt-test","prompt":"private request"}`)
	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/completions?api_key=original-query-secret", bytes.NewReader(requestBody))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Authorization", "Bearer original-header-secret")
	info, err := rec.PrepareLogFileWithOptionsAndBody(req, PrepareOptions{SiteURL: "https://api.openai.com"}, requestBody)
	if err != nil {
		t.Fatalf("PrepareLogFileWithOptionsAndBody() error = %v", err)
	}

	responseHeader := []byte("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\nContent-Length: 10\r\n\r\n")
	if _, err := info.File.Write(append([]byte("\n"), responseHeader...)); err != nil {
		t.Fatalf("write response header: %v", err)
	}
	info.Header.Meta.StatusCode = http.StatusOK
	info.Header.Layout.ResHeaderLen = int64(len(responseHeader))
	written, err := info.WriteResponseBody([]byte("abcdefghij"))
	if err != nil {
		t.Fatalf("WriteResponseBody() error = %v", err)
	}
	if written != 4 {
		t.Fatalf("stored response bytes = %d, want 4", written)
	}
	if info.Header.Layout.ResBodyLen != 4 || info.ResponseBodyTruncatedBytes != 6 || !info.ResponseBodyLimitHit {
		t.Fatalf("response recording state = layout=%+v omitted=%d limit_hit=%t", info.Header.Layout, info.ResponseBodyTruncatedBytes, info.ResponseBodyLimitHit)
	}
	if err := rec.UpdateLogFile(info); err != nil {
		t.Fatalf("UpdateLogFile() error = %v", err)
	}
	cassette, err := os.ReadFile(info.Path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", info.Path, err)
	}
	parsed, err := recordfile.ParsePrelude(cassette)
	if err != nil {
		t.Fatalf("ParsePrelude() error = %v", err)
	}
	if parsed.Header.Layout.ResBodyLen != 4 || parsed.Header.Layout.ResBodyTruncatedBytes != 6 {
		t.Fatalf("recorded layout = %+v, want 4 stored and 6 omitted bytes", parsed.Header.Layout)
	}
	request, _, response, responseBody := recordfile.ExtractSections(cassette, parsed)
	if !strings.Contains(string(request), "/v1/completions?api_key=original-query-secret") ||
		!strings.Contains(string(request), "Bearer original-header-secret") ||
		!bytes.Contains(request, requestBody) {
		t.Fatalf("truncation changed the original request section: %q", request)
	}
	if !bytes.Contains(response, []byte("Content-Length: 4\r\n")) {
		t.Fatalf("response header did not declare stored length: %q", response)
	}
	if string(responseBody) != "abcd" {
		t.Fatalf("recorded response body = %q, want %q", responseBody, "abcd")
	}

	foundTruncation := false
	for _, event := range parsed.Events {
		if event.Type != "recording.response_body_truncated" {
			continue
		}
		foundTruncation = true
		if event.Attributes["omitted_bytes"] != float64(6) || event.Attributes["reason"] != "size_limit" {
			t.Fatalf("truncation event attributes = %#v", event.Attributes)
		}
	}
	if !foundTruncation {
		t.Fatalf("recording.response_body_truncated event not found: %+v", parsed.Events)
	}
	fileInfo, err := os.Stat(info.Path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("cassette mode = %04o, want 0600", got)
	}
}

func TestMinFreeSpaceStopsCassetteAtResponsePrefix(t *testing.T) {
	rec := New(t.TempDir(), false, nil)
	rec.SetMinFreeBytes(100)
	checks := 0
	rec.diskSpaceAvailable = func(string) (int64, error) {
		checks++
		return 100, nil
	}

	req, err := http.NewRequest(http.MethodGet, "http://proxy.local/v1/models", nil)
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	info, err := rec.PrepareLogFileWithOptionsAndBody(req, PrepareOptions{SiteURL: "https://api.openai.com"}, nil)
	if err != nil {
		t.Fatalf("PrepareLogFileWithOptionsAndBody() error = %v", err)
	}
	responseHeader := []byte("HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n\r\n")
	if _, err := info.File.Write(append([]byte("\n"), responseHeader...)); err != nil {
		t.Fatalf("write response header: %v", err)
	}
	info.Header.Meta.StatusCode = http.StatusOK
	info.Header.Layout.ResHeaderLen = int64(len(responseHeader))
	for _, chunk := range []string{"first", "later"} {
		if _, err := info.WriteResponseBody([]byte(chunk)); err != nil {
			t.Fatalf("WriteResponseBody(%q) error = %v", chunk, err)
		}
	}
	if checks != 1 {
		t.Fatalf("disk-space checks = %d, want one check before pausing this response", checks)
	}
	if info.Header.Layout.ResBodyLen != 0 || info.ResponseBodyTruncatedBytes != int64(len("firstlater")) || !info.MinFreeSpaceHit {
		t.Fatalf("response recording state = layout=%+v omitted=%d min_free_hit=%t", info.Header.Layout, info.ResponseBodyTruncatedBytes, info.MinFreeSpaceHit)
	}
	if err := rec.UpdateLogFile(info); err != nil {
		t.Fatalf("UpdateLogFile() error = %v", err)
	}
	cassette, err := os.ReadFile(info.Path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", info.Path, err)
	}
	parsed, err := recordfile.ParsePrelude(cassette)
	if err != nil {
		t.Fatalf("ParsePrelude() error = %v", err)
	}
	_, _, _, responseBody := recordfile.ExtractSections(cassette, parsed)
	if len(responseBody) != 0 {
		t.Fatalf("response body = %q, want no body below the free-space watermark", responseBody)
	}
	foundMinFree := false
	for _, event := range parsed.Events {
		if event.Type == "recording.response_body_truncated" && event.Attributes["reason"] == "min_free_space" {
			foundMinFree = true
		}
	}
	if !foundMinFree {
		t.Fatalf("min-free-space truncation event not found: %+v", parsed.Events)
	}
}
