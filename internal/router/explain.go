package router

import (
	"bytes"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/internal/routeplan"
	"github.com/kingfs/Trajecta/pkg/llm"
)

// ExplainRequest is a read-only routing question about one prospective request.
//
// It exists for the Monitor's routing inspector: the inspector must describe the
// decision the forwarding path would make without sending anything upstream.
type ExplainRequest struct {
	RawPath string
	Body    []byte
	// Strategy is the persisted responses_strategy setting. It is only consulted
	// for `/v1/responses`.
	Strategy routeplan.ResponsesStrategy
	// LocalAvailable tells the dry run whether the local Responses runtime can
	// serve a translated request (the handler answers this from its runtime and
	// chat backend); the inspector passes true because the runtime is always
	// available in the server process.
	LocalAvailable bool
}

// ExplainResult is the inspector-shaped answer: the same Result the old
// routeplan.Plan produced, or the rejection message a client would see.
type ExplainResult struct {
	Result routeplan.Result
	Error  string
}

// Explain answers "what would the router do for this request" with the real
// selection core, in dry-run mode: no sticky binding is read or written and no
// inflight counter changes, so the inspector is repeatable and cannot perturb
// the forwarding path.
func (r *Router) Explain(in ExplainRequest) ExplainResult {
	if r == nil {
		return ExplainResult{Error: "router is not configured"}
	}
	rawPath := strings.TrimSpace(in.RawPath)
	if rawPath == "" {
		rawPath = "/v1/responses"
	}
	endpoint := llm.NormalizeEndpoint(rawPath)
	switch endpoint {
	case "/v1/chat/completions":
		if endpoint != rawPath {
			rawPath = endpoint
		}
		return r.explainProxyEntrypoint(rawPath, in.Body, routeplan.EntrypointChatCompletions, routeplan.UpstreamEndpointChatCompletions)
	case "/v1/messages":
		if endpoint != rawPath {
			rawPath = endpoint
		}
		return r.explainProxyEntrypoint(rawPath, in.Body, routeplan.EntrypointAnthropicMessage, routeplan.UpstreamEndpointAnthropicMessage)
	case "/v1/responses":
		return r.explainResponses(in.Body, in)
	default:
		return ExplainResult{Error: fmt.Sprintf("unsupported endpoint %q", in.RawPath)}
	}
}

// explainProxyEntrypoint plans a plain pass-through entrypoint (chat
// completions or Anthropic messages).
func (r *Router) explainProxyEntrypoint(rawPath string, body []byte, entrypoint routeplan.ClientEntrypoint, endpoint routeplan.UpstreamEndpoint) ExplainResult {
	req, err := newExplainRequest(rawPath, body)
	if err != nil {
		return ExplainResult{Error: err.Error()}
	}
	selection, selErr := r.selectTargetsWith(req, body, nil, selectOptions{DryRun: true})
	model := requestModel(rawPath, body)
	return r.explainResultForSelection(entrypoint, routeplan.ExecutionModeProxyPass, endpoint, model, "", selection, selErr)
}

// explainResponses plans the `/v1/responses` entrypoint. It answers the
// native-versus-local question with the shared routeplan.DecideResponses and then
// dry-runs the entrypoint that decision would take.
func (r *Router) explainResponses(body []byte, in ExplainRequest) ExplainResult {
	const rawPath = "/v1/responses"
	features := extractRequestFeatures(rawPath, body)
	model := features.ModelName
	strategy := explainStrategy(in.Strategy)
	baseReq, err := newExplainRequest(rawPath, body)
	if err != nil {
		return ExplainResult{Error: err.Error()}
	}

	nativeAvailable := r.HasSelectableNativeResponsesCandidateWithBody(baseReq, body)
	nativePresent := r.HasNativeResponsesTargetWithBody(baseReq, body)
	decision := routeplan.DecideResponses(routeplan.ResponsesDecisionInput{
		Strategy:        strategy,
		NativeAvailable: nativeAvailable,
		NativePresent:   nativePresent,
		LocalAvailable:  in.LocalAvailable,
	})

	switch {
	case decision.UseLocal:
		// The local runtime serves the request by calling the upstream's Chat
		// Completions surface, so plan that surface for the same body.
		chatReq, err := newExplainRequest("/v1/chat/completions", body)
		if err != nil {
			return ExplainResult{Error: err.Error()}
		}
		selection, selErr := r.selectTargetsWith(chatReq, body, nil, selectOptions{DryRun: true})
		return r.explainResultForSelection(routeplan.EntrypointResponses, routeplan.ExecutionModeResponsesServer, routeplan.UpstreamEndpointChatCompletions, model, strategy, selection, selErr)
	case decision.RejectReason != "":
		return r.explainResponsesRejection(features, model, strategy, decision.RejectReason)
	default:
		// Native pass-through: mark the request exactly like the hot path does,
		// so the dry run applies the same native requirement.
		req, err := newExplainRequest(rawPath, body)
		if err != nil {
			return ExplainResult{Error: err.Error()}
		}
		marked := WithNativeResponsesPathRequirement(req)
		selection, selErr := r.selectTargetsWith(marked, body, nil, selectOptions{DryRun: true})
		return r.explainResultForSelection(routeplan.EntrypointResponses, routeplan.ExecutionModeProxyPass, routeplan.UpstreamEndpointResponses, model, strategy, selection, selErr)
	}
}

