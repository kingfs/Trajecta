package secretmask

// This file holds the end-to-end performance suite for the detection, masking
// and restore paths. The microbenchmarks next to the rules (mobile_test.go,
// chineseid_test.go) answer "how fast is one rule"; these answer "what does a
// request or a response of a realistic size pay".
//
// Run it with:
//
//	go test ./pkg/secretmask -run '^$' -bench . -benchmem
//
// Every benchmark reports throughput via SetBytes, so the number that matters
// for a proxy is MB/s: a 64 KiB request at 70-200 MB/s costs 0.3-0.9 ms
// depending on how dense its hash and UUID runs are. The fixtures are
// deliberately transcript-shaped - prose, code, hashes and UUIDs around a few
// credentials - because throughput on English prose alone overstates what real
// traffic gets.

import (
	"fmt"
	"io"
	"strings"
	"testing"
)

// benchNoiseBlock is the filler of one message: text a real transcript carries
// in volume, including the hash and UUID shapes that make the DFA work for its
// living without producing a finding. It contains no quote or newline so it can
// sit inside a JSON string.
const benchNoiseBlock = `Please review commit 3f2a9c8e1b4d5a6f7c8e9d0a1b2c3d4e5f607182 and the helper ` +
	`func handleRequest. The upstream returned 502 after 30s; check resp.StatusCode and the retry budget. ` +
	`Blob d41d8cd98f00b204e9800998ecf8427e and id 550e8400-e29b-41d4-a716-446655440000 are not credentials. `

const (
	benchRequestHead = `{"model":"gpt-5","stream":false,"messages":[`
	benchRequestTail = `]}`
)

// benchSecretValues are the values spread through the fixture. They are drawn
// from positiveSamples so the benchmark cannot drift away from the samples the
// rule tests accept. Reusing six values is realistic: a session tends to carry
// the same handful of keys across many turns, and it keeps the vault in its
// steady state.
func benchSecretValues() []string {
	return []string{
		sampleFor(RuleOpenAILegacy),
		sampleFor(RuleAnthropicAPIKey),
		sampleFor(RuleGoogleAPIKey),
		sampleFor(RuleJWT),
		sampleFor(RuleGitHubPAT),
		sampleFor(RuleAWSAccessKeyID),
	}
}

// benchJSONBody builds a request-shaped JSON body of at least target bytes.
// secretEvery controls density: zero means no credential at all, one means every
// message carries one, and n means one message in n does. Each credential gets
// the "OPENAI_API_KEY=" label a real transcript would have, so masking finds it
// maskable rather than merely reportable.
func benchJSONBody(target, secretEvery int) []byte {
	if target < 1<<10 {
		target = 1 << 10
	}
	secrets := benchSecretValues()
	var b strings.Builder
	b.Grow(target + len(benchRequestTail) + 512)
	b.WriteString(benchRequestHead)
	for i := 0; b.Len() < target-len(benchRequestTail); i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"role":"user","content":"`)
		b.WriteString(benchNoiseBlock)
		if secretEvery > 0 && i%secretEvery == 0 {
			b.WriteString(`OPENAI_API_KEY=`)
			b.WriteString(secrets[(i/secretEvery)%len(secrets)])
		}
		b.WriteString(`"}`)
	}
	b.WriteString(benchRequestTail)
	return []byte(b.String())
}

