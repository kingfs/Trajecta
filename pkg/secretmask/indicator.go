package secretmask

import (
	"bytes"
	"strings"
)

// The access-indicator half of the package: local filesystem paths and URLs.
//
// These rules are ReportOnly. Nothing here is rewritten, restored or blocked:
// the audit wants the exact bytes the agent read or produced, and a stand-in
// would erase the fact being audited. Detection runs through its own Ragel
// machine (indicator.rl) rather than through the credential scanner, because a
// Ragel scanner consumes what it matched: a URL rule in the credential machine
// would swallow every credential inside the URL (a presigned AWS URL carries
// AKIA... in its query string) and the masker would never see it.
//
// The machine finds a maximal run of URL/path bytes; the candidate checks below
// are the fine half, the same coarse/fine split that digit_run uses. They decide
// whether that run is a real URL or a local path rather than an API route, a
// MIME type or a bare domain.

const (
	// candidateURLShape is the CandidateCheck name used by RuleURL.
	candidateURLShape = "url_shape"
	// candidateLocalPathShape is the CandidateCheck name used by RuleLocalPath.
	candidateLocalPathShape = "local_path_shape"

	// accessTrimRight is the sentence and markdown punctuation stripped from
	// the end of a report-only match. '(' and ')' are legal URL path bytes, so
	// the machine cannot exclude them; the closing ')' of "[x](https://y)" does
	// not belong to the URL.
	accessTrimRight = ".,;:!?)]}'*"
)

// indicatorCollector receives lexical candidates from the generated indicator
// DFA. Unlike collector it scores nothing: an access indicator is reported at
// info level, and relevance is decided by the caller's audit, not by credential
// evidence.
type indicatorCollector struct {
	src      []byte
	findings []Finding
}

// emit is called from the generated indicator machine for every completed
// match. Trimming happens before the length and boundary checks so a sentence
// period neither lengthens the value nor makes the span touch the next word.
func (c *indicatorCollector) emit(id RuleID, start, end int) {
	if id <= RuleUnknown || id >= ruleIDCount || start < 0 || start >= end || end > len(c.src) {
		return
	}
	r := &ruleTable[id]
	// The indicator machine only knows report-only rules. The guard refuses a
	// credential rule if one is ever added to this machine by mistake: Mask
	// never scans with this machine, and a finding produced here would be
	// refused by Finding.Maskable regardless.
	if !r.ReportOnly {
		return
	}
	// A JSON pointer or a URL fragment (#/definitions/Foo, #/components/...)
	// is a document path, not a filesystem one. The '#' directly before the
	// match is the evidence, and the candidate check cannot see it because the
	// machine matched only the '/...' part.
	if id == RuleLocalPath && start > 0 && c.src[start-1] == '#' {
		return
	}
	if r.TrimRight != "" {
		end = trimIndicatorEnd(c.src, start, end, r.TrimRight)
	}
	n := end - start
	if n < r.MinLen || (r.MaxLen > 0 && n > r.MaxLen) {
		return
	}
	if r.WordBounded && !bounded(c.src, start, end) {
		return
	}
	if r.CandidateCheck != "" && !runCandidateCheck(r.CandidateCheck, c.src[start:end]) {
		return
	}
	c.findings = append(c.findings, Finding{Span: Span{Start: start, End: end, ID: id}})
}

// AppendIndicators appends the report-only access indicators (local paths and
// URLs) for src to dst and returns the extended slice. Reusing dst avoids
// allocating on the hot path.
//
// Every returned Finding has a ReportOnly rule and is not Maskable. Callers
// that want one event per distinct value - the audit path - deduplicate on the
// bytes from Span.Bytes themselves; this layer reports every occurrence so a
// caller can also count them.
func (Detector) AppendIndicators(dst []Finding, src []byte) []Finding {
	if len(src) == 0 {
		return dst
	}
	c := indicatorCollector{src: src, findings: dst}
	scanIndicatorDFA(src, &c)
	return c.findings
}

