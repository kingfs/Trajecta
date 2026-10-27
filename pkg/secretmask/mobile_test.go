package secretmask

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// TestCandidateChecksKnown keeps the rule table and runCandidateCheck in step,
// the way TestValidatorsKnown does for validators. An unknown name fails closed,
// so a typo would silently drop every match for the rule.
func TestCandidateChecksKnown(t *testing.T) {
	known := map[string]bool{
		"":                      true,
		candidateCNMobilePrefix: true,
		candidateURLShape:       true,
		candidateLocalPathShape: true,
	}
	for id := RuleUnknown + 1; id < ruleIDCount; id++ {
		r := ruleTable[id]
		if !known[r.CandidateCheck] {
			t.Errorf("rule %q names unknown candidate check %q", r.Name, r.CandidateCheck)
		}
	}
}

// TestCNMobilePrefixTable keeps the ordered list the renderer draws from and
// the lookup table the candidate check uses in step, and pins the segments that
// are easy to get wrong: 140-144 and 154 are not allocated, 174 is satellite
// numbering, and 199 is allocated.
func TestCNMobilePrefixTable(t *testing.T) {
	if len(cnMobilePrefixes) != 55 {
		t.Fatalf("prefix table has %d entries, want 55", len(cnMobilePrefixes))
	}
	seen := map[int]bool{}
	for _, p := range cnMobilePrefixes {
		if p < 130 || p > 199 || seen[p] {
			t.Fatalf("prefix %d is out of range or repeated", p)
		}
		seen[p] = true
		if !cnMobilePrefixSet[p] {
			t.Errorf("prefix %d is missing from the lookup table", p)
		}
		if !validCNMobilePrefix([]byte(fmt.Sprintf("%d12345678", p))) {
			t.Errorf("prefix %d is rejected by the candidate check", p)
		}
	}
	for _, p := range []int{140, 141, 154, 174, 194} {
		if validCNMobilePrefix([]byte(fmt.Sprintf("%d12345678", p))) {
			t.Errorf("unallocated prefix %d is accepted", p)
		}
	}
	for _, tok := range []string{"", "1380013800", "138001380001", "23800138000", "1380013800x", "abcdefghijk"} {
		if validCNMobilePrefix([]byte(tok)) {
			t.Errorf("validCNMobilePrefix(%q) = true, want false", tok)
		}
	}
}

func TestCNMobileDetection(t *testing.T) {
	cases := []struct {
		name     string
		text     string
		want     int
		maskable bool
	}{
		{"json phone field", `{"phone":"13800138000"}`, 1, true},
		{"json mobile field", `{"mobile": "13800138000"}`, 1, true},
		{"chinese prose", "我的手机号是 13800138000，请联系我。", 1, true},
		{"chinese label", "联系电话：13800138000", 1, true},
		{"full-width punctuation", "手机：13800138000。", 1, true},
		{"bare number is reported but not rewritten", "13800138000", 1, false},
		{"quoted tel field", `{"tel":"13800138000"}`, 1, true},
		{"escaped tel field inside a string", `{\"tel\":\"13800138000\"}`, 1, true},
		{"hotel does not anchor a number", `{"hotel":"13800138000"}`, 1, false},
		{"unallocated head", "手机号 14000138000", 0, false},
		{"ten digits", "1380013800", 0, false},
		{"twelve digits", "138001380001", 0, false},
		{"leading digit", `"phone":"8613800138000"`, 0, false},
		{"trailing letter", `"phone":"13800138000x"`, 0, false},
		{"thirteen digit epoch", "1700000000000", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			findings := Scan([]byte(tc.text))
			if len(findings) != tc.want {
				t.Fatalf("Scan(%q) = %+v, want %d finding(s)", tc.text, findings, tc.want)
			}
			if tc.want == 0 {
				return
			}
			f := findings[0]
			if f.ID != RuleCNMobile {
				t.Fatalf("rule = %q, want %q", ruleTable[f.ID].Name, ruleTable[RuleCNMobile].Name)
			}
			if got := string(f.Bytes([]byte(tc.text))); got != "13800138000" {
				t.Errorf("span covers %q, want the mobile number", got)
			}
			if f.Maskable() != tc.maskable {
				t.Errorf("Maskable() = %v, want %v (score %d, reasons %s)", f.Maskable(), tc.maskable, f.Score, f.Reasons)
			}
			if f.Reasons&ReasonValidator != 0 {
				t.Errorf("reasons = %s: a candidate check must not set the validator bit", f.Reasons)
			}
			if tc.maskable && f.Reasons&ReasonContext == 0 {
				t.Errorf("reasons = %s: a rewriteable mobile number needs context evidence", f.Reasons)
			}
		})
	}
}

