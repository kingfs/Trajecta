package resources

import "strings"

// The shell half of the extractor: a command string becomes the file and URL
// operands the command actually names.
//
// A shell command is the one argument shape where the operation is not a field
// name but a position. `cat /etc/hosts` names a file, `grep -E '/gpt-5/i' src`
// names a regular expression and a directory, and `node -e '...'` names neither.
// Splitting the command the way a POSIX shell would, then reading the operators
// and the command's operand positions, is what tells them apart; the shape
// detector only validates the tokens that survive this layer.

// shellToken is one word of a command plus the position it occupies.
type shellToken struct {
	text  string
	start int
	end   int
	flags uint8
}

const (
	// shellCommandStart marks the first word of a simple command.
	shellCommandStart uint8 = 1 << iota
	// shellRedirect marks a redirection target, which is always a file path.
	shellRedirect
)

// shellSeparators end a word. `&`, `|`, `;`, a newline and a subshell all start
// a new command; `<`, `>`, backticks and braces are handled separately.
const shellSeparators = " \t\r\n;|&()<>`{}"

// shellCandidates yields every token of command that sits in an operand
// position. mode is empty because the caller lets the shape decide whether the
// token is a path or a URL.
func shellCandidates(command string, emit func(mode, token string, start, end int)) {
	tokens := tokenizeShell(command)
	for start := 0; start < len(tokens); {
		end := start + 1
		for end < len(tokens) && tokens[end].flags&shellCommandStart == 0 {
			end++
		}
		processShellSegment(tokens[start:end], emit)
		start = end
	}
}

// processShellSegment handles one simple command: its command word, its
// options, and its operands.
func processShellSegment(tokens []shellToken, emit func(mode, token string, start, end int)) {
	if len(tokens) == 0 {
		return
	}
	// Redirection targets are files regardless of what the command is. Device
	// sinks (/dev/null) are excluded: they are plumbing, not a read.
	for _, token := range tokens {
		if token.flags&shellRedirect == 0 || isDevicePath(token.text) {
			continue
		}
		emit("", token.text, token.start, token.end)
	}
	index := 0
	for index < len(tokens) {
		token := tokens[index]
		if token.flags&shellRedirect != 0 {
			index++
			continue
		}
		if isShellAssignment(token.text) {
			index++
			continue
		}
		if shellPrefixWords[token.text] {
			index++
			index = skipShellPrefixOptions(tokens, index)
			continue
		}
		break
	}
	if index >= len(tokens) {
		return
	}
	commandToken := tokens[index]
	command := commandBasename(commandToken.text)
	if commandToken.flags&shellRedirect == 0 && looksLikePathToken(commandToken.text) {
		// The command itself is a file the shell executed (./scripts/build.sh).
		emit("", commandToken.text, commandToken.start, commandToken.end)
	}
	rest := tokens[index+1:]
	if len(rest) == 0 || noPathCommands[command] {
		return
	}
	// A shell running a script with -c: the script is a command of its own.
	if shellInterpreters[command] {
		if script, ok := shellScriptArgument(rest); ok {
			shellCandidates(script, emit)
			return
		}
	}
	patternPending := patternFirstCommands[command]
	if command == "git" {
		// The subcommand is not an operand, and `git grep` takes a pattern in
		// the position that would otherwise be a path.
		if subcommand, at, ok := gitSubcommand(rest); ok {
			patternPending = subcommand == "grep"
			rest = rest[at+1:]
		}
	}
	skipNext := false
	for _, token := range rest {
		if token.flags&shellRedirect != 0 {
			continue // already emitted
		}
		text := token.text
		if skipNext {
			skipNext = false
			continue
		}
		if isShellAssignment(text) {
			continue
		}
		if isShellOption(text) {
			name, value, hasValue := splitShellOption(text)
			if !hasValue && (name == "-f" || name == "--file") {
				// A script or pattern file is a real read, and the pattern no
				// longer comes from an operand.
				patternPending = false
				continue
			}
			if hasValue {
				if flagTakesContent(command, name) {
					patternPending = false
					continue
				}
				emit("", value, token.start, token.end)
				continue
			}
			if flagTakesContent(command, name) {
				skipNext = true
				patternPending = false
			}
			continue
		}
		if patternPending {
			patternPending = false
			continue
		}
		emit("", text, token.start, token.end)
	}
}

// skipShellPrefixOptions skips the options of a wrapper command (sudo -u root,
// env FOO=bar, nice -n 5) so the word after them is read as the real command.
func skipShellPrefixOptions(tokens []shellToken, index int) int {
	for index < len(tokens) {
		token := tokens[index]
		if token.flags&shellRedirect != 0 {
			return index
		}
		if isShellAssignment(token.text) {
			index++
			continue
		}
		if !isShellOption(token.text) {
			return index
		}
		name, _, hasValue := splitShellOption(token.text)
		index++
		if !hasValue && prefixValueOptions[name] {
			index++
		}
	}
	return index
}

