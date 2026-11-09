package xir

import "testing"

func TestParseEnvelope(t *testing.T) {
	raw := []byte(`{
		"model":"gpt-5",
		"stream":true,
		"tools":[{"type":"function","function":{"name":"f"}}],
		"response_format":{"type":"json_schema"},
		"max_output_tokens":128,
		"previous_response_id":"r1",
		"prompt_cache_key":"c1",
		"conversation_id":"conv",
		"metadata":{"session_id":"s1"}
	}`)
	env, err := ParseEnvelope(raw)
	if err != nil {
		t.Fatal(err)
	}
	if env.Model != "gpt-5" || !env.Stream || !env.Tools || !env.StructuredOutput || env.MaxTokens != 128 {
		t.Fatalf("env = %+v", env)
	}
	if env.PreviousResponseID != "r1" || env.PromptCacheKey != "c1" || env.ConversationID != "conv" || env.SessionID != "s1" {
		t.Fatalf("state = %+v", env)
	}
}

func TestParseEnvelopeGeminiMaxTokens(t *testing.T) {
	env, err := ParseEnvelope([]byte(`{"contents":[],"generationConfig":{"maxOutputTokens":64}}`))
	if err != nil {
		t.Fatal(err)
	}
	if env.MaxTokens != 64 {
		t.Fatalf("MaxTokens = %v, want 64", env.MaxTokens)
	}
}

func TestParseEnvelopeMalformed(t *testing.T) {
	if _, err := ParseEnvelope([]byte(`{bad`)); err == nil {
		t.Fatal("malformed body accepted")
	}
	if _, err := ParseEnvelope([]byte(`[]`)); err == nil {
		t.Fatal("array root accepted")
	}
	if env, err := ParseEnvelope(nil); err != nil || env.Model != "" {
		t.Fatalf("nil body = %+v/%v", env, err)
	}
}

func TestParseEnvelopeStreamSpellings(t *testing.T) {
	for _, body := range []string{`{"stream":true}`, `{"stream":"true"}`, `{"stream":"TRUE"}`, `{"stream":1}`} {
		env, err := ParseEnvelope([]byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if !env.Stream {
			t.Fatalf("%s: stream not recognized", body)
		}
	}
	if env, _ := ParseEnvelope([]byte(`{"stream":false}`)); env.Stream {
		t.Fatal("stream false recognized as true")
	}
}
