package secretmask

// Name-anchored assignment detection.
//
// The DFA in rules.rl matches a credential by its lexical shape: a vendor
// prefix, a checksum, an exact length. A secret that only appears as the value
// half of an assignment - `POSTGRES_PASSWORD=...`, `DATABASE_DSN=...`, a
// docker-compose `auth_password: ...`, a JSON member `\"TOKEN\": \"...\"` - has
// no shape of its own when the value is an opaque database password. The anchor
// is the variable name, and that is what this file scans for.
//
// The walk is deliberately native and not a Ragel pattern:
//
//   - It is one O(n) pass over the bytes, triggered by '=' and ':', with a
//     bounded (<= 64 byte) look-back for the name. It allocates nothing on the
//     no-hit path, which matters because it runs on every proxied request body.
//   - It must never parse JSON. A body is protocol-shaped text whose nesting
//     depth varies (a tool result can itself be a JSON document inside a JSON
//     string), and a JSON parse would either allocate or change the byte
//     semantics. The scanner instead recognises the separators and escapes it
//     finds in place and masks the bytes between them, so the surrounding
//     document is untouched.
//   - Its spans merge with the DFA findings under the package invariant that
//     spans never overlap. A name-anchored span that strictly contains a DFA
//     finding replaces it; on any other overlap the DFA finding wins and the
//     name-anchored one is dropped.

import (
	"bytes"
	"sort"
)

const (
	// assignmentMaxNameLen bounds the look-back for a variable name. Real env
	// names are far shorter; the bound is what keeps the per-separator cost
	// constant.
	assignmentMaxNameLen = 64
	// assignmentMinBuffer is the shortest buffer worth walking.
	assignmentMinBuffer = 8
	// assignmentURLPasswordMin is the value floor for the userinfo password of a
	// URL-class assignment. It is short on purpose: a database password can be
	// short, and the URL syntax around it is the evidence.
	assignmentURLPasswordMin = 3
	// assignmentMaxPlaceholderWordLen is the longest entry of
	// assignmentPlaceholderWords; a value longer than it cannot be one of them.
	assignmentMaxPlaceholderWordLen = 10
	// assignmentMaxQuotedValueLen rejects a quoted blob that is a document, not
	// a value. A shell line such as grep '^PASSWORD=' closes its regex quote
	// right after the '=', and reading that closer as an opener would mask
	// hundreds of bytes of command text. A real quoted secret is far shorter;
	// a private key longer than this is still found by the PEM rule inside the
	// value, which keeps the armor and its label correct.
	assignmentMaxQuotedValueLen = 1024
)

// assignmentNameClass is what the name before the separator says about the
// value. A URL-class name masks only the userinfo password; every other class
// masks the whole value. minLen is the value floor for the class.
type assignmentNameClass struct {
	urlClass bool
	minLen   int
}

func (c assignmentNameClass) ok() bool { return c.minLen > 0 }

// appendAssignmentFindings appends every name-anchored assignment finding in
// src to dst. dfa is the DFA finding set for the same buffer, sorted by start;
// a candidate whose span is already a maskable DFA finding exactly is skipped
// here rather than in mergeFindings, because that is the common case on a body
// that labels every credential, and dropping it early keeps the slice out of
// the allocation path.
//
// The walk advances by separator, not by byte: bytes.IndexByte is the
// vectorized byte search in the standard library, and keeping the next position
// of each of the two trigger bytes means every byte of the body is scanned at
// most twice no matter how dense the separators are. That matters because this
// runs on every proxied request body; a naive byte-at-a-time loop measured
// ~2 ns/byte and dominated the added cost.
func appendAssignmentFindings(dst []Finding, src []byte, dfa []Finding) []Finding {
	if len(src) < assignmentMinBuffer {
		return dst
	}
	nextEq := bytes.IndexByte(src, '=')
	nextCol := bytes.IndexByte(src, ':')
	for {
		at := -1
		switch {
		case nextEq < 0 && nextCol < 0:
			return dst
		case nextEq < 0:
			at = nextCol
		case nextCol < 0:
			at = nextEq
		case nextEq < nextCol:
			at = nextEq
		default:
			at = nextCol
		}
		if assignmentDfaCoversValueStart(dfa, src, at) {
			// The DFA already matched the value and would win the merge (the
			// candidate can only equal or be contained in that span), so the
			// name adds nothing and the value does not have to be parsed. This
			// is the labeled-credential body case, and it is why the pass costs
			// almost nothing on a body that names every key.
		} else if f, valueEnd, ok := assignmentAt(src, at); ok {
			if !assignmentAlreadyMasked(dfa, f) {
				dst = append(dst, f)
			}
			// Skip the value: a separator inside it must not produce a second,
			// overlapping finding for bytes the first span already covers.
			nextEq = -1
			if i := bytes.IndexByte(src[valueEnd:], '='); i >= 0 {
				nextEq = valueEnd + i
			}
			nextCol = -1
			if i := bytes.IndexByte(src[valueEnd:], ':'); i >= 0 {
				nextCol = valueEnd + i
			}
			continue
		}
		from := at + 1
		if at == nextEq {
			nextEq = -1
			if i := bytes.IndexByte(src[from:], '='); i >= 0 {
				nextEq = from + i
			}
		} else {
			nextCol = -1
			if i := bytes.IndexByte(src[from:], ':'); i >= 0 {
				nextCol = from + i
			}
		}
	}
}

