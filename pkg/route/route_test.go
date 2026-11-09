package route

import (
	"context"
	"encoding/hex"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/pkg/xir"
)

var testNow = time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)

func testTarget(id string, priority int, protocols ...xir.Protocol) Target {
	return Target{
		ID:           id,
		ChannelID:    "channel-" + id,
		CredentialID: "credential-" + id,
		Model:        "gpt-4o",
		WireModel:    "gpt-4o",
		Protocols:    protocols,
		Priority:     priority,
		Weight:       1,
		Healthy:      true,
		LatencyMs:    100,
	}
}

func testSnapshot(targets ...Target) Snapshot {
	return Snapshot{
		Targets:  targets,
		Bindings: map[string]string{},
		Now:      func() time.Time { return testNow },
	}
}

func testRequest(protocol xir.Protocol) Request {
	return Request{
		Model:     "gpt-4o",
		Protocol:  protocol,
		Operation: xir.OperationChat,
	}
}

func bindingsFor(session, model, targetID string) map[string]string {
	return map[string]string{BindingKey(session, model): targetID}
}

func planErrorSentinel(reason string) error {
	switch reason {
	case ReasonModelUnknown:
		return ErrModelUnknown
	case ReasonNoCandidate:
		return ErrNoCandidate
	case ReasonAllUnavailable:
		return ErrAllUnavailable
	case ReasonSessionUpstreamUnavailable:
		return ErrSessionUpstreamUnavailable
	case ReasonConversionUnsupported:
		return ErrConversionUnsupported
	default:
		return nil
	}
}

func requirePlanError(t *testing.T, plan *PlanResult, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("Plan() error = nil, want reason %q", want)
	}
	if got := ReasonOf(err); got != want {
		t.Fatalf("ReasonOf(error) = %q, want %q (error: %v)", got, want, err)
	}
	if plan == nil {
		t.Fatalf("Plan() plan = nil, want diagnostic plan for %q", want)
	}
	if plan.Reason != want {
		t.Fatalf("plan.Reason = %q, want %q", plan.Reason, want)
	}
	sentinel := planErrorSentinel(want)
	if sentinel != nil && !errors.Is(err, sentinel) {
		t.Fatalf("errors.Is(err, %v) = false for %v", sentinel, err)
	}
	var typed *PlanError
	if !errors.As(err, &typed) || typed.Reason != want {
		t.Fatalf("errors.As(*PlanError) = %#v, want reason %q", typed, want)
	}
}

