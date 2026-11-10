package gateway

import "testing"

func TestRewriteModelAlias(t *testing.T) {
	body := []byte(`{"model":"alias","x":1}`)
	got, ok := RewriteModelAlias(body, "alias", "wire")
	if !ok || string(got) != `{"model":"wire","x":1}` {
		t.Fatalf("got=%s ok=%v", got, ok)
	}
	if got, ok := RewriteModelAlias(body, "alias", "ALIAS"); ok || &got[0] != &body[0] {
		t.Fatal("equal alias should return the original slice")
	}
}

func TestRewriteModelPath(t *testing.T) {
	cases := []struct {
		name      string
		path      string
		requested string
		upstream  string
		want      string
		rewritten bool
	}{
		{"ai studio generate", "/v1beta/models/gemini-2.5-flash:generateContent", "gemini-2.5-flash", "wire-gemini", "/v1beta/models/wire-gemini:generateContent", true},
		{"ai studio stream", "/v1beta/models/gemini-2.5-flash:streamGenerateContent", "gemini-2.5-flash", "wire-gemini", "/v1beta/models/wire-gemini:streamGenerateContent", true},
		{"vertex resource", "/v1/publishers/google/models/gemini-2.5-pro:generateContent", "gemini-2.5-pro", "wire-pro", "/v1/publishers/google/models/wire-pro:generateContent", true},
		{"vertex scoped", "/v1/projects/p/locations/l/publishers/google/models/gemini-2.5-pro:generateContent", "gemini-2.5-pro", "wire-pro", "/v1/projects/p/locations/l/publishers/google/models/wire-pro:generateContent", true},
		{"query preserved", "/v1beta/models/gemini:generateContent?alt=sse", "gemini", "wire", "/v1beta/models/wire:generateContent?alt=sse", true},
		{"case insensitive request", "/v1beta/models/Gemini-2.5:generateContent", "gemini-2.5", "wire", "/v1beta/models/wire:generateContent", true},
		{"same model untouched", "/v1beta/models/gemini:generateContent", "gemini", "GEMINI", "/v1beta/models/gemini:generateContent", false},
		{"other model untouched", "/v1beta/models/other:generateContent", "gemini", "wire", "/v1beta/models/other:generateContent", false},
		{"no model segment", "/v1beta/models:generateContent", "gemini", "wire", "/v1beta/models:generateContent", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, rewritten := RewriteModelPath(tc.path, tc.requested, tc.upstream)
			if got != tc.want || rewritten != tc.rewritten {
				t.Fatalf("RewriteModelPath(%q,%q,%q) = %q,%v want %q,%v", tc.path, tc.requested, tc.upstream, got, rewritten, tc.want, tc.rewritten)
			}
		})
	}
}
