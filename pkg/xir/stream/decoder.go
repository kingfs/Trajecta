package stream

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"strconv"
)

const (
	// DefaultMaxLineBytes bounds the size of one SSE line. It matches
	// recordfile.MaxStreamLineBytes, the bound the recorder and the replay
	// readers use, so a frame the proxy recorded can always be parsed back.
	DefaultMaxLineBytes = 4 << 20
	// DefaultInitialBuffer is the initial size of the Decoder's read buffer.
	DefaultInitialBuffer = 64 << 10
)

var (
	// ErrLineTooLong marks an SSE line longer than the configured maximum. A
	// line this long is not a recoverable framing error: there is no safe way
	// to resynchronise on the next blank line without trusting the oversized
	// payload, so the Decoder returns this error for the rest of its life.
	ErrLineTooLong = errors.New("xir/stream: sse line exceeds maximum length")
	// ErrClosed is returned by Next after Close.
	ErrClosed = errors.New("xir/stream: decoder closed")
	// ErrNilReader is returned by Next when the Decoder was built with a nil
	// reader.
	ErrNilReader = errors.New("xir/stream: nil reader")
)

// Option configures a Decoder. Options are applied in order; a non-positive
// value leaves the corresponding default in place.
type Option func(*decoderOptions)

type decoderOptions struct {
	maxLineBytes  int
	initialBuffer int
}

// WithMaxLineBytes overrides DefaultMaxLineBytes. Values <= 0 keep the
// default.
func WithMaxLineBytes(n int) Option {
	return func(o *decoderOptions) {
		if n > 0 {
			o.maxLineBytes = n
		}
	}
}

// WithInitialBuffer overrides DefaultInitialBuffer, the size of the Decoder's
// bufio read buffer. It is a performance knob only; lines longer than the
// buffer are accumulated up to the maximum line size, so a small buffer does
// not truncate a frame. Values <= 0 keep the default.
func WithInitialBuffer(n int) Option {
	return func(o *decoderOptions) {
		if n > 0 {
			o.initialBuffer = n
		}
	}
}

// Decoder reads SSE frames from an io.Reader one frame at a time.
//
// A Decoder is stateful and is not safe for concurrent use. It never
// interprets the payload: `[DONE]` is an ordinary data payload, and comments,
// unknown fields and event names are carried or ignored exactly as the SSE
// framing rules require.
type Decoder struct {
	br     *bufio.Reader
	closer io.Closer

	maxLineBytes int

	line []byte
	data []byte

	event    string
	id       string
	retry    int
	hasEvent bool
	hasID    bool
	hasRetry bool
	hasData  bool

	eof    bool
	closed bool
	err    error
}

