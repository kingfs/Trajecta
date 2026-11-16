// Package benchcorpus builds deterministic, realistically shaped LLM traffic
// for benchmarks. It is not used by production code.
//
// One trajectory is a multi-step agent session (user turn, assistant turn with
// tool calls, tool results) that can be encoded into any of the four wire
// protocols the proxy converts between, so the conversion, detection and audit
// benchmarks all measure the same semantic workload. Sizes follow the shapes
// seen in practice:
//
//	Short      a two-turn exchange (~2 KiB)
//	Medium     a short agent session (~32 KiB)
//	Long       a long session (~128 KiB)
//	Agent256K  a 256 KiB context-window agent trajectory (100+ steps)
//
// Options.Secrets plants a fixed set of real-shaped credentials and personal
// data in tool results and prompts (one key is reused across steps, like a real
// session), and Options.Dense plants several per step. The values are
// documented in SensitiveValues so a benchmark can assert that detection really
// saw them; nothing here is a live credential.
package benchcorpus

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kingfs/Trajecta/pkg/usage"
	"github.com/kingfs/Trajecta/pkg/xir"
)

// Sizes used by the benchmarks. They are targets, not exact byte counts: the
// encoded body lands within one step of the target.
const (
	ShortBytes     = 2 << 10
	MediumBytes    = 32 << 10
	LongBytes      = 128 << 10
	Agent256KBytes = 256 << 10
)

// Options controls what a generated trajectory contains.
type Options struct {
	// Secrets plants credentials and personal data in realistic positions.
	Secrets bool
	// Dense plants several sensitive values in every step instead of one
	// roughly every five steps. It is the worst case for detection.
	Dense bool
}

// Tool is one function declaration.
type Tool struct {
	Name        string
	Description string
	Parameters  map[string]any
}

// Call is one tool invocation and its result.
type Call struct {
	ID        string
	Name      string
	Arguments string
	Result    string
}

// Step is one user turn, the assistant reply to it, and the tool calls the
// assistant made.
type Step struct {
	User      string
	Assistant string
	Reasoning string
	Calls     []Call
}

// Trajectory is a full agent session. The same value encodes into every
// protocol, which keeps conversion benchmarks comparable.
type Trajectory struct {
	Name             string
	Model            string
	System           string
	Tools            []Tool
	Steps            []Step
	PromptTokens     int
	CompletionTokens int
}

// SensitiveValues returns the distinct credentials and personal data the
// Secrets/Dense options plant. The values satisfy the real rule shapes (they
// are the same kinds of samples the secretmask tests use) but are not live.
func SensitiveValues() []string {
	return []string{
		openAIProjectKey,
		anthropicKey,
		googleAPIKey,
		gitHubToken,
		awsAccessKey,
		jwtToken,
		chineseID,
		mobileNumber,
		dsnValue,
		envValue,
	}
}

// The credential-shaped samples are assembled at run time from fragments that
// are not themselves tokens. A literal that already looks like a live key is a
// false positive for every secret scanner - including GitHub push protection,
// which blocks the push even though the value is synthetic - while the
// benchmark only needs the final shape.
var (
	openAIProjectKey = "sk-proj-" + strings.Repeat("A", 58) + "T3BlbkFJ" + strings.Repeat("B", 58)
	anthropicKey     = "sk-ant-api03-" + strings.Repeat("A", 95)
	googleAPIKey     = "AIza" + "YwW-8cJylh02Geqt9eScQtJ2qHlp3p7sS2m"
	gitHubToken      = "ghp_" + "Qe6Jr4pF6bHh5OtH5yJsF6KvCcAwV95ADhv6"
	awsAccessKey     = "AKIA" + "YOBVNZYA4RWNVLIY"
	jwtToken         = strings.Join([]string{
		"eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9",
		"eyJzdWIiOiIxMjM0NTY3ODkwIn0",
		"SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c",
	}, ".")
	chineseID    = "11010519491231002X"
	mobileNumber = "13800138000"
	dsnValue     = "postgres://app:4f9c2d7e1a@db.internal:5432/app?sslmode=require"
	envValue     = "OPENAI_API_KEY=" + openAIProjectKey
)