func TestModeOrder(t *testing.T) {
	tests := []struct {
		name      string
		request   Request
		targets   []Target
		policy    Policy
		wantID    string
		wantMode  Mode
		wantWire  xir.Protocol
		wantError string
	}{
		{
			name:    "native wins over local and convert regardless of tier",
			request: testRequest(xir.ProtocolResponses),
			targets: []Target{
				testTarget("native", 1, xir.ProtocolResponses),
				testTarget("local", 500, xir.ProtocolChatCompletions),
				testTarget("convert", 1000, xir.ProtocolMessages),
			},
			policy:   Policy{AllowLocal: true, AllowConversion: true},
			wantID:   "native",
			wantMode: ModeNative,
			wantWire: xir.ProtocolResponses,
		},
		{
			name:    "local beats convert with no native candidate",
			request: testRequest(xir.ProtocolResponses),
			targets: []Target{
				testTarget("local", 1, xir.ProtocolChatCompletions),
				testTarget("convert", 1000, xir.ProtocolMessages),
			},
			policy:   Policy{AllowLocal: true, AllowConversion: true},
			wantID:   "local",
			wantMode: ModeLocal,
			wantWire: xir.ProtocolChatCompletions,
		},
		{
			name:    "convert is used when enabled and non-streaming",
			request: testRequest(xir.ProtocolChatCompletions),
			targets: []Target{
				testTarget("convert", 1, xir.ProtocolMessages),
			},
			policy:   Policy{AllowConversion: true},
			wantID:   "convert",
			wantMode: ModeConvert,
			wantWire: xir.ProtocolMessages,
		},
		{
			name: "streaming conversion is refused",
			request: func() Request {
				request := testRequest(xir.ProtocolChatCompletions)
				request.Stream = true
				return request
			}(),
			targets:   []Target{testTarget("convert", 1, xir.ProtocolMessages)},
			policy:    Policy{AllowConversion: true},
			wantError: ReasonConversionUnsupported,
		},
		{
			name:      "conversion disabled is reported as conversion unsupported",
			request:   testRequest(xir.ProtocolChatCompletions),
			targets:   []Target{testTarget("convert", 1, xir.ProtocolMessages)},
			policy:    Policy{},
			wantError: ReasonConversionUnsupported,
		},
		{
			name:    "responses falls back to conversion when local is disabled",
			request: testRequest(xir.ProtocolResponses),
			targets: []Target{
				testTarget("chat", 1, xir.ProtocolChatCompletions),
			},
			policy:   Policy{AllowConversion: true},
			wantID:   "chat",
			wantMode: ModeConvert,
			wantWire: xir.ProtocolChatCompletions,
		},
		{
			name:      "responses with local and conversion disabled is conversion unsupported",
			request:   testRequest(xir.ProtocolResponses),
			targets:   []Target{testTarget("chat", 1, xir.ProtocolChatCompletions)},
			policy:    Policy{},
			wantError: ReasonConversionUnsupported,
		},
		{
			name:      "target with no protocols is no candidate",
			request:   testRequest(xir.ProtocolChatCompletions),
			targets:   []Target{testTarget("empty", 1)},
			policy:    Policy{AllowConversion: true},
			wantError: ReasonNoCandidate,
		},
		{
			name:    "unavailable native falls through to a conversion candidate",
			request: testRequest(xir.ProtocolResponses),
			targets: []Target{
				func() Target {
					target := testTarget("native", 1000, xir.ProtocolResponses)
					target.Healthy = false
					return target
				}(),
				testTarget("convert", 1, xir.ProtocolMessages),
			},
			policy:   Policy{AllowConversion: true},
			wantID:   "convert",
			wantMode: ModeConvert,
			wantWire: xir.ProtocolMessages,
		},
		{
			name:    "model matches the upstream wire model case-insensitively",
			request: testRequest(xir.ProtocolChatCompletions),
			targets: []Target{
				func() Target {
					target := testTarget("wire", 1, xir.ProtocolChatCompletions)
					target.Model = "alias"
					target.WireModel = "upstream-wire-model"
					return target
				}(),
			},
			policy:   Policy{},
			wantID:   "wire",
			wantMode: ModeNative,
			wantWire: xir.ProtocolChatCompletions,
		},
		{
			name:    "all model targets unhealthy",
			request: testRequest(xir.ProtocolChatCompletions),
			targets: []Target{func() Target {
				target := testTarget("down", 1, xir.ProtocolChatCompletions)
				target.Healthy = false
				return target
			}()},
			policy:    Policy{},
			wantError: ReasonAllUnavailable,
		},
		{
			name:    "unknown model",
			request: testRequest(xir.ProtocolChatCompletions),
			targets: []Target{func() Target {
				target := testTarget("other", 1, xir.ProtocolChatCompletions)
				target.Model = "other-model"
				target.WireModel = "other-model"
				return target
			}()},
			policy:    Policy{},
			wantError: ReasonModelUnknown,
		},
		{
			name:      "empty model is unknown",
			request:   Request{Protocol: xir.ProtocolChatCompletions},
			targets:   []Target{testTarget("target", 1, xir.ProtocolChatCompletions)},
			policy:    Policy{},
			wantError: ReasonModelUnknown,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := test.request
			if test.name == "model matches the upstream wire model case-insensitively" {
				request.Model = "UPSTREAM-WIRE-MODEL"
			}

			plan, err := Plan(context.Background(), request, testSnapshot(test.targets...), test.policy)
			if test.wantError != "" {
				requirePlanError(t, plan, err, test.wantError)
				return
			}
			if err != nil {
				t.Fatalf("Plan() error = %v, want target %q", err, test.wantID)
			}
			if plan.Target.ID != test.wantID {
				t.Fatalf("plan.Target.ID = %q, want %q (candidates: %+v)", plan.Target.ID, test.wantID, plan.Candidates)
			}
			if plan.Mode != test.wantMode {
				t.Fatalf("plan.Mode = %q, want %q", plan.Mode, test.wantMode)
			}
			if plan.Wire != test.wantWire {
				t.Fatalf("plan.Wire = %q, want %q", plan.Wire, test.wantWire)
			}
			if plan.WireModel != "upstream-wire-model" && test.name == "model matches the upstream wire model case-insensitively" {
				t.Fatalf("plan.WireModel = %q, want upstream-wire-model", plan.WireModel)
			}
			if plan.Reason != PlanReasonSelected {
				t.Fatalf("plan.Reason = %q, want %q", plan.Reason, PlanReasonSelected)
			}
			if plan.Affinity.Status != AffinityStatusNone {
				t.Fatalf("plan.Affinity.Status = %q, want %q", plan.Affinity.Status, AffinityStatusNone)
			}
		})
	}
}