// prefixValueOptions are the wrapper-command options whose next token is their
// value rather than the command to run.
var prefixValueOptions = map[string]bool{
	"-u": true, "-g": true, "-p": true, "-C": true, "-U": true, "-r": true,
	"-t": true, "-D": true, "-n": true, "-S": true, "-I": true, "-L": true,
}

// tokenizeShell splits a command into words and marks command starts and
// redirection targets. Quotes and backslashes are removed from the token text,
// which is what makes `"/path/with space"` one token.
func tokenizeShell(src string) []shellToken {
	tokens := make([]shellToken, 0, 8)
	commandStart := true
	redirectNext := false
	for i := 0; i < len(src); {
		c := src[i]
		switch c {
		case ' ', '\t', '\r':
			i++
			continue
		case '\n', ';', '|', '&', '(', ')', '`', '{', '}':
			commandStart = true
			i++
			continue
		case '#':
			// A '#' at the start of a word begins a comment.
			for i < len(src) && src[i] != '\n' {
				i++
			}
			continue
		case '<', '>':
			if c == '<' && i+1 < len(src) && src[i+1] == '<' {
				// A here-document's body is data written to stdin, not a
				// command; it must not be tokenized as one. A here-string's
				// word is data too.
				if i+2 < len(src) && src[i+2] == '<' {
					i = skipHereString(src, i)
					continue
				}
				i = skipHereDocument(src, i)
				continue
			}
			i = skipRedirectOperator(src, i)
			redirectNext = true
			continue
		}
		if c >= '0' && c <= '9' {
			j := i
			for j < len(src) && src[j] >= '0' && src[j] <= '9' {
				j++
			}
			if j < len(src) && (src[j] == '<' || src[j] == '>') {
				i = skipRedirectOperator(src, j)
				redirectNext = true
				continue
			}
		}
		start := i
		var builder strings.Builder
		for i < len(src) {
			c := src[i]
			if strings.IndexByte(shellSeparators, c) >= 0 {
				break
			}
			switch c {
			case '\'':
				i++
				for i < len(src) && src[i] != '\'' {
					builder.WriteByte(src[i])
					i++
				}
				if i < len(src) {
					i++
				}
			case '"':
				i++
				for i < len(src) && src[i] != '"' {
					if src[i] == '\\' && i+1 < len(src) {
						next := src[i+1]
						if next == '"' || next == '\\' || next == '$' || next == '`' {
							builder.WriteByte(next)
							i += 2
							continue
						}
					}
					builder.WriteByte(src[i])
					i++
				}
				if i < len(src) {
					i++
				}
			case '\\':
				if i+1 < len(src) {
					builder.WriteByte(src[i+1])
					i += 2
					continue
				}
				i++
			default:
				builder.WriteByte(c)
				i++
			}
		}
		if builder.Len() == 0 {
			continue
		}
		token := shellToken{text: builder.String(), start: start, end: i}
		if commandStart {
			token.flags |= shellCommandStart
		}
		if redirectNext {
			token.flags |= shellRedirect
			redirectNext = false
		}
		tokens = append(tokens, token)
		commandStart = false
	}
	return tokens
}

// skipHereString consumes a bash `<<< word` and returns the offset after the
// word. The word is data for stdin, never a path operand.
func skipHereString(src string, i int) int {
	i += 3
	for i < len(src) && (src[i] == ' ' || src[i] == '\t') {
		i++
	}
	return skipShellWord(src, i)
}

// skipHereDocument consumes a `<<DELIM` / `<<'DELIM'` operator and the body up
// to the line that repeats the delimiter. The body is not a command, so code or
// documentation inside it is never read as an operand.
func skipHereDocument(src string, i int) int {
	i += 2
	tabs := false
	if i < len(src) && src[i] == '-' {
		tabs = true
		i++
	}
	for i < len(src) && (src[i] == ' ' || src[i] == '\t') {
		i++
	}
	delimiter, after := readHereDocumentDelimiter(src, i)
	if delimiter == "" {
		return after
	}
	i = after
	for i < len(src) {
		// Move to the start of the next line.
		for i < len(src) && src[i] != '\n' {
			i++
		}
		if i >= len(src) {
			return i
		}
		i++
		lineStart := i
		for i < len(src) && src[i] != '\n' {
			i++
		}
		line := src[lineStart:i]
		if tabs {
			line = strings.TrimLeft(line, "\t")
		}
		if line == delimiter {
			if i < len(src) {
				i++ // newline after the terminator
			}
			return i
		}
	}
	return i
}

