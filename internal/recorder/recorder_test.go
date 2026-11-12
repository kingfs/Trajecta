package recorder

import (
	"bytes"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	responsesaudit "github.com/kingfs/Trajecta/internal/responses/audit"
	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/recordfile"
)

func TestPrepareLogFileUsesAdapterModelExtraction(t *testing.T) {
	dir := t.TempDir()
	rec := New(dir, true, nil)

	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", bytes.NewBufferString(`{"model":"gpt-5","input":"hello"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer secret")

	info, err := rec.PrepareLogFile(req, "https://api.openai.com")
	if err != nil {
		t.Fatalf("PrepareLogFile() error = %v", err)
	}
	defer info.File.Close()

	if info.Header.Meta.Model != "gpt-5" {
		t.Fatalf("model = %q, want gpt-5", info.Header.Meta.Model)
	}
	if info.Header.Meta.Operation != "responses" {
		t.Fatalf("operation = %q, want responses", info.Header.Meta.Operation)
	}
}

func TestPrepareLogFilePersistsResponsesAuditCorrelation(t *testing.T) {
	dir := t.TempDir()
	rec := New(dir, false, nil)

	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", bytes.NewBufferString(`{"model":"gpt-5","input":"hello"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req = req.WithContext(responsesaudit.ContextWithRequestAuditID(req.Context(), "reqaudit-recorder-1"))
	req.Header.Set("X-Client-Request-Id", "client-recorder-1")

	info, err := rec.PrepareLogFile(req, "https://api.openai.com")
	if err != nil {
		t.Fatalf("PrepareLogFile() error = %v", err)
	}
	defer info.File.Close()

	if info.Header.Meta.RequestAuditID != "reqaudit-recorder-1" {
		t.Fatalf("RequestAuditID = %q, want reqaudit-recorder-1", info.Header.Meta.RequestAuditID)
	}
	if info.Header.Meta.ClientRequestID != "client-recorder-1" {
		t.Fatalf("ClientRequestID = %q, want client-recorder-1", info.Header.Meta.ClientRequestID)
	}
}

func TestPrepareLogFilePersistsExchangeMetadata(t *testing.T) {
	dir := t.TempDir()
	rec := New(dir, false, nil)

	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", bytes.NewBufferString(`{"model":"gpt-5","input":"hello"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}

	info, err := rec.PrepareLogFileWithOptions(req, PrepareOptions{
		SiteURL:          "https://api.openai.com",
		ExchangeID:       "exchange-model-1",
		ExchangeKind:     "model",
		ExchangeRole:     "main_model_call",
		ParentExchangeID: "exchange-entry-1",
		SequenceIndex:    3,
		TraceID:          "trace-model-1",
	})
	if err != nil {
		t.Fatalf("PrepareLogFileWithOptions() error = %v", err)
	}
	defer info.File.Close()

	meta := info.Header.Meta
	if meta.ExchangeID != "exchange-model-1" || meta.ExchangeKind != "model" || meta.ExchangeRole != "main_model_call" || meta.ParentExchangeID != "exchange-entry-1" || meta.SequenceIndex != 3 || meta.TraceID != "trace-model-1" {
		t.Fatalf("exchange metadata = %+v, want configured values", meta)
	}
}

