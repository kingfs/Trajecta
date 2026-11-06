package secretmask

import "bytes"

// DSN userinfo-password detection.
//
// A database, cache or message-broker connection string carries its credential
// in the URL userinfo: `postgres://user:password@host:5432/db`. The password is
// the only secret part - the user, host, port and database name are the address
// an operator needs to read - so the masker rewrites exactly that sub-span.
//
// The evidence is the scheme. The earlier name-anchored pass only treated a URL
// as a DSN when the whole value hung off a URL-class variable name
// (`DATABASE_URL=...`), which missed a DSN pasted into prose or returned by a
// tool. This pass is independent of any variable name: it finds `://` directly,
// looks the scheme back up against a closed allowlist, and parses the userinfo
// with the same first-colon rule as assignmentURLPassword.
//
// It runs inside appendNativeFindings' separator walk, on the ':' that follows
// the scheme. When a DSN is the value of a URL-class assignment, the '=' is
// seen first and the assignment path consumes the whole value, so the same
// password is never reported twice: the name-anchored `env.url_credential`
// labels it in that case.

// dsnSchemes are the schemes accepted as a credentialed connection string. The
// list is deliberately closed: a general `scheme://user:password@host` rule
// would also rewrite an example URL in documentation or a `git clone` URL in a
// tool result, which is exactly the kind of false rewrite this pass exists to
// avoid. New services belong here explicitly.
var dsnSchemes = [...]string{
	"postgres",
	"postgresql",
	"pgsql",
	"cockroachdb",
	"redis",
	"rediss",
	"mysql",
	"mariadb",
	"mongodb",
	"mongodb+srv",
	"amqp",
	"amqps",
	"mssql",
	"sqlserver",
	"clickhouse",
}

// maxDSNSchemeLen bounds the scheme look-back. The longest accepted base scheme
// is "cockroachdb" at 11 bytes; a SQLAlchemy-style driver suffix adds '+ ' plus
// a driver name ("postgresql+psycopg", "mysql+pymysql"), so the bound leaves
// room for one while keeping the per-separator cost constant.
const maxDSNSchemeLen = 24

// dsnLeadIn reports whether the ':' at colon is followed by "//", which is the
// cheap test every DSN has to pass before the scheme look-back.
func dsnLeadIn(src []byte, colon int) bool {
	return colon+2 < len(src) && src[colon+1] == '/' && src[colon+2] == '/'
}

// dsnAt returns the finding for the userinfo password of the DSN whose scheme
// separator is the ':' at colon, if colon starts a recognised DSN with a
// password. It also returns the position right after the '@', which the walk
// uses to skip the password and continue with the host: skipping the whole
// authority would swallow the ':' of a second `scheme://` on the same line.
//
// The password is everything between the first ':' and the first '@' of the
// userinfo. RFC 3986 does not allow a raw '@' inside userinfo (it must be
// percent-encoded), so the first '@' is the delimiter; a parser that took the
// last '@' would run across a second URL on the same line. Every other userinfo
// byte, including the sub-delims `! $ & ' ( ) * + , ; =` and ':', stays inside
// the span - those are legal unencoded and a password may contain them.
func dsnAt(src []byte, colon int) (Finding, int, bool) {
	if !dsnLeadIn(src, colon) {
		return Finding{}, 0, false
	}
	if _, ok := dsnSchemeBefore(src, colon); !ok {
		return Finding{}, 0, false
	}
	authorityStart := colon + 3
	at := -1
	for i := authorityStart; i < len(src); i++ {
		c := src[i]
		if c == '@' {
			at = i
			break
		}
		if dsnUserinfoTerminator(c) {
			break
		}
	}
	if at < 0 {
		// No userinfo: the DSN carries no password, and this is the common
		// `postgres://host:5432/db` case. Nothing to mask.
		return Finding{}, 0, false
	}
	userinfo := src[authorityStart:at]
	c := bytes.IndexByte(userinfo, ':')
	if c < 0 {
		// A bare user (`postgres://app_user@host/db`) has no password.
		return Finding{}, 0, false
	}
	passwordStart := authorityStart + c + 1
	if passwordStart >= at {
		return Finding{}, 0, false
	}
	password := src[passwordStart:at]
	if len(password) < assignmentURLPasswordMin || assignmentPasswordValueIsSkipped(password) {
		return Finding{}, 0, false
	}
	return newAssignmentFinding(RuleDSNPassword, passwordStart, at), at + 1, true
}

// dsnSchemeBefore returns the scheme in front of colon if it is one of
// dsnSchemes. The scheme has to start a token: a byte of an identifier or a
// scheme character immediately before it means the look-back found the tail of
// a longer word, not a scheme.
func dsnSchemeBefore(src []byte, colon int) (string, bool) {
	start := colon
	for start > 0 && colon-start < maxDSNSchemeLen && dsnSchemeByte(src[start-1]) {
		start--
	}
	if start == colon {
		return "", false
	}
	if start > 0 {
		prev := src[start-1]
		if isAlnumByte(prev) || prev == '.' || prev == '+' || prev == '-' || prev == '_' {
			return "", false
		}
	}
	scheme := src[start:colon]
	for _, known := range dsnSchemes {
		if len(scheme) == len(known) && equalFoldAt(scheme, known) {
			return known, true
		}
	}
	// SQLAlchemy-style driver suffixes name the same service:
	// `postgresql+psycopg://`, `mysql+pymysql://`, `mongodb+srv+...`. Accept the
	// base scheme when the full token is not in the list; the exact match above
	// keeps `mongodb+srv` attributed as its own scheme.
	if plus := bytes.IndexByte(scheme, '+'); plus > 0 {
		base := scheme[:plus]
		for _, known := range dsnSchemes {
			if len(base) == len(known) && equalFoldAt(base, known) {
				return known, true
			}
		}
	}
	return "", false
}

// dsnSchemeByte is the URL scheme character set (RFC 3986): a letter, a digit,
// '+', '-' or '.'.
func dsnSchemeByte(c byte) bool {
	switch {
	case isAlphaLetter(c), isDigitByte(c), c == '+', c == '-', c == '.':
		return true
	}
	return false
}

// dsnUserinfoTerminator ends the userinfo scan before an '@' is found. It lists
// the bytes that cannot appear raw in RFC 3986 userinfo: controls and
// whitespace, the quote/backslash characters that delimit a JSON or shell
// string, the angle brackets of prose/HTML, and the URL structural characters
// `/`, `?` and `#`. Everything else is kept, including the sub-delims
// `! $ & ' ( ) * + , ; =` and ':', which are legal unencoded in userinfo and
// therefore must not truncate a password span. A single quote is deliberately
// NOT a terminator even though it also delimits shell/JSON text: it is legal
// userinfo, and the closing quote of a `'postgres://...'` literal comes after
// the authority anyway. (An unencoded '@' in the password is the documented
// boundary: it splits userinfo, so only the part before it is masked.)
func dsnUserinfoTerminator(c byte) bool {
	if c <= ' ' || c >= 0x7f {
		return true
	}
	switch c {
	case '"', '`', '\\', '<', '>', '|', '^', '{', '}', '[', ']', '/', '?', '#':
		return true
	}
	return false
}
