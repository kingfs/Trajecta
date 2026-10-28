package resources

import (
	"strings"
	"testing"

	"github.com/kingfs/Trajecta/pkg/observe"
)

// callNode builds the IR shape every protocol parser produces for a model tool
// call: a response-side node whose arguments are provider JSON text.
func callNode(id, name, args string) observe.SemanticNode {
	return observe.SemanticNode{
		ID:             id,
		ProviderType:   "function_call",
		NormalizedType: observe.NodeToolCall,
		Path:           "$.output[" + id + "]",
		Text:           name,
		Metadata: map[string]any{
			"name":      name,
			"arguments": args,
		},
	}
}

func observationOf(nodes ...observe.SemanticNode) observe.TraceObservation {
	return observe.TraceObservation{
		Response: observe.ObservationResponse{ToolCalls: nodes, Nodes: nodes},
	}
}

func extractedValues(obs observe.TraceObservation) map[string]Access {
	values := make(map[string]Access, 8)
	for _, access := range Extract(obs) {
		values[access.Value] = access
	}
	return values
}

// TestExtractReadsToolCallArguments pins the positive half: the locations a
// model's file and network tools name are the resources of the trace.
func TestExtractReadsToolCallArguments(t *testing.T) {
	obs := observationOf(
		callNode("read", "read_file", `{"file_path":"/home/alice/project/main.go"}`),
		callNode("fetch", "web_fetch", `{"url":"https://api.example.com/repos/1?page=2","prompt":"summarize"}`),
		callNode("list", "ls", `{"path":"/project/src"}`),
	)
	got := Extract(obs)
	if len(got) != 3 {
		t.Fatalf("extracted %+v, want three resources", got)
	}
	if got[0].Kind != KindLocalPath || got[0].Value != "/home/alice/project/main.go" {
		t.Errorf("first access = %+v", got[0])
	}
	if got[1].Kind != KindURL || got[1].Value != "https://api.example.com/repos/1?page=2" {
		t.Errorf("second access = %+v", got[1])
	}
	if got[2].Value != "/project/src" {
		t.Errorf("third access = %+v", got[2])
	}
	for index, access := range got {
		if access.Position != index || access.Count != 1 {
			t.Errorf("access %d = %+v, want position %d and count 1", index, access, index)
		}
	}
}

// TestExtractIgnoresTextAndToolResults is the scope contract, and the reason
// the index stopped reporting regex literals (/gpt-5/i), import specifiers
// ("../../lib/monitor") and documentation URLs: none of them is a tool call.
func TestExtractIgnoresTextAndToolResults(t *testing.T) {
	obs := observe.TraceObservation{
		Request: observe.ObservationRequest{
			Nodes: []observe.SemanticNode{{
				ID:   "prompt",
				Text: `read /home/alice/project/main.go and fetch https://docs.example.com/guide`,
			}},
		},
		Response: observe.ObservationResponse{
			Outputs: []observe.SemanticNode{{
				ID:   "answer",
				Text: "the import comes from ../../lib/monitor and the flag is /gpt-5/i",
			}},
			ToolResults: []observe.SemanticNode{{
				ID:             "result",
				NormalizedType: observe.NodeToolResult,
				Text:           "cat /etc/passwd\nhttps://internal.example.com:8443/a/b?q=1#f",
			}},
			Nodes: []observe.SemanticNode{
				{ID: "prompt", Text: "see /tmp/secret.txt"},
				{ID: "result", NormalizedType: observe.NodeToolResult, Text: "see /tmp/secret.txt"},
				{ID: "answer", Text: "see /tmp/secret.txt"},
			},
		},
	}
	if got := Extract(obs); len(got) != 0 {
		t.Fatalf("extracted %+v from text and tool results, want none", got)
	}
}

// TestExtractIgnoresContentAndPatternArguments covers the code shapes the old
// full-text scan reported: an Edit tool's old_string carries an import
// specifier, a grep tool's pattern is a regular expression, and neither is a
// file access. The file_path beside them still is.
func TestExtractIgnoresContentAndPatternArguments(t *testing.T) {
	obs := observationOf(
		callNode("edit", "edit", `{"file_path":"/repo/lib/utils.ts","old_string":"import { formatDateTime, normalizeAnalyticsWindow, setOrDeleteParam } from \"../../lib/monitor\";","new_string":"import { formatDateTime, normalizeAnalyticsWindow, setOrDeleteParam } from \"../../lib/monitor\";"}`),
		callNode("grep", "grep", `{"pattern":"/gpt-5/i","path":"/repo/src"}`),
		callNode("write", "write", `{"file_path":"/repo/src/regex.js","content":"const pattern = /gpt-5/i;"}`),
	)
	got := extractedValues(obs)
	for _, want := range []string{"/repo/lib/utils.ts", "/repo/src", "/repo/src/regex.js"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q from %+v", want, got)
		}
	}
	for _, unwanted := range []string{"../../lib/monitor", "/gpt-5/i"} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("indexed %q from code content", unwanted)
		}
	}
	if len(got) != 3 {
		t.Errorf("extracted %+v, want exactly the three file operations", got)
	}
}

