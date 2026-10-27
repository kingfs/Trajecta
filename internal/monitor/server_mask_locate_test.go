package monitor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/llm"
	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// TestTraceMaskLocateAPI pins the audit-side locate for a replaced credential:
// the row names the original bytes and the endpoint returns every place those
// bytes occur in the current parse, addressed by a conversation step, without
// repeating the secret in the response.
func TestTraceMaskLocateAPI(t *testing.T) {
	key := "sk-" + strings.Repeat("A", 20) + "T3BlbkFJ" + strings.Repeat("B", 20)
	outputDir := t.TempDir()
	content := buildRecordFixtureWithStatusHeadersAndMutator(t, "/v1/chat/completions", false, "200 OK", nil,
		`{"messages":[{"role":"user","content":"use `+key+` for this call"}]}`,
		`{"choices":[{"message":{"content":"ok"}}]}`,
		func(header *recordfile.RecordHeader) {
			header.Meta.Provider = llm.ProviderOpenAICompatible
			header.Meta.Operation = llm.OperationChatCompletions
			header.Meta.Endpoint = "/v1/chat/completions"
		})
	writeTraceFixture(t, outputDir, "mask-locate.http", content)

	st, err := store.New(outputDir)
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()
	syncStore(t, st)
	traces, err := st.ListRecent(10)
	if err != nil || len(traces) == 0 {
		t.Fatalf("ListRecent() = %v, %v", traces, err)
	}
	traceID := traces[0].ID

	if err := st.UpsertCredentialMaskAudits([]store.CredentialMaskAuditRecord{{
		TraceID:    traceID,
		EventIndex: 1,
		Rule:       "openai.legacy_key",
		Kind:       "api_key",
		Confidence: "high",
		Score:      90,
		Reasons:    "strong_shape",
		Start:      64,
		End:        64 + len(key),
		Original:   key,
		StandIn:    key,
		DetectedAt: time.Now().UTC(),
	}}); err != nil {
		t.Fatalf("UpsertCredentialMaskAudits() error = %v", err)
	}
	page, err := st.ListCredentialMaskAudits(store.CredentialMaskAuditFilter{TraceID: traceID})
	if err != nil || len(page.Items) == 0 {
		t.Fatalf("ListCredentialMaskAudits() = %v, %v", page, err)
	}
	maskID := page.Items[0].ID

	req := httptest.NewRequest(http.MethodGet, "/api/traces/"+traceID+"/secret-masks/"+maskID+"/locate", nil)
	rr := httptest.NewRecorder()
	traceAPIHandler(st, nil).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("mask locate status = %d, body=%s", rr.Code, rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), key) {
		t.Fatalf("mask locate response repeats the secret")
	}
	var payload maskLocateResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode mask locate: %v", err)
	}
	if payload.Anchor == "" || len(payload.Items) == 0 {
		t.Fatalf("mask locate = %+v, want an anchor and at least one occurrence", payload)
	}
	for _, item := range payload.Items {
		if item.NodeID == "" || item.NodePath == "" || item.Anchor == "" {
			t.Errorf("occurrence = %+v, want the node, its path and the step anchor", item)
		}
		if item.End <= item.Start {
			t.Errorf("occurrence span = %+v, want a non-empty span", item)
		}
	}

	otherTrace := httptest.NewRequest(http.MethodGet, "/api/traces/other-trace/secret-masks/"+maskID+"/locate", nil)
	otherRR := httptest.NewRecorder()
	traceAPIHandler(st, nil).ServeHTTP(otherRR, otherTrace)
	if otherRR.Code == http.StatusOK {
		t.Errorf("a mask from another trace must not locate: %s", otherRR.Body.String())
	}

	missing := httptest.NewRequest(http.MethodGet, "/api/traces/"+traceID+"/secret-masks/missing/locate", nil)
	missingRR := httptest.NewRecorder()
	traceAPIHandler(st, nil).ServeHTTP(missingRR, missing)
	if missingRR.Code != http.StatusNotFound {
		t.Errorf("missing mask status = %d, want 404", missingRR.Code)
	}
}
