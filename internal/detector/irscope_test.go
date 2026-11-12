package detector

import (
	"bytes"
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/pkg/detect"
	"github.com/kingfs/Trajecta/pkg/xir/hook"
)

// spanCovers reports whether needle occurs wholly inside one of spans (relative
// to body). It is the property the hot path needs: a credential in a scannable
// field must lie inside a declared span so the scanner can find it.
func spanCovers(body []byte, spans []detect.Span, needle string) bool {
	idx := bytes.Index(body, []byte(needle))
	if idx < 0 {
		return false
	}
	end := idx + len(needle)
	for _, s := range spans {
		if s.Start <= idx && end <= s.End {
			return true
		}
	}
	return false
}

func secretmaskPoints() []hook.Point {
	reg, _ := Lookup(PluginSecretmask)
	return reg.Plugin.Points
}

func TestParseRequestScopeChatCoversCredential(t *testing.T) {
	body := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"my key is ` + corpusAWSKey + ` please use it"}]}`)
	scope, ok := ParseRequestScope("chat_completions", body)
	if !ok {
		t.Fatal("expected scope for a valid chat body")
	}
	spans := scope.SpansFor(secretmaskPoints())
	if !spanCovers(body, spans, corpusAWSKey) {
		t.Fatalf("credential not covered by any span: %+v", spans)
	}
}

func TestParseRequestScopeExcludesSealed(t *testing.T) {
	// The encrypted_content carries a credential-shaped run; the visible message
	// carries a real one. Only the latter may be covered by a scannable span.
	body := []byte(`{"model":"gpt-5","input":[{"type":"reasoning","encrypted_content":"` + corpusAWSKey + `gAAAA opaque","summary":[]},{"type":"message","role":"user","content":[{"type":"input_text","text":"key ` + corpusAWSKey + `"}]}]}`)
	scope, ok := ParseRequestScope("responses", body)
	if !ok {
		t.Fatal("expected scope for a valid responses body")
	}
	spans := scope.SpansFor(secretmaskPoints())
	// The visible message credential is covered.
	if !spanCovers(body, spans, "key "+corpusAWSKey) {
		t.Fatalf("visible credential not covered: %+v", spans)
	}
	// The encrypted credential (first occurrence, inside encrypted_content) must
	// NOT be covered by any span.
	firstIdx := bytes.Index(body, []byte(corpusAWSKey))
	for _, s := range spans {
		if s.Start <= firstIdx && firstIdx+len(corpusAWSKey) <= s.End {
			t.Fatalf("sealed encrypted credential covered by span %+v", s)
		}
	}
}

func TestParseRequestScopeToolCallArguments(t *testing.T) {
	body := []byte(`{"model":"gpt-5","messages":[{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"run","arguments":"{\"cmd\":\"echo ` + corpusAWSKey + `\"}"}}]}]}`)
	scope, ok := ParseRequestScope("chat_completions", body)
	if !ok {
		t.Fatal("expected scope for a tool-call body")
	}
	spans := scope.SpansFor(secretmaskPoints())
	if !spanCovers(body, spans, corpusAWSKey) {
		t.Fatalf("tool-call credential not covered: %+v", spans)
	}
}

func TestParseRequestScopeUnknownProtocolFallsBack(t *testing.T) {
	if _, ok := ParseRequestScope("unknown", []byte(`{}`)); ok {
		t.Fatal("unknown protocol must fall back")
	}
	if _, ok := ParseRequestScope("chat_completions", []byte(`not json`)); ok {
		t.Fatal("malformed body must fall back")
	}
}

func TestParseRequestScopeGeminiExcludesMedia(t *testing.T) {
	body := []byte(`{"contents":[{"role":"user","parts":[{"inlineData":{"mimeType":"image/png","data":"` + corpusAWSKey + `base64data"}},{"text":"key ` + corpusAWSKey + `"}]}]}`)
	scope, ok := ParseRequestScope("gemini", body)
	if !ok {
		t.Fatal("expected scope for a gemini body")
	}
	spans := scope.SpansFor(secretmaskPoints())
	if !spanCovers(body, spans, "key "+corpusAWSKey) {
		t.Fatalf("visible gemini credential not covered: %+v", spans)
	}
	// The inline media credential (first occurrence) must not be covered.
	firstIdx := strings.Index(string(body), corpusAWSKey)
	for _, s := range spans {
		if s.Start <= firstIdx && firstIdx+len(corpusAWSKey) <= s.End {
			t.Fatalf("sealed media credential covered by span %+v", s)
		}
	}
}
