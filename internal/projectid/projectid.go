// Package projectid extracts the project directory a client harness declares
// about itself in the prompt of one LLM request.
//
// This is deliberately not the resource index. internal/resources records which
// paths a model asked a tool to touch, which answers "what did this exchange
// operate on" and is tolerant by design: a single file path is evidence. The
// question here is different - "which project is this session working in" - and
// a file path is not an answer to it, because a project is a root, not a file,
// and because an agent also touches paths outside its project (dependency
// caches, temporary directories, a sibling repository).
//
// The only accepted evidence is therefore a labelled declaration: the harness
// says, in a fixed shape, where its working directory is. Three shapes are
// recognised today, and each is matched inside its own container so a path that
// merely looks like one - a code sample, a document, a tool result - cannot
// claim a project:
//
//   - DeepSeek Harness runtime context: a <system-reminder> block carrying
//     `Current working directory: "/path"`. The block is a user message, and it
//     supersedes earlier snapshots, so the last one in the request is current.
//   - Claude Code environment block: an <env> block carrying
//     `Primary working directory: /path` (older releases: `Working directory:`).
//     The block moved from the system prompt to a meta user message in 2.x, so
//     it is searched in message text as well.
//   - Codex environment context: an <environment_context> block carrying
//     <cwd>/path</cwd> and, for a multi-root workspace, <workspace_roots>.
//
// A request with none of those shapes yields no declaration. That is the
// intended outcome, not a failure: a wrong project is worse than no project,
// because the whole point of the index is to count sessions per project.
package projectid

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/kingfs/Trajecta/pkg/observe"
)

// Source names the declaration shape a root came from. It is persisted with the
// value so an operator can tell a harness upgrade from a new harness.
type Source string

const (
	// SourceDSHRuntimeContext is the DeepSeek Harness runtime-context snapshot.
	SourceDSHRuntimeContext Source = "dsh_runtime_context"
	// SourceClaudeEnv is the Claude Code environment block.
	SourceClaudeEnv Source = "claude_env"
	// SourceCodexEnv is the Codex cwd inside <environment_context>.
	SourceCodexEnv Source = "codex_env"
	// SourceCodexWorkspaceRoots is the first <root> of Codex's
	// <workspace_roots>, used when <cwd> is absent.
	SourceCodexWorkspaceRoots Source = "codex_workspace_roots"
)

// Declaration is the project directory one request declares.
type Declaration struct {
	// Root is the declared path as the client wrote it, with trailing
	// separators removed.
	Root string
	// Key is the comparison form: Windows paths are case-folded and use forward
	// slashes, POSIX paths are kept verbatim. Two declarations with the same Key
	// are the same project.
	Key string
	// Source is the declaration shape that produced Root.
	Source Source
	// Conflicting reports that this request declared more than one distinct
	// root. Root still holds the last one, which is the effective value: the
	// harnesses that re-declare (DeepSeek Harness, Codex) append the update and
	// supersede the older block.
	Conflicting bool
}

const (
	// maxRootBytes bounds an accepted declaration. No real project path is
	// longer, and a longer "path" is a match that ran away.
	maxRootBytes = 4096
)

// Extract returns the project directory obs declares, if any.
//
// Only instruction and message text is read, in request order, and tool calls,
// tool results, reasoning and response text are never inspected: a project
// declaration is something a harness says about itself, not something a model
// said about a file. The last declaration wins because the harnesses that can
// re-declare append an update, and the update supersedes the earlier block.
func Extract(obs observe.TraceObservation) (Declaration, bool) {
	var (
		best  Declaration
		found bool
	)

	// One node can be reachable from more than one list (Request.Nodes repeats
	// the typed lists), and a node can carry the same text in more than one
	// view. Visiting each node once is what keeps a single declaration from
	// counting as a conflict with itself.
	seen := make(map[string]struct{}, len(obs.Request.Nodes))
	var visit func([]observe.SemanticNode)
	visit = func(nodes []observe.SemanticNode) {
		for _, node := range nodes {
			key := node.ID
			if key == "" {
				key = node.Path + "\x00" + string(node.NormalizedType)
			}
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}
			if declaresProject(node) {
				for _, text := range declarationTexts(node) {
					for _, candidate := range matchText(text) {
						root, rootKey, ok := Normalize(candidate.value)
						if !ok {
							continue
						}
						if found && rootKey != best.Key {
							// Two different roots in one request is expected when
							// the client changed directory mid-session: the last
							// block is the current one.
							best.Conflicting = true
						}
						best = Declaration{Root: root, Key: rootKey, Source: candidate.source, Conflicting: best.Conflicting}
						found = true
					}
				}
			}
			visit(node.Children)
		}
	}
	// The typed lists carry the conversation in protocol order (instructions
	// first, then messages/inputs); Request.Nodes covers a parser that only
	// fills the tree, which is what the client-entry parser does.
	visit(obs.Request.Instructions)
	visit(obs.Request.Messages)
	visit(obs.Request.Inputs)
	visit(obs.Request.Nodes)
	return best, found
}