// Indicators returns the report-only access indicators for src.
func (d Detector) Indicators(src []byte) []Finding {
	return d.AppendIndicators(nil, src)
}

// Indicators returns the report-only access indicators for src using the
// package's compiled indicator DFA. It is shorthand for the zero Detector.
func Indicators(src []byte) []Finding { return Detector{}.Indicators(src) }

// trimIndicatorEnd strips the punctuation that ends a sentence or a markdown
// wrapper from a report-only match. A closing bracket is only stripped when it
// is unbalanced inside the match: the ")" that closes "[docs](https://x)" is
// not part of the URL, while the ")" of ".../wiki/Foo_(bar)" is.
func trimIndicatorEnd(src []byte, start, end int, cutset string) int {
	for end > start && strings.IndexByte(cutset, src[end-1]) >= 0 {
		switch closer := src[end-1]; closer {
		case ')', ']', '}':
			if bracketBalanced(src[start:end], closer) {
				return end
			}
		}
		end--
	}
	return end
}

// bracketBalanced reports whether tok contains at least as many copies of the
// matching opening bracket as of closer. It is a cheap count, not a parse, but
// it is enough to tell a URL's own parenthesis from the one that closes the
// markdown link around it.
func bracketBalanced(tok []byte, closer byte) bool {
	opener := byte(0)
	switch closer {
	case ')':
		opener = '('
	case ']':
		opener = '['
	case '}':
		opener = '{'
	default:
		return true
	}
	opens, closes := 0, 0
	for _, c := range tok {
		switch c {
		case opener:
			opens++
		case closer:
			closes++
		}
	}
	return opens >= closes
}

// validURLShape reports whether tok is a URL with a real authority. The machine
// already pinned the scheme and the byte set; what it cannot express is that
// "http://" followed by punctuation has no host.
func validURLShape(tok []byte) bool {
	sep := bytes.Index(tok, []byte("://"))
	if sep <= 0 {
		return false
	}
	switch string(tok[:sep]) {
	case "http", "https", "ws", "wss":
	default:
		return false
	}
	rest := tok[sep+3:]
	if len(rest) == 0 {
		return false
	}
	authority := rest
	if i := bytes.IndexAny(authority, "/?#"); i >= 0 {
		authority = authority[:i]
	}
	// Userinfo is everything up to the last '@' in the authority; the host is
	// what follows. A '@' later in the path is not userinfo, which is why the
	// machine keeps userinfo from crossing a '/'.
	if at := bytes.LastIndexByte(authority, '@'); at >= 0 {
		authority = authority[at+1:]
	}
	if len(authority) == 0 {
		return false
	}
	if authority[0] == '[' {
		close := bytes.IndexByte(authority, ']')
		if close <= 1 {
			return false
		}
		for _, c := range authority[1:close] {
			if c != ':' && c != '.' && !isHexByte(c) {
				return false
			}
		}
		return true
	}
	host := authority
	if colon := bytes.IndexByte(host, ':'); colon >= 0 {
		host = host[:colon]
	}
	if len(host) == 0 {
		return false
	}
	if host[0] == '.' || host[0] == '-' || host[len(host)-1] == '.' || host[len(host)-1] == '-' {
		return false
	}
	for _, c := range host {
		if isAlnumByte(c) {
			return true
		}
	}
	return false
}

