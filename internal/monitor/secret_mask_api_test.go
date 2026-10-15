package monitor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
)

// TestCredentialMaskAuditAPIHandler pins the read surface the Monitor pages use
// to answer "what left, and what stood in for it". The event feed deliberately
// carries no credential bytes, so this endpoint is the only place a reader can
// compare the pair, and both values plus the offsets have to survive the trip.
//
// event_index is asserted too: the audit list for one trace is ordered by it in
// the UI, and the table's own ORDER BY cannot order the replacements of a single
// exchange because they share a detection time and carry random uuids.
func TestCredentialMaskAuditAPIHandler(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	const (
		traceID  = "trace-monitor-mask"
		original = "sk-proj-AAAABBBBCCCCDDDDEEEEFFFFGGGGHHHHIIIIJJJJT3BlbkFJKKKKLLLLMMMMNNNNOOOOPPPP"
		standIn  = "sk-proj-XXXXYYYYZZZZAAAABBBBCCCCDDDDEEEEFFFFT3BlbkFJGGGGHHHHIIIIJJJJKKKKLLLL"
	)
	detectedAt := time.Date(2026, 10, 15, 8, 0, 0, 0, time.UTC)
	if err := st.UpsertCredentialMaskAudits([]store.CredentialMaskAuditRecord{
		{
			TraceID:    traceID,
			EventIndex: 1,
			Rule:       "google.api_key",
			Vendor:     "Google",
			Kind:       "api_key",
			Confidence: "high",
			Score:      90,
			Reasons:    "strong_shape",
			Start:      300,
			End:        339,
			Original:   "AIzaSyB1cD2eF3gH4iJ5kL6mN7oP8qR9sT0uV1wX",
			StandIn:    "AIzaSyZ9yX8wV7uT6sR5qP4oN3mL2kJ1iH0gF9eD",
			DetectedAt: detectedAt,
		},
		{
			TraceID:    traceID,
			EventIndex: 0,
			Rule:       "openai.project_key",
			Vendor:     "OpenAI",
			Kind:       "api_key",
			Confidence: "high",
			Score:      100,
			Reasons:    "strong_shape,context",
			Start:      148,
			End:        148 + len(original),
			Original:   original,
			StandIn:    standIn,
			DetectedAt: detectedAt,
		},
	}); err != nil {
		t.Fatalf("UpsertCredentialMaskAudits() error = %v", err)
	}

	mux := http.NewServeMux()
	RegisterRoutes(mux, st)

	req := httptest.NewRequest(http.MethodGet, "/api/secret-masks?trace_id="+traceID, nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var list credentialMaskAuditListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if list.Total != 2 || len(list.Items) != 2 {
		t.Fatalf("total = %d, items = %d, want 2 each", list.Total, len(list.Items))
	}
	if list.StorageMode == "" {
		t.Error("storage_mode is empty, so a reader cannot tell an encrypted value from a plaintext one")
	}

	byIndex := make(map[int]credentialMaskAuditView, len(list.Items))
	for _, item := range list.Items {
		if item.TraceID != traceID {
			t.Errorf("trace_id = %q, want %q", item.TraceID, traceID)
		}
		byIndex[item.EventIndex] = item
	}
	first, ok := byIndex[0]
	if !ok {
		t.Fatalf("event_index 0 missing from %+v", list.Items)
	}
	if first.Original != original {
		t.Errorf("original = %q, want the value that was masked", first.Original)
	}
	if first.StandIn != standIn {
		t.Errorf("stand_in = %q, want the value that left the machine", first.StandIn)
	}
	if first.Original == first.StandIn {
		t.Error("the pair is identical, so nothing shows what stood in for the credential")
	}
	if first.Rule != "openai.project_key" || first.Vendor != "OpenAI" {
		t.Errorf("rule/vendor = %q/%q, want openai.project_key/OpenAI", first.Rule, first.Vendor)
	}
	if first.Start != 148 || first.ValueLength != len(original) {
		t.Errorf("start/value_length = %d/%d, want 148/%d", first.Start, first.ValueLength, len(original))
	}
	if second, ok := byIndex[1]; !ok || second.Rule != "google.api_key" {
		t.Errorf("event_index 1 = %+v, want the google.api_key row", second)
	}

	// The rule filter scopes the pair to the notification that was clicked, and
	// the detail endpoint serves the same shape by id.
	req = httptest.NewRequest(http.MethodGet, "/api/secret-masks?trace_id="+traceID+"&rule=google.api_key", nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("filtered status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var filtered credentialMaskAuditListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &filtered); err != nil {
		t.Fatalf("decode filtered response: %v", err)
	}
	if filtered.Total != 1 || len(filtered.Items) != 1 || filtered.Items[0].Rule != "google.api_key" {
		t.Fatalf("filtered rows = %+v, want the single google.api_key row", filtered.Items)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/secret-masks/"+first.ID, nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("detail status = %d, want 200; body=%s", rr.Code, rr.Body.String())
	}
	var detail credentialMaskAuditView
	if err := json.Unmarshal(rr.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail response: %v", err)
	}
	if detail.ID != first.ID || detail.Original != original || detail.StandIn != standIn {
		t.Errorf("detail = %+v, want the row %s with both values", detail, first.ID)
	}
}
