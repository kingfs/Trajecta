package secretmask

// Name-anchored assignment detection and DSN userinfo-password detection.
//
// The DFA in rules.rl matches a credential by its lexical shape: a vendor
// prefix, a checksum, an exact length. Two classes of secret have no shape of
// their own:
//
//   - The env-style assignment: `POSTGRES_PASSWORD=...`, `OPENAI_API_KEY=...`,
//     a docker-compose `..._password: ...`, a JSON member
//     `\"OPENAI_API_KEY\": \"...\"`. The anchor is the variable name, and the
//     name has to be spelled the way an environment variable is: UPPERCASE and
//     ending in `_KEY` or `_PASSWORD`. The earlier, broader rule - any name
//     whose last camelCase or separated token was KEY/TOKEN/SECRET/PASSWORD -
//     also fired on source code, whose `=` and `:` separators are everywhere:
//     `clientSecret: string(target.ClientSecret)` masked the value expression,
//     and `bodyKey: strings.TrimSpace(key)` masked Go code. A code fragment is
//     not a secret, and rewriting one is worse than missing a shapeless value,
//     so the accepted names are now only the env-variable spelling.
//   - The DSN: `postgres://user:password@host/db`. Its only secret part is the
//     userinfo password, and the scheme itself is enough evidence that the
//     middle field is a password. It is therefore found without a variable
//     name, by dsn.go, so a DSN pasted into prose or a tool result is masked
//     while its user, host and database stay readable.
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

	// assignmentKeyMinLen and assignmentPasswordMinLen are the value floors for
	// the two accepted name classes. A key is longer in practice than a password,
	// and the floor is what keeps `API_KEY=none` or `PASSWORD=abc` out.
	assignmentKeyMinLen      = 16
	assignmentPasswordMinLen = 6
)

// assignmentNameClass is what the name before the separator says about the
// value. A URL-class name masks only the userinfo password; every other class
// masks the whole value. minLen is the value floor for the class.
type assignmentNameClass struct {
	urlClass bool
	minLen   int
}

func (c assignmentNameClass) ok() bool { return c.minLen > 0 }

// appendNativeFindings appends every name-anchored assignment and DSN userinfo
// password in src to dst. dfa is the DFA finding set for the same buffer, sorted
// by start; a candidate whose span is already a maskable DFA finding exactly is
// skipped here rather than in mergeFindings, because that is the common case on
// a body that labels every credential, and dropping it early keeps the slice out
// of the allocation path.
//
// The walk advances by separator, not by byte: bytes.IndexByte is the vectorized
// byte search in the standard library, and keeping the next position of each of
// the two trigger bytes means every byte of the body is scanned at most twice no
// matter how dense the separators are. That matters because this runs on every
// proxied request body; a naive byte-at-a-time loop measured ~2 ns/byte and
// dominated the added cost.
func appendNativeFindings(dst []Finding, src []byte, dfa []Finding) []Finding {
	if len(src) < assignmentMinBuffer {
		return dst
	}
	nextEq := nextSeparator(src, 0, '=')
	nextCol := nextSeparator(src, 0, ':')
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
		// A DSN is found by its scheme rather than by a variable name, so it is
		// checked before the assignment path: the ':' in `postgres://` is a
		// separator, and a password that follows it is a secret even in prose.
		if src[at] == ':' && dsnLeadIn(src, at) {
			if f, end, ok := dsnAt(src, at); ok {
				if !assignmentAlreadyMasked(dfa, f) {
					dst = append(dst, f)
				}
				// The authority was parsed as a unit; a separator inside its
				// userinfo or host must not produce a second, overlapping finding.
				nextEq, nextCol = nextSeparator(src, end, '='), nextSeparator(src, end, ':')
				continue
			}
		}
		if !assignmentDfaCoversValueStart(dfa, src, at) {
			if f, valueEnd, ok := assignmentAt(src, at); ok {
				if !assignmentAlreadyMasked(dfa, f) {
					dst = append(dst, f)
				}
				// Skip the value: a separator inside it must not produce a second,
				// overlapping finding for bytes the first span already covers.
				nextEq, nextCol = nextSeparator(src, valueEnd, '='), nextSeparator(src, valueEnd, ':')
				continue
			}
		}
		from := at + 1
		if at == nextEq {
			nextEq = nextSeparator(src, from, '=')
		} else {
			nextCol = nextSeparator(src, from, ':')
		}
	}
}

