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

func TestEnsureStreamIncludeUsage(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    string
		changed bool
	}{
		{
			name:    "missing options",
			raw:     `{"model":"m","stream":true,"seed":123456789012345678901}`,
			want:    `{"model":"m","stream":true,"seed":123456789012345678901,"stream_options":{"include_usage":true}}`,
			changed: true,
		},
		{
			name:    "empty object",
			raw:     `{ "stream" : true }`,
			want:    `{ "stream" : true ,"stream_options":{"include_usage":true}}`,
			changed: true,
		},
		{
			name:    "existing options missing key",
			raw:     `{"stream":true,"stream_options":{"foo":1}}`,
			want:    `{"stream":true,"stream_options":{"foo":1,"include_usage":true}}`,
			changed: true,
		},
		{
			name:    "false include usage",
			raw:     `{"stream":true,"stream_options":{"include_usage":false}}`,
			want:    `{"stream":true,"stream_options":{"include_usage":true}}`,
			changed: true,
		},
		{
			name:    "already true",
			raw:     `{"stream":true,"stream_options":{"include_usage":true}}`,
			want:    `{"stream":true,"stream_options":{"include_usage":true}}`,
			changed: false,
		},
		{
			name:    "not streaming",
			raw:     `{"stream":false}`,
			want:    `{"stream":false}`,
			changed: false,
		},
		{
			name:    "non-object",
			raw:     `[]`,
			want:    `[]`,
			changed: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, changed, err := EnsureStreamIncludeUsage([]byte(tc.raw))
			if err != nil {
				t.Fatal(err)
			}
			if changed != tc.changed {
				t.Fatalf("changed = %v, want %v", changed, tc.changed)
			}
			if string(got) != tc.want {
				t.Fatalf("got %s, want %s", got, tc.want)
			}
		})
	}
}
