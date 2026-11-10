// Package routeplan holds the routing vocabulary shared by the forwarding path
// and the Monitor's routing inspector: the entrypoints and execution modes a
// request can be planned for, the response-shape DTOs the inspector returns, and
// the one Responses execution-mode decision both callers make.
//
// It deliberately contains no planner. Candidate filtering, health handling and
// selection live in internal/router (Router.Explain for a dry run), so the
// inspector and the forwarding path cannot drift apart.
package routeplan

import "time"

type ClientEntrypoint string

const (
	EntrypointChatCompletions  ClientEntrypoint = "chat_completions"
	EntrypointResponses        ClientEntrypoint = "responses"
	EntrypointAnthropicMessage ClientEntrypoint = "anthropic_messages"
)

type ExecutionMode string

const (
	ExecutionModeProxyPass       ExecutionMode = "proxy_pass"
	ExecutionModeResponsesServer ExecutionMode = "responses_server"
)

type UpstreamEndpoint string

const (
	UpstreamEndpointChatCompletions  UpstreamEndpoint = "chat_completions"
	UpstreamEndpointResponses        UpstreamEndpoint = "responses"
	UpstreamEndpointAnthropicMessage UpstreamEndpoint = "anthropic_messages"
	UpstreamEndpointGenerateContent  UpstreamEndpoint = "generate_content"
)

type ResponsesStrategy string

const (
	ResponsesStrategyAuto              ResponsesStrategy = "auto"
	ResponsesStrategyPreferNative      ResponsesStrategy = "prefer_native"
	ResponsesStrategyPreferLocalServer ResponsesStrategy = "prefer_local_server"
	ResponsesStrategyNativeOnly        ResponsesStrategy = "native_only"
	ResponsesStrategyLocalServerOnly   ResponsesStrategy = "local_server_only"
)

// Reason values reported per route-plan candidate. The Monitor UI renders these
// strings; they are part of the public inspect response shape.
//
// The router is the only planner now, so this is exactly the vocabulary
// internal/router.Explain can emit. The six values the deleted routeplan planner
// used to declare - unsupported_entrypoint, unsupported_responses_strategy,
// channel_not_enabled, requires_local_responses_runtime,
// strategy_disallows_native_responses and strategy_disallows_chat_fallback -
// were removed: no code has emitted them since the merge, and the live router
// filters disabled channels out before it builds a candidate list. Add a
// constant only together with the code that reports it; the package test fails
// when a declared reason has no producer.
const (
	ReasonSelected                  = "selected"
	ReasonModelNotMatched           = "model_not_matched"
	ReasonModelNotEnabled           = "model_not_enabled"
	ReasonRequiresChatCompletions   = "requires_chat_completions"
	ReasonRequiresResponses         = "requires_responses"
	ReasonRequiresAnthropicMessages = "requires_anthropic_messages"
	ReasonNoCandidate               = "no_route_candidate"
	ReasonTargetUnhealthy           = "target_unhealthy"
	ReasonTargetCircuitOpen         = "target_circuit_open"
	ReasonModelCircuitOpen          = "model_circuit_open"
	ReasonAllTargetsOpen            = "all_targets_open"
)

type ResolvedModelCandidate struct {
	Model     string `json:"model,omitempty"`
	Alias     string `json:"alias,omitempty"`
	ChannelID string `json:"channel_id,omitempty"`
	Source    string `json:"source,omitempty"`
}

type RoutePlan struct {
	ClientEntrypoint        ClientEntrypoint         `json:"client_entrypoint"`
	RequestedModel          string                   `json:"requested_model,omitempty"`
	ResolvedModelCandidates []ResolvedModelCandidate `json:"resolved_model_candidates,omitempty"`
	ExecutionMode           ExecutionMode            `json:"execution_mode"`
	UpstreamEndpoint        UpstreamEndpoint         `json:"upstream_endpoint"`
	SelectedCandidateID     string                   `json:"selected_candidate_id,omitempty"`
	SelectedRouteTargetID   string                   `json:"selected_route_target_id,omitempty"`
	SelectedChannelID       string                   `json:"selected_channel_id,omitempty"`
	UpstreamModel           string                   `json:"upstream_model,omitempty"`
	// PriorityMode and SelectedTier expose how the router interpreted
	// channel priority for this decision (compat vs strict).
	PriorityMode        string            `json:"priority_mode,omitempty"`
	SelectedTier        int               `json:"selected_tier,omitempty"`
	Strategy            ResponsesStrategy `json:"strategy,omitempty"`
	Reason              string            `json:"reason,omitempty"`
	FallbacksConsidered []PlanCandidate   `json:"fallbacks_considered,omitempty"`
	// ProtocolConversion is set when the selected candidate serves the request
	// only through protocol conversion; UpstreamEndpoint then names the wire
	// protocol that is actually called.
	ProtocolConversion *ProtocolConversion `json:"protocol_conversion,omitempty"`
}

