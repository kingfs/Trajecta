package secretmask

import (
	"strings"
	"testing"
)

// TestValidatorsKnown keeps the rule table and runValidator in step. An
// unrecognised validator name fails closed, which would silently drop every
// match for that rule, so it must be caught here.
func TestValidatorsKnown(t *testing.T) {
	known := map[string]bool{
		"":                    true,
		validatorJWT:          true,
		validatorAnthropicKey: true,
		validatorGitHubPAT:    true,
		validatorJWE:          true,
		validatorBech32:       true,
		validatorChineseID:    true,
	}
	for id := RuleUnknown + 1; id < ruleIDCount; id++ {
		r := ruleTable[id]
		if !known[r.Validator] {
			t.Errorf("rule %q names unknown validator %q", r.Name, r.Validator)
		}
	}
}

// TestConfidencePolicy pins the decision a caller depends on: which findings
// may be rewritten without a human in the loop.
func TestConfidencePolicy(t *testing.T) {
	cases := []struct {
		name       string
		text       string
		wantRule   RuleID
		wantMask   bool
		wantMinist Confidence
	}{
		{
			name:       "openai legacy key is conclusive on shape alone",
			text:       "sk-" + rep("A", 20) + "T3BlbkFJ" + rep("B", 20),
			wantRule:   RuleOpenAILegacy,
			wantMask:   true,
			wantMinist: ConfidenceHigh,
		},
		{
			name:       "google api key exact length is conclusive",
			text:       "AIza" + rep("A", 35),
			wantRule:   RuleGoogleAPIKey,
			wantMask:   true,
			wantMinist: ConfidenceHigh,
		},
		{
			name:       "bare sk- pool is reported but not rewritten",
			text:       "sk-" + rep("a1", 16),
			wantRule:   RuleSkDashGeneric,
			wantMask:   false,
			wantMinist: ConfidenceLow,
		},
		{
			name:       "sk- pool named by an env var becomes rewriteable",
			text:       "DEEPSEEK_API_KEY=" + "sk-" + rep("a1", 16),
			wantRule:   RuleSkDashGeneric,
			wantMask:   true,
			wantMinist: ConfidenceMedium,
		},
		{
			name:       "generic r8_ prefix with an exact length is rewriteable",
			text:       "r8_" + rep("A", 37),
			wantRule:   RuleReplicateToken,
			wantMask:   true,
			wantMinist: ConfidenceMedium,
		},
		{
			name:       "anchored groq prefix alone is not rewriteable",
			text:       "gsk_" + rep("A", 52),
			wantRule:   RuleGroqAPIKey,
			wantMask:   false,
			wantMinist: ConfidenceLow,
		},
		{
			name:       "the same groq key next to its vendor name is rewriteable",
			text:       "GROQ_API_KEY=gsk_" + rep("A", 52),
			wantRule:   RuleGroqAPIKey,
			wantMask:   true,
			wantMinist: ConfidenceHigh,
		},
		{
			name:       "short generic ek_ identifier is rejected outright",
			text:       "ek_from_official_host",
			wantRule:   RuleUnknown,
			wantMask:   false,
			wantMinist: ConfidenceLow,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			findings := Scan([]byte(tc.text))
			if tc.wantRule == RuleUnknown {
				if len(findings) != 0 {
					t.Fatalf("expected no finding, got %+v", findings)
				}
				return
			}
			var got *Finding
			for i := range findings {
				if findings[i].ID == tc.wantRule {
					got = &findings[i]
					break
				}
			}
			if got == nil {
				t.Fatalf("rule %q not matched; findings: %+v", ruleTable[tc.wantRule].Name, findings)
			}
			if got.Maskable() != tc.wantMask {
				t.Errorf("Maskable() = %v, want %v (score %d, confidence %s, reasons %s)",
					got.Maskable(), tc.wantMask, got.Score, got.Confidence, got.Reasons)
			}
			if got.Confidence < tc.wantMinist {
				t.Errorf("confidence = %s, want at least %s (score %d, reasons %s)",
					got.Confidence, tc.wantMinist, got.Score, got.Reasons)
			}
		})
	}
}

