package gateway

// UsageSniffer 专属单测：非流式 usage 抽取（canonical pkg/usage 与 legacy
// RecordUsage 投影一致）、流式 SSE 末帧 usage（stream_options.include_usage
// 语义）、cassette 字节保真、Close 语义、无 usage 响应不报错。
// response_pipeline_test.go 已覆盖 nil 安全与 responses 端点的 Feed 投影，
// 这里不重复，只补 Read 路径与 Chat/SSE 形态。

import (
	"bytes"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/recorder"
	"github.com/kingfs/Trajecta/pkg/llm"
	"github.com/kingfs/Trajecta/pkg/usage"
)

// usageSnifferCloseTracker 记录 Close 调用次数，验证 sniffer 关闭上游链。
type usageSnifferCloseTracker struct {
	io.Reader
	closed int
}

func (c *usageSnifferCloseTracker) Close() error {
	c.closed++
	return nil
}

// readAllInChunks 用固定小缓冲读完整条流，模拟 HTTP copy 的分片读取。
func readAllInChunks(t *testing.T, r io.Reader, chunk int) []byte {
	t.Helper()
	var out bytes.Buffer
	buf := make([]byte, chunk)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			out.Write(buf[:n])
		}
		if err == io.EOF {
			return out.Bytes()
		}
		if err != nil {
			t.Fatalf("read error: %v", err)
		}
	}
}

func TestUsageSnifferNonStreamChatUsageProjectionsAgree(t *testing.T) {
	body := []byte(`{"id":"chatcmpl-1","object":"chat.completion","model":"gpt-5","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":120,"completion_tokens":30,"total_tokens":150,"prompt_tokens_details":{"cached_tokens":64}}}`)
	var canonical usage.Usage
	var record recorder.UsageInfo
	var cassette bytes.Buffer
	var count int64
	var events []recorder.RecordEvent
	var ttft int64
	src := &usageSnifferCloseTracker{Reader: bytes.NewReader(body)}
	sniffer := &UsageSniffer{
		Source:      src,
		File:        &cassette,
		Count:       &count,
		Usage:       &canonical,
		RecordUsage: &record,
		Pipeline:    llm.NewResponsePipeline(llm.ProviderOpenAICompatible, "/v1/chat/completions", false),
		Events:      &events,
		Start:       time.Now().Add(-5 * time.Millisecond),
		TTFTMs:      &ttft,
	}
	got := readAllInChunks(t, sniffer, 7)
	if !bytes.Equal(got, body) {
		t.Fatalf("client bytes differ from upstream body")
	}
	// 非流式的 usage 在 Finalize（Close）时才从尾部抽取。
	if err := sniffer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if canonical.InputTokens != 120 || canonical.OutputTokens != 30 || canonical.TotalTokens != 150 || canonical.CacheReadTokens != 64 {
		t.Fatalf("canonical usage = %+v", canonical)
	}
	// legacy 投影必须与 canonical 完全一致。
	if record.PromptTokens != canonical.InputTokens ||
		record.CompletionTokens != canonical.OutputTokens ||
		record.TotalTokens != canonical.TotalTokens {
		t.Fatalf("record usage = %+v, canonical = %+v", record, canonical)
	}
	if record.PromptTokenDetails == nil || record.PromptTokenDetails.CachedTokens != canonical.CacheReadTokens {
		t.Fatalf("record cached tokens = %+v, want %d", record.PromptTokenDetails, canonical.CacheReadTokens)
	}
	if !bytes.Equal(cassette.Bytes(), body) || count != int64(len(body)) {
		t.Fatalf("cassette fidelity: count=%d len=%d equal=%v", count, len(body), bytes.Equal(cassette.Bytes(), body))
	}
	if src.closed != 1 {
		t.Fatalf("source Close calls = %d, want 1", src.closed)
	}
	if ttft <= 0 {
		t.Fatalf("TTFT = %d, want > 0 after a first read", ttft)
	}
	if !usageSnifferHasUsageEvent(events) {
		t.Fatalf("events missing llm.usage: %+v", events)
	}
}