// TestExtractReadsShellOperands pins the command half: a shell tool names files
// and URLs by operand position, not by field name.
func TestExtractReadsShellOperands(t *testing.T) {
	obs := observationOf(callNode("bash", "Bash", `{"command":"cat /etc/hosts src/main.go && curl -s https://api.example.com/v1/x -o /tmp/out.json","description":"inspect"}`))
	got := extractedValues(obs)
	for _, want := range []string{"/etc/hosts", "src/main.go", "https://api.example.com/v1/x", "/tmp/out.json"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q from %+v", want, got)
		}
	}
	if len(got) != 4 {
		t.Errorf("extracted %+v, want four operands", got)
	}
}

// TestExtractIgnoresShellContent keeps the regex/import/echo shapes out when
// they reach a command: a pattern operand, interpreter inline code and a
// message are not paths.
func TestExtractIgnoresShellContent(t *testing.T) {
	cases := []string{
		"grep -E '/gpt-5/i' /repo/src/main.go",
		"rg '/gpt-5/i' /repo/src",
		"git grep '/gpt-5/i'",
		"python3 -c 'print(\"/gpt-5/i\")'",
		"node -e 'console.log(require(\"../../lib/monitor\"))'",
		"echo /gpt-5/i",
		"printf 'import x from \"../../lib/monitor\"'",
		"sed -n 's#/a/b#x#' /repo/data.txt",
		"jq '.a/b' /repo/data.json",
		"git commit -m '/gpt-5/i'",
	}
	for _, command := range cases {
		args := `{"command":` + quoteJSON(command) + `}`
		got := extractedValues(observationOf(callNode("bash", "Bash", args)))
		for value := range got {
			if value == "/gpt-5/i" || value == "../../lib/monitor" || value == "/a/b" {
				t.Errorf("%q indexed content %q", command, value)
			}
		}
	}
}

// TestExtractKeepsShellRealPaths is the other half of the command rules: the
// file beside a pattern, the script a shell runs, and a directory a command
// changes to are still accesses.
func TestExtractKeepsShellRealPaths(t *testing.T) {
	cases := map[string][]string{
		"grep -E '/gpt-5/i' /repo/src/main.go": {"/repo/src/main.go"},
		"sed -n '1,10p' /repo/data.txt":        {"/repo/data.txt"},
		"bash -c 'cat /etc/hosts'":             {"/etc/hosts"},
		"cd ../../lib/monitor && ls .":         {"../../lib/monitor"},
		"./scripts/build.sh --out /tmp/a.bin":  {"./scripts/build.sh", "/tmp/a.bin"},
		"git clone https://github.com/x/y.git": {"https://github.com/x/y.git"},
	}
	for command, want := range cases {
		got := extractedValues(observationOf(callNode("bash", "Bash", `{"command":`+quoteJSON(command)+`}`)))
		for _, value := range want {
			if _, ok := got[value]; !ok {
				t.Errorf("%q extracted %+v, missing %q", command, got, value)
			}
		}
	}
}

// TestExtractIgnoresGitRefs pins the git operand roles: a branch or remote ref
// has the shape of a relative path, so the command context, not the DFA, has
// to keep it out of the index.
func TestExtractIgnoresGitRefs(t *testing.T) {
	cases := []struct {
		command  string
		unwanted []string
	}{
		{"git log --oneline -3 origin/main", []string{"origin/main"}},
		{"git diff origin/main -- src/main.go", []string{"origin/main"}},
		{"git checkout -b feat/agentbuilder-43-background-upgrade main", []string{"feat/agentbuilder-43-background-upgrade", "main"}},
		{"git merge origin/main", []string{"origin/main"}},
		{"git branch feat/agentbuilder-43-background-upgrade", []string{"feat/agentbuilder-43-background-upgrade"}},
		{"git push origin main", []string{"origin/main", "main"}},
		{"git reset --hard origin/main", []string{"origin/main"}},
		{"git switch feat/agentbuilder-43-background-upgrade", []string{"feat/agentbuilder-43-background-upgrade"}},
		{"git log --author=agentbuilder/43 origin/main", []string{"agentbuilder/43", "origin/main"}},
		{"git commit -am origin/main", []string{"origin/main"}},
		{"git log --source origin/main", []string{"origin/main"}},
		{"git log --max-count=3 origin/main", []string{"origin/main"}},
		{"git ls-files --exclude='agentbuilder/43'", []string{"agentbuilder/43"}},
	}
	for _, tc := range cases {
		got := extractedValues(observationOf(callNode("bash", "Bash", `{"command":`+quoteJSON(tc.command)+`}`)))
		for _, value := range tc.unwanted {
			if _, ok := got[value]; ok {
				t.Errorf("%q extracted %+v, want %q excluded", tc.command, got, value)
			}
		}
	}
}

