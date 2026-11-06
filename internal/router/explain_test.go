package router

import (
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/routeplan"
)

// explainTestRouter builds a static, deterministic router for Explain tests.
func explainTestRouter(t *testing.T, targets ...config.UpstreamTargetConfig) *Router {
	t.Helper()
	cfg := &config.Config{Upstreams: targets}
	cfg.Router.Selection.Policy = PolicyFirstAvailable
	rtr, err := New(cfg)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	return rtr
}

func explainTestTarget(id string, priority int, baseURL string, apiType string, models ...string) config.UpstreamTargetConfig {
	enabled := true
	return config.UpstreamTargetConfig{
		ID:             id,
		Enabled:        &enabled,
		Priority:       priority,
		ModelDiscovery: ModelDiscoveryStaticOnly,
		StaticModels:   models,
		Upstream: config.UpstreamConfig{
			BaseURL:        baseURL,
			ProviderPreset: "openai",
			APIType:        apiType,
		},
	}
}

func explainPlanCandidatesByID(result routeplan.Result) map[string]routeplan.PlanCandidate {
	out := make(map[string]routeplan.PlanCandidate, len(result.Candidates))
	for _, candidate := range result.Candidates {
		out[candidate.CandidateID] = candidate
	}
	return out
}

// TestExplainChatAndMessagesEntrypoints pins the plain pass-through plans.
func TestExplainChatAndMessagesEntrypoints(t *testing.T) {
	t.Run("chat completions", func(t *testing.T) {
		rtr := explainTestRouter(t, explainTestTarget("chat", 100, "https://chat.example/v1", "chat_completions", "gpt-5"))
		explained := rtr.Explain(ExplainRequest{
			RawPath: "/v1/chat/completions",
			Body:    []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}]}`),
		})
		if explained.Error != "" {
			t.Fatalf("Explain() error = %q", explained.Error)
		}
		plan := explained.Result.Plan
		if plan.ClientEntrypoint != routeplan.EntrypointChatCompletions ||
			plan.ExecutionMode != routeplan.ExecutionModeProxyPass ||
			plan.UpstreamEndpoint != routeplan.UpstreamEndpointChatCompletions ||
			plan.SelectedCandidateID != "chat" ||
			plan.Reason != routeplan.ReasonSelected ||
			plan.UpstreamModel != "gpt-5" {
			t.Fatalf("plan = %+v, want a chat proxy_pass plan", plan)
		}
		if len(plan.ResolvedModelCandidates) != 1 || plan.ResolvedModelCandidates[0].Model != "gpt-5" {
			t.Fatalf("resolved_model_candidates = %+v, want the requested model", plan.ResolvedModelCandidates)
		}
	})

	t.Run("anthropic messages", func(t *testing.T) {
		enabled := true
		target := config.UpstreamTargetConfig{
			ID: "anthropic", Enabled: &enabled, Priority: 100,
			ModelDiscovery: ModelDiscoveryStaticOnly, StaticModels: []string{"claude-sonnet"},
			Upstream: config.UpstreamConfig{BaseURL: "https://api.anthropic.com", ProviderPreset: "anthropic", APIType: "messages"},
		}
		rtr := explainTestRouter(t, target)
		explained := rtr.Explain(ExplainRequest{
			RawPath: "/v1/messages",
			Body:    []byte(`{"model":"claude-sonnet","max_tokens":16,"messages":[]}`),
		})
		if explained.Error != "" {
			t.Fatalf("Explain() error = %q", explained.Error)
		}
		plan := explained.Result.Plan
		if plan.ClientEntrypoint != routeplan.EntrypointAnthropicMessage ||
			plan.ExecutionMode != routeplan.ExecutionModeProxyPass ||
			plan.UpstreamEndpoint != routeplan.UpstreamEndpointAnthropicMessage ||
			plan.SelectedCandidateID != "anthropic" {
			t.Fatalf("plan = %+v, want an Anthropic proxy_pass plan", plan)
		}
	})
}

// TestExplainResponsesStrategies moves the old routeplan.Plan strategy matrix onto
// the router's dry run.
func TestExplainResponsesStrategies(t *testing.T) {
	targets := []config.UpstreamTargetConfig{
		explainTestTarget("chat", 90, "https://chat.example/v1", "chat_completions", "gpt-5"),
		explainTestTarget("native", 100, "https://native.example/v1", "responses_native", "gpt-5"),
	}
	tests := []struct {
		name     string
		strategy routeplan.ResponsesStrategy
		wantMode routeplan.ExecutionMode
		wantEP   routeplan.UpstreamEndpoint
		wantID   string
	}{
		{name: "auto prefers native", strategy: routeplan.ResponsesStrategyAuto, wantMode: routeplan.ExecutionModeProxyPass, wantEP: routeplan.UpstreamEndpointResponses, wantID: "native"},
		{name: "prefer native", strategy: routeplan.ResponsesStrategyPreferNative, wantMode: routeplan.ExecutionModeProxyPass, wantEP: routeplan.UpstreamEndpointResponses, wantID: "native"},
		{name: "prefer local", strategy: routeplan.ResponsesStrategyPreferLocalServer, wantMode: routeplan.ExecutionModeResponsesServer, wantEP: routeplan.UpstreamEndpointChatCompletions, wantID: "chat"},
		{name: "native only", strategy: routeplan.ResponsesStrategyNativeOnly, wantMode: routeplan.ExecutionModeProxyPass, wantEP: routeplan.UpstreamEndpointResponses, wantID: "native"},
		{name: "local only", strategy: routeplan.ResponsesStrategyLocalServerOnly, wantMode: routeplan.ExecutionModeResponsesServer, wantEP: routeplan.UpstreamEndpointChatCompletions, wantID: "chat"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rtr := explainTestRouter(t, targets...)
			explained := rtr.Explain(ExplainRequest{
				RawPath:        "/v1/responses",
				Body:           []byte(`{"model":"gpt-5","input":"hi"}`),
				Strategy:       tt.strategy,
				LocalAvailable: true,
			})
			if explained.Error != "" {
				t.Fatalf("Explain() error = %q", explained.Error)
			}
			plan := explained.Result.Plan
			if plan.ExecutionMode != tt.wantMode || plan.UpstreamEndpoint != tt.wantEP || plan.SelectedCandidateID != tt.wantID {
				t.Fatalf("plan = %+v, want mode %s endpoint %s id %s", plan, tt.wantMode, tt.wantEP, tt.wantID)
			}
			candidates := explainPlanCandidatesByID(explained.Result)
			for _, candidate := range explained.Result.Candidates {
				if candidate.Selectable && candidate.Reason != routeplan.ReasonSelected {
					t.Fatalf("selectable candidate %+v reason = %q, want selected", candidate, candidate.Reason)
				}
			}
			if tt.wantMode == routeplan.ExecutionModeProxyPass {
				if candidate, ok := candidates["chat"]; !ok || candidate.Reason != routeplan.ReasonRequiresResponses {
					t.Fatalf("chat candidate = %+v (present %v), want requires_responses under the native attempt", candidate, ok)
				}
			}
		})
	}
}

// TestExplainResponsesAutoFallsBackToChat pins the local-runtime plan when the
// only channel speaks Chat Completions.
func TestExplainResponsesAutoFallsBackToChat(t *testing.T) {
	rtr := explainTestRouter(t, explainTestTarget("chat", 100, "https://chat.example/v1", "chat_completions", "deepseek"))
	explained := rtr.Explain(ExplainRequest{
		RawPath:        "/v1/responses",
		Body:           []byte(`{"model":"deepseek","input":"hi"}`),
		Strategy:       routeplan.ResponsesStrategyAuto,
		LocalAvailable: true,
	})
	if explained.Error != "" {
		t.Fatalf("Explain() error = %q", explained.Error)
	}
	plan := explained.Result.Plan
	if plan.ExecutionMode != routeplan.ExecutionModeResponsesServer || plan.UpstreamEndpoint != routeplan.UpstreamEndpointChatCompletions {
		t.Fatalf("plan = %+v, want responses_server on chat_completions", plan)
	}
	if plan.SelectedCandidateID != "chat" || plan.UpstreamModel != "deepseek" {
		t.Fatalf("plan = %+v, want chat/deepseek", plan)
	}
	if plan.Strategy != routeplan.ResponsesStrategyAuto {
		t.Fatalf("plan.Strategy = %q, want auto", plan.Strategy)
	}
}

// TestExplainResponsesNativeOnlyRejectsChatOnly pins a rejection: the plan carries
// the attempted surface, the reason and the candidate-level cause.
func TestExplainResponsesNativeOnlyRejectsChatOnly(t *testing.T) {
	rtr := explainTestRouter(t, explainTestTarget("chat", 100, "https://chat.example/v1", "chat_completions", "deepseek"))
	explained := rtr.Explain(ExplainRequest{
		RawPath:        "/v1/responses",
		Body:           []byte(`{"model":"deepseek","input":"hi"}`),
		Strategy:       routeplan.ResponsesStrategyNativeOnly,
		LocalAvailable: true,
	})
	if !strings.Contains(explained.Error, "native_only") {
		t.Fatalf("Explain() error = %q, want the native_only rejection", explained.Error)
	}
	plan := explained.Result.Plan
	if plan.ClientEntrypoint != routeplan.EntrypointResponses || plan.Strategy != routeplan.ResponsesStrategyNativeOnly {
		t.Fatalf("plan = %+v, want responses/native_only", plan)
	}
	if plan.ExecutionMode != routeplan.ExecutionModeProxyPass || plan.UpstreamEndpoint != routeplan.UpstreamEndpointResponses {
		t.Fatalf("plan = %+v, want the attempted native pass-through surface named", plan)
	}
	if plan.SelectedCandidateID != "" {
		t.Fatalf("plan.SelectedCandidateID = %q, want none on rejection", plan.SelectedCandidateID)
	}
	if plan.Reason != routeplan.ReasonRequiresResponses {
		t.Fatalf("plan.Reason = %q, want %q", plan.Reason, routeplan.ReasonRequiresResponses)
	}
	candidates := explainPlanCandidatesByID(explained.Result)
	if candidate := candidates["chat"]; candidate.Reason != routeplan.ReasonRequiresResponses || candidate.Selectable {
		t.Fatalf("chat candidate = %+v, want unselectable requires_responses", candidate)
	}
}

// TestExplainRejectsWhenNativePresentButUnselectable pins the health explanation:
// the native channel is structurally present but its breaker is open, so the plan
// rejects and the candidate reports the circuit plus when it may recover.
func TestExplainRejectsWhenNativePresentButUnselectable(t *testing.T) {
	rtr := explainTestRouter(t, explainTestTarget("native", 100, "https://native.example/v1", "responses_native", "gpt-5"))
	openUntil := time.Now().Add(30 * time.Second)
	rtr.targets[0].mu.Lock()
	rtr.targets[0].healthState = HealthOpen
	rtr.targets[0].openUntil = openUntil
	rtr.targets[0].mu.Unlock()

	explained := rtr.Explain(ExplainRequest{
		RawPath:        "/v1/responses",
		Body:           []byte(`{"model":"gpt-5","input":"hi"}`),
		Strategy:       routeplan.ResponsesStrategyAuto,
		LocalAvailable: true,
	})
	if !strings.Contains(explained.Error, "native Responses upstream exists but is not selectable") {
		t.Fatalf("Explain() error = %q, want the native-present rejection", explained.Error)
	}
	plan := explained.Result.Plan
	if plan.Reason != routeplan.ReasonAllTargetsOpen {
		t.Fatalf("plan.Reason = %q, want %q", plan.Reason, routeplan.ReasonAllTargetsOpen)
	}
	candidate := explainPlanCandidatesByID(explained.Result)["native"]
	if candidate.Reason != routeplan.ReasonTargetCircuitOpen || !candidate.CircuitOpen || candidate.AvailableAt == nil {
		t.Fatalf("native candidate = %+v, want an open circuit with available_at", candidate)
	}
}

// TestExplainUsesPerModelCapabilities pins that the dry run resolves the
// native-versus-local decision per model, exactly like the hot path.
func TestExplainUsesPerModelCapabilities(t *testing.T) {
	yes, no := true, false
	enabled := true
	target := config.UpstreamTargetConfig{
		ID: "mixed", Enabled: &enabled, Priority: 100,
		ModelDiscovery: ModelDiscoveryStaticOnly,
		StaticModels:   []string{"model-native", "model-chat"},
		Upstream: config.UpstreamConfig{
			BaseURL:        "https://mixed.example/v1",
			ProviderPreset: "openai",
			APIType:        "responses_native",
			Capabilities:   config.UpstreamCapabilitiesConfig{Responses: &yes, ChatCompletions: &no},
			ModelCapabilities: map[string]config.UpstreamCapabilitiesConfig{
				"model-native": {Responses: &yes, ChatCompletions: &no},
				"model-chat":   {Responses: &no, ChatCompletions: &yes},
			},
		},
	}
	tests := []struct {
		model    string
		wantMode routeplan.ExecutionMode
		wantEP   routeplan.UpstreamEndpoint
	}{
		{model: "model-native", wantMode: routeplan.ExecutionModeProxyPass, wantEP: routeplan.UpstreamEndpointResponses},
		{model: "model-chat", wantMode: routeplan.ExecutionModeResponsesServer, wantEP: routeplan.UpstreamEndpointChatCompletions},
	}
	for _, tt := range tests {
		t.Run(tt.model, func(t *testing.T) {
			rtr := explainTestRouter(t, target)
			explained := rtr.Explain(ExplainRequest{
				RawPath:        "/v1/responses",
				Body:           []byte(`{"model":"` + tt.model + `","input":"hi"}`),
				Strategy:       routeplan.ResponsesStrategyAuto,
				LocalAvailable: true,
			})
			if explained.Error != "" {
				t.Fatalf("Explain() error = %q", explained.Error)
			}
			if plan := explained.Result.Plan; plan.ExecutionMode != tt.wantMode || plan.UpstreamEndpoint != tt.wantEP {
				t.Fatalf("plan = %+v, want mode %s endpoint %s", plan, tt.wantMode, tt.wantEP)
			}
		})
	}
}

// TestExplainRanksCandidatesInSelectionOrder pins the inspector's rank: it is
// the 1-based position of the candidate in the same cost order the selection
// core uses, so the candidate the deterministic policy picks is rank 1, the
// ranks are distinct and a repeated inspection is stable.
func TestExplainRanksCandidatesInSelectionOrder(t *testing.T) {
	enabled := true
	makeTarget := func(id string, weight float64) config.UpstreamTargetConfig {
		return config.UpstreamTargetConfig{
			ID:             id,
			Enabled:        &enabled,
			Priority:       100,
			Weight:         weight,
			ModelDiscovery: ModelDiscoveryStaticOnly,
			StaticModels:   []string{"gpt-5"},
			Upstream: config.UpstreamConfig{
				BaseURL:        "https://" + id + ".example/v1",
				ProviderPreset: "openai",
				APIType:        "chat_completions",
			},
		}
	}
	// Same model, same priority: the weight alone changes the expected cost, so
	// the order is c (cheapest) then b then a.
	rtr := explainTestRouter(t, makeTarget("a", 1), makeTarget("b", 2), makeTarget("c", 4))
	explain := func() ExplainResult {
		return rtr.Explain(ExplainRequest{
			RawPath: "/v1/chat/completions",
			Body:    []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}],"max_tokens":256}`),
		})
	}

	first := explain()
	if first.Error != "" {
		t.Fatalf("Explain() error = %q", first.Error)
	}
	if first.Result.Plan.SelectedCandidateID != "c" {
		t.Fatalf("selected candidate = %q, want the cheapest target c", first.Result.Plan.SelectedCandidateID)
	}
	ranks := map[string]int{}
	for _, candidate := range first.Result.Candidates {
		ranks[candidate.CandidateID] = candidate.Rank
	}
	wantRanks := map[string]int{"c": 1, "b": 2, "a": 3}
	for id, want := range wantRanks {
		if ranks[id] != want {
			t.Fatalf("candidate %q rank = %d, want %d (all ranks: %v)", id, ranks[id], want, ranks)
		}
	}

	second := explain()
	if second.Result.Plan.SelectedCandidateID != first.Result.Plan.SelectedCandidateID {
		t.Fatalf("selection is not stable across dry runs: %q vs %q", first.Result.Plan.SelectedCandidateID, second.Result.Plan.SelectedCandidateID)
	}
	for _, candidate := range second.Result.Candidates {
		if candidate.Rank != ranks[candidate.CandidateID] {
			t.Fatalf("candidate %q rank changed between dry runs: %d then %d", candidate.CandidateID, ranks[candidate.CandidateID], candidate.Rank)
		}
	}
}