// validLocalPathShape separates a local path from the other slash-shaped text a
// model transcript carries. The machine has already matched one of the path
// forms in indicator.rl; this decides whether that form is local:
//
//   - ~/..., ./... and ../... are local only when a real relative path follows
//     the root: the tail needs a file extension or at least two segments, so
//     ".", "./", "../", "../a" and "./$" are not file reads.
//   - a drive-rooted path needs real segments after the drive. A segment that
//     is a JSON escape ("n", "n244", "u4e2d") or carries a printf placeholder
//     ("%s") is escape debris, not a directory: q:\n%s is a print statement.
//   - a backslash-rooted path is a UNC path, and only in one consistent form:
//     the decoded form has one leading pair and single separators, the escaped
//     form has two leading pairs and doubled separators. A token mixing the
//     two is the remains of escaped text (\\n\\tid, \\d+)\\.(\\d+), \\n\\n),
//     never a real share.
//   - /a/b is local unless its first segment is a transport route prefix: an
//     absolute path with two segments is far more likely to be /v1/chat than
//     /project/src, and /v1/chat is not a directory an agent read.
//   - a lone /name is local only for a well-known root directory or a dotfile,
//     because /anything has no evidence of being a path.
//   - a relative a/b needs a file extension on its last segment (src/main.go),
//     a leading dot-directory (.git/config), or a non-numeric first segment;
//     that is what rejects net/http, and/or and 2024/01 while keeping the file
//     reads an agent actually makes. A relative a/b with a '+'-bearing
//     "extension" is a MIME subtype (application/vnd.api+json), not a file.
func validLocalPathShape(tok []byte) bool {
	if len(tok) == 0 {
		return false
	}
	if tok[0] == '\\' {
		return validBackslashRootedPath(tok)
	}
	if tok[0] == '~' {
		return len(tok) > 1 && tok[1] == '/' && validRelativeTail(tok[2:])
	}
	if len(tok) >= 3 && tok[0] == '.' && tok[1] == '.' && tok[2] == '/' {
		return validRelativeTail(tok[3:])
	}
	if tok[0] == '.' && len(tok) >= 2 && tok[1] == '/' {
		return validRelativeTail(tok[2:])
	}
	if isASCIIAlpha(tok[0]) && len(tok) >= 3 && tok[1] == ':' && (tok[2] == '\\' || tok[2] == '/') {
		return validDrivePath(tok)
	}
	if len(tok) >= 2 && tok[0] == '/' && tok[1] == '/' {
		return validForwardRootedPath(tok)
	}
	if tok[0] == '/' {
		segments := splitPathSegments(tok[1:], false)
		switch len(segments) {
		case 0:
			return false
		case 1:
			if localPathRoots[string(segments[0])] {
				return true
			}
			return len(segments[0]) > 1 && segments[0][0] == '.'
		default:
			return !isRoutePrefix(segments[0]) && plausibleSegments(segments)
		}
	}
	segments := splitPathSegments(tok, false)
	if len(segments) < 2 {
		return false
	}
	first := segments[0]
	if len(first) == 0 || allDigits(first) {
		return false
	}
	// A dot after the first byte is a domain name (example.com/path), not a
	// directory. A leading dot is a dot-directory (.git/config).
	if bytes.IndexByte(first[1:], '.') >= 0 {
		return false
	}
	if len(first) > 1 && first[0] == '.' {
		return true
	}
	return hasFileExtension(segments[len(segments)-1]) && plausibleSegments(segments)
}

// validRelativeTail decides whether what follows "~/", "./" or "../" is a real
// relative path. A bare root ("../") has no tail, and a single segment with no
// file extension ("../a", "./$") is not evidence that anything was read; a
// directory needs at least two segments, the way /project/src does.
func validRelativeTail(rest []byte) bool {
	segments := splitPathSegments(rest, false)
	if len(segments) == 0 || !plausibleSegments(segments) {
		return false
	}
	if hasFileExtension(segments[len(segments)-1]) {
		return true
	}
	return len(segments) >= 2
}

// validDrivePath checks the segments after a Windows drive. The drive itself
// proves little - a one-letter identifier followed by a colon and a backslash is
// also a print statement or a JSON escape - so the segments must look like
// names a filesystem could hold.
func validDrivePath(tok []byte) bool {
	segments := splitPathSegments(tok[3:], true)
	if len(segments) == 0 || !plausibleSegments(segments) {
		return false
	}
	// z:\n244 is an escape sequence plus a line number, not a directory. The
	// check is on the first segment only: a later name may legitimately be "n".
	return !isEscapeSegment(segments[0])
}

