package observeworker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/observe"
	"github.com/kingfs/Trajecta/pkg/recordfile"
)

func TestWorkerRunOnceParsesQueuedJob(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	traceID := writeIndexedResponseTrace(t, st, dir)
	if err := st.EnqueueParseJob(traceID); err != nil {
		t.Fatalf("EnqueueParseJob() error = %v", err)
	}

	worker := New(st, Options{BatchSize: 5})
	worker.RunOnce(context.Background())

	summary, err := st.GetObservationSummary(traceID)
	if err != nil {
		t.Fatalf("GetObservationSummary() error = %v", err)
	}
	if summary.Parser != "openai" || summary.Status != "parsed" {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.ExchangeKind != "model" || summary.ExchangeRole != "primary_model_call" {
		t.Fatalf("exchange summary = %+v, want model primary_model_call fallback", summary)
	}
	nodes, err := st.ListSemanticNodes(traceID)
	if err != nil {
		t.Fatalf("ListSemanticNodes() error = %v", err)
	}
	if len(nodes) == 0 {
		t.Fatalf("semantic nodes empty")
	}
}

func TestWorkerRunOnceParsesEntryExchangeWithEntryObservation(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	traceID := writeIndexedEntryTrace(t, st, dir)
	if err := st.EnqueueParseJob(traceID); err != nil {
		t.Fatalf("EnqueueParseJob() error = %v", err)
	}

	worker := New(st, Options{BatchSize: 5})
	worker.RunOnce(context.Background())

	summary, err := st.GetObservationSummary(traceID)
	if err != nil {
		t.Fatalf("GetObservationSummary() error = %v", err)
	}
	if summary.Parser != "entry" || summary.Status != "parsed" {
		t.Fatalf("summary = %+v", summary)
	}
	if summary.ExchangeKind != "entry" || summary.ExchangeRole != "client_request" || summary.RequestAuditID != "audit-entry" || summary.ResponseID != "resp_entry_1" {
		t.Fatalf("exchange summary = %+v", summary)
	}
	obs, err := st.GetObservation(traceID)
	if err != nil {
		t.Fatalf("GetObservation() error = %v", err)
	}
	if obs.ExchangeKind != "entry" || obs.ResponseID != "resp_entry_1" {
		t.Fatalf("observation exchange fields = %+v", obs)
	}
	nodes, err := st.ListSemanticNodes(traceID)
	if err != nil {
		t.Fatalf("ListSemanticNodes() error = %v", err)
	}
	if len(nodes) == 0 {
		t.Fatalf("semantic nodes empty")
	}
}

func TestWorkerRunOnceRecordsPlainTextProxyErrorAsObservation(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	traceID := writeIndexedPlainTextProxyErrorTrace(t, st, dir)
	if err := st.EnqueueParseJob(traceID); err != nil {
		t.Fatalf("EnqueueParseJob() error = %v", err)
	}

	worker := New(st, Options{BatchSize: 5})
	worker.RunOnce(context.Background())

	jobs, err := st.ListParseJobs("failed", 10)
	if err != nil {
		t.Fatalf("ListParseJobs(failed) error = %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("failed jobs = %+v", jobs)
	}
	summary, err := st.GetObservationSummary(traceID)
	if err != nil {
		t.Fatalf("GetObservationSummary() error = %v", err)
	}
	if summary.Parser != "entry" || summary.Status != "parsed" {
		t.Fatalf("summary = %+v", summary)
	}
	obs, err := st.GetObservation(traceID)
	if err != nil {
		t.Fatalf("GetObservation() error = %v", err)
	}
	if len(obs.Warnings) != 1 || obs.Warnings[0].Code != "http_error_response" {
		t.Fatalf("warnings = %+v", obs.Warnings)
	}
	nodes, err := st.ListSemanticNodes(traceID)
	if err != nil {
		t.Fatalf("ListSemanticNodes() error = %v", err)
	}
	var foundError bool
	for _, node := range nodes {
		if node.Node.NormalizedType == observe.NodeError && node.Node.ProviderType == "http_error" {
			foundError = true
			if node.Node.Text == "" {
				t.Fatalf("http error node text empty: %+v", node.Node)
			}
		}
	}
	if !foundError {
		t.Fatalf("semantic error node missing: %+v", nodes)
	}
}

func TestWorkerRunOnceParsesAnthropicQueuedJob(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	traceID := writeIndexedAnthropicTrace(t, st, dir)
	if err := st.EnqueueParseJob(traceID); err != nil {
		t.Fatalf("EnqueueParseJob() error = %v", err)
	}

	worker := New(st, Options{BatchSize: 5})
	worker.RunOnce(context.Background())

	summary, err := st.GetObservationSummary(traceID)
	if err != nil {
		t.Fatalf("GetObservationSummary() error = %v", err)
	}
	if summary.Parser != "anthropic" || summary.Status != "parsed" {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestWorkerRunOnceParsesGeminiQueuedJob(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	traceID := writeIndexedGeminiTrace(t, st, dir)
	if err := st.EnqueueParseJob(traceID); err != nil {
		t.Fatalf("EnqueueParseJob() error = %v", err)
	}

	worker := New(st, Options{BatchSize: 5})
	worker.RunOnce(context.Background())

	summary, err := st.GetObservationSummary(traceID)
	if err != nil {
		t.Fatalf("GetObservationSummary() error = %v", err)
	}
	if summary.Parser != "gemini" || summary.Status != "parsed" {
		t.Fatalf("summary = %+v", summary)
	}
}

func writeIndexedPlainTextProxyErrorTrace(t *testing.T, st *store.Store, dir string) string {
	t.Helper()
	reqHead := "POST /v1/responses HTTP/1.1\r\nHost: local.trajecta\r\nContent-Type: application/json\r\n\r\n"
	reqBody := `{"model":"qwen3.6-35b-a3b","input":"hi"}`
	resHead := "HTTP/1.1 502 Bad Gateway\r\nContent-Type: text/plain; charset=utf-8\r\nX-Content-Type-Options: nosniff\r\n\r\n"
	resBody := `Proxy Error: upstream 3a4c1531-4540-4fa2-ae29-10ea554bbec3 returned status 404 for model "qwen3.6-35b-a3b"`
	header := recordfile.RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta: recordfile.MetaData{
			RequestID:    "req-worker-proxy-error",
			ExchangeKind: "entry",
			ExchangeRole: "client_request",
			Time:         time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC),
			Model:        "qwen3.6-35b-a3b",
			Provider:     "openai_compatible",
			Operation:    "responses",
			Endpoint:     "/v1/responses",
			URL:          "/v1/responses",
			Method:       "POST",
			StatusCode:   502,
			DurationMs:   20,
			ClientIP:     "127.0.0.1",
		},
		Layout: recordfile.LayoutInfo{
			ReqHeaderLen: int64(len(reqHead)),
			ReqBodyLen:   int64(len(reqBody)),
			ResHeaderLen: int64(len(resHead)),
			ResBodyLen:   int64(len(resBody)),
		},
	}
	prelude, err := recordfile.MarshalPrelude(header, recordfile.BuildEvents(header))
	if err != nil {
		t.Fatalf("MarshalPrelude() error = %v", err)
	}
	logPath := filepath.Join(dir, "worker-proxy-error-trace.http")
	if err := os.WriteFile(logPath, []byte(string(prelude)+reqHead+reqBody+"\n"+resHead+resBody), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := st.UpsertLog(logPath, header); err != nil {
		t.Fatalf("UpsertLog() error = %v", err)
	}
	entry, err := st.GetByRequestID(header.Meta.RequestID)
	if err != nil {
		t.Fatalf("GetByRequestID() error = %v", err)
	}
	return entry.ID
}

func writeIndexedResponseTrace(t *testing.T, st *store.Store, dir string) string {
	t.Helper()
	reqHead := "POST /v1/responses HTTP/1.1\r\nHost: example.com\r\n\r\n"
	reqBody := `{"model":"gpt-5.1","input":"hello"}`
	resHead := "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n"
	resBody := `{"id":"resp_1","object":"response","created_at":1741476777,"status":"completed","model":"gpt-5.1","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
	header := recordfile.RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta: recordfile.MetaData{
			RequestID:     "req-worker",
			Time:          time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC),
			Model:         "gpt-5.1",
			Provider:      "openai_compatible",
			Operation:     "responses",
			Endpoint:      "/v1/responses",
			URL:           "/v1/responses",
			Method:        "POST",
			StatusCode:    200,
			DurationMs:    20,
			TTFTMs:        5,
			ClientIP:      "127.0.0.1",
			ContentLength: int64(len(reqBody)),
		},
		Layout: recordfile.LayoutInfo{
			ReqHeaderLen: int64(len(reqHead)),
			ReqBodyLen:   int64(len(reqBody)),
			ResHeaderLen: int64(len(resHead)),
			ResBodyLen:   int64(len(resBody)),
		},
	}
	prelude, err := recordfile.MarshalPrelude(header, recordfile.BuildEvents(header))
	if err != nil {
		t.Fatalf("MarshalPrelude() error = %v", err)
	}
	logPath := filepath.Join(dir, "worker-trace.http")
	if err := os.WriteFile(logPath, []byte(string(prelude)+reqHead+reqBody+"\n"+resHead+resBody), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := st.UpsertLog(logPath, header); err != nil {
		t.Fatalf("UpsertLog() error = %v", err)
	}
	entry, err := st.GetByRequestID(header.Meta.RequestID)
	if err != nil {
		t.Fatalf("GetByRequestID() error = %v", err)
	}
	return entry.ID
}

func writeIndexedEntryTrace(t *testing.T, st *store.Store, dir string) string {
	t.Helper()
	reqHead := "POST /v1/responses HTTP/1.1\r\nHost: local.trajecta\r\n\r\n"
	reqBody := `{"model":"gpt-5.1","input":"hello"}`
	resHead := "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n"
	resBody := `{"id":"resp_entry_1","status":"completed","model":"gpt-5.1","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hi"}]}]}`
	header := recordfile.RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta: recordfile.MetaData{
			RequestID:      "req-worker-entry",
			RequestAuditID: "audit-entry",
			ResponseID:     "resp_entry_1",
			ExchangeID:     "exchange-entry-1",
			ExchangeKind:   "entry",
			ExchangeRole:   "client_request",
			SequenceIndex:  0,
			Time:           time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC),
			Model:          "gpt-5.1",
			Provider:       "openai_compatible",
			Operation:      "responses",
			Endpoint:       "/v1/responses",
			URL:            "/v1/responses",
			Method:         "POST",
			StatusCode:     200,
			DurationMs:     20,
			TTFTMs:         5,
			ClientIP:       "127.0.0.1",
			ContentLength:  int64(len(reqBody)),
		},
		Layout: recordfile.LayoutInfo{
			ReqHeaderLen: int64(len(reqHead)),
			ReqBodyLen:   int64(len(reqBody)),
			ResHeaderLen: int64(len(resHead)),
			ResBodyLen:   int64(len(resBody)),
		},
	}
	prelude, err := recordfile.MarshalPrelude(header, recordfile.BuildEvents(header))
	if err != nil {
		t.Fatalf("MarshalPrelude() error = %v", err)
	}
	logPath := filepath.Join(dir, "worker-entry-trace.http")
	if err := os.WriteFile(logPath, []byte(string(prelude)+reqHead+reqBody+"\n"+resHead+resBody), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := st.UpsertLog(logPath, header); err != nil {
		t.Fatalf("UpsertLog() error = %v", err)
	}
	entry, err := st.GetByRequestID(header.Meta.RequestID)
	if err != nil {
		t.Fatalf("GetByRequestID() error = %v", err)
	}
	return entry.ID
}

func writeIndexedGeminiTrace(t *testing.T, st *store.Store, dir string) string {
	t.Helper()
	reqHead := "POST /v1beta/models/gemini-2.5-flash:generateContent HTTP/1.1\r\nHost: generativelanguage.googleapis.com\r\n\r\n"
	reqBody := `{"contents":[{"role":"user","parts":[{"text":"hello"}]}]}`
	resHead := "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n"
	resBody := `{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":1,"candidatesTokenCount":1,"totalTokenCount":2}}`
	header := recordfile.RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta: recordfile.MetaData{
			RequestID:     "req-worker-gemini",
			Time:          time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC),
			Model:         "gemini-2.5-flash",
			Provider:      "google_genai",
			Operation:     "generate_content",
			Endpoint:      "/v1beta/models:generateContent",
			URL:           "/v1beta/models/gemini-2.5-flash:generateContent",
			Method:        "POST",
			StatusCode:    200,
			DurationMs:    20,
			TTFTMs:        5,
			ClientIP:      "127.0.0.1",
			ContentLength: int64(len(reqBody)),
		},
		Layout: recordfile.LayoutInfo{
			ReqHeaderLen: int64(len(reqHead)),
			ReqBodyLen:   int64(len(reqBody)),
			ResHeaderLen: int64(len(resHead)),
			ResBodyLen:   int64(len(resBody)),
		},
	}
	prelude, err := recordfile.MarshalPrelude(header, recordfile.BuildEvents(header))
	if err != nil {
		t.Fatalf("MarshalPrelude() error = %v", err)
	}
	logPath := filepath.Join(dir, "worker-gemini-trace.http")
	if err := os.WriteFile(logPath, []byte(string(prelude)+reqHead+reqBody+"\n"+resHead+resBody), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := st.UpsertLog(logPath, header); err != nil {
		t.Fatalf("UpsertLog() error = %v", err)
	}
	entry, err := st.GetByRequestID(header.Meta.RequestID)
	if err != nil {
		t.Fatalf("GetByRequestID() error = %v", err)
	}
	return entry.ID
}

