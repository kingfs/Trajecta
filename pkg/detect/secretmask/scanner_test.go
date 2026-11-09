package secretmask

import (
	"context"
	"testing"

	"github.com/kingfs/Trajecta/pkg/detect"
)

func TestScannerFindsAssignedKey(t *testing.T) {
	body := []byte("prefix\nOPENAI_API_KEY=abcdefghijklmnopqrstuvwxyz123456\nsuffix")
	scanner := New()
	matches, err := scanner.Scan(context.Background(), detect.Input{Raw: body})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %+v", matches)
	}
	m := matches[0]
	if m.Start < 0 || m.End > len(body) || m.Value == "" {
		t.Fatalf("bad span/value: %+v", m)
	}
	if m.Metadata["maskable"] != "true" {
		t.Fatalf("maskable = %q", m.Metadata["maskable"])
	}
	if m.Rule == "" || m.Confidence <= 0 {
		t.Fatalf("rule/confidence = %q/%v", m.Rule, m.Confidence)
	}
}

func TestScannerRespectsDeclaredSpans(t *testing.T) {
	raw := []byte("OPENAI_API_KEY=abcdefghijklmnopqrstuvwxyz123456")
	// Declare a span that excludes the key.
	matches, err := New().Scan(context.Background(), detect.Input{
		Raw:   raw,
		Spans: []detect.Span{{Start: 0, End: 5, Path: "$.safe"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("scanned outside declared span: %+v", matches)
	}
}

func TestScannerOffsetsIntoFullBuffer(t *testing.T) {
	raw := []byte("xxxx\nOPENAI_API_KEY=abcdefghijklmnopqrstuvwxyz123456")
	span := detect.Span{Start: 5, End: len(raw), Path: "$.message"}
	matches, err := New().Scan(context.Background(), detect.Input{Raw: raw, Spans: []detect.Span{span}})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches = %+v", matches)
	}
	if matches[0].Start < span.Start {
		t.Fatalf("offset %d is before scope %d", matches[0].Start, span.Start)
	}
	if matches[0].Start < span.Start || matches[0].End > span.End {
		t.Fatalf("match %d..%d outside span %d..%d", matches[0].Start, matches[0].End, span.Start, span.End)
	}
}

func TestScannerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New().Scan(ctx, detect.Input{Raw: []byte("x")}); err == nil {
		t.Fatal("cancelled context returned nil error")
	}
}
