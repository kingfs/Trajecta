package secretmask

import (
	"bytes"
	"testing"
)

// opaqueProtocolCases pins every protocol value the masker must leave byte for
// byte. Each body carries the same maskable xAI key inside the opaque value, so
// the only reason a case produces no event is that the walker classified the
// value as opaque. Adding a provider field is a one-line change in opaque.go and
// one case here.
func TestMaskSkipsKnownOpaqueProtocolValues(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "OpenAI Responses encrypted reasoning",
			body: `{"model":"gpt-5","input":[{"type":"reasoning","encrypted_content":"` + reportedXAIKey + `","summary":[]}]}`,
		},
		{
			name: "Anthropic thinking block keeps text and signature together",
			body: `{"model":"claude-sonnet-4-5","messages":[{"role":"assistant","content":[` +
				`{"type":"thinking","thinking":"the key is ` + reportedXAIKey + `","signature":"` + reportedXAIKey + `"}]}]}`,
		},
		{
			name: "Anthropic redacted thinking keeps the encrypted data",
			body: `{"model":"claude-sonnet-4-5","messages":[{"role":"assistant","content":[` +
				`{"type":"redacted_thinking","data":"` + reportedXAIKey + `"}]}]}`,
		},
		{
			name: "Anthropic base64 image source",
			body: `{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":[` +
				`{"type":"image","source":{"type":"base64","media_type":"image/png","data":"` + reportedXAIKey + `"}}]}]}`,
		},
		{
			name: "Gemini thoughtSignature",
			body: `{"contents":[{"role":"model","parts":[{"functionCall":{"name":"x"},"thoughtSignature":"` + reportedXAIKey + `"}]}]}`,
		},
		{
			name: "Gemini snake-case thought_signature",
			body: `{"contents":[{"role":"model","parts":[{"thought_signature":"` + reportedXAIKey + `"}]}]}`,
		},
		{
			name: "Gemini inlineData",
			body: `{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"` + reportedXAIKey + `"}}]}]}`,
		},
		{
			name: "Gemini snake-case inline_data",
			body: `{"contents":[{"role":"user","parts":[{"inline_data":{"mime_type":"image/png","data":"` + reportedXAIKey + `"}}]}]}`,
		},
		{
			name: "Bedrock reasoningText keeps text and signature together",
			body: `{"messages":[{"role":"assistant","content":[{"reasoningContent":{"reasoningText":{"text":"the key is ` + reportedXAIKey + `","signature":"` + reportedXAIKey + `"}}}]}]}`,
		},
		{
			name: "Bedrock redactedContent",
			body: `{"messages":[{"role":"assistant","content":[{"reasoningContent":{"redactedContent":"` + reportedXAIKey + `"}}]}]}`,
		},
		{
			name: "OpenAI Responses input_file file_data",
			body: `{"model":"gpt-5","input":[{"type":"input_file","file_data":"` + reportedXAIKey + `","filename":"a.pdf"}]}`,
		},
		{
			name: "OpenAI chat input_audio",
			body: `{"model":"gpt-5","messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"` + reportedXAIKey + `","format":"wav"}}]}]}`,
		},
		{
			name: "OpenAI Responses input_image data URI",
			body: `{"model":"gpt-5","input":[{"type":"input_image","image_url":"data:image/png;base64,` + reportedXAIKey + `"}]}`,
		},
		{
			name: "OpenAI chat image_url object with data URI",
			body: `{"model":"gpt-5","messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,` + reportedXAIKey + `"}}]}]}`,
		},
		{
			name: "generic url member with data URI",
			body: `{"metadata":{"url":"data:application/octet-stream;base64,` + reportedXAIKey + `"}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := testVault()
			masked, events := v.Mask([]byte(tc.body))
			if len(events) != 0 {
				t.Fatalf("masked %d value(s), want 0: %+v", len(events), events)
			}
			if !bytes.Equal(masked, []byte(tc.body)) {
				t.Fatalf("body changed although the value is opaque:\n got %q\nwant %q", masked, tc.body)
			}
		})
	}
}

// TestMaskStillScansGenericNamesOutsideOpaqueContainers is the other half of the
// contract: the generic members that the opaque rules deliberately do not list
// (signature, data, url, image_url) must still be masked outside a recognised
// opaque container, otherwise arbitrary JSON would become a place to hide a real
// credential.
func TestMaskStillScansGenericNamesOutsideOpaqueContainers(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "bare signature member",
			body: `{"signature":"` + reportedXAIKey + `"}`,
		},
		{
			name: "bare data member",
			body: `{"data":"` + reportedXAIKey + `"}`,
		},
		{
			name: "signature next to a lookalike type",
			body: `{"type":"thinkingx","signature":"` + reportedXAIKey + `"}`,
		},
		{
			name: "base64 source lookalike type",
			body: `{"source":{"type":"base64x","data":"` + reportedXAIKey + `"}}`,
		},
		{
			name: "remote url is still scanned",
			body: `{"url":"https://example.com/` + reportedXAIKey + `"}`,
		},
		{
			name: "remote image_url is still scanned",
			body: `{"image_url":"https://example.com/` + reportedXAIKey + `"}`,
		},
		{
			name: "text data URI is not a base64 payload",
			body: `{"url":"data:text/plain,` + reportedXAIKey + `"}`,
		},
		{
			name: "ordinary message content",
			body: `{"model":"gpt-5","messages":[{"role":"user","content":"` + reportedXAIKey + `"}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := testVault()
			masked, events := v.Mask([]byte(tc.body))
			if len(events) != 1 {
				t.Fatalf("masked %d value(s), want 1", len(events))
			}
			if bytes.Contains(masked, []byte(reportedXAIKey)) {
				t.Fatalf("the real key survived masking: %q", masked)
			}
			if len(masked) != len(tc.body) {
				t.Fatalf("body length changed: got %d, want %d", len(masked), len(tc.body))
			}
		})
	}
}