// nextSeparator returns the index of the first b at or after from, or -1.
func nextSeparator(src []byte, from int, b byte) int {
	if from < 0 || from >= len(src) {
		return -1
	}
	if i := bytes.IndexByte(src[from:], b); i >= 0 {
		return from + i
	}
	return -1
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
	// name if it ends with one of the accepted uppercase suffixes, so a body full
	// of JSON members ("content", "model", "temperature") is rejected on a few
	// bytes without tokenizing or allocating.
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
	if assignmentValueLooksLikeDottedName(value) {
		// `LANGUAGE_KEY = "trajecta.monitor.language"`, `CACHE_KEY = users.byId`:
		// a dotted name is configuration vocabulary, not a credential. Source
		// code is full of upper-case `*_KEY` constants whose values are exactly
		// this shape, and masking one would rewrite a tool result that carried
		// code.
		return Finding{}, valueEnd, false
	}
	if !quoted && assignmentValueLooksLikeExpression(value) {
		// `API_KEY = os.Getenv(...)`, `PASSWORD = config["x"]`, `KEY = m{}`: the
		// value is a code expression, not a literal. A source fragment that
		// happens to name a credential must not be rewritten.
		return Finding{}, valueEnd, false
	}
	if len(value) < class.minLen || assignmentValueIsSkipped(value, quoted) {
		return Finding{}, valueEnd, false
	}
	if class.urlClass {
		pwStart, pwEnd, ok := assignmentURLPassword(src, valueStart, valueEnd)
		if !ok {
			return Finding{}, valueEnd, false
		}
		password := src[pwStart:pwEnd]
		if len(password) < assignmentURLPasswordMin || assignmentPasswordValueIsSkipped(password) {
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
// separators the tokenizer splits on. It is a loose bound: the name still has
// to be accepted by classifyAssignmentName, which keeps only the uppercase env
// spelling.
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
// Only the env-variable spelling counts: the name must be UPPERCASE (letters,
// digits and '_'), and it must carry a non-empty prefix before `_KEY`,
// `_PASSWORD`, or one of the URL-class suffixes (`_URL`, `_URI`, `_DSN`,
// `_CONNECTION_STRING`). Anything else - `clientSecret`, `dbPassword`, bare
// `PASSWORD`, `PGPASSWORD`, `GITHUB_TOKEN`, `CLIENT_SECRET` - is deliberately
// left alone. The narrower vocabulary is the fix for the reported false
// positives: ordinary code spells `clientSecret:`, `token:`, `password:`, and
// treating those as env assignments masked the value expression next to them.
func classifyAssignmentName(raw []byte) assignmentNameClass {
	name := trimAssignmentName(raw)
	if !assignmentEnvName(name) {
		return assignmentNameClass{}
	}
	if assignmentNameExcluded(name) {
		return assignmentNameClass{}
	}
	// URL-class names describe a whole connection string. The class is decided
	// before the secret names so a name that ends in both is not mistaken for a
	// whole-value secret; in practice the suffixes do not overlap.
	if assignmentSuffixEqual(name, "_CONNECTION_STRING") ||
		assignmentSuffixEqual(name, "_URL") ||
		assignmentSuffixEqual(name, "_URI") ||
		assignmentSuffixEqual(name, "_DSN") {
		return assignmentNameClass{urlClass: true, minLen: assignmentURLPasswordMin}
	}
	switch {
	case assignmentSuffixEqual(name, "_PASSWORD"):
		return assignmentNameClass{minLen: assignmentPasswordMinLen}
	case assignmentSuffixEqual(name, "_KEY"):
		return assignmentNameClass{minLen: assignmentKeyMinLen}
	}
	return assignmentNameClass{}
}

// assignmentExcludedNames are whole names whose last token happens to be
// credential vocabulary but whose value is protocol or configuration
// vocabulary. Masking one of these rewrites a value the protocol depends on:
// `PROMPT_CACHE_KEY` is OpenAI Responses' routing/cache parameter, and
// rewriting it changes prompt-cache keying; token counters and sort/partition
// keys are numbers and identifiers, not secrets. The comparison is over the
// name with '_', '-' and '.' removed and lowercased, so every spelling of a
// listed name matches while a genuine `PROXY_KEY` or `OPENAI_API_KEY` does not.
//
// The list matters less than it used to - the accepted names are uppercase
// `_KEY`/`_PASSWORD` only, so `next_token` cannot match - but the uppercase
// spellings of the same protocol names can, and they are exactly the ones this
// list exists for.
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

// assignmentEnvName reports whether name is the uppercase env spelling:
// letters, digits and '_', starting with an uppercase letter or '_'.
func assignmentEnvName(name []byte) bool {
	if len(name) == 0 {
		return false
	}
	first := name[0]
	if !isUpperLetter(first) && first != '_' {
		return false
	}
	for _, c := range name {
		switch {
		case isUpperLetter(c), isDigitByte(c), c == '_':
		default:
			return false
		}
	}
	// A name that is only underscores and digits cannot carry the suffix, but
	// the suffix check below is the real gate; this condition only keeps the
	// function usable as a segment test for the member-reference guard.
	return true
}

// assignmentLikelyName reports whether a name can end in one of the accepted
// suffixes. It is a cheap necessary condition, not the decision: the caller
// still runs classifyAssignmentName, which checks the uppercase spelling and
// the exclusions.
func assignmentLikelyName(name []byte) bool {
	end := len(name)
	for end > 0 && (name[end-1] == '"' || name[end-1] == '\\') {
		end--
	}
	name = name[:end]
	if len(name) < len("_KEY")+1 {
		return false
	}
	for _, suffix := range [...]string{"_PASSWORD", "_CONNECTION_STRING", "_KEY", "_URL", "_URI", "_DSN"} {
		if len(name) > len(suffix) && string(name[len(name)-len(suffix):]) == suffix {
			return true
		}
	}
	return false
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

// assignmentSuffixEqual reports whether a ends with suffix, comparing ASCII
// case-insensitively. The accepted names are uppercase, so callers pass the
// uppercase suffix; the fold only keeps a lowercase escaped spelling usable.
func assignmentSuffixEqual(a []byte, suffix string) bool {
	return len(a) >= len(suffix) && equalFoldAt(a[len(a)-len(suffix):], suffix)
}

func assignmentFoldEqual(a []byte, b string) bool {
	return len(a) == len(b) && equalFoldAt(a, b)
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

// assignmentValueLooksLikeExpression reports whether an unquoted value is a
// code expression rather than a literal. `.env` values and JSON strings do not
// carry parentheses, brackets or braces; a source fragment such as
// `PASSWORD = os.Getenv(...)` or `API_KEY = config["key"]` does, and masking it
// would rewrite code that was never a credential.
func assignmentValueLooksLikeExpression(value []byte) bool {
	for _, c := range value {
		switch c {
		case '(', '[', '{', '<':
			return true
		}
	}
	return false
}

// assignmentValueLooksLikeDottedName reports whether a quoted or unquoted value
// is a dotted name: two or more dot-separated ASCII identifiers whose first
// segment starts with a lowercase letter or '_'. That is how code spells a
// configuration path (`trajecta.monitor.language`), a namespace, a package
// member (`process.env.OPENAI_API_KEY`, `config.API_KEY`) or a hostname
// (`db.example.com`), and it is also the shape of an upper-case `*_KEY`
// constant's value in a frontend source file:
//
//	export const LANGUAGE_KEY = "trajecta.monitor.language";
//
// Such a source fragment can reach the proxy inside a tool result, and masking
// it would rewrite code rather than a credential. A dotted password whose first
// segment is an upper-case word or contains a digit (`Correct.Horse.2024`,
// `hunter2.pass`) stays a candidate; a value built only of lowercase words and
// dots is missed, which is the accepted side of the false-positive trade.
func assignmentValueLooksLikeDottedName(value []byte) bool {
	segments := bytes.Count(value, []byte{'.'}) + 1
	if segments < 2 {
		return false
	}
	start := 0
	for i := 0; i <= len(value); i++ {
		if i == len(value) || value[i] == '.' {
			if i == start {
				return false
			}
			seg := value[start:i]
			if start == 0 {
				if !assignmentLowerIdentifier(seg) {
					return false
				}
			} else if !assignmentAnyIdentifier(seg) {
				return false
			}
			start = i + 1
		}
	}
	return true
}

// assignmentAnyIdentifier reports whether b is an ASCII identifier: a letter or
// '_' followed by letters, digits or '_'.
func assignmentAnyIdentifier(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	if !isAlphaLetter(b[0]) && b[0] != '_' {
		return false
	}
	for _, c := range b {
		if !isAlphaLetter(c) && !isDigitByte(c) && c != '_' {
			return false
		}
	}
	return true
}

// assignmentLowerIdentifier reports whether b is an ASCII identifier beginning
// with a lowercase letter or '_': a variable, package, field or receiver name.
func assignmentLowerIdentifier(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	if !isLowerLetter(b[0]) && b[0] != '_' {
		return false
	}
	for _, c := range b {
		if !isLowerLetter(c) && !isDigitByte(c) && c != '_' {
			return false
		}
	}
	return true
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

// assignmentValueIsSkipped reports whether a whole-value assignment must not be
// masked: empty, a variable/template reference, a JSON literal, a code
// expression or a placeholder.
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

// assignmentPasswordValueIsSkipped reports whether a password sub-span (the
// userinfo password of a URL, or the password of a DSN) must be left alone.
// Unlike a whole-value assignment, a numeric password can be real, so only
// references and placeholders are skipped here.
func assignmentPasswordValueIsSkipped(value []byte) bool {
	if len(value) == 0 {
		return true
	}
	switch value[0] {
	case '$', '%', '<', '{':
		return true
	}
	return assignmentValueIsPlaceholder(value)
}

// assignmentValueIsJSONLiteral reports whether an unquoted value is a JSON
// scalar: a number, true, false or null. `MAX_TOKENS=4096` is a limit, not a
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
	"password", "passwd", "pass", "secret", "token", "key",
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
	// Split the userinfo at the FIRST ':' so a password that itself contains a
	// colon is masked whole: net/url (and RFC 3986's user ":" password form)
	// treats everything after the first colon as the password. Taking the last
	// colon would mask only the tail of `user:pass:word` and leave `pass:` in
	// the clear on the wire.
	colon := bytes.IndexByte(src[authorityStart:at], ':')
	if colon < 0 {
		return 0, 0, false
	}
	colon += authorityStart
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

// mergeFindings folds the native findings into the DFA findings so that the
// result stays ordered by start and no two spans overlap.
//
// Precedence:
//
//   - A native span that strictly contains one or more DFA findings replaces
//     them. The name says the whole value is one secret, and the DFA may have
//     matched only the shape-looking part of it.
//   - An exactly equal span resolves by actionability, then by specificity: if
//     the DFA finding is already maskable it wins (a vendor rule names the value
//     better in the audit), but a reported-only DFA finding gives way to a
//     maskable native one. That is the `KEY="sk-..."` case: the weak generic
//     shape alone cannot be rewritten, the name is what proves it.
//   - Any other overlap (the candidate is contained, or crosses an edge) keeps
//     the DFA finding and drops the native one: the DFA is the more specific
//     evidence and must never be widened by a name.
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