// TestExtractKeepsGitPaths is the other half of the git rules: real paths in a
// path operand position stay, including the working tree a subcommand creates
// and the pathspec after `--`.
func TestExtractKeepsGitPaths(t *testing.T) {
	cases := map[string][]string{
		"git worktree add .worktrees/agentbuilder-43-background-upgrade -b feat/agentbuilder-43-background-upgrade": {".worktrees/agentbuilder-43-background-upgrade"},
		"git worktree add -b feat/agentbuilder-43-background-upgrade .worktrees/agentbuilder-43-background-upgrade": {".worktrees/agentbuilder-43-background-upgrade"},
		"git worktree remove .worktrees/agentbuilder-43-background-upgrade":                                         {".worktrees/agentbuilder-43-background-upgrade"},
		"git worktree move .worktrees/old .worktrees/new":                                                           {".worktrees/old", ".worktrees/new"},
		"git diff origin/main -- src/main.go":                                                                       {"src/main.go"},
		"git checkout main -- web/dist/learn":                                                                       {"web/dist/learn"},
		"git checkout -- web/dist/learn":                                                                            {"web/dist/learn"},
		"git add web/dist/learn":                                                                                    {"web/dist/learn"},
		"git grep -e 'origin/main' -- web/dist/learn":                                                               {"web/dist/learn"},
		"git grep origin/main web/dist/learn":                                                                       {"web/dist/learn"},
		"git clone https://github.com/x/y.git web/dist/learn":                                                       {"https://github.com/x/y.git", "web/dist/learn"},
		"git -C /repo log origin/main":                                                                              {"/repo"},
		"git commit -m 'update origin/main' -- src/main.go":                                                         {"src/main.go"},
		"git submodule add https://github.com/x/y.git web/dist/learn":                                               {"https://github.com/x/y.git", "web/dist/learn"},
		"git status web/dist/learn":                                                                                 {"web/dist/learn"},
		"git blame src/main.go":                                                                                     {"src/main.go"},
		"git diff --no-index web/dist/learn src/main.go":                                                            {"web/dist/learn", "src/main.go"},
		"git config --file .git/config core.editor true":                                                            {".git/config"},
		"git commit -F .git/COMMIT_EDITMSG":                                                                         {".git/COMMIT_EDITMSG"},
	}
	for command, want := range cases {
		got := extractedValues(observationOf(callNode("bash", "Bash", `{"command":`+quoteJSON(command)+`}`)))
		for _, value := range want {
			if _, ok := got[value]; !ok {
				t.Errorf("%q extracted %+v, missing %q", command, got, value)
			}
		}
	}
}

// TestExtractIgnoresShellPlumbing keeps redirect sinks out: /dev/null is in
// nearly every command and is not a file anyone read.
func TestExtractIgnoresShellPlumbing(t *testing.T) {
	obs := observationOf(callNode("bash", "Bash", `{"command":"make build > /dev/null 2>&1"}`))
	if got := Extract(obs); len(got) != 0 {
		t.Fatalf("extracted %+v from a device sink, want none", got)
	}
}

// TestExtractReadsPatchHeaders covers a free-form apply_patch input: the paths
// are in the patch headers, and the patch body is not scanned.
func TestExtractReadsPatchHeaders(t *testing.T) {
	patch := "*** Begin Patch\n*** Update File: src/main.go\n@@\n-import x from '../../lib/monitor'\n+import y\n*** Add File: src/new_file.ts\n*** End Patch"
	node := observe.SemanticNode{
		ID:             "patch",
		ProviderType:   "custom_tool_call",
		NormalizedType: observe.NodeToolCall,
		Path:           "$.output[0]",
		Metadata: map[string]any{
			"name":  "apply_patch",
			"input": patch,
		},
	}
	got := extractedValues(observationOf(node))
	if len(got) != 2 {
		t.Fatalf("extracted %+v, want two patch paths", got)
	}
	for _, want := range []string{"src/main.go", "src/new_file.ts"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q from %+v", want, got)
		}
	}
	if _, ok := got["../../lib/monitor"]; ok {
		t.Errorf("indexed the patch body: %+v", got)
	}
}

