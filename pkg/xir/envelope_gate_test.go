package xir

// ParseEnvelope 的分配门禁与基准。典型 Chat Completions body：含 tools、
// stream_options、messages、max_completion_tokens 与 response_format。

import "testing"

// gateChatBody 是门禁与基准共用的典型 chat 请求体（约 0.9 KiB）。
const gateChatBody = `{"model":"gpt-5.1","stream":true,"stream_options":{"include_usage":true},` +
	`"max_completion_tokens":2048,"temperature":0.7,"response_format":{"type":"json_object"},` +
	`"tools":[{"type":"function","function":{"name":"read_file","description":"Read a file from disk",` +
	`"parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}}],` +
	`"messages":[{"role":"system","content":"You are a helpful assistant."},` +
	`{"role":"user","content":"Please read the config file and summarize it."},` +
	`{"role":"assistant","content":"I will read the file now.","tool_calls":[{"id":"call_1","type":"function",` +
	`"function":{"name":"read_file","arguments":"{\"path\":\"config.yaml\"}"}}]},` +
	`{"role":"tool","tool_call_id":"call_1","content":"database:\n  driver: sqlite\n"}]}`

// TestAllocGateParseEnvelope 是 ParseEnvelope 的分配门禁。
// 被测函数：xir.ParseEnvelope（gateChatBody，含 tools/stream_options 的典型 chat body）。
// 基线测量值：176 allocs/op（本机 linux/arm64，2026-11-16，-benchtime=100x）。
// 上限 = 基线 + 约 25% 余量取整 = 220。提高上限必须在 PR 说明理由。
func TestAllocGateParseEnvelope(t *testing.T) {
	if allocGateInstrumented {
		t.Skip("allocation gates are measured without -race instrumentation")
	}
	body := []byte(gateChatBody)
	const limit = 220
	got := testing.AllocsPerRun(100, func() {
		if _, err := ParseEnvelope(body); err != nil {
			t.Fatal(err)
		}
	})
	if got > limit {
		t.Fatalf("ParseEnvelope allocs/op = %.0f, gate limit %d (baseline 176, arm64 2026-11-16); 提高上限必须在 PR 说明理由", got, limit)
	}
}

func BenchmarkParseEnvelope(b *testing.B) {
	body := []byte(gateChatBody)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		env, err := ParseEnvelope(body)
		if err != nil || env.Model != "gpt-5.1" || !env.Stream || !env.Tools || !env.StructuredOutput || env.MaxTokens != 2048 {
			b.Fatalf("unexpected envelope: %+v err=%v", env, err)
		}
	}
}
