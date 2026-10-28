package resources

import (
	"encoding/json"
	"io"
	"strings"
)

// The argument half of the extractor: which argument values may name a
// resource, and what kind of resource each one may be.
//
// A tool call's arguments are structured, so the key is evidence. A
// `file_path` argument is a location; a `pattern` argument is a regular
// expression that can look exactly like one (`/gpt-5/i`); a `content` argument
// is a document body whose import specifiers (`../../lib/monitor`) are not
// accesses. Reading the key is what separates those cases, and it is why the
// index no longer depends on the shape of the text alone.
//
// Reading the raw JSON with a decoder rather than unmarshalling into a map keeps
// the argument order the model wrote: first-appearance order is what the trace
// tab and the catalogue use, and Go map iteration would randomize it.

// keyRole is what a tool argument's key says about its value.
type keyRole int

const (
	// roleAny is an unclassified key: the value may be a resource only when it
	// is exactly one token, which keeps prose and code out.
	roleAny keyRole = iota
	// rolePath is a filesystem location (file_path, cwd, target_file, ...).
	rolePath
	// roleURL is a network location (url, href, endpoint, ...).
	roleURL
	// roleCommand is a shell command (command, cmd, script, ...).
	roleCommand
	// roleContent is a document or code body: never an access.
	roleContent
)

// contentKeys are the argument names that carry a document, a pattern or some
// other text in which a path-shaped token is content rather than an operation.
// They take precedence over every other rule: `pattern` is checked before the
// suffix rules so a `search_pattern` cannot read as a path.
var contentKeys = map[string]bool{
	"content": true, "contents": true, "body": true, "text": true,
	"code": true, "sourcecode": true, "source": true, "patch": true,
	"diff": true, "oldstring": true, "newstring": true, "oldstr": true,
	"newstr": true, "oldtext": true, "newtext": true, "pattern": true,
	"regex": true, "regexp": true, "glob": true, "query": true,
	"prompt": true, "description": true, "message": true, "title": true,
	"instructions": true, "input": true, "output": true, "stdout": true,
	"stderr": true, "result": true, "error": true, "data": true,
	"snippet": true, "search": true, "keyword": true, "keywords": true,
	"language": true, "encoding": true, "reason": true, "summary": true,
	"notes": true, "explanation": true, "answer": true, "response": true,
	"commandoutput": true, "context": true, "schema": true,
}

// urlSuffixes are the key tails that make a value a network location.
var urlSuffixes = []string{"url", "urls", "uri", "href"}

// pathSuffixes are the key tails that make a value a filesystem location. They
// are suffixes rather than exact names so an MCP tool's `target_path` or
// `workspace_file` is read without a per-tool table.
var pathSuffixes = []string{
	"path", "paths", "file", "files", "filename", "filenames", "dir", "dirs",
	"directory", "directories", "folder", "folders", "cwd", "workdir",
	"workspace", "root", "target",
}

// commandSuffixes are the key tails that make a value a shell command.
var commandSuffixes = []string{"command", "cmd", "script", "shell"}

// classifyArgumentKey maps one argument key to the role its value plays.
func classifyArgumentKey(key string) keyRole {
	normalized := normalizeKey(key)
	if normalized == "" {
		return roleAny
	}
	if contentKeys[normalized] {
		return roleContent
	}
	for _, suffix := range urlSuffixes {
		if strings.HasSuffix(normalized, suffix) {
			return roleURL
		}
	}
	for _, suffix := range commandSuffixes {
		if strings.HasSuffix(normalized, suffix) {
			return roleCommand
		}
	}
	for _, suffix := range pathSuffixes {
		if strings.HasSuffix(normalized, suffix) {
			return rolePath
		}
	}
	return roleAny
}

// normalizeKey lower-cases a key and drops the separators tools vary on, so
// `file_path`, `filePath` and `FilePath` are one name.
func normalizeKey(key string) string {
	var builder strings.Builder
	builder.Grow(len(key))
	for _, r := range key {
		switch {
		case r == '_' || r == '-' || r == ' ' || r == '.':
			continue
		case r >= 'A' && r <= 'Z':
			builder.WriteRune(r + ('a' - 'A'))
		default:
			builder.WriteRune(r)
		}
	}
	return builder.String()
}

// walkArguments visits every string value of a JSON argument object with the
// role its key gave it. It reports false when text is not a JSON object or array
// so the caller can fall back to the free-form input path.
func walkArguments(text string, emit func(mode, token string, start, end int)) bool {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	walker := argumentWalker{emit: emit}
	ok, err := walker.value(decoder, roleAny, 0)
	if err != nil || !ok {
		return false
	}
	return true
}