// TestExplainExpandsModelAliases pins resolved_model_candidates: the router
// expands a requested alias to every upstream wire model a target resolves it
// to, and the inspector reports that set instead of the single request model.
func TestExplainExpandsModelAliases(t *testing.T) {
	enabled := true
	makeTarget := func(id, baseURL, wireModel string) config.UpstreamTargetConfig {
		return config.UpstreamTargetConfig{
			ID:             id,
			Enabled:        &enabled,
			Priority:       100,
			ModelDiscovery: ModelDiscoveryStaticOnly,
			// RuntimeTargets folds alias names into static models; the router
			// routes the alias and resolves it through ModelAliases.
			StaticModels: []string{"fast", wireModel},
			ModelAliases: map[string]string{"fast": wireModel},
			Upstream: config.UpstreamConfig{
				BaseURL:        baseURL,
				ProviderPreset: "openai",
				APIType:        "chat_completions",
			},
		}
	}
	rtr := explainTestRouter(t,
		makeTarget("chan-a", "https://a.example/v1", "gpt-5"),
		makeTarget("chan-b", "https://b.example/v1", "gpt-4o"),
	)
	explained := rtr.Explain(ExplainRequest{
		RawPath: "/v1/chat/completions",
		Body:    []byte(`{"model":"fast","messages":[{"role":"user","content":"hi"}]}`),
	})
	if explained.Error != "" {
		t.Fatalf("Explain() error = %q", explained.Error)
	}
	plan := explained.Result.Plan
	if plan.UpstreamModel != "gpt-5" {
		t.Fatalf("plan.UpstreamModel = %q, want the alias-resolved gpt-5", plan.UpstreamModel)
	}
	got := map[string]routeplan.ResolvedModelCandidate{}
	for _, candidate := range plan.ResolvedModelCandidates {
		got[candidate.ChannelID+"\x00"+candidate.Model] = candidate
	}
	if len(plan.ResolvedModelCandidates) != 3 {
		t.Fatalf("resolved_model_candidates = %+v, want the request plus both alias expansions", plan.ResolvedModelCandidates)
	}
	request := plan.ResolvedModelCandidates[0]
	if request.Model != "fast" || request.Alias != "" || request.Source != "request" {
		t.Fatalf("first resolved candidate = %+v, want the requested model with source request", request)
	}
	for _, want := range []routeplan.ResolvedModelCandidate{
		{Model: "gpt-5", Alias: "fast", ChannelID: "chan-a", Source: "alias"},
		{Model: "gpt-4o", Alias: "fast", ChannelID: "chan-b", Source: "alias"},
	} {
		key := want.ChannelID + "\x00" + want.Model
		if got[key] != want {
			t.Fatalf("resolved candidate %q = %+v, want %+v (all: %+v)", key, got[key], want, plan.ResolvedModelCandidates)
		}
	}
}

