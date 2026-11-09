package registry

import (
	"testing"

	"github.com/kingfs/Trajecta/pkg/xir"
	"github.com/kingfs/Trajecta/pkg/xir/codec/chat"
)

func TestNewRegistryHasBuiltins(t *testing.T) {
	r := NewRegistry()
	want := []xir.Protocol{
		xir.ProtocolChatCompletions,
		xir.ProtocolResponses,
		xir.ProtocolMessages,
		xir.ProtocolGemini,
	}
	got := r.Protocols()
	if len(got) != len(want) {
		t.Fatalf("Protocols() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Protocols()[%d] = %q, want %q", i, got[i], want[i])
		}
		codec, ok := r.Lookup(want[i])
		if !ok || codec == nil {
			t.Fatalf("Lookup(%q) = %v, %v", want[i], codec, ok)
		}
		if codec.Protocol() != want[i] {
			t.Fatalf("codec.Protocol() = %q, want %q", codec.Protocol(), want[i])
		}
	}
}

func TestLookupMissing(t *testing.T) {
	r := NewRegistry()
	if codec, ok := r.Lookup(xir.Protocol("bogus")); ok || codec != nil {
		t.Fatalf("Lookup(bogus) = %v, %v; want nil, false", codec, ok)
	}
	var zero Registry
	if codec, ok := zero.Lookup(xir.ProtocolChatCompletions); ok || codec != nil {
		t.Fatalf("zero Registry Lookup = %v, %v; want nil, false", codec, ok)
	}
}

func TestRegister(t *testing.T) {
	r := NewRegistry()
	if err := r.Register(nil); err == nil {
		t.Fatal("Register(nil) error = nil")
	}
	if err := r.Register(chat.New()); err != nil {
		t.Fatalf("Register(chat) error = %v", err)
	}
	codec, ok := r.Lookup(xir.ProtocolChatCompletions)
	if !ok || codec == nil {
		t.Fatal("chat codec missing after Register")
	}
}

func TestDefault(t *testing.T) {
	if Default() == nil {
		t.Fatal("Default() = nil")
	}
	if _, ok := Default().Lookup(xir.ProtocolResponses); !ok {
		t.Fatal("Default() has no Responses codec")
	}
}