func TestCNMobileMasking(t *testing.T) {
	const number = "13800138000"
	body := []byte(`{"phone":"` + number + `","note":"keep"}`)

	v := NewVault([]byte("cn-mobile-test-seed"))
	masked, events := v.Mask(body)
	if len(masked) != len(body) {
		t.Fatalf("masked length %d, want %d", len(masked), len(body))
	}
	if bytes.Contains(masked, []byte(number)) {
		t.Fatalf("masked body still carries the mobile number: %s", masked)
	}
	if len(events) != 1 {
		t.Fatalf("Mask() produced %d events, want 1", len(events))
	}
	e := events[0]
	if e.RuleName() != "pii.cn_mobile" || e.Kind() != "mobile" || e.Vendor() != "" {
		t.Errorf("event rule/kind/vendor = %q/%q/%q", e.RuleName(), e.Kind(), e.Vendor())
	}
	if e.Score != 60 || e.Confidence != ConfidenceMedium {
		t.Errorf("event score/confidence = %d/%s, want 60/medium", e.Score, e.Confidence)
	}
	if e.Reasons != ReasonGenericShape|ReasonExactLength|ReasonContext {
		t.Errorf("reasons = %s, want generic+exact+context", e.Reasons)
	}
	original, standIn := e.AuditValues()
	if original != number || standIn == number || len(standIn) != len(number) {
		t.Fatalf("audit pair = (%q, %q)", original, standIn)
	}
	if !validCNMobilePrefix([]byte(standIn)) {
		t.Fatalf("stand-in %q does not start with an allocated mobile head", standIn)
	}
	if standIn[:3] != number[:3] {
		t.Errorf("stand-in %q dropped the real head %q, so a shape check would see a different carrier", standIn, number[:3])
	}

	// Determinism across sessions with the same seed, and difference across
	// seeds: the same number keeps one stand-in, a different secret renders a
	// different one.
	same := NewVault([]byte("cn-mobile-test-seed"))
	maskedSame, eventsSame := same.Mask(body)
	sameOriginal, sameStandIn := eventsSame[0].AuditValues()
	if !bytes.Equal(masked, maskedSame) || len(eventsSame) != 1 || sameOriginal != original || sameStandIn != standIn {
		t.Fatalf("the same seed rendered a different stand-in: %q vs %q", masked, maskedSame)
	}
	other := NewVault([]byte("cn-mobile-other-seed"))
	maskedOther, eventsOther := other.Mask(body)
	otherOriginal, otherStandIn := eventsOther[0].AuditValues()
	if len(eventsOther) != 1 || (otherOriginal == original && otherStandIn == standIn) {
		t.Fatalf("a different seed rendered the same stand-in: %q", maskedOther)
	}

	// Idempotency: the stand-in is a well-formed mobile number with a phone
	// label next to it, so a second pass rediscovers it; the vault must
	// recognise its own fake instead of rendering a stand-in for the stand-in.
	remasked, more := v.Mask(masked)
	if !bytes.Equal(remasked, masked) || len(more) != 0 {
		t.Fatalf("second Mask changed %q into %q with %d events", masked, remasked, len(more))
	}

	restored := v.Restore(masked)
	if !bytes.Equal(restored, body) {
		t.Fatalf("Restore() = %q, want %q", restored, body)
	}
}

// TestCNMobileBareNumberIsNotRewritten is the policy boundary in one place: a
// number with no phone vocabulary around it is reported (an audit can see it)
// but Mask must leave the bytes alone.
func TestCNMobileBareNumberIsNotRewritten(t *testing.T) {
	body := []byte(`{"order":"13800138000"}`)
	v := NewVault([]byte("cn-mobile-bare-seed"))
	masked, events := v.Mask(body)
	if !bytes.Equal(masked, body) || len(events) != 0 {
		t.Fatalf("Mask rewrote an unanchored number: %q with %d events", masked, len(events))
	}
	if got := Scan(body); len(got) != 1 || got[0].Maskable() {
		t.Fatalf("Scan = %+v, want one low-confidence finding", got)
	}
}