// assignmentDfaCoversValueStart reports whether a maskable DFA finding starts at
// the first byte of this separator's value and reaches past the value's end. In
// that case the name-anchored candidate can only equal or be contained in the
// DFA span, so mergeFindings would drop it; the value does not need parsing.
//
// The fast path only applies when the value starts immediately at sep+1: a
// quote, a space or a backslash changes where the value starts, and those forms
// go through the full parse.
func assignmentDfaCoversValueStart(dfa []Finding, src []byte, sep int) bool {
	if len(dfa) == 0 || sep+1 >= len(src) {
		return false
	}
	switch src[sep+1] {
	case ' ', '\t', '"', '\'', '\\':
		return false
	}
	valueStart := sep + 1
	i := sort.Search(len(dfa), func(i int) bool { return dfa[i].Start >= valueStart })
	if i >= len(dfa) || dfa[i].Start != valueStart || !dfa[i].Maskable() {
		return false
	}
	end := dfa[i].End
	if end >= len(src) {
		return true
	}
	if assignmentUnquotedTerminator(src[end]) {
		return true
	}
	return src[end] == '\\' && end+1 < len(src) &&
		(src[end+1] == 'n' || src[end+1] == 'r' || src[end+1] == 't' || src[end+1] == '"')
}

// assignmentAlreadyMasked reports whether the DFA has already found exactly this
// span and considers it maskable. mergeFindings would drop the name-anchored
// candidate in that case, so it never has to be collected.
func assignmentAlreadyMasked(dfa []Finding, f Finding) bool {
	if len(dfa) == 0 {
		return false
	}
	i := sort.Search(len(dfa), func(i int) bool { return dfa[i].Start >= f.Start })
	if i >= len(dfa) || dfa[i].Start != f.Start || dfa[i].End != f.End {
		return false
	}
	return dfa[i].Maskable()
}

