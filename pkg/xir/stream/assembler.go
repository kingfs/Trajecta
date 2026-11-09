package stream

import (
	"encoding/json"

	"github.com/kingfs/Trajecta/pkg/usage"
	"github.com/kingfs/Trajecta/pkg/xir"
)

// Assembler folds normalized xir.StreamEvent values into one xir.Response.
//
// It is the streaming counterpart of a codec's DecodeResponse: it understands
// the semantics of the normalized event kinds but not any wire protocol, and
// it never re-encodes, rewrites or converts a response. An Assembler is
// stateful and is not safe for concurrent use.
//
// Text and reasoning deltas are coalesced into the most recent part of the
// same kind. Tool argument deltas are concatenated onto the most recent tool
// call (or onto a new call when the delta names a different ID). Usage
// snapshots are merged with pkg/usage.Merge, so repeated or zero-valued
// counters never double-count. Ping and unknown events are ignored; sealed
// blocks are preserved as xir.PartSealed so a same-protocol encoder can emit
// them unchanged.
type Assembler struct {
	protocol xir.Protocol
	response xir.Response

	// currentTool indexes response.Parts while a tool call is open. -1 means
	// no call is currently accepting argument deltas.
	currentTool int
}

// NewAssembler returns an Assembler for events decoded from protocol. The
// protocol is used only to stamp a preserved sealed block that does not name
// its own protocol; it does not select any conversion behaviour.
func NewAssembler(protocol xir.Protocol) *Assembler {
	return &Assembler{protocol: protocol, currentTool: -1}
}

// SetIdentity sets the response ID and model. It exists because
// xir.StreamEvent has no dedicated identity fields: a decoder that learns the
// response id/model from a start frame can call SetIdentity, or (by convention
// for a StreamStart event only) put the ID in ToolID and the model in
// ToolName. Empty arguments leave the current value unchanged.
func (a *Assembler) SetIdentity(id, model string) {
	if a == nil {
		return
	}
	if id != "" {
		a.response.ID = id
	}
	if model != "" {
		a.response.Model = model
	}
}

// Apply consumes one event. A nil event is ignored, and a nil Assembler is a
// no-op, so callers may apply a decoder's optional output without guarding.
//
// Apply currently never returns a non-nil error; the error result is reserved
// for future validation (for example, rejecting an event that violates a
// protocol invariant). Callers should still check it so that addition is not
// a breaking change.
func (a *Assembler) Apply(ev *xir.StreamEvent) error {
	if a == nil || ev == nil {
		return nil
	}
	switch ev.Kind {
	case xir.StreamStart:
		a.applyStart(ev)
	case xir.StreamText:
		if ev.Text != "" {
			a.appendText(ev.Text)
		}
	case xir.StreamReasoning:
		a.applyReasoning(ev)
	case xir.StreamToolStart:
		a.applyToolStart(ev)
	case xir.StreamToolArgs:
		a.applyToolArgs(ev)
	case xir.StreamToolEnd:
		a.applyToolEnd(ev)
	case xir.StreamUsage:
		a.applyUsage(ev.Usage)
	case xir.StreamStop:
		a.applyStop(ev)
	case xir.StreamError:
		a.applyError(ev)
	case xir.StreamSealed:
		a.applySealed(ev)
	default:
		// StreamPing, StreamUnknown and an empty kind carry no response
		// content. They are ignored rather than materialized as parts.
	}
	return nil
}

// Response returns the response assembled so far. It is never nil (for a
// nil-receiver safety case it returns an empty response) and is valid before
// any event, after a stop/error event, or mid-stream. The returned pointer is
// the assembler's live value: it observes later Apply calls, and callers must
// not mutate it.
func (a *Assembler) Response() *xir.Response {
	if a == nil {
		return &xir.Response{}
	}
	return &a.response
}

