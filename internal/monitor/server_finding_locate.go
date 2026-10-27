package monitor

import (
	"errors"
	"net/http"
	"os"

	"github.com/kingfs/Trajecta/internal/analyzer"
	"github.com/kingfs/Trajecta/internal/observeworker"
	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/observe"
)

// findingLocateResponse answers "which bytes did this finding see".
//
// The findings table stores a redacted excerpt (personal data is never repeated
// there) and no match column, so the value is recomputed from the current parse
// and returned for one authenticated read. A finding whose category has no
// single span - a safety block, a tool error - still gets the conversation
// anchor, with an empty value, so the existing jump keeps working.
type findingLocateResponse struct {
	ID        string `json:"id"`
	FindingID string `json:"finding_id"`
	Anchor    string `json:"anchor"`
	Value     string `json:"value,omitempty"`
	NodeID    string `json:"node_id,omitempty"`
	NodePath  string `json:"node_path,omitempty"`
	View      string `json:"view,omitempty"`
	Start     int    `json:"start,omitempty"`
	End       int    `json:"end,omitempty"`
}

func handleTraceFindingLocate(w http.ResponseWriter, r *http.Request, st *store.Store, entry store.LogEntry, findingID string) {
	findings, err := st.ListFindings(entry.ID, store.FindingFilter{})
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query findings: " + err.Error()})
		return
	}
	var finding observe.Finding
	found := false
	for _, item := range findings {
		if item.ID == findingID {
			finding = item
			found = true
			break
		}
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "finding not found"})
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
	payload := findingLocateResponse{
		ID:        entry.ID,
		FindingID: finding.ID,
		NodeID:    finding.NodeID,
	}
	if match, ok := analyzer.LocateFinding(observation, finding); ok {
		payload.Value = match.Text
		payload.NodeID = match.NodeID
		payload.NodePath = match.NodePath
		payload.View = match.View
		payload.Start = match.Start
		payload.End = match.End
	}
	anchors := conversationAnchors(observation)
	payload.Anchor = anchors[payload.NodeID]
	if payload.Anchor == "" {
		payload.Anchor = payload.NodeID
	}
	writeJSON(w, http.StatusOK, payload)
}