// validBackslashRootedPath accepts a UNC path in exactly one of the two forms a
// transcript carries: the decoded form (\\server\share, one leading pair,
// single separators) and the escaped form that appears inside JSON text
// (\\\\server\\share, two leading pairs, doubled separators). The mixed form
// (two leading pairs, doubled separators) is what escaped prose looks like
// once the surrounding JSON was decoded - it is not a path.
func validBackslashRootedPath(tok []byte) bool {
	leading := 0
	for leading < len(tok) && tok[leading] == '\\' {
		leading++
	}
	rest := tok[leading:]
	switch leading {
	case 2:
		if bytes.Contains(rest, []byte(`\\`)) {
			return false
		}
		return validUNCSegments(splitPathSegments(rest, true))
	case 4:
		segments, ok := splitEscapedBackslashPath(rest)
		if !ok {
			return false
		}
		return validUNCSegments(segments)
	default:
		// 1 or 3 leading backslashes are the middle of an escape sequence, and
		// a longer run is not a share root this machine should guess at.
		return false
	}
}

// validForwardRootedPath handles //server/share. The machine also matches it as
// a doubled-root POSIX path, which is local either way; the same consistency
// and segment checks apply.
func validForwardRootedPath(tok []byte) bool {
	rest := tok[2:]
	if bytes.Contains(rest, []byte("//")) {
		return false
	}
	segments := splitPathSegments(rest, false)
	return validUNCSegments(segments)
}

// validUNCSegments requires a host and a share, and rejects a host that is an
// escape sequence rather than a name: \\\n\\tid after normalization reads "\n\tid",
// which is a tab-indented line of code, not a network share.
func validUNCSegments(segments [][]byte) bool {
	if len(segments) < 2 || !plausibleSegments(segments) {
		return false
	}
	return !isEscapeSegment(segments[0])
}

// splitEscapedBackslashPath splits the escaped form of a UNC path on doubled
// separators. It reports false when a separator is not doubled, which is the
// other half of the mixed-form rejection.
func splitEscapedBackslashPath(rest []byte) ([][]byte, bool) {
	var segments [][]byte
	for i := 0; ; {
		start := i
		for i < len(rest) && rest[i] != '\\' {
			i++
		}
		if i > start {
			segments = append(segments, rest[start:i])
		}
		if i >= len(rest) {
			return segments, true
		}
		if i+1 >= len(rest) || rest[i+1] != '\\' {
			return nil, false
		}
		i += 2
	}
}

// isEscapeSegment reports whether a segment is a JSON or Go escape sequence
// rather than a file or directory name: "n"/"t"/"r" plus a line number (\n244),
// a \uXXXX rune (u4e2d), or a \xXX byte (x41). Names such as "node_modules" and
// "tmp" are not escape segments.
func isEscapeSegment(seg []byte) bool {
	if len(seg) == 0 {
		return false
	}
	switch seg[0] {
	case 'n', 'r', 't', 'b', 'f', 'v', '0':
		return len(seg) == 1 || allDigits(seg[1:])
	case 'u':
		return len(seg) == 5 && allHex(seg[1:])
	case 'x':
		return len(seg) == 3 && allHex(seg[1:])
	}
	return false
}

// plausibleSegments rejects a path whose segments contain punctuation a
// filesystem name would not hold. The machine's path class is already narrow;
// what it cannot express is a printf placeholder inside a segment (q:\n%s).
func plausibleSegments(segments [][]byte) bool {
	for _, seg := range segments {
		if !plausibleSegment(seg) {
			return false
		}
	}
	return true
}

