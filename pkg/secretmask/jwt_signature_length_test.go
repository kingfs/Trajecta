package secretmask

import (
	"encoding/base64"
	"strings"
	"testing"
)

// jwtTokenForTest builds a structurally real JWT for one algorithm with a
// signature of exactly sigBytes raw bytes.
func jwtTokenForTest(alg string, sigBytes int) string {
	encode := func(value []byte) string {
		return base64.RawURLEncoding.EncodeToString(value)
	}
	header := encode([]byte(`{"alg":"` + alg + `","typ":"JWT"}`))
	payload := encode([]byte(`{"sub":"1234567890","name":"John Doe"}`))
	signature := encode([]byte(strings.Repeat("a", sigBytes)))
	return header + "." + payload + "." + signature
}

func jwtFinding(tok string) (Finding, bool) {
	for _, finding := range Scan([]byte(tok)) {
		if finding.ID == RuleJWT {
			return finding, true
		}
	}
	return Finding{}, false
}

// TestJWTRSAPSSignatureLengths pins the RSA/RSASSA-PSS signature sizes: RS*/PS*
// carry the RSA modulus size (256/384/512 bytes), not the HMAC digest size. A
// real-shaped RS256 JWT was reported by the DFA but rejected by the validator
// (it expected 32 bytes) and therefore never masked.
func TestJWTRSAPSSignatureLengths(t *testing.T) {
	want := map[string]int{
		"RS256": 256, "RS384": 384, "RS512": 512,
		"PS256": 256, "PS384": 384, "PS512": 512,
	}
	for alg, size := range want {
		if got := jwtSignatureBytes[alg]; got != size {
			t.Errorf("jwtSignatureBytes[%q] = %d, want %d", alg, got, size)
		}
		tok := jwtTokenForTest(alg, size)
		if !validateJWT([]byte(tok)) {
			t.Errorf("%s: validateJWT rejected a %d-byte signature", alg, size)
		}
		finding, ok := jwtFinding(tok)
		if !ok {
			t.Fatalf("%s: Scan reported no JWT finding for a well-formed token", alg)
		}
		if !finding.Maskable() {
			t.Errorf("%s: finding is not maskable (score %d, confidence %s)", alg, finding.Score, finding.Confidence)
		}
	}
}

// TestJWTRS256RejectsHMACLengthSignature pins the other side of the fix: an
// RS256 token whose signature is the HS256 digest size is malformed, so the
// validator rejects it and it is not reported.
func TestJWTRS256RejectsHMACLengthSignature(t *testing.T) {
	tok := jwtTokenForTest("RS256", 32)
	if validateJWT([]byte(tok)) {
		t.Fatal("validateJWT accepted an RS256 token with a 32-byte signature")
	}
	if _, ok := jwtFinding(tok); ok {
		t.Fatal("Scan reported a JWT finding for an RS256 token with a 32-byte signature")
	}
}

// TestJWTUnchangedAlgorithms keeps the pre-existing HMAC/ECDSA/EdDSA behaviour:
// the right length validates and the wrong one is rejected.
func TestJWTUnchangedAlgorithms(t *testing.T) {
	cases := []struct {
		alg        string
		validBytes int
		badBytes   int
	}{
		{alg: "HS256", validBytes: 32, badBytes: 48},
		{alg: "HS384", validBytes: 48, badBytes: 64},
		{alg: "HS512", validBytes: 64, badBytes: 32},
		{alg: "ES256", validBytes: 64, badBytes: 32},
		{alg: "ES384", validBytes: 96, badBytes: 64},
		{alg: "ES512", validBytes: 132, badBytes: 96},
		{alg: "EdDSA", validBytes: 64, badBytes: 32},
	}
	for _, tc := range cases {
		t.Run(tc.alg, func(t *testing.T) {
			if !validateJWT([]byte(jwtTokenForTest(tc.alg, tc.validBytes))) {
				t.Fatalf("%s: validateJWT rejected the correct %d-byte signature", tc.alg, tc.validBytes)
			}
			if validateJWT([]byte(jwtTokenForTest(tc.alg, tc.badBytes))) {
				t.Fatalf("%s: validateJWT accepted a %d-byte signature", tc.alg, tc.badBytes)
			}
		})
	}
}