func TestCompatVersusStrict(t *testing.T) {
	slowHighPriority := testTarget("high-priority", 100, xir.ProtocolChatCompletions)
	slowHighPriority.LatencyMs = 500
	fastLowPriority := testTarget("low-priority", 10, xir.ProtocolChatCompletions)
	fastLowPriority.LatencyMs = 10
	request := testRequest(xir.ProtocolChatCompletions)

	t.Run("compat compares all tiers by score", func(t *testing.T) {
		plan, err := Plan(context.Background(), request,
			testSnapshot(slowHighPriority, fastLowPriority),
			Policy{PriorityMode: PriorityCompat})
		if err != nil {
			t.Fatalf("Plan() error = %v", err)
		}
		if plan.Target.ID != "low-priority" {
			t.Fatalf("plan.Target.ID = %q, want low-priority", plan.Target.ID)
		}
	})

	t.Run("strict keeps the highest priority tier", func(t *testing.T) {
		plan, err := Plan(context.Background(), request,
			testSnapshot(slowHighPriority, fastLowPriority),
			Policy{PriorityMode: PriorityStrict})
		if err != nil {
			t.Fatalf("Plan() error = %v", err)
		}
		if plan.Target.ID != "high-priority" {
			t.Fatalf("plan.Target.ID = %q, want high-priority", plan.Target.ID)
		}
		if plan.Tier != 100 {
			t.Fatalf("plan.Tier = %d, want 100", plan.Tier)
		}
		var lowerTierSelectable bool
		for _, candidate := range plan.Candidates {
			if candidate.Target.ID == "low-priority" && candidate.Selectable {
				lowerTierSelectable = true
			}
		}
		if !lowerTierSelectable {
			t.Fatal("strict plan should leave a selectable lower tier visible for failover")
		}
	})

	t.Run("strict descends a tier when the top tier is cooling down", func(t *testing.T) {
		cooling := slowHighPriority
		cooling.CooldownUntil = testNow.Add(time.Hour)
		plan, err := Plan(context.Background(), request,
			testSnapshot(cooling, fastLowPriority),
			Policy{PriorityMode: PriorityStrict})
		if err != nil {
			t.Fatalf("Plan() error = %v", err)
		}
		if plan.Target.ID != "low-priority" {
			t.Fatalf("plan.Target.ID = %q, want low-priority", plan.Target.ID)
		}
		for _, candidate := range plan.Candidates {
			if candidate.Target.ID == "high-priority" {
				if candidate.Selectable {
					t.Fatal("cooling high-priority candidate should not be selectable")
				}
				if candidate.FilterReason != FilterReasonCooldown {
					t.Fatalf("high-priority FilterReason = %q, want %q", candidate.FilterReason, FilterReasonCooldown)
				}
			}
		}
	})

	t.Run("compat uses priority only as a score tiebreak", func(t *testing.T) {
		low := testTarget("low", 10, xir.ProtocolChatCompletions)
		low.LatencyMs = 100
		high := testTarget("high", 100, xir.ProtocolChatCompletions)
		high.LatencyMs = 100
		// low is listed first: a stable implementation must still pick high.
		plan, err := Plan(context.Background(), request,
			testSnapshot(low, high),
			Policy{PriorityMode: PriorityCompat})
		if err != nil {
			t.Fatalf("Plan() error = %v", err)
		}
		if plan.Target.ID != "high" {
			t.Fatalf("plan.Target.ID = %q, want high", plan.Target.ID)
		}
	})
}