func TestUsageSnifferStreamSSEFinalUsageFrame(t *testing.T) {
	// stream_options.include_usage=true 的 Chat SSE：usage 只出现在
	// choices 为空的末帧，随后是 [DONE]。
	stream := []byte("data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hi\"}}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":5,\"total_tokens\":17}}\n\n" +
		"data: [DONE]\n\n")
	var canonical usage.Usage
	var record recorder.UsageInfo
	var cassette bytes.Buffer
	var count int64
	var events []recorder.RecordEvent
	src := &usageSnifferCloseTracker{Reader: bytes.NewReader(stream)}
	sniffer := &UsageSniffer{
		Source:      src,
		File:        &cassette,
		Count:       &count,
		Usage:       &canonical,
		RecordUsage: &record,
		Pipeline:    llm.NewResponsePipeline(llm.ProviderOpenAICompatible, "/v1/chat/completions", true),
		Events:      &events,
	}
	got := readAllInChunks(t, sniffer, 13)
	if !bytes.Equal(got, stream) {
		t.Fatal("client stream bytes differ from upstream")
	}
	// 流式 usage 在 Feed 到末帧时就已可用，不必等 Close。
	if canonical.InputTokens != 12 || canonical.OutputTokens != 5 || canonical.TotalTokens != 17 {
		t.Fatalf("canonical usage before Close = %+v", canonical)
	}
	if err := sniffer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if record.PromptTokens != 12 || record.CompletionTokens != 5 || record.TotalTokens != 17 {
		t.Fatalf("record usage = %+v", record)
	}
	if !bytes.Equal(cassette.Bytes(), stream) || count != int64(len(stream)) {
		t.Fatalf("cassette must keep SSE framing byte for byte: count=%d equal=%v", count, bytes.Equal(cassette.Bytes(), stream))
	}
	if src.closed != 1 {
		t.Fatalf("source Close calls = %d, want 1", src.closed)
	}
	if !usageSnifferHasUsageEvent(events) {
		t.Fatalf("events missing llm.usage: %+v", events)
	}
}

type failingCassetteWriter struct {
	written int
	err     error
}

func (w *failingCassetteWriter) Write(p []byte) (int, error) {
	if w.written > len(p) {
		w.written = len(p)
	}
	return w.written, w.err
}

func TestUsageSnifferReportsCassetteWriteErrorsOnClose(t *testing.T) {
	payload := []byte("client-visible response")
	writeErr := errors.New("disk full")
	var count int64
	sniffer := &UsageSniffer{
		Source: io.NopCloser(bytes.NewReader(payload)),
		File:   &failingCassetteWriter{written: 3, err: writeErr},
		Count:  &count,
	}

	got, err := io.ReadAll(sniffer)
	if err != nil {
		t.Fatalf("io.ReadAll() error = %v, want the client stream to continue", err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("client stream = %q, want %q", got, payload)
	}
	if count != 3 {
		t.Fatalf("cassette byte count = %d, want actual partial write of 3", count)
	}
	if err := sniffer.Close(); err == nil || !errors.Is(err, writeErr) {
		t.Fatalf("Close() error = %v, want to report %v", err, writeErr)
	}
}

func TestUsageSnifferDetectsShortCassetteWrite(t *testing.T) {
	var count int64
	sniffer := &UsageSniffer{
		Source: io.NopCloser(bytes.NewReader([]byte("abcdef"))),
		File:   &failingCassetteWriter{written: 2},
		Count:  &count,
	}
	if _, err := io.ReadAll(sniffer); err != nil {
		t.Fatalf("io.ReadAll() error = %v", err)
	}
	if err := sniffer.Close(); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("Close() error = %v, want io.ErrShortWrite", err)
	}
	if count != 2 {
		t.Fatalf("cassette byte count = %d, want 2", count)
	}
}

