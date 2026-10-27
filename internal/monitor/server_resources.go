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

// Resource views: the paths and URLs a trace carried, and the global catalogue
// that deduplicates them across traces and sessions. Neither view holds file
// content; the value column is the path or URL itself.

type traceResourceListView struct {
	ID    string                `json:"id"`
	Items []store.TraceResource `json:"items"`
	Total int                   `json:"total"`
}

type resourceListResponse struct {
	store.ResourcePage
	Kind   string `json:"kind,omitempty"`
	Query  string `json:"query,omitempty"`
	Window string `json:"window"`
}

func handleTraceResources(w http.ResponseWriter, r *http.Request, st *store.Store, entry store.LogEntry) {
	items, err := st.ListTraceResources(entry.ID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query trace resources: " + err.Error()})
		return
	}
	if items == nil {
		items = []store.TraceResource{}
	}
	writeJSON(w, http.StatusOK, traceResourceListView{ID: entry.ID, Items: items, Total: len(items)})
}

// resourcesAPIHandler serves the catalogue page. Unlike the analytics endpoints
// its default window is "all": a catalogue that hid everything older than today
// would answer "which paths this deployment has ever accessed" with one day.
func resourcesAPIHandler(st *store.Store) http.HandlerFunc {
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
		filter := store.ResourceFilter{
			Kind:     strings.TrimSpace(q.Get("kind")),
			Query:    strings.TrimSpace(q.Get("q")),
			Since:    since,
			Page:     parseInt(q.Get("page"), 1),
			PageSize: parsePageSize(q.Get("page_size")),
		}
		page, err := st.ListResources(filter)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query resources: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, resourceListResponse{
			ResourcePage: page,
			Kind:         filter.Kind,
			Query:        filter.Query,
			Window:       window,
		})
	}
}

type resourceBackfillRequest struct {
	Window string `json:"window"`
	Limit  int    `json:"limit"`
}

type resourceBackfillResponse struct {
	Queued int    `json:"queued"`
	Window string `json:"window"`
}

// resourceBackfillAPIHandler queues parse jobs for traces that have no resource
// rows yet. The parse worker does the extraction, so the request only touches
// the queue; the response reports how many traces were selected.
func resourceBackfillAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		var req resourceBackfillRequest
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
		queued, err := st.EnqueueResourceBackfill(since, req.Limit)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "queue resource backfill: " + err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, resourceBackfillResponse{Queued: queued, Window: window})
	}
}