// TestCNMobileFakeRange pins the stand-in shape for many values: eleven digits,
// an allocated head that matches the real one, and never the real value.
func TestCNMobileFakeRange(t *testing.T) {
	v := NewVault([]byte("cn-mobile-range-seed"))
	for i := 0; i < 64; i++ {
		real := fmt.Sprintf("%d%08d", cnMobilePrefixes[i%len(cnMobilePrefixes)], i)
		fake := v.fakeFor(&ruleTable[RuleCNMobile], []byte(real))
		if len(fake) != 11 || !validCNMobilePrefix([]byte(fake)) {
			t.Fatalf("fake %q for %q is not a well-formed mobile number", fake, real)
		}
		if fake[:3] != real[:3] {
			t.Errorf("fake %q changed the head of %q", fake, real)
		}
		if fake == real {
			t.Errorf("fake %q equals the real number", fake)
		}
	}
	// A direct render of a value the candidate check would reject still has to
	// produce an allocated head, because the renderer is also used by tests and
	// by a caller holding a rule.
	fake := v.fakeFor(&ruleTable[RuleCNMobile], []byte("14000138000"))
	if !validCNMobilePrefix([]byte(fake)) {
		t.Errorf("fake %q for an unallocated head is not a mobile number", fake)
	}
}

func BenchmarkScanWithMobileNumbers(b *testing.B) {
	block := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 40) +
		"phone=13800138000\n"
	var sb strings.Builder
	for sb.Len() < 64<<10 {
		sb.WriteString(block)
	}
	payload := []byte(sb.String())
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = Scan(payload)
	}
}

// BenchmarkScanDenseMobileNumbers is the worst case for the phone rule: the
// same anchored number repeated until the payload is mostly matches. Unlike the
// dense identity benchmark it cannot skip the context scan - the window search
// is what makes each match maskable - so this is the price of the feature.
func BenchmarkScanDenseMobileNumbers(b *testing.B) {
	unit := "phone=13800138000 "
	var sb strings.Builder
	for sb.Len() < 64<<10 {
		sb.WriteString(unit)
	}
	payload := []byte(sb.String())
	b.SetBytes(int64(len(payload)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = Scan(payload)
	}
}

// TestCNMobileContextWindowIsNarrow pins the reason the mobile rule overrides
// the shared window: a phone label belongs next to its number, so a mention of
// a phone ninety bytes away must not turn an order number into a rewrite.
func TestCNMobileContextWindowIsNarrow(t *testing.T) {
	const number = "13800138000"
	near := "手机号 " + number
	far := "手机号" + strings.Repeat("x", 80) + " " + number
	for name, text := range map[string]string{"near": near, "far": far} {
		findings := Scan([]byte(text))
		if len(findings) != 1 {
			t.Fatalf("%s: Scan(%q) = %d findings, want 1", name, text, len(findings))
		}
		if got, want := findings[0].Maskable(), name == "near"; got != want {
			t.Errorf("%s: Maskable() = %v, want %v (score %d, reasons %s)", name, got, want, findings[0].Score, findings[0].Reasons)
		}
	}
}

// TestContextWindowOverride checks the parameter contract of hasContext: zero
// means the shared default, a positive value limits the search to that many
// bytes on each side.
func TestContextWindowOverride(t *testing.T) {
	extra := []string{"phone"}
	src := []byte("phone" + strings.Repeat(".", 60) + "13800138000")
	start, end := len(src)-11, len(src)
	if !hasContext(src, start, end, extra, 0) {
		t.Error("the default window missed a keyword 60 bytes away")
	}
	if hasContext(src, start, end, extra, 24) {
		t.Error("a 24-byte window saw a keyword 60 bytes away")
	}
	if !hasContext([]byte("phone 13800138000"), 6, 17, extra, 24) {
		t.Error("a 24-byte window missed a keyword 6 bytes away")
	}
}

// TestCNMobileRestoresAStreamSplitStandIn covers the response side for the
// shortest secret in the rule set. A provider cuts a message at token
// boundaries, so the eleven-byte stand-in can arrive as two runs; the restorer
// must put it back together and hand the client the real number.
func TestCNMobileRestoresAStreamSplitStandIn(t *testing.T) {
	const real = "13800138000"
	v := NewVault([]byte("cn-mobile-stream-seed"))
	masked, events := v.Mask([]byte("phone " + real))
	if len(events) != 1 {
		t.Fatalf("Mask produced %d events, want 1", len(events))
	}
	fake := strings.TrimPrefix(string(masked), "phone ")
	if len(fake) != 11 || fake == real {
		t.Fatalf("stand-in = %q, want an eleven-byte value different from %q", fake, real)
	}

	// Eight bytes is the entry threshold; the run starts a chain and the
	// remaining three bytes complete it across the envelope.
	got := string(v.Restore([]byte(fake[:8] + streamEnvelope + fake[8:])))
	want := real[:8] + streamEnvelope + real[8:]
	if got != want {
		t.Fatalf("restore across a stream split\n got %q\nwant %q", got, want)
	}
}