// TestExtractReadsLocalShellCommandVector covers the Responses API
// local_shell_call shape: the command is a vector, not a JSON string.
func TestExtractReadsLocalShellCommandVector(t *testing.T) {
	node := observe.SemanticNode{
		ID:             "shell",
		ProviderType:   "local_shell_call",
		NormalizedType: observe.NodeToolCall,
		Path:           "$.output[0]",
		Metadata:       map[string]any{"name": "local_shell"},
		Raw:            []byte(`{"type":"local_shell_call","action":{"type":"exec","command":["bash","-lc","cat /etc/hosts && echo /gpt-5/i"]}}`),
	}
	got := extractedValues(observationOf(node))
	if len(got) != 1 {
		t.Fatalf("extracted %+v, want one path", got)
	}
	if _, ok := got["/etc/hosts"]; !ok {
		t.Errorf("missing /etc/hosts from %+v", got)
	}
}

// TestExtractReadsEntryRawOutput covers the entry parser: the client-visible
// response of the local Responses runtime is kept whole, so the model's calls
// are read out of the Responses API output array.
func TestExtractReadsEntryRawOutput(t *testing.T) {
	raw := `{"id":"resp_1","output":[{"type":"function_call","name":"read","arguments":"{\"file_path\":\"/data/src/project/README.md\"}"},{"type":"function_call_output","call_id":"c1","output":"see https://docs.example.com/guide"}]}`
	node := observe.SemanticNode{
		ID:             "entry-response",
		ProviderType:   "client_response",
		NormalizedType: observe.NodeUnknown,
		Path:           "$",
		Raw:            []byte(raw),
	}
	obs := observe.TraceObservation{
		ExchangeKind: "entry",
		Response:     observe.ObservationResponse{Nodes: []observe.SemanticNode{node}},
	}
	got := Extract(obs)
	if len(got) != 1 || got[0].Value != "/data/src/project/README.md" {
		t.Fatalf("extracted %+v, want the function_call path only", got)
	}
}

// TestExtractReadsEntryRequestHistory covers the entry request: the client
// carries the assistant history back, and a call in it is still a call the
// model produced. The entry parser keeps the request whole, so the extractor
// reads the raw messages instead of the IR request tree.
func TestExtractReadsEntryRequestHistory(t *testing.T) {
	raw := `{"messages":[{"role":"assistant","tool_calls":[{"id":"call-old","function":{"name":"read","arguments":"{\"file_path\":\"/old/history.txt\"}"}}]}]}`
	node := observe.SemanticNode{ID: "entry-request", NormalizedType: observe.NodeUnknown, Raw: []byte(raw)}
	obs := observe.TraceObservation{
		ExchangeKind: "entry",
		Request:      observe.ObservationRequest{Nodes: []observe.SemanticNode{node}},
	}
	got := Extract(obs)
	if len(got) != 1 || got[0].Kind != KindLocalPath || got[0].Value != "/old/history.txt" {
		t.Fatalf("extracted %+v, want the request history path", got)
	}
}

// TestExtractReadsEntryAnthropicHistory covers the same entry request in the
// Anthropic shape: a tool_use content block carries the input object.
func TestExtractReadsEntryAnthropicHistory(t *testing.T) {
	raw := `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":"toolu_old","name":"Bash","input":{"command":"ls web/dist/learn"}}]}]}`
	node := observe.SemanticNode{ID: "entry-request", NormalizedType: observe.NodeUnknown, Raw: []byte(raw)}
	obs := observe.TraceObservation{
		ExchangeKind: "entry",
		Request:      observe.ObservationRequest{Nodes: []observe.SemanticNode{node}},
	}
	got := Extract(obs)
	if len(got) != 1 || got[0].Kind != KindLocalPath || got[0].Value != "web/dist/learn" {
		t.Fatalf("extracted %+v, want the Anthropic history directory", got)
	}
}

