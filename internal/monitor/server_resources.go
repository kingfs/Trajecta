package monitor

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/internal/observeworker"
	"github.com/kingfs/Trajecta/internal/resources"
	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/observe"
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

// resourceOccurrenceView is one place a resource value was detected. NodeID and
// NodePath are the semantic node the value was read from; Anchor is the
// conversation step that carries it (the node's top-level ancestor), which is
// what the 会话 tab can scroll to and open.
type resourceOccurrenceView struct {
	NodeID   string `json:"node_id"`
	NodePath string `json:"node_path"`
	Anchor   string `json:"anchor"`
	View     string `json:"view"`
	Start    int    `json:"start"`
	End      int    `json:"end"`
}

type resourceLocateResponse struct {
	ID    string `json:"id"`
	Kind  string `json:"kind"`
	Value string `json:"value"`
	// Anchor is empty when the cassette no longer carries the value, so the
	// caller can say "not found" instead of opening the conversation at a guess.
	Anchor string                   `json:"anchor"`
	Items  []resourceOccurrenceView `json:"items"`
}

// handleTraceResourceLocate answers "where was this value in the conversation".
//
// The index stores one row per value and no node reference, and the cassette is
// the source of truth for a detail view, so the answer is recomputed from the
// current parse instead of read from a column: a resource indexed before the
// parser changed still locates, and a value the current detector would not
// extract has no occurrence. The parse is the same one the 协议 tab performs.
func handleTraceResourceLocate(w http.ResponseWriter, r *http.Request, st *store.Store, entry store.LogEntry) {
	q := r.URL.Query()
	kind := strings.TrimSpace(q.Get("kind"))
	value := q.Get("value")
	if kind != resources.KindURL && kind != resources.KindLocalPath {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "kind must be url or local_path"})
		return
	}
	if value == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "value is required"})
		return
	}
	observation, err := observeworker.ReparseTrace(r.Context(), st, nil, entry.ID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "trace cassette not found"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	occurrences := resources.Locate(observation, kind, value)
	anchors := conversationAnchors(observation)
	items := make([]resourceOccurrenceView, 0, len(occurrences))
	for _, occurrence := range occurrences {
		anchor := anchors[occurrence.NodeID]
		if anchor == "" {
			anchor = occurrence.NodeID
		}
		items = append(items, resourceOccurrenceView{
			NodeID:   occurrence.NodeID,
			NodePath: occurrence.NodePath,
			Anchor:   anchor,
			View:     occurrence.View,
			Start:    occurrence.Start,
			End:      occurrence.End,
		})
	}
	payload := resourceLocateResponse{
		ID:    entry.ID,
		Kind:  kind,
		Value: value,
		Items: items,
	}
	if len(items) > 0 {
		payload.Anchor = items[0].Anchor
	}
	writeJSON(w, http.StatusOK, payload)
}

// conversationAnchors maps every node id to the id of its top-level ancestor.
// The 会话 tab renders depth-zero nodes as steps and keeps their descendants as
// the step's parts, calls and results, so a value found in a part has to be
// addressed by the step the reader can open.
func conversationAnchors(obs observe.TraceObservation) map[string]string {
	parents := make(map[string]string, 32)
	for _, node := range observe.TraceNodes(obs) {
		if node.ID != "" && node.ParentID != "" {
			parents[node.ID] = node.ParentID
		}
	}
	anchors := make(map[string]string, len(parents))
	for _, node := range observe.TraceNodes(obs) {
		if node.ID == "" {
			continue
		}
		id := node.ID
		seen := make(map[string]struct{}, 4)
		for {
			if _, ok := seen[id]; ok {
				break
			}
			seen[id] = struct{}{}
			parent, ok := parents[id]
			if !ok || parent == "" {
				break
			}
			id = parent
		}
		anchors[node.ID] = id
	}
	return anchors
}
