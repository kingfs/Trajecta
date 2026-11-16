package gateway

// InjectStreamOptions / EnsureStreamIncludeUsage 的分配门禁与基准。
// 两条路径：缺 stream_options 的注入路径，与已含 include_usage:true 的
// 无改动路径（后者仍要 wire.Parse 整个 body，门禁锁住该现状）。

import (
	"net/http/httptest"
	"testing"

	"github.com/kingfs/Trajecta/pkg/xir"
)

const (
	gateStreamBodyAbsent  = `{"model":"gpt-5.1","stream":true,"messages":[{"role":"user","content":"hello"}],"temperature":0.7}`
	gateStreamBodyPresent = `{"model":"gpt-5.1","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hello"}],"temperature":0.7}`
)

// TestAllocGateInjectStreamOptionsInsert 是 InjectStreamOptions 注入路径的分配门禁。
// 被测函数：gateway.InjectStreamOptions（缺 stream_options 的 streaming chat body）。
// 基线测量值：49 allocs/op（本机 linux/arm64，2026-11-16，-benchtime=100x）。
// 上限 = 基线 + 约 25% 余量取整 = 62。提高上限必须在 PR 说明理由。
func TestAllocGateInjectStreamOptionsInsert(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	body := []byte(gateStreamBodyAbsent)
	const limit = 62
	got := testing.AllocsPerRun(100, func() {
		_ = InjectStreamOptions(req, body)
	})
	if got > limit {
		t.Fatalf("InjectStreamOptions(insert) allocs/op = %.0f, gate limit %d (baseline 49, arm64 2026-11-16); 提高上限必须在 PR 说明理由", got, limit)
	}
}

// TestAllocGateInjectStreamOptionsPresent 是已含 include_usage:true 的无改动路径门禁。
// 被测函数：gateway.InjectStreamOptions（已含 stream_options.include_usage:true）。
// 现状不是零分配：仍会 wire.Parse 整个 body 后才判定无改动。
// 基线测量值：50 allocs/op（本机 linux/arm64，2026-11-16，-benchtime=100x）。
// 上限 = 基线 + 约 25% 余量取整 = 63。提高上限必须在 PR 说明理由；
// 若未来加了字节级早退（不解析即判定已含），应下调本上限。
func TestAllocGateInjectStreamOptionsPresent(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	body := []byte(gateStreamBodyPresent)
	const limit = 63
	got := testing.AllocsPerRun(100, func() {
		out := InjectStreamOptions(req, body)
		if &out[0] != &body[0] {
			t.Fatal("already-present body must be returned unchanged")
		}
	})
	if got > limit {
		t.Fatalf("InjectStreamOptions(present) allocs/op = %.0f, gate limit %d (baseline 50, arm64 2026-11-16); 提高上限必须在 PR 说明理由", got, limit)
	}
}

// TestAllocGateEnsureStreamIncludeUsageInsert 是 xir.EnsureStreamIncludeUsage 注入路径门禁。
// 被测函数：xir.EnsureStreamIncludeUsage（缺 stream_options）。
// 基线测量值：36 allocs/op（本机 linux/arm64，2026-11-16，-benchtime=100x）。
// 上限 = 基线 + 约 25% 余量取整 = 45。提高上限必须在 PR 说明理由。
func TestAllocGateEnsureStreamIncludeUsageInsert(t *testing.T) {
	body := []byte(gateStreamBodyAbsent)
	const limit = 45
	got := testing.AllocsPerRun(100, func() {
		if _, changed, err := xir.EnsureStreamIncludeUsage(body); err != nil || !changed {
			t.Fatalf("changed=%v err=%v", changed, err)
		}
	})
	if got > limit {
		t.Fatalf("EnsureStreamIncludeUsage(insert) allocs/op = %.0f, gate limit %d (baseline 36, arm64 2026-11-16); 提高上限必须在 PR 说明理由", got, limit)
	}
}

// TestAllocGateEnsureStreamIncludeUsagePresent 是已含 include_usage:true 的无改动路径门禁。
// 被测函数：xir.EnsureStreamIncludeUsage（已含 stream_options.include_usage:true）。
// 现状不是零分配：仍要 wire.Parse 整个 body（37 allocs/op 是解析树成本）。
// 基线测量值：37 allocs/op（本机 linux/arm64，2026-11-16，-benchtime=100x）。
// 上限 = 基线 + 约 25% 余量取整 = 47。提高上限必须在 PR 说明理由。
func TestAllocGateEnsureStreamIncludeUsagePresent(t *testing.T) {
	body := []byte(gateStreamBodyPresent)
	const limit = 47
	got := testing.AllocsPerRun(100, func() {
		out, changed, err := xir.EnsureStreamIncludeUsage(body)
		if err != nil || changed || &out[0] != &body[0] {
			t.Fatalf("changed=%v err=%v", changed, err)
		}
	})
	if got > limit {
		t.Fatalf("EnsureStreamIncludeUsage(present) allocs/op = %.0f, gate limit %d (baseline 37, arm64 2026-11-16); 提高上限必须在 PR 说明理由", got, limit)
	}
}

func BenchmarkInjectStreamOptions(b *testing.B) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"insert", gateStreamBodyAbsent},
		{"already_present", gateStreamBodyPresent},
	} {
		b.Run(tc.name, func(b *testing.B) {
			req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
			req.Header.Set("Content-Type", "application/json")
			body := []byte(tc.body)
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = InjectStreamOptions(req, body)
			}
		})
	}
}

func BenchmarkEnsureStreamIncludeUsage(b *testing.B) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"insert", gateStreamBodyAbsent},
		{"already_present", gateStreamBodyPresent},
	} {
		b.Run(tc.name, func(b *testing.B) {
			body := []byte(tc.body)
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, _, err := xir.EnsureStreamIncludeUsage(body); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