func TestSoftAffinity(t *testing.T) {
	fast := testTarget("fast", 100, xir.ProtocolChatCompletions)
	fast.LatencyMs = 10
	slow := testTarget("slow", 10, xir.ProtocolChatCompletions)
	slow.LatencyMs = 500
	request := testRequest(xir.ProtocolChatCompletions)
	request.Session = "raw-session-value"

	t.Run("bound target wins even when it scores worse", func(t *testing.T) {
		snapshot := testSnapshot(fast, slow)
		snapshot.Bindings = bindingsFor(request.Session, request.Model, "slow")

		plan, err := Plan(context.Background(), request, snapshot, Policy{})
		if err != nil {
			t.Fatalf("Plan() error = %v", err)
		}
		if plan.Target.ID != "slow" {
			t.Fatalf("plan.Target.ID = %q, want slow", plan.Target.ID)
		}
		if plan.Affinity.Status != AffinityStatusSoft {
			t.Fatalf("plan.Affinity.Status = %q, want %q", plan.Affinity.Status, AffinityStatusSoft)
		}
		if plan.Affinity.Failed {
			t.Fatal("soft affinity must not report Failed")
		}
		if plan.Affinity.TargetID != "slow" {
			t.Fatalf("plan.Affinity.TargetID = %q, want slow", plan.Affinity.TargetID)
		}
		if plan.Affinity.KeyFingerprint != BindingKey(request.Session, request.Model) {
			t.Fatalf("plan.Affinity.KeyFingerprint = %q, want BindingKey result", plan.Affinity.KeyFingerprint)
		}
		if strings.Contains(plan.Affinity.KeyFingerprint, request.Session) {
			t.Fatal("affinity fingerprint leaked the raw session")
		}
	})

	t.Run("strict affinity only applies inside the top selectable tier", func(t *testing.T) {
		snapshot := testSnapshot(fast, slow)
		snapshot.Bindings = bindingsFor(request.Session, request.Model, "slow")

		plan, err := Plan(context.Background(), request, snapshot, Policy{PriorityMode: PriorityStrict})
		if err != nil {
			t.Fatalf("Plan() error = %v", err)
		}
		if plan.Target.ID != "fast" {
			t.Fatalf("plan.Target.ID = %q, want fast", plan.Target.ID)
		}
		if plan.Affinity.Status != AffinityStatusSoft {
			t.Fatalf("plan.Affinity.Status = %q, want %q", plan.Affinity.Status, AffinityStatusSoft)
		}
		if plan.Affinity.TargetID != "slow" {
			t.Fatalf("plan.Affinity.TargetID = %q, want slow (the spilled binding)", plan.Affinity.TargetID)
		}
	})

	t.Run("unavailable bound target spills to another target", func(t *testing.T) {
		slowDown := slow
		slowDown.Healthy = false
		snapshot := testSnapshot(fast, slowDown)
		snapshot.Bindings = bindingsFor(request.Session, request.Model, "slow")

		plan, err := Plan(context.Background(), request, snapshot, Policy{})
		if err != nil {
			t.Fatalf("Plan() error = %v", err)
		}
		if plan.Target.ID != "fast" {
			t.Fatalf("plan.Target.ID = %q, want fast", plan.Target.ID)
		}
		if plan.Affinity.Status != AffinityStatusSoft {
			t.Fatalf("plan.Affinity.Status = %q, want %q", plan.Affinity.Status, AffinityStatusSoft)
		}
		if plan.Affinity.TargetID != "slow" {
			t.Fatalf("plan.Affinity.TargetID = %q, want slow", plan.Affinity.TargetID)
		}
	})

	t.Run("no session means no affinity lookup", func(t *testing.T) {
		requestNoSession := request
		requestNoSession.Session = ""
		snapshot := testSnapshot(fast, slow)
		snapshot.Bindings = bindingsFor("something-else", request.Model, "slow")

		plan, err := Plan(context.Background(), requestNoSession, snapshot, Policy{})
		if err != nil {
			t.Fatalf("Plan() error = %v", err)
		}
		if plan.Target.ID != "fast" {
			t.Fatalf("plan.Target.ID = %q, want fast", plan.Target.ID)
		}
		if plan.Affinity.Status != AffinityStatusNone || plan.Affinity.KeyFingerprint != "" {
			t.Fatalf("plan.Affinity = %+v, want none with empty fingerprint", plan.Affinity)
		}
	})
}

