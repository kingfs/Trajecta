package secretmask

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// chineseIDWithCheck appends the ISO 7064 check character to seventeen digits,
// so a negative case isolates the property it is meant to test instead of
// failing on the checksum first.
func chineseIDWithCheck(first17 string) string {
	return first17 + string(chineseIDCheckChar([]byte(first17)))
}

func TestChineseIDValidator(t *testing.T) {
	valid := []string{
		"11010519491231002X",
		"11010519491231002x",
		chineseIDWithCheck("11010520000229002"), // 2000-02-29, a leap year
		chineseIDWithCheck("44052418800101001"), // 1880 birth, still in archived records
	}
	for _, tok := range valid {
		if !validateChineseID([]byte(tok)) {
			t.Errorf("validateChineseID(%q) = false, want true", tok)
		}
	}

	// Pin the primitive against an externally published number rather than
	// against another call to the same code.
	if got := chineseIDWithCheck("44052418800101001"); got != "440524188001010014" {
		t.Fatalf("check character for 44052418800101001 = %q, want the published 440524188001010014", got)
	}

	bad := map[string]string{
		"checksum":        "110105194912310020",
		"month":           chineseIDWithCheck("11010519491331002"),
		"day":             chineseIDWithCheck("11010519490230002"),
		"non-leap feb 29": chineseIDWithCheck("11010519490229002"),
		"province":        chineseIDWithCheck("99010519491231002"),
		"birth year low":  chineseIDWithCheck("11010517991231002"),
		"birth year high": chineseIDWithCheck("11010521001231002"),
		"letter in body":  "11010519491X31002X",
		"seventeen":       "11010519491231002",
		"nineteen":        "110105194912310021X",
		"empty":           "",
	}
	for name, tok := range bad {
		if validateChineseID([]byte(tok)) {
			t.Errorf("%s: validateChineseID(%q) = true, want false", name, tok)
		}
	}

	if !validCalendarDate(2000, 2, 29) || validCalendarDate(1900, 2, 29) {
		t.Error("the leap-year rule disagrees with the Gregorian century rule")
	}
}

// TestChineseIDProvinceTable keeps the ordered list the renderer draws from and
// the lookup table the validator uses in step.
func TestChineseIDProvinceTable(t *testing.T) {
	count := 0
	seen := map[string]bool{}
	for _, p := range chineseIDProvinceList {
		if len(p) != 2 || seen[p] {
			t.Fatalf("province list entry %q is malformed or repeated", p)
		}
		seen[p] = true
		code := int(p[0]-'0')*10 + int(p[1]-'0')
		if !chineseIDProvinceSet[code] {
			t.Errorf("province %q is missing from the lookup table", p)
		}
	}
	for _, ok := range chineseIDProvinceSet {
		if ok {
			count++
		}
	}
	if count != len(chineseIDProvinceList) {
		t.Fatalf("lookup table has %d entries, list has %d", count, len(chineseIDProvinceList))
	}
}

func TestChineseIDDetection(t *testing.T) {
	const id = "11010519491231002X"
	cases := []struct {
		name string
		text string
		want int
	}{
		{"bare", id, 1},
		{"json value", `{"id":"` + id + `"}`, 1},
		{"chinese prose", "身份证" + id + "号码", 1},
		{"lowercase check char", "11010519491231002x", 1},
		{"leading digit", "7" + id, 0},
		{"trailing digit", id + "7", 0},
		{"leading letter", "user" + id, 0},
		{"trailing letter", id + "x", 0},
		{"nineteen digit timestamp", "1700000000000000000", 0},
		{"eighteen non-id", "123456789012345678", 0},
		{"seventeen digits", "11010519491231002", 0},
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
			if f.ID != RuleChineseID {
				t.Fatalf("rule = %q, want %q", ruleTable[f.ID].Name, ruleTable[RuleChineseID].Name)
			}
			if !f.Maskable() || f.Confidence != ConfidenceHigh || f.Score < 70 {
				t.Fatalf("finding is not conclusive: %+v", f)
			}
			if f.Reasons&ReasonValidator == 0 {
				t.Errorf("reasons = %s, want the validator bit", f.Reasons)
			}
			if f.Reasons&ReasonContext != 0 {
				t.Errorf("reasons = %s, but a validator-proven rule must not pay for context", f.Reasons)
			}
			if got := string(f.Bytes([]byte(tc.text))); !strings.EqualFold(got, id) {
				t.Errorf("span covers %q, want the identity number", got)
			}
		})
	}
}

