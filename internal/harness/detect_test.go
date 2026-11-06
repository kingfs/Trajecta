package harness

import (
	"bytes"
	"net/http"
	"strings"
	"testing"
)

// request builds the minimal request Detect reads. The body is attached with
// http.NewRequest so ContentLength and the URL are realistic.
func request(t *testing.T, method string, target string, headers map[string]string, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(method, target, bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("http.NewRequest(%q) error = %v", target, err)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	return req
}

func TestDetectClaudeCode(t *testing.T) {
	req := request(t, http.MethodPost, "http://proxy.local/v1/messages",
		map[string]string{
			"User-Agent":       "claude-cli/2.0.14 (external, cli)",
			"x-app":            "cli",
			"anthropic-beta":   "claude-code-20250219,oauth-2025-04-20",
			"x-stainless-lang": "js",
		},
		`{"model":"claude-sonnet-4-5","max_tokens":32000,"stream":true,"system":"You are Claude Code, Anthropic's official CLI for Claude.","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"Bash","description":"run"}]}`)

	got := Detect(req, []byte(`{"model":"claude-sonnet-4-5","max_tokens":32000,"stream":true,"system":"You are Claude Code, Anthropic's official CLI for Claude.","messages":[{"role":"user","content":"hi"}],"tools":[{"name":"Bash","description":"run"}]}`))
	if got.ID != "claude-code" || got.Name != "Claude Code" || got.Kind != KindAgentCLI || got.Confidence != ConfidenceHigh {
		t.Fatalf("detection = %+v, want claude-code agent-cli high", got)
	}
	if got.Version != "2.0.14" {
		t.Fatalf("version = %q, want 2.0.14", got.Version)
	}
	assertTagsContain(t, got.Tags, "harness:claude-code", "kind:agent-cli", "sdk:anthropic-typescript", "capability:tool-use", "capability:streaming")
	if len(got.Evidence) == 0 {
		t.Fatalf("evidence is empty")
	}
	for _, evidence := range got.Evidence {
		if strings.ContainsAny(evidence, " \t\n") {
			t.Fatalf("evidence %q carries whitespace; evidence must be signal names, not values", evidence)
		}
	}
}

func TestDetectCodexCLI(t *testing.T) {
	headers := map[string]string{
		"User-Agent":            "codex_cli_rs/0.20.0 (Linux 6.8; x86_64)",
		"originator":            "codex_cli_rs",
		"OpenAI-Beta":           "responses=experimental",
		"x-codex-turn-metadata": `{"model":"gpt-5"}`,
		"session_id":            "sess-1",
	}
	body := `{"model":"gpt-5-codex","instructions":"You are a coding agent running in the Codex CLI.","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],"tools":[{"type":"function","name":"shell"},{"type":"custom","name":"apply_patch"}],"stream":true,"store":false,"prompt_cache_key":"sess-1"}`
	req := request(t, http.MethodPost, "http://proxy.local/v1/responses", headers, body)

	got := Detect(req, []byte(body))
	if got.ID != "codex-cli" || got.Kind != KindAgentCLI || got.Confidence != ConfidenceHigh {
		t.Fatalf("detection = %+v, want codex-cli agent-cli high", got)
	}
	if got.Version != "0.20.0" {
		t.Fatalf("version = %q, want 0.20.0", got.Version)
	}
	assertTagsContain(t, got.Tags, "harness:codex-cli", "kind:agent-cli", "capability:tool-use", "capability:prompt-cache", "capability:streaming", "capability:responses-api")
}

func TestDetectCodexExec(t *testing.T) {
	// `codex exec` (non-interactive) switches the originator to codex_exec, so
	// both the UA product and the originator header carry that token instead
	// of codex_cli_rs. The body deliberately carries no Codex system prompt,
	// so only the header signals can classify it.
	headers := map[string]string{
		"User-Agent": "codex_exec/0.165.0 (Mac OS 26.5.2; arm64) unknown (codex_exec; 0.165.0)",
		"originator": "codex_exec",
	}
	body := `{"model":"gpt-5","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}],"stream":true}`
	req := request(t, http.MethodPost, "http://proxy.local/v1/responses", headers, body)

	got := Detect(req, []byte(body))
	if got.ID != "codex-cli" || got.Kind != KindAgentCLI || got.Confidence != ConfidenceHigh {
		t.Fatalf("detection = %+v, want codex-cli agent-cli high", got)
	}
	if got.Version != "0.165.0" {
		t.Fatalf("version = %q, want 0.165.0", got.Version)
	}
	assertTagsContain(t, got.Tags, "harness:codex-cli", "kind:agent-cli", "capability:streaming", "capability:responses-api")
}

func TestDetectDeepSeekHarness(t *testing.T) {
	// dsh's attribution sends exactly one identifying header: the UA product
	// token deepseek-harness with the release version. No originator, no
	// product prompt, so the UA rule is the whole classification.
	headers := map[string]string{
		"User-Agent": "deepseek-harness/0.2.2-rc.1 (+https://github.com/deepseek-ai/deepseek-harness)",
	}
	body := `{"model":"deepseek-chat","messages":[{"role":"user","content":"hi"}],"stream":true,"tools":[{"type":"function","function":{"name":"shell"}}]}`
	req := request(t, http.MethodPost, "http://proxy.local/v1/chat/completions", headers, body)

	got := Detect(req, []byte(body))
	if got.ID != "deepseek-harness" || got.Name != "DeepSeek Harness" || got.Kind != KindAgentCLI || got.Confidence != ConfidenceHigh {
		t.Fatalf("detection = %+v, want deepseek-harness/DeepSeek Harness agent-cli high", got)
	}
	if got.Version != "0.2.2-rc.1" {
		t.Fatalf("version = %q, want 0.2.2-rc.1", got.Version)
	}
	assertTagsContain(t, got.Tags, "harness:deepseek-harness", "kind:agent-cli", "capability:tool-use", "capability:streaming")
}

func TestDetectFactoryDroid(t *testing.T) {
	req := request(t, http.MethodPost, "http://proxy.local/v1/chat/completions",
		map[string]string{"User-Agent": "factory-cli/1.2.3"}, `{"model":"gpt-5","messages":[]}`)
	got := Detect(req, []byte(`{"model":"gpt-5","messages":[]}`))
	if got.ID != "factory-droid" || got.Name != "Factory Droid" || got.Kind != KindAgentCLI || got.Confidence != ConfidenceHigh || got.Version != "1.2.3" {
		t.Fatalf("detection = %+v, want factory-droid/Factory Droid agent-cli high 1.2.3", got)
	}
}

func TestDetectKimiCode(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		version string
	}{
		{"legacy KimiCLI UA", map[string]string{"User-Agent": "KimiCLI/1.37.0"}, "1.37.0"},
		{"new kimi-code-cli UA", map[string]string{"User-Agent": "kimi-code-cli/1.0.0"}, "1.0.0"},
		{"platform header only", map[string]string{"X-Msh-Platform": "kimi_code_cli"}, ""},
		{"device header only", map[string]string{"X-Msh-Device-Id": "dev-1"}, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := request(t, http.MethodPost, "http://proxy.local/v1/chat/completions", test.headers, `{"model":"kimi-k2","messages":[]}`)
			got := Detect(req, []byte(`{"model":"kimi-k2","messages":[]}`))
			if got.ID != "kimi-code" || got.Kind != KindAgentCLI || got.Confidence != ConfidenceHigh {
				t.Fatalf("detection = %+v, want kimi-code agent-cli high", got)
			}
			if got.Version != test.version {
				t.Fatalf("version = %q, want %q", got.Version, test.version)
			}
		})
	}
}