func TestHardAffinity(t *testing.T) {
	fast := testTarget("fast", 100, xir.ProtocolChatCompletions)
	fast.LatencyMs = 10
	slow := testTarget("slow", 10, xir.ProtocolChatCompletions)
	slow.LatencyMs = 500
	request := testRequest(xir.ProtocolChatCompletions)
	request.Session = "stateful-session"
	request.State = xir.StateRefs{PreviousResponseID: "resp_123"}

	t.Run("server state overrides tier and score", func(t *testing.T) {
		snapshot := testSnapshot(fast, slow)
		snapshot.Bindings = bindingsFor(request.Session, request.Model, "slow")

		plan, err := Plan(context.Background(), request, snapshot, Policy{PriorityMode: PriorityStrict})
		if err != nil {
			t.Fatalf("Plan() error = %v", err)
		}
		if plan.Target.ID != "slow" {
			t.Fatalf("plan.Target.ID = %q, want slow", plan.Target.ID)
		}
		if plan.Affinity.Status != AffinityStatusHard || plan.Affinity.Failed {
			t.Fatalf("plan.Affinity = %+v, want hard success", plan.Affinity)
		}
		for _, candidate := range plan.Candidates {
			if candidate.Target.ID == "fast" {
				if candidate.Selectable {
					t.Fatal("hard affinity left an unbound target selectable")
				}
				if candidate.FilterReason != FilterReasonHardAffinityElsewhere {
					t.Fatalf("fast FilterReason = %q, want %q", candidate.FilterReason, FilterReasonHardAffinityElsewhere)
				}
			}
		}
	})

	t.Run("unavailable bound target returns session_upstream_unavailable", func(t *testing.T) {
		slowDown := slow
		slowDown.Healthy = false
		snapshot := testSnapshot(fast, slowDown)
		snapshot.Bindings = bindingsFor(request.Session, request.Model, "slow")

		plan, err := Plan(context.Background(), request, snapshot, Policy{})
		requirePlanError(t, plan, err, ReasonSessionUpstreamUnavailable)
		if plan.Affinity.Status != AffinityStatusHard || !plan.Affinity.Failed {
			t.Fatalf("plan.Affinity = %+v, want failed hard affinity", plan.Affinity)
		}
		if plan.Affinity.TargetID != "slow" {
			t.Fatalf("plan.Affinity.TargetID = %q, want slow", plan.Affinity.TargetID)
		}
		if plan.Target.ID != "" {
			t.Fatalf("plan.Target.ID = %q, want empty on hard affinity failure", plan.Target.ID)
		}
	})

	t.Run("missing bound target returns session_upstream_unavailable", func(t *testing.T) {
		snapshot := testSnapshot(fast, slow)
		snapshot.Bindings = bindingsFor(request.Session, request.Model, "ghost")

		plan, err := Plan(context.Background(), request, snapshot, Policy{})
		requirePlanError(t, plan, err, ReasonSessionUpstreamUnavailable)
		if !plan.Affinity.Failed || plan.Affinity.TargetID != "ghost" {
			t.Fatalf("plan.Affinity = %+v, want failed affinity to ghost", plan.Affinity)
		}
	})

	t.Run("bound target for another model is unavailable", func(t *testing.T) {
		other := testTarget("other", 1, xir.ProtocolChatCompletions)
		other.Model = "other-model"
		other.WireModel = "other-model"
		snapshot := testSnapshot(fast, other)
		snapshot.Bindings = bindingsFor(request.Session, request.Model, "other")

		plan, err := Plan(context.Background(), request, snapshot, Policy{})
		requirePlanError(t, plan, err, ReasonSessionUpstreamUnavailable)
		if !plan.Affinity.Failed {
			t.Fatalf("plan.Affinity = %+v, want failed affinity", plan.Affinity)
		}
	})

	t.Run("server state without a binding routes normally", func(t *testing.T) {
		snapshot := testSnapshot(fast, slow)
		plan, err := Plan(context.Background(), request, snapshot, Policy{})
		if err != nil {
			t.Fatalf("Plan() error = %v", err)
		}
		if plan.Target.ID != "fast" {
			t.Fatalf("plan.Target.ID = %q, want fast", plan.Target.ID)
		}
		if plan.Affinity.Status != AffinityStatusNone {
			t.Fatalf("plan.Affinity.Status = %q, want %q", plan.Affinity.Status, AffinityStatusNone)
		}
	})
}

