package secretmask

// collector receives lexical candidates from the generated DFA.
type collector struct {
	src      []byte
	findings []Finding
}

// emit is called from generated code for every completed lexical match.
//
// The cheap rejections happen here rather than in the machine. Length is the
// one property a DFA cannot express cheaply: writing {min,max} into the machine
// costs an order of magnitude more states than a plain '+' body and makes the
// match loop slower, so the machine matches prefix+charset and the bound is
// checked natively. Validators and context scoring follow for the survivors,
// which are rare enough that the cost does not show up in a profile.
func (c *collector) emit(id RuleID, start, end int) {
	if id == RuleUnknown || id >= ruleIDCount {
		return
	}
	r := &ruleTable[id]
	n := end - start
	if n < r.MinLen || (r.MaxLen > 0 && n > r.MaxLen) {
		return
	}
	tok := c.src[start:end]
	validatorPassed := false
	if r.Validator != "" {
		if !runValidator(r.Validator, tok) {
			return
		}
		validatorPassed = true
	}
	score, why := scoreFinding(r, hasContext(c.src, start, end, r.Keywords), validatorPassed)
	c.findings = append(c.findings, Finding{
		Span:       Span{Start: start, End: end, ID: id},
		Score:      score,
		Confidence: ConfidenceFor(score),
		Reasons:    why,
	})
}

// Detector scans buffers with the compiled credential DFA.
//
// A Detector is an empty value that holds no state, so it is safe for
// concurrent use; callers should keep one for the process rather than build
// one per request.
type Detector struct{}

// AppendFindings appends the findings for src to dst and returns the extended
// slice. Reusing dst avoids allocating on the hot path.
//
// Every lexical candidate that survives the length bounds is returned, scored.
// Filtering is the caller's decision: Finding.Maskable reports whether a value
// is safe to rewrite, and an audit wants the rest as well.
func (Detector) AppendFindings(dst []Finding, src []byte) []Finding {
	if len(src) == 0 {
		return dst
	}
	c := collector{src: src, findings: dst}
	scanDFA(src, &c)
	return c.findings
}

// Scan returns the findings for src.
func (d Detector) Scan(src []byte) []Finding {
	return d.AppendFindings(nil, src)
}

// Scan returns the findings for src using the package's compiled DFA. It is
// shorthand for the zero Detector.
func Scan(src []byte) []Finding { return Detector{}.Scan(src) }