func TestDetectPi(t *testing.T) {
	detailed := request(t, http.MethodPost, "http://proxy.local/v1/chat/completions",
		map[string]string{"User-Agent": "pi/0.84.0 (darwin; node/v24.0.0; arm64)"}, `{"model":"gpt-5","messages":[]}`)
	got := Detect(detailed, []byte(`{"model":"gpt-5","messages":[]}`))
	if got.ID != "pi" || got.Kind != KindAgentCLI || got.Version != "0.84.0" {
		t.Fatalf("detailed detection = %+v, want pi agent-cli 0.84.0", got)
	}
	short := request(t, http.MethodPost, "http://proxy.local/v1/chat/completions",
		map[string]string{"User-Agent": "pi"}, `{"model":"gpt-5","messages":[]}`)
	got = Detect(short, []byte(`{"model":"gpt-5","messages":[]}`))
	if got.ID != "pi" || got.Version != "" {
		t.Fatalf("short detection = %+v, want pi with no version", got)
	}
	// The two-letter token is matched exactly: an unrelated product whose
	// name merely starts with "pi" must not claim the label.
	lookalike := request(t, http.MethodPost, "http://proxy.local/v1/chat/completions",
		map[string]string{"User-Agent": "pijamas/1.0.0"}, `{"model":"gpt-5","messages":[]}`)
	if got := Detect(lookalike, []byte(`{"model":"gpt-5","messages":[]}`)); !got.Empty() {
		t.Fatalf("lookalike detection = %+v, want empty", got)
	}
}

