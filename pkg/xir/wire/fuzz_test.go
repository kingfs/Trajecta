package wire

import "testing"

func FuzzParse(f *testing.F) {
	seeds := []string{
		`{}`,
		`{"a":1,"b":[true,null,"x"]}`,
		`{"a":"\u4e2d\"b"}`,
		`[1,2,3]`,
		`"plain"`,
		`{"nested":{"x":[{"y":1}]}}`,
		`{"a":}`,
		``,
		`{"a":1} trailing`,
	}
	for _, seed := range seeds {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		node, err := Parse(raw)
		if err != nil {
			return
		}
		if node == nil {
			t.Fatal("nil node without error")
		}
		if !node.Span.Valid() || node.Span.End > len(raw) {
			t.Fatalf("root span %+v outside %d bytes", node.Span, len(raw))
		}
		// Get/Index/String must not panic on arbitrary valid JSON.
		_, _ = node.Get("a")
		_, _ = node.Index(0)
		_, _ = node.String()
		_, _ = node.Int()
		_, _ = node.Bool()
		_ = node.Len()
	})
}
