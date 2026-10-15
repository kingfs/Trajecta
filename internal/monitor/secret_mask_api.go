// HTTP surface of the credential masking audit.
//
// The event feed says that a rule rewrote a credential and points at the trace;
// this is where the pair itself is read from. The two are separate because the
// feed row is copied to every monitor session and kept as a notification, while
// these values are the credential the whole feature exists to keep off the wire.

package monitor

import (
	"net/http"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
)

// credentialMaskAuditView is the wire shape of one row. The stored column names
// are original_value and stand_in_value; the view shortens them and always
// carries storage_mode, so a reader can tell an encrypted value from a
// plaintext one without asking the configuration endpoint.
type credentialMaskAuditView struct {
	ID          string    `json:"id"`
	TraceID     string    `json:"trace_id"`
	Rule        string    `json:"rule"`
	Vendor      string    `json:"vendor"`
	Kind        string    `json:"kind"`
	Confidence  string    `json:"confidence"`
	Score       int       `json:"score"`
	Reasons     string    `json:"reasons"`
	Start       int       `json:"start"`
	End         int       `json:"end"`
	ValueLength int       `json:"value_length"`
	Original    string    `json:"original"`
	StandIn     string    `json:"stand_in"`
	StorageMode string    `json:"storage_mode"`
	DetectedAt  time.Time `json:"detected_at"`
}

type credentialMaskAuditListResponse struct {
	Items       []credentialMaskAuditView `json:"items"`
	Page        int                       `json:"page"`
	PageSize    int                       `json:"page_size"`
	Total       int                       `json:"total"`
	TotalPages  int                       `json:"total_pages"`
	StorageMode string                    `json:"storage_mode"`
	RefreshedAt time.Time                 `json:"refreshed_at"`
}

func credentialMaskAuditViewOf(item store.CredentialMaskAudit) credentialMaskAuditView {
	return credentialMaskAuditView{
		ID:          item.ID,
		TraceID:     item.TraceID,
		Rule:        item.Rule,
		Vendor:      item.Vendor,
		Kind:        item.Kind,
		Confidence:  item.Confidence,
		Score:       item.Score,
		Reasons:     item.Reasons,
		Start:       item.Start,
		End:         item.End,
		ValueLength: item.ValueLength,
		Original:    item.Original,
		StandIn:     item.StandIn,
		StorageMode: item.StorageMode,
		DetectedAt:  item.DetectedAt,
	}
}

// credentialMaskAuditListAPIHandler serves GET /api/secret-masks.
//
// Filters: trace_id, rule, window (the same vocabulary the event feed uses),
// page and page_size.
func credentialMaskAuditListAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		q := r.URL.Query()
		_, since := parseSystemEventWindow(q.Get("window"))
		filter := store.CredentialMaskAuditFilter{
			TraceID:  strings.TrimSpace(q.Get("trace_id")),
			Rule:     strings.TrimSpace(q.Get("rule")),
			Since:    since,
			Page:     parseInt(q.Get("page"), 1),
			PageSize: parsePageSize(q.Get("page_size")),
		}
		page, err := st.ListCredentialMaskAudits(filter)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "query error: " + err.Error()})
			return
		}
		items := make([]credentialMaskAuditView, 0, len(page.Items))
		for _, item := range page.Items {
			items = append(items, credentialMaskAuditViewOf(item))
		}
		writeJSON(w, http.StatusOK, credentialMaskAuditListResponse{
			Items:       items,
			Page:        page.Page,
			PageSize:    page.PageSize,
			Total:       page.Total,
			TotalPages:  page.TotalPages,
			StorageMode: st.SecretStorageMode(),
			RefreshedAt: time.Now().UTC(),
		})
	}
}

// credentialMaskAuditDetailAPIHandler serves GET /api/secret-masks/{id}.
func credentialMaskAuditDetailAPIHandler(st *store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if st == nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "store not configured"})
			return
		}
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/api/secret-masks/")
		id = strings.Trim(strings.TrimSpace(id), "/")
		if id == "" {
			http.NotFound(w, r)
			return
		}
		item, err := st.GetCredentialMaskAudit(id)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		writeJSON(w, http.StatusOK, credentialMaskAuditViewOf(item))
	}
}