func TestUpdateLogFilePersistsPipelineEvents(t *testing.T) {
	dir := t.TempDir()
	rec := New(dir, false, nil)

	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", bytes.NewBufferString(`{"model":"gpt-5","input":"hello"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	info, err := rec.PrepareLogFile(req, "https://api.openai.com")
	if err != nil {
		t.Fatalf("PrepareLogFile() error = %v", err)
	}

	info.Header.Meta.StatusCode = 200
	info.Header.Meta.DurationMs = 42
	info.Header.Meta.TTFTMs = 10
	info.Header.Layout.ResHeaderLen = int64(len("HTTP/1.1 200 OK\r\n\r\n"))
	info.Header.Layout.ResBodyLen = int64(len(`{"ok":true}`))
	info.Events = []RecordEvent{
		{
			Type: "llm.usage",
			Time: time.Date(2026, 3, 31, 12, 0, 1, 0, time.UTC),
			Attributes: map[string]interface{}{
				"total_tokens": 18,
			},
		},
	}
	if _, err := info.File.Write([]byte("\nHTTP/1.1 200 OK\r\n\r\n{\"ok\":true}")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	if err := rec.UpdateLogFile(info); err != nil {
		t.Fatalf("UpdateLogFile() error = %v", err)
	}

	content, err := os.ReadFile(info.Path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	parsed, err := recordfile.ParsePrelude(content)
	if err != nil {
		t.Fatalf("ParsePrelude() error = %v", err)
	}
	found := false
	for _, event := range parsed.Events {
		if event.Type == "llm.usage" {
			found = true
			if event.Attributes["total_tokens"] != float64(18) {
				t.Fatalf("event total_tokens = %#v, want 18", event.Attributes["total_tokens"])
			}
		}
	}
	if !found {
		t.Fatalf("llm.usage event not found: %+v", parsed.Events)
	}
	if !strings.Contains(string(content), "# event:") {
		t.Fatalf("recorded file missing event lines")
	}
}

func TestUpdateLogFileIndexesGroupingFromCodexHeaders(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	rec := New(dir, false, st)

	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", bytes.NewBufferString(`{"model":"gpt-5.4","input":"hello"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Session_id", "sess-codex-live")
	req.Header.Set("X-Client-Request-Id", "req-codex-live")
	req.Header.Set("X-Codex-Window-Id", "sess-codex-live:3")

	info, err := rec.PrepareLogFile(req, "https://api.openai.com")
	if err != nil {
		t.Fatalf("PrepareLogFile() error = %v", err)
	}

	info.Header.Meta.StatusCode = 200
	info.Header.Meta.DurationMs = 42
	info.Header.Meta.TTFTMs = 10
	info.Header.Layout.ResHeaderLen = int64(len("HTTP/1.1 200 OK\r\n\r\n"))
	info.Header.Layout.ResBodyLen = int64(len(`{"ok":true}`))
	if _, err := info.File.Write([]byte("\nHTTP/1.1 200 OK\r\n\r\n{\"ok\":true}")); err != nil {
		t.Fatalf("Write() error = %v", err)
	}

	if err := rec.UpdateLogFile(info); err != nil {
		t.Fatalf("UpdateLogFile() error = %v", err)
	}

	entry, err := st.GetByRequestID(info.Header.Meta.RequestID)
	if err != nil {
		t.Fatalf("GetByRequestID() error = %v", err)
	}
	if entry.SessionID != "sess-codex-live" {
		t.Fatalf("SessionID = %q, want sess-codex-live", entry.SessionID)
	}
	if entry.SessionSource != "header.session_id" {
		t.Fatalf("SessionSource = %q, want header.session_id", entry.SessionSource)
	}
	if entry.WindowID != "sess-codex-live:3" {
		t.Fatalf("WindowID = %q, want sess-codex-live:3", entry.WindowID)
	}
	if entry.ClientRequestID != "req-codex-live" {
		t.Fatalf("ClientRequestID = %q, want req-codex-live", entry.ClientRequestID)
	}
}

func TestUpdateLogFileEnqueuesParseJob(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	rec := New(dir, false, st)
	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", bytes.NewBufferString(`{"model":"gpt-5.1","input":"hello"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	info, err := rec.PrepareLogFile(req, "https://api.openai.com")
	if err != nil {
		t.Fatalf("PrepareLogFile() error = %v", err)
	}
	resHead := "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n"
	resBody := `{"id":"resp_1","object":"response","created_at":1741476777,"status":"completed","model":"gpt-5.1","output":[]}`
	info.Header.Meta.StatusCode = 200
	info.Header.Layout.ResHeaderLen = int64(len(resHead))
	info.Header.Layout.ResBodyLen = int64(len(resBody))
	if _, err := info.File.Write([]byte("\n" + resHead + resBody)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := rec.UpdateLogFile(info); err != nil {
		t.Fatalf("UpdateLogFile() error = %v", err)
	}
	entry, err := st.GetByRequestID(info.Header.Meta.RequestID)
	if err != nil {
		t.Fatalf("GetByRequestID() error = %v", err)
	}
	jobs, err := st.ListParseJobs("queued", 10)
	if err != nil {
		t.Fatalf("ListParseJobs() error = %v", err)
	}
	if len(jobs) != 1 || jobs[0].TraceID != entry.ID {
		t.Fatalf("jobs = %+v, want trace %s", jobs, entry.ID)
	}
}

// TestPrepareLogFileKeepsTheCassettePathInsideTheTraceRoot pins that a
// client-supplied model name cannot steer where the cassette is written.
//
// The model name is read from the request body and was joined into the trace
// path raw, so `../../../../tmp/escape` escaped the trace root through
// filepath.Join's cleaning and a NUL byte made os.MkdirAll fail with `invalid
// argument`, which lost the trace entirely: no cassette, no index row, only an
// ERROR line.
func TestPrepareLogFileKeepsTheCassettePathInsideTheTraceRoot(t *testing.T) {
	cases := []struct {
		name    string
		model   string
		wantSub string
	}{
		{name: "a plain model", model: "deepseek-flash", wantSub: "/deepseek-flash/"},
		{name: "a nested model slug keeps its nesting", model: "qwen/qwen3.6-35b-a3b", wantSub: "/qwen/qwen3.6-35b-a3b/"},
		{name: "a traversal attempt is flattened", model: "../../../../tmp/escape", wantSub: "/tmp/escape/"},
		{name: "a bare parent is dropped", model: "..", wantSub: "/unknown-model/"},
		{name: "an empty model", model: "", wantSub: "/unknown-model/"},
		{name: "a dot component is dropped", model: "./model", wantSub: "/model/"},
		{name: "a backslash is not a separator", model: "..\\..\\escape", wantSub: "/.._.._escape/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			rec := New(dir, true, nil)
			body := "{" + `"model":` + strconv.Quote(tc.model) + `,"messages":[]}`
			req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/chat/completions", bytes.NewBufferString(body))
			if err != nil {
				t.Fatalf("http.NewRequest() error = %v", err)
			}
			req.Header.Set("Content-Type", "application/json")

			info, err := rec.PrepareLogFile(req, "https://api.openai.com")
			if err != nil {
				t.Fatalf("PrepareLogFile(%q) error = %v", tc.model, err)
			}
			defer info.File.Close()

			root, err := filepath.EvalSymlinks(dir)
			if err != nil {
				t.Fatalf("EvalSymlinks(%q) error = %v", dir, err)
			}
			resolved, err := filepath.EvalSymlinks(filepath.Dir(info.Path))
			if err != nil {
				t.Fatalf("EvalSymlinks(%q) error = %v", filepath.Dir(info.Path), err)
			}
			if resolved != root && !strings.HasPrefix(resolved, root+string(filepath.Separator)) {
				t.Fatalf("cassette dir %q is outside the trace root %q", resolved, root)
			}
			if !strings.Contains(info.Path, filepath.FromSlash(tc.wantSub)) {
				t.Fatalf("cassette path %q does not contain %q", info.Path, tc.wantSub)
			}
			// The recorded metadata keeps what the client asked for.
			if info.Header.Meta.Model != tc.model && tc.model != "" {
				t.Fatalf("meta model = %q, want %q", info.Header.Meta.Model, tc.model)
			}
		})
	}
}

// TestPrepareLogFileAcceptsAModelWithANULByte pins that the trace is recorded
// rather than lost when the model name carries a byte the filesystem rejects.
func TestPrepareLogFileAcceptsAModelWithANULByte(t *testing.T) {
	dir := t.TempDir()
	rec := New(dir, true, nil)
	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/chat/completions", bytes.NewBufferString("{\"model\":\"nulprobe\\u0000trajecta\",\"messages\":[]}"))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	info, err := rec.PrepareLogFile(req, "https://api.openai.com")
	if err != nil {
		t.Fatalf("PrepareLogFile(NUL model) error = %v, want the trace to be recorded", err)
	}
	defer info.File.Close()

	if strings.ContainsRune(info.Path, 0) {
		t.Fatalf("cassette path %q contains a NUL byte", info.Path)
	}
	if !strings.Contains(info.Path, "nulprobetrajecta") {
		t.Fatalf("cassette path %q does not carry the model name", info.Path)
	}
}

