package router

// extractRequestFeatures 的分配门禁与基准。
//
// 已知低效点（P4 修复目标）：extractRequestFeatures 目前会 ParseEnvelope
// 两遍——requestModel(rawPath, body) 内部解析一次取 model，随后本函数再
// 解析一次取 stream/tools/max_tokens。本门禁锁住现状防止恶化，不修改生产
// 代码；P4 合并为一次解析后应下调上限（并在 PR 中说明新基线）。

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
// 基线测量值：366 allocs/op（本机 linux/arm64，2026-11-16，-benchtime=100x）。
// 上限 = 基线 + 约 25% 余量取整 = 458。提高上限必须在 PR 说明理由。
// 注意：基线包含 ParseEnvelope 两遍的已知低效（见文件头注释）；P4 合并为
// 一次解析后应下调本上限，而不是沿用 458。
func TestAllocGateExtractRequestFeatures(t *testing.T) {
	body := []byte(routerGateChatBody)
	const limit = 458
	got := testing.AllocsPerRun(100, func() {
		_ = extractRequestFeatures("/v1/chat/completions", body)
	})
	if got > limit {
		t.Fatalf("extractRequestFeatures allocs/op = %.0f, gate limit %d (baseline 366, arm64 2026-11-16); 提高上限必须在 PR 说明理由", got, limit)
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
