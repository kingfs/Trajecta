package monitor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kingfs/Trajecta/pkg/xir/hook"
	"github.com/kingfs/Trajecta/pkg/xir/schema"
)

func TestXIRStructureAPI(t *testing.T) {
	rr := httptest.NewRecorder()
	xirStructureAPIHandler().ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/xir/structure", nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	var got schema.Structure
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != schema.Version || got.Root != "exchange" {
		t.Fatalf("version/root = %d/%q", got.Version, got.Root)
	}
	if len(got.Nodes) == 0 || len(got.Points) != len(hook.AllPoints()) {
		t.Fatalf("nodes = %d, points = %d", len(got.Nodes), len(got.Points))
	}
	// The document must survive a JSON round trip with its tree intact: every
	// child reference resolves to a served node.
	byID := make(map[string]bool, len(got.Nodes))
	for _, n := range got.Nodes {
		byID[n.ID] = true
	}
	for _, n := range got.Nodes {
		for _, child := range n.Children {
			if !byID[child] {
				t.Fatalf("node %q references unserved child %q", n.ID, child)
			}
		}
	}
}

func TestXIRStructureAPIMethodNotAllowed(t *testing.T) {
	rr := httptest.NewRecorder()
	xirStructureAPIHandler().ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/xir/structure", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rr.Code)
	}
	if allow := rr.Header().Get("Allow"); allow != http.MethodGet {
		t.Fatalf("Allow = %q, want GET", allow)
	}
}