// TestExplainNoRouteDiagnostics pins the failure vocabulary: a tool-requiring
// request on a channel without tool calling reports requires_tool_calling.
func TestExplainNoRouteDiagnostics(t *testing.T) {
	enabled := true
	target := config.UpstreamTargetConfig{
		ID: "chat", Enabled: &enabled, Priority: 100,
		ModelDiscovery: ModelDiscoveryStaticOnly, StaticModels: []string{"gpt-5"},
		Upstream: config.UpstreamConfig{
			BaseURL: "https://chat.example/v1", ProviderPreset: "openai", APIType: "chat_completions",
			Capabilities: config.UpstreamCapabilitiesConfig{ToolCalling: boolPtr(false)},
		},
	}
	rtr := explainTestRouter(t, target)
	explained := rtr.Explain(ExplainRequest{
		RawPath: "/v1/chat/completions",
		Body:    []byte(`{"model":"gpt-5","messages":[],"tools":[{"type":"function","function":{"name":"f"}}]}`),
	})
	if explained.Error == "" {
		t.Fatal("Explain() error is empty, want a selection failure")
	}
	if explained.Result.Plan.Reason != "requires_tool_calling" {
		t.Fatalf("plan.Reason = %q, want requires_tool_calling", explained.Result.Plan.Reason)
	}
	candidate := explainPlanCandidatesByID(explained.Result)["chat"]
	if candidate.Reason != "requires_tool_calling" || candidate.Selectable {
		t.Fatalf("candidate = %+v, want unselectable requires_tool_calling", candidate)
	}
}

