package monitor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/internal/routeplan"
	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/internal/upstream"
)

// The inspector has to describe the same authorization the forwarding path
// enforces: a declared and enabled model is routable, a declared but disabled
// model is reported as model_not_enabled, and a name no channel declares is
// reported as model_not_matched. Nothing is forwarded in either rejection.
func TestRoutingInspectDescribesStrictModelAuthorization(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
		ID: "chan", Name: "chan", BaseURL: "https://chat.example/v1",
		ProviderPreset: "openai", APIType: upstream.APITypeChatCompletions,
		Enabled: true,
	}); err != nil {
		t.Fatalf("UpsertChannelConfig() error = %v", err)
	}
	full := 1
	for _, model := range []struct {
		name    string
		enabled bool
	}{{"declared", true}, {"off", false}} {
		if _, err := st.UpsertChannelModel("chan", store.ChannelModelRecord{
			Model: model.name, Source: "manual", Enabled: model.enabled, SupportsChatCompletions: &full,
		}); err != nil {
			t.Fatalf("UpsertChannelModel() error = %v", err)
		}
	}

	handler := routingInspectAPIHandler(st, nil)
	inspect := func(model string) routingInspectResponse {
		t.Helper()
		rr := httptest.NewRecorder()
		body := `{"endpoint":"chat_completions","model":"` + model + `"}`
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/routing/inspect", strings.NewReader(body)))
		if rr.Code != http.StatusOK {
			t.Fatalf("inspect %s status = %d body = %s", model, rr.Code, rr.Body.String())
		}
		var resp routingInspectResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode inspect %s response: %v", model, err)
		}
		if resp.Result == nil {
			t.Fatalf("inspect %s returned no result: %s", model, resp.Error)
		}
		return resp
	}
	reasons := func(resp routingInspectResponse) map[string]string {
		out := map[string]string{}
		for _, candidate := range resp.Result.Candidates {
			out[candidate.CandidateID] = candidate.Reason
		}
		return out
	}

	declared := inspect("declared")
	if declared.Result.Plan.SelectedCandidateID != "chan" {
		t.Fatalf("inspect(declared) planned %+v, want chan", declared.Result.Plan)
	}

	disabled := inspect("off")
	if disabled.Result.Plan.SelectedCandidateID != "" {
		t.Fatalf("inspect(off) planned %+v, want no route", disabled.Result.Plan)
	}
	if got := reasons(disabled)["chan"]; got != routeplan.ReasonModelNotEnabled {
		t.Fatalf("inspect(off) reason = %q, want %q", got, routeplan.ReasonModelNotEnabled)
	}

	unknown := inspect("undeclared")
	if unknown.Result.Plan.SelectedCandidateID != "" {
		t.Fatalf("inspect(undeclared) planned %+v, want no route", unknown.Result.Plan)
	}
	if got := reasons(unknown)["chan"]; got != routeplan.ReasonModelNotMatched {
		t.Fatalf("inspect(undeclared) reason = %q, want %q", got, routeplan.ReasonModelNotMatched)
	}
}