// TestTracePathSegmentBoundsAnOverLongModelName pins that a model name long
// enough to exceed the filesystem limit cannot fail the mkdir.
func TestTracePathSegmentBoundsAnOverLongModelName(t *testing.T) {
	long := strings.Repeat("m", 5000)
	got := tracePathSegment(long, 200)
	if len(got) > 200 {
		t.Fatalf("len(tracePathSegment(long)) = %d, want at most 200", len(got))
	}
	if got == "" {
		t.Fatal("tracePathSegment(long) = empty, want a usable name")
	}
	if got := tracePathSegment(strings.Repeat("\x00", 8), 200); got != "unknown-model" {
		t.Fatalf("tracePathSegment(control only) = %q, want unknown-model", got)
	}
}

// TestUpdateLogFileStreamsTheRecordInsteadOfBufferingIt pins both halves of the
// finalisation contract.
//
// A cassette is written record-first, so finalising it means prepending the prelude
// and rewriting the file. That was done by reading the whole recording into memory
// - and copying it again when a store was attached - which made the transient
// allocation of one recorded exchange grow with the size of its response body: a
// 16 MiB recording cost 32 MiB on its own and 80 MiB with a store. A recording is
// as large as the upstream response, so that is a per-request memory spike the proxy
// cannot bound. The record is now streamed through a temporary file, and this test
// fails if finalisation starts allocating with the recording again.
//
// It also asserts the bytes survive: the finalised file must be the prelude
// followed by exactly the record that was written, byte for byte.
func TestUpdateLogFileStreamsTheRecordInsteadOfBufferingIt(t *testing.T) {
	const recordSize = 8 << 20

	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	rec := New(dir, false, st)

	requestBody := []byte(`{"model":"gpt-5.4","input":"hello"}`)
	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", bytes.NewReader(requestBody))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Session_id", "sess-stream")
	req.Header.Set("X-Codex-Window-Id", "sess-stream:1")

	info, err := rec.PrepareLogFile(req, "https://api.openai.com")
	if err != nil {
		t.Fatalf("PrepareLogFile() error = %v", err)
	}

	// A response body large enough that buffering it shows up clearly against the
	// bound below.
	responseBody := bytes.Repeat([]byte("r"), recordSize)
	responseHead := []byte("HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n")
	response := append([]byte("\n"), responseHead...)
	response = append(response, responseBody...)
	if _, err := info.File.Write(response); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	info.Header.Meta.StatusCode = 200
	info.Header.Layout.ResHeaderLen = int64(len(responseHead))
	info.Header.Layout.ResBodyLen = int64(len(responseBody))

	// The cassette as it stands before finalisation: request record first, no
	// prelude. Finalisation may only prepend to this.
	recordBefore, err := os.ReadFile(info.Path)
	if err != nil {
		t.Fatalf("read cassette before finalisation: %v", err)
	}

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	if err := rec.UpdateLogFile(info); err != nil {
		t.Fatalf("UpdateLogFile() error = %v", err)
	}
	runtime.ReadMemStats(&after)

	// The old implementation allocated between four and five times the record size
	// here. The bound is far enough above a copy buffer to be stable and far enough
	// below the record to catch a return to buffering.
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > recordSize/4 {
		t.Fatalf("UpdateLogFile allocated %.1f MiB for a %d MiB record, want it to stream the record rather than buffer it",
			float64(allocated)/(1<<20), int64(recordSize)>>20)
	}

	finalised, err := os.ReadFile(info.Path)
	if err != nil {
		t.Fatalf("read finalised cassette: %v", err)
	}
	parsed, err := recordfile.ParsePrelude(finalised)
	if err != nil {
		t.Fatalf("ParsePrelude() error = %v", err)
	}
	if parsed.PayloadOffset <= 0 || parsed.PayloadOffset >= int64(len(finalised)) {
		t.Fatalf("PayloadOffset = %d, want a prelude before the record", parsed.PayloadOffset)
	}
	gotRecord := finalised[parsed.PayloadOffset:]
	if !bytes.Equal(gotRecord, recordBefore) {
		t.Fatalf("finalisation changed the record: got %d bytes, want the %d that were there before (first difference at %d)",
			len(gotRecord), len(recordBefore), firstDifference(gotRecord, recordBefore))
	}

	entry, err := st.GetByRequestID(info.Header.Meta.RequestID)
	if err != nil {
		t.Fatalf("GetByRequestID() error = %v", err)
	}
	if entry.SessionID != "sess-stream" {
		t.Fatalf("SessionID = %q, want sess-stream; grouping must survive the streaming rewrite", entry.SessionID)
	}
}