func TestFailoverAndCooldown(t *testing.T) {
	request := testRequest(xir.ProtocolChatCompletions)

	t.Run("cooling target is filtered and explained", func(t *testing.T) {
		cooling := testTarget("cooling", 100, xir.ProtocolChatCompletions)
		cooling.CooldownUntil = testNow.Add(time.Hour)
		fallback := testTarget("fallback", 1, xir.ProtocolChatCompletions)

		plan, err := Plan(context.Background(), request, testSnapshot(cooling, fallback), Policy{})
		if err != nil {
			t.Fatalf("Plan() error = %v", err)
		}
		if plan.Target.ID != "fallback" {
			t.Fatalf("plan.Target.ID = %q, want fallback", plan.Target.ID)
		}
		var found bool
		for _, candidate := range plan.Candidates {
			if candidate.Target.ID == "cooling" {
				found = true
				if candidate.Selectable {
					t.Fatal("cooling candidate is selectable")
				}
				if candidate.FilterReason != FilterReasonCooldown {
					t.Fatalf("cooling FilterReason = %q, want %q", candidate.FilterReason, FilterReasonCooldown)
				}
				if candidate.Score < scoreUnavailablePenalty {
					t.Fatalf("cooling score = %v, want unavailable penalty", candidate.Score)
				}
			}
		}
		if !found {
			t.Fatal("cooling candidate missing from explanation")
		}
	})

	t.Run("unhealthy target is filtered", func(t *testing.T) {
		down := testTarget("down", 100, xir.ProtocolChatCompletions)
		down.Healthy = false
		up := testTarget("up", 1, xir.ProtocolChatCompletions)

		plan, err := Plan(context.Background(), request, testSnapshot(down, up), Policy{})
		if err != nil {
			t.Fatalf("Plan() error = %v", err)
		}
		if plan.Target.ID != "up" {
			t.Fatalf("plan.Target.ID = %q, want up", plan.Target.ID)
		}
	})

	t.Run("cooldown boundary is inclusive at now", func(t *testing.T) {
		atBoundary := testTarget("boundary", 100, xir.ProtocolChatCompletions)
		atBoundary.CooldownUntil = testNow
		fallback := testTarget("fallback", 1, xir.ProtocolChatCompletions)

		plan, err := Plan(context.Background(), request, testSnapshot(atBoundary, fallback), Policy{})
		if err != nil {
			t.Fatalf("Plan() error = %v", err)
		}
		if plan.Target.ID != "boundary" {
			t.Fatalf("plan.Target.ID = %q, want boundary (cooldown ends at now)", plan.Target.ID)
		}
	})

	t.Run("every target unavailable", func(t *testing.T) {
		one := testTarget("one", 100, xir.ProtocolChatCompletions)
		one.Healthy = false
		two := testTarget("two", 50, xir.ProtocolChatCompletions)
		two.CooldownUntil = testNow.Add(time.Minute)

		plan, err := Plan(context.Background(), request, testSnapshot(one, two), Policy{})
		requirePlanError(t, plan, err, ReasonAllUnavailable)
		if len(plan.Candidates) != 2 {
			t.Fatalf("len(plan.Candidates) = %d, want 2", len(plan.Candidates))
		}
	})

	t.Run("score prefers low inflight then high weight", func(t *testing.T) {
		busy := testTarget("busy", 10, xir.ProtocolChatCompletions)
		busy.Inflight = 3
		idle := testTarget("idle", 10, xir.ProtocolChatCompletions)

		plan, err := Plan(context.Background(), request, testSnapshot(busy, idle), Policy{})
		if err != nil {
			t.Fatalf("Plan() error = %v", err)
		}
		if plan.Target.ID != "idle" {
			t.Fatalf("plan.Target.ID = %q, want idle", plan.Target.ID)
		}

		heavy := testTarget("heavy", 10, xir.ProtocolChatCompletions)
		heavy.Weight = 2
		heavy.LatencyMs = 100
		light := testTarget("light", 10, xir.ProtocolChatCompletions)
		light.Weight = 1
		light.LatencyMs = 160

		plan, err = Plan(context.Background(), request, testSnapshot(light, heavy), Policy{})
		if err != nil {
			t.Fatalf("Plan() error = %v", err)
		}
		if plan.Target.ID != "heavy" {
			t.Fatalf("plan.Target.ID = %q, want heavy (weight 2)", plan.Target.ID)
		}
	})

	t.Run("selection is deterministic across repeated calls", func(t *testing.T) {
		one := testTarget("one", 10, xir.ProtocolChatCompletions)
		one.LatencyMs = 77
		two := testTarget("two", 10, xir.ProtocolChatCompletions)
		two.LatencyMs = 77
		snapshot := testSnapshot(one, two)

		var first string
		for iteration := 0; iteration < 50; iteration++ {
			plan, err := Plan(context.Background(), request, snapshot, Policy{})
			if err != nil {
				t.Fatalf("Plan() error = %v", err)
			}
			if iteration == 0 {
				first = plan.Target.ID
				continue
			}
			if plan.Target.ID != first {
				t.Fatalf("iteration %d selected %q, first selected %q", iteration, plan.Target.ID, first)
			}
		}
	})
}

