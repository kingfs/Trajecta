// Code generated from indicator.rl by ragel. DO NOT EDIT.
//
// Regenerate with:
//
//	task generate:secretmask
//
// Source of truth: pkg/secretmask/indicator.rl
//
// This is a second machine, not part of the credential scanner in rules.rl, and
// the split is deliberate. A Ragel scanner consumes what it matched, so a URL
// rule living in the same machine would swallow every credential inside the URL
// (a presigned AWS URL carries AKIA... in its query string) and the masker would
// never see it. Two machines mean two independent passes over the same bytes:
// one protects credentials, this one reports access indicators.

package secretmask

%%{
machine indicator;

# --- character classes -------------------------------------------------
# Everything RFC 3986 admits in a path, query or fragment, minus the bytes
# that delimit a URL in the text this runs over: a double quote (JSON), an
# angle bracket or backtick (markdown/HTML), whitespace, and a backslash.
urlchar  = [0-9A-Za-z._~%!$&'()*+,;=:@/?#\-];
# A host name or an address literal in brackets. Brackets and colons are
# handled by the host alternative below, not by this class.
hostchar = [0-9A-Za-z._~\-];
# A path segment: urlchar without the query/fragment/path separators and
# without the punctuation that separates one path from the next in prose
# (quotes, commas, semicolons, glob stars, parentheses) and without the '='
# of a "key=value" assignment. Those bytes are what put a leading
# "(modules/x.md" or "registry_file=internal/x.go" in front of a real path;
# the native check cannot strip a prefix once it is part of the match, so
# the machine must not consume it in the first place.
pathchar = [0-9A-Za-z._~%$+@\-];
# One or more path bytes: the segment itself, not a single character.
pathseg  = pathchar+;
# Userinfo cannot cross a '/', '?' or '#': e.g. the '@' in a path segment
# (@2x.png) must not turn the authority into the path.
userchar = [0-9A-Za-z._~%!$&'*+,;=:\-];

# --- URLs ---------------------------------------------------------------
# http(s) and ws(s) only. A URL with no scheme (example.com/x) is left to the
# path shape and rejected natively: guessing a scheme is how a hostname in
# prose becomes a "visit".
scheme = ('https' | 'http' | 'wss' | 'ws') '://';
host   = hostchar+ | '[' [0-9A-Fa-f:.]+ ']';
port   = ':' [0-9]{1,5};
userinfo = userchar+ '@';
url    = scheme userinfo? host port? urlchar*;

# --- local paths --------------------------------------------------------
# The machine matches the whole path shape; which of these is a local path
# (and not an API route, a MIME type or a DNS name) is decided natively in
# candidateLocalPathShape (indicator.go), the same coarse/fine split the
# digit_run pattern uses.
posix_abs = '/' ( pathseg '/' )* pathseg?;
tilde     = '~/' ( pathseg '/' )* pathseg?;
dot_rel   = ( './' | '../' ) ( pathseg '/' )* pathseg?;
# Windows separators arrive in two forms: a decoded transcript carries one
# backslash, while the same path inside a tool call's arguments is JSON text
# and carries two (C:\\Users\\alice). Both are accepted, and the analyzer
# normalizes the escaped pair back to one before it records the value.
bs = [\\/];
win_sep = bs | bs bs;
# A drive letter, then a separator. Both '\' and '/' are accepted because a
# transcript may carry either; the native check only requires one segment
# after the drive.
win_path  = [A-Za-z] ':' win_sep ( pathseg win_sep )* pathseg;
# \\server\share or //server/share, in the same one- and two-backslash forms.
# The forward form also matches a POSIX path with a doubled root slash, which
# the native check treats the same way: it is a local path either way.
unc_start = bs bs | bs bs bs bs;
unc_path  = unc_start pathseg ( win_sep pathseg )+;
# A relative path with at least one separator. src/main.go lands here; so do
# net/http and application/vnd.api+json, which the native check rejects (no
# file extension, or a '+' in the extension).
rel_path  = pathseg ( '/' pathseg )+ '/'?;

# Longest match wins; an equal-length tie goes to the pattern declared first.
# URL is first so an http(s) span is never reported as a path fragment. All
# path shapes report the same rule: the native check, not the machine, decides
# which form it is.
main := |*
    url       => { sink.emit(RuleURL, ts, te) };

    win_path  => { sink.emit(RuleLocalPath, ts, te) };
    unc_path  => { sink.emit(RuleLocalPath, ts, te) };
    posix_abs => { sink.emit(RuleLocalPath, ts, te) };
    tilde     => { sink.emit(RuleLocalPath, ts, te) };
    dot_rel   => { sink.emit(RuleLocalPath, ts, te) };
    rel_path  => { sink.emit(RuleLocalPath, ts, te) };

    any;
*|;
}%%

%% write data;

// scanIndicatorDFA runs the report-only access DFA over data, appending every
// candidate to sink. It performs no allocation and no validation beyond the
// rule metadata checks in indicatorCollector.emit; the shape checks that make
// a candidate a URL or a local path live in indicator.go.
func scanIndicatorDFA(data []byte, sink *indicatorCollector) {
	cs, p, pe, eof := 0, 0, len(data), len(data)
	ts, te, act := 0, 0, 0
	_ = act
	_ = te
	// write init is required here: the machine's start state is not 0, and
	// without it `write exec` would enter the state table at state 0 (a middle
	// state) and report spans that neither start nor end where a pattern did.
	%% write init;
	%% write exec;
	_ = cs
	_ = p
	_ = pe
	_ = eof
	_ = ts
}