// readHereDocumentDelimiter reads the word after `<<` and returns it unquoted.
func readHereDocumentDelimiter(src string, i int) (string, int) {
	if i >= len(src) {
		return "", i
	}
	var builder strings.Builder
	if quote := src[i]; quote == '\'' || quote == '"' {
		i++
		for i < len(src) && src[i] != quote {
			builder.WriteByte(src[i])
			i++
		}
		if i < len(src) {
			i++
		}
		return builder.String(), i
	}
	for i < len(src) && src[i] != ' ' && src[i] != '\t' && src[i] != '\n' && src[i] != ';' && src[i] != '|' && src[i] != '&' {
		builder.WriteByte(src[i])
		i++
	}
	return builder.String(), i
}

// skipShellWord consumes one whitespace-delimited word, honouring quotes.
func skipShellWord(src string, i int) int {
	for i < len(src) {
		switch src[i] {
		case ' ', '\t', '\n', ';', '|', '&', '(', ')', '<', '>':
			return i
		case '\'':
			i++
			for i < len(src) && src[i] != '\'' {
				i++
			}
			if i < len(src) {
				i++
			}
		case '"':
			i++
			for i < len(src) && src[i] != '"' {
				if src[i] == '\\' && i+1 < len(src) {
					i += 2
					continue
				}
				i++
			}
			if i < len(src) {
				i++
			}
		default:
			i++
		}
	}
	return i
}

// skipRedirectOperator consumes one redirection operator, including an fd prefix
// that was already skipped and a `>&2` duplication.
func skipRedirectOperator(src string, i int) int {
	for i < len(src) && (src[i] == '<' || src[i] == '>') {
		i++
	}
	if i < len(src) && src[i] == '&' {
		i++
		for i < len(src) && (src[i] == '-' || src[i] >= '0' && src[i] <= '9') {
			i++
		}
	}
	return i
}

// shellScriptArgument returns the script of a `sh -c '...'` invocation: the
// token after a short flag cluster that contains 'c'.
func shellScriptArgument(tokens []shellToken) (string, bool) {
	for index := 0; index < len(tokens); index++ {
		token := tokens[index]
		if token.flags&shellRedirect != 0 {
			continue
		}
		text := token.text
		if text == "-c" || strings.HasPrefix(text, "-") && !strings.HasPrefix(text, "--") && strings.Contains(text[1:], "c") {
			if index+1 < len(tokens) {
				return tokens[index+1].text, true
			}
			return "", false
		}
		if !strings.HasPrefix(text, "-") {
			return "", false
		}
	}
	return "", false
}

// gitSubcommand returns the git subcommand word and its position, skipping the
// global options that precede it.
func gitSubcommand(rest []shellToken) (string, int, bool) {
	for index := 0; index < len(rest); index++ {
		token := rest[index]
		if token.flags&shellRedirect != 0 {
			continue
		}
		if isShellOption(token.text) {
			name, _, hasValue := splitShellOption(token.text)
			if hasValue {
				continue
			}
			switch name {
			case "-C", "-c", "--git-dir", "--work-tree", "--namespace", "--exec-path":
				index++ // option value
			}
			continue
		}
		return token.text, index, true
	}
	return "", 0, false
}

// isShellOption reports whether a token is an option rather than an operand.
func isShellOption(text string) bool {
	return len(text) > 1 && text[0] == '-'
}

// splitShellOption splits --name=value; for every other option it reports the
// name with no value.
func splitShellOption(text string) (name, value string, hasValue bool) {
	if index := strings.IndexByte(text, '='); index > 0 {
		return text[:index], text[index+1:], true
	}
	return text, "", false
}

// flagTakesContent reports whether an option's value is a pattern, a message or
// code rather than a path. A few short flags are content only for the commands
// that use them that way (`grep -e`, `gcc -c`).
func flagTakesContent(command, name string) bool {
	switch name {
	case "-c", "-e":
		return patternFirstCommands[command] || codeInterpreters[command]
	case "-r":
		return command == "php"
	}
	return contentFlags[name]
}

// isShellAssignment reports whether a word is the NAME=value prefix of a
// command, which is not an argument to it.
func isShellAssignment(text string) bool {
	index := strings.IndexByte(text, '=')
	if index <= 0 {
		return false
	}
	for position, r := range text[:index] {
		switch {
		case r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z':
		case r >= '0' && r <= '9' && position > 0:
		default:
			return false
		}
	}
	return true
}

