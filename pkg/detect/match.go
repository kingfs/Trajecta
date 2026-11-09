package detect

import "sort"

// Span is a half-open byte range in the scanned buffer, with End exclusive.
//
// Path and Kind are free-form labels for where the range came from, for
// example a JSON pointer and a semantic field kind. The Engine never
// interprets them: they travel with the match so a caller can attribute a
// finding without re-parsing the buffer. A span is valid when it is non-empty
// and starts at or after zero; Scan additionally requires a match span to fit
// Input.Raw.
type Span struct {
	Start int
	End   int
	Path  string
	Kind  string
}

// Valid reports whether the span is non-empty and ordered. It says nothing
// about any buffer; Scan checks the span against Input.Raw.
func (s Span) Valid() bool { return s.Start >= 0 && s.End > s.Start }

// Len returns the span length in bytes. It is negative for an unordered span.
func (s Span) Len() int { return s.End - s.Start }

// Match is one detected region of the scanned buffer.
type Match struct {
	Span
	// Rule names the rule that produced the match. It scopes deduplication:
	// two rules may report the same bytes and both survive.
	Rule string
	// Confidence is a score in [0,1]. Scan clamps out-of-range values,
	// including NaN, into that interval.
	Confidence float64
	// Value is the matched text as the scanner wants it reported. A scanner
	// may leave it empty; the Engine never fills it in.
	Value string
	// Metadata carries rule-specific evidence. It is passed through as-is and
	// may be nil or shared between matches; the Engine never mutates it.
	Metadata map[string]string
}

// ContainsSpan reports whether sp is fully inside at least one of spans.
//
// Containment is inclusive: equal boundaries count as contained. An invalid
// sp is never contained. Empty spans reports false; a caller that treats an
// empty scope as "the whole buffer" must check for that separately, as
// Engine.Scan does.
func ContainsSpan(spans []Span, sp Span) bool {
	if !sp.Valid() {
		return false
	}
	for _, s := range spans {
		if s.Start <= sp.Start && sp.End <= s.End {
			return true
		}
	}
	return false
}

// MergeMatches returns the union of a and b, deduplicated by (start, end,
// rule) and sorted by start, then end, then rule. For an identical triple the
// first match in a wins over the one in b.
//
// MergeMatches neither validates spans against a buffer nor clamps
// confidence; it is the ordering half of Scan for callers that combine
// already-validated sets. The input slices are not modified and the result
// does not alias their backing arrays.
func MergeMatches(a, b []Match) []Match {
	if len(a) == 0 && len(b) == 0 {
		return nil
	}
	out := make([]Match, 0, len(a)+len(b))
	out = append(out, a...)
	out = append(out, b...)
	sortMatches(out)
	return dedupeMatches(out)
}

// sortMatches orders matches by start, end and rule. It is stable so that
// registration order breaks a tie between two matches with the same triple.
func sortMatches(matches []Match) {
	sort.SliceStable(matches, func(i, j int) bool {
		a, b := matches[i], matches[j]
		if a.Start != b.Start {
			return a.Start < b.Start
		}
		if a.End != b.End {
			return a.End < b.End
		}
		return a.Rule < b.Rule
	})
}

// dedupeMatches drops a match whose (start, end, rule) triple equals the
// previous one's. matches must already be sorted by that triple, which makes
// every duplicate adjacent.
func dedupeMatches(matches []Match) []Match {
	if len(matches) < 2 {
		return matches
	}
	out := matches[:1]
	for _, m := range matches[1:] {
		last := out[len(out)-1]
		if last.Start == m.Start && last.End == m.End && last.Rule == m.Rule {
			continue
		}
		out = append(out, m)
	}
	return out
}
