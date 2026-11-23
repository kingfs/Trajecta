package monitor

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
)

// Project attribution views: which project directory a client harness declared
// about itself, per session and per trace.
//
// The value is a declaration the harness wrote in its prompt (deepseek harness
// runtime context, Claude Code <env>, Codex <environment_context>); it is never
// inferred from the paths a model touched. A session with no declaration is
// absent from these views, and the list reports the coverage so "not counted"
// never reads as "counted as zero".

type projectListItem struct {
	Root         string    `json:"root"`
	RootKey      string    `json:"root_key"`
	SessionCount int       `json:"session_count"`
	RequestCount int       `json:"request_count"`
	TotalTokens  int       `json:"total_tokens"`
	FirstSeen    time.Time `json:"first_seen"`
	LastSeen     time.Time `json:"last_seen"`
	Sources      []string  `json:"sources"`
}

type projectListResponse struct {
	Items              []projectListItem `json:"items"`
	Page               int               `json:"page"`
	PageSize           int               `json:"page_size"`
	Total              int               `json:"total"`
	TotalPages         int               `json:"total_pages"`
	IdentifiedSessions int               `json:"identified_sessions"`
	TotalSessions      int               `json:"total_sessions"`
	Window             string            `json:"window"`
	RefreshedAt        time.Time         `json:"refreshed_at"`
}

// projectView is the project attached to one session or one trace.
type projectView struct {
	Root    string `json:"root"`
	RootKey string `json:"root_key"`
	Source  string `json:"source,omitempty"`
	// LatestRoot is set when the session changed directory after it started;
	// Root stays the project the session belongs to.
	LatestRoot  string `json:"latest_root,omitempty"`
	SwitchCount int    `json:"switch_count,omitempty"`
	// Conflicting is trace-scoped: the request declared more than one root and
	// the last one was used.
	Conflicting bool `json:"conflicting,omitempty"`
}

func projectViewFromSession(item store.SessionProject) *projectView {
	return &projectView{
		Root:        item.Root,
		RootKey:     item.RootKey,
		Source:      item.Source,
		LatestRoot:  item.LatestRoot,
		SwitchCount: item.SwitchCount,
	}
}

func projectViewFromTrace(item store.TraceProject) *projectView {
	return &projectView{
		Root:        item.Root,
		RootKey:     item.RootKey,
		Source:      item.Source,
		Conflicting: item.Conflicting,
	}
}

// attachSessionProjects fills the project of a page of session rows in one
// query. It runs after the list because the two session read paths (logs and
// session_summaries) both end with the same page of session ids, and the
// project is not part of either row.
func attachSessionProjects(st *store.Store, items []sessionListItem) error {
	if len(items) == 0 {
		return nil
	}
	sessionIDs := make([]string, 0, len(items))
	for _, item := range items {
		sessionIDs = append(sessionIDs, item.SessionID)
	}
	rollups, err := st.ListSessionProjects(sessionIDs)
	if err != nil {
		return err
	}
	for index := range items {
		if rollup, ok := rollups[items[index].SessionID]; ok {
			items[index].Project = projectViewFromSession(rollup)
		}
	}
	return nil
}

// projectsAPIHandler serves the project list: one row per declared root, with
// the sessions attributed to it and the window's coverage.
func projectsAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		window := strings.ToLower(strings.TrimSpace(q.Get("window")))
		var since time.Time
		switch window {
		case "", "all":
			window = "all"
		default:
			var label string
			label, since = parseAnalyticsWindow(window)
			window = label
		}
		page, err := st.ListProjects(store.ProjectFilter{
			Query:    strings.TrimSpace(q.Get("q")),
			Since:    since,
			Page:     parseInt(q.Get("page"), 1),
			PageSize: parsePageSize(q.Get("page_size")),
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query projects: " + err.Error()})
			return
		}
		resp := projectListResponse{
			Page:               page.Page,
			PageSize:           page.PageSize,
			Total:              page.Total,
			TotalPages:         page.TotalPages,
			IdentifiedSessions: page.IdentifiedSessions,
			TotalSessions:      page.TotalSessions,
			Window:             window,
			RefreshedAt:        time.Now().UTC(),
			Items:              []projectListItem{},
		}
		for _, item := range page.Items {
			resp.Items = append(resp.Items, projectListItem{
				Root:         item.Root,
				RootKey:      item.RootKey,
				SessionCount: item.SessionCount,
				RequestCount: item.RequestCount,
				TotalTokens:  item.TotalTokens,
				FirstSeen:    item.FirstSeen,
				LastSeen:     item.LastSeen,
				Sources:      item.Sources,
			})
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

type projectBackfillRequest struct {
	Window string `json:"window"`
	Limit  int    `json:"limit"`
}

type projectBackfillResponse struct {
	Queued int    `json:"queued"`
	Window string `json:"window"`
}

// projectBackfillAPIHandler queues parse jobs for traces with no project row
// yet. The parse worker extracts the declaration, so the request only touches
// the queue; the response reports how many traces were selected.
//
// A trace that declares no project never gets a row, so it stays a candidate
// and a repeated call re-parses it. That is the resource backfill's trade-off
// too: the alternative is one marker row per trace forever.
func projectBackfillAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		var req projectBackfillRequest
		if r.Body != nil {
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
				return
			}
		}
		window := strings.ToLower(strings.TrimSpace(req.Window))
		var since time.Time
		switch window {
		case "", "all":
			window = "all"
		default:
			var label string
			label, since = parseAnalyticsWindow(window)
			window = label
		}
		queued, err := st.EnqueueProjectBackfill(since, req.Limit)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "queue project backfill: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, projectBackfillResponse{Queued: queued, Window: window})
	}
}
