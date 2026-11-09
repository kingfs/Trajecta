// Package gateway is the home of the forwarding core's protocol-independent
// request/response pipeline. This first slice introduces Body, the explicit
// pair of client-visible and upstream-wire bytes.
package gateway

import "bytes"

// Body keeps the two byte sequences a forwarded request has:
//
//   - Client is exactly what the client sent. It is the cassette/replay source
//     of truth and must never be replaced by a transformed body.
//   - wire is what the upstream actually receives (alias/conversion/masking).
//     A nil wire means "identical to Client"; this keeps the common
//     passthrough case allocation-free and makes "unchanged" explicit.
type Body struct {
	Client []byte

	wire []byte
}

// NewBody wraps the client body. The slice is not copied: callers must treat
// Client as read-only after construction.
func NewBody(client []byte) *Body { return &Body{Client: client} }

// SetWire records the bytes that will be sent upstream. Passing nil, the same
// slice, or an equal slice restores the passthrough state.
func (b *Body) SetWire(wire []byte) {
	if b == nil {
		return
	}
	if len(wire) == 0 && len(b.Client) == 0 {
		b.wire = nil
		return
	}
	if wire == nil || bytes.Equal(wire, b.Client) {
		b.wire = nil
		return
	}
	b.wire = wire
}

// WireBytes returns the bytes to send upstream. It never returns nil for an
// empty client body; callers that need "was it changed" use Changed.
func (b *Body) WireBytes() []byte {
	if b == nil {
		return nil
	}
	if b.wire == nil {
		return b.Client
	}
	return b.wire
}

// Changed reports whether the upstream body differs from the client body.
func (b *Body) Changed() bool { return b != nil && b.wire != nil }
