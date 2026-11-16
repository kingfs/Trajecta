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
	// The production close path goes through the client reader (UsageSniffer
	// owns that chain in the forwarding path).
	if err := body.ClientReader().Close(); err != nil {
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
