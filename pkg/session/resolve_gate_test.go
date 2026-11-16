package session

// Resolve 补充单测 + 头快路径分配门禁与基准。
// session_test.go 已覆盖：Session_id 优先于 X-Claude-Code-Session-Id、
// X-Codex-Window-Id 的 : 前缀、X-Codex-Turn-Metadata.session_id、五种 body
// 回退、malformed body、fingerprint/compose。这里只补缺口：
// 通用头对 Codex 信号的优先级、头对 body 的优先级、空白头/空 session 行为，
// 以及 Resolve 头快路径的 allocs 门禁。

import (
	"net/http"
	"testing"
)

func TestResolveGenericHeaderWinsOverCodexSignals(t *testing.T) {
	h := http.Header{}
	h.Set("Session_id", "generic")
	h.Set("X-Codex-Window-Id", "window-1:turn-2")
	h.Set("X-Codex-Turn-Metadata", `{"session_id":"codex-turn"}`)
	ref := Resolve(h, nil)
	if ref.Key != "generic" || ref.Source != "header:session_id" {
		t.Fatalf("ref = %+v, want generic header to win", ref)
	}
}

func TestResolveClaudeHeaderWinsOverCodexSignals(t *testing.T) {
	h := http.Header{}
	h.Set("X-Claude-Code-Session-Id", "claude-session")
	h.Set("X-Codex-Window-Id", "window-1:turn-2")
	h.Set("X-Codex-Turn-Metadata", `{"session_id":"codex-turn"}`)
	ref := Resolve(h, nil)
	if ref.Key != "claude-session" || ref.Confidence != ConfidenceHigh || ref.Source != "header:x-claude-code-session-id" {
		t.Fatalf("ref = %+v, want claude header to win", ref)
	}
}

func TestResolveWindowIDWinsOverTurnMetadata(t *testing.T) {
	h := http.Header{}
	h.Set("X-Codex-Window-Id", "window-9:turn-1")
	h.Set("X-Codex-Turn-Metadata", `{"session_id":"codex-turn"}`)
	ref := Resolve(h, nil)
	if ref.Key != "window-9" || ref.Source != "header:x-codex-window-id" {
		t.Fatalf("ref = %+v, want window prefix to win over turn metadata", ref)
	}
}

func TestResolveHeaderWinsOverBody(t *testing.T) {
	h := http.Header{}
	h.Set("Session_id", "from-header")
	ref := Resolve(h, []byte(`{"previous_response_id":"resp_1","session_id":"from-body"}`))
	if ref.Key != "from-header" || ref.Kind != KindHeader {
		t.Fatalf("ref = %+v, want header to win over body refs", ref)
	}
}

func TestResolveBlankHeaderFallsThrough(t *testing.T) {
	h := http.Header{}
	h.Set("Session_id", "   ")
	h.Set("X-Claude-Code-Session-Id", "claude-session")
	ref := Resolve(h, nil)
	if ref.Key != "claude-session" {
		t.Fatalf("ref = %+v, want blank generic header to fall through", ref)
	}

	// 空白前缀的 window id 同样应跳过，落到 turn metadata。
	h2 := http.Header{}
	h2.Set("X-Codex-Window-Id", "  :turn-2")
	h2.Set("X-Codex-Turn-Metadata", `{"session_id":"codex-turn"}`)
	if ref := Resolve(h2, nil); ref.Key != "codex-turn" {
		t.Fatalf("ref = %+v, want blank window prefix to fall through", ref)
	}

	// 无 : 的 window id 取整个值。
	h3 := http.Header{}
	h3.Set("X-Codex-Window-Id", "window-only")
	if ref := Resolve(h3, nil); ref.Key != "window-only" {
		t.Fatalf("ref = %+v, want whole window id without colon", ref)
	}
}

func TestResolveEmptySessionBehavior(t *testing.T) {
	cases := []struct {
		name    string
		headers http.Header
		body    []byte
	}{
		{"nil headers and body", nil, nil},
		{"empty headers and body", http.Header{}, []byte{}},
		{"whitespace body", nil, []byte("   \n\t")},
		{"blank body session_id", nil, []byte(`{"session_id":"   "}`)},
		{"blank header and blank body", func() http.Header {
			h := http.Header{}
			h.Set("Session_id", "  ")
			return h
		}(), []byte(`{"session_id":""}`)},
		{"non-object body", nil, []byte(`"just a string"`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ref := Resolve(tc.headers, tc.body)
			if ref.Valid() {
				t.Fatalf("Resolve returned a valid ref for empty session: %+v", ref)
			}
			if ref.Key != "" || ref.Fingerprint != "" || ref.Kind != "" || ref.Source != "" {
				t.Fatalf("empty session ref should be zero-valued: %+v", ref)
			}
			if BindingFingerprint(ref, "gpt-5") != "" {
				t.Fatal("empty session must not produce a binding fingerprint")
			}
		})
	}
}

// --- 分配门禁与基准 ---

func gateResolveHeader() http.Header {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Set("Authorization", "Bearer sk-test")
	h.Set("Session_id", "gate-session-0123456789")
	return h
}

