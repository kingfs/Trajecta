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
	if r.WordBounded && !bounded(c.src, start, end) {
		return
	}
	if r.CandidateCheck != "" && !runCandidateCheck(r.CandidateCheck, c.src[start:end]) {
		return
	}
	validatorPassed := false
	if r.Validator != "" {
		if !runValidator(r.Validator, c.src[start:end]) {
			return
		}
		validatorPassed = true
	}
	c.append(id, r, start, end, validatorPassed)
}

// emitDigits is the fine half of the coarse digit_run pattern in rules.rl. The
// machine only finds a maximal digit run; which numeric class the run belongs
// to, and whether it is a real value of that class, is decided here by length,
// validator and boundary. Adding another numeric class to digitClasses
// therefore costs no DFA states and no second pass over the request.
func (c *collector) emitDigits(start, end int) {
	if !bounded(c.src, start, end) {
		return
	}
	n := end - start
	for _, id := range digitClasses {
		r := &ruleTable[id]
		if n < r.MinLen || (r.MaxLen > 0 && n > r.MaxLen) {
			continue
		}
		if r.CandidateCheck != "" && !runCandidateCheck(r.CandidateCheck, c.src[start:end]) {
			continue
		}
		validatorPassed := false
		if r.Validator != "" {
			if !runValidator(r.Validator, c.src[start:end]) {
				continue
			}
			validatorPassed = true
		}
		c.append(id, r, start, end, validatorPassed)
		return
	}
}

// append records one accepted candidate. The context scan is the expensive part
// of this path: a 96-byte window on either side searched for the global
// vocabulary plus the rule's own keywords. A rule whose validator already
// proves the value skips it, because the score is at the maskable band either
// way and the reason set only loses an entry that could not change the
// decision.
func (c *collector) append(id RuleID, r *Rule, start, end int, validatorPassed bool) {
	ctx := false
	if !r.SkipContext {
		ctx = hasContext(c.src, start, end, r.Keywords, r.ContextWindow)
	}
	score, why := scoreFinding(r, ctx, validatorPassed)
	c.findings = append(c.findings, Finding{
		Span:       Span{Start: start, End: end, ID: id},
		Score:      score,
		Confidence: ConfidenceFor(score),
		Reasons:    why,
	})
}

// digitClasses are the fine rules the coarse digit_run pattern feeds, in
// decision order: the first rule whose length bounds admit the run and whose
// checks accept it wins. A stricter class must come before a looser one that can
// share its length, because only one finding may cover a span.
//
// The identity number and the mobile number have disjoint lengths, so the order
// cannot change a decision; the eighteen-digit validator runs first because it
// is the conclusive one.
var digitClasses = []RuleID{RuleChineseID, RuleCNMobile}

// bounded reports whether neither side of [start,end) is an ASCII alphanumeric
// byte, so a run inside a longer identifier is not a candidate.
func bounded(src []byte, start, end int) bool {
	if start > 0 && isAlnumByte(src[start-1]) {
		return false
	}
	if end < len(src) && isAlnumByte(src[end]) {
		return false
	}
	return true
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