// TestCatalogPrecedence pins the order contract Detect relies on: a product
// rule must precede any rule that would also claim its traffic. Inserting a
// rule in the wrong slot changes classifications silently, and only a test
// like this notices.
func TestCatalogPrecedence(t *testing.T) {
	index := make(map[string]int, len(catalog))
	for i, rule := range catalog {
		index[rule.id] = i
	}
	before := func(earlier, later string) {
		t.Helper()
		ei, eok := index[earlier]
		li, lok := index[later]
		if !eok || !lok {
			t.Fatalf("catalog order check %s before %s: missing %v/%v", earlier, later, eok, lok)
		}
		if ei >= li {
			t.Fatalf("catalog order: %s (%d) must come before %s (%d)", earlier, ei, later, li)
		}
	}
	before("qwen-code", "claude-code")  // Qwen impersonates Claude Code headers.
	before("codex-vscode", "codex-cli") // Same agent, different surface.
	before("codex-app", "codex-cli")    // Same agent, different surface.
	before("kimi-code", "pi")           // Pi spoofs KimiCLI UA on kimi-coding; either order labels it kimi, pinned for stability.
	before("claude-code", "litellm")    // Every product before every gateway.
	before("deepseek-harness", "litellm")
	before("factory-droid", "litellm")
	before("kimi-code", "litellm")
	before("pi", "litellm")
	before("openhands", "portkey")
	before("zed", "openrouter")
}

// TestDetectPromptInTailDoesNotClaim pins the head-only prompt search: a
// prompt phrase that only appears in the tail window (a pasted conversation,
// not a system prompt) must not claim the trace.
func TestDetectPromptInTailDoesNotClaim(t *testing.T) {
	head := `{"model":"gpt-5","input":[{"role":"user","content":[{"type":"input_text","text":"hi"}]}`
	filler := `,"padding":"` + strings.Repeat("x", bodyPrefixScan+bodySuffixScan+1024) + `"`
	tail := `,"instructions":"You are a coding agent running in the Codex CLI."}`
	body := head + filler + tail
	req := request(t, http.MethodPost, "http://proxy.local/v1/responses", nil, body)
	got := Detect(req, []byte(body))
	if !got.Empty() {
		t.Fatalf("detection = %+v, want empty for a tail-only prompt phrase", got)
	}
}

// TestDetectProductTokenBoundary pins the separator-aware product match: a
// product token that merely starts with a known token's letters (without a
// separator) is a different product.
func TestDetectProductTokenBoundary(t *testing.T) {
	req := request(t, http.MethodPost, "http://proxy.local/v1/chat/completions",
		map[string]string{"User-Agent": "continuedev/1.0.0"}, `{"model":"gpt-5","messages":[]}`)
	if got := Detect(req, []byte(`{"model":"gpt-5","messages":[]}`)); !got.Empty() {
		t.Fatalf("detection = %+v, want empty for continuedev", got)
	}
}

