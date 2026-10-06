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

// TestRoutingInspectResolvesAliasedModelCapabilities pins the inspector's half of the rule the
// forwarding path enforces (TestAliasResolvedModelKeepsTheTargetCapabilities).
//
// The inspector built its per-model override map from the stored rows, keyed by each row's own
// model name, while the router keys the same overrides by the model name in the client request
// and channel.ChannelModelCapabilities gives an alias name its target row's overrides. For an
// alias whose name is also a declared model of the channel, the two answers differed: the
// inspector reported the shadowed row's capabilities, so it planned a native `proxy_pass` for a
// request the proxy answers through the local runtime's Chat Completions translation.
//
// The two requests below resolve to the same upstream model with the same capabilities, so the
// inspector has to describe them identically.
func TestRoutingInspectResolvesAliasedModelCapabilities(t *testing.T) {
	st, err := store.New(t.TempDir())
	if err != nil {
		t.Fatalf("store.New() error = %v", err)
	}
	defer st.Close()

	if _, err := st.UpsertChannelConfig(store.ChannelConfigRecord{
		ID:             "chan",
		Name:           "chan",
		BaseURL:        "https://chat.example/v1",
		ProviderPreset: "openai",
		APIType:        upstream.APITypeChatCompletions,
		Enabled:        true,
	}); err != nil {
		t.Fatalf("UpsertChannelConfig() error = %v", err)
	}
	yes, no := 1, 0
	for _, model := range []store.ChannelModelRecord{
		// `fast` declares native Responses support, but the alias below redirects the name to
		// `slow`, which does not.
		{Model: "fast", Source: "manual", Enabled: true, SupportsResponses: &yes},
		{Model: "slow", Source: "manual", Enabled: true, SupportsChatCompletions: &yes, SupportsResponses: &no},
	} {
		if _, err := st.UpsertChannelModel("chan", model); err != nil {
			t.Fatalf("UpsertChannelModel(%s) error = %v", model.Model, err)
		}
	}
	if _, err := st.UpsertModelAlias(store.ModelAliasRecord{Alias: "fast", TargetModel: "slow", Enabled: true}); err != nil {
		t.Fatalf("UpsertModelAlias() error = %v", err)
	}

	handler := routingInspectAPIHandler(st, nil)
	plans := map[string]struct {
		mode     string
		endpoint string
	}{}
	for _, model := range []string{"fast", "slow"} {
		rr := httptest.NewRecorder()
		body := `{"endpoint":"responses","model":"` + model + `"}`
		handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/routing/inspect", strings.NewReader(body)))
		if rr.Code != http.StatusOK {
			t.Fatalf("inspect %s status = %d body = %s", model, rr.Code, rr.Body.String())
		}
		var resp routingInspectResponse
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode inspect %s response: %v", model, err)
		}
		if resp.Result == nil {
			t.Fatalf("inspect %s returned no plan: %s", model, resp.Error)
		}
		plans[model] = struct {
			mode     string
			endpoint string
		}{mode: string(resp.Result.Plan.ExecutionMode), endpoint: string(resp.Result.Plan.UpstreamEndpoint)}
	}

	alias, target := plans["fast"], plans["slow"]
	if alias != target {
		t.Fatalf("inspect described the aliased model as %+v and its target as %+v; both resolve to the same upstream model with the same capabilities, so the answers must match", alias, target)
	}
	wantMode := string(routeplan.ExecutionModeResponsesServer)
	wantEndpoint := string(routeplan.UpstreamEndpointChatCompletions)
	if alias.mode != wantMode || alias.endpoint != wantEndpoint {
		t.Fatalf("inspect described the aliased model as %+v, want the local runtime (%q on %q): the alias target does not implement the Responses API", alias, wantMode, wantEndpoint)
	}
}