// TestExtractReadsEntryResponsesInputHistory covers the Responses API entry
// request: the client sends the earlier function_call items in `input`.
func TestExtractReadsEntryResponsesInputHistory(t *testing.T) {
	raw := `{"input":[{"type":"function_call","call_id":"call_old","name":"Bash","arguments":"{\"command\":\"ls web/dist/learn\"}"},{"type":"function_call_output","call_id":"call_old","output":"ok"}]}`
	node := observe.SemanticNode{ID: "entry-request", NormalizedType: observe.NodeUnknown, Raw: []byte(raw)}
	obs := observe.TraceObservation{
		ExchangeKind: "entry",
		Request:      observe.ObservationRequest{Nodes: []observe.SemanticNode{node}},
	}
	got := Extract(obs)
	if len(got) != 1 || got[0].Kind != KindLocalPath || got[0].Value != "web/dist/learn" {
		t.Fatalf("extracted %+v, want the Responses input history directory", got)
	}
}

// TestExtractReadsRequestToolCalls covers a model exchange whose request
// carries the earlier assistant turn: the call nodes are in the request tree,
// and the paths they name belong to this exchange's resource list.
func TestExtractReadsRequestToolCalls(t *testing.T) {
	obs := observe.TraceObservation{
		Request: observe.ObservationRequest{Nodes: []observe.SemanticNode{
			callNode("history", "Bash", `{"command":"cd /Users/kingfs/go/src/github.com/kingfs/Linear-Algebra-to-LLM && ls web/dist/learn"}`),
		}},
		Response: observe.ObservationResponse{ToolCalls: []observe.SemanticNode{
			callNode("current", "web_fetch", `{"url":"https://www.yongzhen.wang/Linear-Algebra-to-LLM/figures/basis-dimension.svg"}`),
		}},
	}
	got := extractedValues(obs)
	for _, want := range []string{
		"/Users/kingfs/go/src/github.com/kingfs/Linear-Algebra-to-LLM",
		"web/dist/learn",
		"https://www.yongzhen.wang/Linear-Algebra-to-LLM/figures/basis-dimension.svg",
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q from %+v", want, got)
		}
	}
	if access := got["web/dist/learn"]; access.Kind != KindLocalPath {
		t.Errorf("web/dist/learn kind = %q, want %q", access.Kind, KindLocalPath)
	}
	if len(got) != 3 {
		t.Errorf("extracted %+v, want three resources", got)
	}
}

// TestExtractDedupesACallCarriedOnBothSides keeps one provider call id from
// being counted twice when a response echoes a request-history call.
func TestExtractDedupesACallCarriedOnBothSides(t *testing.T) {
	requestCall := callNode("request-call", "read_file", `{"file_path":"/repo/main.go"}`)
	requestCall.Metadata["id"] = "call-1"
	responseCall := callNode("response-call", "read_file", `{"file_path":"/repo/main.go"}`)
	responseCall.Metadata["id"] = "call-1"
	obs := observe.TraceObservation{
		Request:  observe.ObservationRequest{Nodes: []observe.SemanticNode{requestCall}},
		Response: observe.ObservationResponse{ToolCalls: []observe.SemanticNode{responseCall}},
	}
	got := extractedValues(obs)
	access, ok := got["/repo/main.go"]
	if !ok || access.Count != 1 {
		t.Fatalf("extracted %+v, want one access for the shared call id", got)
	}
}

// TestExtractReadsRelativeDirectoryOperands covers the directory half of the
// shell rule: `ls` and `find` name a directory without a file extension, and a
// path-valued argument does too.
func TestExtractReadsRelativeDirectoryOperands(t *testing.T) {
	command := "cd /Users/kingfs/go/src/github.com/kingfs/Linear-Algebra-to-LLM && ls web/dist/learn | head && find web/dist/learn -maxdepth 2 -name index.html | head -20"
	obs := observationOf(
		callNode("bash", "Bash", `{"command":`+quoteJSON(command)+`}`),
		callNode("list", "list_directory", `{"path":"web/dist/learn"}`),
	)
	got := extractedValues(obs)
	access, ok := got["web/dist/learn"]
	if !ok {
		t.Fatalf("missing the relative directory from %+v", got)
	}
	if access.Kind != KindLocalPath || access.Count != 2 {
		t.Errorf("web/dist/learn = %+v, want 2 accesses (the Bash call and the path field)", access)
	}
	if _, ok := got["/Users/kingfs/go/src/github.com/kingfs/Linear-Algebra-to-LLM"]; !ok {
		t.Errorf("missing the absolute directory from %+v", got)
	}
}

// TestExtractIgnoresDeviceOperands covers a device sink that arrives as an
// option value rather than a redirection: `curl -o /dev/null` writes to a
// sink, which is plumbing and not a resource.
func TestExtractIgnoresDeviceOperands(t *testing.T) {
	obs := observationOf(callNode("bash", "Bash", `{"command":"curl -k -L -sS --max-time 20 -o /dev/null -w '%{http_code}' https://example.com/a.svg"}`))
	got := extractedValues(obs)
	if _, ok := got["/dev/null"]; ok {
		t.Fatalf("indexed a device sink: %+v", got)
	}
	if _, ok := got["https://example.com/a.svg"]; !ok {
		t.Fatalf("missing the URL from %+v", got)
	}
}

