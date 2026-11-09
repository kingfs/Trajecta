package wire

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestParseObjectPreservesOrderAndUnknowns(t *testing.T) {
	raw := []byte(`{"b":2,"a":1,"nested":{"x":true},"unknown":[1,"two",null]}`)
	n, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if n.Kind != Object || len(n.Members) != 4 {
		t.Fatalf("kind/members = %v/%d", n.Kind, len(n.Members))
	}
	if n.Members[0].Key != "b" || n.Members[1].Key != "a" {
		t.Fatalf("order lost: %v", n.Members)
	}
	v, ok := n.IntField("b")
	if !ok || v != 2 {
		t.Fatalf("b = %d/%v", v, ok)
	}
	nested, ok := n.Get("nested")
	if !ok {
		t.Fatal("nested missing")
	}
	if b, ok := nested.BoolField("x"); !ok || !b {
		t.Fatalf("nested.x = %v/%v", b, ok)
	}
	arr, ok := n.Get("unknown")
	if !ok || arr.Kind != Array || len(arr.Items) != 3 {
		t.Fatalf("unknown = %+v", arr)
	}
	if s, ok := arr.Items[1].String(); !ok || s != "two" {
		t.Fatalf("two = %q/%v", s, ok)
	}
}

func TestParseStringEscapes(t *testing.T) {
	raw := []byte(`{"s":"a\"b\\c\n\u4e2d"}`)
	n, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := n.StringField("s")
	want := "a\"b\\c\n中"
	if got != want {
		t.Fatalf("string = %q, want %q", got, want)
	}
}

func TestDuplicateKeyLastWins(t *testing.T) {
	n := MustParse([]byte(`{"k":1,"k":2}`))
	if v, _ := n.IntField("k"); v != 2 {
		t.Fatalf("k = %d, want 2", v)
	}
}

func TestParseRejectsTrailingAndInvalid(t *testing.T) {
	cases := [][]byte{
		[]byte(``),
		[]byte(`{} x`),
		[]byte(`{"a":}`),
		[]byte(`[1,]`),
		[]byte(`"unterminated`),
		[]byte(`tru`),
	}
	for _, raw := range cases {
		if _, err := Parse(raw); err == nil {
			t.Errorf("Parse(%q) succeeded, want error", raw)
		}
	}
}

func TestApplyReplacesAndPreserves(t *testing.T) {
	raw := []byte(`{"a":"secret","b":2}`)
	secret, _ := MustParse(raw).Get("a")
	edits := []Edit{{
		Span:   secret.Span,
		Bytes:  []byte("xxxxxx"),
		Mode:   ReplaceString,
		Reason: "test",
	}}
	out, err := Apply(raw, edits)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"a":"xxxxxx","b":2}` {
		t.Fatalf("out = %s", out)
	}
	if !json.Valid(out) {
		t.Fatal("invalid JSON")
	}
}

func TestApplyPreserveLengthRejects(t *testing.T) {
	raw := []byte(`{"a":"secret"}`)
	secret, _ := MustParse(raw).Get("a")
	_, err := Apply(raw, []Edit{{
		Span:           secret.Span,
		Bytes:          []byte("x"),
		Mode:           ReplaceString,
		PreserveLength: true,
	}})
	if err == nil || !strings.Contains(err.Error(), "preserve length") {
		t.Fatalf("err = %v, want preserve length", err)
	}
}

func TestApplyOverlapRejects(t *testing.T) {
	raw := []byte(`{"a":1}`)
	_, err := Apply(raw, []Edit{
		{Span: Span{Start: 1, End: 4}, Bytes: []byte(`"x"`), Mode: ReplaceRaw},
		{Span: Span{Start: 3, End: 5}, Bytes: []byte(`"y"`), Mode: ReplaceRaw},
	})
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("err = %v, want overlap", err)
	}
}

func TestApplyDelete(t *testing.T) {
	raw := []byte(`{"a":1,"b":2}`)
	b, _ := MustParse(raw).Get("b")
	member, _ := MustParse(raw).ObjectElem("b")
	_ = b
	// Delete the comma and the whole b member by covering from the comma
	// after "a" through the value. Simpler: delete the member value only and
	// accept the remaining key with null is not valid, so cover key through
	// value.
	span := Span{Start: member.KeySpan.Start, End: b.Span.End}
	// Include the preceding comma.
	start := span.Start
	for start > 0 && raw[start-1] != ',' {
		start--
	}
	if start > 0 {
		start--
	}
	out, err := Apply(raw, []Edit{{Span: Span{Start: start, End: span.End}, Mode: Delete}})
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != `{"a":1}` {
		t.Fatalf("out = %s", out)
	}
}

func TestApplyNoEditsReturnsSame(t *testing.T) {
	raw := []byte(`{"a":1}`)
	out, err := Apply(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != string(raw) {
		t.Fatalf("out = %s", out)
	}
}

func TestParseNumber(t *testing.T) {
	for _, raw := range []string{`0`, `-1`, `1.5`, `1e3`, `-2.5e-2`} {
		n, err := Parse([]byte(raw))
		if err != nil {
			t.Fatalf("Parse(%s): %v", raw, err)
		}
		if n.Kind != Number {
			t.Fatalf("kind = %v, want Number", n.Kind)
		}
	}
}
