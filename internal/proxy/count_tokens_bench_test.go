package proxy

import (
	"strings"
	"testing"
)

// Benchmarks for the synthetic /v1/messages/count_tokens estimator. The Legacy
// variants run the pre-refactor two-pass oracle kept in
// count_tokens_estimate_test.go, so one `go test -bench` run reports the
// before/after pair on identical bodies.

func countTokensBenchBodies() []struct {
	name string
	body []byte
} {
	small := []byte(`{"model":"glm-5.1","messages":[{"role":"user","content":[{"type":"text","text":"hello count tokens"}]}]}`)
	medium := []byte(claudeCodeShapedCountTokensBody())
	large := []byte(`{"model":"claude-sonnet-4","system":[{"type":"text","text":"` +
		strings.Repeat("system prompt block ", 512) +
		`"}],"tools":[{"name":"read","description":"` + strings.Repeat("tool description ", 64) +
		`"}],"messages":[{"role":"user","content":"` + strings.Repeat("long conversation turn ", 2048) +
		`"},{"role":"assistant","content":"` + strings.Repeat("assistant reply ", 2048) + `"}]}`)
	return []struct {
		name string
		body []byte
	}{
		{"small", small},
		{"medium", medium},
		{"large", large},
	}
}

func BenchmarkEstimateAnthropicCountTokensInput(b *testing.B) {
	for _, tc := range countTokensBenchBodies() {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(tc.body)))
			for i := 0; i < b.N; i++ {
				_ = estimateAnthropicCountTokensInput(tc.body)
			}
		})
	}
}

func BenchmarkEstimateAnthropicCountTokensInputLegacy(b *testing.B) {
	for _, tc := range countTokensBenchBodies() {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(tc.body)))
			for i := 0; i < b.N; i++ {
				_ = estimateAnthropicCountTokensInputLegacy(tc.body)
			}
		})
	}
}
