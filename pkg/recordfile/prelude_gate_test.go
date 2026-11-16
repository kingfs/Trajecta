package recordfile

// ParsePrelude 的分配门禁与基准（V3 与 V2 各一）。fixture 为自构造的最小
// 合法 cassette：V3 用 MarshalPrelude + BuildEvents，V2 用固定 2KiB JSON
// 头块。大 fixture 的 V3 基准另见 recordfile_bench_test.go 的
// BenchmarkParsePreludeV3；这里的最小 fixture 度量索引器常见的轻量 prelude。

import (
	"encoding/json"
	"testing"
	"time"
)

func gatePreludeHeader(version string) RecordHeader {
	return RecordHeader{
		Version: version,
		Meta: MetaData{
			RequestID:  "req-prelude-gate",
			Time:       time.Date(2026, 11, 16, 10, 0, 0, 0, time.UTC),
			Model:      "gpt-5.1",
			Provider:   "openai_compatible",
			Operation:  "chat_completions",
			Endpoint:   "/v1/chat/completions",
			URL:        "/v1/chat/completions",
			Method:     "POST",
			StatusCode: 200,
			DurationMs: 120,
			TTFTMs:     15,
		},
		Layout: LayoutInfo{ReqHeaderLen: 64, ReqBodyLen: 32, ResHeaderLen: 48, ResBodyLen: 32},
		Usage:  UsageInfo{PromptTokens: 10, CompletionTokens: 4, TotalTokens: 14},
	}
}

func gatePreludeV3Content(tb testing.TB) []byte {
	tb.Helper()
	header := gatePreludeHeader("LLM_PROXY_V3")
	prelude, err := MarshalPrelude(header, BuildEvents(header))
	if err != nil {
		tb.Fatalf("MarshalPrelude() error = %v", err)
	}
	payload := "POST /v1/chat/completions HTTP/1.1\r\nHost: example.com\r\n\r\n" +
		`{"model":"gpt-5.1","stream":false}` + "\n" +
		"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n" +
		`{"id":"chatcmpl-gate","choices":[]}`
	return append(append([]byte{}, prelude...), payload...)
}

func gatePreludeV2Content(tb testing.TB) []byte {
	tb.Helper()
	header := gatePreludeHeader("LLM_PROXY_V2")
	blob, err := json.Marshal(header)
	if err != nil {
		tb.Fatalf("json.Marshal(header) error = %v", err)
	}
	if len(blob) >= LegacyHeaderLen {
		tb.Fatalf("legacy header blob %d bytes exceeds the fixed block", len(blob))
	}
	block := make([]byte, LegacyHeaderLen)
	copy(block, blob)
	block[len(blob)] = '\n'
	payload := "POST /v1/chat/completions HTTP/1.1\r\nHost: example.com\r\n\r\n" +
		`{"model":"gpt-4","stream":false}` + "\n" +
		"HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n" +
		`{"id":"chatcmpl-gate-v2","choices":[]}`
	return append(block, payload...)
}

// TestAllocGateParsePreludeV3 是 V3 prelude 解析的分配门禁。
// 被测函数：recordfile.ParsePrelude（自构造最小合法 V3 cassette）。
// 基线测量值：6 allocs/op（本机 linux/arm64，2026-11-16，-benchtime=100x）。
// 上限 = 基线 + 约 25% 余量取整 = 8。提高上限必须在 PR 说明理由。
func TestAllocGateParsePreludeV3(t *testing.T) {
	if allocGateInstrumented {
		t.Skip("allocation gates are measured without -race instrumentation")
	}
	content := gatePreludeV3Content(t)
	const limit = 8
	got := testing.AllocsPerRun(100, func() {
		if _, err := ParsePrelude(content); err != nil {
			t.Fatal(err)
		}
	})
	if got > limit {
		t.Fatalf("ParsePrelude(V3) allocs/op = %.0f, gate limit %d (baseline 6, arm64 2026-11-16); 提高上限必须在 PR 说明理由", got, limit)
	}
}

// TestAllocGateParsePreludeV2 是 legacy V2 固定 2KiB 头块解析的分配门禁。
// 被测函数：recordfile.ParsePrelude（自构造最小合法 V2 cassette）。
// 基线测量值：2 allocs/op（本机 linux/arm64，2026-11-16，-benchtime=100x）。
// 上限 = 基线 + 约 25% 余量取整 = 3。提高上限必须在 PR 说明理由。
func TestAllocGateParsePreludeV2(t *testing.T) {
	if allocGateInstrumented {
		t.Skip("allocation gates are measured without -race instrumentation")
	}
	content := gatePreludeV2Content(t)
	const limit = 3
	got := testing.AllocsPerRun(100, func() {
		if _, err := ParsePrelude(content); err != nil {
			t.Fatal(err)
		}
	})
	if got > limit {
		t.Fatalf("ParsePrelude(V2) allocs/op = %.0f, gate limit %d (baseline 2, arm64 2026-11-16); 提高上限必须在 PR 说明理由", got, limit)
	}
}

func BenchmarkParsePreludeV3Minimal(b *testing.B) {
	content := gatePreludeV3Content(b)
	b.SetBytes(int64(len(content)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		parsed, err := ParsePrelude(content)
		if err != nil || parsed.Header.Meta.RequestID != "req-prelude-gate" {
			b.Fatalf("parse failed: %v", err)
		}
	}
}

func BenchmarkParsePreludeV2(b *testing.B) {
	content := gatePreludeV2Content(b)
	b.SetBytes(int64(len(content)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		parsed, err := ParsePrelude(content)
		if err != nil || parsed.Header.Version != "LLM_PROXY_V2" || parsed.PayloadOffset != LegacyHeaderLen {
			b.Fatalf("parse failed: %v", err)
		}
	}
}
