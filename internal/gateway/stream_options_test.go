package gateway

import (
	"net/http/httptest"
	"testing"
)

func TestInjectStreamOptions(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	req.Header.Set("Content-Type", "application/json")
	got := InjectStreamOptions(req, []byte(`{"model":"m","stream":true}`))
	if string(got) != `{"model":"m","stream":true,"stream_options":{"include_usage":true}}` {
		t.Fatalf("body = %s", got)
	}

	get := httptest.NewRequest("GET", "/v1/chat/completions", nil)
	get.Header.Set("Content-Type", "application/json")
	if got := InjectStreamOptions(get, []byte(`{"stream":true}`)); string(got) != `{"stream":true}` {
		t.Fatalf("GET body changed: %s", got)
	}
	wrongPath := httptest.NewRequest("POST", "/v1/messages", nil)
	wrongPath.Header.Set("Content-Type", "application/json")
	if got := InjectStreamOptions(wrongPath, []byte(`{"stream":true}`)); string(got) != `{"stream":true}` {
		t.Fatalf("non-chat body changed: %s", got)
	}
}
