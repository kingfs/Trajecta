package xir

import (
	"strings"
	"testing"
)

func TestRewriteModelPreservesBytes(t *testing.T) {
	raw := []byte(`{"model":"alias","seed":123456789012345678901,"temperature":0.70,"nested":{"x":1}}`)
	got, ok, err := RewriteModel(raw, "upstream")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("model not rewritten")
	}
	want := `{"model":"upstream","seed":123456789012345678901,"temperature":0.70,"nested":{"x":1}}`
	if string(got) != want {
		t.Fatalf("got %s, want %s", got, want)
	}
}

func TestRewriteModelRejectsNonObjectOrMissing(t *testing.T) {
	for _, raw := range []string{`[]`, `{"x":1}`, `{"model":1}`, `{bad`} {
		got, ok, err := RewriteModel([]byte(raw), "m")
		if err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
		if ok || string(got) != raw {
			t.Fatalf("%s: got %s/%v", raw, got, ok)
		}
	}
}

func TestRewriteModelEscapesValue(t *testing.T) {
	raw := []byte(`{"model":"alias"}`)
	got, ok, err := RewriteModel(raw, `up"stream`)
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if !strings.Contains(string(got), `up\"stream`) {
		t.Fatalf("value not escaped: %s", got)
	}
}
