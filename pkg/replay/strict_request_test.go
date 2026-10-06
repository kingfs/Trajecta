package replay

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// writeStrictFixture records `POST /v1/responses?b=2&a=1` with a JSON body.
func writeStrictFixture(t *testing.T) string {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "strict.http")
	if err := os.WriteFile(path, buildStrictFixture(t, "POST /v1/responses?b=2&a=1", `{"input":"hello"}`), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func buildStrictFixture(t *testing.T, requestLine string, reqBody string) []byte {
	t.Helper()

	reqHeader := requestLine + " HTTP/1.1\r\nHost: example.com\r\nContent-Type: application/json\r\n\r\n"
	resBody := `{"output":"done"}`
	resHeader := "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n"
	header := recordfile.RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta: recordfile.MetaData{
			RequestID:     "req_strict",
			Time:          time.Date(2026, 4, 21, 8, 0, 0, 0, time.UTC),
			Model:         "gpt-5.1-codex",
			Method:        "POST",
			StatusCode:    200,
			ContentLength: int64(len(resBody)),
		},
		Layout: recordfile.LayoutInfo{
			ReqHeaderLen: int64(len(reqHeader)),
			ReqBodyLen:   int64(len(reqBody)),
			ResHeaderLen: int64(len(resHeader)),
			ResBodyLen:   int64(len(resBody)),
		},
	}
	prelude, err := recordfile.MarshalPrelude(header, recordfile.BuildEvents(header))
	if err != nil {
		t.Fatalf("MarshalPrelude() error = %v", err)
	}
	return append(prelude, []byte(reqHeader+reqBody+"\n"+resHeader+resBody)...)
}

func replayRequest(t *testing.T, tr *Transport, req *http.Request) error {
	t.Helper()

	resp, err := tr.RoundTrip(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Body.Close()
}

func TestTransportDefaultAcceptsAnyRequestForCompatibility(t *testing.T) {
	t.Parallel()

	tr := NewTransport(writeStrictFixture(t))
	req, err := http.NewRequest(http.MethodGet, "http://other.example/v1/other?x=1", strings.NewReader(`{"model":"other"}`))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	if err := replayRequest(t, tr, req); err != nil {
		t.Fatalf("RoundTrip() with the default matcher error = %v, want replay to stay permissive", err)
	}
}

func TestTransportStrictRequestRejectsMismatchedRequest(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		method  string
		url     string
		body    string
		wantSub string
	}{
		{name: "method", method: http.MethodGet, url: "http://localhost/v1/responses", body: `{"input":"hello"}`, wantSub: "method"},
		{name: "path", method: http.MethodPost, url: "http://localhost/v1/other", body: `{"input":"hello"}`, wantSub: "path"},
		{name: "query", method: http.MethodPost, url: "http://localhost/v1/responses?a=1&b=3", body: `{"input":"hello"}`, wantSub: "query"},
		{name: "body", method: http.MethodPost, url: "http://localhost/v1/responses", body: `{"input":"goodbye"}`, wantSub: "body"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			tr := NewTransport(writeStrictFixture(t))
			tr.StrictRequest = true
			req, err := http.NewRequest(tc.method, tc.url, strings.NewReader(tc.body))
			if err != nil {
				t.Fatalf("NewRequest() error = %v", err)
			}
			err = replayRequest(t, tr, req)
			if err == nil {
				t.Fatalf("RoundTrip() error = nil, want a strict request mismatch on %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("RoundTrip() error = %v, want it to mention %q", err, tc.wantSub)
			}
		})
	}
}

func TestTransportStrictRequestAcceptsEquivalentRequest(t *testing.T) {
	t.Parallel()

	tr := NewTransport(writeStrictFixture(t))
	tr.StrictRequest = true
	// Same method, path and JSON body; different host, different query order and
	// different JSON formatting must still replay.
	req, err := http.NewRequest(http.MethodPost, "http://127.0.0.1:18080/v1/responses?b=2&a=1", strings.NewReader("{\n  \"input\": \"hello\"\n}"))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip() error = %v, want the equivalent request to replay", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("StatusCode = %d, want 200", resp.StatusCode)
	}
	// The body must still be readable by the caller after the matcher consumed it.
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatalf("ReadAll(request body) error = %v", err)
	}
	if !strings.Contains(string(body), "hello") {
		t.Fatalf("request body = %q, want it restored after matching", body)
	}
}

func TestTransportVerifyRequestDoesNotReplay(t *testing.T) {
	t.Parallel()

	tr := NewTransport(writeStrictFixture(t))
	ok, err := http.NewRequest(http.MethodPost, "http://localhost/v1/responses?a=1&b=2", strings.NewReader(`{"input":"hello"}`))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	if err := tr.VerifyRequest(ok); err != nil {
		t.Fatalf("VerifyRequest(matching) error = %v", err)
	}

	bad, err := http.NewRequest(http.MethodPost, "http://localhost/v1/wrong?a=1&b=2", strings.NewReader(`{"input":"hello"}`))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	if err := tr.VerifyRequest(bad); err == nil {
		t.Fatalf("VerifyRequest(mismatching) error = nil, want a mismatch")
	}
}

func TestTransportCustomRequestMatcherOverridesBuiltIn(t *testing.T) {
	t.Parallel()

	tr := NewTransport(writeStrictFixture(t))
	var seen RequestSnapshot
	tr.RequestMatcher = func(_ RequestSnapshot, actual RequestSnapshot) error {
		seen = actual
		return nil
	}
	req, err := http.NewRequest(http.MethodDelete, "http://localhost/whatever", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	if err := replayRequest(t, tr, req); err != nil {
		t.Fatalf("RoundTrip() error = %v, want the custom matcher to allow the request", err)
	}
	if seen.Method != http.MethodDelete || seen.Path != "/whatever" {
		t.Fatalf("matcher saw %+v, want the actual DELETE /whatever", seen)
	}
}
