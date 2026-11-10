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
