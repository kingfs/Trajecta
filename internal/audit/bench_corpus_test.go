package audit

// Corpus benchmarks for the audit detection lane: the projection is built once
// outside the timer and detectObservation (the point-scoped detector engine run
// over every projected node) is measured, which is the per-trace audit cost a
// trace detail page pays.
//
//	go test -run '^$' -bench 'BenchmarkAuditDetectCorpus' -benchmem ./internal/audit

import (
	"context"
	"testing"

	"github.com/kingfs/Trajecta/internal/benchcorpus"
	"github.com/kingfs/Trajecta/internal/detector"
	"github.com/kingfs/Trajecta/pkg/xir"
	xiraudit "github.com/kingfs/Trajecta/pkg/xir/audit"
	"github.com/kingfs/Trajecta/pkg/xir/codec/registry"
)

func buildCorpusObservation(b *testing.B, protocol xir.Protocol, trajectory *benchcorpus.Trajectory) *xiraudit.Observation {
	b.Helper()
	codec, ok := registry.Default().Lookup(protocol)
	if !ok {
		b.Fatalf("no codec for %s", protocol)
	}
	operation := xir.OperationChat
	switch protocol {
	case xir.ProtocolResponses:
		operation = xir.OperationResponses
	case xir.ProtocolMessages:
		operation = xir.OperationMessages
	case xir.ProtocolGemini:
		operation = xir.OperationGenerateContent
	}
	options := xir.DecodeOptions{NeedParts: true, NeedTools: true, NeedUsage: true, NeedState: true}
	reqDoc, err := xir.ParseDocument(protocol, operation, benchcorpus.Encode(protocol, trajectory))
	if err != nil {
		b.Fatal(err)
	}
	request, err := codec.DecodeRequest(context.Background(), reqDoc, options)
	if err != nil {
		b.Fatal(err)
	}
	respDoc, err := xir.ParseDocument(protocol, operation, benchcorpus.Response(protocol, trajectory))
	if err != nil {
		b.Fatal(err)
	}
	response, err := codec.DecodeResponse(context.Background(), respDoc, options)
	if err != nil {
		b.Fatal(err)
	}
	return xiraudit.Build(protocol, operation, request, response)
}

func BenchmarkAuditDetectCorpus(b *testing.B) {
	runtime, err := detector.NewRuntimeFromConfig(detector.DefaultConfig())
	if err != nil {
		b.Fatal(err)
	}
	resolve := runtime.Snapshot().AuditEngineFor
	cases := []struct {
		name  string
		bytes int
		opts  benchcorpus.Options
	}{
		{"medium_32k", benchcorpus.MediumBytes, benchcorpus.Options{Secrets: true}},
		{"agent_256k", benchcorpus.Agent256KBytes, benchcorpus.Options{Secrets: true}},
		{"agent_256k_dense", benchcorpus.Agent256KBytes, benchcorpus.Options{Secrets: true, Dense: true}},
	}
	for _, tc := range cases {
		trajectory := benchcorpus.Agent(tc.bytes, tc.opts)
		for _, protocol := range []xir.Protocol{xir.ProtocolChatCompletions, xir.ProtocolResponses, xir.ProtocolMessages, xir.ProtocolGemini} {
			observation := buildCorpusObservation(b, protocol, trajectory)
			b.Run(tc.name+"/"+string(protocol), func(b *testing.B) {
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					detections, err := detectObservation(context.Background(), observation, resolve, nil)
					if err != nil {
						b.Fatal(err)
					}
					auditDetectionCount = len(detections)
				}
			})
		}
	}
}

var auditDetectionCount int