func writeIndexedAnthropicTrace(t *testing.T, st *store.Store, dir string) string {
	t.Helper()
	reqHead := "POST /v1/messages HTTP/1.1\r\nHost: api.anthropic.com\r\n\r\n"
	reqBody := `{"model":"claude-sonnet-4-5","system":"You are helpful.","messages":[{"role":"user","content":"hello"}],"max_tokens":64}`
	resHead := "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n"
	resBody := `{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4-5","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":1,"output_tokens":1}}`
	header := recordfile.RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta: recordfile.MetaData{
			RequestID:     "req-worker-anthropic",
			Time:          time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC),
			Model:         "claude-sonnet-4-5",
			Provider:      "anthropic",
			Operation:     "messages",
			Endpoint:      "/v1/messages",
			URL:           "/v1/messages",
			Method:        "POST",
			StatusCode:    200,
			DurationMs:    20,
			TTFTMs:        5,
			ClientIP:      "127.0.0.1",
			ContentLength: int64(len(reqBody)),
		},
		Layout: recordfile.LayoutInfo{
			ReqHeaderLen: int64(len(reqHead)),
			ReqBodyLen:   int64(len(reqBody)),
			ResHeaderLen: int64(len(resHead)),
			ResBodyLen:   int64(len(resBody)),
		},
	}
	prelude, err := recordfile.MarshalPrelude(header, recordfile.BuildEvents(header))
	if err != nil {
		t.Fatalf("MarshalPrelude() error = %v", err)
	}
	logPath := filepath.Join(dir, "worker-anthropic-trace.http")
	if err := os.WriteFile(logPath, []byte(string(prelude)+reqHead+reqBody+"\n"+resHead+resBody), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := st.UpsertLog(logPath, header); err != nil {
		t.Fatalf("UpsertLog() error = %v", err)
	}
	entry, err := st.GetByRequestID(header.Meta.RequestID)
	if err != nil {
		t.Fatalf("GetByRequestID() error = %v", err)
	}
	return entry.ID
}

