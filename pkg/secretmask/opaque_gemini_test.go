package secretmask

import (
	"bytes"
	"testing"
)

// A Gemini thought part is bound to its thoughtSignature (or is itself
// reasoning state): the provider expects the part back unmodified, so the masker
// must leave every byte of it alone while still masking a sibling text part.
func TestMaskLeavesGeminiThoughtPartUntouched(t *testing.T) {
	body := `{"contents":[{"role":"model","parts":[` +
		`{"text":"my key is ` + reportedXAIKey + `","thought":true},` +
		`{"text":"visible key ` + reportedXAIKey + `"}]}]}`
	v := testVault()
	masked, events := v.Mask([]byte(body))
	if len(events) != 1 {
		t.Fatalf("masked %d value(s), want 1 (the sibling text): %+v", len(events), events)
	}
	thoughtStart := bytes.Index([]byte(body), []byte(reportedXAIKey))
	visibleStart := bytes.LastIndex([]byte(body), []byte(reportedXAIKey))
	if thoughtStart < 0 || visibleStart == thoughtStart {
		t.Fatal("fixture is wrong: need the key in both the thought part and the sibling")
	}
	if got := string(masked[thoughtStart : thoughtStart+len(reportedXAIKey)]); got != reportedXAIKey {
		t.Fatalf("thought text was rewritten: %q", got)
	}
	if string(masked[visibleStart:visibleStart+len(reportedXAIKey)]) == reportedXAIKey {
		t.Fatal("the sibling text was not rewritten")
	}
}

func TestMaskLeavesGeminiSignaturePartUntouched(t *testing.T) {
	body := `{"contents":[{"role":"model","parts":[` +
		`{"text":"key ` + reportedXAIKey + `","thoughtSignature":"c2ln"}]}]}`
	v := testVault()
	masked, events := v.Mask([]byte(body))
	if len(events) != 0 {
		t.Fatalf("signed part was rewritten: %+v", events)
	}
	if !bytes.Contains(masked, []byte(reportedXAIKey)) {
		t.Fatal("signed part lost its bytes")
	}
}

// An unmodeled native part is not signature-bound: the credential inside it must
// still be masked.
func TestMaskMasksGeminiExecutableCode(t *testing.T) {
	body := `{"contents":[{"role":"model","parts":[` +
		`{"executableCode":{"language":"PYTHON","code":"key = '` + reportedXAIKey + `'"}}]}]}`
	v := testVault()
	masked, events := v.Mask([]byte(body))
	if len(events) != 1 {
		t.Fatalf("masked %d value(s), want 1: %+v", len(events), events)
	}
	if bytes.Contains(masked, []byte(reportedXAIKey)) {
		t.Fatal("the executable code still carries the real key")
	}
}
