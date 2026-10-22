package secretmask

import (
	"encoding/base64"
	"hash/crc32"
)

// Validator names used by the rule table. A rule with a validator only reports
// matches that pass it, and a pass is by itself enough to treat the match as
// conclusive.
//
// "Conclusive" is the bar a validator has to clear, and it is a higher bar than
// "plausible". A check that merely narrows a weak form is not a validator: a
// finding that passes one is rewritten automatically, so a check such as
// "base64 that decodes to 64 bytes" would turn every 64-byte blob in a
// transcript into a masked value. The validators here recompute a checksum the
// issuer published, or decode a field whose content is self-describing.
const (
	validatorJWT          = "jwt"
	validatorAnthropicKey = "anthropic_key"
	validatorGitHubPAT    = "github_pat"
	validatorJWE          = "jwe"
	validatorBech32       = "bech32"
	validatorChineseID    = "chinese_id"
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
	case validatorGitHubPAT:
		return validateGitHubPAT(tok)
	case validatorJWE:
		return validateJWE(tok)
	case validatorBech32:
		return validateBech32(tok)
	case validatorChineseID:
		return validateChineseID(tok)
	default:
		return false
	}
}

// githubBase62 is the alphabet GitHub uses to spell the checksum suffix:
// digits, then uppercase, then lowercase. The order is part of the scheme, so
// getting it wrong rejects every real token rather than accepting bad ones.
const githubBase62 = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// githubPATBodyLen is the random part between the prefix and the checksum.
const githubPATBodyLen = 30

// validateGitHubPAT recomputes the CRC32-IEEE checksum GitHub appends to a
// classic personal access token and compares it with the six characters that
// follow the body. The scheme is published, and a run of the right shape passes
// it by accident with probability 62^-6, so a pass is proof rather than a hint.
func validateGitHubPAT(tok []byte) bool {
	const total = 4 + githubPATBodyLen + 6
	if len(tok) != total {
		return false
	}
	sum := crc32.ChecksumIEEE(tok[4 : 4+githubPATBodyLen])
	var want [6]byte
	for i := 5; i >= 0; i-- {
		want[i] = githubBase62[sum%62]
		sum /= 62
	}
	return bytesEqual(tok[4+githubPATBodyLen:], want[:])
}

// validateJWE checks the protected header of a five-segment compact
// serialization: it has to decode to a JSON object naming a content encryption
// algorithm. A JWT is three segments and carries no "enc", so the two cannot be
// confused in either direction. The segment count is not checked here because
// the pattern that reaches this function already pins it.
func validateJWE(tok []byte) bool {
	first := indexByte(tok, '.')
	if first <= 0 {
		return false
	}
	header, ok := decodeBase64URL(tok[:first], 4096)
	if !ok || !looksLikeJSONObject(header) {
		return false
	}
	_, ok = jsonStringMember(header, "enc")
	return ok
}

// bech32Generators are the coefficients of the BIP-173 checksum generator.
var bech32Generators = [5]uint32{0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3}

// bech32Charset is the BIP-173 data alphabet, in order. It deliberately omits
// 1, b, i and o, which is why the separator cannot be confused with data.
const bech32Charset = "qpzry9x8gf2tvdw0s3jn54khce6mua7l"

// bech32MaxLen is the BIP-173 length limit.
const bech32MaxLen = 90

// validateBech32 verifies the BIP-173 checksum. age spells its secret keys as
// an uppercase bech32 string whose human-readable part is age-secret-key-, so
// the checksum is a real offline proof: a key with one wrong character fails
// it, and no other format in reach shares that prefix.
func validateBech32(tok []byte) bool {
	if len(tok) > bech32MaxLen {
		return false
	}
	// BIP-173 forbids mixing case: a string is either all lower or all upper,
	// and the decoder folds case only after that rule is satisfied. Folding
	// unconditionally would accept a chimera that no encoder produces.
	var upper, lower bool
	for _, c := range tok {
		switch {
		case c >= 'A' && c <= 'Z':
			upper = true
		case c >= 'a' && c <= 'z':
			lower = true
		}
	}
	if upper && lower {
		return false
	}
	sep := lastIndexByte(tok, '1')
	if sep < 1 || sep+7 > len(tok) {
		return false
	}
	hrp := tok[:sep]
	chk := uint32(1)
	for _, c := range hrp {
		lc := lowerASCII(c)
		if lc < 33 || lc > 126 {
			return false
		}
		chk = bech32Step(chk, lc>>5)
	}
	chk = bech32Step(chk, 0)
	for _, c := range hrp {
		chk = bech32Step(chk, lowerASCII(c)&31)
	}
	for _, c := range tok[sep+1:] {
		v := bech32Value(lowerASCII(c))
		if v < 0 {
			return false
		}
		chk = bech32Step(chk, byte(v))
	}
	return chk == 1
}

