package monitor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kingfs/Trajecta/internal/store"
)

// TestTraceResourcesAndCatalogueAPI pins both read surfaces: the per-trace list
// the detail tab renders and the catalogue page that deduplicates a value
// across traces.
func TestTraceResourcesAndCatalogueAPI(t *testing.T) {
	outputDir := t.TempDir()
	content := buildRecordFixture(t, "/v1/chat/completions", false,
		`{"messages":[{"role":"user","content":"read /home/alice/project/main.go and call https://api.example.com/x"}]}`,
		`{"choices":[{"message":{"content":"done"}}]}`)
	writeTraceFixture(t, outputDir, "resources.http", content)

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
	if err := st.SaveTraceResources(traceID, []store.ResourceAccess{
		{Kind: "url", Value: "https://api.example.com/x", Count: 2, Position: 0},
		{Kind: "local_path", Value: "/home/alice/project/main.go", Count: 1, Position: 1},
	}); err != nil {
		t.Fatalf("SaveTraceResources() error = %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/traces/"+traceID+"/resources", nil)
	rr := httptest.NewRecorder()
	traceAPIHandler(st, nil).ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("trace resources status = %d, body=%s", rr.Code, rr.Body.String())
	}
	var tracePayload traceResourceListView
	if err := json.Unmarshal(rr.Body.Bytes(), &tracePayload); err != nil {
		t.Fatalf("decode trace resources: %v", err)
	}
	if tracePayload.Total != 2 || len(tracePayload.Items) != 2 {
		t.Fatalf("trace resources = %+v, want 2", tracePayload)
	}
	if tracePayload.Items[0].Value != "https://api.example.com/x" || tracePayload.Items[0].AccessCount != 2 {
		t.Fatalf("first resource = %+v", tracePayload.Items[0])
	}

	// The detail response carries the count the tab badge uses.
	detailReq := httptest.NewRequest(http.MethodGet, "/api/traces/"+traceID, nil)
	detailRR := httptest.NewRecorder()
	traceAPIHandler(st, nil).ServeHTTP(detailRR, detailReq)
	if detailRR.Code != http.StatusOK {
		t.Fatalf("trace detail status = %d, body=%s", detailRR.Code, detailRR.Body.String())
	}
	var detail detailResponse
	if err := json.Unmarshal(detailRR.Body.Bytes(), &detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.Resources != 2 {
		t.Errorf("detail resources = %d, want 2", detail.Resources)
	}

	catReq := httptest.NewRequest(http.MethodGet, "/api/resources?kind=url&q=example", nil)
	catRR := httptest.NewRecorder()
	resourcesAPIHandler(st).ServeHTTP(catRR, catReq)
	if catRR.Code != http.StatusOK {
		t.Fatalf("catalogue status = %d, body=%s", catRR.Code, catRR.Body.String())
	}
	var catalogue resourceListResponse
	if err := json.Unmarshal(catRR.Body.Bytes(), &catalogue); err != nil {
		t.Fatalf("decode catalogue: %v", err)
	}
	if catalogue.Total != 1 || catalogue.Items[0].Value != "https://api.example.com/x" {
		t.Fatalf("catalogue = %+v, want the URL only", catalogue)
	}
	if catalogue.Items[0].TraceCount != 1 || catalogue.Items[0].LastTraceID != traceID {
		t.Errorf("catalogue aggregate = %+v", catalogue.Items[0])
	}
	if catalogue.Window != "all" {
		t.Errorf("default window = %q, want all", catalogue.Window)
	}
}
