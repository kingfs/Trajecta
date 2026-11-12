package monitor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kingfs/Trajecta/internal/store"
)

func TestHandleTraceXIRReturnsAuditProjection(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "unittest", "testdata", "non-stream.http"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "trace.http")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/traces/t/xir", nil)
	handleTraceXIR(rr, req, path, store.LogEntry{ID: "t"}, nil, nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var payload struct {
		Protocol    string `json:"protocol"`
		Streaming   bool   `json:"streaming"`
		Observation struct {
			Model string `json:"model"`
		} `json:"observation"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Protocol != "chat_completions" || payload.Streaming {
		t.Fatalf("payload = %+v", payload)
	}
	if payload.Observation.Model != "qwen3-max" {
		t.Fatalf("model = %q", payload.Observation.Model)
	}
}