func (a *Assembler) applyStart(ev *xir.StreamEvent) {
	if a.response.Status == "" {
		a.response.Status = "in_progress"
	}
	// xir.StreamEvent has no ID/Model fields. For a start event the tool
	// identity slots are otherwise unused, so they are the transport for the
	// response identity; SetIdentity is the explicit alternative. Text is a
	// last-resort model carrier for decoders that only normalize a raw model
	// string.
	if ev.ToolID != "" {
		a.response.ID = ev.ToolID
	}
	if ev.ToolName != "" {
		a.response.Model = ev.ToolName
	}
	if ev.Text != "" && a.response.Model == "" {
		a.response.Model = ev.Text
	}
}

func (a *Assembler) appendText(text string) {
	if n := len(a.response.Parts); n > 0 {
		if last := &a.response.Parts[n-1]; last.Kind == xir.PartText {
			last.Text += text
			return
		}
	}
	a.response.Parts = append(a.response.Parts, xir.Part{Kind: xir.PartText, Text: text})
}

func (a *Assembler) applyReasoning(ev *xir.StreamEvent) {
	r := ev.Reasoning
	if r == nil && ev.Text == "" {
		return
	}

	part := a.reasoningPart()
	reasoning := part.Reasoning

	delta := ev.Text
	if r != nil && r.Text != "" {
		delta = r.Text
	}
	reasoning.Text += delta

	if r == nil {
		return
	}
	// A signature or encrypted payload is carried whole, not as a delta; keep
	// the latest non-empty value. Summary items arrive as separate entries.
	if r.Signature != "" {
		reasoning.Signature = r.Signature
	}
	if r.EncryptedContent != "" {
		reasoning.EncryptedContent = r.EncryptedContent
	}
	if len(r.Summary) > 0 {
		reasoning.Summary = append(reasoning.Summary, r.Summary...)
	}
	if r.Sealed != nil {
		reasoning.Sealed = cloneSealed(r.Sealed, ev.Protocol, a.protocol)
	}
}

func (a *Assembler) reasoningPart() *xir.Part {
	if n := len(a.response.Parts); n > 0 {
		if last := &a.response.Parts[n-1]; last.Kind == xir.PartReasoning {
			if last.Reasoning == nil {
				last.Reasoning = &xir.Reasoning{}
			}
			return last
		}
	}
	a.response.Parts = append(a.response.Parts, xir.Part{
		Kind:      xir.PartReasoning,
		Reasoning: &xir.Reasoning{},
	})
	return &a.response.Parts[len(a.response.Parts)-1]
}

func (a *Assembler) applyToolStart(ev *xir.StreamEvent) {
	call := &xir.ToolCall{
		ID:   ev.ToolID,
		Name: ev.ToolName,
		// StreamEvent does not model a call type. Function calls are the only
		// generic streaming shape; a protocol-native call type has no way to
		// travel through this event and therefore cannot be assembled.
		Type: "function",
	}
	if ev.Text != "" {
		call.ArgsText = ev.Text
		setToolArgs(call)
	}
	a.response.Parts = append(a.response.Parts, xir.Part{Kind: xir.PartToolCall, ToolCall: call})
	a.currentTool = len(a.response.Parts) - 1
}

func (a *Assembler) applyToolArgs(ev *xir.StreamEvent) {
	call := a.currentToolCall(ev)
	if call == nil {
		call = &xir.ToolCall{Type: "function"}
		a.response.Parts = append(a.response.Parts, xir.Part{Kind: xir.PartToolCall, ToolCall: call})
		a.currentTool = len(a.response.Parts) - 1
	}
	if call.ID == "" {
		call.ID = ev.ToolID
	}
	if call.Name == "" {
		call.Name = ev.ToolName
	}
	if ev.Text == "" {
		return
	}
	// Argument deltas are byte fragments of one JSON document and are
	// concatenated, never parsed and re-marshalled.
	call.ArgsText += ev.Text
	setToolArgs(call)
}

