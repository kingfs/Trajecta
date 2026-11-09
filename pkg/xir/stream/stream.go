// Package stream implements the protocol-neutral transport layer of the XIR
// streaming design: Server-Sent Event framing and the assembly of normalized
// xir.StreamEvent values into one xir.Response.
//
// The two halves are deliberately independent. Decoder and Encoder only know
// about SSE frames; they do not parse JSON, map event names to protocol
// semantics or aggregate deltas. Assembler only consumes normalized
// xir.StreamEvent values; it does not read or write SSE bytes. Cross-protocol
// streaming conversion is a non-goal of this package: callers that need it
// build a state machine from these three primitives.
package stream

// Frame is one Server-Sent Event frame.
//
// A frame is the unit between two blank lines in an SSE stream. Data is the
// frame payload with multi-line `data:` fields joined by "\n" and the trailing
// newline removed, matching the SSE dispatch algorithm. Data is always owned
// by the Frame: a Decoder allocates a fresh slice for each frame and an
// Encoder never retains it.
type Frame struct {
	// Event is the `event:` field, or "" when the frame did not name one.
	Event string
	// Data is the joined `data:` payload. It may be empty but non-nil for a
	// frame that carried an empty `data:` field.
	Data []byte
	// ID is the `id:` field, or "" when the frame did not name one.
	ID string
	// Retry is the `retry:` reconnection time in milliseconds. Zero means the
	// field was absent (or invalid and therefore ignored); the encoder writes
	// the field only for a positive value.
	Retry int
}