func firstDifference(a, b []byte) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return min(len(a), len(b))
}

// TestPrepareLogFileMasksCredentialHeadersWithoutMutatingTheRequest pins the security
// property SECURITY.md, README.md and README_EN.md promise: with masking on, the credential
// headers are replaced with placeholders in the recorded request while the request that
// continues to the upstream keeps the real values (the recorder restores them after the dump).
//
// Nothing tested this before, which is how `debug.mask_key` could default to false in
// config.Load for as long as it did while three documents described the opposite.
func TestPrepareLogFileMasksCredentialHeadersWithoutMutatingTheRequest(t *testing.T) {
	dir := t.TempDir()
	rec := New(dir, true, nil)

	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/chat/completions", bytes.NewBufferString(`{"model":"gpt-5"}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	secrets := map[string]string{
		"Authorization":  "Bearer sk-live-secret",
		"api-key":        "live-api-key-secret",
		"x-api-key":      "live-x-api-key-secret",
		"x-goog-api-key": "live-goog-api-key-secret",
	}
	for name, value := range secrets {
		req.Header.Set(name, value)
	}
	// A header that names no credential must survive: over-redacting the whole request would
	// make the recording useless for diagnosing what the client actually sent.
	req.Header.Set("X-Keep-Me", "keep-me-visible")

	info, err := rec.PrepareLogFile(req, "https://api.openai.com")
	if err != nil {
		t.Fatalf("PrepareLogFile() error = %v", err)
	}
	if err := info.File.Close(); err != nil {
		t.Fatalf("close cassette error = %v", err)
	}
	cassette, err := os.ReadFile(info.Path)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", info.Path, err)
	}
	recorded := string(cassette)
	if !strings.Contains(recorded, "Bearer fake-key-logging") || !strings.Contains(recorded, "fake-key-logging") {
		t.Fatalf("cassette does not carry the masking placeholder:\n%s", recorded)
	}
	for name, value := range secrets {
		if strings.Contains(recorded, value) {
			t.Fatalf("%s was written to the cassette verbatim (%q); the recording is on disk", name, value)
		}
	}
	if !strings.Contains(recorded, "keep-me-visible") {
		t.Fatalf("a non-credential header was dropped from the recording:\n%s", recorded)
	}
	// The forwarded request still carries the real credentials.
	for name, value := range secrets {
		if got := req.Header.Get(name); got != value {
			t.Fatalf("live request header %s = %q, want %q: masking must not rewrite the outgoing request", name, got, value)
		}
	}
}

