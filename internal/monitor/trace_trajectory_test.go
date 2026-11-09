package monitor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/internal/trajectory"
	"github.com/kingfs/Trajecta/pkg/recordfile"
)

func TestTraceTrajectoryExportsOneTrace(t *testing.T) {
	outputDir := t.TempDir()
	st, err := store.New(outputDir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	reqBody := `{"model":"gpt-5.1","input":"hello"}`
	resBody := `{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.1","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello"}]}],"usage":{"input_tokens":2,"output_tokens":3,"total_tokens":5}}`
	raw := buildRecordFixtureWithRequestHeaders(t, "/v1/responses", false, []string{"Session-Id: sess-trace"}, reqBody, resBody)
	path := filepath.Join(outputDir, "trace-trajectory.http")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	prelude, err := recordfile.ParsePrelude(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertLog(path, prelude.Header); err != nil {
		t.Fatal(err)
	}
	entry, err := st.GetByRequestID(prelude.Header.Meta.RequestID)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/traces/"+entry.ID+"/trajectory", nil)
	rr := httptest.NewRecorder()
	traceAPIHandler(st, nil).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if got := rr.Header().Get("Content-Type"); got != "application/x-ndjson; charset=utf-8" {
		t.Fatalf("content type = %q", got)
	}
	var result trajectory.Trajectory
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode trajectory: %v", err)
	}
	wantSession := entry.SessionID
	if wantSession == "" {
		wantSession = entry.ID
	}
	if result.SessionID != wantSession {
		t.Fatalf("session id = %q, want %q", result.SessionID, wantSession)
	}
	if len(result.Steps) == 0 {
		t.Fatalf("trajectory has no steps: %+v", result)
	}
}