// declaresProject reports whether a node can carry a harness declaration.
//
// Tool calls and tool results are excluded structurally rather than by their
// text: a tool that reads a file containing an <environment_context> sample
// would otherwise announce that sample as the session's project. Instruction
// and message nodes are the only places a harness speaks in its own voice.
func declaresProject(node observe.SemanticNode) bool {
	switch node.NormalizedType {
	case observe.NodeInstruction, observe.NodeMessage, observe.NodeText:
	case observe.NodeUnknown:
		// The client-entry parser keeps the whole client request as one node so
		// the raw payload stays available; declarations are read out of its
		// role-bearing objects below.
		if node.ProviderType != "client_request" {
			return false
		}
	default:
		return false
	}
	// A harness states its working directory in the prompt it builds, never in
	// something the model said. Restricting the roles is what stops an assistant
	// message that quotes an environment block (or a tool message that cat'ed one
	// out of a file) from being read as a declaration.
	switch strings.ToLower(strings.TrimSpace(node.Role)) {
	case "", "system", "developer", "user":
		return true
	default:
		return false
	}
}

// declarationTexts returns the texts of node that a declaration may live in.
func declarationTexts(node observe.SemanticNode) []string {
	if node.ProviderType == "client_request" {
		return jsonRoleTexts(node.Raw)
	}
	var texts []string
	if node.Text != "" {
		texts = append(texts, node.Text)
	}
	// A Claude Code <env> block is a user message whose content is a text part;
	// the message node carries the first part, the child carries each one.
	for _, child := range node.Children {
		if child.Text != "" {
			texts = append(texts, child.Text)
		}
	}
	return texts
}

// jsonRoleTexts returns the text of every role-bearing message object in one
// raw JSON payload, for the parsers that keep a whole request as one node.
//
// Only objects that carry a role and are not tool output are read, which is the
// same boundary declaresProject draws for structured nodes: the entry payload
// also holds tool results and tool arguments, and a path-shaped string in those
// is an operation on a file, not a statement about the project.
func jsonRoleTexts(raw []byte) []string {
	if len(raw) == 0 {
		return nil
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	var texts []string
	var walk func(value any)
	walk = func(value any) {
		switch typed := value.(type) {
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			role, _ := typed["role"].(string)
			switch strings.ToLower(strings.TrimSpace(role)) {
			case "system", "developer", "user":
				texts = append(texts, contentTexts(typed)...)
			}
			// `instructions` (Responses) and `system` (Anthropic) carry no role
			// of their own; they are prompt text by position.
			for _, key := range []string{"instructions", "system", "developer"} {
				if item, ok := typed[key]; ok {
					texts = append(texts, promptValueTexts(item)...)
				}
			}
			// Recurse so a role-bearing item nested under `input`/`messages`
			// still reaches the role check above.
			for key, item := range typed {
				switch key {
				case "role", "content", "text", "instructions", "system", "developer":
					continue
				}
				walk(item)
			}
		}
	}
	walk(value)
	return texts
}

// contentTexts returns the strings of a message object's content, whether the
// content is a string or a list of parts.
func contentTexts(message map[string]any) []string {
	var texts []string
	for _, key := range []string{"content", "text"} {
		item, ok := message[key]
		if !ok {
			continue
		}
		texts = append(texts, promptValueTexts(item)...)
	}
	return texts
}

// promptValueTexts returns the strings of a prompt value: a string, or a list
// of parts whose `text` field is the text.
func promptValueTexts(value any) []string {
	switch typed := value.(type) {
	case string:
		if typed == "" {
			return nil
		}
		return []string{typed}
	case []any:
		var texts []string
		for _, item := range typed {
			switch part := item.(type) {
			case string:
				if part != "" {
					texts = append(texts, part)
				}
			case map[string]any:
				if text, ok := part["text"].(string); ok && text != "" {
					texts = append(texts, text)
				}
			}
		}
		return texts
	case map[string]any:
		if text, ok := typed["text"].(string); ok && text != "" {
			return []string{text}
		}
	}
	return nil
}

// candidate is one raw declared value plus where it came from.
type candidate struct {
	value  string
	source Source
	offset int
}

// matchText returns the declarations in one text in appearance order.
//
// Each shape is only searched inside its own container. That containment is the
// whole safety argument: a path-shaped line in prose is not evidence, but a
// value inside a block the harness opens with a fixed tag is.
func matchText(text string) []candidate {
	if text == "" {
		return nil
	}
	var out []candidate
	out = append(out, matchDSH(text)...)
	out = append(out, matchClaude(text)...)
	out = append(out, matchCodex(text)...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].offset < out[j].offset })
	return out
}