// TestDetectSpaceSeparatedUserAgent pins the no-slash UA form: the product
// and version are separate fields, as some app-server clients send them.
func TestDetectSpaceSeparatedUserAgent(t *testing.T) {
	req := request(t, http.MethodPost, "http://proxy.local/v1/responses",
		map[string]string{"User-Agent": "codex-cli 0.153.4"}, `{"model":"gpt-5","input":[]}`)
	got := Detect(req, []byte(`{"model":"gpt-5","input":[]}`))
	if got.ID != "codex-cli" || got.Version != "0.153.4" {
		t.Fatalf("detection = %+v, want codex-cli 0.153.4", got)
	}
}

func TestDetectCodexVSCode(t *testing.T) {
	req := request(t, http.MethodPost, "http://proxy.local/v1/responses", map[string]string{"originator": "codex_vscode"}, `{"model":"gpt-5","instructions":"You are Codex.","input":[]}`)
	got := Detect(req, []byte(`{"model":"gpt-5","instructions":"You are Codex.","input":[]}`))
	if got.ID != "codex-vscode" || got.Kind != KindIDE {
		t.Fatalf("detection = %+v, want codex-vscode ide-extension", got)
	}
}

func TestDetectGeminiCLI(t *testing.T) {
	body := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"systemInstruction":{"parts":[{"text":"You are Gemini CLI, an interactive CLI agent."}]},"tools":[{"functionDeclarations":[{"name":"run_shell_command"},{"name":"read_many_files"}]}],"generationConfig":{}}`
	req := request(t, http.MethodPost, "http://proxy.local/v1beta/models/gemini-2.5-pro:streamGenerateContent?alt=sse",
		map[string]string{"User-Agent": "GeminiCLI/0.5.0 (linux; x64)"}, body)

	got := Detect(req, []byte(body))
	if got.ID != "gemini-cli" || got.Kind != KindAgentCLI || got.Version != "0.5.0" {
		t.Fatalf("detection = %+v, want gemini-cli 0.5.0", got)
	}
	assertTagsContain(t, got.Tags, "harness:gemini-cli", "capability:streaming", "capability:tool-use")
}

func TestDetectQwenCodeBeforeGeminiFamily(t *testing.T) {
	body := `{"systemInstruction":{"parts":[{"text":"You are Qwen Code, an interactive CLI tool."}]},"contents":[]}`
	req := request(t, http.MethodPost, "http://proxy.local/v1/chat/completions", map[string]string{"User-Agent": "QwenCode/0.9.0"}, body)
	got := Detect(req, []byte(body))
	if got.ID != "qwen-code" || got.Version != "0.9.0" {
		t.Fatalf("detection = %+v, want qwen-code 0.9.0", got)
	}
}

func TestDetectPromptOnlyHarnesses(t *testing.T) {
	tests := []struct {
		name string
		body string
		id   string
	}{
		{
			name: "cline",
			body: `{"model":"gpt-4.1","messages":[{"role":"system","content":"You are Cline, a highly skilled software engineer. <execute_command> <read_file> <write_to_file>"}]}`,
			id:   "cline",
		},
		{
			name: "roo",
			body: `{"model":"gpt-4.1","messages":[{"role":"system","content":"You are Roo, a highly skilled software engineer. <switch_mode> <apply_diff>"}]}`,
			id:   "roo-code",
		},
		{
			name: "kilo",
			body: `{"model":"gpt-4.1","messages":[{"role":"system","content":"You are Kilo Code, a highly skilled software engineer."}]}`,
			id:   "kilo-code",
		},
		{
			name: "opencode",
			body: `{"model":"gpt-4.1","messages":[{"role":"system","content":"You are opencode, an AI coding agent."}]}`,
			id:   "opencode",
		},
		{
			name: "claude-code",
			body: `{"model":"claude-sonnet-4-5","system":[{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."}],"messages":[]}`,
			id:   "claude-code",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// No User-Agent and no vendor headers: only the body can identify it.
			req := request(t, http.MethodPost, "http://proxy.local/v1/chat/completions", nil, test.body)
			got := Detect(req, []byte(test.body))
			if got.ID != test.id {
				t.Fatalf("detection = %+v, want %s", got, test.id)
			}
			if got.Confidence != ConfidenceHigh {
				t.Fatalf("confidence = %q, want high", got.Confidence)
			}
		})
	}
}