// NewDecoder returns a Decoder reading from r. A nil r produces a Decoder
// whose Next returns ErrNilReader.
func NewDecoder(r io.Reader, opts ...Option) *Decoder {
	cfg := decoderOptions{
		maxLineBytes:  DefaultMaxLineBytes,
		initialBuffer: DefaultInitialBuffer,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	if cfg.maxLineBytes <= 0 {
		cfg.maxLineBytes = DefaultMaxLineBytes
	}
	if cfg.initialBuffer <= 0 {
		cfg.initialBuffer = DefaultInitialBuffer
	}
	// A buffer larger than the line limit is wasted: no line can fill it.
	if cfg.initialBuffer > cfg.maxLineBytes {
		cfg.initialBuffer = cfg.maxLineBytes
	}

	d := &Decoder{maxLineBytes: cfg.maxLineBytes}
	if r == nil {
		d.err = ErrNilReader
		return d
	}
	d.br = bufio.NewReaderSize(r, cfg.initialBuffer)
	d.closer, _ = r.(io.Closer)
	return d
}

// Next returns the next complete frame. It returns io.EOF only after the final
// frame; a stream whose last line has no terminating blank line still yields
// that frame before io.EOF. A comment-only stretch of the stream yields no
// frame. After a framing or read error Next keeps returning that error.
func (d *Decoder) Next() (Frame, error) {
	if d.closed {
		return Frame{}, ErrClosed
	}
	if d.err != nil {
		return Frame{}, d.err
	}
	if d.eof {
		return Frame{}, io.EOF
	}

	for {
		line, err := d.readLine()
		if err == nil {
			if len(line) == 0 {
				if frame, ok := d.frame(); ok {
					return frame, nil
				}
				continue
			}
			d.field(line)
			continue
		}

		// The reader ended inside a line. The partial line is still a valid
		// final field, and any accumulated frame is dispatched before the
		// terminal error is reported.
		if len(line) > 0 {
			d.field(line)
		}
		if err == io.EOF {
			d.eof = true
		} else {
			d.err = err
		}
		if frame, ok := d.frame(); ok {
			return frame, nil
		}
		if err == io.EOF {
			return Frame{}, io.EOF
		}
		return Frame{}, err
	}
}

// Close closes the underlying reader when it implements io.Closer and marks
// the Decoder closed. It is idempotent; Next returns ErrClosed afterwards.
func (d *Decoder) Close() error {
	if d == nil || d.closed {
		return nil
	}
	d.closed = true
	if d.closer != nil {
		return d.closer.Close()
	}
	return nil
}

// readLine returns one line without its terminator (LF or CRLF). A line whose
// bytes exceed maxLineBytes returns ErrLineTooLong. At EOF a partial final line
// is returned together with io.EOF.
func (d *Decoder) readLine() ([]byte, error) {
	d.line = d.line[:0]
	for {
		fragment, err := d.br.ReadSlice('\n')
		if len(fragment) > 0 {
			content := fragment
			complete := err == nil
			if complete {
				content = fragment[:len(fragment)-1]
			}
			// A CR immediately before the terminator is part of CRLF and not
			// part of the line, so it must not count against the line limit.
			if (complete || err == io.EOF) && len(content) > 0 && content[len(content)-1] == '\r' {
				content = content[:len(content)-1]
			}
			if len(d.line)+len(content) > d.maxLineBytes {
				return nil, fmt.Errorf("%w (%d bytes, limit %d)", ErrLineTooLong, len(d.line)+len(content), d.maxLineBytes)
			}
			d.line = append(d.line, content...)
			if complete {
				return d.line, nil
			}
		}
		if err != nil {
			if err == bufio.ErrBufferFull {
				continue
			}
			if len(d.line) > 0 {
				return trimCR(d.line), err
			}
			return nil, err
		}
	}
}

// trimCR removes a trailing carriage return, turning CRLF into LF. A bare CR
// inside a line is data; only the terminator pair is normalised.
func trimCR(line []byte) []byte {
	if n := len(line); n > 0 && line[n-1] == '\r' {
		return line[:n-1]
	}
	return line
}

// field applies one non-blank SSE line to the pending frame.
func (d *Decoder) field(line []byte) {
	if len(line) == 0 || line[0] == ':' {
		return
	}

	name := line
	var value []byte
	if colon := bytes.IndexByte(line, ':'); colon >= 0 {
		name = line[:colon]
		value = line[colon+1:]
		// SSE strips one optional space after the colon. Further spaces are
		// data and must survive (the encoder emits an extra space for a value
		// that itself starts with one).
		if len(value) > 0 && value[0] == ' ' {
			value = value[1:]
		}
	}

	switch string(name) {
	case "data":
		d.hasData = true
		d.data = append(d.data, value...)
		d.data = append(d.data, '\n')
	case "event":
		d.hasEvent = true
		d.event = string(value)
	case "id":
		// The SSE specification forbids a NUL in an id. Ignore the field
		// rather than carrying a value a reconnecting client cannot use.
		if !bytes.ContainsRune(value, 0) {
			d.hasID = true
			d.id = string(value)
		}
	case "retry":
		if n, ok := parseRetry(value); ok {
			d.hasRetry = true
			d.retry = n
		}
	default:
		// Unknown fields are ignored, as SSE requires.
	}
}

// parseRetry accepts only a run of ASCII digits. Surrounding spaces are
// tolerated: the field is numeric, so their only possible meaning is the
// optional post-colon space some producers repeat.
func parseRetry(value []byte) (int, bool) {
	value = bytes.TrimSpace(value)
	if len(value) == 0 {
		return 0, false
	}
	for _, b := range value {
		if b < '0' || b > '9' {
			return 0, false
		}
	}
	n, err := strconv.Atoi(string(value))
	if err != nil {
		return 0, false
	}
	return n, true
}

// frame dispatches the pending frame, resetting the per-frame state. It
// reports false when the terminator followed no field at all.
func (d *Decoder) frame() (Frame, bool) {
	if !d.hasData && !d.hasEvent && !d.hasID && !d.hasRetry {
		return Frame{}, false
	}

	var data []byte
	if d.hasData {
		// Each data field appended its value plus a newline; the dispatch
		// algorithm removes exactly the final newline. Copy so the caller owns
		// the slice independently of the decoder's reuse.
		n := len(d.data)
		data = make([]byte, n-1)
		copy(data, d.data[:n-1])
	}

	frame := Frame{
		Event: d.event,
		Data:  data,
		ID:    d.id,
		Retry: d.retry,
	}

	d.data = d.data[:0]
	d.event = ""
	d.id = ""
	d.retry = 0
	d.hasData = false
	d.hasEvent = false
	d.hasID = false
	d.hasRetry = false
	return frame, true
}
