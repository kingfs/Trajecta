package secretmask

// collector receives lexical candidates from the generated DFA.
type collector struct {
	spans []Span
}

// emit is called from generated code for every completed lexical match.
//
// Only the per-rule length bounds are applied here. Length is the one property
// a DFA cannot express cheaply: writing {min,max} into the machine costs an
// order of magnitude more states than a plain '+' body and makes the match
// loop measurably slower, so the machine matches prefix+charset and the bound
// is checked natively. Rejecting here also keeps the candidate set small for
// the validation and context layers that will sit above this one.
func (c *collector) emit(id RuleID, start, end int) {
	if id == RuleUnknown || id >= ruleIDCount {
		return
	}
	r := &ruleTable[id]
	n := end - start
	if n < r.MinLen || (r.MaxLen > 0 && n > r.MaxLen) {
		return
	}
	c.spans = append(c.spans, Span{Start: start, End: end, ID: id})
}

// Detector scans buffers with the compiled credential DFA.
//
// A Detector is an empty value that holds no state, so it is safe for
// concurrent use; callers should keep one for the process rather than build
// one per request.
type Detector struct{}

// AppendSpans appends the credential-shaped spans found in src to dst and
// returns the extended slice. Reusing dst avoids allocating on the hot path.
func (Detector) AppendSpans(dst []Span, src []byte) []Span {
	if len(src) == 0 {
		return dst
	}
	c := collector{spans: dst}
	scanDFA(src, &c)
	return c.spans
}

// Scan returns the credential-shaped spans found in src.
func (d Detector) Scan(src []byte) []Span {
	return d.AppendSpans(nil, src)
}

// Scan returns the credential-shaped spans found in src using the package's
// compiled DFA. It is shorthand for the zero Detector.
func Scan(src []byte) []Span { return Detector{}.Scan(src) }