func TestDetectHeaderOnlyHarnesses(t *testing.T) {
	tests := []struct {
		name    string
		headers map[string]string
		id      string
		kind    string
	}{
		{"cursor", map[string]string{"x-cursor-client-version": "0.48.0"}, "cursor", KindIDE},
		{"windsurf", map[string]string{"User-Agent": "Windsurf/1.2.3"}, "windsurf", KindIDE},
		{"copilot", map[string]string{"copilot-integration-id": "vscode-chat"}, "github-copilot", KindIDE},
		{"continue", map[string]string{"User-Agent": "Continue/0.9.1"}, "continue", KindIDE},
		{"aider", map[string]string{"User-Agent": "aider/0.70.0"}, "aider", KindAgentCLI},
		{"crush", map[string]string{"User-Agent": "crush/0.1.0"}, "crush", KindAgentCLI},
		{"goose", map[string]string{"User-Agent": "goose/1.0.0"}, "goose", KindAgentCLI},
		{"openhands", map[string]string{"User-Agent": "OpenHands/0.9.0"}, "openhands", KindAgentApp},
		{"zed", map[string]string{"x-zed-version": "0.160.0"}, "zed", KindIDE},
		{"litellm", map[string]string{"x-litellm-call-id": "call-1"}, "litellm", KindGateway},
		{"portkey", map[string]string{"x-portkey-trace-id": "trace-1"}, "portkey", KindGateway},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := request(t, http.MethodPost, "http://proxy.local/v1/chat/completions", test.headers, `{"model":"gpt-4.1","messages":[]}`)
			got := Detect(req, nil)
			if got.ID != test.id || got.Kind != test.kind {
				t.Fatalf("detection = %+v, want %s/%s", got, test.id, test.kind)
			}
		})
	}
}

func TestDetectPlainSDKKeepsTagsWithoutHarnessID(t *testing.T) {
	body := `{"model":"gpt-4.1","stream":true,"messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"lookup"}}],"response_format":{"type":"json_schema","json_schema":{"name":"x","schema":{}}}}`
	req := request(t, http.MethodPost, "http://proxy.local/v1/chat/completions",
		map[string]string{
			"User-Agent":                  "OpenAI/Python 1.40.0",
			"x-stainless-lang":            "python",
			"x-stainless-package-version": "1.40.0",
		}, body)

	got := Detect(req, []byte(body))
	if !got.Empty() {
		t.Fatalf("ID = %q, want empty for a plain SDK call", got.ID)
	}
	assertTagsContain(t, got.Tags, "sdk:openai-python", "capability:streaming", "capability:tool-use", "capability:structured-output")
	for _, tag := range got.Tags {
		if strings.HasPrefix(tag, "harness:") || strings.HasPrefix(tag, "kind:") {
			t.Fatalf("tag %q invented a harness classification", tag)
		}
	}
	if !tagsSorted(got.Tags) {
		t.Fatalf("tags are not sorted: %v", got.Tags)
	}
}

func TestDetectAnthropicSDK(t *testing.T) {
	req := request(t, http.MethodPost, "http://proxy.local/v1/messages",
		map[string]string{"User-Agent": "Anthropic/Python 0.40.0", "anthropic-version": "2023-06-01"},
		`{"model":"claude-sonnet-4-5","max_tokens":1024,"messages":[]}`)
	got := Detect(req, nil)
	if !got.Empty() {
		t.Fatalf("ID = %q, want empty", got.ID)
	}
	assertTagsContain(t, got.Tags, "sdk:anthropic-python")
}

func TestDetectUnknownRequest(t *testing.T) {
	req := request(t, http.MethodPost, "http://proxy.local/v1/chat/completions", nil, `{"model":"m","messages":[]}`)
	got := Detect(req, nil)
	if !got.Empty() || len(got.Tags) != 0 || len(got.Evidence) != 0 {
		t.Fatalf("detection = %+v, want zero value", got)
	}
	if Detect(nil, nil).Empty() != true {
		t.Fatalf("Detect(nil) must be empty")
	}
}

