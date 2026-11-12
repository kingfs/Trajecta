package detector

// Stage-isolation benchmarks for the hot-path IR overhead root-cause analysis.
// They measure each stage of the IR path in isolation on the SAME fixture, so
// the additive cost of XIR parsing can be attributed precisely against the
// irreducible DFA scan the byte path already pays.
//
// Run:
//
//	go test ./internal/detector -run '^$' -bench 'BenchmarkStage' -benchmem

import (
	"context"
	"testing"

	"github.com/kingfs/Trajecta/pkg/secretmask"
	"github.com/kingfs/Trajecta/pkg/xir"
	xiraudit "github.com/kingfs/Trajecta/pkg/xir/audit"
	"github.com/kingfs/Trajecta/pkg/xir/codec/registry"
)

func stageBody() []byte {
	return []byte(largeChatBody(220, 8)) // ~64 KiB chat transcript
}

func stageSnapshot(b *testing.B) *Snapshot {
	b.Helper()
	rt, err := NewRuntime(context.Background(), nil)
	if err != nil {
		b.Fatal(err)
	}
	return rt.Snapshot()
}

// BenchmarkStageDFA is the irreducible detection cost: one compiled-DFA pass over
// the whole body, exactly what the byte path pays. The Ragel machine is unchanged
// by the IR work; this is the reference floor.
func BenchmarkStageDFA(b *testing.B) {
	body := stageBody()
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = (secretmask.Detector{}).Scan(body)
	}
}

// BenchmarkStageWireParse isolates xir.ParseDocument (span-preserving JSON parse).
func BenchmarkStageWireParse(b *testing.B) {
	body := stageBody()
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := xir.ParseDocument(xir.ProtocolChatCompletions, xir.OperationChat, body); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkStageDecode isolates wire parse + codec semantic decode.
func BenchmarkStageDecode(b *testing.B) {
	body := stageBody()
	codec, _ := registry.Default().Lookup(xir.ProtocolChatCompletions)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		doc, err := xir.ParseDocument(xir.ProtocolChatCompletions, xir.OperationChat, body)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := codec.DecodeRequest(context.Background(), doc, xir.DecodeOptions{NeedParts: true}); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkStageAuditBuild isolates wire parse + decode + audit projection (the
// full ParseRequestScope projection cost, before span collection).
func BenchmarkStageAuditBuild(b *testing.B) {
	body := stageBody()
	codec, _ := registry.Default().Lookup(xir.ProtocolChatCompletions)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		doc, err := xir.ParseDocument(xir.ProtocolChatCompletions, xir.OperationChat, body)
		if err != nil {
			b.Fatal(err)
		}
		req, err := codec.DecodeRequest(context.Background(), doc, xir.DecodeOptions{NeedParts: true})
		if err != nil {
			b.Fatal(err)
		}
		_ = xiraudit.Build(xir.ProtocolChatCompletions, xir.OperationChat, req, nil)
	}
}

// BenchmarkStageIRCollect is the full IR candidate collection (projection +
// whole-body DFA), without the Vault render. Compare to BenchmarkStageByteCollect.
func BenchmarkStageIRCollect(b *testing.B) {
	body := stageBody()
	snap := stageSnapshot(b)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		scope, ok := ParseRequestScope("chat_completions", body, ScopeOptions{RequireFields: true})
		if !ok {
			b.Fatal("scope failed")
		}
		_ = snap.collectCandidatesScoped(body, scope)
	}
}

// BenchmarkStageByteCollect is the byte candidate collection (whole-body DFA),
// without the Vault render. The IRCollect-minus-ByteCollect delta is the IR tax.
func BenchmarkStageByteCollect(b *testing.B) {
	body := stageBody()
	snap := stageSnapshot(b)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = snap.collectCandidates(body)
	}
}