func TestPrepareLogFileRecordsHarnessDetection(t *testing.T) {
	dir := t.TempDir()
	rec := New(dir, false, nil)

	body := `{"model":"claude-sonnet-4-5","max_tokens":1024,"stream":true,"system":"You are Claude Code, Anthropic's official CLI for Claude.","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"Bash"}]}`
	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/messages", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("User-Agent", "claude-cli/2.0.14 (external, cli)")
	req.Header.Set("x-app", "cli")
	req.Header.Set("anthropic-beta", "claude-code-20250219,oauth-2025-04-20")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("x-stainless-lang", "js")

	info, err := rec.PrepareLogFile(req, "https://api.anthropic.com")
	if err != nil {
		t.Fatalf("PrepareLogFile() error = %v", err)
	}
	defer info.File.Close()

	meta := info.Header.Meta
	if meta.ClientHarness != "claude-code" || meta.ClientHarnessName != "Claude Code" || meta.ClientHarnessVersion != "2.0.14" || meta.ClientHarnessKind != "agent-cli" || meta.ClientHarnessConfidence != "high" {
		t.Fatalf("harness metadata = %+v, want claude-code/Claude Code/2.0.14/agent-cli/high", meta)
	}
	if len(meta.ClientHarnessTags) == 0 {
		t.Fatalf("harness tags are empty")
	}
	found := false
	for _, event := range info.Events {
		if event.Type != "harness.detected" {
			continue
		}
		found = true
		if event.Attributes["harness"] != "claude-code" {
			t.Fatalf("event harness = %#v, want claude-code", event.Attributes["harness"])
		}
		if _, ok := event.Attributes["evidence"]; !ok {
			t.Fatalf("event evidence missing: %#v", event.Attributes)
		}
	}
	if !found {
		t.Fatalf("harness.detected event not recorded: %+v", info.Events)
	}
}