// dshPrefix is the label the DeepSeek Harness runtime context uses. The value is
// JSON-quoted, which is why the pattern ends at the closing quote.
const dshPrefix = "Current working directory:"

func matchDSH(text string) []candidate {
	block := containerBody(text, "<system-reminder>", "</system-reminder>")
	if block == "" {
		return nil
	}
	var out []candidate
	for _, line := range strings.Split(block, "\n") {
		trimmed := strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(trimmed, dshPrefix)
		if !ok {
			continue
		}
		rest = strings.TrimSpace(rest)
		value, ok := quotedValue(rest)
		if !ok {
			continue
		}
		out = append(out, candidate{value: value, source: SourceDSHRuntimeContext, offset: strings.Index(text, line)})
	}
	return out
}

// claudePrefixes are the labels the Claude Code environment block has used.
var claudePrefixes = []string{"Primary working directory:", "Working directory:"}

func matchClaude(text string) []candidate {
	block := containerBody(text, "<env>", "</env>")
	if block == "" {
		return nil
	}
	var out []candidate
	for _, line := range strings.Split(block, "\n") {
		trimmed := strings.TrimSpace(line)
		for _, prefix := range claudePrefixes {
			rest, ok := strings.CutPrefix(trimmed, prefix)
			if !ok {
				continue
			}
			value := strings.TrimSpace(rest)
			if value == "" {
				continue
			}
			out = append(out, candidate{value: value, source: SourceClaudeEnv, offset: strings.Index(text, line)})
		}
	}
	return out
}

func matchCodex(text string) []candidate {
	block := containerBody(text, "<environment_context>", "</environment_context>")
	if block == "" {
		return nil
	}
	var out []candidate
	// <cwd> is the session's directory; <workspace_roots> is the broader set a
	// multi-root workspace was opened with. When both are present the cwd is the
	// more specific answer, so the roots are only a fallback.
	if value, ok := firstTagValue(block, "cwd"); ok {
		return append(out, candidate{value: value, source: SourceCodexEnv, offset: strings.Index(text, "<cwd>")})
	}
	if roots := containerBody(block, "<workspace_roots>", "</workspace_roots>"); roots != "" {
		if value, ok := firstTagValue(roots, "root"); ok {
			out = append(out, candidate{value: value, source: SourceCodexWorkspaceRoots, offset: strings.Index(text, "<workspace_roots>")})
		}
	}
	return out
}

// containerBody returns the text between open and close, or "" when the block
// is absent. The tags are literal protocol markers, never user text, so a plain
// search is the correct comparison.
func containerBody(text string, open string, close string) string {
	start := strings.Index(text, open)
	if start < 0 {
		return ""
	}
	start += len(open)
	end := strings.Index(text[start:], close)
	if end < 0 {
		return ""
	}
	return text[start : start+end]
}

// firstTagValue returns the trimmed text of the first <tag>...</tag>.
func firstTagValue(text string, tag string) (string, bool) {
	open := "<" + tag + ">"
	close := "</" + tag + ">"
	body := containerBody(text, open, close)
	body = strings.TrimSpace(body)
	if body == "" {
		return "", false
	}
	return body, true
}