// assignmentAt decides the assignment whose separator byte is at sep.
//
// It returns the finding, the end of the value (which the walk uses to skip
// ahead) and whether a finding was produced.
func assignmentAt(src []byte, sep int) (Finding, int, bool) {
	// Three name bytes are the shortest identifier any rule accepts, so a
	// separator with fewer bytes (or a non-name byte) in front of it is not a
	// candidate. This is the first and cheapest rejection.
	back := sep
	for back > 0 && (src[back-1] == ' ' || src[back-1] == '\t') {
		back--
	}
	if back < 3 || !assignmentNameByte(src[back-1]) || !assignmentNameByte(src[back-2]) || !assignmentNameByte(src[back-3]) {
		return Finding{}, 0, false
	}
	nameStart, nameEnd := assignmentNameBounds(src, sep)
	if nameEnd <= nameStart {
		return Finding{}, 0, false
	}
	// The second rejection is the hot-path filter: a name is only a credential
	// name if it ends with one of the vocabulary words, so a body full of JSON
	// members ("content", "model", "temperature") is rejected on three bytes
	// without tokenizing or allocating.
	if !assignmentLikelyName(src[nameStart:nameEnd]) {
		return Finding{}, 0, false
	}
	// `${NAME:-default}` and `${NAME=default}` are variable references, not
	// assignments: the value is a fallback for a variable the script did not
	// set, and masking it would rewrite the script's default rather than a
	// secret that left the machine.
	if assignmentFollowsDollar(src, nameStart) {
		return Finding{}, 0, false
	}
	class := classifyAssignmentName(src[nameStart:nameEnd])
	if !class.ok() {
		return Finding{}, 0, false
	}
	valueStart, valueEnd, quoted, ok := assignmentValueSpan(src, sep+1)
	if !ok {
		return Finding{}, 0, false
	}
	if quoted && valueEnd-valueStart > assignmentMaxQuotedValueLen {
		// A closing shell quote read as an opener spans the rest of the line.
		return Finding{}, valueEnd, false
	}
	value := src[valueStart:valueEnd]
	if len(value) < class.minLen || assignmentValueIsSkipped(value, quoted) {
		return Finding{}, valueEnd, false
	}
	if class.urlClass {
		pwStart, pwEnd, ok := assignmentURLPassword(src, valueStart, valueEnd)
		if !ok {
			return Finding{}, valueEnd, false
		}
		password := src[pwStart:pwEnd]
		if len(password) < assignmentURLPasswordMin || assignmentValueIsSkipped(password, false) {
			return Finding{}, valueEnd, false
		}
		return newAssignmentFinding(RuleEnvURLCredential, pwStart, pwEnd), valueEnd, true
	}
	return newAssignmentFinding(RuleEnvSecretAssignment, valueStart, valueEnd), valueEnd, true
}

// assignmentNameBounds returns the byte range of the name that precedes sep,
// looking back over optional spaces (so `NAME = value` is covered) and at most
// assignmentMaxNameLen name bytes.
//
// An escape sequence in the raw body is a separator, not part of the name: in a
// recorded tool result a line break is the two bytes '\' 'n', so the assignment
// after it reads `\nKEY=` and only KEY is the name. The same loop keeps the
// backslash and quote of an escaped JSON member name (`\"KEY\":`), which do
// belong to the spelling and are stripped by trimAssignmentName.
func assignmentNameBounds(src []byte, sep int) (int, int) {
	i := sep
	for i > 0 && (src[i-1] == ' ' || src[i-1] == '\t') {
		i--
	}
	end := i
	for i > 0 && end-i < assignmentMaxNameLen {
		if src[i-1] == '\\' && i < end {
			switch src[i] {
			case 'n', 'r', 't':
				return i + 1, end
			}
		}
		if !assignmentNameByte(src[i-1]) {
			break
		}
		i--
	}
	return i, end
}

// assignmentNameByte is a byte that may appear in a variable name, including
// the quote and backslash an escaped JSON member name carries and the
// separators the tokenizer splits on.
func assignmentNameByte(c byte) bool {
	switch {
	case c >= '0' && c <= '9', c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		return true
	case c == '_', c == '-', c == '.', c == '/', c == '\\', c == '"':
		return true
	}
	return false
}

// assignmentFollowsDollar reports whether the name starts a shell variable
// reference, `${NAME` or `$NAME`.
func assignmentFollowsDollar(src []byte, nameStart int) bool {
	if nameStart <= 0 {
		return false
	}
	if src[nameStart-1] == '$' {
		return true
	}
	return nameStart > 1 && src[nameStart-1] == '{' && src[nameStart-2] == '$'
}

