package monitor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// projectTestLog indexes one minimal cassette under a session id, the way the
// recorder does, so the project API has a session to attribute.
func projectTestLog(t *testing.T, st *store.Store, outputDir, requestID, sessionID string) store.LogEntry {
	t.Helper()
	recorded := time.Date(2026, 11, 23, 9, 0, 0, 0, time.UTC)
	reqHead := "POST /v1/chat/completions HTTP/1.1\r\nHost: example.com\r\nContent-Type: application/json\r\n\r\n"
	reqBody := `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	resHead := "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n"
	resBody := `{"id":"chatcmpl_1","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}]}`
	header := recordfile.RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta: recordfile.MetaData{
			RequestID:     requestID,
			Time:          recorded,
			Model:         "gpt-4o",
			Provider:      "openai_compatible",
			Operation:     "chat_completions",
			Endpoint:      "/v1/chat/completions",
			URL:           "/v1/chat/completions",
			Method:        http.MethodPost,
			StatusCode:    200,
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
		t.Fatalf("MarshalPrelude: %v", err)
	}
	path := filepath.Join(outputDir, requestID+".http")
	if err := os.WriteFile(path, []byte(string(prelude)+reqHead+reqBody+"\n"+resHead+resBody), 0o644); err != nil {
		t.Fatalf("write cassette: %v", err)
	}
	if err := st.UpsertLogWithGrouping(path, header, store.GroupingInfo{SessionID: sessionID, SessionSource: "test"}); err != nil {
		t.Fatalf("upsert log: %v", err)
	}
	entry, err := st.GetByRequestID(requestID)
	if err != nil {
		t.Fatalf("get log: %v", err)
	}
	return entry
}

func TestProjectsAPIAndSessionFilter(t *testing.T) {
	outputDir := t.TempDir()
	st, err := newTestStore(outputDir)
	if err != nil {
		t.Fatalf("newTestStore() error = %v", err)
	}
	defer st.Close()

	alpha := projectTestLog(t, st, outputDir, "project-api-alpha", "s-alpha")
	projectTestLog(t, st, outputDir, "project-api-beta", "s-beta")
	if err := st.SaveTraceProject(alpha.ID, "s-alpha", &store.ProjectRecord{
		Root: "/repo/alpha", RootKey: "/repo/alpha", Source: "dsh_runtime_context",
	}); err != nil {
		t.Fatalf("SaveTraceProject() error = %v", err)
	}
	if err := st.RebuildSessionProject("s-alpha"); err != nil {
		t.Fatalf("RebuildSessionProject() error = %v", err)
	}
	if err := st.RebuildSessionSummaries(); err != nil {
		t.Fatalf("RebuildSessionSummaries() error = %v", err)
	}

	// The list reports the one project and the attribution coverage of the
	// window: one of the two sessions declared a project.
	rr := httptest.NewRecorder()
	projectsAPIHandler(st).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/projects?window=all", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("projects status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var projects projectListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &projects); err != nil {
		t.Fatalf("decode projects: %v", err)
	}
	if len(projects.Items) != 1 || projects.Items[0].Root != "/repo/alpha" {
		t.Fatalf("projects = %+v", projects.Items)
	}
	if projects.Items[0].SessionCount != 1 || projects.Items[0].RequestCount != 1 {
		t.Fatalf("project counts = %+v", projects.Items[0])
	}
	if projects.IdentifiedSessions != 1 || projects.TotalSessions != 2 {
		t.Fatalf("coverage = %d/%d, want 1/2", projects.IdentifiedSessions, projects.TotalSessions)
	}

	// The session list filters by project and carries the project of each row.
	rr = httptest.NewRecorder()
	sessionListAPIHandler(st).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/sessions?window=all&project=%2Frepo%2Falpha", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("sessions status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var sessions sessionListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &sessions); err != nil {
		t.Fatalf("decode sessions: %v", err)
	}
	if sessions.Total != 1 || len(sessions.Items) != 1 || sessions.Items[0].SessionID != "s-alpha" {
		t.Fatalf("filtered sessions = %+v (total %d)", sessions.Items, sessions.Total)
	}
	if sessions.Items[0].Project == nil || sessions.Items[0].Project.Root != "/repo/alpha" {
		t.Fatalf("session project = %+v", sessions.Items[0].Project)
	}

	// Unfiltered, both sessions are listed and only the attributed one has a
	// project.
	rr = httptest.NewRecorder()
	sessionListAPIHandler(st).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/sessions?window=all", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("unfiltered sessions status = %d", rr.Code)
	}
	sessions = sessionListResponse{}
	if err := json.Unmarshal(rr.Body.Bytes(), &sessions); err != nil {
		t.Fatalf("decode unfiltered sessions: %v", err)
	}
	if sessions.Total != 2 {
		t.Fatalf("unfiltered total = %d, want 2", sessions.Total)
	}
	for _, item := range sessions.Items {
		if item.SessionID == "s-beta" && item.Project != nil {
			t.Fatalf("s-beta has project %+v, want none", item.Project)
		}
	}

	// The trace detail carries the trace-level declaration.
	rr = httptest.NewRecorder()
	traceAPIHandlerWithSuppression(st, nil, nil).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/traces/"+alpha.ID, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("trace detail status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var detail detailResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.Project == nil || detail.Project.Root != "/repo/alpha" || detail.Project.Source != "dsh_runtime_context" {
		t.Fatalf("detail project = %+v", detail.Project)
	}

	// The session detail carries the rollup.
	rr = httptest.NewRecorder()
	sessionDetailAPIHandler(st).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/sessions/s-alpha", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("session detail status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var sessionDetail sessionDetailResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &sessionDetail); err != nil {
		t.Fatalf("decode session detail: %v", err)
	}
	if sessionDetail.Project == nil || sessionDetail.Project.Root != "/repo/alpha" {
		t.Fatalf("session detail project = %+v", sessionDetail.Project)
	}
}