func TestPrepareLogFileSkipsHarnessDetectionForModelChild(t *testing.T) {
	dir := t.TempDir()
	rec := New(dir, false, nil)

	// A child model exchange of the local Responses runtime has synthetic headers
	// and a converted body; classifying it would attribute the client's harness
	// to the internal upstream call.
	body := `{"model":"gpt-5","messages":[{"role":"system","content":"You are Claude Code, Anthropic's official CLI for Claude."}]}`
	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/chat/completions", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("User-Agent", "claude-cli/2.0.14 (external, cli)")

	info, err := rec.PrepareLogFileWithOptions(req, PrepareOptions{
		SiteURL:      "https://api.openai.com",
		ExchangeKind: "model",
	})
	if err != nil {
		t.Fatalf("PrepareLogFileWithOptions() error = %v", err)
	}
	defer info.File.Close()

	if info.Header.Meta.ClientHarness != "" || len(info.Header.Meta.ClientHarnessTags) != 0 {
		t.Fatalf("model child was classified: %+v", info.Header.Meta)
	}
	if len(info.Events) != 0 {
		t.Fatalf("model child recorded events: %+v", info.Events)
	}
}

func TestPrepareLogFileRecordsSDKTagsWithoutHarness(t *testing.T) {
	dir := t.TempDir()
	rec := New(dir, false, nil)

	body := `{"model":"gpt-4.1","stream":true,"messages":[],"tools":[{"type":"function","function":{"name":"lookup"}}]}`
	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/chat/completions", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("User-Agent", "OpenAI/Python 1.40.0")
	req.Header.Set("x-stainless-lang", "python")

	info, err := rec.PrepareLogFile(req, "https://api.openai.com")
	if err != nil {
		t.Fatalf("PrepareLogFile() error = %v", err)
	}
	defer info.File.Close()

	if info.Header.Meta.ClientHarness != "" {
		t.Fatalf("ClientHarness = %q, want empty for a plain SDK call", info.Header.Meta.ClientHarness)
	}
	if len(info.Header.Meta.ClientHarnessTags) == 0 {
		t.Fatalf("SDK/capability tags were dropped")
	}
	if len(info.Events) != 0 {
		t.Fatalf("tag-only detection must not write a harness event: %+v", info.Events)
	}
}

