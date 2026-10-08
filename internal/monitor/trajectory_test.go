package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/internal/trajectory"
	"github.com/kingfs/Trajecta/pkg/recordfile"
)

func TestSessionTrajectoryDownload(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trace.http")
	content := buildRecordFixtureWithRequestHeaders(t, "/v1/responses", false, []string{"Session-Id: export-session"}, `{"input":"hello"}`, `{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"world"}]}]}`)
	if err := os.WriteFile(path, content, 0600); err != nil {
		t.Fatal(err)
	}
	st, err := store.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	syncStore(t, st)
	request := func(method, id string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		sessionDetailAPIHandler(st).ServeHTTP(rr, httptest.NewRequest(method, "/api/sessions/"+id+"/trajectory", nil))
		return rr
	}
	rr := request(http.MethodGet, "export-session")
	if rr.Code != 200 {
		t.Fatal(rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Header().Get("Content-Disposition"), ".atif.jsonl") || rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatal(rr.Header())
	}
	if strings.Count(rr.Body.String(), "\n") != 1 {
		t.Fatal("not one JSONL record")
	}
	var result trajectory.Trajectory
	if err := json.Unmarshal(rr.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.SchemaVersion != trajectory.SchemaVersion || len(result.Steps) != 2 || result.Steps[1].Message != "world" {
		t.Fatalf("%+v", result)
	}
	if result.Extra["trace_count"] != float64(1) || result.Extra["included_traces"] != float64(1) || result.Extra["truncated"] != false {
		t.Fatalf("truncation fields = %+v", result.Extra)
	}
	if got := request(http.MethodPost, "export-session"); got.Code != http.StatusMethodNotAllowed {
		t.Fatal(got.Code)
	}
	if got := request(http.MethodGet, "absent"); got.Code != http.StatusNotFound {
		t.Fatal(got.Code)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(content) {
		t.Fatal("export changed source cassette")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	// The cached trajectory for this session state would otherwise hide the
	// now-missing cassette; drop it to exercise the rebuild path.
	if err := os.RemoveAll(filepath.Join(dir, trajectory.DefaultCacheDirName)); err != nil {
		t.Fatal(err)
	}
	rr = request(http.MethodGet, "export-session")
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), "cassette_unavailable") {
		t.Fatal("missing cassette not reported", rr.Code, rr.Body.String())
	}
}

func TestSessionTrajectoryDefaultCapAndFullExport(t *testing.T) {
	withTrajectoryTestGlobals(t)
	trajectoryTraceCap = 2
	dir := t.TempDir()
	sessionID := "cap-session"
	writeTrajectorySession(t, dir, sessionID, 3)
	st, err := store.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	syncStore(t, st)

	capped := trajectoryRequest(st, sessionID, "")
	if capped.Code != http.StatusOK {
		t.Fatal(capped.Code, capped.Body.String())
	}
	var cappedPayload trajectory.Trajectory
	if err := json.Unmarshal(capped.Body.Bytes(), &cappedPayload); err != nil {
		t.Fatal(err)
	}
	if cappedPayload.Extra["trace_count"] != float64(3) || cappedPayload.Extra["included_traces"] != float64(2) ||
		cappedPayload.Extra["truncated"] != true || cappedPayload.Extra["trace_cap"] != float64(2) {
		t.Fatalf("capped extras = %+v", cappedPayload.Extra)
	}
	messages := trajectoryStepMessages(cappedPayload)
	if !containsString(messages, "m0") || !containsString(messages, "m1") || containsString(messages, "m2") {
		t.Fatalf("capped window messages = %v, want the oldest two", messages)
	}

	full := trajectoryRequest(st, sessionID, "?full=1")
	if full.Code != http.StatusOK {
		t.Fatal(full.Code, full.Body.String())
	}
	var fullPayload trajectory.Trajectory
	if err := json.Unmarshal(full.Body.Bytes(), &fullPayload); err != nil {
		t.Fatal(err)
	}
	if fullPayload.Extra["trace_count"] != float64(3) || fullPayload.Extra["included_traces"] != float64(3) || fullPayload.Extra["truncated"] != false {
		t.Fatalf("full extras = %+v", fullPayload.Extra)
	}
	messages = trajectoryStepMessages(fullPayload)
	for _, want := range []string{"m0", "m1", "m2"} {
		if !containsString(messages, want) {
			t.Fatalf("full export messages = %v, missing %q", messages, want)
		}
	}
}

func TestSessionTrajectoryStreamNDJSON(t *testing.T) {
	withTrajectoryTestGlobals(t)
	trajectoryTraceCap = 2
	dir := t.TempDir()
	sessionID := "stream-session"
	writeTrajectorySession(t, dir, sessionID, 3)
	st, err := store.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	syncStore(t, st)

	rr := trajectoryRequest(st, sessionID, "?stream=1")
	if rr.Code != http.StatusOK {
		t.Fatal(rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Header().Get("Content-Type"), "application/x-ndjson") || !strings.Contains(rr.Header().Get("Content-Disposition"), ".atif.ndjson") {
		t.Fatal(rr.Header())
	}
	if !rr.Flushed {
		t.Fatal("stream was never flushed")
	}
	lines := strings.Split(strings.TrimRight(rr.Body.String(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("records = %d, want header + 2 traces + final: %q", len(lines), rr.Body.String())
	}
	for i, line := range lines {
		var record map[string]any
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			t.Fatalf("record %d is not a JSON object: %v", i, err)
		}
	}
	var header trajectory.StreamHeader
	if err := json.Unmarshal([]byte(lines[0]), &header); err != nil {
		t.Fatal(err)
	}
	if header.Type != trajectory.StreamRecordHeader || header.TraceCount != 3 || header.IncludedTraces != 2 || !header.Truncated || header.TraceCap != 2 {
		t.Fatalf("header = %+v", header)
	}
	var final trajectory.StreamFinal
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &final); err != nil {
		t.Fatal(err)
	}
	if final.Type != trajectory.StreamRecordFinal || final.Extra["included_traces"] != float64(2) || final.Extra["truncated"] != true {
		t.Fatalf("final = %+v", final)
	}

	full := trajectoryRequest(st, sessionID, "?stream=1&full=1")
	fullLines := strings.Split(strings.TrimRight(full.Body.String(), "\n"), "\n")
	if len(fullLines) != 5 {
		t.Fatalf("full stream records = %d, want header + 3 traces + final", len(fullLines))
	}
	var fullHeader trajectory.StreamHeader
	if err := json.Unmarshal([]byte(fullLines[0]), &fullHeader); err != nil {
		t.Fatal(err)
	}
	if fullHeader.Truncated || fullHeader.IncludedTraces != 3 {
		t.Fatalf("full stream header = %+v", fullHeader)
	}
}

func TestSessionTrajectoryCacheHitSkipsRebuild(t *testing.T) {
	withTrajectoryTestGlobals(t)
	dir := t.TempDir()
	sessionID := "cache-hit-session"
	writeTrajectorySession(t, dir, sessionID, 1)
	st, err := store.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	syncStore(t, st)

	calls := 0
	buildSessionTrajectory = func(ctx context.Context, sessionID, sessionSource string, exchanges []trajectory.Exchange) (trajectory.Trajectory, error) {
		calls++
		return trajectory.Build(ctx, sessionID, sessionSource, exchanges)
	}

	first := trajectoryRequest(st, sessionID, "")
	if first.Code != http.StatusOK {
		t.Fatal(first.Code, first.Body.String())
	}
	if calls != 1 {
		t.Fatalf("rebuilds after first request = %d, want 1", calls)
	}
	second := trajectoryRequest(st, sessionID, "")
	if second.Code != http.StatusOK {
		t.Fatal(second.Code, second.Body.String())
	}
	if calls != 1 {
		t.Fatalf("rebuilds after cached request = %d, want 1 (a cache hit must not rebuild)", calls)
	}
	if first.Body.String() != second.Body.String() {
		t.Fatalf("cached body differs:\n%s\n%s", first.Body.String(), second.Body.String())
	}
}

func TestSessionTrajectoryCacheKeyTracksAppendedTrace(t *testing.T) {
	withTrajectoryTestGlobals(t)
	dir := t.TempDir()
	sessionID := "cache-append-session"
	writeTrajectorySession(t, dir, sessionID, 1)
	st, err := store.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	syncStore(t, st)

	calls := 0
	buildSessionTrajectory = func(ctx context.Context, sessionID, sessionSource string, exchanges []trajectory.Exchange) (trajectory.Trajectory, error) {
		calls++
		return trajectory.Build(ctx, sessionID, sessionSource, exchanges)
	}

	if rr := trajectoryRequest(st, sessionID, ""); rr.Code != http.StatusOK {
		t.Fatal(rr.Code, rr.Body.String())
	}
	if calls != 1 {
		t.Fatalf("rebuilds = %d, want 1", calls)
	}
	trajectoryRequest(st, sessionID, "")
	if calls != 1 {
		t.Fatalf("rebuilds = %d, want 1 before the append", calls)
	}

	// Appending a trace changes both the trace count and the newest trace id,
	// which is exactly what the cache key is built from, so the next request
	// must rebuild.
	writeTrajectoryTrace(t, dir, sessionID, "trace-append.http", time.Minute, `{"input":"m1"}`, `{"output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"a1"}]}]}`)
	syncStore(t, st)
	if rr := trajectoryRequest(st, sessionID, ""); rr.Code != http.StatusOK {
		t.Fatal(rr.Code, rr.Body.String())
	}
	if calls != 2 {
		t.Fatalf("rebuilds after append = %d, want 2", calls)
	}
	trajectoryRequest(st, sessionID, "")
	if calls != 2 {
		t.Fatalf("rebuilds after append cache hit = %d, want 2", calls)
	}
}

func withTrajectoryTestGlobals(t *testing.T) {
	t.Helper()
	originalBuild := buildSessionTrajectory
	originalCap := trajectoryTraceCap
	t.Cleanup(func() {
		buildSessionTrajectory = originalBuild
		trajectoryTraceCap = originalCap
	})
}

func trajectoryRequest(st *store.Store, sessionID, query string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	sessionDetailAPIHandler(st).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/sessions/"+sessionID+"/trajectory"+query, nil))
	return rr
}

func writeTrajectorySession(t *testing.T, outputDir, sessionID string, traces int) {
	t.Helper()
	for i := 0; i < traces; i++ {
		writeTrajectoryTrace(
			t,
			outputDir,
			sessionID,
			fmt.Sprintf("trace-%d.http", i),
			time.Duration(i)*time.Minute,
			fmt.Sprintf(`{"input":"m%d"}`, i),
			fmt.Sprintf(`{"id":"r%d","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"a%d"}]}]}`, i, i),
		)
	}
}

func writeTrajectoryTrace(t *testing.T, outputDir, sessionID, name string, offset time.Duration, requestBody, responseBody string) {
	t.Helper()
	content := buildRecordFixtureWithStatusHeadersAndMutator(t, "/v1/responses", false, "200 OK", []string{"Session-Id: " + sessionID}, requestBody, responseBody, func(header *recordfile.RecordHeader) {
		header.Meta.Time = time.Date(2026, 3, 27, 8, 0, 0, 0, time.UTC).Add(offset)
	})
	if err := os.WriteFile(filepath.Join(outputDir, name), content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func trajectoryStepMessages(result trajectory.Trajectory) []string {
	messages := make([]string, 0, len(result.Steps))
	for _, step := range result.Steps {
		if step.Message != "" {
			messages = append(messages, step.Message)
		}
	}
	return messages
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
