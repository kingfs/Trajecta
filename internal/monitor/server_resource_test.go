package monitor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/llm"
	"github.com/kingfs/Trajecta/pkg/recordfile"
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

// TestTraceResourceLocateAPI pins the locate read the resource tree uses: the
// value is found in the current parse of the cassette, and the answer names a
// conversation step the reader can open. The index itself is not consulted, so
// an old row is still locatable, and a value the cassette no longer carries
// answers with no anchor instead of a guess.
func TestTraceResourceLocateAPI(t *testing.T) {
	outputDir := t.TempDir()
	content := buildRecordFixtureWithStatusHeadersAndMutator(t, "/v1/chat/completions", false, "200 OK", nil,
		`{"messages":[{"role":"user","content":"read the project"}]}`,
		`{"choices":[{"message":{"content":"done","tool_calls":[{"id":"call_1","type":"function","function":{"name":"read","arguments":"{\"file_path\":\"/home/alice/project/main.go\"}"}},{"id":"call_2","type":"function","function":{"name":"web_fetch","arguments":"{\"url\":\"https://api.example.com/x\"}"}}]}}]}`,
		func(header *recordfile.RecordHeader) {
			header.Meta.Provider = llm.ProviderOpenAICompatible
			header.Meta.Operation = llm.OperationChatCompletions
			header.Meta.Endpoint = "/v1/chat/completions"
		})
	writeTraceFixture(t, outputDir, "locate.http", content)

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

	locate := func(kind, value string) (resourceLocateResponse, int) {
		t.Helper()
		target := "/api/traces/" + traceID + "/resources/locate?kind=" + url.QueryEscape(kind) + "&value=" + url.QueryEscape(value)
		req := httptest.NewRequest(http.MethodGet, target, nil)
		rr := httptest.NewRecorder()
		traceAPIHandler(st, nil).ServeHTTP(rr, req)
		var payload resourceLocateResponse
		if rr.Code == http.StatusOK {
			if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
				t.Fatalf("decode locate: %v", err)
			}
		}
		return payload, rr.Code
	}

	located, code := locate("local_path", "/home/alice/project/main.go")
	if code != http.StatusOK {
		t.Fatalf("locate status = %d", code)
	}
	if located.Anchor == "" || len(located.Items) == 0 {
		t.Fatalf("locate = %+v, want an anchor and at least one occurrence", located)
	}
	if located.Items[0].NodeID == "" || located.Items[0].NodePath == "" {
		t.Errorf("occurrence = %+v, want the node and its path", located.Items[0])
	}
	if located.Items[0].Anchor != located.Anchor {
		t.Errorf("response anchor = %q, want the first occurrence anchor %q", located.Anchor, located.Items[0].Anchor)
	}
	if located.Items[0].End <= located.Items[0].Start {
		t.Errorf("occurrence span = %+v, want a non-empty span", located.Items[0])
	}

	urlLocated, code := locate("url", "https://api.example.com/x")
	if code != http.StatusOK || urlLocated.Anchor == "" {
		t.Fatalf("url locate = %+v, status = %d", urlLocated, code)
	}

	missing, code := locate("local_path", "/not/in/this/trace.go")
	if code != http.StatusOK || missing.Anchor != "" || len(missing.Items) != 0 {
		t.Fatalf("missing locate = %+v, status = %d", missing, code)
	}

	if _, code := locate("other", "x"); code != http.StatusBadRequest {
		t.Errorf("bad kind status = %d, want 400", code)
	}
}