// pemBlock is report-only for the masking lane (it is never rewritten), which
// is exactly the kind of finding the audit lane must still report.
const pemBlock = "-----BEGIN RSA PRIVATE KEY-----\n" +
	"MIIEowIBAAKCAQEAw7m3nZ0aR2kP0sT4uV6xY8zA1bC3dE5fG7hI9jK1lM3nO5pQ7r\n" +
	"-----END RSA PRIVATE KEY-----"

func benchTools() []Tool {
	return []Tool{
		{Name: "read_file", Description: "Read a text file from the repository", Parameters: map[string]any{
			"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "start": map[string]any{"type": "integer"}, "end": map[string]any{"type": "integer"}}, "required": []string{"path"},
		}},
		{Name: "grep", Description: "Search the repository with a regular expression", Parameters: map[string]any{
			"type": "object", "properties": map[string]any{"pattern": map[string]any{"type": "string"}, "path": map[string]any{"type": "string"}}, "required": []string{"pattern"},
		}},
		{Name: "apply_patch", Description: "Apply a unified diff to the repository", Parameters: map[string]any{
			"type": "object", "properties": map[string]any{"patch": map[string]any{"type": "string"}}, "required": []string{"patch"},
		}},
		{Name: "run_tests", Description: "Run the package tests", Parameters: map[string]any{
			"type": "object", "properties": map[string]any{"package": map[string]any{"type": "string"}, "run": map[string]any{"type": "string"}}, "required": []string{"package"},
		}},
		{Name: "list_dir", Description: "List a directory", Parameters: map[string]any{
			"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}},
		}},
	}
}

const benchSystemPrompt = `You are a coding agent working in a local repository. Investigate the failing tests,
read the relevant files, propose a minimal patch, run the package tests and report what changed.
Prefer reading a file over guessing. Never print a credential or a personal identifier in your
final answer; if you find one in a log or a config file, describe where it is instead.`

var benchTasks = []string{
	"inspect the failing test in internal/proxy/handler_test.go and explain the root cause",
	"read cmd/server/root.go and list every flag the server binary registers",
	"find where the conversion engine is selected and summarize the fallback path",
	"check internal/store/channel_store.go for the transaction boundary around a model update",
	"look at web/monitor-ui/src/features/traces and report which query keys it invalidates",
	"search for the session fingerprint helper and list its callers",
	"review the Postgres migration that adds the trace_findings index",
	"run the router package tests and summarize any failure",
}

var benchPaths = []string{
	"internal/proxy/handler.go",
	"internal/router/router.go",
	"internal/store/channel_store.go",
	"pkg/xir/convert/losses.go",
	"pkg/secretmask/vault.go",
	"web/monitor-ui/src/features/traces/TraceDetail.tsx",
	"internal/appdbmigrate/migrations.go",
	"cmd/trajecta/root.go",
}

const benchLogNoise = `2026-11-16T10:22:31.418Z INFO  request completed request_id=1794827645532144365 status=200 duration_ms=187 ttft_ms=92
2026-11-16T10:22:31.902Z WARN  retry budget nearly exhausted attempt=2/3 peer=10.2.68.223:19090
2026-11-16T10:22:32.117Z INFO  cache hit ratio=0.87 key=550e8400-e29b-41d4-a716-446655440000 sha=d41d8cd98f00b204e9800998ecf8427e
2026-11-16T10:22:32.550Z ERROR upstream returned 502 after 30s; check resp.StatusCode and the retry budget
`

func codeSnippet(i int) string {
	name := benchPaths[i%len(benchPaths)]
	return fmt.Sprintf(`func handle%d(ctx context.Context, req *Request) (*Response, error) {
	// %s
	// The request is converted only when no native target is selectable, so the
	// bytes on the wire stay identical for every native request.
	selection, err := router.Select(ctx, req.Model)
	if err != nil {
		return nil, fmt.Errorf("select upstream: %%w", err)
	}
	result, err := convertRequestBody(selection, req.Body)
	if err != nil {
		return nil, err
	}
	return recordAndForward(ctx, selection, result)
}

type requestItem struct {
	ID        string            `+"`json:\"id\"`"+`
	Model     string            `+"`json:\"model\"`"+`
	Messages  []json.RawMessage `+"`json:\"messages\"`"+`
	Metadata  map[string]string `+"`json:\"metadata,omitempty\"`"+`
}
`, i, name)
}