// writeIndexedEmbeddingsTrace records an /v1/embeddings exchange, an operation
// the parser set has no parser for.
func writeIndexedEmbeddingsTrace(t *testing.T, st *store.Store, dir string) string {
	t.Helper()
	reqHead := "POST /v1/embeddings HTTP/1.1\r\nHost: example.com\r\n\r\n"
	reqBody := `{"model":"text-embedding-3-small","input":"hello"}`
	resHead := "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n"
	resBody := `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2]}],"model":"text-embedding-3-small"}`
	header := recordfile.RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta: recordfile.MetaData{
			RequestID:     "req-embeddings",
			Time:          time.Date(2026, 5, 13, 10, 0, 0, 0, time.UTC),
			Model:         "text-embedding-3-small",
			Provider:      "openai_compatible",
			Operation:     "embeddings",
			Endpoint:      "/v1/embeddings",
			URL:           "/v1/embeddings",
			Method:        "POST",
			StatusCode:    200,
			DurationMs:    12,
			ClientIP:      "127.0.0.1",
			ContentLength: int64(len(reqBody)),
		},
		Layout: recordfile.LayoutInfo{
			ReqHeaderLen: int64(len(reqHead)),
			ReqBodyLen:   int64(len(reqBody)),
			ResHeaderLen: int64(len(resHead)),
			ResBodyLen:   int64(len(resBody)),
		},
	}
	prelude, err := recordfile.MarshalPrelude(header, recordfile.BuildEvents(header))
	if err != nil {
		t.Fatalf("MarshalPrelude() error = %v", err)
	}
	logPath := filepath.Join(dir, "embeddings-trace.http")
	if err := os.WriteFile(logPath, []byte(string(prelude)+reqHead+reqBody+"\n"+resHead+resBody), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := st.UpsertLog(logPath, header); err != nil {
		t.Fatalf("UpsertLog() error = %v", err)
	}
	entry, err := st.GetByRequestID(header.Meta.RequestID)
	if err != nil {
		t.Fatalf("GetByRequestID() error = %v", err)
	}
	return entry.ID
}