func TestConversionOnlyLast(t *testing.T) {
	t.Run("any native candidate beats every conversion candidate", func(t *testing.T) {
		native := testTarget("native", -100, xir.ProtocolChatCompletions)
		native.LatencyMs = 1000
		convert := testTarget("convert", 10000, xir.ProtocolMessages)
		convert.LatencyMs = 0

		plan, err := Plan(context.Background(), testRequest(xir.ProtocolChatCompletions),
			testSnapshot(convert, native), Policy{AllowConversion: true, PriorityMode: PriorityStrict})
		if err != nil {
			t.Fatalf("Plan() error = %v", err)
		}
		if plan.Target.ID != "native" {
			t.Fatalf("plan.Target.ID = %q, want native", plan.Target.ID)
		}
	})

	t.Run("conversion is used only after native candidates fail", func(t *testing.T) {
		native := testTarget("native", 100, xir.ProtocolChatCompletions)
		native.Healthy = false
		convert := testTarget("convert", 1, xir.ProtocolMessages)

		plan, err := Plan(context.Background(), testRequest(xir.ProtocolChatCompletions),
			testSnapshot(native, convert), Policy{AllowConversion: true})
		if err != nil {
			t.Fatalf("Plan() error = %v", err)
		}
		if plan.Target.ID != "convert" || plan.Mode != ModeConvert {
			t.Fatalf("plan = %+v, want convert target", plan)
		}
		if plan.Wire != xir.ProtocolMessages {
			t.Fatalf("plan.Wire = %q, want messages", plan.Wire)
		}
	})

	t.Run("conversion wire protocol has fixed preference", func(t *testing.T) {
		target := testTarget("multi", 1, xir.ProtocolGemini, xir.ProtocolMessages, xir.ProtocolResponses)
		plan, err := Plan(context.Background(), testRequest(xir.ProtocolChatCompletions),
			testSnapshot(target), Policy{AllowConversion: true})
		if err != nil {
			t.Fatalf("Plan() error = %v", err)
		}
		if plan.Wire != xir.ProtocolResponses {
			t.Fatalf("plan.Wire = %q, want responses (first after excluding chat)", plan.Wire)
		}
	})

	t.Run("local mode needs a chat backend", func(t *testing.T) {
		target := testTarget("messages", 1, xir.ProtocolMessages)
		plan, err := Plan(context.Background(), testRequest(xir.ProtocolResponses),
			testSnapshot(target), Policy{AllowLocal: true})
		requirePlanError(t, plan, err, ReasonConversionUnsupported)
	})
}

func TestExplainMatchesPlanAndDoesNotMutateSnapshot(t *testing.T) {
	targets := []Target{
		testTarget("one", 100, xir.ProtocolChatCompletions, xir.ProtocolMessages),
		testTarget("two", 10, xir.ProtocolChatCompletions),
	}
	targets[0].Protocols = append([]xir.Protocol(nil), targets[0].Protocols...)
	bindings := bindingsFor("session", "gpt-4o", "two")
	snapshot := Snapshot{
		Targets:  targets,
		Bindings: bindings,
		Now:      func() time.Time { return testNow },
	}
	request := testRequest(xir.ProtocolChatCompletions)
	request.Session = "session"
	policy := Policy{PriorityMode: PriorityStrict, AllowConversion: true}

	planFromPlan, err := Plan(context.Background(), request, snapshot, policy)
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	planFromExplain, err := Explain(context.Background(), request, snapshot, policy)
	if err != nil {
		t.Fatalf("Explain() error = %v", err)
	}
	if !reflect.DeepEqual(planFromPlan, planFromExplain) {
		t.Fatalf("Plan() = %#v, Explain() = %#v", planFromPlan, planFromExplain)
	}
	if !reflect.DeepEqual(snapshot.Targets, targets) {
		t.Fatalf("snapshot targets mutated: got %#v, want %#v", snapshot.Targets, targets)
	}
	if !reflect.DeepEqual(snapshot.Bindings, bindings) {
		t.Fatalf("snapshot bindings mutated: got %#v, want %#v", snapshot.Bindings, bindings)
	}
}

func TestBindingKeyFingerprint(t *testing.T) {
	key := BindingKey("raw-session", "gpt-4o")
	if len(key) != 64 {
		t.Fatalf("len(BindingKey) = %d, want 64", len(key))
	}
	if _, err := hex.DecodeString(key); err != nil {
		t.Fatalf("BindingKey is not hex: %v", err)
	}
	if strings.Contains(key, "raw-session") {
		t.Fatal("BindingKey contains the raw session")
	}
	if key != BindingKey("raw-session", "gpt-4o") {
		t.Fatal("BindingKey is not stable")
	}
	if key != BindingKey(" raw-session ", " GPT-4O ") {
		t.Fatal("BindingKey should trim and case-fold model")
	}
	if key == BindingKey("raw-session", "gpt-4.1") {
		t.Fatal("BindingKey must depend on the model")
	}
	if key == BindingKey("another-session", "gpt-4o") {
		t.Fatal("BindingKey must depend on the session")
	}
	if len(BindingKey("", "model")) != 64 {
		t.Fatal("BindingKey with an empty session should still be a fingerprint")
	}
}