func TestDetectDoesNotMutateRequest(t *testing.T) {
	req := request(t, http.MethodPost, "http://proxy.local/v1/messages",
		map[string]string{"User-Agent": "claude-cli/2.0.14"}, `{"model":"claude-sonnet-4-5","system":"You are Claude Code"}`)
	before := req.Header.Get("User-Agent")
	_ = Detect(req, []byte(`{"model":"claude-sonnet-4-5","system":"You are Claude Code"}`))
	if req.Header.Get("User-Agent") != before {
		t.Fatalf("Detect mutated the request headers")
	}
}

// TestDetectLargeBodyWindow documents the deliberate bound: markers are sought
// in the head and tail of a body, not throughout a multi-megabyte payload, so
// detection cost stays flat. A harness prompt always lands in the head (system
// message / instructions / systemInstruction) and a tool array in the tail.
func TestDetectLargeBodyWindow(t *testing.T) {
	head := `{"model":"gpt-4.1","messages":[{"role":"system","content":"You are Cline, a highly skilled software engineer."}]`
	filler := `,"padding":"` + strings.Repeat("x", bodyPrefixScan+bodySuffixScan+1024) + `"`
	tail := `,"tools":[{"type":"function"}]}`
	body := head + filler + tail

	req := request(t, http.MethodPost, "http://proxy.local/v1/chat/completions", nil, body)
	got := Detect(req, []byte(body))
	if got.ID != "cline" {
		t.Fatalf("detection = %+v, want cline from the head window", got)
	}
	assertTagsContain(t, got.Tags, "capability:tool-use")
}

func assertTagsContain(t *testing.T, tags []string, wanted ...string) {
	t.Helper()
	have := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		have[tag] = struct{}{}
	}
	for _, tag := range wanted {
		if _, ok := have[tag]; !ok {
			t.Fatalf("tags = %v, missing %q", tags, tag)
		}
	}
}

func tagsSorted(tags []string) bool {
	for i := 1; i < len(tags); i++ {
		if tags[i-1] > tags[i] {
			return false
		}
	}
	return true
}

// TestDetectQwenCodeSpoofingClaudeCode covers the one deliberate impersonation
// found in the wild: Qwen Code sends claude-cli/<version> + x-app: cli when its
// base URL is not Anthropic. The body prompt is what keeps the classification
// correct, which is why the Qwen rule runs before the Claude Code rule.
func TestDetectQwenCodeSpoofingClaudeCode(t *testing.T) {
	body := `{"model":"claude-sonnet-4-5","system":[{"type":"text","text":"You are Qwen Code, an interactive CLI tool."}],"messages":[{"role":"user","content":"hi"}]}`
	req := request(t, http.MethodPost, "http://proxy.local/v1/messages", map[string]string{
		"User-Agent": "claude-cli/2.0.14 (external, cli)",
		"x-app":      "cli",
	}, body)

	got := Detect(req, []byte(body))
	if got.ID != "qwen-code" {
		t.Fatalf("detection = %+v, want qwen-code for the spoofed Claude Code headers", got)
	}
}

func TestDetectCodexHeaderNamespace(t *testing.T) {
	// A proxy may strip originator and rewrite the UA; the private x-codex-*
	// namespace survives because it carries request state the agent needs.
	req := request(t, http.MethodPost, "http://proxy.local/v1/responses",
		map[string]string{"x-codex-installation-id": "install-1"}, `{"model":"gpt-5","input":[]}`)
	got := Detect(req, nil)
	if got.ID != "codex-cli" {
		t.Fatalf("detection = %+v, want codex-cli from x-codex-*", got)
	}
}

func TestDetectCodexOriginatorVariants(t *testing.T) {
	cases := map[string]string{
		"codex-tui":             "codex-cli",
		"codex_atlas":           "codex-app",
		"codex_chatgpt_desktop": "codex-app",
	}
	for originator, want := range cases {
		req := request(t, http.MethodPost, "http://proxy.local/v1/responses", map[string]string{"originator": originator}, `{"model":"gpt-5","input":[]}`)
		got := Detect(req, nil)
		if got.ID != want {
			t.Fatalf("originator %q detection = %+v, want %s", originator, got, want)
		}
	}
}