func sensitiveLines(i int, opts Options) string {
	if !opts.Secrets {
		return ""
	}
	var b strings.Builder
	plant := func(step int, line string) {
		if opts.Dense || (i+1)%step == 0 {
			b.WriteString(line)
			b.WriteByte('\n')
		}
	}
	plant(5, "env: OPENAI_API_KEY="+openAIProjectKey)
	plant(6, "DATABASE_URL="+dsnValue)
	plant(7, "Authorization: Bearer "+jwtToken)
	plant(8, "customer id=客户身份证号 "+chineseID+" phone="+mobileNumber)
	plant(9, "aws_access_key_id="+awsAccessKey)
	plant(10, "GH_TOKEN="+gitHubToken)
	plant(11, "ANTHROPIC_API_KEY="+anthropicKey)
	plant(12, "GOOGLE_API_KEY="+googleAPIKey)
	if i == 3 {
		b.WriteString(pemBlock)
		b.WriteByte('\n')
	}
	return b.String()
}

func toolResult(i int, opts Options) string {
	return codeSnippet(i) + "\n" + benchLogNoise + sensitiveLines(i, opts)
}

func agentStep(i int, opts Options) Step {
	task := benchTasks[i%len(benchTasks)]
	path := benchPaths[i%len(benchPaths)]
	calls := []Call{{
		ID:        fmt.Sprintf("call_%d", i+1),
		Name:      "read_file",
		Arguments: fmt.Sprintf(`{"path":%q}`, path),
		Result:    toolResult(i, opts),
	}}
	if i%11 == 10 {
		calls = append(calls, Call{
			ID:        fmt.Sprintf("call_%d_b", i+1),
			Name:      "grep",
			Arguments: fmt.Sprintf(`{"pattern":"Select|convertRequestBody","path":%q}`, path),
			Result:    toolResult(i+1, opts),
		})
	}
	return Step{
		User:      fmt.Sprintf("Step %d: %s", i+1, task),
		Assistant: fmt.Sprintf("I will read %s and check the failure path before proposing a patch.", path),
		Reasoning: "The failing assertion points at the conversion branch, so the native path is probably unaffected.",
		Calls:     calls,
	}
}

const benchNoise = "The quick brown fox jumps over the lazy dog. " +
	"Please review commit 3f2a9c8e1b4d5a6f7c8e9d0a1b2c3d4e5f607182 and the helper handleRequest. " +
	"Blob d41d8cd98f00b204e9800998ecf8427e and id 550e8400-e29b-41d4-a716-446655440000 are not credentials. "

func estimateStepBytes(step Step) int {
	size := len(step.User) + len(step.Assistant) + len(step.Reasoning) + 160
	for _, call := range step.Calls {
		size += len(call.ID) + len(call.Name) + len(call.Arguments) + len(call.Result) + 120
	}
	return size
}

// Agent builds a multi-step agent trajectory of at least target bytes (the chat
// encoding is the sizing reference) and ends with a normal assistant summary.
func Agent(target int, opts Options) *Trajectory {
	if target < ShortBytes {
		target = ShortBytes
	}
	trajectory := &Trajectory{
		Name:             fmt.Sprintf("agent-%d", target),
		Model:            "gpt-5-bench",
		System:           benchSystemPrompt,
		Tools:            benchTools(),
		PromptTokens:     target / 4,
		CompletionTokens: 512,
	}
	size := len(benchSystemPrompt) + 2048 // tools overhead
	for i := 0; size < target; i++ {
		step := agentStep(i, opts)
		trajectory.Steps = append(trajectory.Steps, step)
		size += estimateStepBytes(step)
	}
	if len(trajectory.Steps) == 0 {
		trajectory.Steps = append(trajectory.Steps, agentStep(0, opts))
	}
	// Pad with repeatable prose until the real chat encoding reaches the target.
	// The loop encodes at most a handful of times; it runs before the benchmark
	// timer starts.
	for attempt := 0; attempt < 4 && len(EncodeChat(trajectory)) < target; attempt++ {
		deficit := target - len(EncodeChat(trajectory)) + 128
		last := &trajectory.Steps[len(trajectory.Steps)-1]
		if len(last.Calls) == 0 {
			last.User += strings.Repeat(benchNoise, deficit/len(benchNoise)+1)
			continue
		}
		last.Calls[len(last.Calls)-1].Result += strings.Repeat(benchNoise, deficit/len(benchNoise)+1)
	}
	trajectory.Steps = append(trajectory.Steps, Step{
		Assistant: "Done. The failing test was caused by the retry branch reusing the first selection; " +
			"the patch moves the re-selection after the conversion decision and the package tests pass.",
	})
	return trajectory
}