// TestExtractReadsWindowsArguments covers the escaped form a tool call's
// arguments carry.
func TestExtractReadsWindowsArguments(t *testing.T) {
	obs := observationOf(callNode("read", "read_file", `{"file_path":"C:\\\\Users\\\\alice\\\\project\\\\main.go"}`))
	got := Extract(obs)
	if len(got) != 1 {
		t.Fatalf("extracted %+v, want one path", got)
	}
	if got[0].Kind != KindLocalPath || got[0].Value != `C:\Users\alice\project\main.go` {
		t.Fatalf("path = %+v, want the decoded Windows path", got[0])
	}
}

// TestExtractSkipsPathologicallyLongValues keeps one pathological argument out
// of the index rather than truncating it into a colliding value.
func TestExtractSkipsPathologicallyLongValues(t *testing.T) {
	long := "https://example.com/" + strings.Repeat("a", maxValueBytes)
	obs := observationOf(callNode("fetch", "web_fetch", `{"url":`+quoteJSON(long)+`}`))
	if got := Extract(obs); len(got) != 0 {
		t.Fatalf("extracted an over-long value: %+v", got)
	}
}

// TestExtractKeepsAURLThatCarriesACredential pins the containment rule: the
// URL is the access being audited and starts before the key, so it is kept even
// though a credential span sits inside it.
func TestExtractKeepsAURLThatCarriesACredential(t *testing.T) {
	key := "sk-" + "A" + "AAAAAAAAAAAAAAAAAAAT3BlbkFJ" + "BBBBBBBBBBBBBBBBBBBB"
	url := "https://api.example.com/v1?key=" + key
	obs := observationOf(callNode("fetch", "web_fetch", `{"url":`+quoteJSON(url)+`}`))
	got := Extract(obs)
	if len(got) != 1 || got[0].Kind != KindURL || got[0].Value != url {
		t.Fatalf("extracted %+v, want the URL that carries the key", got)
	}
}

// TestExtractIgnoresServerToolCalls keeps provider-executed calls out: a
// web_search action is not a client resource operation.
func TestExtractIgnoresServerToolCalls(t *testing.T) {
	node := observe.SemanticNode{
		ID:             "search",
		ProviderType:   "web_search_call",
		NormalizedType: observe.NodeServerToolCall,
		Path:           "$.output[0]",
		Metadata:       map[string]any{"name": "web_search"},
		Raw:            []byte(`{"type":"web_search_call","action":{"type":"search","query":"https://internal.example.com:8443/a/b?q=1#f"}}`),
	}
	obs := observe.TraceObservation{Response: observe.ObservationResponse{ToolCalls: []observe.SemanticNode{node}, Nodes: []observe.SemanticNode{node}}}
	if got := Extract(obs); len(got) != 0 {
		t.Fatalf("extracted %+v from a server tool call, want none", got)
	}
}

// TestExtractCountsCallsNotArguments pins count semantics: two calls that name
// the same file are two accesses, two arguments in one call are one.
func TestExtractCountsCallsNotArguments(t *testing.T) {
	obs := observationOf(
		callNode("one", "read_file", `{"file_path":"/repo/main.go","path":"/repo/main.go"}`),
		callNode("two", "read_file", `{"file_path":"/repo/main.go"}`),
	)
	got := extractedValues(obs)
	access, ok := got["/repo/main.go"]
	if !ok {
		t.Fatalf("missing /repo/main.go from %+v", got)
	}
	if access.Count != 2 {
		t.Errorf("count = %d, want 2 tool calls", access.Count)
	}
}

// TestExtractIgnoresUnknownText keeps a field that is not a location and is not
// a single token from being read as one: free-form prose is not an argument.
func TestExtractIgnoresUnknownText(t *testing.T) {
	obs := observationOf(callNode("tool", "mcp_tool", `{"note":"the file lives at /repo/main.go maybe","title":"src/main.go"}`))
	got := extractedValues(obs)
	if _, ok := got["/repo/main.go"]; ok {
		t.Errorf("indexed a path from prose: %+v", got)
	}
}

