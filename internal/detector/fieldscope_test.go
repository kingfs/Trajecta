package detector

import (
	"context"
	"testing"

	"github.com/kingfs/Trajecta/pkg/detect"
	"github.com/kingfs/Trajecta/pkg/xir/hook"
)

// recordingScanner records the scan scope it was handed and reports one match so
// the caller can prove the plugin path is intact.
type recordingScanner struct {
	seen [][]detect.Span
}

func (s *recordingScanner) Name() string { return "test.recording" }

func (s *recordingScanner) Scan(_ context.Context, in detect.Input) ([]detect.Match, error) {
	s.seen = append(s.seen, in.Spans)
	return nil, nil
}

// A plugin that declares Points is fed only those fields on the IR path: it does
// not pay for the rest of the body and cannot match text it does not own.
func TestHotScannerPointScoping(t *testing.T) {
	body := []byte(`{"model":"gpt-5","messages":[` +
		`{"role":"user","content":"hello"},` +
		`{"role":"assistant","content":"","tool_calls":[{"id":"c1","type":"function","function":{"name":"run","arguments":"{\"cmd\":\"ls\"}"}}]}]}`)
	scope, ok := ParseRequestScope("chat_completions", body, ScopeOptions{RequireFields: true})
	if !ok {
		t.Fatal("scope required")
	}
	rec := &recordingScanner{}
	snap := &Snapshot{hotScanners: []hotScanner{{scanner: rec, points: []hook.Point{hook.PointRequestToolCall}}}}
	snap.collectCandidatesScoped(body, scope)

	if len(rec.seen) != 1 {
		t.Fatalf("scanner runs = %d, want 1", len(rec.seen))
	}
	want := scope.FieldsFor([]hook.Point{hook.PointRequestToolCall})
	if len(rec.seen[0]) != len(want) {
		t.Fatalf("scanner spans = %+v, want %+v", rec.seen[0], want)
	}
	for i := range want {
		if rec.seen[0][i] != want[i] {
			t.Fatalf("scanner spans = %+v, want %+v", rec.seen[0], want)
		}
	}
	// The whole-body message span must not be in the tool-call scope.
	if spanHas(body, rec.seen[0], "hello") {
		t.Fatalf("tool-call scope leaked message text: %+v", rec.seen[0])
	}
}

// A plugin without Points keeps whole-body coverage.
func TestHotScannerWithoutPointsScansBody(t *testing.T) {
	body := []byte(`{"model":"gpt-5","messages":[{"role":"user","content":"hello"}]}`)
	scope, ok := ParseRequestScope("chat_completions", body, ScopeOptions{RequireFields: true})
	if !ok {
		t.Fatal("scope required")
	}
	rec := &recordingScanner{}
	snap := &Snapshot{hotScanners: []hotScanner{{scanner: rec}}}
	snap.collectCandidatesScoped(body, scope)
	if len(rec.seen) != 1 || rec.seen[0] != nil {
		t.Fatalf("body-scoped scanner spans = %+v, want nil (whole body)", rec.seen)
	}
}

func TestHasHotFields(t *testing.T) {
	if (&Snapshot{secretmaskHot: true}).hasHotFields() {
		t.Fatal("builtin-only snapshot must not require the field projection")
	}
	snap := &Snapshot{hotScanners: []hotScanner{{scanner: &recordingScanner{}}}}
	if snap.hasHotFields() {
		t.Fatal("a scanner without points must not require the field projection")
	}
	snap.hotScanners[0].points = []hook.Point{hook.PointRequestMessage}
	if !snap.hasHotFields() {
		t.Fatal("a point-scoped scanner must require the field projection")
	}
}
