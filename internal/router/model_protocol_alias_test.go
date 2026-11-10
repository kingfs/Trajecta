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
