package detector

// Corpus benchmarks for the hot masking lane on realistic agent traffic: the
// byte fast path versus the XIR-scoped path, sparse versus dense sensitive
// values, and a request that carries a sealed reasoning block (the case the IR
// path exists for). The bodies come from internal/benchcorpus so the same
// workload drives the conversion and audit benchmarks.
//
//	go test -run '^$' -bench 'BenchmarkMaskCorpus' -benchmem ./internal/detector

import (
	"encoding/json"
	"testing"

	"github.com/kingfs/Trajecta/internal/benchcorpus"
	"github.com/kingfs/Trajecta/pkg/secretmask"
	"github.com/kingfs/Trajecta/pkg/xir"
)

func corpusMasker(b *testing.B, maxScopeBytes int) *Masker {
	b.Helper()
	runtime, err := NewRuntimeFromConfig(DefaultConfig())
	if err != nil {
		b.Fatal(err)
	}
	vault := secretmask.NewVault([]byte("bench-corpus-seed"))
	masker := NewMasker(vault, runtime, maxScopeBytes)
	// Warm the vault once so the benchmark measures steady state (the first
	// call renders every stand-in).
	masker.Mask([]byte(`OPENAI_API_KEY=` + benchcorpus.SensitiveValues()[0]))
	return masker
}

func benchmarkMaskCorpus(b *testing.B, masker *Masker, body []byte, protocol string) {
	b.Helper()
	b.ReportAllocs()
	b.SetBytes(int64(len(body)))
	events := 0
	for i := 0; i < b.N; i++ {
		var output []byte
		var maskEvents []secretmask.MaskEvent
		if protocol == "" {
			output, maskEvents = masker.Mask(body)
		} else {
			output, maskEvents = masker.MaskWithProtocol(body, protocol)
		}
		benchMasked = output
		events += len(maskEvents)
	}
	b.ReportMetric(float64(events)/float64(b.N), "events/op")
}

// BenchmarkMaskCorpus compares the byte path and the IR path on the same
// multi-step bodies. Chat bodies never project; Messages bodies project when a
// sealed block is present.
func BenchmarkMaskCorpus(b *testing.B) {
	masker := corpusMasker(b, 0)
	for _, size := range []struct {
		name  string
		bytes int
	}{
		{"short_2k", benchcorpus.ShortBytes},
		{"medium_32k", benchcorpus.MediumBytes},
		{"long_128k", benchcorpus.LongBytes},
		{"agent_256k", benchcorpus.Agent256KBytes},
	} {
		trajectory := benchcorpus.Agent(size.bytes, benchcorpus.Options{Secrets: true})
		chatBody := benchcorpus.EncodeChat(trajectory)
		b.Run("byte_chat/"+size.name, func(b *testing.B) {
			benchmarkMaskCorpus(b, masker, chatBody, "")
		})
	}
	for _, size := range []struct {
		name  string
		bytes int
	}{
		{"medium_32k", benchcorpus.MediumBytes},
		{"agent_256k", benchcorpus.Agent256KBytes},
	} {
		trajectory := benchcorpus.Agent(size.bytes, benchcorpus.Options{Secrets: true})
		messagesBody := benchcorpus.EncodeMessages(trajectory)
		b.Run("byte_messages/"+size.name, func(b *testing.B) {
			benchmarkMaskCorpus(b, masker, messagesBody, "")
		})
		b.Run("ir_messages/"+size.name, func(b *testing.B) {
			benchmarkMaskCorpus(b, masker, messagesBody, string(xir.ProtocolMessages))
		})
		sealed := addSealedThinking(b, messagesBody)
		b.Run("ir_messages_sealed/"+size.name, func(b *testing.B) {
			benchmarkMaskCorpus(b, masker, sealed, string(xir.ProtocolMessages))
		})
	}
	dense := benchcorpus.EncodeChat(benchcorpus.Agent(benchcorpus.Agent256KBytes, benchcorpus.Options{Secrets: true, Dense: true}))
	b.Run("byte_chat/agent_256k_dense", func(b *testing.B) {
		benchmarkMaskCorpus(b, masker, dense, "")
	})
}

// addSealedThinking inserts an assistant thinking block into a Messages request
// so the IR path has a MustRoundTrip span to exclude.
func addSealedThinking(b *testing.B, body []byte) []byte {
	b.Helper()
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		b.Fatal(err)
	}
	messages, _ := parsed["messages"].([]any)
	if len(messages) == 0 {
		b.Fatal("no messages")
	}
	first := messages[0].(map[string]any)
	parsed["messages"] = append([]any{map[string]any{
		"role": "assistant",
		"content": []any{map[string]any{
			"type": "thinking", "thinking": "I should read the failing handler before patching.",
			"signature": "bench-sealed-signature",
		}},
	}}, append([]any{first}, messages[1:]...)...)
	raw, err := json.Marshal(parsed)
	if err != nil {
		b.Fatal(err)
	}
	return raw
}

var benchMasked []byte