// The detection report surfaces the rejections that the project list cannot
// show (a rejected trace has no project), and the trace detail says why.
func TestProjectDetectionAPI(t *testing.T) {
	outputDir := t.TempDir()
	st, err := newTestStore(outputDir)
	if err != nil {
		t.Fatalf("newTestStore() error = %v", err)
	}
	defer st.Close()

	rejected := projectTestLog(t, st, outputDir, "project-detect-rejected", "s-detect")
	if err := st.SaveTraceProject(rejected.ID, "s-detect", &store.ProjectRecord{
		Rejected: true,
		Source:   "codex_env",
		Reason:   "unreadable",
		Evidence: "<environment_context> without <cwd>",
	}); err != nil {
		t.Fatalf("SaveTraceProject() error = %v", err)
	}
	if err := st.RebuildSessionProject("s-detect"); err != nil {
		t.Fatalf("RebuildSessionProject() error = %v", err)
	}

	rr := httptest.NewRecorder()
	projectDetectionAPIHandler(st).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/projects/detection?window=all", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("detection status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var detection projectDetectionResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &detection); err != nil {
		t.Fatalf("decode detection: %v", err)
	}
	if detection.Declared != 0 || detection.Rejected != 1 {
		t.Fatalf("detection = %+v, want 1 rejection", detection)
	}
	if len(detection.ByReason) != 1 || detection.ByReason[0].Label != "unreadable" {
		t.Fatalf("by reason = %+v", detection.ByReason)
	}
	if len(detection.Recent) != 1 || detection.Recent[0].TraceID != rejected.ID {
		t.Fatalf("recent = %+v", detection.Recent)
	}

	// A rejected trace is not a project, so the project list stays empty.
	rr = httptest.NewRecorder()
	projectsAPIHandler(st).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/projects?window=all", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("projects status = %d", rr.Code)
	}
	var projects projectListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &projects); err != nil {
		t.Fatalf("decode projects: %v", err)
	}
	if len(projects.Items) != 0 {
		t.Fatalf("projects = %+v, want none", projects.Items)
	}

	// The trace detail carries the rejection and its reason.
	rr = httptest.NewRecorder()
	traceAPIHandlerWithSuppression(st, nil, nil).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/traces/"+rejected.ID, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("trace detail status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var detail detailResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.Project == nil || detail.Project.Status != "rejected" || detail.Project.Reason != "unreadable" {
		t.Fatalf("detail project = %+v", detail.Project)
	}
	if detail.Project.Root != "" {
		t.Fatalf("rejected project root = %q, want empty", detail.Project.Root)
	}
}

