package identify

import (
	"strings"
	"testing"
)

// Known-valid positive samples. The national ID passes the offline checksum
// validator; the mobile number carries the allocated 1[3-9]x head; the AWS and
// OpenAI shapes are the documented conclusive forms.
const (
	validID     = "11010519491231002X"
	validMobile = "13800138000"
	awsKey      = "AKIAIOSFODNN7EXAMPLE"
)

// openAIKey is a 51-byte legacy OpenAI key: sk- + 20 + T3BlbkFJ + 20.
var openAIKey = "sk-" + strings.Repeat("a", 20) + "T3BlbkFJ" + strings.Repeat("b", 20)

func TestScanFindsCredentialsAndPII(t *testing.T) {
	src := []byte("key=" + openAIKey + " aws=" + awsKey + " 身份证 " + validID + " 手机 " + validMobile)
	matches := Scan(src)
	if len(matches) == 0 {
		t.Fatal("Scan returned no matches")
	}
	byRule := map[string]Match{}
	for _, m := range matches {
		byRule[m.Rule] = m
		if m.Start < 0 || m.End > len(src) || m.Start >= m.End {
			t.Fatalf("bad span: %+v", m)
		}
		if string(m.Bytes(src)) != string(src[m.Start:m.End]) {
			t.Fatalf("Bytes mismatch: %+v", m)
		}
	}
	for _, want := range []string{"openai.legacy_key", "aws.access_key_id", "pii.chinese_id", "pii.cn_mobile"} {
		if _, ok := byRule[want]; !ok {
			t.Errorf("Scan missing rule %q; got %v", want, keys(byRule))
		}
	}
	// The national ID is validator-proven, so it is maskable on its own.
	if m := byRule["pii.chinese_id"]; !m.Maskable || m.Confidence < 1.0 {
		t.Errorf("chinese_id should be high-confidence maskable: %+v", m)
	}
	// The mobile number sits next to phone vocabulary, so it is maskable too.
	if m := byRule["pii.cn_mobile"]; !m.Maskable {
		t.Errorf("cn_mobile with phone context should be maskable: %+v", m)
	}
}

func TestScanIsOrderedAndNonOverlapping(t *testing.T) {
	src := []byte(openAIKey + " " + awsKey + " " + validID)
	matches := Scan(src)
	for i := 1; i < len(matches); i++ {
		if matches[i].Start < matches[i-1].Start {
			t.Fatalf("matches not ordered by start: %+v", matches)
		}
	}
}

func TestChineseIDProbe(t *testing.T) {
	src := []byte("身份证号 " + validID + " 结束")
	got := ChineseID(src)
	if len(got) != 1 {
		t.Fatalf("ChineseID = %+v, want 1 match", got)
	}
	if got[0].Rule != RuleChineseID || string(got[0].Bytes(src)) != validID {
		t.Fatalf("ChineseID match = %+v", got[0])
	}
	// An eighteen-digit run inside a longer identifier is not an ID.
	if embedded := ChineseID([]byte("order99" + validID + "tail")); len(embedded) != 0 {
		t.Errorf("ChineseID matched inside a longer identifier: %+v", embedded)
	}
}

func TestCNMobileProbe(t *testing.T) {
	// With phone vocabulary the number is maskable.
	withCtx := CNMobile([]byte("联系电话：" + validMobile))
	if len(withCtx) != 1 || !withCtx[0].Maskable {
		t.Fatalf("CNMobile with context = %+v, want 1 maskable", withCtx)
	}
	// A bare eleven-digit run is reported but not maskable (no check digit).
	bare := CNMobile([]byte("order " + validMobile + " shipped"))
	if len(bare) != 1 {
		t.Fatalf("CNMobile bare = %+v, want 1 reported", bare)
	}
	if bare[0].Maskable {
		t.Errorf("bare mobile should not be maskable: %+v", bare[0])
	}
}

func TestPIIAndCredentialsPartition(t *testing.T) {
	src := []byte(openAIKey + " " + validID + " " + validMobile + " 手机")
	pii := PII(src)
	cred := Credentials(src)
	for _, m := range pii {
		if m.Kind != "national_id" && m.Kind != "mobile" {
			t.Errorf("PII returned non-PII kind %q", m.Kind)
		}
	}
	for _, m := range cred {
		if m.Kind == "national_id" || m.Kind == "mobile" {
			t.Errorf("Credentials returned PII kind %q", m.Kind)
		}
	}
	if len(pii)+len(cred) != len(Scan(src)) {
		t.Errorf("PII(%d)+Credentials(%d) != Scan(%d)", len(pii), len(cred), len(Scan(src)))
	}
}

func TestScanIndicators(t *testing.T) {
	src := []byte("visit https://example.com/a/b and read /usr/local/bin/tool now")
	got := ScanIndicators(src)
	if len(got) < 2 {
		t.Fatalf("ScanIndicators = %+v, want url + path", got)
	}
	var sawURL, sawPath bool
	for _, m := range got {
		if m.Maskable {
			t.Errorf("indicator must never be maskable: %+v", m)
		}
		switch m.Rule {
		case RuleURL:
			sawURL = true
		case RuleLocalPath:
			sawPath = true
		}
	}
	if !sawURL || !sawPath {
		t.Errorf("ScanIndicators missing url=%v path=%v: %+v", sawURL, sawPath, got)
	}
}

func TestScanAllMergesBothMachines(t *testing.T) {
	src := []byte("key=" + openAIKey + " url=https://example.com/x id=" + validID)
	all := ScanAll(src)
	cred := Scan(src)
	ind := ScanIndicators(src)
	if len(all) != len(cred)+len(ind) {
		t.Fatalf("ScanAll(%d) != Scan(%d)+ScanIndicators(%d)", len(all), len(cred), len(ind))
	}
	for i := 1; i < len(all); i++ {
		if all[i].Start < all[i-1].Start {
			t.Fatalf("ScanAll not ordered: %+v", all)
		}
	}
}

func TestScanRuleAndKind(t *testing.T) {
	src := []byte("key=" + openAIKey + " url=https://example.com/x id=" + validID)
	if got := ScanRule(src, "openai.legacy_key"); len(got) != 1 {
		t.Errorf("ScanRule openai = %+v", got)
	}
	if got := ScanRule(src, RuleURL); len(got) != 1 {
		t.Errorf("ScanRule url = %+v", got)
	}
	if got := ScanRule(src, "no.such.rule"); got != nil {
		t.Errorf("ScanRule unknown = %+v, want nil", got)
	}
	if got := ScanKind(src, "national_id"); len(got) != 1 {
		t.Errorf("ScanKind national_id = %+v", got)
	}
}

func TestEmptyAndCleanInput(t *testing.T) {
	if Scan(nil) != nil || Scan([]byte{}) != nil {
		t.Error("Scan of empty input should be nil")
	}
	if ScanIndicators(nil) != nil || ScanAll(nil) != nil {
		t.Error("indicator scans of empty input should be nil")
	}
	clean := []byte("the quick brown fox jumps over the lazy dog")
	if len(Scan(clean)) != 0 {
		t.Errorf("Scan of clean prose should be empty: %+v", Scan(clean))
	}
}

func TestMatchBytesFailsSafe(t *testing.T) {
	m := Match{Start: 0, End: 10}
	if m.Bytes([]byte("short")) != nil {
		t.Error("Bytes should be nil when span exceeds buffer")
	}
	if (Match{Start: 5, End: 5}).Bytes([]byte("abcdef")) != nil {
		t.Error("Bytes should be nil for an empty span")
	}
}

func keys(m map[string]Match) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