// classifyAssignmentName maps a variable name to the assignment class it
// selects, or the zero class when the name is not credential vocabulary.
//
// The comparison is case-insensitive and allocation-free. Names are tokenized
// by the caller-visible separators and by camelCase boundaries, and the
// no-separator env spellings (PGPASSWORD, AUTHTOKEN, CLIENT_SECRET written as a
// single run) are recognised by a raw suffix rule. KEY is the exception: it is
// only a key name as the exact bare name, after a separator, or inside APIKEY,
// so MONKEY and TURKEY do not match.
func classifyAssignmentName(raw []byte) assignmentNameClass {
	name := trimAssignmentName(raw)
	if len(name) == 0 {
		return assignmentNameClass{}
	}
	if assignmentNameExcluded(name) {
		return assignmentNameClass{}
	}
	tok, separated := assignmentLastToken(name)
	// URL-class names describe a whole connection string. The class is decided
	// before the secret names so `DATABASE_URL` cannot fall into a plain-name
	// rule and get the whole address masked.
	switch {
	case assignmentFoldEqual(tok, "DSN"),
		assignmentFoldEqual(tok, "URI"),
		assignmentFoldEqual(tok, "URL"),
		assignmentSuffixEqual(name, "CONNECTION_STRING"):
		return assignmentNameClass{urlClass: true, minLen: assignmentURLPasswordMin}
	}
	// Compound names whose last token (KEY, TOKEN, SECRET) is too weak on its
	// own but whose pair is conventional. They are matched on the raw name so a
	// separator is required; `MONKEY` never reaches this branch.
	switch {
	case assignmentSuffixEqual(name, "PRIVATE_KEY"),
		assignmentSuffixEqual(name, "ACCESS_KEY"),
		assignmentSuffixEqual(name, "AUTH_TOKEN"):
		return assignmentNameClass{minLen: 16}
	case assignmentSuffixEqual(name, "CLIENT_SECRET"),
		assignmentSuffixEqual(name, "JWT_SECRET"):
		return assignmentNameClass{minLen: 6}
	}
	// Exact bare names. TOKEN/PASSWORD/SECRET also arrive through the token
	// case below; they are listed here so the rule is readable on its own.
	switch {
	case assignmentFoldEqual(name, "KEY"):
		return assignmentNameClass{minLen: 16}
	case assignmentFoldEqual(name, "TOKEN"):
		return assignmentNameClass{minLen: 16}
	case assignmentFoldEqual(name, "PASSWORD"), assignmentFoldEqual(name, "SECRET"):
		return assignmentNameClass{minLen: 6}
	}
	// No-separator env spellings such as PGPASSWORD and AUTHSECRET. The longer
	// suffixes are checked first so SECRETS is not left in SECRET's shadow.
	switch {
	case assignmentSuffixEqual(name, "PASSPHRASE"),
		assignmentSuffixEqual(name, "CREDENTIALS"),
		assignmentSuffixEqual(name, "CREDENTIAL"),
		assignmentSuffixEqual(name, "PASSWORDS"),
		assignmentSuffixEqual(name, "PASSWORD"),
		assignmentSuffixEqual(name, "PASSWD"),
		assignmentSuffixEqual(name, "SECRETS"),
		assignmentSuffixEqual(name, "SECRET"):
		return assignmentNameClass{minLen: 6}
	case assignmentSuffixEqual(name, "APIKEY"),
		assignmentSuffixEqual(name, "TOKEN"):
		return assignmentNameClass{minLen: 16}
	}
	// Last token of a tokenized name. PWD and PASS are only trusted as their own
	// token (`DB_PASS`, `MYSQL_PWD`), never as a raw suffix.
	switch {
	case assignmentFoldEqual(tok, "PASSWORD"),
		assignmentFoldEqual(tok, "PASSWD"),
		assignmentFoldEqual(tok, "PASSPHRASE"),
		assignmentFoldEqual(tok, "SECRET"),
		assignmentFoldEqual(tok, "SECRETS"),
		assignmentFoldEqual(tok, "CREDENTIAL"),
		assignmentFoldEqual(tok, "CREDENTIALS"):
		return assignmentNameClass{minLen: 6}
	case assignmentFoldEqual(tok, "TOKEN"),
		assignmentFoldEqual(tok, "APIKEY"):
		return assignmentNameClass{minLen: 16}
	case assignmentFoldEqual(tok, "KEY") && separated:
		return assignmentNameClass{minLen: 16}
	case (assignmentFoldEqual(tok, "PASS") || assignmentFoldEqual(tok, "PWD")) && separated:
		// Only as a token of a longer name (`DB_PASS`, `MYSQL_PWD`): the bare
		// word "pass" is ordinary prose ("all tests pass: ...").
		return assignmentNameClass{minLen: 6}
	}
	return assignmentNameClass{}
}

// assignmentExcludedNames are whole names whose last token happens to be
// credential vocabulary but whose value is protocol or configuration
// vocabulary. Masking one of these rewrites a value the protocol depends on:
// `prompt_cache_key` is OpenAI Responses' routing/cache parameter, and
// rewriting it changes prompt-cache keying; token counters and sort/partition
// keys are numbers and identifiers, not secrets. The comparison is over the
// name with '_', '-' and '.' removed and lowercased, so every spelling of a
// listed name matches while a genuine `PROXY_KEY` or `OPENAI_API_KEY` does not.
var assignmentExcludedNames = [...]string{
	"promptcachekey",
	"nexttoken", "pagetoken", "continuationtoken",
	"cachekey", "idempotencykey",
	"sortkey", "partitionkey", "routingkey", "groupkey", "primarykey",
	"foreignkey", "objectkey", "storagekey", "s3key",
	"tokentype",
	"tokencount", "maxtokens", "prompttokens", "completiontokens",
	"totaltokens", "reasoningtokens",
}

