package router

import "testing"

func TestResolveModelAliasForProtocol(t *testing.T) {
	target := &Target{
		ModelAliases: map[string]string{"gpt-5": "fallback-wire"},
		ProtocolModelAliases: map[string]map[string]string{
			"chat_completions": {"gpt-5": "chat-wire"},
			"messages":         {"gpt-5": "msg-wire"},
		},
	}
	if got := target.ResolveModelAliasForProtocol("GPT-5", "chat_completions"); got != "chat-wire" {
		t.Fatalf("chat alias = %q", got)
	}
	if got := target.ResolveModelAliasForProtocol("gpt-5", "messages"); got != "msg-wire" {
		t.Fatalf("messages alias = %q", got)
	}
	if got := target.ResolveModelAliasForProtocol("gpt-5", "responses"); got != "fallback-wire" {
		t.Fatalf("fallback alias = %q", got)
	}
}

func TestResolveModelAliasForProtocolUsesUpstreamModel(t *testing.T) {
	target := &Target{
		ModelWireNames: map[string]string{"gpt-5": "provider/private-gpt-5"},
	}
	if got := target.ResolveModelAliasForProtocol("gpt-5", "chat_completions"); got != "provider/private-gpt-5" {
		t.Fatalf("upstream_model fallback = %q", got)
	}
	if got := target.ResolveModelAliasForProtocol("gpt-5", "messages"); got != "provider/private-gpt-5" {
		t.Fatalf("upstream_model fallback for messages = %q", got)
	}
}

func TestResolveModelAliasForProtocolAppliesAliasTargetWire(t *testing.T) {
	target := &Target{
		ModelAliases: map[string]string{"friendly": "gpt-5"},
		ProtocolModelAliases: map[string]map[string]string{
			"chat_completions": {"gpt-5": "wire-chat"},
			"messages":         {"gpt-5": "wire-msg"},
		},
	}
	if got := target.ResolveModelAliasForProtocol("friendly", "chat_completions"); got != "wire-chat" {
		t.Fatalf("alias -> target protocol wire = %q", got)
	}
	if got := target.ResolveModelAliasForProtocol("friendly", "messages"); got != "wire-msg" {
		t.Fatalf("alias -> target messages wire = %q", got)
	}
}

func TestResolveModelAliasForProtocolAliasWithoutWireUsesTarget(t *testing.T) {
	target := &Target{ModelAliases: map[string]string{"friendly": "gpt-5"}}
	if got := target.ResolveModelAliasForProtocol("friendly", "chat_completions"); got != "gpt-5" {
		t.Fatalf("alias without wire = %q", got)
	}
}

func TestResolveModelAliasForProtocolProtocolEntryBeatsUpstreamModel(t *testing.T) {
	target := &Target{
		ModelWireNames: map[string]string{"gpt-5": "provider/private-gpt-5"},
		ProtocolModelAliases: map[string]map[string]string{
			"chat_completions": {"gpt-5": "wire-chat"},
		},
	}
	if got := target.ResolveModelAliasForProtocol("gpt-5", "chat_completions"); got != "wire-chat" {
		t.Fatalf("protocol wire should beat upstream_model = %q", got)
	}
	if got := target.ResolveModelAliasForProtocol("gpt-5", "responses"); got != "provider/private-gpt-5" {
		t.Fatalf("responses should fall back to upstream_model = %q", got)
	}
}
