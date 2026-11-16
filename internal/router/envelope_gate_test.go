package router

// extractRequestFeatures 的分配门禁与基准。
//
// P4 已修复双解析：extractRequestFeatures 现在只 ParseEnvelope 一次，
// model 解析（modelFromEnvelope）与 stream/tools/max_tokens 提取复用同一个
// env；Explain 的 dry-run 也改为一次特征提取。本门禁锁住单解析基线防止恶化。

import "testing"

// routerGateChatBody 与 pkg/xir/envelope_gate_test.go 的典型 chat body 同形。
const routerGateChatBody = `{"model":"gpt-5.1","stream":true,"stream_options":{"include_usage":true},` +
	`"max_completion_tokens":2048,"temperature":0.7,"response_format":{"type":"json_object"},` +
	`"tools":[{"type":"function","function":{"name":"read_file","description":"Read a file from disk",` +
	`"parameters":{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}}}],` +
	`"messages":[{"role":"system","content":"You are a helpful assistant."},` +
	`{"role":"user","content":"Please read the config file and summarize it."},` +
	`{"role":"assistant","content":"I will read the file now.","tool_calls":[{"id":"call_1","type":"function",` +
	`"function":{"name":"read_file","arguments":"{\"path\":\"config.yaml\"}"}}]},` +
	`{"role":"tool","tool_call_id":"call_1","content":"database:\n  driver: sqlite\n"}]}`

// TestAllocGateExtractRequestFeatures 是 extractRequestFeatures 的分配门禁。
// 被测函数：router.extractRequestFeatures（routerGateChatBody，/v1/chat/completions）。
// 基线测量值：190 allocs/op（本机 linux/arm64，2026-11-16，-benchtime=100x；
// P4 去重：ParseEnvelope 两遍合并为一遍，366 -> 190，约等于单次 ParseEnvelope
// 的 176 加特征组装开销）。上限 = 基线 + 约 25% 余量取整 = 238。
// 提高上限必须在 PR 说明理由。
func TestAllocGateExtractRequestFeatures(t *testing.T) {
	if allocGateInstrumented {
		t.Skip("allocation gates are measured without -race instrumentation")
	}
	body := []byte(routerGateChatBody)
	const limit = 238
	got := testing.AllocsPerRun(100, func() {
		_ = extractRequestFeatures("/v1/chat/completions", body)
	})
	if got > limit {
		t.Fatalf("extractRequestFeatures allocs/op = %.0f, gate limit %d (baseline 190, arm64 2026-11-16, P4 单解析); 提高上限必须在 PR 说明理由", got, limit)
	}
}

func BenchmarkExtractRequestFeatures(b *testing.B) {
	body := []byte(routerGateChatBody)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		features := extractRequestFeatures("/v1/chat/completions", body)
		if features.ModelName != "gpt-5.1" || !features.Stream || !features.HasTools || !features.HasStructuredOutput || features.MaxTokens != 2048 {
			b.Fatalf("unexpected features: %+v", features)
		}
	}
}