// TestAllocGateResolveHeaderFastPath 是 Resolve 头快路径的分配门禁。
// 被测函数：session.Resolve（Session_id 头命中，不读 body）。
// 基线测量值：1 allocs/op（本机 linux/arm64，2026-11-16，-benchtime=100x；
// 唯一分配是 fingerprint 的 "sha256:"+hex 拼接）。
// 上限 = 基线 + 约 25% 余量取整 = 2。提高上限必须在 PR 说明理由。
func TestAllocGateResolveHeaderFastPath(t *testing.T) {
	if allocGateInstrumented {
		t.Skip("allocation gates are measured without -race instrumentation")
	}
	h := gateResolveHeader()
	const limit = 2
	got := testing.AllocsPerRun(100, func() {
		if ref := Resolve(h, nil); ref.Key != "gate-session-0123456789" {
			t.Fatalf("unexpected key %q", ref.Key)
		}
	})
	if got > limit {
		t.Fatalf("Resolve(header) allocs/op = %.0f, gate limit %d (baseline 1, arm64 2026-11-16); 提高上限必须在 PR 说明理由", got, limit)
	}
}

// TestAllocGateResolveBodyFallback 是 Resolve body 回退路径的分配门禁。
// 被测函数：session.Resolve（无头信号，body previous_response_id 命中，
// 定向扫描发现候选键后走精确 json.Unmarshal 回退）。
// 基线测量值：2 allocs/op（本机 linux/arm64，2026-11-16，-benchtime=100x；
// P4 去重：bytes.TrimSpace 取代 string/[]byte 双重全量拷贝，4 -> 2，
// 剩余分配为解码命中字符串与 fingerprint 拼接）。
// 上限 = 基线 + 约 25% 余量取整 = 3。提高上限必须在 PR 说明理由。
func TestAllocGateResolveBodyFallback(t *testing.T) {
	if allocGateInstrumented {
		t.Skip("allocation gates are measured without -race instrumentation")
	}
	body := []byte(`{"model":"gpt-5","previous_response_id":"resp_gate_1","messages":[{"role":"user","content":"hello"}]}`)
	const limit = 3
	got := testing.AllocsPerRun(100, func() {
		if ref := Resolve(nil, body); ref.Key != "resp_gate_1" {
			t.Fatalf("unexpected key %q", ref.Key)
		}
	})
	if got > limit {
		t.Fatalf("Resolve(body) allocs/op = %.0f, gate limit %d (baseline 2, arm64 2026-11-16, P4 免拷贝); 提高上限必须在 PR 说明理由", got, limit)
	}
}

// TestAllocGateResolveBodyNoSignal 是 Resolve 无信号 body 快路径的分配门禁。
// 被测函数：session.Resolve（无头信号，body 不含任何 session 键——多数 SDK
// 流量的形态；P4 定向扫描直接证明无信号，不做全量 json.Unmarshal）。
// 基线测量值：0 allocs/op（本机 linux/arm64，2026-11-16，-benchtime=100x；
// P4 之前同形 body 为 5 allocs/op、约 1133 B/op 的全量解码）。
// 上限 = 0：快路径必须保持零分配。提高上限必须在 PR 说明理由。
func TestAllocGateResolveBodyNoSignal(t *testing.T) {
	if allocGateInstrumented {
		t.Skip("allocation gates are measured without -race instrumentation")
	}
	body := []byte(gateResolveNoSignalBody)
	const limit = 0
	got := testing.AllocsPerRun(100, func() {
		if ref := Resolve(nil, body); ref.Valid() {
			t.Fatalf("unexpected session ref: %+v", ref)
		}
	})
	if got > limit {
		t.Fatalf("Resolve(no-signal body) allocs/op = %.0f, gate limit %d (baseline 0, arm64 2026-11-16, P4 定向扫描); 提高上限必须在 PR 说明理由", got, limit)
	}
}

func BenchmarkResolveHeaderFastPath(b *testing.B) {
	h := gateResolveHeader()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ref := Resolve(h, nil)
		if ref.Key != "gate-session-0123456789" {
			b.Fatal("unexpected key")
		}
	}
}

func BenchmarkResolveBodyFallback(b *testing.B) {
	body := []byte(`{"model":"gpt-5","previous_response_id":"resp_gate_1","messages":[{"role":"user","content":"hello"}]}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ref := Resolve(nil, body)
		if ref.Key != "resp_gate_1" {
			b.Fatal("unexpected key")
		}
	}
}

// gateResolveNoSignalBody 是不含任何 session 信号的典型 SDK 请求体（Anthropic
// messages 形状，metadata 只有 user_id）。多数 SDK 流量走这条路径：P4 之前
// fromBody 对它做全量 json.Unmarshal，P4 之后顶层键定向扫描即可证明无信号。
const gateResolveNoSignalBody = `{"model":"claude-sonnet-4-5","max_tokens":1024,"stream":true,` +
	`"metadata":{"user_id":"user_9f2c"},"system":[{"type":"text","text":"You are a helpful coding assistant."}],` +
	`"messages":[{"role":"user","content":[{"type":"text","text":"Please read the config file and summarize it."}]},` +
	`{"role":"assistant","content":[{"type":"text","text":"I will read the file now."}]},` +
	`{"role":"user","content":[{"type":"text","text":"Thanks, also check the logs directory."}]}]}`

func BenchmarkResolveBodyNoSignal(b *testing.B) {
	body := []byte(gateResolveNoSignalBody)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if ref := Resolve(nil, body); ref.Valid() {
			b.Fatalf("unexpected session ref: %+v", ref)
		}
	}
}