// ProtocolConversion describes a selected passthrough-first conversion.
type ProtocolConversion struct {
	ClientProtocol   string `json:"client_protocol,omitempty"`
	UpstreamProtocol string `json:"upstream_protocol,omitempty"`
}

type PlanCandidate struct {
	CandidateID      string           `json:"candidate_id,omitempty"`
	RouteTargetID    string           `json:"route_target_id,omitempty"`
	ChannelID        string           `json:"channel_id,omitempty"`
	ExecutionMode    ExecutionMode    `json:"execution_mode,omitempty"`
	UpstreamEndpoint UpstreamEndpoint `json:"upstream_endpoint,omitempty"`
	UpstreamModel    string           `json:"upstream_model,omitempty"`
	Selectable       bool             `json:"selectable"`
	Reason           string           `json:"reason,omitempty"`
	Rank             int              `json:"rank"`
	// HealthState mirrors the runtime health used to decide Selectable so a caller
	// can explain why a structurally capable candidate was filtered out.
	HealthState string     `json:"health_state,omitempty"`
	CircuitOpen bool       `json:"circuit_open,omitempty"`
	AvailableAt *time.Time `json:"available_at,omitempty"`
	// ModelHealthState is the per-model breaker that filtered this candidate
	// when the channel-level HealthState still reads healthy.
	ModelHealthState string `json:"model_health_state,omitempty"`
}

type Result struct {
	Plan       RoutePlan       `json:"plan"`
	Candidates []PlanCandidate `json:"candidates"`
}

// ResponsesDecisionInput is the native-versus-local question for one
// `/v1/responses` request. The forwarding path answers the three booleans from
// the live router and the local runtime; the Monitor's inspector passes the same
// shape from its dry run.
type ResponsesDecisionInput struct {
	Strategy        ResponsesStrategy
	NativeAvailable bool
	NativePresent   bool
	LocalAvailable  bool
}

// ResponsesDecision is the shared answer: either serve the request through the
// local Responses runtime, or reject it with the message the client sees.
type ResponsesDecision struct {
	UseLocal     bool
	RejectReason string
}

// DecideResponses is the single implementation of the Responses
// native-versus-local policy. It replaces the proxy handler's strategy switch so
// the forwarding hot path and the Monitor's dry run cannot disagree about which
// strategy is in effect or which rejection message a client gets.
//
// An empty or unknown strategy behaves as `auto`.
func DecideResponses(in ResponsesDecisionInput) ResponsesDecision {
	switch in.Strategy {
	case ResponsesStrategyPreferLocalServer:
		if in.LocalAvailable {
			return ResponsesDecision{UseLocal: true}
		}
		if in.NativeAvailable {
			return ResponsesDecision{}
		}
		return ResponsesDecision{RejectReason: noMatchingResponsesRoute}
	case ResponsesStrategyNativeOnly:
		if in.NativeAvailable {
			return ResponsesDecision{}
		}
		return ResponsesDecision{RejectReason: "responses_strategy native_only requires a matching native Responses upstream"}
	case ResponsesStrategyLocalServerOnly:
		if in.LocalAvailable {
			return ResponsesDecision{UseLocal: true}
		}
		return ResponsesDecision{RejectReason: "responses_strategy local_server_only requires a matching chat completions backend"}
	case ResponsesStrategyAuto, ResponsesStrategyPreferNative, "":
		if in.NativeAvailable {
			return ResponsesDecision{}
		}
		if in.NativePresent {
			return ResponsesDecision{RejectReason: "native Responses upstream exists but is not selectable for this request"}
		}
		if in.LocalAvailable {
			return ResponsesDecision{UseLocal: true}
		}
		return ResponsesDecision{RejectReason: noMatchingResponsesRoute}
	default:
		return DecideResponses(ResponsesDecisionInput{
			Strategy:        ResponsesStrategyAuto,
			NativeAvailable: in.NativeAvailable,
			NativePresent:   in.NativePresent,
			LocalAvailable:  in.LocalAvailable,
		})
	}
}

const noMatchingResponsesRoute = "no matching Responses route is available for the requested model and strategy"