// assignmentNameExcluded reports whether the whole name is a known non-secret
// name from assignmentExcludedNames.
func assignmentNameExcluded(name []byte) bool {
	var buf [assignmentMaxNameLen]byte
	n := 0
	for _, c := range name {
		if c == '_' || c == '-' || c == '.' {
			continue
		}
		if n >= len(buf) {
			return false
		}
		buf[n] = lowerASCII(c)
		n++
	}
	cand := buf[:n]
	for _, word := range assignmentExcludedNames {
		if len(cand) == len(word) && equalFoldAt(cand, word) {
			return true
		}
	}
	return false
}

// assignmentLikelyName reports whether a name can end in a vocabulary word. It
// is a necessary condition, not a decision: a passing name still goes through
// classifyAssignmentName, which may reject it (a bare MONKEY still does not
// match). The trigram set below is the tail of every accepted spelling -
// PASSWORD/ORD, PASSWD/SWD, PWD, PASS/ASS, PASSPHRASE/ASE, SECRET/RET,
// SECRETS/ETS, TOKEN/KEN, CREDENTIAL/IAL, CREDENTIALS/LLS, APIKEY/KEY, DSN,
// URI, URL, CONNECTION_STRING/ING.
func assignmentLikelyName(name []byte) bool {
	end := len(name)
	for end > 0 && (name[end-1] == '"' || name[end-1] == '\\') {
		end--
	}
	name = name[:end]
	if len(name) < 3 {
		return false
	}
	switch lowerASCII(name[len(name)-1]) {
	case 'd':
		return assignmentEnds3(name, "ord") || assignmentEnds3(name, "swd") || assignmentEnds3(name, "pwd") || assignmentEnds3(name, "rds")
	case 's':
		return assignmentEnds3(name, "ass") || assignmentEnds3(name, "ets") || assignmentEnds3(name, "lls")
	case 'e':
		return assignmentEnds3(name, "ase")
	case 't':
		return assignmentEnds3(name, "ret")
	case 'n':
		return assignmentEnds3(name, "ken") || assignmentEnds3(name, "dsn")
	case 'l':
		return assignmentEnds3(name, "ial") || assignmentEnds3(name, "url")
	case 'y':
		return assignmentEnds3(name, "key")
	case 'i':
		return assignmentEnds3(name, "uri")
	case 'g':
		return assignmentEnds3(name, "ing")
	}
	return false
}

func assignmentEnds3(name []byte, word string) bool {
	return len(name) >= 3 && equalFoldAt(name[len(name)-3:], word)
}

// trimAssignmentName removes the quote and backslash bytes that belong to the
// JSON spelling of a member name around the identifier itself. A line-break
// escape is already excluded by assignmentNameBounds, which stops the look-back
// at the escape introducer.
func trimAssignmentName(name []byte) []byte {
	start, end := 0, len(name)
	for start < end && (name[start] == '"' || name[start] == '\\') {
		start++
	}
	for end > start && (name[end-1] == '"' || name[end-1] == '\\') {
		end--
	}
	return name[start:end]
}

// assignmentLastToken returns the last identifier token of name and whether a
// separator byte preceded it. CamelCase boundaries also start a token; an
// acronym run followed by a lowercase word (`HTTPServer`) starts one at the
// first lowercase letter.
func assignmentLastToken(name []byte) ([]byte, bool) {
	start := 0
	separated := false
	for i := 0; i < len(name); i++ {
		if assignmentTokenSeparator(name[i]) {
			start = i + 1
			separated = true
			continue
		}
		if i == 0 {
			continue
		}
		prev := name[i-1]
		if isLowerLetter(prev) && isUpperLetter(name[i]) {
			start = i
			separated = false
			continue
		}
		if i > 1 && isUpperLetter(name[i-2]) && isUpperLetter(prev) && isLowerLetter(name[i]) {
			start = i - 1
			separated = false
		}
	}
	return name[start:], separated
}

