package xir_test

// Corpus benchmarks for the semantic core: wire parse + codec decode (the cost
// every conversion, audit projection and masking IR path pays) and the audit
// projection build. The bodies are the realistic agent trajectories from
// internal/benchcorpus.
//
//	go test -run '^$' -bench 'BenchmarkCorpusParse|BenchmarkCorpusAuditBuild' -benchmem ./pkg/xir

import (
	"context"
	"testing"

	"github.com/kingfs/Trajecta/internal/benchcorpus"
	"github.com/kingfs/Trajecta/pkg/xir"
	xiraudit "github.com/kingfs/Trajecta/pkg/xir/audit"
	"github.com/kingfs/Trajecta/pkg/xir/codec/registry"
	"github.com/kingfs/Trajecta/pkg/xir/stream"
)

func corpusOperation(protocol xir.Protocol) xir.Operation {
	switch protocol {
	case xir.ProtocolChatCompletions:
		return xir.OperationChat
	case xir.ProtocolResponses:
		return xir.OperationResponses
	case xir.ProtocolMessages:
		return xir.OperationMessages
	case xir.ProtocolGemini:
		return xir.OperationGenerateContent
	}
	return xir.OperationUnknown
}

var benchDecodeOptions = xir.DecodeOptions{NeedParts: true, NeedTools: true, NeedUsage: true, NeedState: true}

func BenchmarkCorpusParseDecode(b *testing.B) {
	cases := []struct {
		name  string
		bytes int
	}{
		{"medium_32k", benchcorpus.MediumBytes},
		{"agent_256k", benchcorpus.Agent256KBytes},
	}
	for _, tc := range cases {
		trajectory := benchcorpus.Agent(tc.bytes, benchcorpus.Options{Secrets: true})
		for _, protocol := range []xir.Protocol{xir.ProtocolChatCompletions, xir.ProtocolResponses, xir.ProtocolMessages, xir.ProtocolGemini} {
			body := benchcorpus.Encode(protocol, trajectory)
			codec, ok := registry.Default().Lookup(protocol)
			if !ok {
				b.Fatalf("no codec for %s", protocol)
			}
			operation := corpusOperation(protocol)
			b.Run(tc.name+"/"+string(protocol), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(body)))
				for i := 0; i < b.N; i++ {
					doc, err := xir.ParseDocument(protocol, operation, body)
					if err != nil {
						b.Fatal(err)
					}
					request, err := codec.DecodeRequest(context.Background(), doc, benchDecodeOptions)
					if err != nil {
						b.Fatal(err)
					}
					benchDecoded = request
				}
			})
		}
	}
}

// BenchmarkCorpusAuditBuild measures the projection only: the request is
// decoded once outside the timer.
func BenchmarkCorpusAuditBuild(b *testing.B) {
	trajectory := benchcorpus.Agent(benchcorpus.Agent256KBytes, benchcorpus.Options{Secrets: true})
	for _, protocol := range []xir.Protocol{xir.ProtocolChatCompletions, xir.ProtocolResponses, xir.ProtocolMessages, xir.ProtocolGemini} {
		codec, ok := registry.Default().Lookup(protocol)
		if !ok {
			b.Fatalf("no codec for %s", protocol)
		}
		operation := corpusOperation(protocol)
		reqDoc, err := xir.ParseDocument(protocol, operation, benchcorpus.Encode(protocol, trajectory))
		if err != nil {
			b.Fatal(err)
		}
		request, err := codec.DecodeRequest(context.Background(), reqDoc, benchDecodeOptions)
		if err != nil {
			b.Fatal(err)
		}
		respDoc, err := xir.ParseDocument(protocol, operation, benchcorpus.Response(protocol, trajectory))
		if err != nil {
			b.Fatal(err)
		}
		response, err := codec.DecodeResponse(context.Background(), respDoc, benchDecodeOptions)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(string(protocol), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				observation := xiraudit.Build(protocol, operation, request, response)
				benchNodes = len(observation.Nodes)
			}
		})
	}
}

var (
	benchDecoded *xir.Request
	benchNodes   int
)

// BenchmarkCorpusStreamAssemble measures assembling a 256 KiB chat text stream
// plus a tool call into one response, the work the audit projection does for a
// streamed cassette.
func BenchmarkCorpusStreamAssemble(b *testing.B) {
	events := benchcorpus.ChatStreamEvents(benchcorpus.Agent256KBytes)
	b.Run("chat_256k", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			assembler := stream.NewAssembler(xir.ProtocolChatCompletions)
			for _, event := range events {
				if err := assembler.Apply(event); err != nil {
					b.Fatal(err)
				}
			}
			benchAssembled = assembler.Response()
		}
	})
}

var benchAssembled *xir.Response