// LongContext builds one large user turn (a pasted document or log) plus a short
// assistant reply. It isolates long-context parsing and detection from the
// multi-step tool-loop shape.
func LongContext(target int, opts Options) *Trajectory {
	if target < ShortBytes {
		target = ShortBytes
	}
	trajectory := &Trajectory{
		Name:             fmt.Sprintf("context-%d", target),
		Model:            "gpt-5-bench",
		System:           benchSystemPrompt,
		Tools:            benchTools(),
		PromptTokens:     target / 4,
		CompletionTokens: 256,
	}
	var document strings.Builder
	document.Grow(target + 1024)
	document.WriteString("Here is the incident dump. Find the root cause and the leaked values:\n")
	document.WriteString(sensitiveLines(0, Options{Secrets: opts.Secrets, Dense: opts.Dense}))
	for i := 0; document.Len() < target; i++ {
		document.WriteString(codeSnippet(i))
		document.WriteString("\n")
		document.WriteString(benchLogNoise)
		document.WriteString(sensitiveLines(i+1, opts))
		document.WriteString(benchNoise)
		document.WriteString("\n")
	}
	trajectory.Steps = []Step{
		{User: document.String()},
		{Assistant: "The 502 is a retry-budget interaction; the leaked values are in the env dump, not in the code."},
	}
	return trajectory
}

func chatTools(tools []Tool) []any {
	out := make([]any, 0, len(tools))
	for _, tool := range tools {
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name": tool.Name, "description": tool.Description, "parameters": tool.Parameters,
			},
		})
	}
	return out
}

func functionDeclarations(tools []Tool) []any {
	out := make([]any, 0, len(tools))
	for _, tool := range tools {
		out = append(out, map[string]any{
			"name": tool.Name, "description": tool.Description, "parameters": tool.Parameters,
		})
	}
	return out
}

func mustJSON(value any) []byte {
	raw, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return raw
}

// EncodeChat renders the trajectory as an OpenAI Chat Completions request body.
func EncodeChat(trajectory *Trajectory) []byte {
	messages := []any{map[string]any{"role": "system", "content": trajectory.System}}
	for _, step := range trajectory.Steps {
		messages = append(messages, map[string]any{"role": "user", "content": step.User})
		if step.Assistant != "" || len(step.Calls) > 0 {
			message := map[string]any{"role": "assistant"}
			if step.Assistant == "" {
				message["content"] = nil
			} else {
				message["content"] = step.Assistant
			}
			if len(step.Calls) > 0 {
				calls := make([]any, 0, len(step.Calls))
				for _, call := range step.Calls {
					calls = append(calls, map[string]any{
						"id": call.ID, "type": "function",
						"function": map[string]any{"name": call.Name, "arguments": call.Arguments},
					})
				}
				message["tool_calls"] = calls
			}
			messages = append(messages, message)
		}
		for _, call := range step.Calls {
			messages = append(messages, map[string]any{
				"role": "tool", "tool_call_id": call.ID, "content": call.Result,
			})
		}
	}
	return mustJSON(map[string]any{
		"model": trajectory.Model, "messages": messages, "tools": chatTools(trajectory.Tools),
		"max_tokens": 4096, "temperature": 0.2, "stream": false,
		"metadata": map[string]any{"bench_trajectory": trajectory.Name, "session_id": "bench-session"},
	})
}

