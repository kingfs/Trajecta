package router

// Eligibility tests for the conversion-aware path check and the hard
// passthrough-first rule. Native passthrough must win whenever a native
// candidate is available; conversion only widens eligibility, and never for
// streaming requests or the Responses entrypoint.

import (
	"testing"

	"github.com/kingfs/Trajecta/internal/upstream"
)

func chatTarget(id string) *Target {
	return &Target{ID: id, Upstream: upstream.ResolvedUpstream{
		ProtocolFamily: upstream.ProtocolFamilyOpenAICompatible,
		APIType:        upstream.APITypeChatCompletions,
	}}
}

func anthropicTarget(id string) *Target {
	return &Target{ID: id, Upstream: upstream.ResolvedUpstream{
		ProtocolFamily: upstream.ProtocolFamilyAnthropicMessages,
		APIType:        upstream.APITypeMessages,
	}}
}

func TestConvertAwareSupportsPath(t *testing.T) {
	chat := chatTarget("chat")
	anthropic := anthropicTarget("anthropic")
	features := RequestFeatures{ModelName: "m"}

	// Native passthrough is unaffected by the flag.
	if !convertAwareSupportsPath(chat, "/v1/chat/completions", features, false) {
		t.Error("native chat on chat target should be supported")
	}
	if !convertAwareSupportsPath(anthropic, "/v1/messages", features, false) {
		t.Error("native messages on anthropic target should be supported")
	}

	// Mismatched protocol: disabled means ineligible (historical behaviour).
	if convertAwareSupportsPath(chat, "/v1/messages", features, false) {
		t.Error("messages on chat target must be ineligible when conversion is disabled")
	}

	// Enabled, non-streaming: eligible through conversion.
	if !convertAwareSupportsPath(chat, "/v1/messages", features, true) {
		t.Error("messages on chat target should be eligible when conversion is enabled")
	}
	if !convertAwareSupportsPath(anthropic, "/v1/chat/completions", features, true) {
		t.Error("chat on anthropic target should be eligible when conversion is enabled")
	}

	// Enabled, streaming: conversion is excluded by design.
	streamFeatures := features
	streamFeatures.Stream = true
	if convertAwareSupportsPath(chat, "/v1/messages", streamFeatures, true) {
		t.Error("streaming messages on chat target must stay ineligible: streaming conversion is unsupported")
	}
	// Native streaming passthrough still works.
	if !convertAwareSupportsPath(chat, "/v1/chat/completions", streamFeatures, true) {
		t.Error("native streaming chat passthrough should stay eligible")
	}

	// The Responses entrypoint is owned by the local runtime, never the
	// forwarding conversion path. A chat-capable target is still eligible for
	// the runtime's own Chat Completions orchestration, but an upstream that
	// could only answer through conversion is not.
	if convertAwareSupportsPath(anthropic, "/v1/responses", features, true) {
		t.Error("the Responses entrypoint must never be converted in the forwarding path")
	}
}

func TestPreferNativeCandidates(t *testing.T) {
	chat := chatTarget("chat")
	anthropic := anthropicTarget("anthropic")
	features := RequestFeatures{ModelName: "m"}

	// Both a native and a converting candidate are available: only the native
	// one survives, regardless of measured latency or priority.
	got := preferNativeCandidates([]*Target{anthropic, chat}, "/v1/chat/completions", features)
	if len(got) != 1 || got[0] != chat {
		t.Fatalf("native-first filter = %v, want only the chat target", got)
	}
	got = preferNativeCandidates([]*Target{chat, anthropic}, "/v1/messages", features)
	if len(got) != 1 || got[0] != anthropic {
		t.Fatalf("native-first filter = %v, want only the anthropic target", got)
	}

	// No native candidate: the converting fallback is kept.
	got = preferNativeCandidates([]*Target{anthropic}, "/v1/chat/completions", features)
	if len(got) != 1 || got[0] != anthropic {
		t.Fatalf("conversion fallback was dropped: %v", got)
	}

	// All native: unchanged.
	got = preferNativeCandidates([]*Target{chat}, "/v1/chat/completions", features)
	if len(got) != 1 || got[0] != chat {
		t.Fatalf("single native candidate was dropped: %v", got)
	}
}

// TestLocalResponsesBackendIgnoresConversionTargets pins P0-1: the local
// Responses runtime forwards a Chat body verbatim and never converts, so a
// Messages-only target must not count as its chat backend even when
// protocol conversion is enabled.
func TestLocalResponsesBackendIgnoresConversionTargets(t *testing.T) {
	anthropic := anthropicTarget("anthropic")
	r := &Router{policy: PolicyFirstAvailable, costs: defaultCostConfig(), conversionEnabled: true}
	features := RequestFeatures{ModelName: "m"}
	// Conversion-aware eligibility: chat on an Anthropic target is eligible
	// only through conversion.
	if !r.supportsPathWithConversion(anthropic, "/v1/chat/completions", features, true) {
		t.Fatal("conversion-aware check should find the anthropic target eligible via conversion")
	}
	// The local Responses runtime check (native-only) must not: it speaks
	// Chat natively or not at all.
	if r.supportsPathWithConversion(anthropic, "/v1/chat/completions", features, false) {
		t.Fatal("native-only check must not count the anthropic target as a local Responses chat backend")
	}
}
