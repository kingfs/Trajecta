package gateway

import "testing"

func TestBodyPassthrough(t *testing.T) {
	client := []byte(`{"model":"m"}`)
	body := NewBody(client)
	if body.WireBytes() == nil || string(body.WireBytes()) != string(client) {
		t.Fatalf("WireBytes = %q", body.WireBytes())
	}
	if body.Changed() {
		t.Fatal("new body reported changed")
	}
}

func TestBodySetWire(t *testing.T) {
	body := NewBody([]byte(`{"model":"m"}`))
	wire := []byte(`{"model":"upstream"}`)
	body.SetWire(wire)
	if !body.Changed() || string(body.WireBytes()) != string(wire) {
		t.Fatalf("changed=%v wire=%q", body.Changed(), body.WireBytes())
	}
	// Setting an equal slice restores passthrough.
	body.SetWire([]byte(`{"model":"m"}`))
	if body.Changed() {
		t.Fatal("equal wire reported changed")
	}
	// Setting the client slice itself also restores passthrough.
	body.SetWire(body.Client)
	if body.Changed() {
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
	if body.WireBytes() != nil || body.Changed() {
		t.Fatalf("empty body = %q/%v", body.WireBytes(), body.Changed())
	}
	body.SetWire([]byte("x"))
	if !body.Changed() || string(body.WireBytes()) != "x" {
		t.Fatalf("empty->wire = %q/%v", body.WireBytes(), body.Changed())
	}
}
