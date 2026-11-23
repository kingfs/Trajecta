package projectid

import (
	"context"
	"testing"

	"github.com/kingfs/Trajecta/pkg/llm"
	"github.com/kingfs/Trajecta/pkg/observe"
	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// userMessage builds the message node a protocol parser would produce for one
// text part, so a case can state the role the harness used without depending on
// a parser detail.
func userMessage(id string, role string, text string) observe.SemanticNode {
	return observe.SemanticNode{
		ID:             id,
		ProviderType:   "message",
		NormalizedType: observe.NodeMessage,
		Role:           role,
		Path:           "$.messages[" + id + "]",
		Text:           text,
	}
}

func observation(messages ...observe.SemanticNode) observe.TraceObservation {
	return observe.TraceObservation{
		Request: observe.ObservationRequest{Messages: messages, Nodes: messages},
	}
}

const dshSnapshot = `<system-reminder>
The following information describes your current session:

Current working directory: "/data/src/github.com/kingfs/Trajecta".
</system-reminder>`

func TestExtractDeepSeekHarnessRuntimeContext(t *testing.T) {
	decl, ok := Extract(observation(
		userMessage("0", "user", "please fix the bug"),
		userMessage("1", "user", dshSnapshot),
	))
	if !ok {
		t.Fatal("Extract() found no declaration")
	}
	if decl.Root != "/data/src/github.com/kingfs/Trajecta" {
		t.Fatalf("Root = %q", decl.Root)
	}
	if decl.Source != SourceDSHRuntimeContext {
		t.Fatalf("Source = %q", decl.Source)
	}
	if decl.Conflicting {
		t.Fatal("Conflicting = true for a single declaration")
	}
}

// A DeepSeek Harness session that changed directory carries both snapshots and
// says the newer one supersedes the older, so the last value is the current one.
func TestExtractLastSnapshotWins(t *testing.T) {
	older := `<system-reminder>
The following information describes your current session:

Current working directory: "/repo/one".
</system-reminder>`
	newer := `<system-reminder>
The following information describes your current session. It replaces earlier versions of this context, including any retained in conversation summaries.

Current working directory: "/repo/two".
</system-reminder>`
	decl, ok := Extract(observation(userMessage("0", "user", older), userMessage("1", "user", newer)))
	if !ok {
		t.Fatal("Extract() found no declaration")
	}
	if decl.Root != "/repo/two" {
		t.Fatalf("Root = %q, want the superseding snapshot", decl.Root)
	}
	if !decl.Conflicting {
		t.Fatal("Conflicting = false, want true for two distinct roots")
	}
}

func TestExtractClaudeEnvBlock(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "primary prefix",
			body: "<env>\nPrimary working directory: /Users/me/src/app\nIs directory a git repo: Yes\nPlatform: darwin\n</env>",
			want: "/Users/me/src/app",
		},
		{
			name: "legacy prefix",
			body: "<env>\nWorking directory: /home/me/app\nIs directory a git repo: No\n</env>",
			want: "/home/me/app",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			decl, ok := Extract(observation(
				userMessage("0", "system", "You are Claude Code."),
				userMessage("1", "user", tc.body),
			))
			if !ok {
				t.Fatal("Extract() found no declaration")
			}
			if decl.Root != tc.want || decl.Source != SourceClaudeEnv {
				t.Fatalf("got %q from %q", decl.Root, decl.Source)
			}
		})
	}
}

func TestExtractCodexEnvironmentContext(t *testing.T) {
	body := `<environment_context>
  <cwd>/data/src/github.com/kingfs/Trajecta</cwd>
  <shell>zsh</shell>
  <current_date>2026-11-23</current_date>
  <workspace_roots><root>/data/src/github.com/kingfs/Trajecta</root></workspace_roots>
</environment_context>`
	decl, ok := Extract(observation(userMessage("0", "user", body)))
	if !ok {
		t.Fatal("Extract() found no declaration")
	}
	if decl.Root != "/data/src/github.com/kingfs/Trajecta" || decl.Source != SourceCodexEnv {
		t.Fatalf("got %q from %q", decl.Root, decl.Source)
	}
}

// A multi-root workspace without a <cwd> still names a project.
func TestExtractCodexWorkspaceRootsOnly(t *testing.T) {
	body := `<environment_context>
  <shell>bash</shell>
  <workspace_roots><root>/work/mono</root><root>/work/other</root></workspace_roots>
</environment_context>`
	decl, ok := Extract(observation(userMessage("0", "user", body)))
	if !ok {
		t.Fatal("Extract() found no declaration")
	}
	if decl.Root != "/work/mono" || decl.Source != SourceCodexWorkspaceRoots {
		t.Fatalf("got %q from %q", decl.Root, decl.Source)
	}
}

