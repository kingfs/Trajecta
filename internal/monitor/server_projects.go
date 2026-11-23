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
	// PathCount is how many raw paths this row merges (operator aliases).
	PathCount int `json:"path_count"`
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
	// MultipleRoots is trace-scoped: the request declared more than one root
	// and the last one was used.
	MultipleRoots bool `json:"multiple_roots,omitempty"`
	// Status is "rejected" when a declaration block carried no usable root;
	// Reason and Evidence then say why. A declared project leaves them empty.
	Status   string `json:"status,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Evidence string `json:"evidence,omitempty"`
}

func projectViewFromSession(item store.SessionProject) *projectView {
	root, rootKey := item.CanonicalRoot, item.CanonicalKey
	if root == "" {
		root = item.Root
	}
	if rootKey == "" {
		rootKey = item.RootKey
	}
	return &projectView{
		Root:        root,
		RootKey:     rootKey,
		Source:      item.Source,
		LatestRoot:  item.LatestRoot,
		SwitchCount: item.SwitchCount,
	}
}

func projectViewFromTrace(item store.TraceProject) *projectView {
	view := &projectView{
		Root:          item.Root,
		RootKey:       item.RootKey,
		Source:        item.Source,
		MultipleRoots: item.MultipleRoots,
	}
	if item.Status == store.ProjectRejected {
		view.Status = string(store.ProjectRejected)
		view.Reason = item.Reason
		view.Evidence = item.Evidence
	}
	return view
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
				PathCount:    item.PathCount,
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

type projectDetectionCount struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

type projectDetectionHarness struct {
	Harness  string `json:"harness"`
	Declared int    `json:"declared"`
	Rejected int    `json:"rejected"`
}

type projectRejectionView struct {
	TraceID    string    `json:"trace_id"`
	SessionID  string    `json:"session_id,omitempty"`
	Source     string    `json:"source,omitempty"`
	Reason     string    `json:"reason"`
	Evidence   string    `json:"evidence,omitempty"`
	Harness    string    `json:"harness,omitempty"`
	RecordedAt time.Time `json:"recorded_at"`
}

type projectDetectionResponse struct {
	Declared    int                       `json:"declared"`
	Rejected    int                       `json:"rejected"`
	ByReason    []projectDetectionCount   `json:"by_reason"`
	ByHarness   []projectDetectionHarness `json:"by_harness"`
	Recent      []projectRejectionView    `json:"recent"`
	Window      string                    `json:"window"`
	RefreshedAt time.Time                 `json:"refreshed_at"`
}

// projectDetectionAPIHandler reports how declaration extraction went: how many
// traces declared a project, how many carried a declaration-shaped value that
// was rejected and why. It is the early-warning surface for a harness that
// changed its prompt format, which otherwise only shows up as a quietly lower
// attribution coverage.
func projectDetectionAPIHandler(st *store.Store) http.HandlerFunc {
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
		report, err := st.ProjectDetectionReport(store.ProjectDetectionFilter{Since: since})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query project detection: " + err.Error()})
			return
		}
		resp := projectDetectionResponse{
			Declared:    report.Declared,
			Rejected:    report.Rejected,
			ByReason:    make([]projectDetectionCount, 0, len(report.ByReason)),
			ByHarness:   make([]projectDetectionHarness, 0, len(report.ByHarness)),
			Recent:      make([]projectRejectionView, 0, len(report.Recent)),
			Window:      window,
			RefreshedAt: time.Now().UTC(),
		}
		for _, item := range report.ByReason {
			resp.ByReason = append(resp.ByReason, projectDetectionCount{Label: item.Label, Count: item.Count})
		}
		for _, item := range report.ByHarness {
			resp.ByHarness = append(resp.ByHarness, projectDetectionHarness{
				Harness: item.Harness, Declared: item.Declared, Rejected: item.Rejected,
			})
		}
		for _, item := range report.Recent {
			resp.Recent = append(resp.Recent, projectRejectionView{
				TraceID: item.TraceID, SessionID: item.SessionID, Source: item.Source,
				Reason: item.Reason, Evidence: item.Evidence, Harness: item.Harness,
				RecordedAt: item.RecordedAt,
			})
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// projectAliasView is one operator merge: a raw path counted as the project it
// was merged into.
type projectAliasView struct {
	AliasKey      string    `json:"alias_key"`
	CanonicalKey  string    `json:"canonical_key"`
	CanonicalRoot string    `json:"canonical_root"`
	Note          string    `json:"note,omitempty"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type projectAliasListResponse struct {
	Items []projectAliasView `json:"items"`
}

type projectAliasRequest struct {
	AliasKey      string `json:"alias_key"`
	CanonicalKey  string `json:"canonical_key"`
	CanonicalRoot string `json:"canonical_root"`
	Note          string `json:"note"`
}

func projectAliasViewOf(item store.ProjectAlias) projectAliasView {
	return projectAliasView{
		AliasKey:      item.AliasKey,
		CanonicalKey:  item.CanonicalKey,
		CanonicalRoot: item.CanonicalRoot,
		Note:          item.Note,
		UpdatedAt:     item.UpdatedAt,
	}
}

// projectAliasesAPIHandler lists the merges, creates or retargets one, and
// deletes one. A merge is a read-time view: it rewrites no derived row, so
// deleting the alias restores the separate projects.
func projectAliasesAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			items, err := st.ListProjectAliases()
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query project aliases: " + err.Error()})
				return
			}
			resp := projectAliasListResponse{Items: make([]projectAliasView, 0, len(items))}
			for _, item := range items {
				resp.Items = append(resp.Items, projectAliasViewOf(item))
			}
			writeJSON(w, http.StatusOK, resp)
		case http.MethodPost:
			var req projectAliasRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
				return
			}
			err := st.SaveProjectAlias(store.ProjectAlias{
				AliasKey:      req.AliasKey,
				CanonicalKey:  req.CanonicalKey,
				CanonicalRoot: req.CanonicalRoot,
				Note:          req.Note,
			})
			if err != nil {
				switch {
				case errors.Is(err, store.ErrProjectAliasRequired), errors.Is(err, store.ErrProjectAliasSelf):
					writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
				case errors.Is(err, store.ErrProjectAliasChain):
					writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
				default:
					writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "save project alias: " + err.Error()})
				}
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		default:
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		}
	}
}

// projectAliasesDeleteAPIHandler deletes one alias. The key travels as a query
// parameter, not a path segment: a project key is an absolute path, and URL
// path cleaning would eat its leading slash.
func projectAliasesDeleteAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		aliasKey := strings.TrimSpace(r.URL.Query().Get("alias_key"))
		if aliasKey == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "alias_key is required"})
			return
		}
		if err := st.DeleteProjectAlias(aliasKey); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "delete project alias: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
