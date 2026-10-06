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

// TestRoutingInspectCapabilitiesFollowTheProtocolFamily pins the inspector's half of the rule
// the forwarding path enforces, for the channel shape the shipped examples use.
//
// `POST /api/routing/inspect` answered from the stored `api_type` alone. That field is optional:
// the resolver fills it in from the provider preset, so `config/examples/anthropic.yaml`,
// `google_genai.yaml` and `vertex.yaml` - provider preset, no api_type - were described to the
// inspector as Chat Completions channels. The inspector therefore denied the `/v1/messages`
// request the proxy serves with 200 and offered the `/v1/chat/completions` request the proxy
// refuses with 502, for the same channel record. It now resolves the channel the way the
// forwarding path does and asks the same predicate (`upstream.ResolvedUpstream.SupportsRawPath`),
// and the proxy-side half of this expectation is pinned by
// TestPresetOnlyTargetsServeOnlyTheirProtocolFamily.
func TestRoutingInspectCapabilitiesFollowTheProtocolFamily(t *testing.T) {
	channels := []store.ChannelConfigRecord{
		{
			ID:             "anthropic-preset-only",
			Name:           "Anthropic by preset",
			BaseURL:        "https://api.anthropic.com",
			ProviderPreset: "anthropic",
			ProtocolFamily: upstream.ProtocolFamilyAnthropicMessages,
			Enabled:        true,
		},
		{
			ID:             "google-preset-only",
			Name:           "Google by preset",
			BaseURL:        "https://generativelanguage.googleapis.com",
			ProviderPreset: "google_genai",
			ProtocolFamily: upstream.ProtocolFamilyGoogleGenAI,
			Enabled:        true,
		},
		{
			ID:             "chat",
			Name:           "Chat backend",
			BaseURL:        "https://chat.example/v1",
			ProviderPreset: "openai",
			APIType:        upstream.APITypeChatCompletions,
			Enabled:        true,
		},
	}

	for _, tc := range []struct {
		channel  string
		endpoint string
		want     bool
	}{
		{"anthropic-preset-only", "anthropic_messages", true},
		{"anthropic-preset-only", "chat_completions", false},
		{"google-preset-only", "chat_completions", false},
		{"google-preset-only", "anthropic_messages", false},
		{"google-preset-only", "responses", false},
		{"chat", "chat_completions", true},
		{"chat", "responses", true},
		{"chat", "anthropic_messages", false},
	} {
		t.Run(tc.channel+"/"+tc.endpoint, func(t *testing.T) {
			st, err := store.New(t.TempDir())
			if err != nil {
				t.Fatalf("store.New() error = %v", err)
			}
			defer st.Close()

			for _, channel := range channels {
				if _, err := st.UpsertChannelConfig(channel); err != nil {
					t.Fatalf("UpsertChannelConfig(%s) error = %v", channel.ID, err)
				}
				if _, err := st.UpsertChannelModel(channel.ID, store.ChannelModelRecord{
					Model:   "model-" + channel.ID,
					Source:  "manual",
					Enabled: true,
				}); err != nil {
					t.Fatalf("UpsertChannelModel(%s) error = %v", channel.ID, err)
				}
			}

			handler := routingInspectAPIHandler(st, nil)
			body := `{"endpoint":"` + tc.endpoint + `","model":"model-` + tc.channel + `"}`
			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/api/routing/inspect", strings.NewReader(body)))
			if rr.Code != http.StatusOK {
				t.Fatalf("inspect status = %d body=%s", rr.Code, rr.Body.String())
			}
			var resp routingInspectResponse
			if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
				t.Fatalf("decode response: %v", err)
			}

			selected := resp.Result != nil && resp.Result.Plan.SelectedCandidateID == tc.channel
			if selected != tc.want {
				reason := resp.Error
				if resp.Result != nil {
					for _, candidate := range resp.Result.Candidates {
						if candidate.CandidateID == tc.channel {
							reason = candidate.Reason
						}
					}
				}
				t.Fatalf("inspect endpoint %q on channel %q selectable = %v (reason %q), want %v; the forwarding path answers %v for this pair",
					tc.endpoint, tc.channel, selected, reason, tc.want, tc.want)
			}
		})
	}
}