func assignmentTokenSeparator(c byte) bool {
	switch c {
	case '_', '.', '-', '/', '\\', '"':
		return true
	}
	return false
}

func assignmentFoldEqual(a []byte, b string) bool {
	return len(a) == len(b) && equalFoldAt(a, b)
}

func assignmentSuffixEqual(a []byte, suffix string) bool {
	return len(a) >= len(suffix) && equalFoldAt(a[len(a)-len(suffix):], suffix)
}

func isUpperLetter(c byte) bool { return c >= 'A' && c <= 'Z' }
func isLowerLetter(c byte) bool { return c >= 'a' && c <= 'z' }
func isDigitByte(c byte) bool   { return c >= '0' && c <= '9' }

// assignmentValueSpan returns the byte range of the value that follows a
// separator at v, plus whether the value was quoted.
//
// Three spellings are recognised: a raw double-quoted JSON string, a
// double-quoted string nested inside one or more escaped JSON strings (opener =
// a run of backslashes then a quote, closer = the same run), and a single- or
// unquoted scalar. The range always excludes the quotes and the separator, so
// masking preserves them byte for byte.
func assignmentValueSpan(src []byte, v int) (int, int, bool, bool) {
	for v < len(src) && (src[v] == ' ' || src[v] == '\t') {
		v++
	}
	if v >= len(src) {
		return 0, 0, false, false
	}
	if src[v] == '\\' {
		j := v
		for j < len(src) && src[j] == '\\' && j-v < 4 {
			j++
		}
		if j > v && j < len(src) && src[j] == '"' {
			return assignmentEscapedQuotedValue(src, j+1, j-v)
		}
	}
	if src[v] == '"' {
		return assignmentQuotedValue(src, v+1)
	}
	if src[v] == '\'' {
		return assignmentSingleQuotedValue(src, v+1)
	}
	start := v
	for v < len(src) {
		if assignmentUnquotedTerminator(src[v]) {
			break
		}
		if src[v] == '\\' {
			j := v
			for j < len(src) && src[j] == '\\' {
				j++
			}
			if j < len(src) {
				switch src[j] {
				case 'n', 'r', 't', '"':
					return start, v, false, v > start
				}
			}
			v = j
			continue
		}
		v++
	}
	return start, v, false, v > start
}

// assignmentUnquotedTerminator is a byte that ends an unquoted scalar. The set
// is deliberately the structural punctuation of the document forms this
// scanner sees - prose, .env lines and JSON member values - not every byte a
// password may contain, because an over-long span is the direction that leaks.
func assignmentUnquotedTerminator(c byte) bool {
	switch c {
	case ' ', '\t', '\r', '\n', '"', '\'', ',', '}', ']', ')', ';', '&', '{':
		return true
	}
	return false
}

// assignmentQuotedValue scans a raw double-quoted string that starts at start.
// A backslash escape is skipped whole so an escaped quote does not close the
// value; an escaped line break ends it, which is the .env line inside a JSON
// string.
func assignmentQuotedValue(src []byte, start int) (int, int, bool, bool) {
	for i := start; i < len(src); i++ {
		switch src[i] {
		case '"':
			return start, i, true, i > start
		case '\\':
			if i+1 >= len(src) {
				return 0, 0, false, false
			}
			switch src[i+1] {
			case 'n', 'r', 't':
				return start, i, true, i > start
			}
			i++
		}
	}
	return 0, 0, false, false
}

// assignmentEscapedQuotedValue scans a string whose quotes are themselves
// JSON-escaped: depth backslashes, then the quote, at both ends. That is how a
// JSON document embedded in a tool result appears in the recorded body.
func assignmentEscapedQuotedValue(src []byte, start, depth int) (int, int, bool, bool) {
	for i := start; i < len(src); i++ {
		if src[i] != '\\' {
			continue
		}
		j := i
		for j < len(src) && src[j] == '\\' {
			j++
		}
		if j < len(src) {
			switch src[j] {
			case '"':
				if j-i == depth {
					return start, i, true, i > start
				}
				i = j
			case 'n', 'r', 't':
				return start, i, true, i > start
			default:
				i = j
			}
		} else {
			i = j
		}
	}
	return 0, 0, false, false
}