func TestDetectCopilotHeaderMixin(t *testing.T) {
	req := request(t, http.MethodPost, "http://proxy.local/v1/messages", map[string]string{
		"Copilot-Integration-Id": "vscode-chat",
		"Editor-Version":         "vscode/1.100.0",
		"Editor-Plugin-Version":  "copilot-chat/0.30.0",
		"VScode-SessionId":       "session-1",
	}, `{"model":"claude-sonnet-4-5","messages":[]}`)
	got := Detect(req, nil)
	if got.ID != "github-copilot" || got.Kind != KindIDE {
		t.Fatalf("detection = %+v, want github-copilot ide-extension", got)
	}
}

func TestDetectIDEAttributionHeaders(t *testing.T) {
	cases := []struct {
		name    string
		headers map[string]string
		id      string
	}{
		{"cline-referer", map[string]string{"HTTP-Referer": "https://cline.bot", "X-Title": "Cline"}, "cline"},
		{"cline-originator", map[string]string{"originator": "cline", "session_id": "s-1"}, "cline"},
		{"roo-referer", map[string]string{"HTTP-Referer": "https://github.com/RooVetGit/Roo-Cline", "X-Title": "Roo Code"}, "roo-code"},
		{"opencode-session", map[string]string{"x-opencode-session": "ses-1"}, "opencode"},
		{"openrouter", map[string]string{"X-OpenRouter-Title": "my app", "HTTP-Referer": "https://example.com"}, "openrouter"},
		{"gemini-code-assist", map[string]string{"User-Agent": "CloudCodeVSCode/2.1.0 (aidev_client; proxy_client=geminicli)"}, "gemini-code-assist"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			req := request(t, http.MethodPost, "http://proxy.local/v1/chat/completions", test.headers, `{"model":"m","messages":[]}`)
			got := Detect(req, nil)
			if got.ID != test.id {
				t.Fatalf("detection = %+v, want %s", got, test.id)
			}
		})
	}
}

func TestDetectVercelAISDKGetsSDKTag(t *testing.T) {
	body := `{"model":"m","messages":[],"stream":true}`
	req := request(t, http.MethodPost, "http://proxy.local/v1/chat/completions",
		map[string]string{"User-Agent": "node.js/v22.14.0 ai-sdk-provider-utils/1.2.0"}, body)
	got := Detect(req, []byte(body))
	if !got.Empty() {
		t.Fatalf("ID = %q, want empty (AI SDK is a framework, not a harness)", got.ID)
	}
	assertTagsContain(t, got.Tags, "sdk:vercel-ai-sdk", "capability:streaming")
}

// TestDetectClineOverCodexWire pins the passthrough case: Cline can use a Codex
// account, in which case it sends originator: cline plus its own UA over the
// Responses wire. It must stay Cline, not be claimed by the Codex header rule.
func TestDetectClineOverCodexWire(t *testing.T) {
	req := request(t, http.MethodPost, "http://proxy.local/v1/responses", map[string]string{
		"User-Agent":         "Cline/3.20.0",
		"originator":         "cline",
		"session_id":         "sess-1",
		"ChatGPT-Account-Id": "acct-1",
		"x-codex-window-id":  "sess-1:1",
	}, `{"model":"gpt-5-codex","instructions":"You are Cline.","input":[]}`)
	got := Detect(req, nil)
	if got.ID != "cline" {
		t.Fatalf("detection = %+v, want cline for the Codex-account passthrough", got)
	}
}

// TestDetectBrowserAccessHeaderAloneIsNotClaudeCode: the dangerous-browser-access
// header is set by any browser SDK, so it cannot claim the CLI by itself.
func TestDetectBrowserAccessHeaderAloneIsNotClaudeCode(t *testing.T) {
	req := request(t, http.MethodPost, "http://proxy.local/v1/messages", map[string]string{
		"anthropic-dangerous-direct-browser-access": "true",
		"x-api-key":         "sk-ant-api03-example",
		"anthropic-version": "2023-06-01",
	}, `{"model":"claude-sonnet-4-5","max_tokens":16,"messages":[]}`)
	got := Detect(req, nil)
	if got.ID != "" {
		t.Fatalf("detection = %+v, want no harness from the browser-access header alone", got)
	}
}
