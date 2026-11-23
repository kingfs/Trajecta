package monitor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// writeStepsCassette writes one full-prelude cassette and returns its path. The
// indexer reads the prelude, so it has to carry the provider, operation and
// timing facts the way the recorder writes them.
func writeStepsCassette(t *testing.T, outputDir, name, sessionID string, at time.Time, traceID, reqBody, resBody string) {
	t.Helper()

	reqHead := "POST /v1/chat/completions HTTP/1.1\r\nHost: example.com\r\nSession-Id: " + sessionID + "\r\nContent-Type: application/json\r\n\r\n"
	resHead := "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n"
	header := recordfile.RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta: recordfile.MetaData{
			RequestID:  traceID,
			Time:       at,
			Model:      "gpt-test",
			Provider:   "openai_compatible",
			Operation:  "chat.completions",
			Endpoint:   "/v1/chat/completions",
			URL:        "/v1/chat/completions",
			Method:     http.MethodPost,
			StatusCode: http.StatusOK,
			DurationMs: 1500,
			TTFTMs:     300,
		},
		Layout: recordfile.LayoutInfo{
			ReqHeaderLen: int64(len(reqHead)),
			ReqBodyLen:   int64(len(reqBody)),
			ResHeaderLen: int64(len(resHead)),
			ResBodyLen:   int64(len(resBody)),
		},
		Usage: recordfile.UsageInfo{
			PromptTokens:     100,
			CompletionTokens: 10,
			TotalTokens:      110,
			PromptTokenDetails: &recordfile.PromptTokenDetails{
				CachedTokens: 80,
			},
		},
	}
	prelude, err := recordfile.MarshalPrelude(header, recordfile.BuildEvents(header))
	if err != nil {
		t.Fatalf("MarshalPrelude() error = %v", err)
	}
	content := string(prelude) + reqHead + reqBody + "\n" + resHead + resBody
	if err := os.WriteFile(filepath.Join(outputDir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", name, err)
	}
}

func TestTraceSessionStepsAPIHandlerReturnsDerivedTurns(t *testing.T) {
	outputDir := t.TempDir()
	base := time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)
	userTurn := `{"model":"gpt-test","messages":[{"role":"user","content":"hello"}]}`
	finalTurn := `{"id":"r1","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110}}`
	writeStepsCassette(t, outputDir, "session-steps-a.http", "sess-steps", base, "trace-a", userTurn, finalTurn)
	writeStepsCassette(t, outputDir, "session-steps-b.http", "sess-steps", base.Add(time.Minute), "trace-b", userTurn, finalTurn)

	st, err := newTestStore(outputDir)
	if err != nil {
		t.Fatalf("newTestStore() error = %v", err)
	}
	defer st.Close()
	syncStore(t, st)

	entries, err := st.ListTracesBySession("sess-steps")
	if err != nil {
		t.Fatalf("ListTracesBySession() error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(entries))
	}
	// The list is newest first; focusing the newest trace keeps both steps.
	focus := entries[0]
	req := httptest.NewRequest(http.MethodGet, "/api/traces/"+focus.ID+"/session-steps?limit=10", nil)
	rr := httptest.NewRecorder()
	handleTraceSessionSteps(rr, req, st, focus)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var view struct {
		SessionID string `json:"session_id"`
		Focus     string `json:"focus_trace_id"`
		Window    struct {
			Returned  int  `json:"returned"`
			FirstStep int  `json:"first_step"`
			LastStep  int  `json:"last_step"`
			HasBefore bool `json:"has_before"`
		} `json:"window"`
		Totals struct {
			Steps   int      `json:"steps"`
			Context int      `json:"context"`
			Output  int      `json:"output"`
			Cache   *float64 `json:"cache_hit"`
			Turns   *int     `json:"turns"`
		} `json:"totals"`
		Turns []struct {
			Steps []struct {
				TraceID string `json:"trace_id"`
				Content *struct {
					Answer string `json:"answer"`
				} `json:"content"`
			} `json:"steps"`
		} `json:"turns"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if view.SessionID != "sess-steps" {
		t.Fatalf("session_id = %q", view.SessionID)
	}
	if view.Focus != focus.ID {
		t.Fatalf("focus_trace_id = %q, want %q", view.Focus, focus.ID)
	}
	if view.Window.Returned != 2 || view.Window.FirstStep != 1 || view.Window.LastStep != 2 {
		t.Fatalf("window = %+v, want both steps", view.Window)
	}
	if view.Totals.Steps != 2 {
		t.Fatalf("totals.steps = %d, want the whole session", view.Totals.Steps)
	}
	if view.Totals.Output != 20 {
		t.Fatalf("totals.output = %d, want 20", view.Totals.Output)
	}
	if view.Totals.Turns == nil || *view.Totals.Turns != 1 {
		t.Fatalf("totals.turns = %v, want 1 for a fully covered session", view.Totals.Turns)
	}
	if len(view.Turns) != 1 || len(view.Turns[0].Steps) != 2 {
		t.Fatalf("turns = %+v, want one turn with two steps", view.Turns)
	}
	if view.Turns[0].Steps[0].Content == nil || view.Turns[0].Steps[0].Content.Answer != "hi" {
		t.Fatalf("step content = %+v, want the parsed answer", view.Turns[0].Steps[0].Content)
	}
	if view.Window.HasBefore {
		t.Fatal("has_before = true for a two-step session")
	}
}

func TestSessionStepsAPIHandlerPagesBackwards(t *testing.T) {
	outputDir := t.TempDir()
	base := time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)
	userTurn := `{"model":"gpt-test","messages":[{"role":"user","content":"hello"}]}`
	finalTurn := `{"id":"r1","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":1,"total_tokens":11}}`
	for index := 0; index < 3; index++ {
		writeStepsCassette(t, outputDir, "page-"+string(rune('a'+index))+".http", "sess-page",
			base.Add(time.Duration(index)*time.Minute), "trace-"+string(rune('a'+index)), userTurn, finalTurn)
	}
	st, err := newTestStore(outputDir)
	if err != nil {
		t.Fatalf("newTestStore() error = %v", err)
	}
	defer st.Close()
	syncStore(t, st)

	entries, err := st.ListTracesBySession("sess-page")
	if err != nil {
		t.Fatalf("ListTracesBySession() error = %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	// The list is newest first, so the middle entry is the second step.
	before := entries[1].ID
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/sess-page/steps?limit=1&before="+before, nil)
	rr := httptest.NewRecorder()
	handleSessionSteps(rr, req, st, "sess-page")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}
	var view struct {
		Window struct {
			Returned  int  `json:"returned"`
			FirstStep int  `json:"first_step"`
			LastStep  int  `json:"last_step"`
			HasAfter  bool `json:"has_after"`
		} `json:"window"`
		Totals struct {
			Steps int `json:"steps"`
		} `json:"totals"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &view); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if view.Window.Returned != 1 || view.Window.FirstStep != 1 || view.Window.LastStep != 1 {
		t.Fatalf("window = %+v, want only the first step", view.Window)
	}
	if !view.Window.HasAfter {
		t.Fatal("has_after = false, want newer steps to stay reachable")
	}
	if view.Totals.Steps != 3 {
		t.Fatalf("totals.steps = %d, want the whole session", view.Totals.Steps)
	}
}