// EncodeMessages renders the trajectory as an Anthropic Messages request body.
func EncodeMessages(trajectory *Trajectory) []byte {
	messages := make([]any, 0, len(trajectory.Steps)*3)
	for _, step := range trajectory.Steps {
		messages = append(messages, map[string]any{"role": "user", "content": step.User})
		if step.Assistant != "" || len(step.Calls) > 0 {
			blocks := make([]any, 0, len(step.Calls)+1)
			if step.Assistant != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": step.Assistant})
			}
			for _, call := range step.Calls {
				var input any
				if err := json.Unmarshal([]byte(call.Arguments), &input); err != nil {
					input = map[string]any{}
				}
				blocks = append(blocks, map[string]any{
					"type": "tool_use", "id": call.ID, "name": call.Name, "input": input,
				})
			}
			messages = append(messages, map[string]any{"role": "assistant", "content": blocks})
		}
		if len(step.Calls) > 0 {
			blocks := make([]any, 0, len(step.Calls))
			for _, call := range step.Calls {
				blocks = append(blocks, map[string]any{
					"type": "tool_result", "tool_use_id": call.ID, "content": call.Result,
				})
			}
			messages = append(messages, map[string]any{"role": "user", "content": blocks})
		}
	}
	tools := make([]any, 0, len(trajectory.Tools))
	for _, tool := range trajectory.Tools {
		tools = append(tools, map[string]any{
			"name": tool.Name, "description": tool.Description, "input_schema": tool.Parameters,
		})
	}
	return mustJSON(map[string]any{
		"model": trajectory.Model, "system": trajectory.System, "messages": messages,
		"tools": tools, "max_tokens": 4096, "temperature": 0.2,
	})
}

// EncodeResponses renders the trajectory as an OpenAI Responses request body.
func EncodeResponses(trajectory *Trajectory) []byte {
	input := make([]any, 0, len(trajectory.Steps)*3)
	for _, step := range trajectory.Steps {
		input = append(input, map[string]any{
			"type": "message", "role": "user",
			"content": []any{map[string]any{"type": "input_text", "text": step.User}},
		})
		if step.Assistant != "" {
			input = append(input, map[string]any{
				"type": "message", "role": "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": step.Assistant}},
			})
		}
		for _, call := range step.Calls {
			input = append(input, map[string]any{
				"type": "function_call", "call_id": call.ID, "name": call.Name, "arguments": call.Arguments,
			})
		}
		for _, call := range step.Calls {
			input = append(input, map[string]any{
				"type": "function_call_output", "call_id": call.ID, "output": call.Result,
			})
		}
	}
	return mustJSON(map[string]any{
		"model": trajectory.Model, "instructions": trajectory.System, "input": input,
		"tools": chatTools(trajectory.Tools), "max_output_tokens": 4096,
		"temperature": 0.2, "store": false,
	})
}

// EncodeGemini renders the trajectory as a Gemini generateContent request body.
func EncodeGemini(trajectory *Trajectory) []byte {
	contents := make([]any, 0, len(trajectory.Steps)*3)
	for _, step := range trajectory.Steps {
		contents = append(contents, map[string]any{
			"role": "user", "parts": []any{map[string]any{"text": step.User}},
		})
		if step.Assistant != "" || len(step.Calls) > 0 {
			parts := make([]any, 0, len(step.Calls)+1)
			if step.Assistant != "" {
				parts = append(parts, map[string]any{"text": step.Assistant})
			}
			for _, call := range step.Calls {
				var args any
				if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
					args = map[string]any{}
				}
				parts = append(parts, map[string]any{"functionCall": map[string]any{"name": call.Name, "args": args}})
			}
			contents = append(contents, map[string]any{"role": "model", "parts": parts})
		}
		if len(step.Calls) > 0 {
			parts := make([]any, 0, len(step.Calls))
			for _, call := range step.Calls {
				parts = append(parts, map[string]any{
					"functionResponse": map[string]any{
						"name": call.Name, "response": map[string]any{"result": call.Result},
					},
				})
			}
			contents = append(contents, map[string]any{"role": "user", "parts": parts})
		}
	}
	return mustJSON(map[string]any{
		"systemInstruction": map[string]any{"parts": []any{map[string]any{"text": trajectory.System}}},
		"contents":          contents,
		"tools":             []any{map[string]any{"functionDeclarations": functionDeclarations(trajectory.Tools)}},
		"generationConfig":  map[string]any{"maxOutputTokens": 4096, "temperature": 0.2},
	})
}

