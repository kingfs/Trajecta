package observeworker

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// A Responses request carrying the DeepSeek Harness runtime-context snapshot
// must end up attributed to the declared project, both on the trace and on the
// session rollup, with no extra step: parsing is what extracts it.
func TestWorkerPersistsProjectDeclaration(t *testing.T) {
	dir := t.TempDir()
	st, err := newTestStore(dir)
	if err != nil {
		t.Fatalf("newTestStore() error = %v", err)
	}
	defer st.Close()

	traceID := writeIndexedProjectTrace(t, st, dir)
	if err := st.EnqueueParseJob(traceID); err != nil {
		t.Fatalf("EnqueueParseJob() error = %v", err)
	}
	New(st, Options{BatchSize: 5}).RunOnce(context.Background())

	project, ok, err := st.GetTraceProject(traceID)
	if err != nil {
		t.Fatalf("GetTraceProject() error = %v", err)
	}
	if !ok {
		t.Fatal("trace has no project after parsing")
	}
	if project.Root != "/data/src/github.com/kingfs/Trajecta" || project.Source != "dsh_runtime_context" {
		t.Fatalf("trace project = %+v", project)
	}
	if project.SessionID != "sess-project" {
		t.Fatalf("trace project session = %q", project.SessionID)
	}

	rollup, ok, err := st.GetSessionProject("sess-project")
	if err != nil {
		t.Fatalf("GetSessionProject() error = %v", err)
	}
	if !ok {
		t.Fatal("session has no project rollup after parsing")
	}
	if rollup.Root != "/data/src/github.com/kingfs/Trajecta" || rollup.RootKey != "/data/src/github.com/kingfs/Trajecta" {
		t.Fatalf("session project = %+v", rollup)
	}
	if rollup.FirstTraceID != traceID || rollup.SwitchCount != 0 {
		t.Fatalf("session rollup first trace = %q switches = %d", rollup.FirstTraceID, rollup.SwitchCount)
	}
}

// A request that declares nothing must leave no project, and a reparse must not
// keep an old attribution alive.
func TestWorkerClearsProjectOnReparseWithoutDeclaration(t *testing.T) {
	dir := t.TempDir()
	st, err := newTestStore(dir)
	if err != nil {
		t.Fatalf("newTestStore() error = %v", err)
	}
	defer st.Close()

	traceID := writeIndexedProjectTrace(t, st, dir)
	if err := st.EnqueueParseJob(traceID); err != nil {
		t.Fatalf("EnqueueParseJob() error = %v", err)
	}
	New(st, Options{BatchSize: 5}).RunOnce(context.Background())

	obs, err := ReparseTrace(context.Background(), st, nil, traceID)
	if err != nil {
		t.Fatalf("ReparseTrace() error = %v", err)
	}
	// Simulate a request whose prompt no longer carries the snapshot.
	obs.Request.Messages = nil
	obs.Request.Inputs = nil
	obs.Request.Instructions = nil
	obs.Request.Nodes = nil
	if err := PersistTraceProject(st, traceID, obs); err != nil {
		t.Fatalf("PersistTraceProject() error = %v", err)
	}
	if _, ok, err := st.GetTraceProject(traceID); err != nil || ok {
		t.Fatalf("GetTraceProject() ok=%v err=%v, want cleared", ok, err)
	}
	if _, ok, err := st.GetSessionProject("sess-project"); err != nil || ok {
		t.Fatalf("GetSessionProject() ok=%v err=%v, want cleared", ok, err)
	}
}

func writeIndexedProjectTrace(t *testing.T, st *store.Store, dir string) string {
	t.Helper()
	reqHead := "POST /v1/responses HTTP/1.1\r\nHost: example.com\r\n\r\n"
	reqBody := `{"model":"gpt-5.1","input":[
		{"type":"message","role":"developer","content":[{"type":"input_text","text":"You are a coding agent."}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"<system-reminder>\nThe following information describes your current session:\n\nCurrent working directory: \"/data/src/github.com/kingfs/Trajecta\".\n</system-reminder>"}]},
		{"type":"message","role":"user","content":[{"type":"input_text","text":"fix the parse worker"}]}
	]}`
	resHead := "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n"
	resBody := `{"id":"resp_project","object":"response","created_at":1741476777,"status":"completed","model":"gpt-5.1","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
	header := recordfile.RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta: recordfile.MetaData{
			RequestID:     "req-project",
			Time:          time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC),
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
	logPath := filepath.Join(dir, "project-trace.http")
	if err := os.WriteFile(logPath, []byte(string(prelude)+reqHead+reqBody+"\n"+resHead+resBody), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := st.UpsertLogWithGrouping(logPath, header, store.GroupingInfo{SessionID: "sess-project", SessionSource: "test"}); err != nil {
		t.Fatalf("UpsertLogWithGrouping() error = %v", err)
	}
	entry, err := st.GetByRequestID("req-project")
	if err != nil {
		t.Fatalf("GetByRequestID() error = %v", err)
	}
	return entry.ID
}
