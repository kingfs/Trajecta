package gateway

import (
	"bytes"
	"io"
	"testing"

	"github.com/kingfs/Trajecta/internal/recorder"
	"github.com/kingfs/Trajecta/pkg/llm"
	"github.com/kingfs/Trajecta/pkg/secretmask"
	"github.com/kingfs/Trajecta/pkg/usage"
)

func TestResponsePipelineTeesClientBytes(t *testing.T) {
	upstream := io.NopCloser(bytes.NewReader([]byte("hello")))
	var written bytes.Buffer
	var count int64
	pipeline := NewResponsePipeline(ResponsePipelineOptions{
		Upstream: upstream,
		File:     &written,
		Count:    &count,
	})
	got, err := io.ReadAll(pipeline.Reader())
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello" || string(written.Bytes()) != "hello" || count != 5 {
		t.Fatalf("got=%q written=%q count=%d", got, written.String(), count)
	}
	if err := pipeline.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestResponsePipelineRestoresClientBytes(t *testing.T) {
	vault := secretmask.NewVault([]byte("gateway-response-pipeline"))
	original := []byte("OPENAI_API_KEY=abcdefghijklmnopqrstuvwxyz123456")
	masked, events := vault.Mask(original)
	if len(events) == 0 {
		t.Fatal("test credential was not masked")
	}
	pipeline := NewResponsePipeline(ResponsePipelineOptions{
		Upstream: io.NopCloser(bytes.NewReader(masked)),
		Vault:    vault,
	})
	got, err := io.ReadAll(pipeline.Reader())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, original) {
		t.Fatalf("restored = %q, want %q", got, original)
	}
	if pipeline.Restored() == 0 {
		t.Fatal("Restored() = 0")
	}
	if err := pipeline.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestUsageSnifferWithoutCountOrSourceIsSafe(t *testing.T) {
	sniffer := &UsageSniffer{Source: io.NopCloser(bytes.NewReader([]byte("x"))), File: &bytes.Buffer{}}
	if _, err := io.ReadAll(sniffer); err != nil {
		t.Fatal(err)
	}
	if err := sniffer.Close(); err != nil {
		t.Fatal(err)
	}
	var nilSniffer *UsageSniffer
	if _, err := nilSniffer.Read(make([]byte, 1)); err == nil {
		t.Fatal("nil sniffer read did not error")
	}
	if err := nilSniffer.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestUsageSnifferWritesCanonicalAndRecordUsage(t *testing.T) {
	var canonical usage.Usage
	var record recorder.UsageInfo
	sniffer := &UsageSniffer{
		Source:      io.NopCloser(bytes.NewReader(nil)),
		Usage:       &canonical,
		RecordUsage: &record,
		Pipeline:    llm.NewResponsePipeline(llm.ProviderOpenAICompatible, "/v1/responses", false),
	}
	sniffer.Pipeline.Feed([]byte(`{"usage":{"input_tokens":10,"output_tokens":4,"total_tokens":14,"input_tokens_details":{"cached_tokens":3}}}`))
	if err := sniffer.Close(); err != nil {
		t.Fatal(err)
	}
	if canonical.InputTokens != 10 || canonical.OutputTokens != 4 || canonical.TotalTokens != 14 || canonical.CacheReadTokens != 3 {
		t.Fatalf("canonical = %+v", canonical)
	}
	if record.PromptTokens != 10 || record.CompletionTokens != 4 || record.TotalTokens != 14 {
		t.Fatalf("record = %+v", record)
	}
}