// TestMaskSkipsThinkingBlockAndMasksItsNeighbour pins the mixed body: only the
// message text is rewritten, while the signed thinking block beside it is
// untouched.
func TestMaskSkipsThinkingBlockAndMasksItsNeighbour(t *testing.T) {
	const messageKey = `xai-vNeighbourKey0123456789abcdefghijklmnopqrstuvwxyzABCDEFGH`
	if len(messageKey) < 44 || len(messageKey) > 104 {
		t.Fatalf("test key is %d bytes, outside the xai.api_key bounds", len(messageKey))
	}
	body := `{"model":"claude-sonnet-4-5","messages":[` +
		`{"role":"assistant","content":[{"type":"thinking","thinking":"` + reportedXAIKey + `","signature":"` + reportedXAIKey + `"}]},` +
		`{"role":"user","content":"the key is ` + messageKey + `"}]}`

	v := testVault()
	masked, events := v.Mask([]byte(body))
	if len(events) != 1 {
		t.Fatalf("masked %d value(s), want 1: %+v", len(events), events)
	}
	messageStart := bytes.LastIndex([]byte(body), []byte(messageKey))
	if messageStart < 0 {
		t.Fatal("fixture is wrong: the message key is not in the body")
	}
	if events[0].Start != messageStart || events[0].End != messageStart+len(messageKey) {
		t.Fatalf("masked span = [%d,%d), want the message occurrence at [%d,%d)",
			events[0].Start, events[0].End, messageStart, messageStart+len(messageKey))
	}
	thinkingStart := bytes.Index([]byte(body), []byte(`{"type":"thinking"`))
	thinkingEnd := thinkingStart + len(`{"type":"thinking","thinking":"`+reportedXAIKey+`","signature":"`+reportedXAIKey+`"}`)
	if !bytes.Equal(masked[thinkingStart:thinkingEnd], []byte(body)[thinkingStart:thinkingEnd]) {
		t.Fatalf("the thinking block was modified: %q", masked[thinkingStart:thinkingEnd])
	}
	if got := v.Restore(masked); !bytes.Equal(got, []byte(body)) {
		t.Fatalf("restore did not round-trip\n got %q\nwant %q", got, body)
	}
}