// TestExplainRejectionStillListsResponsesPathCandidates pins the review note: an
// auto rejection (no native target, local backend unavailable) still lists the
// /v1/responses candidates with requires_responses, and names the attempted
// execution surface.
func TestExplainRejectionStillListsResponsesPathCandidates(t *testing.T) {
	rtr := explainTestRouter(t, explainTestTarget("chat", 100, "https://chat.example/v1", "chat_completions", "deepseek"))
	explained := rtr.Explain(ExplainRequest{
		RawPath:        "/v1/responses",
		Body:           []byte(`{"model":"deepseek","input":"hi"}`),
		Strategy:       routeplan.ResponsesStrategyAuto,
		LocalAvailable: false,
	})
	if !strings.Contains(explained.Error, "no matching Responses route") {
		t.Fatalf("Explain() error = %q, want the no-matching-route rejection", explained.Error)
	}
	plan := explained.Result.Plan
	if plan.ExecutionMode != routeplan.ExecutionModeProxyPass || plan.UpstreamEndpoint != routeplan.UpstreamEndpointResponses {
		t.Fatalf("plan = %+v, want the attempted proxy_pass/responses surface", plan)
	}
	if len(explained.Result.Candidates) == 0 {
		t.Fatal("rejected plan listed no candidates, want the /v1/responses path candidates")
	}
	candidate := explainPlanCandidatesByID(explained.Result)["chat"]
	if candidate.Reason != routeplan.ReasonRequiresResponses || candidate.Selectable {
		t.Fatalf("chat candidate = %+v, want unselectable requires_responses", candidate)
	}
	if plan.Reason != routeplan.ReasonRequiresResponses {
		t.Fatalf("plan.Reason = %q, want %q", plan.Reason, routeplan.ReasonRequiresResponses)
	}
}

