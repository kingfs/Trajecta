package recorder

import (
	"testing"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

func TestApplyRoutingDetailFromEvents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		events []RecordEvent
		want   MetaData
	}{
		{
			name: "selection and sticky hit record the dispatched identity",
			events: []RecordEvent{
				{Type: "routing.selection", Attributes: map[string]interface{}{"upstream_id": "openai-primary"}},
				{Type: "routing.selected", Attributes: map[string]interface{}{
					"upstream_id": "openai-primary", "route_target_id": "openai-primary:cred-a",
					"channel_id": "openai-primary", "credential_id": "cred-a",
				}},
				{Type: "routing.sticky.hit", Attributes: map[string]interface{}{"sticky_status": "hit", "upstream_id": "openai-primary"}},
			},
			want: MetaData{
				SelectedUpstreamID: "openai-primary",
				RouteTargetID:      "openai-primary:cred-a",
				ChannelID:          "openai-primary",
				CredentialID:       "cred-a",
				StickyStatus:       "hit",
			},
		},
		{
			name: "the last attempt wins so a retry records where it finished",
			events: []RecordEvent{
				{Type: "routing.selected", Attributes: map[string]interface{}{
					"upstream_id": "primary", "route_target_id": "primary:default", "channel_id": "primary", "credential_id": "default",
				}},
				{Type: "routing.selected", Attributes: map[string]interface{}{
					"upstream_id": "fallback", "route_target_id": "fallback:default", "channel_id": "fallback", "credential_id": "default",
				}},
			},
			want: MetaData{
				SelectedUpstreamID: "primary",
				RouteTargetID:      "fallback:default",
				ChannelID:          "fallback",
				CredentialID:       "default",
			},
		},
		{
			name: "a sticky break records the upstream being left",
			events: []RecordEvent{
				{Type: "routing.sticky.break", Attributes: map[string]interface{}{
					"sticky_status": "break", "previous_upstream_id": "openai-primary", "upstream_id": "openrouter-fallback",
					"route_target_id": "openrouter-fallback:default", "channel_id": "openrouter-fallback", "credential_id": "default",
				}},
			},
			// A sticky event alone does not name the selected upstream; that is
			// the selection events' job, and the old event walk counted the
			// sticky upstream only in the break breakdown.
			want: MetaData{
				StickyStatus:             "break",
				StickyPreviousUpstreamID: "openai-primary",
				RouteTargetID:            "openrouter-fallback:default",
				ChannelID:                "openrouter-fallback",
				CredentialID:             "default",
			},
		},
		{
			name: "a filter rejection supplies the failure reason",
			events: []RecordEvent{
				{Type: "routing.filtered", Attributes: map[string]interface{}{"routing_failure_reason": "no_supporting_target"}},
			},
			want: MetaData{RoutingFailureReason: "no_supporting_target"},
		},
		{
			name: "the proxy's own values are not overwritten",
			events: []RecordEvent{
				{Type: "routing.selected", Attributes: map[string]interface{}{"upstream_id": "from-event"}},
				{Type: "routing.filtered", Attributes: map[string]interface{}{"routing_failure_reason": "from-event"}},
			},
			want: MetaData{
				SelectedUpstreamID:   "written-by-proxy",
				RoutingFailureReason: "written-by-proxy",
			},
		},
		{
			name:   "events that carry no routing facts leave the header untouched",
			events: []RecordEvent{{Type: "llm.usage", Attributes: map[string]interface{}{"total_tokens": 12}}},
			want:   MetaData{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			meta := MetaData{}
			if tc.name == "the proxy's own values are not overwritten" {
				meta.SelectedUpstreamID = "written-by-proxy"
				meta.RoutingFailureReason = "written-by-proxy"
			}
			ApplyRoutingDetailFromEvents(tc.events, &meta)

			if meta.SelectedUpstreamID != tc.want.SelectedUpstreamID ||
				meta.RouteTargetID != tc.want.RouteTargetID ||
				meta.ChannelID != tc.want.ChannelID ||
				meta.CredentialID != tc.want.CredentialID ||
				meta.StickyStatus != tc.want.StickyStatus ||
				meta.StickyPreviousUpstreamID != tc.want.StickyPreviousUpstreamID ||
				meta.RoutingFailureReason != tc.want.RoutingFailureReason {
				t.Fatalf("derived routing detail = %+v, want %+v", meta, tc.want)
			}
		})
	}
}

func TestApplyRoutingDetailFromEventsSurvivesRoundTrip(t *testing.T) {
	t.Parallel()

	// The write path and the `Sync` re-index read the same header, so the derived
	// values have to survive being marshalled into the cassette prelude.
	header := recordfile.RecordHeader{Version: "LLM_PROXY_V3", Meta: MetaData{RequestID: "req-round-trip"}}
	events := []recordfile.RecordEvent{
		{Type: "routing.selected", Attributes: map[string]interface{}{
			"upstream_id": "openai-primary", "route_target_id": "openai-primary:cred-a",
			"channel_id": "openai-primary", "credential_id": "cred-a",
		}},
		{Type: "routing.sticky.break", Attributes: map[string]interface{}{
			"sticky_status": "break", "previous_upstream_id": "openrouter-fallback",
		}},
	}
	ApplyRoutingDetailFromEvents(events, &header.Meta)

	prelude, err := recordfile.MarshalPrelude(header, events)
	if err != nil {
		t.Fatalf("MarshalPrelude() error = %v", err)
	}
	parsed, err := recordfile.ParsePrelude(prelude)
	if err != nil {
		t.Fatalf("ParsePrelude() error = %v", err)
	}

	if parsed.Header.Meta.RouteTargetID != "openai-primary:cred-a" ||
		parsed.Header.Meta.ChannelID != "openai-primary" ||
		parsed.Header.Meta.CredentialID != "cred-a" ||
		parsed.Header.Meta.StickyStatus != "break" ||
		parsed.Header.Meta.StickyPreviousUpstreamID != "openrouter-fallback" {
		t.Fatalf("round-tripped routing detail = %+v", parsed.Header.Meta)
	}
}