// TestWorkerRecordsExchangeWithoutAParserAsUnsupported pins that an operation
// the parser set does not cover is parsed into a successful, non-failed
// observation.
//
// /v1/embeddings has no parser, so it was recorded as a failed observation and a
// failed parse job. That state is permanent: the payload is not malformed, so
// every later reanalysis of the trace failed again, and a batch reanalysis over
// failed traces could never succeed while such a trace existed. Recorded as
// unsupported, the trace is visibly "not parsed" instead of broken, and
// reanalysis completes.
func TestWorkerRecordsExchangeWithoutAParserAsUnsupported(t *testing.T) {
	dir := t.TempDir()
	st, err := store.New(dir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	traceID := writeIndexedEmbeddingsTrace(t, st, dir)
	if err := st.EnqueueParseJob(traceID); err != nil {
		t.Fatalf("EnqueueParseJob() error = %v", err)
	}

	worker := New(st, Options{BatchSize: 5})
	worker.RunOnce(context.Background())

	failed, err := st.ListParseJobs("failed", 10)
	if err != nil {
		t.Fatalf("ListParseJobs(failed) error = %v", err)
	}
	if len(failed) != 0 {
		t.Fatalf("failed jobs = %+v, want none: an unsupported operation is not a parse failure", failed)
	}

	summary, err := st.GetObservationSummary(traceID)
	if err != nil {
		t.Fatalf("GetObservationSummary() error = %v", err)
	}
	if summary.Status != string(observe.ParseStatusUnsupported) {
		t.Fatalf("observation status = %q, want %q", summary.Status, observe.ParseStatusUnsupported)
	}

	obs, err := st.GetObservation(traceID)
	if err != nil {
		t.Fatalf("GetObservation() error = %v", err)
	}
	// The endpoint is not a column on trace_observations (it stays on the trace
	// row), so only the operation is asserted here.
	if obs.Operation != "embeddings" || obs.Provider != "openai_compatible" {
		t.Fatalf("observation identity = %+v, want the embeddings exchange", obs)
	}
	if len(obs.Warnings) != 1 || obs.Warnings[0].Code != "no_parser" {
		t.Fatalf("warnings = %+v, want one no_parser warning", obs.Warnings)
	}

	// Reanalysis must also succeed, which is what the failed status used to block.
	reparsed, err := ReparseTrace(context.Background(), st, nil, traceID)
	if err != nil {
		t.Fatalf("ReparseTrace() error = %v, want nil for an unsupported operation", err)
	}
	if reparsed.Status != observe.ParseStatusUnsupported {
		t.Fatalf("reparsed status = %q, want %q", reparsed.Status, observe.ParseStatusUnsupported)
	}
}
