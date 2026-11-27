package gateway

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"sync"
	"time"

	"github.com/kingfs/Trajecta/internal/recorder"
	"github.com/kingfs/Trajecta/pkg/xir/wire"
)

// maxReasoningSSEFrameBytes bounds one frame this normalizer will parse or
// hold. A Responses frame is normally a few KiB (the encrypted reasoning state
// is the bulk); the bound only keeps a non-compliant or malicious upstream from
// turning the delay into unbounded memory. Exceeding it falls back to
// pass-through for the frames at hand.
const maxReasoningSSEFrameBytes = 8 << 20

// ReasoningSignaturePolicy remembers whether an upstream ever returned a
// reasoning done value that differs from its terminal value. The first
// response from an unknown upstream is normalized because the client must not
// store a session-bound token; after that, a consistent upstream is passed
// through in stream order and only an observed mismatch flips it back to the
// normalized path. A nil policy means "always normalize" (tests and callers
// that do not share state).
type ReasoningSignaturePolicy struct {
	mu     sync.Mutex
	states map[string]reasoningSignatureState
}

type reasoningSignatureState uint8

const (
	reasoningSignatureUnknown reasoningSignatureState = iota
	reasoningSignatureConsistent
	reasoningSignatureInconsistent
)

// NewReasoningSignaturePolicy returns an empty policy: every upstream starts
// unknown and its first reasoning response is normalized.
func NewReasoningSignaturePolicy() *ReasoningSignaturePolicy {
	return &ReasoningSignaturePolicy{states: make(map[string]reasoningSignatureState)}
}

