// Package secretmask finds credential-shaped values and personal data in a byte
// buffer and replaces them with reversible stand-ins that keep the original
// length, prefix, punctuation and per-segment alphabet.
//
// Detection is a Ragel-compiled DFA over the patterns in rules.rl, scored by
// rule strength, an offline structural validator and the credential vocabulary
// around the match; only findings at or above the maskable band are rewritten,
// so a bare `sk-` value with nothing else to go on is reported and left alone
// while the same value next to a variable name is replaced. A stand-in is a
// pure function of the process seed, the rule and the real value, so one
// credential always renders to the same stand-in, and the mapping back to the
// real value exists only in this process's memory (Vault).
//
// Numeric shapes that share the same lexical form - a run of digits - are
// matched by one coarse pattern and classified natively after the match, so a
// new numeric class needs no new DFA states. The Chinese resident identity
// number is the first such class (KindNationalID): its stand-in is re-rendered
// as a well-formed number with a fresh birthdate rather than byte-substituted,
// because a shape-only replacement would fail every downstream validator.
//
// The package is stdlib-only and never writes anything: a caller decides what
// to do with a Finding, and Restorer reverses stand-ins as bytes flow through
// it, including stand-ins that a provider split across several streamed deltas.
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
