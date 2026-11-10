package monitor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/recordfile"
)

func TestTraceFindingsMergesXIRDetections(t *testing.T) {
	outputDir := t.TempDir()
	st, err := store.New(outputDir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	reqBody := `{"model":"gpt-5.1","input":"key sk-abcdefghijklmnopqrstuvwxyz123456"}`
	resBody := `{"id":"resp_1","object":"response","status":"completed","model":"gpt-5.1","output":[]}`
	header := buildRecordHeader("/v1/responses", false, reqBody, resBody)
	header.Meta.RequestID = "req-xir-findings"
	header.Meta.Provider = "openai_compatible"
	header.Meta.Operation = "responses"
	header.Meta.Endpoint = "/v1/responses"
	header.Meta.Model = "gpt-5.1"
	prelude, err := recordfile.MarshalPrelude(header, recordfile.BuildEvents(header))
	if err != nil {
		t.Fatal(err)
	}
	reqHead := "POST /v1/responses HTTP/1.1\r\nHost: example.com\r\nContent-Type: application/json\r\n\r\n"
	resHead := "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n"
	logPath := filepath.Join(outputDir, "trace-xir-findings.http")
	if err := os.WriteFile(logPath, []byte(string(prelude)+reqHead+reqBody+"\n"+resHead+resBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertLog(logPath, header); err != nil {
		t.Fatal(err)
	}
	entry, err := st.GetByRequestID("req-xir-findings")
	if err != nil {
		t.Fatal(err)
	}

	rr := httptest.NewRecorder()
	handleTraceFindings(rr, httptest.NewRequest(http.MethodGet, "/api/traces/"+entry.ID+"/findings", nil), st, entry)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var payload findingListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Total == 0 {
		t.Fatalf("findings = %+v", payload)
	}
	found := false
	for _, item := range payload.Items {
		if item.Source == "xir" && item.Category == "secret_leak" && item.EvidenceExcerpt == "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("XIR finding missing: %+v", payload.Items)
	}
}
