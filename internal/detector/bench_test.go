package detector

import (
	"context"
	"testing"

	"github.com/kingfs/Trajecta/pkg/secretmask"
)

// The hot-path masking benchmark suite. Phase 1 establishes the byte-level
// baseline (the path the proxy runs today); Phase 4 adds the IR-scoped and
// hybrid variants beside it so the performance delta is measured on identical
// fixtures with identical vault seeding.
//
// Run:
//
//	go test ./internal/detector -run '^$' -bench . -benchmem

func benchMasker(b *testing.B, body []byte, ir bool) {
	b.Helper()
	rt, err := NewRuntime(context.Background(), nil)
	if err != nil {
		b.Fatal(err)
	}
	vault := secretmask.NewVault([]byte("hot-path-bench-seed"))
	m := NewMasker(vault, rt, 0)
	mask := func() int {
		if ir {
			_, events := m.MaskWithProtocol(body, "chat_completions")
			return len(events)
		}
		_, events := m.Mask(body)
		return len(events)
	}
	// Warm the vault so the steady-state cost (mapping hit, no render) is what
	// the proxy pays on a conversation that reuses the same credentials.
	if mask() == 0 {
		b.Fatalf("fixture produced no mask events")
	}
	b.SetBytes(int64(len(body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if mask() == 0 {
			b.Fatal("warm mask lost its events")
		}
	}
}

// BenchmarkMaskerByte is the baseline: the detector.Masker byte path, equivalent
// to Vault.Mask today. Sizes vary at a realistic credential density.
func BenchmarkMaskerByte(b *testing.B) {
	for _, turns := range []int{4, 64, 256} {
		body := []byte(largeChatBody(turns, 8))
		b.Run(sizeLabel(len(body)), func(b *testing.B) { benchMasker(b, body, false) })
	}
}

// BenchmarkMaskerIR is the XIR field-scoped hot path: parse the body to the IR
// projection, scan only the declared field spans (sealed content excluded), then
// render through the Vault. The delta against BenchmarkMaskerByte is the cost of
// applying XIR scoping on the hot path.
func BenchmarkMaskerIR(b *testing.B) {
	for _, turns := range []int{4, 64, 256} {
		body := []byte(largeChatBody(turns, 8))
		b.Run(sizeLabel(len(body)), func(b *testing.B) { benchMasker(b, body, true) })
	}
}

// BenchmarkVaultMask is the direct Vault.Mask reference (no detector layer), to
// separate the detector coordinator's overhead from the masking core itself.
func BenchmarkVaultMask(b *testing.B) {
	for _, turns := range []int{4, 64, 256} {
		body := []byte(largeChatBody(turns, 8))
		vault := secretmask.NewVault([]byte("hot-path-bench-seed"))
		if _, events := vault.Mask(body); len(events) == 0 {
			b.Fatalf("fixture produced no mask events")
		}
		b.Run(sizeLabel(len(body)), func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, events := vault.Mask(body); len(events) == 0 {
					b.Fatal("warm mask lost its events")
				}
			}
		})
	}
}

func sizeLabel(n int) string {
	switch {
	case n >= 1<<20:
		return itoa(n>>20) + "MiB"
	case n >= 1<<10:
		return itoa(n>>10) + "KiB"
	default:
		return itoa(n) + "B"
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