// explainResponsesRejection describes why the request cannot be served. The
// rejection is not a selection failure, so the candidate list and the plan reason
// are derived from the surface the decision was about: a native pass-through
// attempt (native target present or native_only) applies the native requirement,
// while a local_server_only rejection explains the Chat Completions backend it
// needs.
func (r *Router) explainResponsesRejection(features RequestFeatures, model string, strategy routeplan.ResponsesStrategy, rejectReason string) ExplainResult {
	mode := routeplan.ExecutionModeProxyPass
	endpoint := routeplan.UpstreamEndpointResponses
	tracePath := "/v1/responses"
	// A rejection is best explained from the attempt that failed: the native
	// pass-through surface for native_only/prefer_native/native_present and the
	// Chat Completions surface for a local_server_only rejection. For a plain
	// auto rejection (no native target and no local backend) the native surface
	// says each chat-only channel `requires_responses`, which is exactly why the
	// request cannot pass through.
	requireNative := strategy != routeplan.ResponsesStrategyLocalServerOnly
	if strategy == routeplan.ResponsesStrategyLocalServerOnly {
		mode = routeplan.ExecutionModeResponsesServer
		endpoint = routeplan.UpstreamEndpointChatCompletions
		tracePath = "/v1/chat/completions"
		requireNative = false
	}
	trace := r.dryRunDecisionTrace(tracePath, model, features, requireNative)
	candidates := r.planCandidates(trace, mode, endpoint, model, nil)
	plan := routeplan.RoutePlan{
		ClientEntrypoint:        routeplan.EntrypointResponses,
		RequestedModel:          model,
		ResolvedModelCandidates: r.explainResolvedModelCandidates(model),
		ExecutionMode:           mode,
		UpstreamEndpoint:        endpoint,
		Strategy:                strategy,
		Reason:                  rejectPlanReason(strategy, candidates),
		FallbacksConsidered:     candidates,
	}
	return ExplainResult{Result: routeplan.Result{Plan: plan, Candidates: candidates}, Error: rejectReason}
}

// dryRunDecisionTrace builds the same candidate list the hot path records for the
// given path and native requirement, without selecting anything.
func (r *Router) dryRunDecisionTrace(rawPath string, model string, features RequestFeatures, requireNative bool) *DecisionTrace {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.buildDecisionTrace(rawPath, model, nil, features, requireNative)
}

func explainStrategy(strategy routeplan.ResponsesStrategy) routeplan.ResponsesStrategy {
	switch strategy {
	case routeplan.ResponsesStrategyAuto,
		routeplan.ResponsesStrategyPreferNative,
		routeplan.ResponsesStrategyPreferLocalServer,
		routeplan.ResponsesStrategyNativeOnly,
		routeplan.ResponsesStrategyLocalServerOnly:
		return strategy
	default:
		return routeplan.ResponsesStrategyAuto
	}
}

func newExplainRequest(rawPath string, body []byte) (*http.Request, error) {
	return http.NewRequest(http.MethodPost, "http://router.invalid"+rawPath, bytes.NewReader(body))
}

// explainResultForSelection turns a selection (or its failure) into the
// inspector's Result.
func (r *Router) explainResultForSelection(entrypoint routeplan.ClientEntrypoint, mode routeplan.ExecutionMode, endpoint routeplan.UpstreamEndpoint, model string, strategy routeplan.ResponsesStrategy, selection *Selection, selErr error) ExplainResult {
	var decision *DecisionTrace
	if selection != nil {
		decision = selection.Decision
	} else if selErr != nil {
		decision = SelectionDecision(selErr)
	}
	ranks := r.candidateRanks(selection)
	candidates := r.planCandidates(decision, mode, endpoint, model, ranks)

	plan := routeplan.RoutePlan{
		ClientEntrypoint:        entrypoint,
		RequestedModel:          model,
		ResolvedModelCandidates: r.explainResolvedModelCandidates(model),
		ExecutionMode:           mode,
		UpstreamEndpoint:        endpoint,
		Strategy:                strategy,
		FallbacksConsidered:     candidates,
	}
	if selection != nil && selection.Target != nil {
		plan.SelectedCandidateID = selection.Target.ID
		plan.SelectedRouteTargetID = selection.Target.RouteTargetID
		plan.SelectedChannelID = selection.Target.ChannelID
		plan.UpstreamModel = selection.Target.ResolveModelAlias(model)
		if strings.TrimSpace(plan.UpstreamModel) == "" {
			plan.UpstreamModel = model
		}
		plan.Reason = routeplan.ReasonSelected
		return ExplainResult{Result: routeplan.Result{Plan: plan, Candidates: candidates}}
	}
	plan.Reason = explainFailureReason(selErr, candidates)
	errorText := ""
	if selErr != nil {
		errorText = selErr.Error()
	}
	return ExplainResult{Result: routeplan.Result{Plan: plan, Candidates: candidates}, Error: errorText}
}

