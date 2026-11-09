package monitor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/internal/store"
)

func TestSecretSuppressionAPIHandler(t *testing.T) {
	st, vault := newTestSecretMappingVault(t)

	body := `{"messages":[{"role":"user","content":"use ` + monitorMappingTestKey + `"}]}`
	if _, events := vault.Mask([]byte(body)); len(events) != 1 {
		t.Fatalf("initial Mask() produced %d events, want 1", len(events))
	}

	mux := http.NewServeMux()
	RegisterRoutes(mux, st, RouteOptions{SecretMappings: vault, SecretSuppressions: vault})

	get := func(url string) secretSuppressionListResponse {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, url, nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("GET %s status = %d; body=%s", url, rr.Code, rr.Body.String())
		}
		var response secretSuppressionListResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode %s: %v", url, err)
		}
		return response
	}

	initial := get("/api/secret-suppressions")
	if !initial.Available {
		t.Fatal("available = false, want the live vault")
	}
	if initial.Total != 0 {
		t.Fatalf("initial total = %d, want 0", initial.Total)
	}
	if !slices.Contains(initial.Rules, "openai.project_key") {
		t.Fatal("rules does not include openai.project_key")
	}
	if slices.Contains(initial.Rules, "access.url") {
		t.Fatal("rules includes the report-only access.url rule")
	}

	post := func(payload string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/secret-suppressions", strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		return rr
	}

	rr := post(`{"rule":"","real":"` + monitorMappingTestKey + `","reason":"false positive"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("POST status = %d; body=%s", rr.Code, rr.Body.String())
	}
	var created secretSuppressionView
	if err := json.Unmarshal(rr.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created suppression: %v", err)
	}
	if created.ID == 0 || created.Rule != "" || created.Real != monitorMappingTestKey || created.Reason != "false positive" {
		t.Fatalf("created = %+v, want a wildcard suppression", created)
	}
	if created.CreatedAt.IsZero() {
		t.Fatal("created_at is zero")
	}

	list := get("/api/secret-suppressions")
	if list.Total != 1 || len(list.Items) != 1 {
		t.Fatalf("total/items = %d/%d, want 1/1", list.Total, len(list.Items))
	}
	if list.Items[0].ID != created.ID {
		t.Fatalf("listed id = %d, want %d", list.Items[0].ID, created.ID)
	}

	// The mapping row still exists from the first request, but the suppression
	// must stop the next replacement before it even looks at it.
	if _, events := vault.Mask([]byte(body)); len(events) != 0 {
		t.Fatalf("Mask() produced %d events under a suppression, want 0", len(events))
	}

	duplicate := post(`{"rule":"","real":"` + monitorMappingTestKey + `","reason":"again"}`)
	if duplicate.Code != http.StatusOK {
		t.Fatalf("duplicate POST status = %d; body=%s", duplicate.Code, duplicate.Body.String())
	}
	var duplicateView secretSuppressionView
	if err := json.Unmarshal(duplicate.Body.Bytes(), &duplicateView); err != nil {
		t.Fatalf("decode duplicate: %v", err)
	}
	if duplicateView.ID != created.ID {
		t.Fatalf("duplicate id = %d, want the existing %d", duplicateView.ID, created.ID)
	}
	if after := get("/api/secret-suppressions"); after.Total != 1 {
		t.Fatalf("total after duplicate = %d, want 1", after.Total)
	}

	secondResponse := post(`{"rule":"anthropic.api_key","real":"another-value","reason":"anthropic note"}`)
	if secondResponse.Code != http.StatusOK {
		t.Fatalf("second POST status = %d; body=%s", secondResponse.Code, secondResponse.Body.String())
	}
	var second secretSuppressionView
	if err := json.Unmarshal(secondResponse.Body.Bytes(), &second); err != nil {
		t.Fatalf("decode second suppression: %v", err)
	}
	if second.Rule != "anthropic.api_key" || second.ID == created.ID {
		t.Fatalf("second = %+v, want a distinct rule-scoped entry", second)
	}
	if filtered := get("/api/secret-suppressions?q=anthropic"); filtered.Total != 1 || filtered.Items[0].ID != second.ID {
		t.Fatalf("rule filter = %+v, want only the anthropic entry", filtered)
	}
	if filtered := get("/api/secret-suppressions?q=sk-proj"); filtered.Total != 1 || filtered.Items[0].ID != created.ID {
		t.Fatalf("value filter = %+v, want only the OpenAI entry", filtered)
	}

	if bad := post(`{"rule":"not.a.rule","real":"value"}`); bad.Code != http.StatusBadRequest {
		t.Fatalf("unknown rule POST status = %d, want 400; body=%s", bad.Code, bad.Body.String())
	}
	if bad := post(`{"rule":"","real":""}`); bad.Code != http.StatusBadRequest {
		t.Fatalf("empty value POST status = %d, want 400; body=%s", bad.Code, bad.Body.String())
	}

	req := httptest.NewRequest(http.MethodDelete, "/api/secret-suppressions/1", nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d; body=%s", rr.Code, rr.Body.String())
	}
	if after := get("/api/secret-suppressions"); after.Total != 1 {
		t.Fatalf("total after first delete = %d, want the unrelated entry to remain", after.Total)
	}
	if _, events := vault.Mask([]byte(body)); len(events) != 1 {
		t.Fatalf("Mask() after deleting the suppression produced %d events, want 1", len(events))
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/secret-suppressions/"+strconv.FormatUint(second.ID, 10), nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("second DELETE status = %d; body=%s", rr.Code, rr.Body.String())
	}
	if after := get("/api/secret-suppressions"); after.Total != 0 {
		t.Fatalf("total after both deletes = %d, want 0", after.Total)
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/secret-suppressions/1", nil)
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("deleted-entry DELETE status = %d, want 404", rr.Code)
	}
}

func TestSecretSuppressionAPIHandlerUnavailable(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	mux := http.NewServeMux()
	RegisterRoutes(mux, st)

	req := httptest.NewRequest(http.MethodGet, "/api/secret-suppressions", nil)
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200", rr.Code)
	}
	var response secretSuppressionListResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Available || response.Reason != secretMappingUnavailableReason {
		t.Fatalf("response = %+v, want unavailable", response)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/secret-suppressions", strings.NewReader(`{"real":"x"}`))
	rr = httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST status = %d, want 503", rr.Code)
	}
}
