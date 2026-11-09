package detect

import (
	"reflect"
	"testing"
)

func TestSpanValidAndLen(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		span  Span
		valid bool
		len   int
	}{
		{"zero value", Span{}, false, 0},
		{"normal", Span{Start: 3, End: 8}, true, 5},
		{"one byte", Span{Start: 0, End: 1}, true, 1},
		{"empty", Span{Start: 4, End: 4}, false, 0},
		{"negative start", Span{Start: -1, End: 4}, false, 5},
		{"reversed", Span{Start: 8, End: 3}, false, -5},
		{"negative end", Span{Start: 0, End: -2}, false, -2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.span.Valid(); got != tc.valid {
				t.Fatalf("Valid() = %v, want %v", got, tc.valid)
			}
			if got := tc.span.Len(); got != tc.len {
				t.Fatalf("Len() = %d, want %d", got, tc.len)
			}
		})
	}
}

func TestContainsSpan(t *testing.T) {
	t.Parallel()
	spans := []Span{
		{Start: 10, End: 20, Path: "/a", Kind: "string"},
		{Start: 30, End: 40},
	}
	cases := []struct {
		name string
		in   Span
		want bool
	}{
		{"inside first", Span{Start: 12, End: 18}, true},
		{"equal to first", Span{Start: 10, End: 20}, true},
		{"inside second", Span{Start: 35, End: 36}, true},
		{"spans both", Span{Start: 15, End: 35}, false},
		{"crosses first boundary", Span{Start: 8, End: 12}, false},
		{"between", Span{Start: 22, End: 28}, false},
		{"outside", Span{Start: 50, End: 60}, false},
		{"invalid match span", Span{Start: 12, End: 12}, false},
		{"reversed match span", Span{Start: 18, End: 12}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ContainsSpan(spans, tc.in); got != tc.want {
				t.Fatalf("ContainsSpan(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
	if ContainsSpan(nil, Span{Start: 0, End: 1}) {
		t.Fatal("ContainsSpan(nil, ...) = true, want false")
	}
}

func TestMergeMatches(t *testing.T) {
	t.Parallel()
	a := []Match{
		{Span: Span{Start: 5, End: 9}, Rule: "b", Value: "a-second"},
		{Span: Span{Start: 0, End: 2}, Rule: "a", Value: "a-first"},
		{Span: Span{Start: 5, End: 7}, Rule: "b", Value: "a-short"},
	}
	b := []Match{
		{Span: Span{Start: 5, End: 9}, Rule: "b", Value: "b-duplicate"},
		{Span: Span{Start: 5, End: 9}, Rule: "a", Value: "b-same-span-other-rule"},
		{Span: Span{Start: 0, End: 2}, Rule: "a", Value: "b-duplicate"},
		{Span: Span{Start: 5, End: 7}, Rule: "a", Value: "b-short"},
	}
	aCopy := make([]Match, len(a))
	copy(aCopy, a)
	bCopy := make([]Match, len(b))
	copy(bCopy, b)

	got := MergeMatches(a, b)

	want := []Match{
		{Span: Span{Start: 0, End: 2}, Rule: "a", Value: "a-first"},
		{Span: Span{Start: 5, End: 7}, Rule: "a", Value: "b-short"},
		{Span: Span{Start: 5, End: 7}, Rule: "b", Value: "a-short"},
		{Span: Span{Start: 5, End: 9}, Rule: "a", Value: "b-same-span-other-rule"},
		{Span: Span{Start: 5, End: 9}, Rule: "b", Value: "a-second"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MergeMatches() = %+v, want %+v", got, want)
	}
	if len(a) != len(aCopy) || !reflect.DeepEqual(a, aCopy) {
		t.Fatalf("MergeMatches mutated a: %+v", a)
	}
	if len(b) != len(bCopy) || !reflect.DeepEqual(b, bCopy) {
		t.Fatalf("MergeMatches mutated b: %+v", b)
	}

	if got := MergeMatches(nil, nil); got != nil {
		t.Fatalf("MergeMatches(nil, nil) = %+v, want nil", got)
	}
}

func TestMergeMatchesDoesNotAliasInput(t *testing.T) {
	t.Parallel()
	a := []Match{{Span: Span{Start: 1, End: 2}, Rule: "r"}}
	b := []Match{{Span: Span{Start: 3, End: 4}, Rule: "r"}}
	got := MergeMatches(a, b)
	got[0].Rule = "changed"
	if a[0].Rule != "r" {
		t.Fatal("MergeMatches result aliases the input backing array")
	}
}
