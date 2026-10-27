package monitor

import (
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/kingfs/Trajecta/internal/observeworker"
	"github.com/kingfs/Trajecta/internal/resources"
	"github.com/kingfs/Trajecta/internal/store"
)

// maskOccurrenceView is one place a masked original value appears in the
// conversation, addressed by the step that carries it.
type maskOccurrenceView struct {
	NodeID   string `json:"node_id"`
	NodePath string `json:"node_path"`
	Anchor   string `json:"anchor"`
	View     string `json:"view"`
	Start    int    `json:"start"`
	End      int    `json:"end"`
}

type maskLocateResponse struct {
	ID     string               `json:"id"`
	MaskID string               `json:"mask_id"`
	Anchor string               `json:"anchor"`
	Items  []maskOccurrenceView `json:"items"`
}

// handleTraceMaskLocate answers "where did the credential that was replaced come
// from". The audit row holds the original bytes, but no node reference, so the
// value is searched literally in the current parse and every occurrence is
// returned; the conversation can then open the first one and mark all of them.
//
// The response never repeats the value: the caller already has it from the
// credential-mask list, and keeping it out of this read keeps the endpoint from
// becoming another plaintext transport for a secret.
func handleTraceMaskLocate(w http.ResponseWriter, r *http.Request, st *store.Store, entry store.LogEntry, maskID string) {
	mask, err := st.GetCredentialMaskAudit(maskID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "credential mask not found"})
		return
	}
	if mask.TraceID != entry.ID {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "credential mask does not belong to this trace"})
		return
	}
	if strings.TrimSpace(mask.Original) == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "credential mask original value is unavailable"})
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
	occurrences := resources.LocateValue(observation, mask.Original)
	anchors := conversationAnchors(observation)
	items := make([]maskOccurrenceView, 0, len(occurrences))
	for _, occurrence := range occurrences {
		anchor := anchors[occurrence.NodeID]
		if anchor == "" {
			anchor = occurrence.NodeID
		}
		items = append(items, maskOccurrenceView{
			NodeID:   occurrence.NodeID,
			NodePath: occurrence.NodePath,
			Anchor:   anchor,
			View:     occurrence.View,
			Start:    occurrence.Start,
			End:      occurrence.End,
		})
	}
	payload := maskLocateResponse{
		ID:     entry.ID,
		MaskID: mask.ID,
		Items:  items,
	}
	// Prefer a view the conversation actually renders for the step it scrolls
	// to; a raw-JSON-only hit still locates, but the marked text would be
	// invisible there.
	for _, item := range items {
		if item.Anchor == "" {
			continue
		}
		if item.View == "text" || item.View == "arguments" {
			payload.Anchor = item.Anchor
			break
		}
	}
	if payload.Anchor == "" && len(items) > 0 {
		payload.Anchor = items[0].Anchor
	}
	writeJSON(w, http.StatusOK, payload)
}