// shouldHold reports whether the upstream's reasoning done frame must be
// delayed for normalization.
func (p *ReasoningSignaturePolicy) shouldHold(upstreamID string) bool {
	if p == nil {
		return true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.states[upstreamID] != reasoningSignatureConsistent
}

// observe records the outcome of one response. A mismatch is sticky; a clean
// response only promotes an unknown upstream to consistent.
func (p *ReasoningSignaturePolicy) observe(upstreamID string, sawReasoning, mismatch bool) {
	if p == nil || !sawReasoning {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if mismatch {
		p.states[upstreamID] = reasoningSignatureInconsistent
		return
	}
	if p.states[upstreamID] == reasoningSignatureUnknown {
		p.states[upstreamID] = reasoningSignatureConsistent
	}
}

// reasoningSignatureNormalizer rewrites the encrypted_content of a streaming
// Responses reasoning item so the client stores the terminal value.
//
// A few OpenAI-compatible relays encrypt reasoning twice: the
// response.output_item.done item carries an encryption bound to the upstream
// session, while response.completed may carry a self-contained value. The
// client library follows OpenAI's documented rule and persists the done item,
// so the next request replays a token that only the original session can
// verify. When that session is lost (relay failover, cache eviction) the
// provider rejects the whole request with invalid_encrypted_content, and every
// later turn fails because the bad token stays in the history.
//
// A relay may also repeat the same session-bound value in both events; this
// normalizer sees nothing to rewrite there. The request-side quarantine drop is
// the fallback for that case.
//
// The terminal event is authoritative. This normalizer holds the reasoning
// done frame until response.completed/response.incomplete arrives, replaces
// only its encrypted_content with the terminal value, and emits it immediately
// before the terminal frame. Non-reasoning frames and every other byte are
// forwarded as they arrive; only the one reasoning done frame is delayed.
//
// The request-side opaque pass-through is untouched: secret masking already
// skips the whole reasoning object, and this transform never rewrites the
// client request.
type reasoningSignatureNormalizer struct {
	source io.ReadCloser

	buf []byte
	out []byte

	held      []heldReasoningFrame
	heldBytes int

	events     *[]recorder.RecordEvent
	policy     *ReasoningSignaturePolicy
	upstreamID string
	hold       bool
	observed   map[string]string

	eof      bool
	err      error
	disabled bool
}

type sseFrame struct {
	event string
	data  []byte
}

type heldReasoningFrame struct {
	raw  []byte
	sep  []byte
	id   string
	blob string
}

// NewReasoningSignatureNormalizer wraps a client-visible Responses SSE stream.
// events, when non-nil, receives one reasoning.signature_normalized event per
// rewritten item. policy and upstreamID decide whether this response holds the
// reasoning done frame; a nil policy always holds. The normalizer owns source
// and closes it from Close.
func NewReasoningSignatureNormalizer(source io.ReadCloser, events *[]recorder.RecordEvent, policy *ReasoningSignaturePolicy, upstreamID string) io.ReadCloser {
	if source == nil {
		return nil
	}
	hold := policy == nil || policy.shouldHold(upstreamID)
	return &reasoningSignatureNormalizer{
		source:     source,
		events:     events,
		policy:     policy,
		upstreamID: upstreamID,
		hold:       hold,
		observed:   make(map[string]string),
	}
}

func (n *reasoningSignatureNormalizer) Read(p []byte) (int, error) {
	if n == nil || n.source == nil {
		return 0, io.ErrClosedPipe
	}
	if len(p) == 0 {
		return 0, nil
	}
	for {
		if len(n.out) > 0 {
			read := copy(p, n.out)
			n.out = n.out[read:]
			return read, nil
		}
		for {
			raw, sep, ok := n.takeFrame()
			if !ok {
				break
			}
			n.consume(raw, sep)
		}
		if len(n.out) > 0 {
			continue
		}
		if n.eof {
			n.flushHeldUnchanged()
			if len(n.out) > 0 {
				continue
			}
			return 0, io.EOF
		}
		if n.err != nil {
			n.flushHeldUnchanged()
			if len(n.out) > 0 {
				continue
			}
			err := n.err
			n.err = nil
			n.eof = true
			return 0, err
		}
		var chunk [32 << 10]byte
		read, err := n.source.Read(chunk[:])
		if read > 0 {
			n.buf = append(n.buf, chunk[:read]...)
		}
		switch {
		case err == io.EOF:
			n.eof = true
		case err != nil:
			n.err = err
		}
	}
}

func (n *reasoningSignatureNormalizer) Close() error {
	if n == nil || n.source == nil {
		return nil
	}
	return n.source.Close()
}

// takeFrame returns the next complete frame without its blank-line separator.
// It supports both LF and CRLF framing and leaves an incomplete tail buffered.
func (n *reasoningSignatureNormalizer) takeFrame() (raw, sep []byte, ok bool) {
	if len(n.buf) == 0 {
		return nil, nil, false
	}
	lf := bytes.Index(n.buf, []byte("\n\n"))
	crlf := bytes.Index(n.buf, []byte("\r\n\r\n"))
	switch {
	case crlf >= 0 && (lf < 0 || crlf < lf):
		raw, sep = n.buf[:crlf], n.buf[crlf:crlf+4]
	case lf >= 0:
		raw, sep = n.buf[:lf], n.buf[lf:lf+2]
	default:
		if n.eof && len(n.buf) > 0 {
			raw = n.buf
			n.buf = nil
			return raw, nil, true
		}
		if len(n.buf) > maxReasoningSSEFrameBytes {
			// No separator in an oversized buffer: stop parsing and pass the
			// rest through. The held frames still get a chance to normalize if
			// a terminal event exists later; otherwise EOF flushes them.
			n.disabled = true
			n.flushHeldUnchanged()
			n.emit(n.buf, nil)
			n.buf = nil
		}
		return nil, nil, false
	}
	n.buf = n.buf[len(raw)+len(sep):]
	return raw, sep, true
}

func (n *reasoningSignatureNormalizer) consume(raw, sep []byte) {
	if n.disabled {
		n.emit(raw, sep)
		return
	}
	if len(raw) > maxReasoningSSEFrameBytes {
		// A frame this large is not a normal reasoning item. Emit whatever is
		// held and pass it through without parsing; the client still receives
		// the upstream value, only without normalization.
		n.flushHeldUnchanged()
		n.emit(raw, sep)
		return
	}
	frame, hasData := parseSSEFrame(raw)
	if !hasData || frame.event == "" {
		n.emit(raw, sep)
		return
	}
	switch frame.event {
	case "response.output_item.done":
		if n.heldBytes+len(raw) > maxReasoningSSEFrameBytes {
			n.flushHeldUnchanged()
			n.emit(raw, sep)
			return
		}
		if n.holdReasoning(raw, sep, frame.data) {
			return
		}
		n.emit(raw, sep)
	case "response.completed", "response.incomplete":
		n.finishTerminal(raw, sep, frame.data, frame.event)
	case "response.failed", "error":
		n.flushHeldUnchanged()
		n.emit(raw, sep)
	default:
		n.emit(raw, sep)
	}
}

// holdReasoning reports whether the frame is a reasoning output_item.done with
// encrypted_content. Such a frame is delayed until the terminal event so its
// value can be replaced.
func (n *reasoningSignatureNormalizer) holdReasoning(raw, sep, data []byte) bool {
	root, err := wire.Parse(data)
	if err != nil {
		return false
	}
	item, ok := root.Get("item")
	if !ok {
		return false
	}
	if kind, _ := item.StringField("type"); kind != "reasoning" {
		return false
	}
	id, _ := item.StringField("id")
	blob, ok := item.StringField("encrypted_content")
	if id == "" || !ok || blob == "" {
		return false
	}
	if !n.hold {
		n.observed[id] = blob
		return false
	}
	n.held = append(n.held, heldReasoningFrame{
		raw:  append([]byte(nil), raw...),
		sep:  append([]byte(nil), sep...),
		id:   id,
		blob: blob,
	})
	n.heldBytes += len(raw)
	return true
}

// finishTerminal emits the delayed reasoning frames with their terminal
// encrypted_content, then the terminal frame itself. The terminal event is the
// authoritative full response; a missing or non-reasoning output leaves the
// held frame byte-for-byte unchanged.
func (n *reasoningSignatureNormalizer) finishTerminal(raw, sep, data []byte, event string) {
	canonical := terminalReasoningBlobs(data)
	if !n.hold {
		mismatch := false
		for id, blob := range n.observed {
			terminal, ok := canonical[id]
			if ok && terminal != blob {
				mismatch = true
				n.recordMismatch(id, blob, terminal, event)
			}
		}
		n.policy.observe(n.upstreamID, len(n.observed) > 0, mismatch)
		n.observed = make(map[string]string)
		n.emit(raw, sep)
		return
	}
	mismatch := false
	for _, held := range n.held {
		frame := held.raw
		if blob, ok := canonical[held.id]; ok && blob != held.blob {
			mismatch = true
			if rewritten, ok := rewriteEncryptedContent(held.raw, blob); ok {
				frame = rewritten
				n.recordNormalized(held, blob, event)
			}
		}
		n.emit(frame, held.sep)
	}
	n.policy.observe(n.upstreamID, len(n.held) > 0, mismatch)
	n.held = nil
	n.heldBytes = 0
	n.emit(raw, sep)
}

func (n *reasoningSignatureNormalizer) flushHeldUnchanged() {
	for _, held := range n.held {
		n.emit(held.raw, held.sep)
	}
	n.held = nil
	n.heldBytes = 0
}

func (n *reasoningSignatureNormalizer) emit(raw, sep []byte) {
	n.out = append(n.out, raw...)
	n.out = append(n.out, sep...)
}

func (n *reasoningSignatureNormalizer) recordMismatch(id, from, to, event string) {
	if n.events == nil {
		return
	}
	*n.events = append(*n.events, recorder.RecordEvent{
		Type: "reasoning.signature_mismatch",
		Time: time.Now().UTC(),
		Attributes: map[string]interface{}{
			"item_id":        id,
			"from_sha256":    shortHash(from),
			"to_sha256":      shortHash(to),
			"from_length":    len(from),
			"to_length":      len(to),
			"terminal_event": event,
		},
	})
}

func (n *reasoningSignatureNormalizer) recordNormalized(held heldReasoningFrame, blob, event string) {
	if n.events == nil {
		return
	}
	*n.events = append(*n.events, recorder.RecordEvent{
		Type: "reasoning.signature_normalized",
		Time: time.Now().UTC(),
		Attributes: map[string]interface{}{
			"item_id":        held.id,
			"from_sha256":    shortHash(held.blob),
			"to_sha256":      shortHash(blob),
			"from_length":    len(held.blob),
			"to_length":      len(blob),
			"terminal_event": event,
		},
	})
}

// terminalReasoningBlobs indexes the terminal response's reasoning items by id.
func terminalReasoningBlobs(data []byte) map[string]string {
	root, err := wire.Parse(data)
	if err != nil {
		return nil
	}
	response, ok := root.Get("response")
	if !ok {
		return nil
	}
	output, ok := response.Get("output")
	if !ok || output.Kind != wire.Array {
		return nil
	}
	blobs := make(map[string]string, output.Len())
	for i := 0; i < output.Len(); i++ {
		item, ok := output.Index(i)
		if !ok {
			continue
		}
		if kind, _ := item.StringField("type"); kind != "reasoning" {
			continue
		}
		id, _ := item.StringField("id")
		blob, ok := item.StringField("encrypted_content")
		if id != "" && ok && blob != "" {
			blobs[id] = blob
		}
	}
	return blobs
}

// rewriteEncryptedContent replaces only the item's encrypted_content member,
// preserving the rest of the event and the rest of the data payload.
func rewriteEncryptedContent(raw []byte, blob string) ([]byte, bool) {
	frame, hasData := parseSSEFrame(raw)
	if !hasData {
		return nil, false
	}
	root, err := wire.Parse(frame.data)
	if err != nil {
		return nil, false
	}
	item, ok := root.Get("item")
	if !ok {
		return nil, false
	}
	member, ok := item.ObjectElem("encrypted_content")
	if !ok || member.Value.Kind != wire.String {
		return nil, false
	}
	rewritten, err := wire.Apply(frame.data, []wire.Edit{{
		Span:  member.Value.Span,
		Bytes: []byte(blob),
		Mode:  wire.ReplaceString,
	}})
	if err != nil {
		return nil, false
	}
	return replaceSSEData(raw, rewritten), true
}

// replaceSSEData replaces the single `data:` value of a frame. Responses
// frames carry the JSON payload on one line; a multi-line payload (not emitted
// by any supported provider) is rebuilt from its fields instead.
func replaceSSEData(raw, data []byte) []byte {
	start, end, ok := singleDataValueSpan(raw)
	if !ok {
		return rebuildSSEFrame(raw, data)
	}
	out := make([]byte, 0, len(raw)-(end-start)+len(data))
	out = append(out, raw[:start]...)
	out = append(out, data...)
	out = append(out, raw[end:]...)
	return out
}

func singleDataValueSpan(raw []byte) (start, end int, ok bool) {
	lineStart := 0
	found := false
	for lineStart <= len(raw) {
		lineEnd := bytes.IndexByte(raw[lineStart:], '\n')
		if lineEnd < 0 {
			lineEnd = len(raw)
		} else {
			lineEnd += lineStart
		}
		line := raw[lineStart:lineEnd]
		line = bytes.TrimSuffix(line, []byte("\r"))
		if bytes.HasPrefix(line, []byte("data:")) {
			if found {
				return 0, 0, false
			}
			valueStart := lineStart + len("data:")
			if valueStart < lineEnd && raw[valueStart] == ' ' {
				valueStart++
			}
			valueEnd := lineStart + len(line)
			return valueStart, valueEnd, true
		}
		if lineEnd == len(raw) {
			break
		}
		lineStart = lineEnd + 1
	}
	return 0, 0, false
}

func rebuildSSEFrame(raw, data []byte) []byte {
	var out bytes.Buffer
	crlf := bytes.Contains(raw, []byte("\r\n"))
	writeLine := func(line string) {
		out.WriteString(line)
		if crlf {
			out.WriteString("\r\n")
		} else {
			out.WriteByte('\n')
		}
	}
	for _, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		if len(line) == 0 {
			continue
		}
		writeLine(string(line))
	}
	for _, line := range bytes.Split(data, []byte("\n")) {
		writeLine("data: " + string(line))
	}
	out.WriteByte('\n')
	return out.Bytes()
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

// parseSSEFrame returns the event name and joined data payload of one frame.
func parseSSEFrame(raw []byte) (sseFrame, bool) {
	var frame sseFrame
	hasData := false
	for _, line := range bytes.Split(raw, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		if len(line) == 0 || line[0] == ':' {
			continue
		}
		field, value, ok := bytes.Cut(line, []byte(":"))
		if !ok {
			continue
		}
		value = bytes.TrimPrefix(value, []byte(" "))
		switch string(field) {
		case "event":
			frame.event = string(value)
		case "data":
			if hasData {
				frame.data = append(frame.data, '\n')
			}
			frame.data = append(frame.data, value...)
			hasData = true
		}
	}
	return frame, hasData
}