func TestUsageSnifferCassetteByteFidelity(t *testing.T) {
	// sniffer 读到的字节必须原样进 cassette：不做规范化、不丢 \r\n、
	// 不改 JSON 空白。构造含 CRLF、尾随空白与多字节字符的载荷。
	payload := []byte("data: {\"choices\":[{\"delta\":{\"content\":\"héllo 世界\"}}]}\r\n\r\n" +
		"data:  {\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}  \n\n" +
		"data: [DONE]\n\n")
	var cassette bytes.Buffer
	var count int64
	sniffer := &UsageSniffer{
		Source:   io.NopCloser(bytes.NewReader(payload)),
		File:     &cassette,
		Count:    &count,
		Pipeline: llm.NewResponsePipeline(llm.ProviderOpenAICompatible, "/v1/chat/completions", true),
	}
	got := readAllInChunks(t, sniffer, 5)
	if !bytes.Equal(got, payload) {
		t.Fatal("sniffer altered the client-visible bytes")
	}
	if err := sniffer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if !bytes.Equal(cassette.Bytes(), payload) {
		t.Fatalf("cassette bytes differ:\n got %q\nwant %q", cassette.Bytes(), payload)
	}
	if count != int64(len(payload)) {
		t.Fatalf("count = %d, want %d", count, len(payload))
	}
}

func TestUsageSnifferCloseFinalizesUsageAndEvents(t *testing.T) {
	body := []byte(`{"id":"chatcmpl-2","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`)
	var canonical usage.Usage
	var events []recorder.RecordEvent
	src := &usageSnifferCloseTracker{Reader: bytes.NewReader(body)}
	sniffer := &UsageSniffer{
		Source:   src,
		Usage:    &canonical,
		Pipeline: llm.NewResponsePipeline(llm.ProviderOpenAICompatible, "/v1/chat/completions", false),
		Events:   &events,
	}
	if _, err := io.ReadAll(sniffer); err != nil {
		t.Fatal(err)
	}
	if canonical.TotalTokens != 0 {
		t.Fatalf("non-stream usage leaked before Finalize: %+v", canonical)
	}
	if len(events) != 0 {
		t.Fatalf("events appended before Close: %+v", events)
	}
	if err := sniffer.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if canonical.TotalTokens != 5 {
		t.Fatalf("usage after Close = %+v", canonical)
	}
	if !usageSnifferHasUsageEvent(events) {
		t.Fatalf("Close did not append pipeline events: %+v", events)
	}
	if src.closed != 1 {
		t.Fatalf("source Close calls = %d, want 1", src.closed)
	}
	// Source 为 nil 时 Close 仍应完成 finalize 且不报错。
	nilSrc := &UsageSniffer{Pipeline: llm.NewResponsePipeline(llm.ProviderOpenAICompatible, "/v1/chat/completions", false)}
	if err := nilSrc.Close(); err != nil {
		t.Fatalf("Close with nil source error = %v", err)
	}
}

func TestUsageSnifferNoUsageResponseIsQuiet(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   []byte
		stream bool
	}{
		{"non-stream without usage", []byte(`{"id":"chatcmpl-3","choices":[{"message":{"content":"ok"}}]}`), false},
		{"stream without usage frame", []byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var canonical usage.Usage
			var record recorder.UsageInfo
			var events []recorder.RecordEvent
			sniffer := &UsageSniffer{
				Source:      io.NopCloser(bytes.NewReader(tc.body)),
				Usage:       &canonical,
				RecordUsage: &record,
				Pipeline:    llm.NewResponsePipeline(llm.ProviderOpenAICompatible, "/v1/chat/completions", tc.stream),
				Events:      &events,
			}
			if _, err := io.ReadAll(sniffer); err != nil {
				t.Fatalf("read error = %v", err)
			}
			if err := sniffer.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
			if !canonical.Empty() {
				t.Fatalf("canonical usage should stay zero: %+v", canonical)
			}
			if record.PromptTokens != 0 || record.CompletionTokens != 0 || record.TotalTokens != 0 {
				t.Fatalf("record usage should stay zero: %+v", record)
			}
			if usageSnifferHasUsageEvent(events) {
				t.Fatalf("unexpected llm.usage event: %+v", events)
			}
		})
	}
}

func usageSnifferHasUsageEvent(events []recorder.RecordEvent) bool {
	for _, e := range events {
		if e.Type == "llm.usage" {
			return true
		}
	}
	return false
}