// quotedValue extracts the content of a JSON-quoted string, accepting the raw
// (unescaped) form as a fallback so a harness that stops quoting still reads.
func quotedValue(text string) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}
	if strings.HasPrefix(text, `"`) {
		decoder := json.NewDecoder(strings.NewReader(text))
		decoder.UseNumber()
		var value string
		if err := decoder.Decode(&value); err == nil {
			return value, true
		}
		// A quote that did not close (a truncated body) falls through to the
		// plain form so a usable prefix is not thrown away.
		text = strings.TrimPrefix(text, `"`)
		text = strings.TrimSuffix(text, `"`)
	}
	return strings.TrimSpace(text), true
}

// systemRoots are directories that are never a project. They are compared
// against the normalised key, so the Windows entries are lower-case and use
// forward slashes. `/app` and `/workspace` are deliberately absent: they are
// legitimate project roots in a container.
var systemRoots = map[string]struct{}{
	"/":          {},
	"/bin":       {},
	"/boot":      {},
	"/dev":       {},
	"/etc":       {},
	"/home":      {},
	"/lib":       {},
	"/lib64":     {},
	"/opt":       {},
	"/private":   {},
	"/proc":      {},
	"/root":      {},
	"/run":       {},
	"/sbin":      {},
	"/sys":       {},
	"/tmp":       {},
	"/usr":       {},
	"/var":       {},
	"/Users":     {},
	"/Volumes":   {},
	"/users":     {},
	"/volumes":   {},
	"c:":         {},
	"c:/users":   {},
	"c:/windows": {},
	"d:":         {},
}

// Normalize validates one declared path and returns its display form and its
// comparison key.
//
// The validation is what keeps a placeholder or a system directory out of the
// index; it is not a guess about where the project "really" is. A value that is
// relative, templated ("/path/to/repo"), home-relative ("~/repo") or a system
// root yields ok=false, and the caller records no project.
func Normalize(path string) (root string, key string, ok bool) {
	value := strings.TrimSpace(path)
	value = strings.Trim(value, `"'`)
	if value == "" || len(value) > maxRootBytes {
		return "", "", false
	}
	if strings.ContainsAny(value, "<>{}$*?\x00") || strings.Contains(value, "…") {
		return "", "", false
	}
	lower := strings.ToLower(value)
	for _, fragment := range []string{"/path/to/", "/your/", "/your-project", "\\path\\to\\", "c:\\path\\"} {
		if strings.Contains(lower, fragment) {
			return "", "", false
		}
	}
	windows := isWindowsPath(value)
	switch {
	case strings.HasPrefix(value, `\\`):
		// A UNC root keeps its doubled prefix; every other doubled run in the
		// value is JSON escaping from a harness that serialised the path.
		value = `\\` + strings.ReplaceAll(value[2:], `\\`, `\`)
	case windows:
		value = strings.ReplaceAll(value, `\\`, `\`)
	}
	if !isAbsolutePath(value, windows) {
		return "", "", false
	}
	root = strings.TrimRight(value, `/\`)
	if root == "" {
		return "", "", false
	}
	if windows {
		key = strings.ToLower(strings.ReplaceAll(root, `\`, "/"))
	} else {
		key = root
	}
	if _, reserved := systemRoots[key]; reserved {
		return "", "", false
	}
	return root, key, true
}

// isWindowsPath reports whether value names a Windows drive or UNC path.
func isWindowsPath(value string) bool {
	if strings.HasPrefix(value, `\\`) {
		return true
	}
	return len(value) >= 3 && isASCIIAlpha(value[0]) && value[1] == ':' &&
		(value[2] == '\\' || value[2] == '/')
}

// isAbsolutePath reports whether value is rooted, which is the only form a
// declaration can be resolved to: a relative path needs the working directory
// to interpret, and that is exactly what the declaration is supposed to supply.
func isAbsolutePath(value string, windows bool) bool {
	if windows {
		if strings.HasPrefix(value, `\\`) {
			// \\server\share: both components are required.
			rest := strings.TrimPrefix(value, `\\`)
			host, tail, found := strings.Cut(rest, `\`)
			return found && host != "" && tail != ""
		}
		return len(value) >= 3 && isASCIIAlpha(value[0]) && value[1] == ':' &&
			(value[2] == '\\' || value[2] == '/')
	}
	return strings.HasPrefix(value, "/")
}

func isASCIIAlpha(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}
