package secretmask

// Detector.Scan 的分配门禁与基准：清洁 body（prefilter 早退路径）与含
// 凭据 body；另附 Vault.MaskCandidates 小候选集基准。fixture 复用
// bench_test.go 的 largeNoSecretBody / benchJSONBody（规则样本来自
// positiveSamples，不读生成文件）。

import "testing"

// TestAllocGateScanCleanBody 是清洁 body 的分配门禁（prefilter 早退路径）。
// 被测函数：secretmask.Detector.Scan（largeNoSecretBody，约 50 KiB 无凭据 body）。
// 基线测量值：0 allocs/op（本机 linux/arm64，2026-11-16，-benchtime=100x）。
// 上限 = 0（零分配路径不允许任何余量）。提高上限必须在 PR 说明理由。
func TestAllocGateScanCleanBody(t *testing.T) {
	body := []byte(largeNoSecretBody())
	const limit = 0
	got := testing.AllocsPerRun(100, func() {
		if findings := (Detector{}).Scan(body); len(findings) != 0 {
			t.Fatalf("clean body produced %d findings", len(findings))
		}
	})
	if got > limit {
		t.Fatalf("Scan(clean) allocs/op = %.0f, gate limit %d (baseline 0, arm64 2026-11-16); prefilter 早退必须保持零分配，提高上限必须在 PR 说明理由", got, limit)
	}
}

// TestAllocGateScanCredentialBody 是含凭据 body 的分配门禁（DFA 全量路径）。
// 被测函数：secretmask.Detector.Scan（benchJSONBody(4KiB, 每 8 条消息 1 凭据)）。
// 基线测量值：2 allocs/op（本机 linux/arm64，2026-11-16，-benchtime=100x）。
// 上限 = 基线 + 约 25% 余量取整 = 3。提高上限必须在 PR 说明理由。
func TestAllocGateScanCredentialBody(t *testing.T) {
	body := benchJSONBody(4<<10, 8)
	const limit = 3
	got := testing.AllocsPerRun(100, func() {
		if findings := (Detector{}).Scan(body); len(findings) == 0 {
			t.Fatal("credential body produced no findings")
		}
	})
	if got > limit {
		t.Fatalf("Scan(credential) allocs/op = %.0f, gate limit %d (baseline 2, arm64 2026-11-16); 提高上限必须在 PR 说明理由", got, limit)
	}
}

func BenchmarkScanGateCleanBody(b *testing.B) {
	body := []byte(largeNoSecretBody())
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if findings := (Detector{}).Scan(body); len(findings) != 0 {
			b.Fatalf("clean body produced %d findings", len(findings))
		}
	}
}

func BenchmarkScanGateCredentialBody(b *testing.B) {
	body := benchJSONBody(4<<10, 8)
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if findings := (Detector{}).Scan(body); len(findings) == 0 {
			b.Fatal("credential body produced no findings")
		}
	}
}

// BenchmarkMaskCandidatesSmallSet 是热遮蔽 lane 的形态：检测一次得到少量
// 候选，Vault 在温态下反复执行 MaskCandidates（幂等检查 + 一次拷贝）。
func BenchmarkMaskCandidatesSmallSet(b *testing.B) {
	body := benchJSONBody(4<<10, 16)
	findings := Scan(body)
	if len(findings) == 0 || len(findings) > 8 {
		b.Fatalf("fixture finding count = %d, want a small set (1..8)", len(findings))
	}
	cands := make([]MaskCandidate, len(findings))
	for i, f := range findings {
		cands[i] = MaskCandidate{Finding: f}
	}
	v := NewVault([]byte("secretmask-gate-seed"))
	if _, events := v.MaskCandidates(body, cands); len(events) == 0 {
		b.Fatal("warm-up mask produced no events")
	}
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, events := v.MaskCandidates(body, cands); len(events) == 0 {
			b.Fatal("warm mask lost its events")
		}
	}
}