// assignmentSingleQuotedValue scans a raw single-quoted scalar.
func assignmentSingleQuotedValue(src []byte, start int) (int, int, bool, bool) {
	for i := start; i < len(src); i++ {
		if src[i] == '\'' {
			return start, i, true, i > start
		}
		if src[i] == '\n' || src[i] == '\r' {
			break
		}
	}
	return 0, 0, false, false
}

// assignmentValueIsSkipped reports whether a value must not be masked: empty,
// a variable/template reference, a JSON literal, or a placeholder.
func assignmentValueIsSkipped(value []byte, quoted bool) bool {
	if len(value) == 0 {
		return true
	}
	switch value[0] {
	case '$', '%', '<', '{':
		// $VAR / ${VAR} / %VAR% / <placeholder> / {{ template }} / a JSON object.
		return true
	}
	if quoted && (value[0] == ' ' || value[0] == '\t') {
		// A quoted value starts at its first content byte, so a leading space
		// means the "value" is the remainder of a shell line whose closing quote
		// was read as an opener. A real credential does not start with padding.
		return true
	}
	if !quoted && assignmentValueIsJSONLiteral(value) {
		return true
	}
	return assignmentValueIsPlaceholder(value)
}

// assignmentValueIsJSONLiteral reports whether an unquoted value is a JSON
// scalar: a number, true, false or null. `MAX_TOKEN=4096` is a limit, not a
// token, and an unquoted numeric value is never a credential in practice.
func assignmentValueIsJSONLiteral(value []byte) bool {
	switch {
	case assignmentFoldEqual(value, "true"),
		assignmentFoldEqual(value, "false"),
		assignmentFoldEqual(value, "null"):
		return true
	}
	i := 0
	if i < len(value) && (value[i] == '-' || value[i] == '+') {
		i++
	}
	digits := 0
	for i < len(value) && isDigitByte(value[i]) {
		i++
		digits++
	}
	if digits == 0 {
		return false
	}
	if i < len(value) && value[i] == '.' {
		i++
		for i < len(value) && isDigitByte(value[i]) {
			i++
		}
	}
	if i < len(value) && (value[i] == 'e' || value[i] == 'E') {
		i++
		if i < len(value) && (value[i] == '-' || value[i] == '+') {
			i++
		}
		for i < len(value) && isDigitByte(value[i]) {
			i++
		}
	}
	return i == len(value)
}

// assignmentPlaceholderWords are values that spell "put the real value here"
// rather than a value. The list is intentionally broad: a placeholder is the
// one kind of value where masking is pure noise in the audit table.
var assignmentPlaceholderWords = []string{
	"password", "passwd", "secret", "token", "key",
	"dummy", "example", "sample", "test",
	// JSON-schema type names sit right after a password/token member name in a
	// tool's function schema (`"password": "string"`).
	"string", "number", "integer", "boolean", "object", "array",
	"null", "nil", "none", "true", "false", "undefined",
	"redacted", "masked", "changeme", "change_me", "change-me",
	"replace_me", "replaceme",
}

// assignmentValueIsPlaceholder reports whether a value is template text.
//
// The single-word list is only worth consulting for a value short enough to be
// one of its entries, and the character scan returns as soon as it sees a
// normal alphanumeric: a real value ("sk-...", a database password) therefore
// rejects in one or two bytes instead of walking the whole value. That matters
// on a body that carries a labeled credential in every message.
func assignmentValueIsPlaceholder(value []byte) bool {
	if len(value) <= assignmentMaxPlaceholderWordLen {
		for _, w := range assignmentPlaceholderWords {
			if assignmentFoldEqual(value, w) {
				return true
			}
		}
	}
	if len(value) > 0 {
		switch lowerASCII(value[0]) {
		case 'y', 'r':
			for _, p := range [...]string{"your_", "your-", "replace"} {
				if len(value) >= len(p) && equalFoldAt(value, p) {
					return true
				}
			}
		}
	}
	// A value built only from placeholder characters: "***", "...", "xxxx",
	// "xxxx-xxxx", "****-****". It carries no information either way.
	alnum := false
	marker := false
	onlyPlaceholder := true
	for _, c := range value {
		if isAlnumByte(c) {
			alnum = true
			if c != 'x' && c != 'X' {
				return false
			}
			marker = true
			continue
		}
		switch c {
		case '*', '.', '-', '_', '#', '?', ' ':
			marker = true
		default:
			onlyPlaceholder = false
		}
	}
	if !alnum {
		return true
	}
	return marker && onlyPlaceholder
}

