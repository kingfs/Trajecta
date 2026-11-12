package secretmask

import "testing"

// Every positive sample of every rule the machine can emit must pass the gate.
// A new rule without a trigger (or with a sample that does not carry it) fails
// here instead of silently losing detections in production.
func TestPrefilterCoversEveryPositiveSample(t *testing.T) {
	for _, tc := range positiveSamples {
		rule := ruleTable[tc.rule]
		if rule.ReportOnly {
			continue // indicator.rl is a separate machine, not gated by this one
		}
		if !mayContainSecret([]byte(tc.text)) {
			t.Errorf("prefilter refuses rule %q sample %q", rule.Name, tc.text)
		}
	}
}

// A rule that is neither prefixed nor recognised by a shape check would be
// invisible behind the gate. Fail the build when one is added.
func TestPrefilterCoversEveryRule(t *testing.T) {
	for id := RuleUnknown + 1; id < ruleIDCount; id++ {
		rule := &ruleTable[id]
		if rule.ReportOnly || rule.Name == "" {
			continue
		}
		if len(rule.Prefixes) > 0 {
			continue
		}
		if _, ok := shapeTriggerNames[rule.Name]; ok {
			continue
		}
		if nativeTriggerRuleNames[rule.Name] {
			continue
		}
		t.Errorf("rule %q has no prefilter trigger: add Prefixes, a shape check, or a native literal", rule.Name)
	}
}

// The gate must not change what the DFA reports for any known sample.
func TestPrefilterKeepsDFAFindings(t *testing.T) {
	for _, tc := range positiveSamples {
		rule := ruleTable[tc.rule]
		if rule.ReportOnly {
			continue
		}
		src := []byte(tc.text)
		var want []Finding
		scanDFA(src, &collector{src: src, findings: want})
		got := Detector{}.Scan(src)
		if len(got) == 0 {
			t.Errorf("rule %q: Scan lost its finding for %q", rule.Name, tc.text)
		}
	}
}

func TestPrefilterRejectsBodiesWithoutTriggers(t *testing.T) {
	cases := []string{
		`{"model":"gpt-5","messages":[{"role":"user","content":"please review the diff"}]}`,
		`{"code":200,"status":"ok","items":[1,2,3],"ids":["a","b"]}`,
		"",
	}
	for _, body := range cases {
		if mayContainSecret([]byte(body)) {
			t.Errorf("gate accepted a body with no trigger: %q", body)
		}
	}
}

func TestPrefilterAcceptsShapeAndNativeTriggers(t *testing.T) {
	cases := []string{
		"AKIAIOSFODNN7EXAMPLE",              // literal prefix
		"13800138000",                       // 11-digit mobile shape
		"11010519491231002X",                // 18-digit identity shape
		"12345678901234567X",                // 17 digits + check character
		"0123456789abcdef0123456789abcdef.", // 32 hex + '.' (Zhipu)
		"POSTGRES_PASSWORD=hunter2",         // native assignment
		"https://user:pw@example.com",       // DSN
		"eyJhbGciOiJIUzI1NiJ9.x.y",          // JWT
	}
	for _, body := range cases {
		if !mayContainSecret([]byte(body)) {
			t.Errorf("gate refused a triggering body: %q", body)
		}
	}
}
