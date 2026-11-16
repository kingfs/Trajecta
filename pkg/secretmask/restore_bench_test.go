package secretmask

// 流式 Restorer 还原含假值流的基准。fixture 是一条 Chat SSE 流：凭据
// （规则正样本）出现在 delta.content 里，先整体 Mask 得到含假值的流，再让
// Restorer 以不同读分片还原，断言逐字节回到原始流。与 bench_test.go 的
// BenchmarkRestoreStreamingChunks（整块 JSON body）互补：这里假值被 SSE
// JSON 信封包围，且分片可能把假值切成多段，触发 hold-back 路径。

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// gateRestoreSSEStream 构造含凭据的 SSE 流（原始明文）。
func gateRestoreSSEStream() []byte {
	secrets := []string{
		sampleFor(RuleOpenAILegacy),
		sampleFor(RuleAnthropicAPIKey),
		sampleFor(RuleAWSAccessKeyID),
	}
	var b strings.Builder
	for i := 0; i < 24; i++ {
		b.WriteString(`data: {"id":"chatcmpl-gate","object":"chat.completion.chunk","choices":[{"index":0,"delta":{"content":"`)
		b.WriteString(benchNoiseBlock)
		b.WriteString(`OPENAI_API_KEY=`)
		b.WriteString(secrets[i%len(secrets)])
		b.WriteString(`"}}]}` + "\n\n")
	}
	b.WriteString("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":24,\"total_tokens\":34}}\n\n")
	b.WriteString("data: [DONE]\n\n")
	return []byte(b.String())
}

func BenchmarkRestorerStreamWithFakes(b *testing.B) {
	original := gateRestoreSSEStream()
	v := NewVault([]byte("secretmask-restore-gate-seed"))
	masked, events := v.Mask(original)
	if len(events) == 0 {
		b.Fatal("SSE fixture produced no mask events")
	}
	if len(masked) != len(original) {
		b.Fatalf("mask changed stream length: %d -> %d", len(original), len(masked))
	}
	for _, chunk := range []int{512, 4 << 10, 32 << 10} {
		b.Run(benchSizeLabel(chunk), func(b *testing.B) {
			b.SetBytes(int64(len(masked)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				r := NewRestorer(&chunkReader{data: masked, n: chunk}, v)
				got, err := io.ReadAll(r)
				if err != nil {
					b.Fatal(err)
				}
				if !bytes.Equal(got, original) {
					b.Fatal("restored stream differs from the original")
				}
				if r.Restored() == 0 {
					b.Fatal("restorer reported zero restorations")
				}
			}
		})
	}
}
