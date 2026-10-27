package resources

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/kingfs/Trajecta/pkg/observe"
)

// The resource index answers "which files and URLs did the model ask to touch",
// not "which path-shaped strings did the payload contain". The two are very
// different: a model transcript is full of code, documentation and tool output,
// and a regular expression literal (/gpt-5/i), an import specifier
// ("../../lib/monitor") or a doc-link URL in prose all look exactly like a path
// or a URL to a lexical scanner.
//
// The legal source of an access is therefore a tool call the model produced:
// the arguments of a tool call are the only place a model states an operation on
// a resource. Tool results, message text, reasoning and code bodies are never
// scanned. The call is located through the Observation IR's response tree, which
// every protocol parser fills with a normalized tool-call node; a raw payload
// the entry parser keeps whole (the local Responses runtime's client-visible
// response) is read as the Responses API output array it is.

// toolCall is one model-produced call reduced to the parts the extractor needs.
//
// nodeID is the semantic node an occurrence is anchored to; key identifies the
// call itself, because a raw entry payload can carry several calls in one node.
// args is JSON argument text when the provider sent JSON, input is the free-form
// input of a custom tool, and command is a shell command vector
// (local_shell_call's action).
type toolCall struct {
	key      string
	nodeID   string
	nodePath string
	name     string
	kind     string
	args     string
	input    string
	command  []string
}

