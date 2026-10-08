package monitor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/internal/trajectory"
	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// trajectoryTraceCap bounds the default response. A trajectory rebuild opens one
// cassette per trace, and on the production disk a random cassette open costs
// about 57 ms independently of the bytes read, so the default view is capped and
// the complete session is an explicit `?full=1` (or `?stream=1`) export. It is a
// var rather than a const so tests can lower it without writing 500 cassettes.
var trajectoryTraceCap = 500

// buildSessionTrajectory is the rebuild seam: production calls trajectory.Build,
// while tests replace it to prove that a cache hit never rebuilds.
var buildSessionTrajectory = func(ctx context.Context, sessionID, sessionSource string, exchanges []trajectory.Exchange) (trajectory.Trajectory, error) {
	return trajectory.Build(ctx, sessionID, sessionSource, exchanges)
}

func handleSessionTrajectory(w http.ResponseWriter, r *http.Request, st *store.Store, sessionID string) {
	// One query returns every client-visible trace of the session; the per-trace
	// work left is the cassette read itself, which is the cost the cache and the
	// cap exist to bound.
	entries, err := st.ListTracesBySession(sessionID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Unable to query session"})
		return
	}
	if len(entries) == 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Session has no recorded requests"})
		return
	}

	query := r.URL.Query()
	full := trajectoryBoolParam(query.Get("full"))
	stream := trajectoryBoolParam(query.Get("stream"))
	limit := trajectoryTraceCap
	if full {
		limit = 0
	}

	total := len(entries)
	included := entries
	if limit > 0 && total > limit {
		// ListTracesBySession returns newest first. The default cap keeps the
		// oldest `limit` traces so the reconstruction starts at the beginning of
		// the session and the builder sees a coherent history; the complete tail
		// is the explicit full export.
		included = entries[total-limit:]
	}
	truncated := len(included) < total
	sessionSource := entries[0].SessionSource

	if stream {
		handleTrajectoryStream(w, r, sessionID, sessionSource, included, total, truncated, limit)
		return
	}

	cache := trajectoryCache(st)
	var (
		key  string
		data []byte
	)
	if cache != nil && limit > 0 {
		// Cache hits are keyed by (session_id, last_trace_id, trace_count, limit).
		// A full export is never cached: for the sessions it exists for it would
		// be a multi-hundred-megabyte file that immediately falls out of the
		// bounded cache, so it is cheaper to rebuild it on demand.
		key = trajectory.CacheKey(sessionID, entries[0].ID, total, limit)
		if cached, ok := cache.Load(key); ok {
			data = cached
		}
	}

	if data == nil {
		exchanges, err := readTrajectoryExchanges(r.Context(), included)
		if err != nil {
			// The client is gone; there is nothing left to answer.
			return
		}
		result, err := buildSessionTrajectory(r.Context(), sessionID, sessionSource, exchanges)
		if err != nil {
			if r.Context().Err() != nil {
				return
			}
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": err.Error()})
			return
		}
		annotateTrajectoryExtra(&result, total, len(exchanges), limit)
		data, err = json.Marshal(result)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "Unable to serialize trajectory"})
			return
		}
		if cache != nil && limit > 0 {
			_ = cache.Store(key, data)
		}
	}

	writeTrajectoryBody(w, sessionID, data, len(data)+1, ".atif.jsonl")
}

// handleTrajectoryStream writes the NDJSON export. The response is started
// before the first cassette is read and each record is flushed as it is
// produced, so a 10,000-trace session never has to fit in memory at either end.
func handleTrajectoryStream(w http.ResponseWriter, r *http.Request, sessionID, sessionSource string, entries []store.LogEntry, total int, truncated bool, limit int) {
	ordered := ascendingTrajectoryEntries(entries)
	position := 0
	next := func() (trajectory.Exchange, bool) {
		if position >= len(ordered) {
			return trajectory.Exchange{}, false
		}
		entry := ordered[position]
		position++
		return readTrajectoryExchange(entry), true
	}
	flush := func() error { return nil }
	if flusher, ok := w.(http.Flusher); ok {
		flush = func() error {
			flusher.Flush()
			return nil
		}
	}

	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": trajectoryFilename(sessionID, ".atif.ndjson")}))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)

	options := trajectory.StreamOptions{TraceCount: total, IncludedTraces: len(ordered), Truncated: truncated, TraceCap: limit}
	// Headers are already committed, so a mid-stream error can only end the
	// response early; the client sees a truncated stream, never a wrong one.
	_ = trajectory.Stream(r.Context(), sessionID, sessionSource, options, next, w, flush)
}