func (a *Assembler) applyToolEnd(ev *xir.StreamEvent) {
	call := a.currentToolCall(ev)
	if call == nil {
		if ev.ToolID == "" && ev.ToolName == "" && ev.Text == "" {
			return
		}
		call = &xir.ToolCall{Type: "function"}
		a.response.Parts = append(a.response.Parts, xir.Part{Kind: xir.PartToolCall, ToolCall: call})
		a.currentTool = len(a.response.Parts) - 1
	}
	if call.ID == "" {
		call.ID = ev.ToolID
	}
	if call.Name == "" {
		call.Name = ev.ToolName
	}
	// A protocol that carries the last fragment on the end event keeps it.
	if ev.Text != "" {
		call.ArgsText += ev.Text
	}
	setToolArgs(call)
	// The call is closed. A later argument delta without its own start opens a
	// new call rather than appending to this one.
	a.currentTool = -1
}

// currentToolCall returns the open call a delta belongs to, or nil when the
// delta names a different call and a new part must be started.
func (a *Assembler) currentToolCall(ev *xir.StreamEvent) *xir.ToolCall {
	if a.currentTool < 0 || a.currentTool >= len(a.response.Parts) {
		return nil
	}
	part := &a.response.Parts[a.currentTool]
	if part.Kind != xir.PartToolCall || part.ToolCall == nil {
		return nil
	}
	call := part.ToolCall
	if ev.ToolID != "" && call.ID != "" && call.ID != ev.ToolID {
		return nil
	}
	return call
}

// setToolArgs mirrors the codec rule: ArgsText is the authoritative payload,
// and Args is a copy of it only when the accumulated text is valid JSON.
func setToolArgs(call *xir.ToolCall) {
	if call == nil {
		return
	}
	if call.ArgsText == "" || !json.Valid([]byte(call.ArgsText)) {
		call.Args = nil
		return
	}
	call.Args = json.RawMessage(call.ArgsText)
}

func (a *Assembler) applyUsage(u *usage.Usage) {
	if u == nil {
		return
	}
	var current usage.Usage
	if a.response.Usage != nil {
		current = *a.response.Usage
	}
	merged := usage.Merge(current, *u)
	a.response.Usage = &merged
}

func (a *Assembler) applyStop(ev *xir.StreamEvent) {
	// An error event's FinishError is terminal: a later stop frame must not
	// turn a failed response into a successful one.
	if ev.StopReason != "" && a.response.StopReason != xir.FinishError {
		a.response.StopReason = ev.StopReason
	}
	if a.response.StopReason == "" {
		a.response.StopReason = xir.FinishStop
	}
	if a.response.Status == "" || a.response.Status == "in_progress" {
		if a.response.StopReason == xir.FinishError {
			a.response.Status = "failed"
		} else {
			a.response.Status = "completed"
		}
	}
}

func (a *Assembler) applyError(ev *xir.StreamEvent) {
	a.response.Status = "failed"
	a.response.StopReason = xir.FinishError
	switch {
	case ev.Error != nil:
		errCopy := *ev.Error
		if errCopy.Raw != nil {
			errCopy.Raw = append(json.RawMessage(nil), errCopy.Raw...)
		}
		a.response.Error = &errCopy
	case ev.Text != "":
		a.response.Error = &xir.Error{Message: ev.Text}
	}
}

func (a *Assembler) applySealed(ev *xir.StreamEvent) {
	sealed := ev.Sealed
	if sealed == nil && ev.Reasoning != nil {
		sealed = ev.Reasoning.Sealed
	}
	if sealed == nil {
		return
	}
	a.response.Parts = append(a.response.Parts, xir.Part{
		Kind:   xir.PartSealed,
		Sealed: cloneSealed(sealed, ev.Protocol, a.protocol),
	})
}

// cloneSealed copies a sealed block and fills its protocol from the event or
// the assembler when the producer left it empty.
func cloneSealed(sealed *xir.Sealed, eventProtocol, assemblerProtocol xir.Protocol) *xir.Sealed {
	if sealed == nil {
		return nil
	}
	out := *sealed
	if out.Protocol == "" {
		out.Protocol = eventProtocol
		if out.Protocol == "" {
			out.Protocol = assemblerProtocol
		}
	}
	if out.Raw != nil {
		out.Raw = append(json.RawMessage(nil), out.Raw...)
	}
	return &out
}
