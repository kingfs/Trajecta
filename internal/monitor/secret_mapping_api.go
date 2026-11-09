// HTTP surface of the live secret-mask mapping file.
//
// GET  /api/secret-mappings       list the global real-to-stand-in pairs
// DELETE /api/secret-mappings/{seq}  remove one pair and forget it in memory
//
// This is deliberately separate from /api/secret-masks. That endpoint reads the
// per-trace audit table: it answers "what stood in for what in this exchange".
// This one reads the bbolt store the Vault consults before rendering a fake, so
// it answers "which values does this installation already know, and which fake
// did it assign them". A pair can outlive every trace that used it and can exist
// for a value whose trace has been pruned, which is why the audit table is not
// enough to find a false positive.
package monitor

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/pkg/secretmask"
)

const secretMappingUnavailableReason = "mapping_store_disabled"

// secretMappingView is one pair as the page renders it. The values are the
// point of the page - a reader has to recognize a false positive - and the
// lengths make the format-preserving guarantee visible: every fake is exactly as
// long as the real value it replaces.
type secretMappingView struct {
	Seq         uint64 `json:"seq"`
	Real        string `json:"real"`
	Fake        string `json:"fake"`
	RealLength  int    `json:"real_length"`
	FakeLength  int    `json:"fake_length"`
	Encrypted   bool   `json:"encrypted"`
	DecodeError string `json:"decode_error,omitempty"`
}

type secretMappingStoreView struct {
	Path           string `json:"path"`
	FileSizeBytes  int64  `json:"file_size_bytes"`
	EntryCount     int    `json:"entry_count"`
	EncryptedRows  int    `json:"encrypted_rows"`
	PlaintextRows  int    `json:"plaintext_rows"`
	UnreadableRows int    `json:"unreadable_rows"`
}

type secretMappingListResponse struct {
	Items       []secretMappingView     `json:"items"`
	Page        int                     `json:"page"`
	PageSize    int                     `json:"page_size"`
	Total       int                     `json:"total"`
	TotalPages  int                     `json:"total_pages"`
	Available   bool                    `json:"available"`
	Reason      string                  `json:"reason,omitempty"`
	Store       *secretMappingStoreView `json:"store,omitempty"`
	RefreshedAt time.Time               `json:"refreshed_at"`
}

func secretMappingViewOf(row secretmask.MappingRow) secretMappingView {
	return secretMappingView{
		Seq:         row.Seq,
		Real:        row.Real,
		Fake:        row.Fake,
		RealLength:  len(row.Real),
		FakeLength:  len(row.Fake),
		Encrypted:   row.Encrypted,
		DecodeError: row.DecodeError,
	}
}

// secretMappingListAPIHandler serves GET /api/secret-mappings.
//
// Filters: q (a substring of the real or fake value) and page/page_size. The
// listing is newest first, which is the insertion order reversed, because a
// value that was just recognized is the one an operator is most likely to
// inspect.
//
// The row set is the whole file, not the Vault's loaded window: a positive
// mapping_store.load_limit can leave older pairs on disk and the page is meant
// to show the global mapping. Decryption happens here through the same store the
// proxy uses, so an unreadable row is reported rather than hidden.
func secretMappingListAPIHandler(admin SecretMappingAdmin) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.NotFound(w, r)
			return
		}
		page := parseInt(r.URL.Query().Get("page"), 1)
		if page < 1 {
			page = 1
		}
		pageSize := parsePageSize(r.URL.Query().Get("page_size"))
		query := strings.TrimSpace(r.URL.Query().Get("q"))

		response := secretMappingListResponse{
			Items:       []secretMappingView{},
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
		// New pairs are appended asynchronously. Flush first so a page opened
		// immediately after a request shows the pair that request rendered.
		if err := admin.FlushMappings(); err != nil {
			if errors.Is(err, secretmask.ErrMappingAdminUnavailable) {
				response.Available = false
				response.Reason = secretMappingUnavailableReason
				writeJSON(w, http.StatusOK, response)
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "flush secret mappings: " + err.Error()})
			return
		}

		stats, err := admin.MappingStats()
		if err != nil {
			if errors.Is(err, secretmask.ErrMappingAdminUnavailable) {
				response.Available = false
				response.Reason = secretMappingUnavailableReason
				writeJSON(w, http.StatusOK, response)
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "read secret mapping stats: " + err.Error()})
			return
		}
		response.Store = &secretMappingStoreView{
			Path:           stats.Path,
			FileSizeBytes:  stats.FileSizeBytes,
			EntryCount:     stats.EntryCount,
			EncryptedRows:  stats.EncryptedRows,
			PlaintextRows:  stats.PlaintextRows,
			UnreadableRows: stats.UnreadableRows,
		}

		matches := make([]secretMappingView, 0, 64)
		err = admin.ListMappings(true, func(row secretmask.MappingRow) error {
			if query != "" &&
				!strings.Contains(row.Real, query) &&
				!strings.Contains(row.Fake, query) {
				return nil
			}
			matches = append(matches, secretMappingViewOf(row))
			return nil
		})
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "list secret mappings: " + err.Error()})
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
}

// secretMappingDeleteAPIHandler serves DELETE /api/secret-mappings/{seq}.
//
// The sequence identifies the row, so the request never has to carry the real
// value back in a URL. The Vault deletes the durable row and forgets the pair in
// memory; see secretmask.Vault.DeleteMappingBySeq. Deleting a mapping is not a
// suppression rule: the next request carrying the same value can render and
// record a new pair, which is the honest behaviour for a false positive whose
// detector was not changed.
func secretMappingDeleteAPIHandler(admin SecretMappingAdmin) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			http.NotFound(w, r)
			return
		}
		if admin == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "secret mapping store is not available"})
			return
		}
		raw := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/secret-mappings/"), "/")
		seq, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || seq == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid secret mapping sequence"})
			return
		}
		row, found, err := admin.DeleteMappingBySeq(seq)
		if err != nil {
			if errors.Is(err, secretmask.ErrMappingAdminUnavailable) {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "delete secret mapping: " + err.Error()})
			return
		}
		if !found {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "secret mapping not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"deleted": true,
			"seq":     row.Seq,
		})
	}
}
