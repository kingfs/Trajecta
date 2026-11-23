package monitor

import (
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/internal/sessionsteps"
	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/internal/trajectory"
)

// sessionStepsCacheDir holds the throwaway step views. It is deliberately not
// `*.http`, so the cassette indexer ignores it, and it lives beside the
// trajectory cache because both are derived and both can be deleted while the
// server is stopped.
const sessionStepsCacheDir = "session-steps-cache"

// sessionStepsCache returns the derived cache for the step views, or nil when
// the store has no output directory configured.
func sessionStepsCache(st *store.Store) *trajectory.Cache {
	if st == nil {
		return nil
	}
	dir := strings.TrimSpace(st.OutputDir())
	if dir == "" {
		return nil
	}
	return trajectory.NewCache(filepath.Join(dir, sessionStepsCacheDir))
}

// sessionStepsOptions reads the window parameters. The defaults are the ones
// the page uses: one hundred steps ending at the focused trace. The cassette
// budget is larger than the trace list previews' two seconds because this read
// is an explicit action - a reader opened the tab and is waiting for it.
func sessionStepsOptions(r *http.Request, focus string) sessionsteps.Options {
	query := r.URL.Query()
	options := sessionsteps.Options{FocusTraceID: focus}
	if value := strings.TrimSpace(query.Get("limit")); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			options.Limit = parsed
		}
	}
	options.BeforeTraceID = strings.TrimSpace(query.Get("before"))
	if value := strings.TrimSpace(query.Get("budget_ms")); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed >= 0 && parsed <= 10000 {
			options.Budget = time.Duration(parsed) * time.Millisecond
		}
	}
	return options
}

// handleTraceSessionSteps renders the step model of the session a trace
// belongs to, ending at that trace. A trace recorded without a session still
// renders: one step is a truthful answer, an error is not.
func handleTraceSessionSteps(w http.ResponseWriter, r *http.Request, st *store.Store, entry store.LogEntry) {
	if st == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
		return
	}
	options := sessionStepsOptions(r, entry.ID)
	options.Cache = sessionStepsCache(st)

	sessionID := strings.TrimSpace(entry.SessionID)
	entries := []store.LogEntry{entry}
	if sessionID != "" {
		listed, err := st.ListTracesBySession(sessionID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		if len(listed) > 0 {
			entries = listed
		}
	}
	view, err := sessionsteps.Build(r.Context(), sessionID, entries, options)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	view.FocusTraceID = entry.ID
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, view)
}

// handleSessionSteps renders the same model for the session itself, which the
// session detail page uses and which the trace tab reuses through its own
// focus-scoped route.
func handleSessionSteps(w http.ResponseWriter, r *http.Request, st *store.Store, sessionID string) {
	if st == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		http.NotFound(w, r)
		return
	}
	entries, err := st.ListTracesBySession(sessionID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	options := sessionStepsOptions(r, "")
	options.FocusTraceID = ""
	options.Cache = sessionStepsCache(st)
	view, err := sessionsteps.Build(r.Context(), sessionID, entries, options)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, view)
}