// TestLocateFindsTheCallNode pins the jump target the resource tree uses: the
// value is searched through the same argument views Extract indexed, and the
// result names the call's node and the span inside the decoded arguments.
func TestLocateFindsTheCallNode(t *testing.T) {
	obs := observationOf(
		callNode("call-one", "read_file", `{"file_path":"/home/alice/project/main.go"}`),
		callNode("call-two", "web_fetch", `{"url":"https://api.example.com/docs?page=2"}`),
	)
	cases := []struct {
		kind  string
		value string
		node  string
	}{
		{kind: KindLocalPath, value: "/home/alice/project/main.go", node: "call-one"},
		{kind: KindURL, value: "https://api.example.com/docs?page=2", node: "call-two"},
	}
	for _, tc := range cases {
		got := Locate(obs, tc.kind, tc.value)
		if len(got) != 1 {
			t.Fatalf("Locate(%q) = %+v, want one occurrence", tc.value, got)
		}
		if got[0].NodeID != tc.node || got[0].View != viewArguments || got[0].End <= got[0].Start {
			t.Errorf("Locate(%q) = %+v, want node %q in the arguments view", tc.value, got[0], tc.node)
		}
		if got[0].NodePath == "" {
			t.Errorf("Locate(%q) has no node path: %+v", tc.value, got[0])
		}
	}
	for _, value := range []string{"", "/repo/not-there.go", "/gpt-5/i"} {
		if got := Locate(obs, KindLocalPath, value); len(got) != 0 {
			t.Errorf("Locate(%q) = %+v, want none", value, got)
		}
	}
	if got := Locate(obs, "other", "/home/alice/project/main.go"); len(got) != 0 {
		t.Errorf("Locate with an unknown kind = %+v, want none", got)
	}
}

// TestLocateReportsEachCallOnce keeps the jump list short when the same value
// appears in several arguments of one call.
func TestLocateReportsEachCallOnce(t *testing.T) {
	obs := observationOf(callNode("call-both", "edit", `{"file_path":"/home/alice/project/main.go","old_string":"open /home/alice/project/main.go"}`))
	got := Locate(obs, KindLocalPath, "/home/alice/project/main.go")
	if len(got) != 1 {
		t.Fatalf("Locate = %+v, want one occurrence for the call", got)
	}
}

// TestLocateValueReturnsEveryOccurrence pins the audit-side literal search:
// the credential audit row names the exact original bytes, and the reader wants
// every place they appear, not only the first.
func TestLocateValueReturnsEveryOccurrence(t *testing.T) {
	value := `sk-` + strings.Repeat("A", 20) + "T3BlbkFJ" + strings.Repeat("B", 20)
	twice := observe.SemanticNode{
		ID:   "node-twice",
		Path: "$.messages[0]",
		Text: "first " + value + " second " + value,
	}
	call := observe.SemanticNode{
		ID:       "node-call",
		Path:     "$.messages[1].tool_calls[0]",
		Metadata: map[string]any{"arguments": `{"key":"` + value + `"}`},
	}
	other := observe.SemanticNode{ID: "node-other", Path: "$.messages[2]", Text: "nothing here"}
	obs := observe.TraceObservation{Request: observe.ObservationRequest{Nodes: []observe.SemanticNode{twice, call, other}}}

	got := LocateValue(obs, value)
	if len(got) != 3 {
		t.Fatalf("LocateValue() = %d occurrences, want 3: %+v", len(got), got)
	}
	byNode := map[string][]Occurrence{}
	for _, occurrence := range got {
		byNode[occurrence.NodeID] = append(byNode[occurrence.NodeID], occurrence)
		if occurrence.View == "" || occurrence.End-occurrence.Start != len(value) {
			t.Errorf("occurrence = %+v, want a view and the value's span", occurrence)
		}
	}
	if len(byNode["node-twice"]) != 2 || len(byNode["node-call"]) != 1 {
		t.Errorf("occurrences by node = %+v, want two in the message and one in the call", byNode)
	}
	if got := LocateValue(obs, ""); len(got) != 0 {
		t.Errorf("LocateValue(\"\") = %+v, want none", got)
	}
}

// quoteJSON renders a Go string as a JSON string literal.
func quoteJSON(value string) string {
	var builder strings.Builder
	builder.WriteByte('"')
	for _, r := range value {
		switch r {
		case '"':
			builder.WriteString(`\"`)
		case '\\':
			builder.WriteString(`\\`)
		case '\n':
			builder.WriteString(`\n`)
		case '\t':
			builder.WriteString(`\t`)
		default:
			builder.WriteRune(r)
		}
	}
	builder.WriteByte('"')
	return builder.String()
}