// A tool that read a file containing an environment block must not become the
// session's project: the block is content there, not a declaration.
func TestExtractIgnoresToolMessages(t *testing.T) {
	body := `<environment_context>
  <cwd>/somewhere/else</cwd>
</environment_context>`
	for _, role := range []string{"tool", "function"} {
		if _, ok := Extract(observation(userMessage("0", role, body))); ok {
			t.Fatalf("role %q produced a declaration", role)
		}
	}
	toolResult := observe.SemanticNode{
		ID:             "tr",
		NormalizedType: observe.NodeToolResult,
		Role:           "tool",
		Path:           "$.messages[1]",
		Text:           body,
	}
	if _, ok := Extract(observe.TraceObservation{Request: observe.ObservationRequest{
		Messages: []observe.SemanticNode{toolResult}, Nodes: []observe.SemanticNode{toolResult},
	}}); ok {
		t.Fatal("tool_result node produced a declaration")
	}
}

// An assistant message can quote an environment block; that is the model
// talking, not the harness.
func TestExtractIgnoresAssistantMessages(t *testing.T) {
	body := "<env>\nWorking directory: /quoted/by/model\n</env>"
	if _, ok := Extract(observation(userMessage("0", "assistant", body))); ok {
		t.Fatal("assistant message produced a declaration")
	}
}

// A path in prose is not a declaration, however path-shaped it looks.
func TestExtractIgnoresProse(t *testing.T) {
	texts := []string{
		"I changed the working directory to /repo/app earlier.",
		"Current working directory: /repo/app",
		"<env>\nNo working directory here\n</env>",
		"<environment_context><shell>bash</shell></environment_context>",
	}
	for _, text := range texts {
		if _, ok := Extract(observation(userMessage("0", "user", text))); ok {
			t.Fatalf("%q produced a declaration", text)
		}
	}
}

func TestNormalize(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		wantRoot string
		wantKey  string
		wantOK   bool
	}{
		{"posix", "/data/src/app", "/data/src/app", "/data/src/app", true},
		{"trailing separator", "/data/src/app/", "/data/src/app", "/data/src/app", true},
		{"container root", "/app", "/app", "/app", true},
		{"windows drive", `C:\Users\me\repo`, `C:\Users\me\repo`, "c:/users/me/repo", true},
		{"windows escaped", `C:\\Users\\me\\repo`, `C:\Users\me\repo`, "c:/users/me/repo", true},
		{"unc", `\\server\share\repo`, `\\server\share\repo`, "//server/share/repo", true},
		{"relative", "web/dist", "", "", false},
		{"home relative", "~/repo", "", "", false},
		{"placeholder", "/path/to/repo", "", "", false},
		{"template", "<repo>", "", "", false},
		{"root", "/", "", "", false},
		{"tmp", "/tmp", "", "", false},
		{"home parent", "/home", "", "", false},
		{"users parent", "/Users", "", "", false},
		{"drive root", `C:\`, "", "", false},
		{"windows users", `C:\Users`, "", "", false},
		{"empty", "  ", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root, key, ok := Normalize(tc.path)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v (root %q key %q)", ok, tc.wantOK, root, key)
			}
			if !tc.wantOK {
				return
			}
			if root != tc.wantRoot || key != tc.wantKey {
				t.Fatalf("got root %q key %q, want %q / %q", root, key, tc.wantRoot, tc.wantKey)
			}
		})
	}
}

// The extractor has to work on the Observation IR a real parser produces, not
// only on hand-built nodes: a chat request keeps the system prompt in Messages
// and a Responses request keeps instructions in Instructions.
func TestExtractFromParsedChatRequest(t *testing.T) {
	parser := observe.NewOpenAIParser()
	input := observeParseInput(t, `{
		"model": "gpt-4o",
		"messages": [
			{"role": "system", "content": "You are a coding agent."},
			{"role": "user", "content": "<system-reminder>\nThe following information describes your current session:\n\nCurrent working directory: \"/data/src/app\".\n</system-reminder>"}
		]
	}`, `{"id":"chatcmpl_1","model":"gpt-4o","choices":[{"index":0,"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	obs, err := parser.Parse(context.Background(), input)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	decl, ok := Extract(obs)
	if !ok || decl.Root != "/data/src/app" {
		t.Fatalf("got %q ok=%v", decl.Root, ok)
	}
}

// The client-entry parser keeps the whole request as one raw node, so the
// declaration has to be read out of its role-bearing message objects.
func TestExtractFromEntryRequest(t *testing.T) {
	parser := observe.NewEntryParser()
	input := observeParseInput(t, `{
		"model": "gpt-5",
		"input": [
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "<env>\nPrimary working directory: /repo/entry\n</env>"}]}
		]
	}`, `{"id":"resp_1","model":"gpt-5","output":[]}`)
	obs, err := parser.Parse(context.Background(), input)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	decl, ok := Extract(obs)
	if !ok || decl.Root != "/repo/entry" || decl.Source != SourceClaudeEnv {
		t.Fatalf("got %q from %q ok=%v", decl.Root, decl.Source, ok)
	}
}

func observeParseInput(t *testing.T, request string, response string) observe.ParseInput {
	t.Helper()
	return observe.ParseInput{
		TraceID: "trace-project",
		Header: recordfile.RecordHeader{Meta: recordfile.MetaData{
			Provider:     llm.ProviderOpenAICompatible,
			Operation:    llm.OperationChatCompletions,
			Endpoint:     "/v1/chat/completions",
			ExchangeKind: "model",
		}},
		RequestBody:  []byte(request),
		ResponseBody: []byte(response),
	}
}
