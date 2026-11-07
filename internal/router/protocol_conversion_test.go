package router

// Eligibility tests for the conversion-aware path check. Native passthrough
// must win and must be the only answer when conversion is disabled;
// conversion only widens eligibility, and never for streaming requests.

import (
	"testing"

	"github.com/kingfs/Trajecta/internal/upstream"
)

func TestConvertAwareSupportsPath(t *testing.T) {
	chatTarget := &Target{Upstream: upstream.ResolvedUpstream{
		ProtocolFamily: upstream.ProtocolFamilyOpenAICompatible,
		APIType:        upstream.APITypeChatCompletions,
	}}
	anthropicTarget := &Target{Upstream: upstream.ResolvedUpstream{
		ProtocolFamily: upstream.ProtocolFamilyAnthropicMessages,
		APIType:        upstream.APITypeMessages,
	}}
	features := RequestFeatures{ModelName: "m"}

	// Native passthrough is unaffected by the flag.
	if !convertAwareSupportsPath(chatTarget, "/v1/chat/completions", features, false) {
		t.Error("native chat on chat target should be supported")
	}
	if !convertAwareSupportsPath(anthropicTarget, "/v1/messages", features, false) {
		t.Error("native messages on anthropic target should be supported")
	}

	// Mismatched protocol: disabled means ineligible (historical behaviour).
	if convertAwareSupportsPath(chatTarget, "/v1/messages", features, false) {
		t.Error("messages on chat target must be ineligible when conversion is disabled")
	}

	// Enabled, non-streaming: eligible through conversion.
	if !convertAwareSupportsPath(chatTarget, "/v1/messages", features, true) {
		t.Error("messages on chat target should be eligible when conversion is enabled")
	}
	if !convertAwareSupportsPath(anthropicTarget, "/v1/chat/completions", features, true) {
		t.Error("chat on anthropic target should be eligible when conversion is enabled")
	}

	// Enabled, streaming: conversion is excluded by design.
	streamFeatures := features
	streamFeatures.Stream = true
	if convertAwareSupportsPath(chatTarget, "/v1/messages", streamFeatures, true) {
		t.Error("streaming messages on chat target must stay ineligible: streaming conversion is unsupported")
	}
	// Native streaming passthrough still works.
	if !convertAwareSupportsPath(chatTarget, "/v1/chat/completions", streamFeatures, true) {
		t.Error("native streaming chat passthrough should stay eligible")
	}

	// Endpoints without conversion vocabulary keep the native rule.
	if convertAwareSupportsPath(chatTarget, "/v1/models", features, true) == false {
		// /v1/models is native-supported for every family; just assert it
		// does not error and follows the native answer.
		t.Error("/v1/models eligibility should follow the native rule")
	}
}

func TestPickScorePenalizesConversionCandidates(t *testing.T) {
	r := &Router{policy: PolicyFirstAvailable, costs: defaultCostConfig()}
	chatTarget := &Target{Upstream: upstream.ResolvedUpstream{
		ProtocolFamily: upstream.ProtocolFamilyOpenAICompatible,
		APIType:        upstream.APITypeChatCompletions,
	}}
	features := RequestFeatures{ModelName: "m"}

	nativeScore := r.pickScore(chatTarget, features, "/v1/chat/completions")
	convertScore := r.pickScore(chatTarget, features, "/v1/messages")
	if convertScore != nativeScore*conversionCostPenaltyFactor {
		t.Errorf("convert score = %v, want %v * %v", convertScore, nativeScore, conversionCostPenaltyFactor)
	}
	// pick must prefer the native candidate even though both are eligible
	// under conversion and their measured costs are equal.
	anthropicTarget := &Target{Upstream: upstream.ResolvedUpstream{
		ProtocolFamily: upstream.ProtocolFamilyAnthropicMessages,
		APIType:        upstream.APITypeMessages,
	}}
	picked, _ := r.pick([]*Target{chatTarget, anthropicTarget}, features, "/v1/chat/completions")
	if picked != chatTarget {
		t.Error("pick should prefer the native chat target over the anthropic (convert) target at equal cost")
	}
}
