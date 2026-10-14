package secretmask

// Span is one credential-shaped region of a scanned buffer. Start and End are
// byte offsets into that buffer with End exclusive.
//
// Spans returned by a single scan never overlap. The DFA is a longest-match
// scanner and consumes the bytes it matched, so competing patterns for the
// same region are already resolved by the machine: the longest match wins, and
// an equal-length tie goes to the pattern declared first in rules.rl.
type Span struct {
	Start int
	End   int
	// ID is the rule that matched.
	ID RuleID
}

// Len returns the length of the span in bytes.
func (s Span) Len() int { return s.End - s.Start }

// Bytes returns the matched bytes from src. It returns nil when the span does
// not fit src, which can only happen if src is not the buffer the span came
// from.
func (s Span) Bytes(src []byte) []byte {
	if s.Start < 0 || s.End > len(src) || s.Start > s.End {
		return nil
	}
	return src[s.Start:s.End]
}

// Rule returns the metadata of the rule that produced the span.
func (s Span) Rule() (Rule, bool) { return RuleFor(s.ID) }
