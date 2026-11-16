package detector

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/kingfs/Trajecta/pkg/detect"
	"github.com/kingfs/Trajecta/pkg/secretmask"
)

// stubScanner emits a fixed match list so the filteredScanner wrapper can be
// exercised once per compiled rule name (plus unknown names) without depending
// on the DFA producing a sample for every rule.
type stubScanner struct{ matches []detect.Match }

func (s stubScanner) Name() string { return "stub" }

func (s stubScanner) Scan(context.Context, detect.Input) ([]detect.Match, error) {
	// A fresh slice per call: filteredScanner filters in place (matches[:0]),
	// so sharing one backing array across calls would corrupt the fixture.
	out := make([]detect.Match, len(s.matches))
	copy(out, s.matches)
	return out, nil
}

// legacyPIIFilter is the pre-optimization filter, verbatim: a per-match linear
// secretmask.RuleByName lookup. It is the equivalence oracle for the
// construction-time piiKinds map.
func legacyPIIFilter(matches []detect.Match, disabled map[string]bool, disablePII bool) []detect.Match {
	out := matches[:0]
	for _, m := range matches {
		if disabled[m.Rule] {
			continue
		}
		if disablePII {
			if rule, ok := secretmask.RuleByName(m.Rule); ok && rule.Kind.PII() {
				continue
			}
		}
		out = append(out, m)
	}
	return out
}

// TestFilteredScannerPIIMapEquivalence proves the precomputed PII rule-name map
// drops exactly the matches the old per-match RuleByName lookup dropped, for
// every compiled rule name plus unknown/empty names, across the settings
// combinations (disable_pii on/off × disabled_rules).
func TestFilteredScannerPIIMapEquivalence(t *testing.T) {
	rules := secretmask.Rules()
	names := make([]string, 0, len(rules)+2)
	for _, r := range rules {
		names = append(names, r.Name)
	}
	names = append(names, "no.such.rule", "")

	matches := make([]detect.Match, len(names))
	for i, name := range names {
		matches[i] = detect.Match{
			Span:       detect.Span{Start: i * 10, End: i*10 + 5},
			Rule:       name,
			Confidence: 1,
			Value:      "value",
			Metadata:   map[string]string{"maskable": "true"},
		}
	}
	stub := stubScanner{matches: matches}

	cases := []struct {
		name     string
		settings secretmaskSettings
	}{
		{"pii_on", secretmaskSettings{DisablePII: true}},
		{"pii_on_with_disabled", secretmaskSettings{DisablePII: true, DisabledRules: []string{"aws.access_key_id", "pii.cn_mobile", "no.such.rule"}}},
		{"pii_off_with_disabled", secretmaskSettings{DisabledRules: []string{"aws.access_key_id"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.settings)
			if err != nil {
				t.Fatal(err)
			}
			// Go through the real constructor so the construction-time map is
			// the one under test, then swap in the stub base.
			s, err := newSecretmaskScanner(raw)
			if err != nil {
				t.Fatal(err)
			}
			fs, ok := s.(*filteredScanner)
			if !ok {
				t.Fatalf("expected *filteredScanner, got %T", s)
			}
			fs.base = stub

			got, err := fs.Scan(context.Background(), detect.Input{Raw: []byte("unused")})
			if err != nil {
				t.Fatal(err)
			}
			// The oracle uses the same disabled map the constructor built and
			// the legacy per-match RuleByName lookup.
			want := legacyPIIFilter(mustMatches(t, stub), fs.disabled, tc.settings.DisablePII)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("filter mismatch\n got: %+v\nwant: %+v", got, want)
			}
		})
	}
}

func mustMatches(t *testing.T, s stubScanner) []detect.Match {
	t.Helper()
	m, err := s.Scan(context.Background(), detect.Input{})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// TestFilteredScannerRealBodyDisablePII runs the real DFA base with disable_pii
// on: credential findings survive, national-ID and mobile findings are dropped.
func TestFilteredScannerRealBodyDisablePII(t *testing.T) {
	raw, _ := json.Marshal(secretmaskSettings{DisablePII: true})
	s, err := newSecretmaskScanner(raw)
	if err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"key":"` + fixtureAWSKey + `","id":"` + fixtureChineseID + `","tel":"13812345678"}`)
	matches, err := s.Scan(context.Background(), detect.Input{Raw: body})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, m := range matches {
		seen[m.Rule] = true
	}
	if !seen["aws.access_key_id"] {
		t.Fatalf("credential finding lost under disable_pii: %v", seen)
	}
	if seen["pii.chinese_id"] || seen["pii.cn_mobile"] {
		t.Fatalf("PII findings must be dropped under disable_pii: %v", seen)
	}

	// With disable_pii off the same body keeps the PII findings.
	s2, err := newSecretmaskScanner(nil)
	if err != nil {
		t.Fatal(err)
	}
	matches2, err := s2.Scan(context.Background(), detect.Input{Raw: body})
	if err != nil {
		t.Fatal(err)
	}
	seen2 := map[string]bool{}
	for _, m := range matches2 {
		seen2[m.Rule] = true
	}
	if !seen2["pii.chinese_id"] || !seen2["pii.cn_mobile"] {
		t.Fatalf("PII findings must survive without disable_pii: %v", seen2)
	}
}
