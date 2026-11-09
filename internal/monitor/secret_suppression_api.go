// HTTP surface of the secret-mask suppression list.
//
// GET    /api/secret-suppressions       list the false-positive entries
// POST   /api/secret-suppressions       add one entry
// DELETE /api/secret-suppressions/{id}  remove one entry
//
// A suppression is policy, not an audit row: while it exists the Vault skips the
// matching rule/value before rendering a stand-in, so no mapping, mask event or
// credential-mask audit row is produced. Removing it re-enables the rule on the
// next request. The list keeps the rule, the exact value and an operator note so
// the detector can be tuned later against what was suppressed.
package monitor

import (
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/pkg/secretmask"
)

type secretSuppressionView struct {
	ID          uint64    `json:"id"`
	Rule        string    `json:"rule"`
	Real        string    `json:"real"`
	Reason      string    `json:"reason,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	DecodeError string    `json:"decode_error,omitempty"`
}

type secretSuppressionListResponse struct {
	Items      []secretSuppressionView `json:"items"`
	Page       int                     `json:"page"`
	PageSize   int                     `json:"page_size"`
	Total      int                     `json:"total"`
	TotalPages int                     `json:"total_pages"`
	Available  bool                    `json:"available"`
	Reason     string                  `json:"reason,omitempty"`
	// Rules is the maskable rule catalogue for the add form. Report-only access
	// indicators are excluded: they are never rewritten, so suppressing one
	// could not change anything.
	Rules       []string  `json:"rules"`
	RefreshedAt time.Time `json:"refreshed_at"`
}

type secretSuppressionCreateRequest struct {
	Rule   string `json:"rule"`
	Real   string `json:"real"`
	Reason string `json:"reason"`
}

func secretSuppressionViewOf(row secretmask.Suppression) secretSuppressionView {
	return secretSuppressionView{
		ID:          row.ID,
		Rule:        row.Rule,
		Real:        row.Real,
		Reason:      row.Reason,
		CreatedAt:   row.CreatedAt,
		DecodeError: row.DecodeError,
	}
}

func maskableRuleNames() []string {
	rules := secretmask.Rules()
	names := make([]string, 0, len(rules))
	for _, rule := range rules {
		if rule.ReportOnly {
			continue
		}
		names = append(names, rule.Name)
	}
	sort.Strings(names)
	return names
}

func secretSuppressionMatches(row secretmask.Suppression, query string) bool {
	if query == "" {
		return true
	}
	if strings.Contains(row.Real, query) {
		return true
	}
	lower := strings.ToLower(query)
	return strings.Contains(strings.ToLower(row.Rule), lower) ||
		strings.Contains(strings.ToLower(row.Reason), lower)
}

// secretSuppressionAPIHandler dispatches GET (list) and POST (add) on the
// collection path.
func secretSuppressionAPIHandler(admin SecretSuppressionAdmin) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			secretSuppressionListAPIHandler(admin, w, r)
		case http.MethodPost:
			secretSuppressionCreateAPIHandler(admin, w, r)
		default:
			http.NotFound(w, r)
		}
	}
}

func secretSuppressionListAPIHandler(admin SecretSuppressionAdmin, w http.ResponseWriter, r *http.Request) {
	page := parseInt(r.URL.Query().Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := parsePageSize(r.URL.Query().Get("page_size"))
	query := strings.TrimSpace(r.URL.Query().Get("q"))

	response := secretSuppressionListResponse{
		Items:       []secretSuppressionView{},
		Page:        page,
		PageSize:    pageSize,
		TotalPages:  1,
		Available:   admin != nil,
		RefreshedAt: time.Now().UTC(),
	}
	if admin == nil {
		response.Reason = secretMappingUnavailableReason
		writeJSON(w, http.StatusOK, response)
		return
	}
	response.Rules = maskableRuleNames()

	matches := make([]secretSuppressionView, 0, 32)
	if err := admin.ListSuppressions(true, func(row secretmask.Suppression) error {
		if secretSuppressionMatches(row, query) {
			matches = append(matches, secretSuppressionViewOf(row))
		}
		return nil
	}); err != nil {
		if errors.Is(err, secretmask.ErrMappingAdminUnavailable) {
			response.Available = false
			response.Reason = secretMappingUnavailableReason
			response.Rules = nil
			writeJSON(w, http.StatusOK, response)
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list secret suppressions: " + err.Error()})
		return
	}

	response.Total = len(matches)
	response.TotalPages = (response.Total + pageSize - 1) / pageSize
	if response.TotalPages < 1 {
		response.TotalPages = 1
	}
	if page > response.TotalPages {
		page = response.TotalPages
		response.Page = page
	}
	start := (page - 1) * pageSize
	end := start + pageSize
	if start > len(matches) {
		start = len(matches)
	}
	if end > len(matches) {
		end = len(matches)
	}
	response.Items = matches[start:end]
	writeJSON(w, http.StatusOK, response)
}

func secretSuppressionCreateAPIHandler(admin SecretSuppressionAdmin, w http.ResponseWriter, r *http.Request) {
	if admin == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "secret mapping store is not available"})
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var request secretSuppressionCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid suppression body: " + err.Error()})
		return
	}
	row, err := admin.AddSuppression(request.Rule, request.Real, request.Reason)
	if err != nil {
		switch {
		case errors.Is(err, secretmask.ErrInvalidSuppression):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		case errors.Is(err, secretmask.ErrMappingAdminUnavailable):
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "add secret suppression: " + err.Error()})
		}
		return
	}
	writeJSON(w, http.StatusOK, secretSuppressionViewOf(row))
}

func secretSuppressionDeleteAPIHandler(admin SecretSuppressionAdmin) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.NotFound(w, r)
			return
		}
		if admin == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "secret mapping store is not available"})
			return
		}
		raw := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/secret-suppressions/"), "/")
		id, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || id == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid secret suppression id"})
			return
		}
		removed, found, err := admin.DeleteSuppression(id)
		if err != nil {
			if errors.Is(err, secretmask.ErrMappingAdminUnavailable) {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "delete secret suppression: " + err.Error()})
			return
		}
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "secret suppression not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"deleted": true,
			"id":      removed.ID,
		})
	}
}
