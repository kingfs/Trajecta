package replay

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// writeFixture records one exchange with the supplied raw response head and
// body so a test can pin an unusual wire shape.
func writeFixture(t *testing.T, name string, resHeader string, resBody []byte) string {
	t.Helper()

	reqHeader := "POST /v1/chat/completions HTTP/1.1\r\nHost: example.com\r\nContent-Type: application/json\r\n\r\n"
	reqBody := `{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`
	header := recordfile.RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta: recordfile.MetaData{
			RequestID:     "req_" + name,
			Time:          time.Date(2026, 5, 2, 9, 0, 0, 0, time.UTC),
			Model:         "gpt-5",
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

	dir := t.TempDir()
	path := filepath.Join(dir, name+".http")
	payload := append(prelude, []byte(reqHeader+reqBody+"\n"+resHeader)...)
	payload = append(payload, resBody...)
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	return path
}

func replayOnce(t *testing.T, tr *Transport) *http.Response {
	t.Helper()

	req, err := http.NewRequest(http.MethodPost, "http://example.com/v1/chat/completions", strings.NewReader(`{"model":"gpt-5"}`))
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	resp, err := tr.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip() error = %v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// TestReplayPreservesChunkedResponses pins a chunked upstream: the cassette
// stores the wire form, so the replayed response must carry the same framing
// and the identical decoded body.
func TestReplayPreservesChunkedResponses(t *testing.T) {
	t.Parallel()

	// `{"choices":[]}` split as 8 + 6 bytes.
	chunked := "8\r\n{\"choice\r\n6\r\ns\":[]}\r\n0\r\n\r\n"
	resHeader := "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nTransfer-Encoding: chunked\r\n\r\n"

	tr := NewTransport(writeFixture(t, "chunked", resHeader, []byte(chunked)))
	resp := replayOnce(t, tr)

	if got := resp.TransferEncoding; len(got) == 0 || got[0] != "chunked" {
		t.Fatalf("TransferEncoding = %v, want chunked", got)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	// http.ReadResponse decodes the chunk framing for the caller, so the body
	// must be the concatenated chunks.
	if string(got) != `{"choices":[]}` {
		t.Fatalf("decoded body = %q, want the concatenated chunks", got)
	}
}

// TestReplayPreservesEncodedResponses pins a gzip response. Replay must hand
// the consumer the recorded bytes and the Content-Encoding header, because the
// cassette is the raw wire form and the proxy must not silently decompress.
func TestReplayPreservesEncodedResponses(t *testing.T) {
	t.Parallel()

	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write([]byte(`{"choices":[{"index":0}]}`)); err != nil {
		t.Fatalf("gzip write error = %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("gzip close error = %v", err)
	}
	compressed := buf.Bytes()

	resHeader := "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Encoding: gzip\r\n\r\n"
	tr := NewTransport(writeFixture(t, "gzip", resHeader, compressed))
	resp := replayOnce(t, tr)

	if got := resp.Header.Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if !bytes.Equal(got, compressed) {
		t.Fatalf("body = %d bytes, want the %d recorded bytes unchanged", len(got), len(compressed))
	}
}

// TestReplayPreservesRedirectResponses pins a 3xx: the status, the Location
// header and the empty body must survive, because a replayed SDK that follows
// redirects would otherwise take a different path than the recording did.
func TestReplayPreservesRedirectResponses(t *testing.T) {
	t.Parallel()

	resHeader := "HTTP/1.1 307 Temporary Redirect\r\nLocation: /v1/chat/completions/retry\r\nContent-Length: 0\r\n\r\n"
	tr := NewTransport(writeFixture(t, "redirect", resHeader, nil))
	resp := replayOnce(t, tr)

	if resp.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("StatusCode = %d, want 307", resp.StatusCode)
	}
	if got := resp.Header.Get("Location"); got != "/v1/chat/completions/retry" {
		t.Fatalf("Location = %q", got)
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("body = %q, want empty", got)
	}
}

// TestReplayPreservesLargeBodies pins a 1 MiB response so a buffered or
// truncated read path cannot silently shorten a cassette.
func TestReplayPreservesLargeBodies(t *testing.T) {
	t.Parallel()

	large := bytes.Repeat([]byte("abcdefgh"), 128*1024) // 1 MiB
	body := append([]byte(`{"choices":[{"index":0,"message":{"role":"assistant","content":"`), large...)
	body = append(body, []byte(`"}}]}`)...)

	resHeader := "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: " + itoa(len(body)) + "\r\n\r\n"
	tr := NewTransport(writeFixture(t, "large", resHeader, body))
	resp := replayOnce(t, tr)

	if resp.ContentLength != int64(len(body)) {
		t.Fatalf("ContentLength = %d, want %d", resp.ContentLength, len(body))
	}
	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("body = %d bytes, want the %d recorded bytes unchanged", len(got), len(body))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
