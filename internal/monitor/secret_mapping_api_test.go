package monitor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/secretmask"
)

const monitorMappingTestKey = "sk-proj-" + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" +
	"T3BlbkFJ" + "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"

func newTestSecretMappingVault(t *testing.T) (*store.Store, *secretmask.Vault) {
	t.Helper()
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	mappingStore, err := st.OpenSecretMappingStore(filepath.Join(t.TempDir(), "secret_mask_mappings.db"))
	if err != nil {
		t.Fatalf("OpenSecretMappingStore() error = %v", err)
	}
	vault, err := secretmask.NewVaultWithStore(nil, mappingStore, 0)
	if err != nil {
		t.Fatalf("NewVaultWithStore() error = %v", err)
	}
	t.Cleanup(func() { _ = vault.Close() })
	return st, vault
}

// TestSecretMappingAPIHandler pins the page's two operations against the real
// bbolt-backed Vault: the list exposes the pair the proxy rendered, and the
// delete removes it from disk and memory so a subsequent list is empty.
func TestSecretMappingAPIHandler(t *testing.T) {
	st, vault := newTestSecretMappingVault(t)

	body := `{"messages":[{"role":"user","content":"use ` + monitorMappingTestKey + `"}]}`
	_, events := vault.Mask([]byte(body))
	if len(events) != 1 {
		t.Fatalf("Mask() produced %d events, want 1", len(events))
	}
	fake := string(events[0].StandIn)

	mux := http.NewServeMux()
	RegisterRoutes(mux, st, RouteOptions{SecretMappings: vault})

	list := func(url string) secretMappingListResponse {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, url, nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d; body=%s", url, rr.Code, rr.Body.String())
		}
		var response secretMappingListResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode %s: %v", url, err)
		}
		return response
	}

	first := list("/api/secret-mappings")
	if !first.Available {
		t.Fatal("available = false, want the live vault")
	}
	if first.Store == nil || first.Store.Path == "" || first.Store.EntryCount != 1 {
		t.Fatalf("store = %+v, want one row and a path", first.Store)
	}
	if first.Total != 1 || len(first.Items) != 1 {
		t.Fatalf("total/items = %d/%d, want 1/1", first.Total, len(first.Items))
	}
	item := first.Items[0]
	if item.Real != monitorMappingTestKey || item.Fake != fake {
		t.Fatalf("row = %+v, want the masked pair", item)
	}
	if item.RealLength != len(monitorMappingTestKey) || item.FakeLength != len(fake) {
		t.Fatalf("lengths = %d/%d, want %d/%d", item.RealLength, item.FakeLength, len(monitorMappingTestKey), len(fake))
	}
	if !item.Encrypted {
		t.Fatal("encrypted = false, want the row sealed with the local key")
	}

	// The search filter is applied to both sides, so a reader can find a false
	// positive by any fragment they remember.
	if filtered := list("/api/secret-mappings?q=" + fake[:12]); filtered.Total != 1 {
		t.Fatalf("filtered total = %d, want 1", filtered.Total)
	}
	if empty := list("/api/secret-mappings?q=definitely-not-a-mapping"); empty.Total != 0 || len(empty.Items) != 0 {
		t.Fatalf("non-matching search = %+v, want no rows", empty)
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/secret-mappings/1", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d; body=%s", rr.Code, rr.Body.String())
	}
	if vault.Len() != 0 {
		t.Fatalf("vault length after delete = %d, want 0", vault.Len())
	}
	if after := list("/api/secret-mappings"); after.Total != 0 {
		t.Fatalf("total after delete = %d, want 0", after.Total)
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/secret-mappings/1", nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("second DELETE status = %d, want 404", rr.Code)
	}
}

func TestSecretMappingAPIHandlerUnavailable(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	mux := http.NewServeMux()
	RegisterRoutes(mux, st)

	req := httptest.NewRequest(http.MethodGet, "/api/secret-mappings", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	var response secretMappingListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Available {
		t.Fatal("available = true, want false without a vault")
	}
	if response.Reason != secretMappingUnavailableReason {
		t.Fatalf("reason = %q, want %q", response.Reason, secretMappingUnavailableReason)
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/secret-mappings/1", nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("DELETE status = %d, want 503", rr.Code)
	}

	// A vault exists whenever masking is on, but without mapping_store it has no
	// durable admin surface. The page has to show the same "not enabled" notice,
	// not a 500 from a failed flush.
	memoryOnly := http.NewServeMux()
	RegisterRoutes(memoryOnly, st, RouteOptions{SecretMappings: secretmask.NewVault(nil)})
	req = httptest.NewRequest(http.MethodGet, "/api/secret-mappings", nil)
	rr = httptest.NewRecorder()
	memoryOnly.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("in-memory vault GET status = %d, want 200", rr.Code)
	}
	response = secretMappingListResponse{}
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode in-memory response: %v", err)
	}
	if response.Available || response.Reason != secretMappingUnavailableReason {
		t.Fatalf("in-memory response = %+v, want unavailable", response)
	}
}
