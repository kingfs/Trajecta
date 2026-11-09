package wire

import (
	"bytes"
	"strings"
	"testing"
)

func benchmarkBody(n int) []byte {
	var b strings.Builder
	b.WriteString(`{"model":"gpt-5","stream":true,"messages":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"role":"user","content":"hello world `)
		b.WriteString(strings.Repeat("x", 40))
		b.WriteString(`"}`)
	}
	b.WriteString(`]}`)
	return []byte(b.String())
}

func BenchmarkParseSmall(b *testing.B) {
	raw := []byte(`{"model":"gpt-5","stream":true}`)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := Parse(raw); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkParseLarge(b *testing.B) {
	raw := benchmarkBody(500)
	b.SetBytes(int64(len(raw)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := Parse(raw); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkApplyOneString(b *testing.B) {
	raw := []byte(`{"a":"secret","b":2}`)
	node := MustParse(raw)
	value, _ := node.Get("a")
	edits := []Edit{{Span: value.Span, Bytes: []byte("xxxxxx"), Mode: ReplaceString}}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := Apply(raw, edits); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkApplyNoEdits(b *testing.B) {
	raw := bytes.Repeat([]byte("x"), 4096)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := Apply(raw, nil); err != nil {
			b.Fatal(err)
		}
	}
}
