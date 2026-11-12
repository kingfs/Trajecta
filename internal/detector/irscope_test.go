package detector

import (
	"bytes"
	"testing"

	"github.com/kingfs/Trajecta/pkg/detect"
)

// spanCovers reports whether needle occurs wholly inside one of spans (relative
// to body). The hot path needs a credential in any non-sealed position — even a
// structural JSON key — to lie inside a scannable span.
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

func TestScannableSpansCoversStructuralPositions(t *testing.T) {
	// A credential in a JSON key, a user field and a metadata field: none is a
	// projected text node, but all must remain scannable (coverage preservation).
	cases := []string{
		`{"model":"gpt-5","messages":[{"role":"user","content":"hi"}],"` + corpusAWSKey + `":"v"}`,
		`{"model":"gpt-5","user":"` + corpusAWSKey + `","messages":[{"role":"user","content":"hi"}]}`,
		`{"model":"gpt-5","metadata":{"token":"` + corpusAWSKey + `"},"messages":[{"role":"user","content":"hi"}]}`,
	}
	for _, body := range cases {
		scope, ok := ParseRequestScope("chat_completions", []byte(body))
		if !ok {
			t.Fatalf("expected scope for %s", body)
		}
		if !spanCovers([]byte(body), scope.ScannableSpans(), corpusAWSKey) {
			t.Fatalf("structural credential not scannable: %s", body)
		}
	}
}

func TestScannableSpansExcludesSealed(t *testing.T) {
	body := []byte(`{"model":"gpt-5","input":[{"type":"reasoning","encrypted_content":"` + corpusAWSKey + `gAAAA opaque","summary":[]},{"type":"message","role":"user","content":[{"type":"input_text","text":"key ` + corpusAWSKey + `"}]}]}`)
	scope, ok := ParseRequestScope("responses", body)
	if !ok {
		t.Fatal("expected scope for a valid responses body")
	}
	spans := scope.ScannableSpans()
	if !spanCovers(body, spans, "key "+corpusAWSKey) {
		t.Fatalf("visible credential not scannable: %+v", spans)
	}
	firstIdx := bytes.Index(body, []byte(corpusAWSKey))
	for _, s := range spans {
		if s.Start <= firstIdx && firstIdx+len(corpusAWSKey) <= s.End {
			t.Fatalf("sealed encrypted credential is scannable by span %+v", s)
		}
	}
}

func TestScannableSpansNoSealedIsWholeBody(t *testing.T) {
	body := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"key ` + corpusAWSKey + `"}]}`)
	scope, ok := ParseRequestScope("chat_completions", body)
	if !ok {
		t.Fatal("expected scope")
	}
	spans := scope.ScannableSpans()
	if len(spans) != 1 || spans[0].Start != 0 || spans[0].End != len(body) {
		t.Fatalf("no-sealed scope should be the whole body, got %+v", spans)
	}
}

func TestComplementSpans(t *testing.T) {
	cases := []struct {
		name     string
		excluded []detect.Span
		n        int
		want     []detect.Span
	}{
		{"none", nil, 10, []detect.Span{{Start: 0, End: 10}}},
		{"middle", []detect.Span{{Start: 3, End: 6}}, 10, []detect.Span{{Start: 0, End: 3}, {Start: 6, End: 10}}},
		{"leading", []detect.Span{{Start: 0, End: 4}}, 10, []detect.Span{{Start: 4, End: 10}}},
		{"trailing", []detect.Span{{Start: 6, End: 10}}, 10, []detect.Span{{Start: 0, End: 6}}},
		{"all", []detect.Span{{Start: 0, End: 10}}, 10, nil},
		{"overlap", []detect.Span{{Start: 2, End: 5}, {Start: 3, End: 7}}, 10, []detect.Span{{Start: 0, End: 2}, {Start: 7, End: 10}}},
		{"unsorted", []detect.Span{{Start: 6, End: 8}, {Start: 1, End: 3}}, 10, []detect.Span{{Start: 0, End: 1}, {Start: 3, End: 6}, {Start: 8, End: 10}}},
		{"out_of_range", []detect.Span{{Start: -1, End: 3}, {Start: 8, End: 20}}, 10, []detect.Span{{Start: 0, End: 10}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := complementSpans(tc.excluded, tc.n)
			if len(got) != len(tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got %+v, want %+v", got, tc.want)
				}
			}
		})
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
