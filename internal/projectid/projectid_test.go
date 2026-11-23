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

// declarationOf returns the accepted declaration, failing with the rejections
// when the extractor found none.
func declarationOf(t *testing.T, obs observe.TraceObservation) Declaration {
	t.Helper()
	outcome := Extract(obs)
	if outcome.Declaration == nil {
		t.Fatalf("Extract() found no declaration (rejections: %+v)", outcome.Rejections)
	}
	return *outcome.Declaration
}

// hasDeclaration reports whether the request produced a usable project.
func hasDeclaration(obs observe.TraceObservation) bool {
	return Extract(obs).Declaration != nil
}

const dshSnapshot = `<system-reminder>
The following information describes your current session:

Current working directory: "/data/src/github.com/kingfs/Trajecta".
</system-reminder>`

func TestExtractDeepSeekHarnessRuntimeContext(t *testing.T) {
	decl := declarationOf(t, observation(
		userMessage("0", "user", "please fix the bug"),
		userMessage("1", "user", dshSnapshot),
	))
	if decl.Root != "/data/src/github.com/kingfs/Trajecta" {
		t.Fatalf("Root = %q", decl.Root)
	}
	if decl.Source != SourceDSHRuntimeContext {
		t.Fatalf("Source = %q", decl.Source)
	}
	if decl.MultipleRoots {
		t.Fatal("MultipleRoots = true for a single declaration")
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
	decl := declarationOf(t, observation(userMessage("0", "user", older), userMessage("1", "user", newer)))
	if decl.Root != "/repo/two" {
		t.Fatalf("Root = %q, want the superseding snapshot", decl.Root)
	}
	if !decl.MultipleRoots {
		t.Fatal("MultipleRoots = false, want true for two distinct roots")
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
			decl := declarationOf(t, observation(
				userMessage("0", "system", "You are Claude Code."),
				userMessage("1", "user", tc.body),
			))
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
	decl := declarationOf(t, observation(userMessage("0", "user", body)))
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
	decl := declarationOf(t, observation(userMessage("0", "user", body)))
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
		if hasDeclaration(observation(userMessage("0", role, body))) {
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
	if hasDeclaration(observe.TraceObservation{Request: observe.ObservationRequest{
		Messages: []observe.SemanticNode{toolResult}, Nodes: []observe.SemanticNode{toolResult},
	}}) {
		t.Fatal("tool_result node produced a declaration")
	}
}

// An assistant message can quote an environment block; that is the model
// talking, not the harness.
func TestExtractIgnoresAssistantMessages(t *testing.T) {
	body := "<env>\nWorking directory: /quoted/by/model\n</env>"
	if hasDeclaration(observation(userMessage("0", "assistant", body))) {
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
		if hasDeclaration(observation(userMessage("0", "user", text))) {
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
	decl := declarationOf(t, obs)
	if decl.Root != "/data/src/app" {
		t.Fatalf("got %q", decl.Root)
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
	decl := declarationOf(t, obs)
	if decl.Root != "/repo/entry" || decl.Source != SourceClaudeEnv {
		t.Fatalf("got %q from %q", decl.Root, decl.Source)
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

// A declaration-shaped value that the validator refuses is reported with the
// reason, not silently dropped: that is what makes a placeholder or a sudden
// relative path visible in the detection report.
func TestExtractReportsRejections(t *testing.T) {
	cases := []struct {
		name       string
		text       string
		wantReason Reason
		wantSource Source
	}{
		{"placeholder", "<env>\nPrimary working directory: /path/to/repo\n</env>", ReasonPlaceholder, SourceClaudeEnv},
		{"relative", "<environment_context><cwd>web/dist</cwd></environment_context>", ReasonRelative, SourceCodexEnv},
		{"home relative", "<system-reminder>\nCurrent working directory: \"~/repo\".\n</system-reminder>", ReasonHomeRelative, SourceDSHRuntimeContext},
		{"system root", "<environment_context><cwd>/tmp</cwd></environment_context>", ReasonSystemRoot, SourceCodexEnv},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome := Extract(observation(userMessage("0", "user", tc.text)))
			if outcome.Declaration != nil {
				t.Fatalf("declaration = %+v, want none", outcome.Declaration)
			}
			found := false
			for _, rejection := range outcome.Rejections {
				if rejection.Source == tc.wantSource && rejection.Reason == tc.wantReason {
					found = true
				}
			}
			if !found {
				t.Fatalf("rejections = %+v, want %s/%s", outcome.Rejections, tc.wantSource, tc.wantReason)
			}
		})
	}
}

// A known environment block that carries no readable value is the format-drift
// signal: the harness still sends its block, but not the line we parse.
func TestExtractReportsUnreadableBlocks(t *testing.T) {
	cases := []struct {
		name       string
		text       string
		wantSource Source
	}{
		{"codex without cwd", "<environment_context>\n<shell>bash</shell>\n</environment_context>", SourceCodexEnv},
		{"claude changed wording", "<env>\nPrimary Working Directory: /repo/x\n</env>", SourceClaudeEnv},
		{"dsh label moved", "Current working directory: /repo/x", SourceDSHRuntimeContext},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome := Extract(observation(userMessage("0", "user", tc.text)))
			if outcome.Declaration != nil {
				t.Fatalf("declaration = %+v, want none", outcome.Declaration)
			}
			if len(outcome.Rejections) == 0 {
				t.Fatal("no rejection recorded for an unreadable declaration block")
			}
			if outcome.Rejections[0].Source != tc.wantSource || outcome.Rejections[0].Reason != ReasonUnreadable {
				t.Fatalf("rejection = %+v, want %s/unreadable", outcome.Rejections[0], tc.wantSource)
			}
		})
	}
}

// A real declaration wins over a rejection in the same request: the rejected
// value is reported, but the project is still recorded.
func TestExtractDeclarationWinsOverRejection(t *testing.T) {
	outcome := Extract(observation(
		userMessage("0", "user", "<env>\nPrimary working directory: /path/to/repo\n</env>"),
		userMessage("1", "user", "<env>\nPrimary working directory: /repo/real\n</env>"),
	))
	if outcome.Declaration == nil || outcome.Declaration.Root != "/repo/real" {
		t.Fatalf("declaration = %+v", outcome.Declaration)
	}
	if len(outcome.Rejections) != 1 || outcome.Rejections[0].Reason != ReasonPlaceholder {
		t.Fatalf("rejections = %+v", outcome.Rejections)
	}
}
