package router

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kingfs/Trajecta/internal/config"
)

// nativeResponsesRequirementRouter builds the pair the routing decision has to keep
// apart: a target that serves the Responses API itself, and a Chat Completions target
// that can only serve /v1/responses through the local runtime's translation.
func nativeResponsesRequirementRouter(t *testing.T, upstreams ...config.UpstreamTargetConfig) *Router {
	t.Helper()
	cfg := &config.Config{Upstreams: upstreams}
	cfg.Router.Selection.Policy = PolicyFirstAvailable

	rtr, err := New(cfg, nil)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := rtr.Initialize(); err != nil {
		t.Fatalf("Initialize() error = %v", err)
	}
	return rtr
}

func nativeResponsesTarget() config.UpstreamTargetConfig {
	responsesEnabled := true
	chatDisabled := false
	return config.UpstreamTargetConfig{
		ID:             "native-responses",
		Enabled:        boolPtr(true),
		Priority:       200,
		ModelDiscovery: ModelDiscoveryStaticOnly,
		StaticModels:   []string{"gpt-5"},
		Upstream: config.UpstreamConfig{
			BaseURL:        "https://api.openai.com/v1",
			ProviderPreset: "openai",
			APIType:        "responses_native",
			Mode:           "proxy",
			Capabilities: config.UpstreamCapabilitiesConfig{
				Responses:       &responsesEnabled,
				ChatCompletions: &chatDisabled,
			},
		},
	}
}

func chatOnlyTarget() config.UpstreamTargetConfig {
	return config.UpstreamTargetConfig{
		ID:             "chat",
		Enabled:        boolPtr(true),
		Priority:       100,
		ModelDiscovery: ModelDiscoveryStaticOnly,
		StaticModels:   []string{"gpt-5"},
		Upstream: config.UpstreamConfig{
			BaseURL:        "https://compat.example.com/v1",
			ProviderPreset: "openai",
			APIType:        "chat_completions",
			Mode:           "responses_server",
		},
	}
}

func responsesPassThroughRequest(t *testing.T, body []byte, marked bool) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "http://proxy.local/v1/responses", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if marked {
		req = WithNativeResponsesPathRequirement(req)
	}
	return req
}

// TestNativeResponsesPassThroughNeverSelectsAChatOnlyTarget pins the boundary between the
// two eligibility questions.
//
// "Can this model be served on /v1/responses at all" deliberately accepts a Chat
// Completions backend, because the local runtime translates for it. A pass-through does
// not translate: it forwards the request unchanged, so that backend would receive a
// Responses request it does not implement. The proxy marks the request when its
// native-versus-local decision lands on pass-through, and the marked request must not
// select a target that cannot serve the path natively - including on the retry
// re-selection, which is why the mark travels in the request context.
//
// Observed before the constraint: with a native target failing over to a chat-only
// target, the live suite saw a chat upstream receive POST /v1/responses.
func TestNativeResponsesPassThroughNeverSelectsAChatOnlyTarget(t *testing.T) {
	body := []byte(`{"model":"gpt-5","input":"hello"}`)
	rtr := nativeResponsesRequirementRouter(t, nativeResponsesTarget(), chatOnlyTarget())

	// Unmarked, the chat-only target stays a legitimate answer for the local runtime, so
	// excluding the native target still routes to it. This is the behaviour the fix must
	// not remove.
	selection, err := rtr.SelectWithExclusion(responsesPassThroughRequest(t, body, false), body, []string{"native-responses"})
	if err != nil {
		t.Fatalf("unmarked selection with the native target excluded: error = %v, want the chat backend", err)
	}
	if selection.Target.ID != "chat" {
		t.Fatalf("unmarked selection = %q, want chat", selection.Target.ID)
	}

	// Marked, the same exclusion must not fall back to a target that cannot serve the
	// path natively: failing the request is correct, silently changing the protocol is not.
	_, err = rtr.SelectWithExclusion(responsesPassThroughRequest(t, body, true), body, []string{"native-responses"})
	if err == nil {
		t.Fatal("a marked Responses pass-through selected a target that does not serve the Responses API")
	}
	if got := SelectionFailureReason(err); got != SelectionFailureAllTargetsExcluded {
		t.Fatalf("failure reason = %q, want %q", got, SelectionFailureAllTargetsExcluded)
	}

	// The requirement alone does the excluding: with only the chat backend configured, a
	// marked pass-through has no candidate at all rather than a wrong one.
	chatOnly := nativeResponsesRequirementRouter(t, chatOnlyTarget())
	_, err = chatOnly.SelectWithExclusion(responsesPassThroughRequest(t, body, true), body, nil)
	if err == nil {
		t.Fatal("a marked Responses pass-through selected the only target, which does not serve the Responses API")
	}
	if got := SelectionFailureReason(err); got != SelectionFailureNoSupportingTarget {
		t.Fatalf("chat-only failure reason = %q, want %q", got, SelectionFailureNoSupportingTarget)
	}
	// The same router still serves the unmarked request, so the local runtime keeps its
	// backend.
	if selection, err := chatOnly.SelectWithExclusion(responsesPassThroughRequest(t, body, false), body, nil); err != nil {
		t.Fatalf("unmarked chat-only selection: error = %v", err)
	} else if selection.Target.ID != "chat" {
		t.Fatalf("unmarked chat-only selection = %q, want chat", selection.Target.ID)
	}

	// With the native target available the marked request selects it and the trace says
	// why the chat target was left out - while still reporting that the target supports
	// the path through the local runtime.
	selection, err = rtr.SelectWithExclusion(responsesPassThroughRequest(t, body, true), body, nil)
	if err != nil {
		t.Fatalf("marked selection: error = %v", err)
	}
	if selection.Target.ID != "native-responses" {
		t.Fatalf("marked selection = %q, want native-responses", selection.Target.ID)
	}
	if selection.Decision == nil {
		t.Fatal("marked selection has no decision trace")
	}
	foundChat := false
	for _, candidate := range selection.Decision.Candidates {
		if candidate.ID != "chat" {
			continue
		}
		foundChat = true
		if candidate.Selectable {
			t.Fatal("the chat-only target is selectable for a marked Responses pass-through")
		}
		if candidate.FilterReason != "native_responses_required" {
			t.Fatalf("chat candidate filter_reason = %q, want native_responses_required", candidate.FilterReason)
		}
		if !candidate.SupportsPath {
			t.Fatal("SupportsPath must stay true: the local runtime can still serve this target on the path")
		}
	}
	if !foundChat {
		t.Fatal("the decision trace lost the chat candidate")
	}

	// The constraint is about the Responses create path only: a marked request for the
	// Chat Completions path is still served by the chat backend.
	chatReq := httptest.NewRequest(http.MethodPost, "http://proxy.local/v1/chat/completions", bytes.NewReader(body))
	chatReq.Header.Set("Content-Type", "application/json")
	chatReq = WithNativeResponsesPathRequirement(chatReq)
	selection, err = rtr.SelectWithExclusion(chatReq, body, []string{"native-responses"})
	if err != nil {
		t.Fatalf("marked /v1/chat/completions selection: error = %v", err)
	}
	if selection.Target.ID != "chat" {
		t.Fatalf("marked /v1/chat/completions selection = %q, want chat", selection.Target.ID)
	}
}