// modelToolCalls returns every tool call the model produced in this exchange, in
// output order. Calls the provider executed (web search, file search, code
// interpreter) are not accesses the client can attribute to a path or URL and
// are skipped.
func modelToolCalls(obs observe.TraceObservation) []toolCall {
	index := make(map[string]observe.ToolCallObservation, len(obs.Tools.Calls))
	for _, call := range obs.Tools.Calls {
		if call.NodeID != "" {
			index[call.NodeID] = call
		}
	}
	calls := make([]toolCall, 0, len(obs.Response.ToolCalls))
	seen := make(map[string]struct{}, len(obs.Response.ToolCalls))
	add := func(node observe.SemanticNode) {
		key := node.ID
		if key == "" {
			key = node.Path + "\x00" + node.ProviderType
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		if node.NormalizedType != observe.NodeToolCall {
			return
		}
		call, ok := toolCallFromNode(node, index[node.ID])
		if !ok {
			return
		}
		call.key = key
		calls = append(calls, call)
	}
	// The response tree is the primary source: every parser puts its tool-call
	// nodes in it, and walking it keeps the payload order. The two normalized
	// lists are read as well so a parser that only fills them is not missed.
	var walk func([]observe.SemanticNode)
	walk = func(nodes []observe.SemanticNode) {
		for _, node := range nodes {
			add(node)
			walk(node.Children)
		}
	}
	walk(obs.Response.Nodes)
	for _, node := range obs.Response.ToolCalls {
		add(node)
	}
	for _, node := range obs.Stream.AccumulatedToolCalls {
		add(node)
	}
	calls = append(calls, entryResponseToolCalls(obs)...)
	return calls
}

// toolCallFromNode reads one IR tool-call node. The normalized call observation
// already knows the argument text for every protocol parser, so it is preferred;
// the raw envelope is the fallback for the shapes the IR does not decode
// (custom_tool_call's free-form input and local_shell_call's command vector).
func toolCallFromNode(node observe.SemanticNode, call observe.ToolCallObservation) (toolCall, bool) {
	out := toolCall{
		nodeID:   node.ID,
		nodePath: node.Path,
		name:     firstNonEmptyText(metadataText(node.Metadata, "name"), call.Name, node.Text),
		kind:     firstNonEmptyText(call.Kind, node.ProviderType),
	}
	out.args = firstNonEmptyText(call.ArgsText, observe.NodeArguments(node))
	if out.args == "" {
		out.args = metadataText(node.Metadata, "input")
	}
	if out.args == "" {
		out.args = firstNonEmptyText(
			metadataText(node.Metadata, "functionCall"),
			metadataText(node.Metadata, "toolCall"),
		)
	}
	if obj, err := decodeJSONObject(node.Raw); err == nil {
		if out.args == "" {
			out.args = jsonStringField(obj, "arguments")
		}
		if out.input == "" {
			out.input = jsonStringField(obj, "input")
		}
		if fn, err := decodeJSONObject(obj["function"]); err == nil {
			if out.name == "" {
				out.name = jsonStringField(fn, "name")
			}
			if out.args == "" {
				out.args = jsonStringField(fn, "arguments")
			}
		}
		if len(out.command) == 0 {
			out.command = commandVector(obj["action"])
		}
		if len(out.command) == 0 {
			out.command = commandVector(obj["command"])
		}
	}
	if out.name == "" && out.kind == "" && out.args == "" && out.input == "" && len(out.command) == 0 {
		return toolCall{}, false
	}
	if out.args == "" && out.input == "" && len(out.command) == 0 {
		// A call with no arguments names no resource. Keeping it would only
		// add empty candidates.
		return toolCall{}, false
	}
	return out, true
}

// commandVector reads a command vector (or a single command string) out of a
// local_shell_call action. Both forms appear in the wild.
func commandVector(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var object struct {
		Command json.RawMessage `json:"command"`
	}
	if err := json.Unmarshal(raw, &object); err == nil && len(object.Command) > 0 {
		raw = object.Command
	}
	var vector []string
	if err := json.Unmarshal(raw, &vector); err == nil && len(vector) > 0 {
		return vector
	}
	var single string
	if err := json.Unmarshal(raw, &single); err == nil && single != "" {
		return []string{single}
	}
	return nil
}

// entryResponseToolCalls reads the calls out of the raw payload the entry parser
// keeps whole. That exchange is the client-visible side of the local Responses
// runtime: the request is history (already indexed by the trace that produced
// it) and only the response is a model output. The payload is a Responses API
// object, with a Chat Completions shape accepted for the translated path.
func entryResponseToolCalls(obs observe.TraceObservation) []toolCall {
	if obs.ExchangeKind != observeExchangeKindEntry {
		return nil
	}
	var out []toolCall
	for _, node := range obs.Response.Nodes {
		if len(node.Raw) == 0 {
			continue
		}
		obj, err := decodeJSONObject(node.Raw)
		if err != nil {
			continue
		}
		out = append(out, callsFromResponsesOutput(obj["output"], node)...)
		out = append(out, callsFromChatChoices(obj["choices"], node)...)
	}
	return out
}

// observeExchangeKindEntry is the exchange kind the entry parser owns.
const observeExchangeKindEntry = "entry"

// responsesCallTypes are the Responses API output item types that carry a
// client-executed tool call. Output items (function_call_output) are results and
// are deliberately absent.
var responsesCallTypes = map[string]bool{
	"function_call":    true,
	"custom_tool_call": true,
	"local_shell_call": true,
	"apply_patch":      true,
}

func callsFromResponsesOutput(raw json.RawMessage, node observe.SemanticNode) []toolCall {
	if len(raw) == 0 {
		return nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil
	}
	out := make([]toolCall, 0, len(items))
	for i, item := range items {
		obj, err := decodeJSONObject(item)
		if err != nil {
			continue
		}
		kind := jsonStringField(obj, "type")
		if !responsesCallTypes[kind] {
			continue
		}
		call := toolCall{
			key:      firstNonEmptyText(node.ID, node.Path) + "#output[" + strconv.Itoa(i) + "]",
			nodeID:   node.ID,
			nodePath: node.Path,
			name:     jsonStringField(obj, "name"),
			kind:     kind,
			args:     rawFieldText(obj, "arguments"),
			input:    rawFieldText(obj, "input"),
			command:  commandVector(obj["action"]),
		}
		if call.name == "" {
			call.name = kind
		}
		if call.args == "" && call.input == "" && len(call.command) == 0 {
			continue
		}
		out = append(out, call)
	}
	return out
}

func callsFromChatChoices(raw json.RawMessage, node observe.SemanticNode) []toolCall {
	if len(raw) == 0 {
		return nil
	}
	var choices []json.RawMessage
	if err := json.Unmarshal(raw, &choices); err != nil {
		return nil
	}
	var out []toolCall
	for i, choiceRaw := range choices {
		choice, err := decodeJSONObject(choiceRaw)
		if err != nil {
			continue
		}
		message, err := decodeJSONObject(choice["message"])
		if err != nil {
			continue
		}
		var toolCalls []json.RawMessage
		if err := json.Unmarshal(message["tool_calls"], &toolCalls); err != nil {
			continue
		}
		for j, callRaw := range toolCalls {
			callObj, err := decodeJSONObject(callRaw)
			if err != nil {
				continue
			}
			fn, err := decodeJSONObject(callObj["function"])
			if err != nil {
				continue
			}
			call := toolCall{
				key:      firstNonEmptyText(node.ID, node.Path) + "#choices[" + strconv.Itoa(i) + "].tool_calls[" + strconv.Itoa(j) + "]",
				nodeID:   node.ID,
				nodePath: node.Path,
				name:     jsonStringField(fn, "name"),
				kind:     "tool_call",
				args:     jsonStringField(fn, "arguments"),
			}
			if call.args == "" && call.name == "" {
				continue
			}
			out = append(out, call)
		}
	}
	return out
}

// forEachCallCandidate visits every argument token of call that may name an
// access. It is the structural half of the extractor: forEachCallCandidate
// decides *which tokens are operations* (a URL argument, a command operand, a
// path field), and validateCandidate decides *whether the token really is a
// path or a URL*.
func forEachCallCandidate(call toolCall, yield func(mode, token, view string, start, end int)) {
	emit := func(mode, token string, start, end int) {
		yield(mode, token, viewArguments, start, end)
	}
	if len(call.command) > 0 {
		shellCandidates(joinCommandVector(call.command), emit)
		return
	}
	if call.args != "" {
		if walkArguments(call.args, emit) {
			return
		}
		// Arguments that are not JSON are a free-form tool input: a shell
		// script, a patch, or a single token.
		freeFormCandidates(call, call.args, emit)
		return
	}
	if call.input != "" {
		freeFormCandidates(call, call.input, emit)
	}
}

// freeFormCandidates handles the arguments of a tool that does not take a JSON
// object. Only shapes whose operation is unambiguous are read: a shell script,
// a patch header, or a value that is exactly one path/URL token.
func freeFormCandidates(call toolCall, text string, emit func(mode, token string, start, end int)) {
	if looksLikeShellTool(call.name) || call.kind == "local_shell_call" {
		shellCandidates(text, emit)
		return
	}
	if looksLikePatchTool(call.name) || call.kind == "apply_patch" {
		forEachPatchPath(text, emit)
		return
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || len(trimmed) != len(text) {
		return
	}
	if strings.ContainsAny(trimmed, " \t\r\n") {
		return
	}
	emit(modeStrictPath, trimmed, 0, len(trimmed))
}

// forEachPatchPath reads the file headers of an apply_patch payload. A patch is
// a real set of file operations, and the header is the only structured place
// the paths appear.
func forEachPatchPath(text string, emit func(mode, token string, start, end int)) {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		for _, prefix := range []string{"*** Add File: ", "*** Update File: ", "*** Delete File: ", "*** Move to: "} {
			if !strings.HasPrefix(line, prefix) {
				continue
			}
			path := strings.TrimSpace(strings.TrimPrefix(line, prefix))
			if path != "" {
				emit("", path, 0, len(path))
			}
		}
	}
}

// looksLikeShellTool reports whether a tool name names a shell. The check is on
// word boundaries so "truncate" is not read as "run" and "search" is not read as
// "sh".
func looksLikeShellTool(name string) bool {
	for _, word := range splitToolName(name) {
		switch word {
		case "bash", "shell", "sh", "zsh", "dash", "ksh", "fish", "pwsh", "powershell",
			"exec", "execute", "exec_command", "command", "cmd", "terminal", "console",
			"run", "local_shell":
			return true
		}
	}
	return false
}

// looksLikePatchTool reports whether a tool name names a patch editor.
func looksLikePatchTool(name string) bool {
	for _, word := range splitToolName(name) {
		switch word {
		case "patch", "apply_patch", "diff":
			return true
		}
	}
	return false
}

// splitToolName splits a tool name into lower-case words on punctuation and
// camel-case boundaries.
func splitToolName(name string) []string {
	var words []string
	var current strings.Builder
	flush := func() {
		if current.Len() > 0 {
			words = append(words, strings.ToLower(current.String()))
			current.Reset()
		}
	}
	runes := []rune(name)
	for i, r := range runes {
		switch {
		case r == '_' || r == '-' || r == '.' || r == ' ' || r == '/' || r == ':':
			flush()
		case r >= 'A' && r <= 'Z':
			if i > 0 && runes[i-1] >= 'a' && runes[i-1] <= 'z' {
				flush()
			}
			current.WriteRune(r)
		default:
			current.WriteRune(r)
		}
	}
	flush()
	return words
}

// --- small JSON helpers -------------------------------------------------

func decodeJSONObject(raw []byte) (map[string]json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, errNotObject
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(raw, &object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, errNotObject
	}
	return object, nil
}

var (
	// errNotObject reports that a payload is not a JSON object.
	errNotObject = errors.New("resources: payload is not a JSON object")
	// errTooDeep bounds the argument walk; a deeper payload is not a tool
	// argument structure either.
	errTooDeep = errors.New("resources: arguments nested too deeply")
)

func jsonStringField(object map[string]json.RawMessage, key string) string {
	raw, ok := object[key]
	if !ok || len(raw) == 0 {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return ""
	}
	return value
}

// rawFieldText renders a JSON field as the text a detector should read: a string
// is returned as-is, an object or array is kept as its JSON text.
func rawFieldText(object map[string]json.RawMessage, key string) string {
	raw, ok := object[key]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var value string
	if err := json.Unmarshal(raw, &value); err == nil {
		return value
	}
	return string(raw)
}

func metadataText(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case json.RawMessage:
		return string(typed)
	case []byte:
		return string(typed)
	default:
		raw, err := json.Marshal(typed)
		if err != nil {
			return ""
		}
		return string(raw)
	}
}

func firstNonEmptyText(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