func TestPlanPreservesWireModel(t *testing.T) {
	target := testTarget("alias", 1, xir.ProtocolChatCompletions)
	target.Model = "public-alias"
	target.WireModel = "vendor-wire-id"
	request := testRequest(xir.ProtocolChatCompletions)
	request.Model = "public-alias"

	plan, err := Plan(context.Background(), request, testSnapshot(target), Policy{})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if plan.WireModel != "vendor-wire-id" {
		t.Fatalf("plan.WireModel = %q, want vendor-wire-id", plan.WireModel)
	}
}

func TestPlanHonoursCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	plan, err := Plan(ctx, testRequest(xir.ProtocolChatCompletions),
		testSnapshot(testTarget("target", 1, xir.ProtocolChatCompletions)), Policy{})
	if err == nil {
		t.Fatal("Plan() error = nil, want context cancellation")
	}
	if plan != nil {
		t.Fatalf("Plan() = %#v, want nil on context cancellation", plan)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Plan() error = %v, want context.Canceled", err)
	}
}

func TestCandidateScoresAreStable(t *testing.T) {
	target := testTarget("target", 10, xir.ProtocolChatCompletions)
	first := scoreTarget(target, testNow)
	second := scoreTarget(target, testNow)
	if first != second {
		t.Fatalf("scoreTarget changed between calls: %v vs %v", first, second)
	}
	want := 100.0 + 100.0 // LatencyMs + 100/Weight
	if first != want {
		t.Fatalf("scoreTarget = %v, want %v", first, want)
	}

	target.Inflight = 2
	if got := scoreTarget(target, testNow); got != want+2*scoreInflightMs {
		t.Fatalf("scoreTarget with inflight = %v, want %v", got, want+2*scoreInflightMs)
	}
}

func TestCustomModeOrder(t *testing.T) {
	native := testTarget("native", 100, xir.ProtocolChatCompletions)
	convert := testTarget("convert", 1, xir.ProtocolMessages)
	request := testRequest(xir.ProtocolChatCompletions)

	plan, err := Plan(context.Background(), request, testSnapshot(native, convert), Policy{
		AllowConversion: true,
		ModeOrder:       []Mode{ModeConvert},
	})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if plan.Target.ID != "convert" || plan.Mode != ModeConvert {
		t.Fatalf("plan = %+v, want the explicitly ordered convert mode", plan)
	}
}

func TestHardAffinityWithDisabledMode(t *testing.T) {
	native := testTarget("native", 1, xir.ProtocolChatCompletions)
	request := testRequest(xir.ProtocolChatCompletions)
	request.Session = "session"
	request.State = xir.StateRefs{ConversationID: "conv_1"}
	snapshot := testSnapshot(native)
	snapshot.Bindings = bindingsFor(request.Session, request.Model, "native")

	plan, err := Plan(context.Background(), request, snapshot, Policy{ModeOrder: []Mode{ModeConvert}})
	requirePlanError(t, plan, err, ReasonSessionUpstreamUnavailable)
	if !plan.Affinity.Failed {
		t.Fatalf("plan.Affinity = %+v, want failed hard affinity", plan.Affinity)
	}
	for _, candidate := range plan.Candidates {
		if candidate.Target.ID == "native" && candidate.FilterReason != FilterReasonModeDisabled {
			t.Fatalf("native FilterReason = %q, want %q", candidate.FilterReason, FilterReasonModeDisabled)
		}
	}
}

func TestWireModelFallsBackToModel(t *testing.T) {
	target := testTarget("target", 1, xir.ProtocolChatCompletions)
	target.WireModel = ""
	plan, err := Plan(context.Background(), testRequest(xir.ProtocolChatCompletions), testSnapshot(target), Policy{})
	if err != nil {
		t.Fatalf("Plan() error = %v", err)
	}
	if plan.WireModel != target.Model {
		t.Fatalf("plan.WireModel = %q, want %q", plan.WireModel, target.Model)
	}
}

func TestPlanErrorFormatting(t *testing.T) {
	if got := ErrNoCandidate.Error(); !strings.Contains(got, ReasonNoCandidate) {
		t.Fatalf("ErrNoCandidate.Error() = %q, want reason", got)
	}
	detailed := &PlanError{Reason: ReasonModelUnknown, Detail: `model "x" is absent`}
	if got := detailed.Error(); !strings.Contains(got, ReasonModelUnknown) || !strings.Contains(got, "is absent") {
		t.Fatalf("PlanError.Error() = %q, want reason and detail", got)
	}
	if !errors.Is(detailed, ErrModelUnknown) {
		t.Fatal("errors.Is(detailed, ErrModelUnknown) = false")
	}
	if ReasonOf(errors.New("unrelated")) != "" {
		t.Fatal("ReasonOf(unrelated) should be empty")
	}
}