// bech32Step folds one five-bit value into the running checksum.
func bech32Step(chk uint32, v byte) uint32 {
	b := chk >> 25
	chk = (chk&0x1ffffff)<<5 ^ uint32(v)
	for i, g := range bech32Generators {
		if (b>>uint(i))&1 == 1 {
			chk ^= g
		}
	}
	return chk
}

// bech32Value maps one character to its five-bit value, or -1.
func bech32Value(c byte) int {
	for i := 0; i < len(bech32Charset); i++ {
		if bech32Charset[i] == c {
			return i
		}
	}
	return -1
}

// lastIndexByte is bytes.LastIndexByte for the single-byte case, without the
// import.
func lastIndexByte(b []byte, c byte) int {
	for i := len(b) - 1; i >= 0; i-- {
		if b[i] == c {
			return i
		}
	}
	return -1
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

// chineseIDWeights and chineseIDCheckChars spell out ISO 7064 MOD 11-2, the
// scheme GB 11643 uses for the last character of a resident identity number.
var chineseIDWeights = [17]int{7, 9, 10, 5, 8, 4, 2, 1, 6, 3, 7, 9, 10, 5, 8, 4, 2}

const chineseIDCheckChars = "10X98765432"

// chineseIDProvinceList is the province-level prefix set of GB/T 2260, in code
// order. 71, 81 and 82 are Taiwan, Hong Kong and Macao.
var chineseIDProvinceList = []string{
	"11", "12", "13", "14", "15",
	"21", "22", "23",
	"31", "32", "33", "34", "35", "36", "37",
	"41", "42", "43", "44", "45", "46",
	"50", "51", "52", "53", "54",
	"61", "62", "63", "64", "65",
	"71", "81", "82",
}

// chineseIDProvinceSet is chineseIDProvinceList indexed by the two-digit code,
// built once so the validator is an array lookup. TestChineseIDProvinceTable
// keeps the two in step.
var chineseIDProvinceSet = func() (set [100]bool) {
	for _, p := range chineseIDProvinceList {
		set[int(p[0]-'0')*10+int(p[1]-'0')] = true
	}
	return set
}()

// validateChineseID checks the three independent properties a real identity
// number has: the region code names a province, the birthdate is a calendar
// date, and the last character is the ISO 7064 MOD 11-2 check character of the
// first seventeen. The check character alone filters ten of eleven random digit
// runs; the calendar cuts the rest by two orders of magnitude, and the province
// prefix removes most of what is left.
//
// The birth-year bounds are fixed rather than read from the clock: a validator
// that answered differently as time passed would make an old cassette
// re-analyze to different findings. The lower bound admits the 19th-century
// births still present in archived records; the upper bound is the last year a
// four-digit birth year can plausibly carry.
func validateChineseID(tok []byte) bool {
	if len(tok) != 18 {
		return false
	}
	for i := 0; i < 17; i++ {
		if tok[i] < '0' || tok[i] > '9' {
			return false
		}
	}
	c := tok[17]
	if c == 'x' {
		c = 'X'
	}
	if (c < '0' || c > '9') && c != 'X' {
		return false
	}
	if !chineseIDProvinceSet[int(tok[0]-'0')*10+int(tok[1]-'0')] {
		return false
	}
	year := int(tok[6]-'0')*1000 + int(tok[7]-'0')*100 + int(tok[8]-'0')*10 + int(tok[9]-'0')
	month := int(tok[10]-'0')*10 + int(tok[11]-'0')
	day := int(tok[12]-'0')*10 + int(tok[13]-'0')
	if year < 1800 || year > 2099 || !validCalendarDate(year, month, day) {
		return false
	}
	return chineseIDCheckChar(tok[:17]) == c
}

// chineseIDCheckChar returns the ISO 7064 MOD 11-2 check character for the
// seventeen leading digits.
func chineseIDCheckChar(digits []byte) byte {
	sum := 0
	for i := 0; i < 17; i++ {
		sum += int(digits[i]-'0') * chineseIDWeights[i]
	}
	return chineseIDCheckChars[sum%11]
}

// validCalendarDate reports whether the day exists in the month, including the
// leap-year rule.
func validCalendarDate(year, month, day int) bool {
	switch month {
	case 1, 3, 5, 7, 8, 10, 12:
		return day >= 1 && day <= 31
	case 4, 6, 9, 11:
		return day >= 1 && day <= 30
	case 2:
		leap := year%4 == 0 && (year%100 != 0 || year%400 == 0)
		if leap {
			return day >= 1 && day <= 29
		}
		return day >= 1 && day <= 28
	default:
		return false
	}
}