// looksLikePathToken reports whether a command word could itself be a file. A
// bare binary name (`cat`) is not evidence of a file; `./tool`, `/usr/bin/tool`
// and `~/bin/tool` are.
func looksLikePathToken(text string) bool {
	if text == "" || text == "." || text == ".." {
		return false
	}
	if strings.ContainsAny(text, `/\`) {
		return true
	}
	return strings.HasPrefix(text, ".")
}

// commandBasename reduces a command word to its name: /usr/bin/python3.12 is
// python, and C:\Windows\system32\cmd.exe is cmd.
func commandBasename(text string) string {
	if index := strings.LastIndexAny(text, `/\`); index >= 0 {
		text = text[index+1:]
	}
	text = strings.ToLower(strings.TrimSuffix(text, ".exe"))
	trimmed := strings.TrimRight(text, "0123456789.")
	if trimmed == "" {
		return text
	}
	return trimmed
}

// isDevicePath reports whether a path names a device or a stream rather than a
// file an agent read or wrote.
func isDevicePath(text string) bool {
	switch text {
	case "/dev/null", "/dev/zero", "/dev/random", "/dev/urandom",
		"/dev/stdin", "/dev/stdout", "/dev/stderr", "/dev/tty":
		return true
	}
	return false
}

// shellPrefixWords are wrappers that precede the real command word.
var shellPrefixWords = map[string]bool{
	"sudo": true, "doas": true, "env": true, "time": true, "nohup": true,
	"nice": true, "ionice": true, "stdbuf": true, "command": true, "exec": true,
	"builtin": true, "then": true, "do": true, "else": true, "elif": true,
	"if": true, "while": true, "until": true, "!": true, "xargs": true,
}

// shellInterpreters run a script given as an argument.
var shellInterpreters = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true,
	"fish": true, "ash": true,
}

// codeInterpreters accept inline code through a flag (-c, -e).
var codeInterpreters = map[string]bool{
	"python": true, "node": true, "nodejs": true, "ruby": true, "perl": true,
	"php": true, "deno": true, "bun": true, "lua": true, "luajit": true,
	"rscript": true, "pwsh": true, "powershell": true, "osascript": true,
}

// noPathCommands read no files from their operands, so their arguments are not
// accesses even when they look like paths. `echo /gpt-5/i` is the shape this
// list exists for.
var noPathCommands = map[string]bool{
	"echo": true, "printf": true, "export": true, "set": true, "unset": true,
	"alias": true, "unalias": true, "true": true, "false": true, ":": true,
	"sleep": true, "pwd": true, "date": true, "whoami": true, "id": true,
	"uname": true, "hostname": true, "ps": true, "kill": true, "killall": true,
	"wait": true, "jobs": true, "fg": true, "bg": true, "type": true,
	"which": true, "whereis": true, "hash": true, "history": true,
	"shopt": true, "trap": true, "ulimit": true, "umask": true, "test": true,
	"[": true, "[[": true, "exit": true, "return": true, "shift": true,
	"read": true, "declare": true, "local": true, "let": true,
}

// patternFirstCommands take a pattern as their first operand, so that operand is
// not a path even when it looks like one (`grep -E '/gpt-5/i'`).
var patternFirstCommands = map[string]bool{
	"grep": true, "egrep": true, "fgrep": true, "zgrep": true, "rg": true,
	"ag": true, "ack": true, "sed": true, "gsed": true, "awk": true,
	"gawk": true, "mawk": true, "jq": true, "yq": true,
}

// contentFlags take a non-path value. They keep a message, a pattern, a code
// snippet or a header out of the operand stream.
var contentFlags = map[string]bool{
	"-m": true, "--eval": true, "--command": true,
	"--expression": true, "--message": true, "-H": true, "--header": true,
	"-d": true, "--data": true, "--data-raw": true, "--data-binary": true,
	"--data-urlencode": true, "-X": true, "--request": true, "-F": true,
	"--form": true, "-q": true, "--query": true, "--json": true,
	"--body": true, "--pattern": true, "--regex": true, "--regexp": true,
	"--glob": true, "--include": true, "--exclude": true, "--name": true,
	"-name": true, "-iname": true, "-path": true, "-ipath": true,
	"-regex": true, "-iregex": true, "-lname": true, "-ilname": true,
	"-wholename": true, "-grep": true, "--grep": true, "-S": true, "-G": true,
	"--format": true, "--pretty": true, "--date": true, "--color": true,
	"--filter": true, "--label": true,
}

// joinCommandVector renders an already-split command vector as one command
// string. Every element is quoted so the shell tokenizer keeps a script that
// contains spaces (["bash", "-lc", "cat /etc/hosts"]) as one token.
func joinCommandVector(vector []string) string {
	parts := make([]string, 0, len(vector))
	for _, part := range vector {
		parts = append(parts, "'"+strings.ReplaceAll(part, "'", `'\''`)+"'")
	}
	return strings.Join(parts, " ")
}
