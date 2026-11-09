package stream

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

var (
	// ErrNilWriter is returned by WriteFrame and Flush when the Encoder was
	// built with a nil writer.
	ErrNilWriter = errors.New("xir/stream: nil writer")
	// ErrInvalidField marks an event or id value that contains a line break
	// and therefore cannot be represented as one SSE field.
	ErrInvalidField = errors.New("xir/stream: field value contains a line break")
)

// Encoder writes SSE frames to an io.Writer.
//
// A frame is a sequence of `event:`, `id:` and `retry:` fields (when set),
// one `data:` field per line of Frame.Data, and a terminating blank line. The
// writer is flushed after every frame when it exposes either Flush() error or
// the http.Flusher-shaped Flush() (no error).
type Encoder struct {
	w io.Writer
}

// NewEncoder returns an Encoder writing to w.
func NewEncoder(w io.Writer) *Encoder {
	return &Encoder{w: w}
}

// WriteFrame writes one frame and flushes the underlying writer when it
// supports flushing. It validates only framing invariants: an event or id
// containing a line break cannot be represented and returns ErrInvalidField.
func (e *Encoder) WriteFrame(frame Frame) error {
	if e == nil || e.w == nil {
		return ErrNilWriter
	}
	if strings.ContainsAny(frame.Event, "\r\n") {
		return fmt.Errorf("%w: event %q", ErrInvalidField, frame.Event)
	}
	if strings.ContainsAny(frame.ID, "\r\n") {
		return fmt.Errorf("%w: id %q", ErrInvalidField, frame.ID)
	}

	var buf bytes.Buffer
	if frame.Event != "" {
		writeField(&buf, "event", frame.Event)
	}
	if frame.ID != "" {
		writeField(&buf, "id", frame.ID)
	}
	if frame.Retry > 0 {
		buf.WriteString("retry: ")
		buf.WriteString(strconv.Itoa(frame.Retry))
		buf.WriteByte('\n')
	}

	// Data always emits at least one field, so a frame with an empty payload
	// round-trips as a frame with empty data rather than as a blank line.
	for _, line := range strings.Split(string(frame.Data), "\n") {
		buf.WriteString("data:")
		if line != "" {
			buf.WriteByte(' ')
			buf.WriteString(line)
		}
		buf.WriteByte('\n')
	}
	buf.WriteByte('\n')

	if _, err := e.w.Write(buf.Bytes()); err != nil {
		return err
	}
	return e.Flush()
}

// Flush flushes the underlying writer when it supports flushing. A writer
// with no Flush method is not buffered from this package's point of view, so
// Flush succeeds.
func (e *Encoder) Flush() error {
	if e == nil || e.w == nil {
		return ErrNilWriter
	}
	switch flusher := e.w.(type) {
	case interface{ Flush() error }:
		return flusher.Flush()
	case interface{ Flush() }:
		flusher.Flush()
		return nil
	default:
		return nil
	}
}

func writeField(buf *bytes.Buffer, name, value string) {
	buf.WriteString(name)
	buf.WriteString(": ")
	buf.WriteString(value)
	buf.WriteByte('\n')
}
