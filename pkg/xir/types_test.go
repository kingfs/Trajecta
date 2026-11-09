package xir

import (
	"encoding/json"
	"testing"
)

func TestParseProtocolAliases(t *testing.T) {
	cases := map[string]Protocol{
		"chat":             ProtocolChatCompletions,
		"chat_completions": ProtocolChatCompletions,
		"openai-chat":      ProtocolChatCompletions,
		"responses":        ProtocolResponses,
		"messages":         ProtocolMessages,
		"anthropic":        ProtocolMessages,
		"claude":           ProtocolMessages,
		"gemini":           ProtocolGemini,
		"generate_content": ProtocolGemini,
		"vertex":           ProtocolGemini,
	}
	for in, want := range cases {
		got, err := ParseProtocol(in)
		if err != nil {
			t.Fatalf("ParseProtocol(%q): %v", in, err)
		}
		if got != want {
			t.Fatalf("ParseProtocol(%q) = %q, want %q", in, got, want)
		}
	}
	if _, err := ParseProtocol("unknown"); err == nil {
		t.Fatal("unknown protocol accepted")
	}
}

func TestCapabilities(t *testing.T) {
	c := Capabilities{ChatCompletions: true, Messages: true}
	if !c.Has(ProtocolChatCompletions) || !c.Has(ProtocolMessages) {
		t.Fatal("expected protocols missing")
	}
	if c.Has(ProtocolResponses) {
		t.Fatal("unexpected responses capability")
	}
	got := c.Protocols()
	if len(got) != 2 || got[0] != ProtocolChatCompletions || got[1] != ProtocolMessages {
		t.Fatalf("Protocols() = %v", got)
	}
}

func TestStateRefsHasServerState(t *testing.T) {
	if (StateRefs{PromptCacheKey: "x"}).HasServerState() {
		t.Fatal("prompt cache key must not be server state")
	}
	if !(StateRefs{PreviousResponseID: "r"}).HasServerState() {
		t.Fatal("previous_response_id must be server state")
	}
	if !(StateRefs{ConversationID: "c"}).HasServerState() {
		t.Fatal("conversation must be server state")
	}
}

func TestLossError(t *testing.T) {
	l := Loss{Field: "reasoning", Reason: "cannot carry", Fatal: true, From: ProtocolResponses, To: ProtocolChatCompletions}
	if l.Error() == "" {
		t.Fatal("empty error")
	}
}

func TestParseDocument(t *testing.T) {
	doc, err := ParseDocument(ProtocolChatCompletions, OperationChat, []byte(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	if doc.Protocol != ProtocolChatCompletions || doc.Operation != OperationChat {
		t.Fatalf("doc = %+v", doc)
	}
	if m, _ := doc.Root.StringField("model"); m != "m" {
		t.Fatalf("model = %q", m)
	}
}

func TestSealedAndExtensionsRoundTrip(t *testing.T) {
	req := &Request{
		Model:      "m",
		Extensions: map[string]json.RawMessage{"responses": json.RawMessage(`{"store":true}`)},
	}
	raw, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	var got Request
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if string(got.Extensions["responses"]) != `{"store":true}` {
		t.Fatalf("extension = %s", got.Extensions["responses"])
	}
}