// readTrajectoryExchanges reads the selected cassettes into memory for the
// buffered response. The streaming path reads them lazily instead.
func readTrajectoryExchanges(ctx context.Context, entries []store.LogEntry) ([]trajectory.Exchange, error) {
	exchanges := make([]trajectory.Exchange, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		exchanges = append(exchanges, readTrajectoryExchange(entry))
	}
	return exchanges, nil
}

// readTrajectoryExchange builds the client-visible part of one recorded trace.
// A cassette that cannot be read is not a hard failure: it becomes a recorded
// gap in the trajectory, exactly as before.
func readTrajectoryExchange(entry store.LogEntry) trajectory.Exchange {
	ex := trajectory.Exchange{TraceID: entry.ID, Time: entry.Header.Meta.Time, Model: entry.Header.Meta.Model, Endpoint: entry.Header.Meta.Endpoint, StatusCode: entry.Header.Meta.StatusCode, DurationMs: entry.Header.Meta.DurationMs, ExchangeKind: entry.Header.Meta.ExchangeKind}
	content, readErr := os.ReadFile(entry.LogPath)
	if readErr != nil {
		ex.Error = "Recorded cassette is missing or unreadable"
		return ex
	}
	parsed, parseErr := recordfile.ParsePrelude(content)
	if parseErr != nil {
		ex.Error = "Recorded cassette prelude is invalid"
		return ex
	}
	var requestFull []byte
	requestFull, ex.Request, _, ex.Response = recordfile.ExtractSections(content, parsed)
	if request, err := http.ReadRequest(bufio.NewReader(bytes.NewReader(requestFull))); err == nil {
		ex.UserAgent = request.UserAgent()
		ex.Originator = request.Header.Get("Originator")
		_ = request.Body.Close()
	}
	ex.Stream = parsed.Header.Layout.IsStream
	if ex.Endpoint == "" {
		ex.Endpoint = parsed.Header.Meta.Endpoint
	}
	return ex
}

// ascendingTrajectoryEntries mirrors the ordering Build applies, so the
// streaming path consumes traces in the same order the buffered path would.
func ascendingTrajectoryEntries(entries []store.LogEntry) []store.LogEntry {
	ordered := append([]store.LogEntry(nil), entries...)
	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := ordered[i].Header.Meta.Time, ordered[j].Header.Meta.Time
		if left.Equal(right) {
			return ordered[i].ID < ordered[j].ID
		}
		return left.Before(right)
	})
	return ordered
}

func annotateTrajectoryExtra(result *trajectory.Trajectory, total, included, limit int) {
	if result.Extra == nil {
		result.Extra = map[string]any{}
	}
	result.Extra["trace_count"] = total
	result.Extra["included_traces"] = included
	result.Extra["truncated"] = included < total
	if limit > 0 {
		result.Extra["trace_cap"] = limit
	}
}

func trajectoryCache(st *store.Store) *trajectory.Cache {
	if st == nil {
		return nil
	}
	dir := strings.TrimSpace(st.OutputDir())
	if dir == "" {
		return nil
	}
	return trajectory.NewCache(filepath.Join(dir, trajectory.DefaultCacheDirName))
}

func writeTrajectoryBody(w http.ResponseWriter, sessionID string, data []byte, contentLength int, extension string) {
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": trajectoryFilename(sessionID, extension)}))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(contentLength))
	_, _ = w.Write(append(data, '\n'))
}

func trajectoryFilename(sessionID, extension string) string {
	return "session-" + strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, sessionID) + extension
}

func trajectoryBoolParam(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
