package session

import (
	"encoding/json"
	"strings"
	"unicode/utf8"
)

// sessionFieldKeys are the top-level string fields fromBody decodes. metadata
// is handled separately because its signal lives one level deeper.
var sessionFieldKeys = [...]string{
	"previous_response_id",
	"prompt_cache_key",
	"conversation_id",
	"session_id",
}

// bodyHasSessionKey reports whether a JSON object body (body[0] == '{') has
// any top-level member that could feed fromBody's decode: one of the four
// session string fields, or a metadata object with a direct session_id member.
//
// It is a conservative pre-filter, not a parser, and it is sound in the only
// direction that matters:
//
//   - On a valid JSON body it visits every top-level key (and every direct key
//     of each metadata object), so a false answer proves the full decode would
//     leave every tracked field zero: fromBody returns the zero Ref without
//     running json.Unmarshal at all. This is the common SDK case.
//   - On a malformed body either answer is byte-identical to the old behaviour:
//     false keeps the zero Ref that json.Unmarshal's failure also produces,
//     and true falls back to the exact historical decode.
//
// Key matching over-approximates encoding/json's case-insensitive field
// matching: ASCII literals are folded in place, and escaped or non-ASCII
// spellings (`\u0073ession_id`, `ſession_id`, a Kelvin-sign `k`) are decoded
// exactly as encoding/json decodes object keys and then folded. The scan is
// iterative (a hostile deeply-nested body cannot blow the stack) and the ASCII
// fast path never allocates.
func bodyHasSessionKey(body []byte) bool {
	matched, _ := scanObjectKeys(body, 0, scanTopLevel)
	return matched
}

type scanMode uint8

const (
	scanTopLevel scanMode = iota
	scanMetadata
)

// scanObjectKeys walks the direct members of the object whose '{' is at b[i].
// ok reports whether the walk completed structurally; a false ok means the
// body is malformed and the caller owes no fidelity beyond the zero Ref.
func scanObjectKeys(b []byte, i int, mode scanMode) (matched, ok bool) {
	i++ // '{'
	for {
		i = skipScanSpace(b, i)
		if i >= len(b) {
			return false, false
		}
		if b[i] == '}' {
			return false, true
		}
		if b[i] != '"' {
			return false, false
		}
		lit, next, ok := scanStringLiteral(b, i)
		if !ok {
			return false, false
		}
		i = skipScanSpace(b, next)
		if i >= len(b) || b[i] != ':' {
			return false, false
		}
		value := skipScanSpace(b, i+1)
		if value >= len(b) {
			return false, false
		}
		switch mode {
		case scanTopLevel:
			for _, name := range sessionFieldKeys {
				if keyMatchesName(lit, name) {
					return true, true
				}
			}
			// metadata only signals through a direct session_id member of an
			// object value; every other shape (string, array, null, ...) makes
			// metadataSession return "" without affecting the other fields.
			// Any occurrence counts: a later duplicate may still win the
			// RawMessage decode, so an earlier signal-shaped metadata forces
			// the exact path, which resolves duplicates authoritatively.
			if b[value] == '{' && keyMatchesName(lit, "metadata") {
				m, innerOK := scanObjectKeys(b, value, scanMetadata)
				if m || !innerOK {
					return true, true
				}
			}
		case scanMetadata:
			if keyMatchesName(lit, "session_id") {
				return true, true
			}
		}
		i, ok = skipScanValue(b, value)
		if !ok {
			return false, false
		}
		i = skipScanSpace(b, i)
		if i >= len(b) {
			return false, false
		}
		if b[i] == ',' {
			i++
			continue
		}
		if b[i] == '}' {
			return false, true
		}
		return false, false
	}
}

// scanStringLiteral returns the raw bytes between the quotes of the string
// starting at b[i] == '"' and the position after the closing quote. Escape
// sequences are stepped over, not decoded; the caller decides whether the
// literal needs decoding.
func scanStringLiteral(b []byte, i int) (lit []byte, next int, ok bool) {
	start := i + 1
	i++
	for i < len(b) {
		switch b[i] {
		case '\\':
			i += 2
		case '"':
			return b[start:i], i + 1, true
		default:
			i++
		}
	}
	return nil, i, false
}

// skipScanValue skips one JSON value starting at or after b[i] (whitespace
// included) and returns the position after it. Containers are skipped with a
// depth counter that steps over strings, so brackets inside string values do
// not confuse it. Scalars are consumed up to the next structural byte; their
// content is deliberately not validated because a malformed body owes only
// the zero Ref.
func skipScanValue(b []byte, i int) (int, bool) {
	i = skipScanSpace(b, i)
	if i >= len(b) {
		return i, false
	}
	switch b[i] {
	case '"':
		_, next, ok := scanStringLiteral(b, i)
		return next, ok
	case '{', '[':
		depth := 0
		for i < len(b) {
			switch b[i] {
			case '{', '[':
				depth++
			case '}', ']':
				depth--
				if depth == 0 {
					return i + 1, true
				}
			case '"':
				var ok bool
				_, i, ok = scanStringLiteral(b, i)
				if !ok {
					return i, false
				}
				continue
			}
			i++
		}
		return i, false
	default:
		for i < len(b) {
			switch b[i] {
			case ',', '}', ']', ' ', '\t', '\r', '\n':
				return i, true
			}
			i++
		}
		return i, true
	}
}

func skipScanSpace(b []byte, i int) int {
	for i < len(b) {
		switch b[i] {
		case ' ', '\t', '\r', '\n':
			i++
		default:
			return i
		}
	}
	return i
}

// keyMatchesName reports whether a raw key literal could match name under
// encoding/json's case-insensitive field matching. The ASCII fast path folds
// in place; a literal carrying escapes or non-ASCII bytes is decoded exactly
// as encoding/json decodes object keys (including invalid-UTF-8 replacement)
// and then folded. A decode failure means a malformed body: answer true so
// the exact decode path produces the authoritative result.
func keyMatchesName(lit []byte, name string) bool {
	if len(lit) == len(name) && equalFoldASCII(lit, name) {
		return true
	}
	plain := true
	for _, c := range lit {
		if c == '\\' || c >= utf8.RuneSelf {
			plain = false
			break
		}
	}
	if plain {
		return false
	}
	quoted := make([]byte, 0, len(lit)+2)
	quoted = append(quoted, '"')
	quoted = append(quoted, lit...)
	quoted = append(quoted, '"')
	var key string
	if err := json.Unmarshal(quoted, &key); err != nil {
		return true
	}
	return strings.EqualFold(key, name)
}

func equalFoldASCII(lit []byte, name string) bool {
	for j := 0; j < len(lit); j++ {
		c := lit[j]
		if 'a' <= c && c <= 'z' {
			c -= 'a' - 'A'
		}
		n := name[j]
		if 'a' <= n && n <= 'z' {
			n -= 'a' - 'A'
		}
		if c != n {
			return false
		}
	}
	return true
}