// explainResolvedModelCandidates reports the model names this request actually
// resolves to on the live router: the requested name plus every alias the
// router resolves it to. Aliases are part of each target's model set and alias
// map (see channel.RuntimeTargets), so the dry run reads the same expansion the
// forwarding path applies when it resolves the upstream wire name; the inspector
// needs no store lookup and no second planner.
func (r *Router) explainResolvedModelCandidates(model string) []routeplan.ResolvedModelCandidate {
	model = strings.TrimSpace(model)
	if model == "" {
		return nil
	}
	out := []routeplan.ResolvedModelCandidate{{Model: model, Source: "request"}}
	r.mu.RLock()
	targets := append([]*Target(nil), r.targets...)
	r.mu.RUnlock()
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		if target == nil {
			continue
		}
		wireModel := target.ResolveModelAlias(model)
		if wireModel == "" {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(target.ChannelID)) + "\x00" + strings.ToLower(wireModel)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, routeplan.ResolvedModelCandidate{
			Model:     wireModel,
			Alias:     model,
			ChannelID: target.ChannelID,
			Source:    "alias",
		})
	}
	return out
}

// candidateRanks returns the 1-based position of every available candidate in
// the order the selection core would consider it for this request: lowest
// expected cost first, then the compareScore tie-break (priority, then candidate
// id). It is the same cost function the forwarding path uses when it picks a
// target, so under the deterministic first_available policy the chosen target is
// rank 1. Only candidates that survived filtering get a rank; a filtered
// candidate keeps 0 because it never entered the selection order.
func (r *Router) candidateRanks(selection *Selection) map[string]int {
	if selection == nil || len(selection.Candidates) == 0 {
		return nil
	}
	selected := make(map[string]*Target, len(selection.Candidates))
	for _, id := range selection.Candidates {
		selected[id] = nil
	}
	r.mu.RLock()
	for _, target := range r.targets {
		if target == nil {
			continue
		}
		if _, wanted := selected[target.ID]; wanted {
			selected[target.ID] = target
		}
	}
	r.mu.RUnlock()

	type rankedCandidate struct {
		target *Target
		score  float64
	}
	ranked := make([]rankedCandidate, 0, len(selected))
	for _, target := range selected {
		if target == nil {
			continue
		}
		ranked = append(ranked, rankedCandidate{target: target, score: r.expectedCost(target, selection.Request)})
	}
	slices.SortFunc(ranked, func(a, b rankedCandidate) int {
		return compareScore(a.target, a.score, b.target, b.score)
	})
	ranks := make(map[string]int, len(ranked))
	for index, candidate := range ranked {
		ranks[candidate.target.ID] = index + 1
	}
	return ranks
}

// explainFailureReason maps a selection failure to the inspect reason vocabulary.
func explainFailureReason(selErr error, candidates []routeplan.PlanCandidate) string {
	switch SelectionFailureReason(selErr) {
	case SelectionFailureAllTargetsOpen:
		return routeplan.ReasonAllTargetsOpen
	case SelectionFailureModelNotEnabled:
		return routeplan.ReasonModelNotEnabled
	case SelectionFailureModelUnknown:
		return routeplan.ReasonModelNotMatched
	}
	if first := firstRequiresReason(candidates); first != "" {
		return first
	}
	if anyHealthReason(candidates) {
		return routeplan.ReasonAllTargetsOpen
	}
	return routeplan.ReasonNoCandidate
}

// rejectPlanReason names the reason of a plan the Responses policy rejected.
func rejectPlanReason(strategy routeplan.ResponsesStrategy, candidates []routeplan.PlanCandidate) string {
	if anyHealthReason(candidates) {
		return routeplan.ReasonAllTargetsOpen
	}
	if first := firstRequiresReason(candidates); first != "" {
		return first
	}
	switch strategy {
	case routeplan.ResponsesStrategyNativeOnly:
		return routeplan.ReasonRequiresResponses
	case routeplan.ResponsesStrategyLocalServerOnly:
		return routeplan.ReasonRequiresChatCompletions
	default:
		return routeplan.ReasonNoCandidate
	}
}