func TestChineseIDMasking(t *testing.T) {
	const id = "11010519491231002X"
	body := []byte(`{"user":{"id":"` + id + `"},"note":"keep"}`)

	v := NewVault([]byte("chinese-id-test-seed"))
	masked, events := v.Mask(body)
	if len(masked) != len(body) {
		t.Fatalf("masked length %d, want %d", len(masked), len(body))
	}
	if bytes.Contains(masked, []byte(id)) {
		t.Fatalf("masked body still carries the identity number: %s", masked)
	}
	if len(events) != 1 {
		t.Fatalf("Mask() produced %d events, want 1", len(events))
	}
	e := events[0]
	if e.RuleName() != "pii.chinese_id" || e.Kind() != "national_id" || e.Vendor() != "" {
		t.Errorf("event rule/kind/vendor = %q/%q/%q", e.RuleName(), e.Kind(), e.Vendor())
	}
	if e.Score < 70 || e.Confidence != ConfidenceHigh {
		t.Errorf("event score/confidence = %d/%s, want a high-confidence finding", e.Score, e.Confidence)
	}
	original, standIn := e.AuditValues()
	if original != id || standIn == id || len(standIn) != len(id) {
		t.Fatalf("audit pair = (%q, %q)", original, standIn)
	}
	if !validateChineseID([]byte(standIn)) {
		t.Fatalf("stand-in %q is not a well-formed identity number, so a downstream validator would reject it", standIn)
	}

	// Determinism: the same seed and the same value render the same stand-in,
	// which is what keeps a multi-turn conversation consistent.
	same := NewVault([]byte("chinese-id-test-seed"))
	maskedSame, eventsSame := same.Mask(body)
	sameOriginal, sameStandIn := eventsSame[0].AuditValues()
	if !bytes.Equal(masked, maskedSame) || len(eventsSame) != 1 || sameOriginal != original || sameStandIn != standIn {
		t.Fatalf("the same seed rendered a different stand-in: %q vs %q", masked, maskedSame)
	}
	other := NewVault([]byte("chinese-id-test-other-seed"))
	maskedOther, eventsOther := other.Mask(body)
	otherOriginal, otherStandIn := eventsOther[0].AuditValues()
	if len(eventsOther) != 1 || (otherOriginal == original && otherStandIn == standIn) {
		t.Fatalf("a different seed rendered the same stand-in: %q", maskedOther)
	}

	// Idempotency: the stand-in is itself a well-formed identity number, so a
	// second pass rediscovers it; the vault must recognise its own fake.
	remasked, more := v.Mask(masked)
	if !bytes.Equal(remasked, masked) || len(more) != 0 {
		t.Fatalf("second Mask changed %q into %q with %d events", masked, remasked, len(more))
	}

	restored := v.Restore(masked)
	if !bytes.Equal(restored, body) {
		t.Fatalf("Restore() = %q, want %q", restored, body)
	}
}

// TestChineseIDFakeRange pins the shape of the rendered stand-in: a real
// province, an always-valid calendar date and a check character that matches.
// The values it checks come from the renderer, so it would catch a keystream
// index drifting into the day field or a province list edit that the validator
// table did not follow.
func TestChineseIDFakeRange(t *testing.T) {
	v := NewVault([]byte("chinese-id-range-seed"))
	for i := 0; i < 64; i++ {
		real := chineseIDWithCheck("11010519900101" + fmt.Sprintf("%03d", i))
		fake := v.fakeFor(&ruleTable[RuleChineseID], []byte(real))
		if len(fake) != 18 || !validateChineseID([]byte(fake)) {
			t.Fatalf("fake %q for %q is not a valid identity number", fake, real)
		}
		year := int(fake[6]-'0')*1000 + int(fake[7]-'0')*100 + int(fake[8]-'0')*10 + int(fake[9]-'0')
		day := int(fake[12]-'0')*10 + int(fake[13]-'0')
		if year < 1950 || year > 2009 || day < 1 || day > 28 {
			t.Errorf("fake %q has an implausible birthdate", fake)
		}
		if !chineseIDProvinceSet[int(fake[0]-'0')*10+int(fake[1]-'0')] {
			t.Errorf("fake %q has no real province code", fake)
		}
	}
}

func BenchmarkScanWithChineseIDs(b *testing.B) {
	block := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 40) +
		"id=11010519491231002X\n"
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

// BenchmarkScanDenseChineseIDs is the worst case for the PII path: the same
// number repeated until the payload is mostly matches. SkipContext is what
// keeps it near the plain lexical rate instead of paying the 96-byte window
// search thousands of times.
func BenchmarkScanDenseChineseIDs(b *testing.B) {
	unit := "id=11010519491231002X "
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
