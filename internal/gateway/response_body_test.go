package gateway

import (
	"io"
	"strings"
	"testing"
)

func TestResponseBodyPassthrough(t *testing.T) {
	upstream := io.NopCloser(strings.NewReader("raw"))
	body := NewResponseBody(upstream)
	if body.ClientReader() != upstream {
		t.Fatal("passthrough client is not upstream")
	}
	if err := body.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestResponseBodyClientOverride(t *testing.T) {
	upstream := io.NopCloser(strings.NewReader("raw"))
	client := io.NopCloser(strings.NewReader("restored"))
	body := NewResponseBody(upstream)
	body.SetClient(client)
	if body.ClientReader() != client {
		t.Fatal("client override not used")
	}
	if body.Upstream != upstream {
		t.Fatal("upstream was replaced")
	}
	body.SetClient(nil)
	if body.ClientReader() != upstream {
		t.Fatal("nil client did not restore passthrough")
	}
}
