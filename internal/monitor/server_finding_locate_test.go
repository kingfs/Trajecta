package monitor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/internal/observeworker"
	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/llm"
	"github.com/kingfs/Trajecta/pkg/observe"
	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// TestTraceFindingLocateAPI pins the audit-side locate read: a finding names a
// node, the endpoint recomputes the exact bytes from the current parse (the
// stored excerpt is redacted for personal data), and the answer carries both the
// value to highlight and the conversation step to open. A category with no
// single span still answers with the anchor.
func TestTraceFindingLocateAPI(t *testing.T) {
	const identityNumber = "11010519491231002X"
	outputDir := t.TempDir()
	content := buildRecordFixtureWithStatusHeadersAndMutator(t, "/v1/chat/completions", false, "200 OK", nil,
		`{"messages":[{"role":"user","content":"我的身份证号是 `+identityNumber+`，请帮我核对。"}]}`,
		`{"choices":[{"message":{"content":"ok"}}]}`,
		func(header *recordfile.RecordHeader) {
			header.Meta.Provider = llm.ProviderOpenAICompatible
			header.Meta.Operation = llm.OperationChatCompletions
			header.Meta.Endpoint = "/v1/chat/completions"
		})
	writeTraceFixture(t, outputDir, "finding-locate.http", content)

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

	observation, err := observeworker.ReparseTrace(context.Background(), st, nil, traceID)
	if err != nil {
		t.Fatalf("ReparseTrace() error = %v", err)
	}
	var nodeID string
	for _, node := range observe.TraceNodes(observation) {
		if strings.Contains(node.Text, identityNumber) {
			nodeID = node.ID
			break
		}
	}
	if nodeID == "" {
		t.Fatalf("fixture parse has no node carrying the identity number: %+v", observe.TraceNodes(observation))
	}
	findings := []observe.Finding{
		{
			ID:              "finding_pii",
			TraceID:         traceID,
			Category:        "pii_leak",
			Severity:        observe.SeverityHigh,
			Confidence:      0.95,
			Title:           "Personal data: national_id",
			EvidenceExcerpt: "我的身份证号是 [redacted]，请帮我核对。",
			EvidencePath:    observe.EvidencePath(traceID, observe.SemanticNode{ID: nodeID}),
			NodeID:          nodeID,
			Detector:        "pii",
			DetectorVersion: "0.1.0",
		},
		{
			ID:              "finding_error",
			TraceID:         traceID,
			Category:        "tool_result_error",
			Severity:        observe.SeverityMedium,
			Confidence:      0.9,
			Title:           "Tool result error",
			EvidenceExcerpt: "error",
			EvidencePath:    observe.EvidencePath(traceID, observe.SemanticNode{ID: nodeID}),
			NodeID:          nodeID,
			Detector:        "tool_error",
			DetectorVersion: "0.1.0",
		},
	}
	if err := st.SaveFindings(traceID, findings); err != nil {
		t.Fatalf("SaveFindings() error = %v", err)
	}

	locate := func(findingID string) (findingLocateResponse, int) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/traces/"+traceID+"/findings/"+findingID+"/locate", nil)
		rr := httptest.NewRecorder()
		traceAPIHandler(st, nil).ServeHTTP(rr, req)
		var payload findingLocateResponse
		if rr.Code == http.StatusOK {
			if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode locate: %v", err)
			}
		}
		return payload, rr.Code
	}

	located, code := locate("finding_pii")
	if code != http.StatusOK {
		t.Fatalf("locate status = %d", code)
	}
	if located.Value != identityNumber {
		t.Errorf("located value = %q, want the identity number", located.Value)
	}
	if located.Anchor == "" || located.NodeID != nodeID || located.NodePath == "" {
		t.Errorf("located = %+v, want the node and its conversation anchor", located)
	}
	if located.View != "text" || located.End <= located.Start {
		t.Errorf("located span = %+v, want a text span", located)
	}
	if !strings.Contains(strings.ToLower(located.NodePath), "messages") {
		t.Errorf("node path = %q, want the message path", located.NodePath)
	}

	// A whole-node finding has no span to highlight, but it still locates.
	errorLocated, code := locate("finding_error")
	if code != http.StatusOK {
		t.Fatalf("error finding locate status = %d", code)
	}
	if errorLocated.Value != "" {
		t.Errorf("error finding value = %q, want none", errorLocated.Value)
	}
	if errorLocated.Anchor == "" {
		t.Errorf("error finding = %+v, want an anchor", errorLocated)
	}

	if _, code := locate("finding_missing"); code != http.StatusNotFound {
		t.Errorf("missing finding status = %d, want 404", code)
	}
}
