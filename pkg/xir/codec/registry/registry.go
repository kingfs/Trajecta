// Package registry maps xir.Protocol values to the codec that speaks them.
//
// The registry is deliberately small: codecs depend only on pkg/xir, while this
// package is the one place that imports all four built-in codecs. Callers that
// only need conversion can use pkg/xir/convert, which owns a default registry.
package registry

import (
	"fmt"
	"sync"

	"github.com/kingfs/Trajecta/pkg/xir"
	"github.com/kingfs/Trajecta/pkg/xir/codec/chat"
	"github.com/kingfs/Trajecta/pkg/xir/codec/gemini"
	"github.com/kingfs/Trajecta/pkg/xir/codec/messages"
	"github.com/kingfs/Trajecta/pkg/xir/codec/responses"
)

// Registry is a concurrency-safe protocol-to-codec map. The zero value is
// ready to use but contains no codecs; NewRegistry returns one with all four
// built-ins registered.
type Registry struct {
	mu     sync.RWMutex
	codecs map[xir.Protocol]xir.Codec
}

// NewRegistry returns a registry with the Chat Completions, Responses,
// Messages and Gemini codecs registered.
func NewRegistry() *Registry {
	r := &Registry{codecs: make(map[xir.Protocol]xir.Codec, 4)}
	for _, codec := range []xir.Codec{
		chat.New(),
		responses.New(),
		messages.New(),
		gemini.New(),
	} {
		if err := r.Register(codec); err != nil {
			// The built-ins are known-good; a failure here is a programming
			// error, not runtime input.
			panic(err)
		}
	}
	return r
}

var defaultRegistry = NewRegistry()

// Default returns the process-wide registry with the four built-ins. Callers
// that need an isolated registry should build their own with NewRegistry.
func Default() *Registry { return defaultRegistry }

// Register adds or replaces the codec for its protocol. A nil codec or a codec
// with an invalid protocol is rejected.
func (r *Registry) Register(codec xir.Codec) error {
	if r == nil {
		return fmt.Errorf("registry: nil registry")
	}
	if codec == nil {
		return fmt.Errorf("registry: nil codec")
	}
	protocol := codec.Protocol()
	if !protocol.Valid() {
		return fmt.Errorf("registry: codec reports invalid protocol %q", protocol)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.codecs == nil {
		r.codecs = make(map[xir.Protocol]xir.Codec, 4)
	}
	r.codecs[protocol] = codec
	return nil
}

// Lookup returns the codec registered for protocol. The bool is false when no
// codec is registered.
func (r *Registry) Lookup(protocol xir.Protocol) (xir.Codec, bool) {
	if r == nil {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	codec, ok := r.codecs[protocol]
	return codec, ok
}

// Protocols returns the registered protocols in their canonical order.
func (r *Registry) Protocols() []xir.Protocol {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]xir.Protocol, 0, len(r.codecs))
	for _, protocol := range []xir.Protocol{
		xir.ProtocolChatCompletions,
		xir.ProtocolResponses,
		xir.ProtocolMessages,
		xir.ProtocolGemini,
	} {
		if _, ok := r.codecs[protocol]; ok {
			out = append(out, protocol)
		}
	}
	return out
}
