package secretmask

import "encoding/base64"

// Validator names used by the rule table. A rule with a validator only reports
// matches that pass it, and a pass is by itself enough to treat the match as
// conclusive.
const (
	validatorJWT          = "jwt"
	validatorAnthropicKey = "anthropic_key"
)

// runValidator dispatches to the named offline check. An unrecognised name
// fails closed so that a rule cannot silently lose its check; TestValidatorsKnown
// keeps the table and this switch in step.
func runValidator(name string, tok []byte) bool {
	switch name {
	case "":
		return true
	case validatorJWT:
		return validateJWT(tok)
	case validatorAnthropicKey:
		return validateAnthropicKey(tok)
	default:
		return false
	}
}

// validateAnthropicKey checks the trailing "AA" that all published Anthropic
// key families carry. The reference notes that flipping it to "BB" makes
// scanners reject the value, so it is a real structural marker rather than
// decoration.
func validateAnthropicKey(tok []byte) bool {
	return len(tok) >= 2 && tok[len(tok)-2] == 'A' && tok[len(tok)-1] == 'A'
}

// jwtMinSigBytes rejects signatures too short to be real, which is what
// separates a JWT from three dot-separated base64 runs.
const jwtMinSigBytes = 16

// validateJWT decodes a candidate and checks the structural invariants a real
// JWT has: three base64url segments, a JSON object header naming an algorithm,
// a JSON object payload, and a signature whose decoded length is consistent
// with that algorithm.
func validateJWT(tok []byte) bool {
	first := indexByte(tok, '.')
	if first <= 0 {
		return false
	}
	rest := tok[first+1:]
	second := indexByte(rest, '.')
	if second <= 0 {
		return false
	}
	header, payload, sig := tok[:first], rest[:second], rest[second+1:]
	if len(sig) == 0 {
		return false
	}

	headerJSON, ok := decodeBase64URL(header, 4096)
	if !ok {
		return false
	}
	if !looksLikeJSONObject(headerJSON) {
		return false
	}
	alg, ok := jsonStringMember(headerJSON, "alg")
	if !ok || alg == "" {
		return false
	}

	payloadJSON, ok := decodeBase64URL(payload, 1<<20)
	if !ok || !looksLikeJSONObject(payloadJSON) {
		return false
	}

	sigLen := decodedBase64URLLen(sig)
	if sigLen < jwtMinSigBytes || sigLen > 4096 {
		return false
	}
	if want, known := jwtSignatureBytes[alg]; known && sigLen != want {
		return false
	}
	return true
}

// jwtSignatureBytes maps a JWS algorithm to its signature length. The map is
// only consulted as a consistency oracle: an algorithm absent from it still
// passes, it just gets no extra confirmation.
var jwtSignatureBytes = map[string]int{
	"HS256": 32,
	"HS384": 48,
	"HS512": 64,
	"RS256": 32,
	"RS384": 48,
	"RS512": 64,
	"PS256": 32,
	"PS384": 48,
	"PS512": 64,
	"ES256": 64,
	"ES384": 96,
	"ES512": 132,
	"EdDSA": 64,
}

func indexByte(b []byte, c byte) int {
	for i := 0; i < len(b); i++ {
		if b[i] == c {
			return i
		}
	}
	return -1
}

func looksLikeJSONObject(b []byte) bool {
	for _, c := range b {
		switch c {
		case ' ', '\t', '\r', '\n':
			continue
		case '{':
			return true
		default:
			return false
		}
	}
	return false
}

// jsonStringMember finds "name":"value" without pulling in encoding/json, which
// would allocate a map per candidate.
func jsonStringMember(obj []byte, name string) (string, bool) {
	needle := make([]byte, 0, len(name)+2)
	needle = append(needle, '"')
	needle = append(needle, name...)
	needle = append(needle, '"')
	i := indexBytes(obj, needle)
	if i < 0 {
		return "", false
	}
	i += len(needle)
	for i < len(obj) && (obj[i] == ' ' || obj[i] == '\t' || obj[i] == '\r' || obj[i] == '\n') {
		i++
	}
	if i >= len(obj) || obj[i] != ':' {
		return "", false
	}
	i++
	for i < len(obj) && (obj[i] == ' ' || obj[i] == '\t' || obj[i] == '\r' || obj[i] == '\n') {
		i++
	}
	if i >= len(obj) || obj[i] != '"' {
		return "", false
	}
	i++
	start := i
	for i < len(obj) && obj[i] != '"' {
		if obj[i] == '\\' {
			return "", false
		}
		i++
	}
	if i >= len(obj) {
		return "", false
	}
	return string(obj[start:i]), true
}

func indexBytes(hay, needle []byte) int {
	if len(needle) == 0 || len(needle) > len(hay) {
		return -1
	}
	for i := 0; i+len(needle) <= len(hay); i++ {
		match := true
		for j := range needle {
			if hay[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// decodeBase64URL decodes a JWT segment, accepting both raw and padded
// encodings. It refuses anything longer than limit so a hostile body cannot
// make validation allocate without bound.
func decodeBase64URL(seg []byte, limit int) ([]byte, bool) {
	if len(seg) == 0 || len(seg) > limit {
		return nil, false
	}
	if out, err := base64.RawURLEncoding.DecodeString(string(seg)); err == nil {
		return out, true
	}
	out, err := base64.URLEncoding.DecodeString(string(seg))
	if err != nil {
		return nil, false
	}
	return out, true
}

// decodedBase64URLLen returns the decoded length without allocating.
func decodedBase64URLLen(seg []byte) int {
	n := len(seg)
	for n > 0 && seg[n-1] == '=' {
		n--
	}
	return n * 6 / 8
}