// The alias API creates, lists and deletes a merge; the projects page and the
// session filter both see the merged view.
func TestProjectAliasAPI(t *testing.T) {
	outputDir := t.TempDir()
	st, err := newTestStore(outputDir)
	if err != nil {
		t.Fatalf("newTestStore() error = %v", err)
	}
	defer st.Close()

	container := projectTestLog(t, st, outputDir, "project-alias-container", "s-container")
	host := projectTestLog(t, st, outputDir, "project-alias-host", "s-host")
	for _, item := range []struct {
		entry store.LogEntry
		root  string
	}{
		{container, "/app"},
		{host, "/data/src/app"},
	} {
		if err := st.SaveTraceProject(item.entry.ID, item.entry.SessionID, &store.ProjectRecord{
			Root: item.root, RootKey: item.root, Source: "dsh_runtime_context",
		}); err != nil {
			t.Fatalf("SaveTraceProject(%s): %v", item.root, err)
		}
		if err := st.RebuildSessionProject(item.entry.SessionID); err != nil {
			t.Fatalf("RebuildSessionProject(%s): %v", item.root, err)
		}
	}

	body := `{"alias_key":"/app","canonical_key":"/data/src/app","canonical_root":"/data/src/app","note":"container mount"}`
	rr := httptest.NewRecorder()
	projectAliasesAPIHandler(st).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/project-aliases", strings.NewReader(body)))
	if rr.Code != http.StatusOK {
		t.Fatalf("create alias status = %d, body=%s", rr.Code, rr.Body.String())
	}

	rr = httptest.NewRecorder()
	projectAliasesAPIHandler(st).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/project-aliases", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("list aliases status = %d", rr.Code)
	}
	var aliases projectAliasListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &aliases); err != nil {
		t.Fatalf("decode aliases: %v", err)
	}
	if len(aliases.Items) != 1 || aliases.Items[0].AliasKey != "/app" {
		t.Fatalf("aliases = %+v", aliases.Items)
	}

	rr = httptest.NewRecorder()
	projectsAPIHandler(st).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/projects?window=all", nil))
	var projects projectListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &projects); err != nil {
		t.Fatalf("decode projects: %v", err)
	}
	if projects.Total != 1 || len(projects.Items) != 1 {
		t.Fatalf("merged projects = %+v", projects.Items)
	}
	if projects.Items[0].SessionCount != 2 || projects.Items[0].PathCount != 2 || projects.Items[0].RootKey != "/data/src/app" {
		t.Fatalf("merged row = %+v", projects.Items[0])
	}

	// Both the canonical key and the merged path list the whole project.
	for _, key := range []string{"/data/src/app", "/app"} {
		rr = httptest.NewRecorder()
		sessionListAPIHandler(st).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/sessions?window=all&project="+url.QueryEscape(key), nil))
		var sessions sessionListResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &sessions); err != nil {
			t.Fatalf("decode sessions for %q: %v", key, err)
		}
		if sessions.Total != 2 {
			t.Fatalf("filter %q total = %d, want 2", key, sessions.Total)
		}
	}

	// Self-merge and chained merges are rejected with precise codes.
	for _, tc := range []struct {
		body string
		want int
	}{
		{`{"alias_key":"/x","canonical_key":"/x"}`, http.StatusBadRequest},
		{`{"alias_key":"/data/src/app","canonical_key":"/y"}`, http.StatusConflict},
	} {
		rr = httptest.NewRecorder()
		projectAliasesAPIHandler(st).ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/project-aliases", strings.NewReader(tc.body)))
		if rr.Code != tc.want {
			t.Fatalf("body %s status = %d, want %d", tc.body, rr.Code, tc.want)
		}
	}

	// Deleting the alias restores the separate projects.
	rr = httptest.NewRecorder()
	projectAliasesDeleteAPIHandler(st).ServeHTTP(rr, httptest.NewRequest(http.MethodDelete, "/api/project-aliases?alias_key="+url.QueryEscape("/app"), nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("delete alias status = %d, body=%s", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	projectsAPIHandler(st).ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/projects?window=all", nil))
	projects = projectListResponse{}
	if err := json.Unmarshal(rr.Body.Bytes(), &projects); err != nil {
		t.Fatalf("decode projects after delete: %v", err)
	}
	if projects.Total != 2 {
		t.Fatalf("after delete total = %d, want 2", projects.Total)
	}
}