// plausibleSegment is the per-segment check: a name needs something other than
// punctuation, and a '%' must start a two-digit escape (%20) rather than a
// printf verb (%s).
func plausibleSegment(seg []byte) bool {
	if len(seg) == 0 {
		return false
	}
	if string(seg) == "." || string(seg) == ".." {
		return true
	}
	hasName := false
	for i := 0; i < len(seg); i++ {
		switch c := seg[i]; {
		case isAlnumByte(c):
			hasName = true
		case c == '.' || c == '_' || c == '-' || c == '~' || c == '+' || c == '@' || c == '$':
		case c == '%':
			if i+2 >= len(seg) || !isHexByte(seg[i+1]) || !isHexByte(seg[i+2]) {
				return false
			}
			i += 2
		default:
			return false
		}
	}
	return hasName
}

// localPathRoots are the single-segment absolute paths accepted without any
// further evidence. They are the roots an agent or a container actually reads
// from; /project alone is not in the list because a lone /name is weak evidence.
var localPathRoots = map[string]bool{
	"home": true, "Users": true, "root": true, "etc": true, "var": true,
	"usr": true, "opt": true, "tmp": true, "mnt": true, "media": true,
	"srv": true, "private": true, "Volumes": true, "proc": true, "sys": true,
	"dev": true, "boot": true, "run": true, "bin": true, "sbin": true,
	"lib": true, "lib64": true, "nix": true, "snap": true, "System": true,
	"Library": true, "Applications": true, "storage": true, "sdcard": true,
	"android": true, "workspace": true, "workspaces": true, "app": true,
	"data": true, "project": true, "projects": true, "repo": true,
	"repos": true, "code": true, "src": true, "build": true, "work": true,
}

// isRoutePrefix names the first segments that make an absolute path an HTTP
// route rather than a filesystem path. The list is deliberately short and
// covers the protocol family this proxy carries; a local /api directory is far
// rarer than /api/v1 in LLM traffic.
func isRoutePrefix(segment []byte) bool {
	switch string(segment) {
	case "api", "apis", "oauth", "health", "healthz", "metrics", "graphql", "swagger":
		return true
	}
	if len(segment) < 2 || (segment[0] != 'v' && segment[0] != 'V') {
		return false
	}
	for _, c := range segment[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// splitPathSegments splits s on '/' and, when windows is set, also on '\'. Empty
// segments are dropped, so a trailing separator does not create one.
func splitPathSegments(s []byte, windows bool) [][]byte {
	var segments [][]byte
	start := 0
	for i := 0; i <= len(s); i++ {
		if i < len(s) && s[i] != '/' && (!windows || s[i] != '\\') {
			continue
		}
		if i > start {
			segments = append(segments, s[start:i])
		}
		start = i + 1
	}
	return segments
}

// hasFileExtension reports whether segment ends in a plausible file extension:
// ASCII alphanumerics after the last dot, with at least one letter. A leading
// dot is a dotfile name (.env, .gitignore) rather than an extension, so it gets
// a wider bound. Requiring a letter and rejecting '+' is what keeps
// application/vnd.api+json from reading as a file path.
func hasFileExtension(segment []byte) bool {
	dot := bytes.LastIndexByte(segment, '.')
	if dot < 0 || dot == len(segment)-1 {
		return false
	}
	ext := segment[dot+1:]
	if dot > 0 && len(ext) > 8 {
		return false
	}
	if dot == 0 && len(ext) > 16 {
		return false
	}
	hasLetter := false
	for _, c := range ext {
		if !isAlnumByte(c) {
			return false
		}
		if isASCIIAlpha(c) {
			hasLetter = true
		}
	}
	return hasLetter
}

func allHex(b []byte) bool {
	for _, c := range b {
		if !isHexByte(c) {
			return false
		}
	}
	return len(b) > 0
}

func allDigits(b []byte) bool {
	for _, c := range b {
		if c < '0' || c > '9' {
			return false
		}
	}
	return len(b) > 0
}

func isASCIIAlpha(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isHexByte(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}