func firstRequiresReason(candidates []routeplan.PlanCandidate) string {
	for _, candidate := range candidates {
		if strings.HasPrefix(candidate.Reason, "requires_") {
			return candidate.Reason
		}
	}
	return ""
}

func anyHealthReason(candidates []routeplan.PlanCandidate) bool {
	for _, candidate := range candidates {
		switch candidate.Reason {
		case routeplan.ReasonTargetCircuitOpen, routeplan.ReasonModelCircuitOpen, routeplan.ReasonTargetUnhealthy:
			return true
		}
	}
	return false
}

// planCandidates maps the router's live candidate decisions onto the inspector's
// candidate DTO. The shapes are different on purpose - the router carries
// protocol/credential internals, the inspector carries an operator-facing reason -
// but both are computed from the same decision trace. ranks is the candidate-id
// to selection-position map from candidateRanks; a candidate missing from it was
// filtered out before selection and keeps rank 0.
func (r *Router) planCandidates(decision *DecisionTrace, mode routeplan.ExecutionMode, endpoint routeplan.UpstreamEndpoint, model string, ranks map[string]int) []routeplan.PlanCandidate {
	if decision == nil {
		return nil
	}
	now := time.Now()
	out := make([]routeplan.PlanCandidate, 0, len(decision.Candidates))
	for _, candidate := range decision.Candidates {
		out = append(out, routeplan.PlanCandidate{
			CandidateID:      candidate.ID,
			RouteTargetID:    candidate.RouteTargetID,
			ChannelID:        candidate.ChannelID,
			ExecutionMode:    mode,
			UpstreamEndpoint: endpoint,
			UpstreamModel:    model,
			Selectable:       candidate.Selectable,
			Reason:           candidatePlanReason(candidate, endpoint, model, r),
			Rank:             ranks[candidate.ID],
			HealthState:      candidate.HealthState,
			ModelHealthState: candidate.ModelHealthState,
			CircuitOpen:      candidate.FilterReason == "target_open" || candidate.FilterReason == "model_open",
			AvailableAt:      nearestAvailableAt(candidate, now),
		})
	}
	return out
}

func candidatePlanReason(candidate CandidateDecision, endpoint routeplan.UpstreamEndpoint, model string, r *Router) string {
	if candidate.Selectable && candidate.FilterReason == "" {
		return routeplan.ReasonSelected
	}
	switch candidate.FilterReason {
	case "unsupported_path":
		switch endpoint {
		case routeplan.UpstreamEndpointChatCompletions:
			return routeplan.ReasonRequiresChatCompletions
		case routeplan.UpstreamEndpointResponses:
			return routeplan.ReasonRequiresResponses
		default:
			return routeplan.ReasonRequiresAnthropicMessages
		}
	case "unsupported_model":
		if r != nil && r.targetModelDisabled(candidate.ID, model) {
			return routeplan.ReasonModelNotEnabled
		}
		return routeplan.ReasonModelNotMatched
	case "unsupported_tools":
		return "requires_tool_calling"
	case "native_responses_required":
		return routeplan.ReasonRequiresResponses
	case "target_open":
		return routeplan.ReasonTargetCircuitOpen
	case "model_open":
		return routeplan.ReasonModelCircuitOpen
	case "target_probation_full", "model_probation_full":
		return routeplan.ReasonTargetUnhealthy
	case "excluded":
		return "excluded"
	default:
		return candidate.FilterReason
	}
}

// nearestAvailableAt returns the nearest future breaker window, which is when the
// candidate may become selectable again.
func nearestAvailableAt(candidate CandidateDecision, now time.Time) *time.Time {
	var out *time.Time
	consider := func(window *time.Time) {
		if window == nil || !window.After(now) {
			return
		}
		if out == nil || window.Before(*out) {
			value := *window
			out = &value
		}
	}
	consider(candidate.OpenUntil)
	consider(candidate.ModelOpenUntil)
	return out
}

// targetModelDisabled reports whether the target declares the model but switched
// it off (as opposed to never declaring it).
func (r *Router) targetModelDisabled(targetID string, model string) bool {
	key := strings.ToLower(strings.TrimSpace(model))
	if key == "" {
		return false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, target := range r.targets {
		if target == nil || target.ID != targetID {
			continue
		}
		target.mu.Lock()
		_, disabled := target.disabledModels[key]
		target.mu.Unlock()
		return disabled
	}
	return false
}
