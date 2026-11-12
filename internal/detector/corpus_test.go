package detector

// This file holds the differential-correctness corpus for the hot-path masking
// work. Each entry is a realistic request body for one of the four wire
// protocols, shaped to exercise a specific invariant: a maskable credential in
// ordinary text, personal data, a name-anchored assignment, credentials hidden
// inside opaque/encrypted blocks that must NOT be masked, tool-call arguments,
// and a credential-free body. The protocol is carried so the IR-scoped path can
// parse the body with the right codec; the byte path ignores it.
//
// The credentials are deliberately maskable without surrounding context where
// possible (the AWS access key id is Strong + exact length; the national ID
// passes its checksum), so a differential failure points at the path under test
// rather than at a context-scoring subtlety.

const (
	corpusAWSKey    = "AKIAIOSFODNN7EXAMPLE"
	corpusChineseID = "11010519491231002X"
	corpusEnvValue  = "sk-test-abcdefghijklmnopqrstuvwxyz0123456789"
)

type corpusEntry struct {
	name     string
	protocol string
	body     string
}

func maskCorpus() []corpusEntry {
	return []corpusEntry{
		{
			name:     "chat_small_credential",
			protocol: "chat_completions",
			body:     `{"model":"gpt-5","stream":false,"messages":[{"role":"user","content":"my key is ` + corpusAWSKey + ` please use it"}]}`,
		},
		{
			name:     "chat_pii",
			protocol: "chat_completions",
			body:     `{"model":"gpt-5","messages":[{"role":"user","content":"身份证 ` + corpusChineseID + ` 手机号 13800138000"}]}`,
		},
		{
			name:     "chat_env_assignment",
			protocol: "chat_completions",
			body:     `{"model":"gpt-5","messages":[{"role":"user","content":"OPENAI_API_KEY=` + corpusEnvValue + `"}]}`,
		},
		{
			name:     "chat_no_secret",
			protocol: "chat_completions",
			body:     `{"model":"gpt-5","messages":[{"role":"user","content":"Please review commit 3f2a9c8e1b4d5a6f7c8e9d0a1b2c3d4e5f607182 and blob d41d8cd98f00b204e9800998ecf8427e; id 550e8400-e29b-41d4-a716-446655440000 is not a credential."}]}`,
		},
		{
			name:     "chat_tool_call_arguments",
			protocol: "chat_completions",
			body:     `{"model":"gpt-5","messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"run","arguments":"{\"cmd\":\"echo ` + corpusAWSKey + `\"}"}}]}]}`,
		},
		{
			name:     "responses_encrypted_reasoning",
			protocol: "responses",
			// The encrypted_content carries a credential-shaped run that must
			// round-trip untouched; the visible message carries a real one.
			body: `{"model":"gpt-5","input":[{"type":"reasoning","encrypted_content":"` + corpusAWSKey + `gAAAA opaque","summary":[]},{"type":"message","role":"user","content":[{"type":"input_text","text":"key ` + corpusAWSKey + `"}]}]}`,
		},
		{
			name:     "messages_thinking_block",
			protocol: "messages",
			body:     `{"model":"claude","max_tokens":1024,"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"` + corpusAWSKey + `","signature":"sig"},{"type":"text","text":"key ` + corpusAWSKey + `"}]},{"role":"user","content":"thanks"}]}`,
		},
		{
			name:     "gemini_inline_media",
			protocol: "gemini",
			body:     `{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"` + corpusAWSKey + `base64data"}},{"text":"key ` + corpusAWSKey + `"}]}]}`,
		},
		{
			name:     "chat_large_sparse",
			protocol: "chat_completions",
			body:     largeChatBody(40, 8),
		},
		// Coverage-boundary cases: credentials in positions the audit projection
		// does not represent as Text. The hot path must still mask them (the IR
		// scan universe is the whole body minus sealed spans, not just text
		// fields), so these pin parity with the byte path.
		{
			name:     "chat_credential_in_json_key",
			protocol: "chat_completions",
			body:     `{"model":"gpt-5","messages":[{"role":"user","content":"hi"}],"` + corpusAWSKey + `":"structural-value"}`,
		},
		{
			name:     "chat_credential_in_user_field",
			protocol: "chat_completions",
			body:     `{"model":"gpt-5","user":"` + corpusAWSKey + `","messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name:     "chat_credential_in_metadata",
			protocol: "chat_completions",
			body:     `{"model":"gpt-5","metadata":{"token":"` + corpusAWSKey + `"},"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			name:     "messages_structured_tool_result",
			protocol: "messages",
			body:     `{"model":"claude","max_tokens":256,"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","content":[{"type":"text","text":"` + corpusAWSKey + `"}]}]}]}`,
		},
	}
}

// largeChatBody builds a multi-turn chat body of roughly n turns with a
// credential every every turns, padded with transcript-shaped noise.
func largeChatBody(turns, every int) string {
	noise := "Please review commit 3f2a9c8e1b4d5a6f7c8e9d0a1b2c3d4e5f607182 and the helper func handleRequest. " +
		"The upstream returned 502 after 30s; check resp.StatusCode and the retry budget. " +
		"Blob d41d8cd98f00b204e9800998ecf8427e and id 550e8400-e29b-41d4-a716-446655440000 are not credentials. "
	body := `{"model":"gpt-5","stream":false,"messages":[`
	for i := 0; i < turns; i++ {
		if i > 0 {
			body += ","
		}
		body += `{"role":"user","content":"` + noise
		if every > 0 && i%every == 0 {
			body += "OPENAI_API_KEY=" + corpusEnvValue
		}
		body += `"}`
	}
	body += `]}`
	return body
}