func TestUpdateLogFilePersistsHarnessEvent(t *testing.T) {
	dir := t.TempDir()
	rec := New(dir, false, nil)

	body := `{"model":"claude-sonnet-4-5","system":"You are Claude Code, Anthropic's official CLI for Claude.","messages":[]}`
	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/messages", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("User-Agent", "claude-cli/2.0.14 (external, cli)")
	req.Header.Set("anthropic-version", "2023-06-01")

	info, err := rec.PrepareLogFile(req, "https://api.anthropic.com")
	if err != nil {
		t.Fatalf("PrepareLogFile() error = %v", err)
	}
	info.Header.Meta.StatusCode = 200
	resHead := "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n"
	resBody := `{"ok":true}`
	info.Header.Layout.ResHeaderLen = int64(len(resHead))
	info.Header.Layout.ResBodyLen = int64(len(resBody))
	if _, err := info.File.Write([]byte("\n" + resHead + resBody)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := rec.UpdateLogFile(info); err != nil {
		t.Fatalf("UpdateLogFile() error = %v", err)
	}

	content, err := os.ReadFile(info.Path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	parsed, err := recordfile.ParsePrelude(content)
	if err != nil {
		t.Fatalf("ParsePrelude() error = %v", err)
	}
	if parsed.Header.Meta.ClientHarness != "claude-code" {
		t.Fatalf("persisted ClientHarness = %q, want claude-code", parsed.Header.Meta.ClientHarness)
	}
	found := false
	for _, event := range parsed.Events {
		if event.Type == "harness.detected" {
			found = true
			if event.Attributes["harness"] != "claude-code" {
				t.Fatalf("event harness = %#v", event.Attributes["harness"])
			}
		}
	}
	if !found {
		t.Fatalf("harness.detected event not persisted: %+v", parsed.Events)
	}
}

// TestUpdateLogFileIndexesClientHarness wires the recorder, the cassette and the
// store together: the detection written into the prelude is what the index row
// carries, and the list filter selects the row from the database. The pieces are
// unit-tested separately; this pins the hand-off between them.
func TestUpdateLogFileIndexesClientHarness(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	rec := New(dir, false, st)
	body := `{"model":"claude-sonnet-4-5","max_tokens":1024,"stream":true,"system":"You are Claude Code, Anthropic's official CLI for Claude.","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"Bash"}]}`
	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/messages", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "claude-cli/2.0.14 (external, cli)")
	req.Header.Set("x-app", "cli")
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("x-stainless-lang", "js")

	info, err := rec.PrepareLogFile(req, "https://api.anthropic.com")
	if err != nil {
		t.Fatalf("PrepareLogFile() error = %v", err)
	}
	info.Header.Meta.StatusCode = 200
	info.Header.Meta.DurationMs = 12
	resHead := "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n"
	resBody := `{"ok":true}`
	info.Header.Layout.ResHeaderLen = int64(len(resHead))
	info.Header.Layout.ResBodyLen = int64(len(resBody))
	if _, err := info.File.Write([]byte("\n" + resHead + resBody)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if err := rec.UpdateLogFile(info); err != nil {
		t.Fatalf("UpdateLogFile() error = %v", err)
	}

	entries, err := st.ListPage(1, 10, store.ListFilter{Harness: "claude-code"})
	if err != nil {
		t.Fatalf("ListPage(harness) error = %v", err)
	}
	if len(entries.Items) != 1 {
		t.Fatalf("harness-filtered items = %d, want 1", len(entries.Items))
	}
	meta := entries.Items[0].Header.Meta
	if meta.ClientHarness != "claude-code" || meta.ClientHarnessKind != "agent-cli" || meta.ClientHarnessVersion != "2.0.14" {
		t.Fatalf("indexed harness = %+v, want claude-code/agent-cli/2.0.14", meta)
	}
	if len(meta.ClientHarnessTags) == 0 {
		t.Fatalf("indexed harness tags are empty")
	}

	if other, err := st.ListPage(1, 10, store.ListFilter{Harness: "codex-cli"}); err != nil {
		t.Fatalf("ListPage(other harness) error = %v", err)
	} else if len(other.Items) != 0 {
		t.Fatalf("other-harness items = %d, want 0", len(other.Items))
	}
}

// TestPrepareLogFileHarnessDetectionIgnoresBody pins the recorder half of the
// head-only contract: the classification written into the cassette meta does not
// depend on the request body at all, so an empty and an oversized body with a
// spoofing prompt produce the same harness metadata.
func TestPrepareLogFileHarnessDetectionIgnoresBody(t *testing.T) {
	classify := func(t *testing.T, body string) recordfile.MetaData {
		t.Helper()
		rec := New(t.TempDir(), false, nil)
		req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/messages", bytes.NewBufferString(body))
		if err != nil {
			t.Fatalf("http.NewRequest() error = %v", err)
		}
		req.Header.Set("User-Agent", "claude-cli/2.0.14 (external, cli)")
		req.Header.Set("anthropic-beta", "claude-code-20250219")
		info, err := rec.PrepareLogFile(req, "https://api.anthropic.com")
		if err != nil {
			t.Fatalf("PrepareLogFile() error = %v", err)
		}
		defer info.File.Close()
		return info.Header.Meta
	}

	empty := classify(t, "")
	huge := classify(t, `{"system":"You are Qwen Code, an interactive CLI tool."}`+strings.Repeat(" ", 2*1024*1024))
	if empty.ClientHarness != huge.ClientHarness ||
		empty.ClientHarnessName != huge.ClientHarnessName ||
		empty.ClientHarnessVersion != huge.ClientHarnessVersion ||
		empty.ClientHarnessKind != huge.ClientHarnessKind ||
		empty.ClientHarnessConfidence != huge.ClientHarnessConfidence ||
		!reflect.DeepEqual(empty.ClientHarnessTags, huge.ClientHarnessTags) {
		t.Fatalf("harness metadata depends on the body: empty=%+v huge=%+v", empty, huge)
	}
	if empty.ClientHarness != "claude-code" || empty.ClientHarnessConfidence != "high" {
		t.Fatalf("harness metadata = %+v, want claude-code/high", empty)
	}
}

// TestDetectClientHarnessExchangeKinds pins the exchange-kind gate: only the
// proxy-minted local Responses child (`model`) is skipped. Every client-visible
// exchange kind is classified, and a nil request is a no-op rather than a panic.
func TestDetectClientHarnessExchangeKinds(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/messages", bytes.NewBufferString(`{}`))
	if err != nil {
		t.Fatalf("http.NewRequest() error = %v", err)
	}
	req.Header.Set("User-Agent", "claude-cli/2.0.14")
	req.Header.Set("anthropic-version", "2023-06-01")

	for _, kind := range []string{"", "entry", "proxy"} {
		if got := detectClientHarness(req, kind); got.ID != "claude-code" {
			t.Fatalf("exchange kind %q detection = %+v, want claude-code", kind, got)
		}
	}
	if got := detectClientHarness(req, "model"); !got.Empty() {
		t.Fatalf("model child detection = %+v, want empty", got)
	}
	if got := detectClientHarness(nil, "entry"); !got.Empty() {
		t.Fatalf("nil request detection = %+v, want empty", got)
	}
}

// A caller that already knows the wire model must not make the recorder parse
// the body again: the recorded model is the caller's, even when the body names
// something else.
func TestPrepareLogFileUsesWireModelWithoutParsing(t *testing.T) {
	dir := t.TempDir()
	rec := New(dir, true, nil)

	body := []byte(`{"model":"body-model","messages":[{"role":"user","content":"hi"}]}`)
	req, err := http.NewRequest(http.MethodPost, "http://proxy.local/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	info, err := rec.PrepareLogFileWithOptionsAndBody(req, PrepareOptions{
		SiteURL:   "https://api.openai.com",
		WireModel: "wire-model",
	}, body)
	if err != nil {
		t.Fatalf("PrepareLogFileWithOptionsAndBody() error = %v", err)
	}
	defer info.File.Close()
	if info.Header.Meta.Model != "wire-model" {
		t.Fatalf("model = %q, want wire-model", info.Header.Meta.Model)
	}
}
