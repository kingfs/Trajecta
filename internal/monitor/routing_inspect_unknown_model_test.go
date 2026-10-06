package monitor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/internal/upstream"
)

// TestRoutingInspectHonoursAllowUnknownModels pins the inspector's half of the rule the forwarding
// path enforces (TestUndeclaredModelIsRoutedOnlyWhenTheTargetAllowsIt).
//
// A channel serves a model it does not declare only when allow_unknown_models is set, which the
// router decides in Target.supportsModelLocked. The planner had no such input and used its own
// fallback instead - "a candidate that declares no models serves every model" - so for the normal
// shape (a channel with declared models) the inspector reported no route for exactly the models the
// proxy forwards, and for a channel whose model set is still empty because discovery has not run it
// planned a route the strict default rejects. Both directions are covered here.
func TestRoutingInspectHonoursAllowUnknownModels(t *testing.T) {
	cases := []struct {
		name  string
		allow bool
		// declared is the single model row the channel declares; empty means the channel has no
		// model rows yet, which is the shape the planner used to treat as "serves everything".
		declared    string
		wantPlanned bool
	}{
		{name: "allow-unknown", allow: true, declared: "declared", wantPlanned: true},
		{name: "strict", allow: false, declared: "declared", wantPlanned: false},
		{name: "strict with no declared models", allow: false, declared: "", wantPlanned: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st, err := store.New(t.TempDir())
			if err != nil {
				t.Fatalf("store.New() error = %v", err)
			}
			defer st.Close()

			if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
				ID: "chan", Name: "chan", BaseURL: "https://chat.example/v1",
				ProviderPreset: "openai", APIType: upstream.APITypeChatCompletions,
				Enabled: true, AllowUnknownModels: tc.allow,
			}); err != nil {
				t.Fatalf("UpsertChannelConfig() error = %v", err)
			}
			if tc.declared != "" {
				yes := 1
				if _, err := st.UpsertChannelModel("chan", store.ChannelModelRecord{
					Model: tc.declared, Source: "manual", Enabled: true, SupportsChatCompletions: &yes,
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

			// The declared model is routable in both settings; it is the control that shows the
			// difference below comes from the unknown-model rule and not from a broken fixture.
			declared := routingInspectResponse{}
			if tc.declared != "" {
				declared = inspect(tc.declared)
				if declared.Result.Plan.SelectedCandidateID != "chan" || declared.Result.Plan.ExecutionMode == "" {
					t.Fatalf("inspect(%s) planned %+v, want the channel to serve its own declared model", tc.declared, declared.Result.Plan)
				}
			}

			unknown := inspect("undeclared")
			if tc.wantPlanned {
				if unknown.Result.Plan.SelectedCandidateID != "chan" {
					t.Fatalf("inspect(undeclared) planned %+v with candidates %+v, want the channel that allows unknown models", unknown.Result.Plan, unknown.Result.Candidates)
				}
				if unknown.Result.Plan.ExecutionMode != declared.Result.Plan.ExecutionMode || unknown.Result.Plan.UpstreamEndpoint != declared.Result.Plan.UpstreamEndpoint {
					t.Fatalf("inspect(undeclared) = %s/%s, want the same mode and endpoint as the declared model (%s/%s): the forwarding path sends an undeclared model to the same endpoint",
						unknown.Result.Plan.ExecutionMode, unknown.Result.Plan.UpstreamEndpoint, declared.Result.Plan.ExecutionMode, declared.Result.Plan.UpstreamEndpoint)
				}
				if unknown.Result.Plan.UpstreamModel != "undeclared" {
					t.Fatalf("inspect(undeclared) upstream model = %q, want the requested name %q", unknown.Result.Plan.UpstreamModel, "undeclared")
				}
				return
			}

			if unknown.Result.Plan.SelectedCandidateID != "" || unknown.Result.Plan.ExecutionMode != "" {
				t.Fatalf("inspect(undeclared) planned %+v, want no route: the channel does not allow unknown models", unknown.Result.Plan)
			}
			for _, candidate := range unknown.Result.Candidates {
				if candidate.Selectable {
					t.Fatalf("inspect(undeclared) marked candidate %q selectable, want it rejected because the model is not declared", candidate.CandidateID)
				}
			}
		})
	}
}
