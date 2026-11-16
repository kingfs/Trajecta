package gateway

import (
	"bytes"
	"testing"
)

func TestBodyPassthrough(t *testing.T) {
	client := []byte(`{"model":"m"}`)
	body := NewBody(client)
	if body.WireBytes() == nil || string(body.WireBytes()) != string(client) {
		t.Fatalf("WireBytes = %q", body.WireBytes())
	}
	// Passthrough means the wire is the client slice itself, not a copy.
	if !bytes.Equal(body.WireBytes(), body.Client) {
		t.Fatal("new body reported changed")
	}
}

func TestBodySetWire(t *testing.T) {
	body := NewBody([]byte(`{"model":"m"}`))
	wire := []byte(`{"model":"upstream"}`)
	body.SetWire(wire)
	if string(body.WireBytes()) != string(wire) {
		t.Fatalf("wire=%q", body.WireBytes())
	}
	// Setting an equal slice restores passthrough.
	body.SetWire([]byte(`{"model":"m"}`))
	if !bytes.Equal(body.WireBytes(), body.Client) {
		t.Fatal("equal wire reported changed")
	}
	// Setting the client slice itself also restores passthrough.
	body.SetWire(body.Client)
	if !bytes.Equal(body.WireBytes(), body.Client) {
		t.Fatal("client wire reported changed")
	}
}

func TestBodyClientIsPreserved(t *testing.T) {
	client := []byte(`{"model":"m"}`)
	body := NewBody(client)
	body.SetWire([]byte(`{"model":"upstream"}`))
	if string(body.Client) != string(client) {
		t.Fatalf("Client changed: %q", body.Client)
	}
}

func TestBodyEmpty(t *testing.T) {
	body := NewBody(nil)
	if body.WireBytes() != nil {
		t.Fatalf("empty body = %q", body.WireBytes())
	}
	body.SetWire([]byte("x"))
	if string(body.WireBytes()) != "x" {
		t.Fatalf("empty->wire = %q", body.WireBytes())
	}
}