// assignmentURLPassword returns the byte range of the userinfo password in a
// URL value: scheme://user:password@host. Only the password is in scope; the
// host and the database name are what an operator reads a DSN for.
func assignmentURLPassword(src []byte, start, end int) (int, int, bool) {
	i := start
	if i >= end || !isAlphaLetter(src[i]) {
		return 0, 0, false
	}
	i++
	for i < end && src[i] != ':' {
		c := src[i]
		if isAlnumByte(c) || c == '+' || c == '-' || c == '.' {
			i++
			continue
		}
		return 0, 0, false
	}
	if i+2 >= end || src[i] != ':' || src[i+1] != '/' || src[i+2] != '/' {
		return 0, 0, false
	}
	authorityStart := i + 3
	authorityEnd := end
	for j := authorityStart; j < end; j++ {
		if src[j] == '/' || src[j] == '?' || src[j] == '#' {
			authorityEnd = j
			break
		}
	}
	at := -1
	for j := authorityStart; j < authorityEnd; j++ {
		if src[j] == '@' {
			at = j
		}
	}
	if at < 0 {
		return 0, 0, false
	}
	colon := -1
	for j := authorityStart; j < at; j++ {
		if src[j] == ':' {
			colon = j
		}
	}
	if colon < 0 {
		return 0, 0, false
	}
	return colon + 1, at, at > colon+1
}

func isAlphaLetter(c byte) bool { return isUpperLetter(c) || isLowerLetter(c) }

// newAssignmentFinding scores a name-anchored finding. A name-anchored
// assignment is StrengthStrong: the label itself is the evidence, so it needs
// no surrounding context and no validator.
func newAssignmentFinding(id RuleID, start, end int) Finding {
	score, why := scoreFinding(&ruleTable[id], false, false)
	return Finding{
		Span:       Span{Start: start, End: end, ID: id},
		Score:      score,
		Confidence: ConfidenceFor(score),
		Reasons:    why,
	}
}

// mergeFindings folds the name-anchored findings into the DFA findings so that
// the result stays ordered by start and no two spans overlap.
//
// Precedence:
//
//   - A name-anchored span that strictly contains one or more DFA findings
//     replaces them. The name says the whole value is one secret, and the DFA
//     may have matched only the shape-looking part of it.
//   - An exactly equal span resolves by actionability, then by specificity: if
//     the DFA finding is already maskable it wins (a vendor rule names the
//     value better in the audit), but a reported-only DFA finding gives way to
//     a maskable name-anchored one. That is the `KEY="sk-..."` case: the weak
//     generic shape alone cannot be rewritten, the name is what proves it.
//   - Any other overlap (the candidate is contained, or crosses an edge) keeps
//     the DFA finding and drops the name-anchored one: the DFA is the more
//     specific evidence and must never be widened by a name.
func mergeFindings(dfa, extra []Finding) []Finding {
	if len(extra) == 0 {
		return dfa
	}
	out := make([]Finding, 0, len(dfa)+len(extra))
	i := 0
	for _, cand := range extra {
		for i < len(dfa) && dfa[i].End <= cand.Start {
			out = append(out, dfa[i])
			i++
		}
		j := i
		contained := 0
		partial := false
		for j < len(dfa) && dfa[j].Start < cand.End {
			if cand.Start <= dfa[j].Start && dfa[j].End <= cand.End &&
				!(cand.Start == dfa[j].Start && cand.End == dfa[j].End) {
				contained++
			} else {
				partial = true
			}
			j++
		}
		if j == i {
			out = append(out, cand)
			continue
		}
		if contained > 0 && !partial {
			out = append(out, cand)
			i = j
			continue
		}
		if j == i+1 && dfa[i].Start == cand.Start && dfa[i].End == cand.End &&
			!dfa[i].Maskable() && cand.Maskable() {
			// Equal span, but only the name makes the value actionable.
			out = append(out, cand)
			i = j
			continue
		}
		// A partial overlap (or only an equal span) keeps the DFA finding; the
		// candidate is dropped. Append the DFA findings it overlapped so the
		// two lists keep their order.
		for ; i < j; i++ {
			out = append(out, dfa[i])
		}
	}
	out = append(out, dfa[i:]...)
	return out
}