// TestExtractIgnoresHereDocumentBody keeps the code a shell writes through a
// here-document out of the index: the body is stdin data, not a command, so the
// import specifier and regex literal inside it are not operands.
func TestExtractIgnoresHereDocumentBody(t *testing.T) {
	command := "cat > /tmp/notes.txt <<'EOF'\nimport { formatDateTime } from \"../../lib/monitor\";\nconst re = /gpt-5/i;\nEOF\ncat /tmp/notes.txt"
	got := extractedValues(observationOf(callNode("bash", "Bash", `{"command":`+quoteJSON(command)+`}`)))
	for _, unwanted := range []string{"../../lib/monitor", "/gpt-5/i"} {
		if _, ok := got[unwanted]; ok {
			t.Errorf("indexed %q from a here-document body", unwanted)
		}
	}
	for _, want := range []string{"/tmp/notes.txt"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q from %+v", want, got)
		}
	}
	if len(got) != 1 {
		t.Errorf("extracted %+v, want the redirect target only", got)
	}
}

// TestExtractReadsThroughWrapperOptions covers the command word behind sudo,
// env and nice options: the wrapper's own options are not the command.
func TestExtractReadsThroughWrapperOptions(t *testing.T) {
	cases := map[string]string{
		"sudo -u root cat /etc/shadow":           "/etc/shadow",
		"env FOO=bar ./scripts/run.sh --dry-run": "./scripts/run.sh",
		"nice -n 5 cat /var/log/syslog":          "/var/log/syslog",
		"time -p cat /tmp/report.txt":            "/tmp/report.txt",
	}
	for command, want := range cases {
		got := extractedValues(observationOf(callNode("bash", "Bash", `{"command":`+quoteJSON(command)+`}`)))
		if _, ok := got[want]; !ok {
			t.Errorf("%q extracted %+v, missing %q", command, got, want)
		}
	}
}

// TestExtractReadsPluralAndNestedArguments covers the key forms MCP tools use:
// a list of paths under a plural key, and the arguments re-wrapped as JSON text.
func TestExtractReadsPluralAndNestedArguments(t *testing.T) {
	obs := observationOf(
		callNode("multi", "multi_read", `{"files":["/repo/a.go","/repo/b.go"],"dirs":["/repo/src"]}`),
		callNode("wrapped", "mcp_tool", `{"arguments":"{\"path\":\"/repo/wrapped.go\"}"}`),
	)
	got := extractedValues(obs)
	for _, want := range []string{"/repo/a.go", "/repo/b.go", "/repo/src", "/repo/wrapped.go"} {
		if _, ok := got[want]; !ok {
			t.Errorf("missing %q from %+v", want, got)
		}
	}
	if len(got) != 4 {
		t.Errorf("extracted %+v, want four resources", got)
	}
}

// TestExtractIgnoresUnclassifiedSpecifier keeps an import specifier out of the
// index even when it reaches a field whose name says nothing about a location:
// an unclassified argument needs a strong path shape, and "../../lib/monitor"
// has none.
func TestExtractIgnoresUnclassifiedSpecifier(t *testing.T) {
	obs := observationOf(callNode("tool", "some_mcp_tool", `{"from":"../../lib/monitor"}`))
	if got := Extract(obs); len(got) != 0 {
		t.Fatalf("extracted %+v from an import specifier, want none", got)
	}
}

// TestExtractReadsUnclassifiedURL keeps a URL from an unclassified field: a URL
// is an unambiguous shape, so only local paths need the stronger evidence.
func TestExtractReadsUnclassifiedURL(t *testing.T) {
	obs := observationOf(callNode("tool", "mcp_tool", `{"webhook":"https://api.example.com/hook"}`))
	got := extractedValues(obs)
	if _, ok := got["https://api.example.com/hook"]; !ok {
		t.Fatalf("missing the URL from an unclassified field: %+v", got)
	}
}

// TestExtractReadsNestedToolCallNode covers the tree shape every protocol
// parser produces: the call is a child of the message that emitted it, and the
// extractor reads it from the response tree rather than requiring a flat list.
func TestExtractReadsNestedToolCallNode(t *testing.T) {
	call := callNode("nested", "read_file", `{"file_path":"/repo/nested.go"}`)
	message := observe.SemanticNode{
		ID:             "message",
		NormalizedType: observe.NodeMessage,
		Path:           "$.choices[0].message",
		Children:       []observe.SemanticNode{call},
	}
	obs := observe.TraceObservation{Response: observe.ObservationResponse{Nodes: []observe.SemanticNode{message}}}
	got := extractedValues(obs)
	if _, ok := got["/repo/nested.go"]; !ok {
		t.Fatalf("missing the nested call's path: %+v", got)
	}
}
