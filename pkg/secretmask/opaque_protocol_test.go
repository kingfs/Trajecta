package secretmask

import (
	"bytes"
	"strings"
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
		{
			name: "data URI with media-type parameters",
			body: `{"url":"data:image/svg+xml;charset=utf-8;base64,` + reportedXAIKey + `"}`,
		},
		{
			name: "text/plain base64 data URI is still a payload",
			body: `{"url":"data:text/plain;base64,` + reportedXAIKey + `"}`,
		},
		{
			name: "OpenRouter reasoning item keeps content, summary and signature",
			body: `{"input":[{"type":"reasoning","summary":[{"type":"summary_text","text":"t"}],` +
				`"signature":"` + reportedXAIKey + `","encrypted_content":"` + reportedXAIKey + `"}]}`,
		},
		{
			name: "OpenRouter reasoning_details keeps signature and encrypted data",
			body: `{"messages":[{"role":"assistant","reasoning_details":[` +
				`{"type":"reasoning.text","text":"t","signature":"` + reportedXAIKey + `"},` +
				`{"type":"reasoning.encrypted","data":"` + reportedXAIKey + `"},` +
				`{"type":"reasoning.summary","summary":"` + reportedXAIKey + `"}]}]}`,
		},
		{
			name: "thinking block with type last",
			body: `{"messages":[{"content":[{"thinking":"` + reportedXAIKey + `","signature":"` + reportedXAIKey + `","type":"thinking"}]}]}`,
		},
		{
			name: "duplicate type last wins: thinking",
			body: `{"messages":[{"content":[{"type":"message","type":"thinking","thinking":"` + reportedXAIKey + `"}]}]}`,
		},
		{
			name: "escaped type is decoded when the walk runs",
			body: `{"a":{"encrypted_content":"opaque"},"b":{"type":"\u0074hinking","thinking":"` + reportedXAIKey + `","signature":"` + reportedXAIKey + `"}}`,
		},
		{
			name: "container behind a scoped member name is still walked",
			body: `{"outer":{"data":{"encrypted_content":"` + reportedXAIKey + `"}}}`,
		},
		{
			name: "standalone reasoningText member",
			body: `{"reasoningText":{"text":"` + reportedXAIKey + `","signature":"` + reportedXAIKey + `"}}`,
		},
		{
			name: "standalone redactedContent member",
			body: `{"redactedContent":"` + reportedXAIKey + `"}`,
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
			name: "uppercase data scheme is not a data URI",
			body: `{"url":"DATA:image/png;base64,` + reportedXAIKey + `"}`,
		},
		{
			name: "uppercase base64 marker is not recognised",
			body: `{"url":"data:image/png;BASE64,` + reportedXAIKey + `"}`,
		},
		{
			name: "over-long media type before the marker",
			body: `{"url":"data:` + strings.Repeat("a", 140) + `;base64,` + reportedXAIKey + `"}`,
		},
		{
			name: "non-string type does not make the object opaque",
			body: `{"type":["base64"],"data":"` + reportedXAIKey + `"}`,
		},
		{
			name: "duplicate type last wins: message keeps the data scanned",
			body: `{"type":"base64","type":"message","data":"` + reportedXAIKey + `"}`,
		},
		{
			name: "only the scoped data member is skipped in a base64 source",
			body: `{"type":"base64","data":"opaque","token":"` + reportedXAIKey + `"}`,
		},
		{
			name: "escaped type without a literal trigger stays unparsed",
			body: `{"type":"\u0074hinking","text":"` + reportedXAIKey + `"}`,
		},
		{
			name: "reasoning lookalike type",
			body: `{"type":"reasoningx","signature":"` + reportedXAIKey + `"}`,
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

// TestOpaqueJSONValueSpansWholeObjects pins the byte range itself, not just the
// mask/no-mask outcome: the whole-object rule must cover exactly `{...}` and the
// scoped base64 rule must cover exactly its `data` value.
func TestOpaqueJSONValueSpansWholeObjects(t *testing.T) {
	t.Run("type last thinking block", func(t *testing.T) {
		src := []byte(`{"content":[{"thinking":"a b","signature":"c","type":"thinking"},{"type":"message","content":"x"}]}`)
		want := `{"thinking":"a b","signature":"c","type":"thinking"}`
		spans := opaqueJSONValueSpans(src)
		if len(spans) != 1 {
			t.Fatalf("spans = %+v, want one whole-object span", spans)
		}
		if got := string(src[spans[0].start:spans[0].end]); got != want {
			t.Fatalf("span = %q, want %q", got, want)
		}
	})
	t.Run("type first thinking block", func(t *testing.T) {
		src := []byte(`{"content":[{"type":"thinking","thinking":"a b","signature":"c"}]}`)
		want := `{"type":"thinking","thinking":"a b","signature":"c"}`
		spans := opaqueJSONValueSpans(src)
		if len(spans) != 1 || string(src[spans[0].start:spans[0].end]) != want {
			t.Fatalf("spans = %+v, want exactly %q", spans, want)
		}
	})
	t.Run("scoped base64 data", func(t *testing.T) {
		src := []byte(`{"type":"base64","media_type":"image/png","data":"raw"}`)
		spans := opaqueJSONValueSpans(src)
		if len(spans) != 1 || string(src[spans[0].start:spans[0].end]) != `"raw"` {
			t.Fatalf("spans = %+v, want exactly the data value", spans)
		}
	})
	t.Run("duplicate type last wins", func(t *testing.T) {
		src := []byte(`{"type":"base64","type":"message","data":"raw"}`)
		if spans := opaqueJSONValueSpans(src); len(spans) != 0 {
			t.Fatalf("spans = %+v, want none", spans)
		}
	})
	t.Run("escaped type is decoded once the walk runs", func(t *testing.T) {
		src := []byte(`{"a":{"encrypted_content":"opaque"},"b":{"type":"\u0074hinking","thinking":"a b"}}`)
		want := `{"type":"\u0074hinking","thinking":"a b"}`
		spans := opaqueJSONValueSpans(src)
		found := false
		for _, span := range spans {
			if got := string(src[span.start:span.end]); got == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("spans = %+v, want whole object %q", spans, want)
		}
	})
	t.Run("non-string type", func(t *testing.T) {
		src := []byte(`{"type":["base64"],"data":"raw"}`)
		if spans := opaqueJSONValueSpans(src); len(spans) != 0 {
			t.Fatalf("spans = %+v, want none", spans)
		}
	})
}
