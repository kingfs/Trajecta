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
//   - ~/..., ./..., ../ and a drive- or UNC-rooted path are local by prefix.
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
	if tok[0] == '~' {
		return len(tok) > 1 && tok[1] == '/'
	}
	if tok[0] == '.' && len(tok) >= 2 && tok[1] == '/' {
		return true
	}
	if len(tok) >= 3 && tok[0] == '.' && tok[1] == '.' && tok[2] == '/' {
		return true
	}
	if isASCIIAlpha(tok[0]) && len(tok) >= 3 && tok[1] == ':' && (tok[2] == '\\' || tok[2] == '/') {
		return len(tok) > 3
	}
	if len(tok) >= 2 && (tok[0] == '\\' || tok[0] == '/') && (tok[1] == '\\' || tok[1] == '/') {
		return len(splitPathSegments(tok[2:], true)) >= 2
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
			return !isRoutePrefix(segments[0])
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
	return hasFileExtension(segments[len(segments)-1])
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
