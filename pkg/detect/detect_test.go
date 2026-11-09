package detect

import (
	"context"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync"
	"testing"
)

// fakeScanner is a Scanner backed by a function.
type fakeScanner struct {
	name string
	scan func(context.Context, Input) ([]Match, error)
}

func (f *fakeScanner) Name() string { return f.name }

func (f *fakeScanner) Scan(ctx context.Context, in Input) ([]Match, error) {
	if f.scan == nil {
		return nil, nil
	}
	return f.scan(ctx, in)
}

// newFake returns a scanner that reports a copy of matches on every call.
func newFake(name string, matches ...Match) *fakeScanner {
	return &fakeScanner{
		name: name,
		scan: func(context.Context, Input) ([]Match, error) {
			out := make([]Match, len(matches))
			copy(out, matches)
			return out, nil
		},
	}
}

func TestEngineZeroValue(t *testing.T) {
	t.Parallel()
	var e Engine
	got, err := e.Scan(context.Background(), Input{})
	if err != nil {
		t.Fatalf("Scan() error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("Scan() = %+v, want no matches", got)
	}
	if e.Scanners() != nil {
		t.Fatalf("Scanners() = %+v, want nil", e.Scanners())
	}
	if err := e.Register(nil); !errors.Is(err, ErrInvalidScanner) {
		t.Fatalf("Register(nil) = %v, want ErrInvalidScanner", err)
	}
	if err := e.Register(newFake("ok")); err != nil {
		t.Fatalf("Register(ok) = %v, want nil", err)
	}
	if names := scannerNames(e.Scanners()); !reflect.DeepEqual(names, []string{"ok"}) {
		t.Fatalf("Scanners() = %v, want [ok]", names)
	}
}

func TestEngineRunsScannersInRegistrationOrder(t *testing.T) {
	t.Parallel()
	var order []string
	var seen Input
	first := &fakeScanner{
		name: "first",
		scan: func(_ context.Context, in Input) ([]Match, error) {
			order = append(order, "first")
			seen = in
			return []Match{{Span: Span{Start: 2, End: 4, Kind: "text"}, Rule: "r1"}}, nil
		},
	}
	second := &fakeScanner{
		name: "second",
		scan: func(_ context.Context, in Input) ([]Match, error) {
			order = append(order, "second")
			return []Match{{Span: Span{Start: 0, End: 1}, Rule: "r2"}}, nil
		},
	}
	in := Input{
		Raw:      []byte("0123456789"),
		Protocol: "chat_completions",
		Meta:     map[string]string{"trace": "t1"},
	}
	got, err := New(first, second).Scan(context.Background(), in)
	if err != nil {
		t.Fatalf("Scan() error = %v, want nil", err)
	}
	if !reflect.DeepEqual(order, []string{"first", "second"}) {
		t.Fatalf("scan order = %v, want registration order [first second]", order)
	}
	if !reflect.DeepEqual(seen.Raw, in.Raw) || seen.Protocol != in.Protocol || !reflect.DeepEqual(seen.Meta, in.Meta) {
		t.Fatalf("scanner input = %+v, want %+v", seen, in)
	}
	want := []Match{
		{Span: Span{Start: 0, End: 1}, Rule: "r2"},
		{Span: Span{Start: 2, End: 4, Kind: "text"}, Rule: "r1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Scan() = %+v, want %+v", got, want)
	}
}

func TestEngineRegister(t *testing.T) {
	t.Parallel()
	first := newFake("first")
	e := New(first)

	if err := e.Register(newFake("first")); !errors.Is(err, ErrDuplicateScanner) {
		t.Fatalf("Register(duplicate) = %v, want ErrDuplicateScanner", err)
	}
	if err := e.Register(nil); !errors.Is(err, ErrInvalidScanner) {
		t.Fatalf("Register(nil) = %v, want ErrInvalidScanner", err)
	}
	if err := e.Register(&fakeScanner{}); !errors.Is(err, ErrInvalidScanner) {
		t.Fatalf("Register(empty name) = %v, want ErrInvalidScanner", err)
	}

	second := newFake("second")
	if err := e.Register(second); err != nil {
		t.Fatalf("Register(second) = %v, want nil", err)
	}

	names := scannerNames(e.Scanners())
	if !reflect.DeepEqual(names, []string{"first", "second"}) {
		t.Fatalf("Scanners() = %v, want [first second]", names)
	}

	// Scanners returns a copy: mutating it must not reach the engine.
	returned := e.Scanners()
	returned[0] = nil
	if names := scannerNames(e.Scanners()); !reflect.DeepEqual(names, []string{"first", "second"}) {
		t.Fatalf("Scanners() after caller mutation = %v, want [first second]", names)
	}
}

func TestEngineNewSkipsInvalidScanners(t *testing.T) {
	t.Parallel()
	first := newFake("first")
	duplicate := newFake("first")
	last := newFake("last")
	e := New(nil, &fakeScanner{}, first, duplicate, last)
	if names := scannerNames(e.Scanners()); !reflect.DeepEqual(names, []string{"first", "last"}) {
		t.Fatalf("Scanners() = %v, want [first last]", names)
	}
}

func TestEngineRejectsInvalidMatchSpans(t *testing.T) {
	t.Parallel()
	raw := []byte("0123456789")
	scanner := newFake("s",
		Match{Span: Span{Start: 0, End: 0}, Rule: "empty"},
		Match{Span: Span{Start: -1, End: 3}, Rule: "negative-start"},
		Match{Span: Span{Start: 4, End: 2}, Rule: "reversed"},
		Match{Span: Span{Start: 5, End: 11}, Rule: "overrun"},
		Match{Span: Span{Start: 2, End: 5}, Rule: "ok"},
	)
	got, err := New(scanner).Scan(context.Background(), Input{Raw: raw})
	if err != nil {
		t.Fatalf("Scan() error = %v, want nil", err)
	}
	want := []Match{{Span: Span{Start: 2, End: 5}, Rule: "ok"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Scan() = %+v, want %+v", got, want)
	}
}

func TestEngineFiltersMatchesToDeclaredSpans(t *testing.T) {
	t.Parallel()
	raw := []byte("0123456789abcdef")
	scanner := newFake("s",
		Match{Span: Span{Start: 4, End: 6}, Rule: "inside"},
		Match{Span: Span{Start: 12, End: 14}, Rule: "exact"},
		Match{Span: Span{Start: 2, End: 6}, Rule: "crosses"},
		Match{Span: Span{Start: 8, End: 10}, Rule: "outside"},
	)
	spans := []Span{
		{Start: 4, End: 8, Path: "/a", Kind: "string"},
		{Start: 12, End: 14},
	}

	got, err := New(scanner).Scan(context.Background(), Input{Raw: raw, Spans: spans})
	if err != nil {
		t.Fatalf("Scan() error = %v, want nil", err)
	}
	want := []Match{
		{Span: Span{Start: 4, End: 6}, Rule: "inside"},
		{Span: Span{Start: 12, End: 14}, Rule: "exact"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Scan() = %+v, want %+v", got, want)
	}

	// Without a declared scope every valid match is accepted.
	got, err = New(scanner).Scan(context.Background(), Input{Raw: raw})
	if err != nil {
		t.Fatalf("Scan(no scope) error = %v, want nil", err)
	}
	if len(got) != 4 {
		t.Fatalf("Scan(no scope) = %+v, want all 4 matches", got)
	}
}

func TestEngineClampsDeclaredSpans(t *testing.T) {
	t.Parallel()
	raw := []byte("0123456789")
	scanner := newFake("s",
		Match{Span: Span{Start: 0, End: 1}, Rule: "head"},
		Match{Span: Span{Start: 9, End: 10}, Rule: "tail"},
		Match{Span: Span{Start: 10, End: 12}, Rule: "beyond"},
	)
	// The declared span starts before and ends after the buffer; it clamps to
	// the whole buffer, so both in-buffer matches survive.
	got, err := New(scanner).Scan(context.Background(), Input{
		Raw:   raw,
		Spans: []Span{{Start: -5, End: 100, Path: "p", Kind: "k"}},
	})
	if err != nil {
		t.Fatalf("Scan() error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("Scan() = %+v, want the two in-buffer matches", got)
	}

	// A span entirely past the buffer clamps away, so it cannot widen the
	// scope back to the whole buffer.
	got, err = New(scanner).Scan(context.Background(), Input{
		Raw:   raw,
		Spans: []Span{{Start: 50, End: 60}},
	})
	if err != nil {
		t.Fatalf("Scan(past buffer) error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("Scan(past buffer) = %+v, want no matches", got)
	}

	// The same holds when every declared span clamps away.
	got, err = New(scanner).Scan(context.Background(), Input{
		Raw:   raw,
		Spans: []Span{{Start: -5, End: -1}, {Start: 20, End: 30}},
	})
	if err != nil {
		t.Fatalf("Scan(invalid scope) error = %v, want nil", err)
	}
	if len(got) != 0 {
		t.Fatalf("Scan(invalid scope) = %+v, want no matches", got)
	}
}

func TestEngineDeduplicatesAndSorts(t *testing.T) {
	t.Parallel()
	raw := []byte("0123456789")
	// One scanner reports duplicates of its own output; a second repeats the
	// same (start, end, rule) triples with different evidence.
	a := &fakeScanner{
		name: "a",
		scan: func(context.Context, Input) ([]Match, error) {
			return []Match{
				{Span: Span{Start: 5, End: 9}, Rule: "b", Value: "a-b"},
				{Span: Span{Start: 0, End: 2}, Rule: "z", Value: "a-z"},
				{Span: Span{Start: 5, End: 9}, Rule: "a", Value: "a-a"},
				{Span: Span{Start: 5, End: 9}, Rule: "b", Value: "a-b-duplicate"},
			}, nil
		},
	}
	b := &fakeScanner{
		name: "b",
		scan: func(context.Context, Input) ([]Match, error) {
			return []Match{
				{Span: Span{Start: 5, End: 9}, Rule: "b", Value: "b-b"},
				{Span: Span{Start: 2, End: 4}, Rule: "m", Value: "b-m"},
				{Span: Span{Start: 5, End: 9}, Rule: "a", Value: "b-a"},
			}, nil
		},
	}
	got, err := New(a, b).Scan(context.Background(), Input{Raw: raw})
	if err != nil {
		t.Fatalf("Scan() error = %v, want nil", err)
	}
	want := []Match{
		{Span: Span{Start: 0, End: 2}, Rule: "z", Value: "a-z"},
		{Span: Span{Start: 2, End: 4}, Rule: "m", Value: "b-m"},
		{Span: Span{Start: 5, End: 9}, Rule: "a", Value: "a-a"},
		{Span: Span{Start: 5, End: 9}, Rule: "b", Value: "a-b"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Scan() = %+v, want %+v", got, want)
	}
}

func TestEngineClampsConfidence(t *testing.T) {
	t.Parallel()
	raw := []byte("0123456789")
	scanner := newFake("s",
		Match{Span: Span{Start: 0, End: 1}, Rule: "high", Confidence: 1.5},
		Match{Span: Span{Start: 1, End: 2}, Rule: "negative", Confidence: -0.25},
		Match{Span: Span{Start: 2, End: 3}, Rule: "nan", Confidence: math.NaN()},
		Match{Span: Span{Start: 3, End: 4}, Rule: "kept", Confidence: 0.25},
		Match{Span: Span{Start: 4, End: 5}, Rule: "zero", Confidence: 0},
	)
	got, err := New(scanner).Scan(context.Background(), Input{Raw: raw})
	if err != nil {
		t.Fatalf("Scan() error = %v, want nil", err)
	}
	want := []float64{1, 0, 0, 0.25, 0}
	if len(got) != len(want) {
		t.Fatalf("Scan() returned %d matches, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Confidence != want[i] {
			t.Fatalf("match %q confidence = %v, want %v", got[i].Rule, got[i].Confidence, want[i])
		}
	}
}

func TestEngineScannerError(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	secondRan := false
	first := &fakeScanner{
		name: "broken",
		scan: func(context.Context, Input) ([]Match, error) {
			return []Match{{Span: Span{Start: 0, End: 1}, Rule: "partial"}}, boom
		},
	}
	second := &fakeScanner{
		name: "after",
		scan: func(context.Context, Input) ([]Match, error) {
			secondRan = true
			return nil, nil
		},
	}
	got, err := New(first, second).Scan(context.Background(), Input{Raw: []byte("0123456789")})
	if !errors.Is(err, boom) {
		t.Fatalf("Scan() error = %v, want wrapped boom", err)
	}
	if want := `detect: scanner "broken"`; !strings.Contains(err.Error(), want) {
		t.Fatalf("Scan() error = %q, want it to contain %q", err, want)
	}
	if got != nil {
		t.Fatalf("Scan() = %+v, want partial matches discarded", got)
	}
	if secondRan {
		t.Fatal("a scanner after the failing one ran")
	}
}

func TestEngineEmptyInput(t *testing.T) {
	t.Parallel()
	if got, err := New().Scan(context.Background(), Input{}); err != nil || got != nil {
		t.Fatalf("Scan(no scanners) = (%+v, %v), want (nil, nil)", got, err)
	}
	// A match cannot exist in a zero-length buffer, even at offset zero.
	scanner := newFake("s", Match{Span: Span{Start: 0, End: 0}, Rule: "empty"})
	got, err := New(scanner).Scan(context.Background(), Input{})
	if err != nil {
		t.Fatalf("Scan(empty raw) error = %v, want nil", err)
	}
	if got != nil {
		t.Fatalf("Scan(empty raw) = %+v, want nil", got)
	}
}

func TestEnginePassesContextAndNilContext(t *testing.T) {
	t.Parallel()
	type ctxKey struct{}
	var got any
	scanner := &fakeScanner{
		name: "s",
		scan: func(ctx context.Context, _ Input) ([]Match, error) {
			if ctx == nil {
				return nil, errors.New("nil context")
			}
			got = ctx.Value(ctxKey{})
			return nil, nil
		},
	}
	ctx := context.WithValue(context.Background(), ctxKey{}, "trace-1")
	if _, err := New(scanner).Scan(ctx, Input{}); err != nil {
		t.Fatalf("Scan() error = %v, want nil", err)
	}
	if got != "trace-1" {
		t.Fatalf("scanner context value = %v, want trace-1", got)
	}
	// A nil context is replaced with context.Background().
	got = nil
	if _, err := New(scanner).Scan(nil, Input{}); err != nil {
		t.Fatalf("Scan(nil ctx) error = %v, want nil", err)
	}
	if got != nil {
		t.Fatalf("scanner context value = %v, want nil", got)
	}
}

func TestEnginePreservesValueAndMetadata(t *testing.T) {
	t.Parallel()
	metadata := map[string]string{"rule_id": "42", "source": "test"}
	scanner := newFake("s", Match{
		Span:       Span{Start: 0, End: 6, Path: "/input", Kind: "string"},
		Rule:       "credential",
		Confidence: 0.9,
		Value:      "secret",
		Metadata:   metadata,
	})
	got, err := New(scanner).Scan(context.Background(), Input{Raw: []byte("secret")})
	if err != nil {
		t.Fatalf("Scan() error = %v, want nil", err)
	}
	if len(got) != 1 {
		t.Fatalf("Scan() returned %d matches, want 1", len(got))
	}
	m := got[0]
	if m.Value != "secret" || m.Rule != "credential" || m.Confidence != 0.9 {
		t.Fatalf("match = %+v, want value/rule/confidence preserved", m)
	}
	if m.Path != "/input" || m.Kind != "string" {
		t.Fatalf("match span = %+v, want path and kind preserved", m.Span)
	}
	if !reflect.DeepEqual(m.Metadata, metadata) {
		t.Fatalf("match metadata = %v, want %v", m.Metadata, metadata)
	}
}

func TestEngineConcurrentScan(t *testing.T) {
	t.Parallel()
	raw := []byte("0123456789")
	e := New(
		newFake("a",
			Match{Span: Span{Start: 4, End: 6}, Rule: "x"},
			Match{Span: Span{Start: 0, End: 2}, Rule: "x"},
		),
		newFake("b",
			Match{Span: Span{Start: 0, End: 2}, Rule: "x"}, // duplicate of a's
			Match{Span: Span{Start: 7, End: 9}, Rule: "y"},
		),
	)
	const goroutines, iterations = 16, 50
	var wg sync.WaitGroup
	errs := make(chan error, goroutines)
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				got, err := e.Scan(context.Background(), Input{Raw: raw})
				if err != nil {
					errs <- err
					return
				}
				want := [][2]int{{0, 2}, {4, 6}, {7, 9}}
				if len(got) != len(want) {
					errs <- fmt.Errorf("got %d matches, want %d", len(got), len(want))
					return
				}
				for j, w := range want {
					if got[j].Start != w[0] || got[j].End != w[1] {
						errs <- fmt.Errorf("match %d = [%d,%d), want [%d,%d)", j, got[j].Start, got[j].End, w[0], w[1])
						return
					}
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestEngineConcurrentRegisterAndScan(t *testing.T) {
	t.Parallel()
	raw := []byte("0123456789")
	e := New(newFake("base", Match{Span: Span{Start: 0, End: 1}, Rule: "base"}))
	const goroutines = 8
	var wg sync.WaitGroup
	errs := make(chan error, goroutines*2)

	for g := 0; g < goroutines; g++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for i := 0; i < 20; i++ {
				if _, err := e.Scan(context.Background(), Input{Raw: raw}); err != nil {
					errs <- err
					return
				}
			}
		}()
		go func(g int) {
			defer wg.Done()
			if err := e.Register(newFake(fmt.Sprintf("extra-%d", g))); err != nil {
				errs <- err
			}
		}(g)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if want := 1 + goroutines; len(e.Scanners()) != want {
		t.Fatalf("Scanners() has %d entries, want %d", len(e.Scanners()), want)
	}
}

func scannerNames(scanners []Scanner) []string {
	names := make([]string, len(scanners))
	for i, s := range scanners {
		names[i] = s.Name()
	}
	return names
}