// TestJWTValidatorRejectsLookalikes checks that the structural proof actually
// discriminates: three dot-separated base64url runs are not a JWT.
func TestJWTValidatorRejectsLookalikes(t *testing.T) {
	real := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9." +
		"eyJzdWIiOiIxMjM0NTY3ODkwIn0." +
		"SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"
	if validateJWT([]byte(real)) != true {
		t.Fatal("a well-formed HS256 JWT must validate")
	}

	bad := []struct {
		name string
		tok  string
	}{
		{"payload is not base64", "eyJhbGciOiJIUzI1NiJ9.!!!.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"},
		{"header is not JSON", "aGVsbG8gd29ybGQh.eyJzdWIiOiIxIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"},
		{"header names no algorithm", "eyJ0eXAiOiJKV1QifQ.eyJzdWIiOiIxIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"},
		{"signature too short for HS256", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.AAAA"},
		{"only two segments", "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0"},
	}
	for _, tc := range bad {
		if validateJWT([]byte(tc.tok)) {
			t.Errorf("%s: validateJWT accepted %q", tc.name, tc.tok)
		}
	}
}

// TestAnthropicValidator checks the trailing AA marker.
func TestAnthropicValidator(t *testing.T) {
	if !validateAnthropicKey([]byte("sk-ant-api03-" + rep("A", 95))) {
		t.Error("a key ending in AA must validate")
	}
	if validateAnthropicKey([]byte("sk-ant-api03-" + rep("A", 93) + "BB")) {
		t.Error("a key ending in BB must be rejected")
	}
}

// TestContextWindowIsBounded checks that vocabulary far away does not promote a
// generic finding, which would make the gate meaningless on large documents.
func TestContextWindowIsBounded(t *testing.T) {
	tok := "sk-" + rep("a1", 16)
	// Put the vocabulary further away than the window so only the boundary
	// decides the outcome.
	far := "API_KEY" + strings.Repeat("x", contextWindow+40) + " " + tok
	near := "API_KEY=" + tok

	if f, ok := find(Scan([]byte(far)), RuleSkDashGeneric); ok && f.Maskable() {
		t.Errorf("vocabulary more than %d bytes away must not promote a finding (score %d, reasons %s)", contextWindow, f.Score, f.Reasons)
	}
	if f, ok := find(Scan([]byte(near)), RuleSkDashGeneric); !ok || !f.Maskable() {
		t.Errorf("adjacent vocabulary must promote a finding, got %+v", f)
	}
}

func find(fs []Finding, id RuleID) (Finding, bool) {
	for _, f := range fs {
		if f.ID == id {
			return f, true
		}
	}
	return Finding{}, false
}

// TestReasonString covers the audit rendering.
func TestReasonString(t *testing.T) {
	cases := []struct {
		r    Reason
		want string
	}{
		{0, "none"},
		{ReasonStrongShape, "strong_shape"},
		{ReasonStrongShape | ReasonExactLength, "strong_shape,exact_length"},
		{ReasonGenericShape | ReasonContext, "generic_shape,context"},
	}
	for _, tc := range cases {
		if got := tc.r.String(); got != tc.want {
			t.Errorf("Reason(%d).String() = %q, want %q", tc.r, got, tc.want)
		}
	}
}

// TestConfidenceBands pins the band edges.
func TestConfidenceBands(t *testing.T) {
	cases := []struct {
		score int
		want  Confidence
	}{
		{0, ConfidenceLow},
		{44, ConfidenceLow},
		{45, ConfidenceMedium},
		{69, ConfidenceMedium},
		{70, ConfidenceHigh},
		{100, ConfidenceHigh},
	}
	for _, tc := range cases {
		if got := ConfidenceFor(tc.score); got != tc.want {
			t.Errorf("ConfidenceFor(%d) = %s, want %s", tc.score, got, tc.want)
		}
	}
}

// TestStrengthCoverage makes the tiering explicit so a new rule cannot be added
// without deciding how conclusive it is.
func TestStrengthCoverage(t *testing.T) {
	counts := map[Strength]int{}
	for id := RuleUnknown + 1; id < ruleIDCount; id++ {
		switch ruleTable[id].Strength {
		case StrengthGeneric, StrengthAnchored, StrengthStrong:
			counts[ruleTable[id].Strength]++
		default:
			t.Errorf("rule %q has no strength", ruleTable[id].Name)
		}
	}
	// The exact numbers are pinned because two documents state them and the total
	// is part of the coverage claim. Adding a rule moves a tier here and in
	// docs/SECRET_MASKING.md together, which is the point: silently changing how
	// conclusive a shape is, is how a rule starts masking something it should
	// only have reported.
	const wantStrong, wantAnchored, wantGeneric = 35, 24, 18
	if counts[StrengthStrong] != wantStrong || counts[StrengthAnchored] != wantAnchored || counts[StrengthGeneric] != wantGeneric {
		t.Errorf("strength distribution: strong=%d anchored=%d generic=%d, want %d/%d/%d",
			counts[StrengthStrong], counts[StrengthAnchored], counts[StrengthGeneric],
			wantStrong, wantAnchored, wantGeneric)
	}
}

// TestBech32Checksum checks the checksum primitive against the vectors BIP-173
// publishes, rather than against a key this package produced. Without that, a
// wrong generator constant would make every age key fail and the rule's own
// sample would still look self-consistent.
func TestBech32Checksum(t *testing.T) {
	valid := []string{
		"A12UEL5L",
		"a12uel5l",
		"abcdef1qpzry9x8gf2tvdw0s3jn54khce6mua7lmqqqxw",
		"split1checkupstagehandshakeupstreamerranterredcaperred2y9e3w",
	}
	for _, v := range valid {
		if !validateBech32([]byte(v)) {
			t.Errorf("validateBech32(%q) = false, want true", v)
		}
	}
	invalid := []string{
		"A12UEL5M",                           // last checksum character changed
		"a12uel5l1",                          // trailing character past the checksum
		"fi1",                                // too short to carry a checksum
		"A12UEL5L" + strings.Repeat("q", 90), // over the BIP-173 length limit
		"A12uel5l",                           // mixed case, which BIP-173 forbids
		"a12UEL5L",                           // and the same mix the other way round
	}
	for _, v := range invalid {
		if validateBech32([]byte(v)) {
			t.Errorf("validateBech32(%q) = true, want false", v)
		}
	}
}

// TestGitHubPATChecksumCoversTheBody changes one character at each position of
// the body and requires the check to reject it. A validator that only looked at
// the length, or at part of the body, would accept some of these.
func TestGitHubPATChecksumCoversTheBody(t *testing.T) {
	const good = "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"
	if !validateGitHubPAT([]byte(good)) {
		t.Fatalf("validateGitHubPAT(%q) = false, want true", good)
	}
	flip := func(i int) []byte {
		b := []byte(good)
		if b[i] == 'A' {
			b[i] = 'B'
		} else {
			b[i] = 'A'
		}
		return b
	}
	for i := 4; i < 4+githubPATBodyLen; i++ {
		if validateGitHubPAT(flip(i)) {
			t.Errorf("validateGitHubPAT accepted a token with body byte %d changed", i-4)
		}
	}
	for i := 4 + githubPATBodyLen; i < len(good); i++ {
		if validateGitHubPAT(flip(i)) {
			t.Errorf("validateGitHubPAT accepted a token with checksum byte %d changed", i-4-githubPATBodyLen)
		}
	}
}
