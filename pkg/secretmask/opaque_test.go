package secretmask

import (
	"bytes"
	"testing"
)

// reportedEncryptedContent is the value from the false positive that motivated
// opaque JSON fields: an OpenAI Responses reasoning item's ciphertext carried a
// run that the credential DFA reports as an xAI API key. The value is kept
// verbatim so the regression test pins the exact bytes that failed.
const reportedEncryptedContent = "orsr1_wooEp4YZRn73XhBnGQVNFetZXg9dt6ofKlIiTVNvVqRRbWE81orIoDsL5y1nCLGT5sS_0mDm1zY1a68j8pT8QIEm2AaJda8KX-0sV-wIQAFVCV_hxV9zI-vFe6EY3ELzjZFEG9vYqjQBF-W9VqD34P_fRSBZP4mU40u4YfqABH_ombQebY_I6BQiy9JbvgSstbXaMs-71P46yNsVwlQdL9KJ2Gsz2wiYH5NBU96eRmeNLW0gxai-3xZ4uADr20ElWRZWhOTPcVeV332eFnjgj6IlK9PEDoti5TmEpJXSb086boDFpIRi-"

// reportedXAIKey is the exact false-positive match inside
// reportedEncryptedContent. It is also a legitimate key value, so it is what
// the tests below put in a normal string to prove masking still works beside a
// skipped blob.
const reportedXAIKey = "xai-3xZ4uADr20ElWRZWhOTPcVeV332eFnjgj6IlK9PEDoti5TmEpJXSb086boDFpIRi"

// TestReportedEncryptedContentIsMaskableWithoutGuard proves the sample above
// really is what the detector considers a rewrite-worthy xAI key. Without this
// guard the skip tests could pass because the sample never matched in the first
// place.
func TestReportedEncryptedContentIsMaskableWithoutGuard(t *testing.T) {
	findings := Scan([]byte(reportedEncryptedContent))
	if len(findings) != 1 {
		t.Fatalf("findings = %+v, want one", findings)
	}
	if findings[0].ID != RuleXAIAPIKey || !findings[0].Maskable() {
		t.Fatalf("finding = %+v, want a maskable xai.api_key", findings[0])
	}
	if got := string(findings[0].Bytes([]byte(reportedEncryptedContent))); got != reportedXAIKey {
		t.Fatalf("matched %q, want %q", got, reportedXAIKey)
	}
}

// TestMaskLeavesEncryptedContentUntouched is the end-to-end unit test for the
// reported failure: a Responses request carries a reasoning item's ciphertext
// and a real key in a message. The ciphertext must reach the upstream byte for
// byte while the key beside it is still rewritten and restorable.
func TestMaskLeavesEncryptedContentUntouched(t *testing.T) {
	body := `{"model":"gpt-5","include":["reasoning.encrypted_content"],` +
		`"input":[` +
		`{"type":"reasoning","encrypted_content":"` + reportedEncryptedContent + `","summary":[]},` +
		`{"type":"message","role":"user","content":"my key is ` + reportedXAIKey + `"}` +
		`]}`

	v := testVault()
	masked, events := v.Mask([]byte(body))
	if len(events) != 1 {
		t.Fatalf("masked %d value(s), want 1: %+v", len(events), events)
	}
	if events[0].RuleName() != "xai.api_key" {
		t.Fatalf("rule = %q, want xai.api_key", events[0].RuleName())
	}
	realStart := bytes.LastIndex([]byte(body), []byte(reportedXAIKey))
	if realStart < 0 {
		t.Fatal("fixture is wrong: the real key is not in the body")
	}
	if events[0].Start != realStart || events[0].End != realStart+len(reportedXAIKey) {
		t.Fatalf("masked span = [%d,%d), want the message occurrence at [%d,%d)",
			events[0].Start, events[0].End, realStart, realStart+len(reportedXAIKey))
	}
	if got := string(masked[events[0].Start:events[0].End]); got == reportedXAIKey {
		t.Fatal("the real key was not replaced")
	}

	opaqueStart := bytes.Index([]byte(body), []byte(reportedEncryptedContent))
	if opaqueStart < 0 {
		t.Fatal("fixture is wrong: the ciphertext is not in the body")
	}
	opaqueEnd := opaqueStart + len(reportedEncryptedContent)
	if !bytes.Equal(masked[opaqueStart:opaqueEnd], []byte(reportedEncryptedContent)) {
		t.Fatalf("the ciphertext was rewritten: %q", masked[opaqueStart:opaqueEnd])
	}
	if len(masked) != len(body) {
		t.Fatalf("body length changed: %d, want %d", len(masked), len(body))
	}
	if got := v.Restore(masked); !bytes.Equal(got, []byte(body)) {
		t.Fatalf("restore did not round-trip\n got %q\nwant %q", got, body)
	}
}