// TestExplainLocalServerOnlyRejectionNamesTheChatSurface pins the other rejection
// surface: the frontend still gets execution_mode/upstream_endpoint for the mode
// that was attempted.
func TestExplainLocalServerOnlyRejectionNamesTheChatSurface(t *testing.T) {
	rtr := explainTestRouter(t, explainTestTarget("native", 100, "https://native.example/v1", "responses_native", "gpt-5"))
	explained := rtr.Explain(ExplainRequest{
		RawPath:        "/v1/responses",
		Body:           []byte(`{"model":"gpt-5","input":"hi"}`),
		Strategy:       routeplan.ResponsesStrategyLocalServerOnly,
		LocalAvailable: false,
	})
	if !strings.Contains(explained.Error, "local_server_only") {
		t.Fatalf("Explain() error = %q, want the local_server_only rejection", explained.Error)
	}
	plan := explained.Result.Plan
	if plan.ExecutionMode != routeplan.ExecutionModeResponsesServer || plan.UpstreamEndpoint != routeplan.UpstreamEndpointChatCompletions {
		t.Fatalf("plan = %+v, want the attempted responses_server/chat_completions surface", plan)
	}
	if plan.Reason != routeplan.ReasonRequiresChatCompletions {
		t.Fatalf("plan.Reason = %q, want %q", plan.Reason, routeplan.ReasonRequiresChatCompletions)
	}
}

