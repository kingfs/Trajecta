package convert_test

// Corpus benchmarks: realistic multi-step agent traffic at four context sizes,
// converted through both engines. The shapes come from internal/benchcorpus, so
// the same workload also drives the masking and audit benchmarks; a performance
// regression in the shared codec layer shows up here first.
//
// Run with:
//
//	go test -run '^$' -bench 'BenchmarkCorpus' -benchmem ./pkg/xir/convert

import (
	"context"
	"testing"

	"github.com/kingfs/Trajecta/internal/benchcorpus"
	"github.com/kingfs/Trajecta/pkg/protocolconvert"
	"github.com/kingfs/Trajecta/pkg/xir"
	convert "github.com/kingfs/Trajecta/pkg/xir/convert"
)

type corpusSize struct {
	name  string
	bytes int
}

var corpusSizes = []corpusSize{
	{"short_2k", benchcorpus.ShortBytes},
	{"medium_32k", benchcorpus.MediumBytes},
	{"long_128k", benchcorpus.LongBytes},
	{"agent_256k", benchcorpus.Agent256KBytes},
}

type corpusPair struct {
	name string
	from xir.Protocol
	to   xir.Protocol
}

var corpusPairs = []corpusPair{
	{"chat_to_messages", xir.ProtocolChatCompletions, xir.ProtocolMessages},
	{"chat_to_responses", xir.ProtocolChatCompletions, xir.ProtocolResponses},
	{"chat_to_gemini", xir.ProtocolChatCompletions, xir.ProtocolGemini},
	{"messages_to_chat", xir.ProtocolMessages, xir.ProtocolChatCompletions},
	{"responses_to_chat", xir.ProtocolResponses, xir.ProtocolChatCompletions},
	{"gemini_to_chat", xir.ProtocolGemini, xir.ProtocolChatCompletions},
}

func legacyProtocol(protocol xir.Protocol) protocolconvert.Protocol {
	switch protocol {
	case xir.ProtocolChatCompletions:
		return protocolconvert.ProtocolChatCompletions
	case xir.ProtocolResponses:
		return protocolconvert.ProtocolResponses
	case xir.ProtocolMessages:
		return protocolconvert.ProtocolMessages
	case xir.ProtocolGemini:
		return protocolconvert.ProtocolGemini
	}
	panic("benchcorpus: unsupported protocol " + string(protocol))
}

func benchmarkCorpusRequest(b *testing.B, body []byte, from, to xir.Protocol) {
	legacyFrom, legacyTo := legacyProtocol(from), legacyProtocol(to)
	b.Run("legacy", func(b *testing.B) {
		// The corpus is real agent traffic, including Gemini function-call
		// history, which the legacy engine refuses to convert. Keep the cell
		// visible as a skip instead of hiding the shape or failing the suite.
		if preflight, err := protocolconvert.ConvertRequest(body, legacyFrom, legacyTo, benchWireModel, false); err != nil || len(preflight.FatalWarnings()) > 0 {
			b.Skipf("legacy engine cannot convert this corpus shape")
		}
		b.ReportAllocs()
		b.SetBytes(int64(len(body)))
		for i := 0; i < b.N; i++ {
			result, err := protocolconvert.ConvertRequest(body, legacyFrom, legacyTo, benchWireModel, false)
			if err != nil {
				b.Fatal(err)
			}
			if len(result.FatalWarnings()) > 0 {
				b.Fatalf("legacy refused: %+v", result.FatalWarnings())
			}
			benchSink = result.Body
		}
	})
	b.Run("xir", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(body)))
		for i := 0; i < b.N; i++ {
			result, err := convert.ConvertRequest(context.Background(), body, from, to, convert.Options{Model: benchWireModel, Policy: convert.Strict})
			if err != nil {
				b.Fatal(err)
			}
			benchSink = result.Bytes
		}
	})
}

func benchmarkCorpusResponse(b *testing.B, body []byte, from, to xir.Protocol) {
	legacyFrom, legacyTo := legacyProtocol(from), legacyProtocol(to)
	b.Run("legacy", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(body)))
		for i := 0; i < b.N; i++ {
			result, err := protocolconvert.ConvertResponse(body, legacyFrom, legacyTo, benchWireModel, false)
			if err != nil {
				b.Fatal(err)
			}
			benchSink = result.Body
		}
	})
	b.Run("xir", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(body)))
		for i := 0; i < b.N; i++ {
			result, err := convert.ConvertResponse(context.Background(), body, from, to, convert.Options{Model: benchWireModel, Policy: convert.Strict})
			if err != nil {
				b.Fatal(err)
			}
			benchSink = result.Bytes
		}
	})
}

// BenchmarkCorpusRequest measures request conversion of a real multi-step agent
// session. Four context sizes cover the short exchange through the 256 KiB
// trajectory; the largest sizes only run the two directions real traffic hits
// most (client chat and client messages).
func BenchmarkCorpusRequest(b *testing.B) {
	for _, size := range corpusSizes {
		b.Run(size.name, func(b *testing.B) {
			trajectory := benchcorpus.Agent(size.bytes, benchcorpus.Options{Secrets: true})
			for _, pair := range corpusPairs {
				if size.bytes >= benchcorpus.LongBytes && pair.name != "chat_to_messages" && pair.name != "messages_to_chat" {
					continue
				}
				body := benchcorpus.Encode(pair.from, trajectory)
				b.Run(pair.name, func(b *testing.B) {
					benchmarkCorpusRequest(b, body, pair.from, pair.to)
				})
			}
		})
	}
}

// BenchmarkCorpusResponse measures response conversion, including the
// protocol-owned reasoning blocks every real provider sends. The 128 KiB
// response is a large tool result / file body, not a chat reply.
func BenchmarkCorpusResponse(b *testing.B) {
	trajectory := benchcorpus.Agent(benchcorpus.MediumBytes, benchcorpus.Options{Secrets: true})
	for _, pair := range []corpusPair{
		{"chat_to_messages", xir.ProtocolChatCompletions, xir.ProtocolMessages},
		{"messages_to_chat", xir.ProtocolMessages, xir.ProtocolChatCompletions},
		{"responses_to_chat", xir.ProtocolResponses, xir.ProtocolChatCompletions},
		{"gemini_to_chat", xir.ProtocolGemini, xir.ProtocolChatCompletions},
	} {
		body := benchcorpus.Response(pair.from, trajectory)
		b.Run("short_"+pair.name, func(b *testing.B) {
			benchmarkCorpusResponse(b, body, pair.from, pair.to)
		})
	}

	long := benchcorpus.LongResponse(xir.ProtocolMessages, benchcorpus.LongBytes, benchcorpus.Options{Secrets: true})
	b.Run("long_128k_messages_to_chat", func(b *testing.B) {
		benchmarkCorpusResponse(b, long, xir.ProtocolMessages, xir.ProtocolChatCompletions)
	})
}