// TestOpaqueJSONFieldMatchingIsStructural pins that only a real JSON object
// member is skipped. A prompt is allowed to contain the member name as text,
// and that text must not become a place to hide a credential from the masker.
func TestOpaqueJSONFieldMatchingIsStructural(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantEvents int
	}{
		{
			name:       "top-level member",
			body:       `{"encrypted_content":"` + reportedXAIKey + `"}`,
			wantEvents: 0,
		},
		{
			name:       "nested member",
			body:       `{"input":[{"reasoning":{"encrypted_content":"` + reportedXAIKey + `"}}]}`,
			wantEvents: 0,
		},
		{
			name:       "whitespace around the colon",
			body:       "{\n  \"encrypted_content\"\t:\n  \"" + reportedXAIKey + "\"\n}",
			wantEvents: 0,
		},
		{
			name:       "duplicate member",
			body:       `{"encrypted_content":"` + reportedXAIKey + `","encrypted_content":"` + reportedXAIKey + `"}`,
			wantEvents: 0,
		},
		{
			name:       "non-string value",
			body:       `{"encrypted_content":["` + reportedXAIKey + `"]}`,
			wantEvents: 0,
		},
		{
			name:       "member name inside a string is not a field",
			body:       `{"content":"the field \"encrypted_content\" is set to ` + reportedXAIKey + `"}`,
			wantEvents: 1,
		},
		{
			name:       "member name in prose is not a field",
			body:       `encrypted_content: ` + reportedXAIKey,
			wantEvents: 1,
		},
		{
			name:       "truncated JSON fails closed and still masks",
			body:       `{"encrypted_content":"` + reportedXAIKey + `"`,
			wantEvents: 1,
		},
		{
			name:       "trailing bytes make the body untrusted and fail closed",
			body:       `{"encrypted_content":"` + reportedXAIKey + `"}garbage`,
			wantEvents: 1,
		},
		{
			name:       "trailing whitespace keeps the document valid",
			body:       "{\"encrypted_content\":\"" + reportedXAIKey + "\"}\n\t ",
			wantEvents: 0,
		},
		{
			// The prefilter is a literal byte search, so an ASCII member name
			// spelled with a JSON \u escape is a documented boundary, not a
			// skipped isolation. No standard JSON encoder emits that spelling.
			name:       "escaped member name is the documented prefilter boundary",
			body:       `{"encrypted_\u0063ontent":"` + reportedXAIKey + `"}`,
			wantEvents: 1,
		},
		{
			name:       "top-level array has no members",
			body:       `["` + reportedXAIKey + `"]`,
			wantEvents: 1,
		},
		{
			name:       "real key beside the opaque member",
			body:       `{"encrypted_content":"` + reportedXAIKey + `","content":"` + reportedXAIKey + `"}`,
			wantEvents: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			v := testVault()
			masked, events := v.Mask([]byte(tc.body))
			if len(events) != tc.wantEvents {
				t.Fatalf("masked %d value(s), want %d: %+v", len(events), tc.wantEvents, events)
			}
			if tc.wantEvents == 0 && !bytes.Equal(masked, []byte(tc.body)) {
				t.Fatalf("body changed although nothing should be masked: %q", masked)
			}
		})
	}
}

// TestOpaqueJSONValueSpans pins the byte range itself: Mask filters findings by
// overlap, so a range that starts or ends in the wrong place would either leak
// the blob or skip the neighbouring comma.
func TestOpaqueJSONValueSpans(t *testing.T) {
	src := []byte(`{"a":{"encrypted_content" : "\u0041 \\ "},"b":[{"encrypted_content":"x"}]}`)
	spans := opaqueJSONValueSpans(src)
	want := []string{`"\u0041 \\ "`, `"x"`}
	if len(spans) != len(want) {
		t.Fatalf("spans = %+v, want %d", spans, len(want))
	}
	for i, span := range spans {
		if got := string(src[span.start:span.end]); got != want[i] {
			t.Fatalf("span %d = %q, want %q", i, got, want[i])
		}
	}
}