// TestExplainIsSideEffectFree pins the dry-run contract: Explain must not read or
// write sticky bindings, must not account inflight, and must be repeatable.
func TestExplainIsSideEffectFree(t *testing.T) {
	rtr := explainTestRouter(t, explainTestTarget("chat", 100, "https://chat.example/v1", "chat_completions", "gpt-5"))
	body := []byte(`{"model":"gpt-5","input":"hi","prompt_cache_key":"cache-key-1"}`)

	first := rtr.Explain(ExplainRequest{RawPath: "/v1/responses", Body: body, LocalAvailable: true})
	if first.Error != "" {
		t.Fatalf("Explain() error = %q", first.Error)
	}
	second := rtr.Explain(ExplainRequest{RawPath: "/v1/responses", Body: body, LocalAvailable: true})
	if first.Result.Plan.SelectedCandidateID != second.Result.Plan.SelectedCandidateID ||
		len(first.Result.Candidates) != len(second.Result.Candidates) {
		t.Fatalf("Explain() is not repeatable: %+v vs %+v", first.Result.Plan, second.Result.Plan)
	}

	rtr.sticky.mu.Lock()
	bindings := len(rtr.sticky.bindings)
	rtr.sticky.mu.Unlock()
	if bindings != 0 {
		t.Fatalf("sticky bindings = %d after dry-run inspections, want 0", bindings)
	}
	for _, target := range rtr.targets {
		target.mu.Lock()
		inflight := target.inflight
		target.mu.Unlock()
		if inflight != 0 {
			t.Fatalf("target %q inflight = %d after dry-run inspections, want 0", target.ID, inflight)
		}
	}

	// The same body on the real path does bind and account, proving the dry run
	// is what is different.
	req, err := newExplainRequest("/v1/responses", body)
	if err != nil {
		t.Fatalf("newExplainRequest() error = %v", err)
	}
	selection, err := rtr.SelectWithBody(req, body)
	if err != nil {
		t.Fatalf("SelectWithBody() error = %v", err)
	}
	rtr.Release(selection)
	rtr.sticky.mu.Lock()
	bindings = len(rtr.sticky.bindings)
	rtr.sticky.mu.Unlock()
	if bindings != 1 {
		t.Fatalf("sticky bindings = %d after a real selection, want 1 (the dry run must be what skips the binding)", bindings)
	}
}
