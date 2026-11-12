package secretmask

import "testing"

// candidatesFor converts the builtin detector's findings into Vault candidates,
// which is what the hot lane does.
func candidatesFor(src []byte) []MaskCandidate {
	findings := (Detector{}).Scan(src)
	out := make([]MaskCandidate, len(findings))
	for i, f := range findings {
		out[i] = MaskCandidate{Finding: f}
	}
	return out
}

func TestMaskCandidatesExcludingDropsOverlap(t *testing.T) {
	src := []byte(`first AKIAIOSFODNN7EXAMPLE second AKIAZZZZZZZZZZZZZZZZ end`)
	cands := candidatesFor(src)
	if len(cands) != 2 {
		t.Fatalf("fixture findings = %d, want 2", len(cands))
	}
	first := Span{Start: cands[0].Start, End: cands[0].End}
	out, events := NewVault([]byte("exclude-test-seed")).MaskCandidatesExcluding(src, cands, []Span{first})
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1 (only the unsealed value)", len(events))
	}
	if string(src[first.Start:first.End]) != "AKIAIOSFODNN7EXAMPLE" {
		t.Fatalf("fixture span moved: %q", src[first.Start:first.End])
	}
	if string(out[first.Start:first.End]) != "AKIAIOSFODNN7EXAMPLE" {
		t.Fatalf("sealed value was rewritten: %q", out[first.Start:first.End])
	}
	if string(out) == string(src) {
		t.Fatal("the unsealed candidate was not rewritten")
	}
}

// A candidate that only partially overlaps an excluded range must be dropped as
// a whole: rewriting it would change bytes of the sealed block.
func TestMaskCandidatesExcludingPartialOverlap(t *testing.T) {
	src := []byte(`key AKIAIOSFODNN7EXAMPLE end`)
	cands := candidatesFor(src)
	if len(cands) != 1 {
		t.Fatalf("fixture findings = %d, want 1", len(cands))
	}
	// Exclude the second half of the candidate.
	excluded := []Span{{Start: cands[0].Start + 5, End: cands[0].End}}
	out, events := NewVault([]byte("exclude-test-seed")).MaskCandidatesExcluding(src, cands, excluded)
	if len(events) != 0 || string(out) != string(src) {
		t.Fatalf("partial overlap must drop the candidate: events=%d out=%q", len(events), out)
	}
}

func TestMaskCandidatesExcludingNilMatchesMaskCandidates(t *testing.T) {
	src := []byte(`key AKIAIOSFODNN7EXAMPLE end`)
	seed := []byte("exclude-test-seed")
	a, ea := NewVault(seed).MaskCandidates(src, candidatesFor(src))
	b, eb := NewVault(seed).MaskCandidatesExcluding(src, candidatesFor(src), nil)
	if string(a) != string(b) || len(ea) != len(eb) {
		t.Fatalf("nil exclusion differs: %q/%d vs %q/%d", a, len(ea), b, len(eb))
	}
}