type argumentWalker struct {
	emit func(mode, token string, start, end int)
}

// value consumes one JSON value and emits every access candidate inside it.
// It returns false when the value is not a JSON object or array (a bare string
// or number is not an argument object).
func (w argumentWalker) value(decoder *json.Decoder, role keyRole, depth int) (bool, error) {
	if depth > 12 {
		return false, errTooDeep
	}
	token, err := decoder.Token()
	if err != nil {
		return false, err
	}
	switch typed := token.(type) {
	case json.Delim:
		switch typed {
		case '{':
			if err := w.object(decoder, role, depth); err != nil {
				return false, err
			}
			return true, nil
		case '[':
			if err := w.array(decoder, role, depth); err != nil {
				return false, err
			}
			return true, nil
		}
		return false, nil
	default:
		// A top-level scalar is not an argument object.
		return false, nil
	}
}

func (w argumentWalker) object(decoder *json.Decoder, parent keyRole, depth int) error {
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, _ := keyToken.(string)
		role := classifyArgumentKey(key)
		if role == roleAny && (parent == rolePath || parent == roleURL) {
			role = parent
		}
		if role == roleContent {
			if err := skipJSONValue(decoder); err != nil {
				return err
			}
			continue
		}
		if err := w.child(decoder, role, depth+1); err != nil {
			return err
		}
	}
	_, err := decoder.Token() // closing '}'
	return err
}

func (w argumentWalker) array(decoder *json.Decoder, role keyRole, depth int) error {
	if role == roleCommand {
		// A command vector (["bash", "-lc", "cat /x"]) is one command, not one
		// command per element.
		var parts []string
		for decoder.More() {
			token, err := decoder.Token()
			if err != nil {
				return err
			}
			if text, ok := token.(string); ok {
				parts = append(parts, text)
				continue
			}
			if delim, ok := token.(json.Delim); ok {
				if err := skipJSONBody(decoder, delim); err != nil {
					return err
				}
			}
		}
		_, err := decoder.Token() // closing ']'
		if err != nil {
			return err
		}
		if len(parts) > 0 {
			shellCandidates(joinCommandVector(parts), w.emit)
		}
		return nil
	}
	for decoder.More() {
		if err := w.child(decoder, role, depth+1); err != nil {
			return err
		}
	}
	_, err := decoder.Token() // closing ']'
	return err
}

// child consumes one value with a known role.
func (w argumentWalker) child(decoder *json.Decoder, role keyRole, depth int) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	switch typed := token.(type) {
	case json.Delim:
		switch typed {
		case '{':
			return w.object(decoder, role, depth)
		case '[':
			return w.array(decoder, role, depth)
		}
		return nil
	case string:
		w.emitString(role, typed)
		return nil
	default:
		return nil
	}
}

func (w argumentWalker) emitString(role keyRole, text string) {
	if text == "" {
		return
	}
	switch role {
	case roleCommand:
		shellCandidates(text, w.emit)
	case roleURL:
		w.emit(KindURL, text, 0, len(text))
	case rolePath:
		w.emit(modePathField, text, 0, len(text))
	case roleContent:
		return
	default:
		// A wrapper object can carry the real arguments as a JSON string
		// (`{"arguments": "{\"path\": \"/x\"}"}`); read it at the same depth
		// instead of treating the whole string as one token.
		if strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") {
			if walkArguments(text, w.emit) {
				return
			}
		}
		// An unclassified argument is a resource only when it is exactly one
		// token with a strong path/URL shape: a path or a URL has no
		// whitespace, prose does, and "../../lib/monitor" needs a file
		// extension before an unclassified field can mean a file.
		if strings.ContainsAny(text, " \t\r\n") {
			return
		}
		w.emit(modeStrictPath, text, 0, len(text))
	}
}

// skipJSONValue consumes one JSON value without visiting it.
func skipJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delim, ok := token.(json.Delim); ok {
		return skipJSONBody(decoder, delim)
	}
	return nil
}

// skipJSONBody consumes the remainder of an already-opened object or array.
func skipJSONBody(decoder *json.Decoder, open json.Delim) error {
	closer := json.Delim('}')
	if open == '[' {
		closer = ']'
	}
	for {
		token, err := decoder.Token()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if delim, ok := token.(json.Delim); ok {
			if delim == closer {
				return nil
			}
			if err := skipJSONBody(decoder, delim); err != nil {
				return err
			}
		}
	}
}