// Encode renders the trajectory for one protocol.
func Encode(protocol xir.Protocol, trajectory *Trajectory) []byte {
	switch protocol {
	case xir.ProtocolChatCompletions:
		return EncodeChat(trajectory)
	case xir.ProtocolResponses:
		return EncodeResponses(trajectory)
	case xir.ProtocolMessages:
		return EncodeMessages(trajectory)
	case xir.ProtocolGemini:
		return EncodeGemini(trajectory)
	default:
		panic("benchcorpus: unsupported protocol " + string(protocol))
	}
}

// Response renders a short assistant response for one protocol, including the
// protocol-owned reasoning block each real provider sends (Anthropic thinking
// with a signature, Responses encrypted reasoning, Gemini text). The response
// is deliberately small: request bodies carry the context and dominate the
// conversion cost.
func Response(protocol xir.Protocol, trajectory *Trajectory) []byte {
	text := "Done. The failing test was caused by the retry branch reusing the first selection."
	if len(trajectory.Steps) > 0 && trajectory.Steps[len(trajectory.Steps)-1].Assistant != "" {
		text = trajectory.Steps[len(trajectory.Steps)-1].Assistant
	}
	switch protocol {
	case xir.ProtocolChatCompletions:
		return mustJSON(map[string]any{
			"id": "chatcmpl-bench", "object": "chat.completion", "created": 1794816000,
			"model": trajectory.Model,
			"choices": []any{map[string]any{
				"index": 0, "message": map[string]any{
					"role": "assistant", "content": text,
					"reasoning_content": trajectory.Steps[len(trajectory.Steps)-1].Reasoning,
				},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 1024, "completion_tokens": 128, "total_tokens": 1152,
				"prompt_tokens_details": map[string]any{"cached_tokens": 512}},
		})
	case xir.ProtocolMessages:
		return mustJSON(map[string]any{
			"id": "msg_bench", "type": "message", "role": "assistant", "model": trajectory.Model,
			"content": []any{
				map[string]any{"type": "thinking", "thinking": "The conversion branch is only reached when no native target exists.", "signature": "bench-signature"},
				map[string]any{"type": "text", "text": text},
			},
			"stop_reason": "end_turn",
			"usage":       map[string]any{"input_tokens": 1024, "output_tokens": 128},
		})
	case xir.ProtocolResponses:
		return mustJSON(map[string]any{
			"id": "resp_bench", "object": "response", "created_at": 1794816000,
			"status": "completed", "model": trajectory.Model,
			"output": []any{
				map[string]any{"type": "reasoning", "encrypted_content": "gAAAAABbenchmark-encrypted-reasoning"},
				map[string]any{"type": "message", "role": "assistant", "status": "completed",
					"content": []any{map[string]any{"type": "output_text", "text": text, "annotations": []any{}}}},
			},
			"usage": map[string]any{"input_tokens": 1024, "output_tokens": 128, "total_tokens": 1152,
				"input_tokens_details":  map[string]any{"cached_tokens": 512},
				"output_tokens_details": map[string]any{"reasoning_tokens": 64}},
		})
	case xir.ProtocolGemini:
		return mustJSON(map[string]any{
			"candidates": []any{map[string]any{
				"content":      map[string]any{"role": "model", "parts": []any{map[string]any{"text": text}}},
				"finishReason": "STOP", "index": 0,
			}},
			"usageMetadata": map[string]any{"promptTokenCount": 1024, "candidatesTokenCount": 128, "totalTokenCount": 1152},
			"modelVersion":  "gemini-bench",
		})
	}
	panic("benchcorpus: unsupported protocol " + string(protocol))
}

// LongResponse renders a response whose final text is at least target bytes, for
// response-side parsing, masking and audit benchmarks.
func LongResponse(protocol xir.Protocol, target int, opts Options) []byte {
	var text strings.Builder
	text.Grow(target + 1024)
	for text.Len() < target {
		text.WriteString(benchNoise)
		text.WriteString(sensitiveLines(text.Len()/1024, opts))
		text.WriteString("\n")
	}
	switch protocol {
	case xir.ProtocolChatCompletions:
		return mustJSON(map[string]any{
			"id": "chatcmpl-bench-long", "object": "chat.completion", "created": 1794816000,
			"model": "gpt-5-bench",
			"choices": []any{map[string]any{
				"index": 0, "message": map[string]any{"role": "assistant", "content": text.String()},
				"finish_reason": "stop",
			}},
			"usage": map[string]any{"prompt_tokens": 1024, "completion_tokens": target / 4, "total_tokens": 1024 + target/4},
		})
	case xir.ProtocolMessages:
		return mustJSON(map[string]any{
			"id": "msg_bench_long", "type": "message", "role": "assistant", "model": "gpt-5-bench",
			"content":     []any{map[string]any{"type": "text", "text": text.String()}},
			"stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 1024, "output_tokens": target / 4},
		})
	case xir.ProtocolResponses:
		return mustJSON(map[string]any{
			"id": "resp_bench_long", "object": "response", "created_at": 1794816000,
			"status": "completed", "model": "gpt-5-bench",
			"output": []any{map[string]any{"type": "message", "role": "assistant", "status": "completed",
				"content": []any{map[string]any{"type": "output_text", "text": text.String(), "annotations": []any{}}}}},
			"usage": map[string]any{"input_tokens": 1024, "output_tokens": target / 4, "total_tokens": 1024 + target/4},
		})
	case xir.ProtocolGemini:
		return mustJSON(map[string]any{
			"candidates": []any{map[string]any{
				"content":      map[string]any{"role": "model", "parts": []any{map[string]any{"text": text.String()}}},
				"finishReason": "STOP", "index": 0,
			}},
			"usageMetadata": map[string]any{"promptTokenCount": 1024, "candidatesTokenCount": target / 4, "totalTokenCount": 1024 + target/4},
		})
	}
	panic("benchcorpus: unsupported protocol " + string(protocol))
}

// ChatStreamEvents returns a text stream of at least target bytes as normalized
// events, for the SSE assembler benchmark. Tool-call deltas are included so the
// assembler's open-call path is exercised as well.
func ChatStreamEvents(target int) []*xir.StreamEvent {
	const chunk = benchNoise
	events := make([]*xir.StreamEvent, 0, target/len(chunk)+8)
	events = append(events, &xir.StreamEvent{
		Kind: xir.StreamStart, Protocol: xir.ProtocolChatCompletions, ToolID: "chatcmpl-bench", ToolName: "gpt-5-bench",
	})
	for size := 0; size < target; size += len(chunk) {
		events = append(events, &xir.StreamEvent{
			Kind: xir.StreamText, Protocol: xir.ProtocolChatCompletions, Text: chunk,
		})
	}
	usageValue := usage.Usage{InputTokens: 1024, OutputTokens: 128, TotalTokens: 1152}
	events = append(events,
		&xir.StreamEvent{Kind: xir.StreamToolStart, Protocol: xir.ProtocolChatCompletions, ToolID: "call_1", ToolName: "read_file"},
		&xir.StreamEvent{Kind: xir.StreamToolArgs, Protocol: xir.ProtocolChatCompletions, Text: `{"path":"internal/proxy/handler.go"}`},
		&xir.StreamEvent{Kind: xir.StreamToolEnd, Protocol: xir.ProtocolChatCompletions, ToolID: "call_1"},
		&xir.StreamEvent{Kind: xir.StreamUsage, Protocol: xir.ProtocolChatCompletions, Usage: &usageValue},
		&xir.StreamEvent{Kind: xir.StreamStop, Protocol: xir.ProtocolChatCompletions, StopReason: xir.FinishStop},
	)
	return events
}