func benchSizeLabel(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%dMiB", n>>20)
	case n >= 1<<10:
		return fmt.Sprintf("%dKiB", n>>10)
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// BenchmarkScanRequest measures the unavoidable cost every request pays when
// masking is enabled: one DFA pass over the whole body. The first table varies
// body size at a realistic density; the second fixes the size and varies
// credential density, because context scoring and validators only run on
// candidates, so a body full of keys is the worst case.
func BenchmarkScanRequest(b *testing.B) {
	for _, size := range []int{4 << 10, 32 << 10, 128 << 10, 512 << 10} {
		payload := benchJSONBody(size, 32)
		b.Run(benchSizeLabel(size)+"/sparse", func(b *testing.B) {
			b.SetBytes(int64(len(payload)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = Scan(payload)
			}
		})
	}

	// Density at a fixed size: how much the per-candidate work (validator,
	// context window) adds once the matches stop being rare.
	const base = 64 << 10
	for _, tc := range []struct {
		name  string
		every int
	}{
		{"none", 0},
		{"sparse", 64},
		{"medium", 8},
		{"dense", 1},
	} {
		payload := benchJSONBody(base, tc.every)
		b.Run(benchSizeLabel(base)+"/"+tc.name, func(b *testing.B) {
			b.SetBytes(int64(len(payload)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_ = Scan(payload)
			}
		})
	}
}

// BenchmarkMaskRequest measures a request pass on a warm vault: detection, the
// idempotence check, one copy of the body and the in-place stand-in rendering.
// This is the whole cost the proxy adds to the outbound request, including the
// buffer the caller hands to the wire.
func BenchmarkMaskRequest(b *testing.B) {
	for _, size := range []int{4 << 10, 64 << 10, 512 << 10} {
		body := benchJSONBody(size, 16)
		v := NewVault([]byte("secretmask-bench-seed"))
		if _, events := v.Mask(body); len(events) == 0 {
			b.Fatalf("%s fixture produced no mask events", benchSizeLabel(size))
		}
		b.Run(benchSizeLabel(size), func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, events := v.Mask(body); len(events) == 0 {
					b.Fatal("warm mask lost its events")
				}
			}
		})
	}
}

// BenchmarkRestoreResponse measures the response side in the two states that
// matter: a vault that knows some stand-ins but a body that contains none (the
// common case), and a body that carries the stand-ins a masked request produced.
func BenchmarkRestoreResponse(b *testing.B) {
	for _, size := range []int{64 << 10, 512 << 10} {
		realBody := benchJSONBody(size, 16)
		v := NewVault([]byte("secretmask-bench-seed"))
		masked, events := v.Mask(realBody)
		if len(events) == 0 {
			b.Fatalf("%s fixture produced no mask events", benchSizeLabel(size))
		}
		// A similarly sized body that contains none of the stand-ins, so the
		// scan has to reject every position rather than skip to a match.
		clean := benchJSONBody(size, 0)

		b.Run(benchSizeLabel(size)+"/no_fakes", func(b *testing.B) {
			b.SetBytes(int64(len(clean)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				v.Restore(clean)
			}
		})
		b.Run(benchSizeLabel(size)+"/with_fakes", func(b *testing.B) {
			b.SetBytes(int64(len(masked)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				v.Restore(masked)
			}
		})
	}
}

// BenchmarkRestoreStreamingChunks measures the Restorer at the read sizes an
// HTTP body tends to hand it. The restorer has its own read chunk, so this
// varies the producer side and the hold-back behaviour on each call, which is
// what a streamed provider response exercises.
func BenchmarkRestoreStreamingChunks(b *testing.B) {
	body := benchJSONBody(256<<10, 16)
	v := NewVault([]byte("secretmask-bench-seed"))
	masked, events := v.Mask(body)
	if len(events) == 0 {
		b.Fatal("fixture produced no mask events")
	}
	for _, chunk := range []int{512, 4 << 10, 16 << 10, 32 << 10, 64 << 10} {
		b.Run(benchSizeLabel(chunk), func(b *testing.B) {
			b.SetBytes(int64(len(masked)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				r := NewRestorer(&chunkReader{data: masked, n: chunk}, v)
				if _, err := io.Copy(io.Discard, r); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkMaskRestoreRoundTrip is the number to quote for "what does one
// exchange pay": mask a request body, then restore the same-sized masked body
// against the now-warm vault. SetBytes counts both directions.
func BenchmarkMaskRestoreRoundTrip(b *testing.B) {
	body := benchJSONBody(64<<10, 16)
	v := NewVault([]byte("secretmask-bench-seed"))
	if _, events := v.Mask(body); len(events) == 0 {
		b.Fatal("fixture produced no mask events")
	}
	b.SetBytes(int64(2 * len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		out, got := v.Mask(body)
		if len(got) == 0 {
			b.Fatal("warm mask lost its events")
		}
		v.Restore(out)
	}
}

// TestBenchmarkFixtures guards the benchmarks from measuring nothing. A fixture
// that stopped matching a rule would still benchmark fast, so assert its shape
// as an ordinary test: no noise finding, maskable findings in the seeded body,
// and a full restore of what masking produced.
func TestBenchmarkFixtures(t *testing.T) {
	clean := benchJSONBody(64<<10, 0)
	if findings := Scan(clean); len(findings) != 0 {
		t.Fatalf("no-secret fixture produced %d findings, want 0", len(findings))
	}

	body := benchJSONBody(64<<10, 16)
	findings := Scan(body)
	if len(findings) == 0 {
		t.Fatal("seeded fixture produced no findings")
	}
	for _, f := range findings {
		if !f.Maskable() {
			t.Fatalf("fixture finding %s at %d is not maskable (score %d)", ruleTable[f.ID].Name, f.Start, f.Score)
		}
	}

	v := NewVault([]byte("secretmask-bench-seed"))
	masked, events := v.Mask(body)
	if len(events) == 0 {
		t.Fatal("mask produced no events")
	}
	if string(v.Restore(masked)) != string(body) {
		t.Fatal("restore did not recover the fixture")
	}
}
