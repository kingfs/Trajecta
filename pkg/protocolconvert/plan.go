package protocolconvert

import "fmt"

// Capabilities is the set of wire protocols one upstream (for one model)
// serves natively. It is the input to Plan and the output of the
// upstream-side derivation in internal/upstream, which builds it from the
// channel api_type, the channel capabilities and the per-model supports_*
// overrides (the values POST /api/provider-model-detect persists).
type Capabilities struct {
	ChatCompletions bool
	Responses       bool
	Messages        bool
	Gemini          bool
}

// Has reports whether the upstream serves p natively.
func (c Capabilities) Has(p Protocol) bool {
	switch canonical(p) {
	case ProtocolChatCompletions:
		return c.ChatCompletions
	case ProtocolResponses:
		return c.Responses
	case ProtocolMessages:
		return c.Messages
	case ProtocolGemini:
		return c.Gemini
	}
	return false
}

// Protocols returns the served protocols in conversion-preference order.
func (c Capabilities) Protocols() []Protocol {
	var out []Protocol
	for _, p := range AllProtocols {
		if c.Has(p) {
			out = append(out, p)
		}
	}
	return out
}

// Mode selects how Plan treats a client/upstream protocol mismatch.
type Mode string

const (
	// ModePassthroughPreferred is the default: native passthrough wins, and
	// conversion is only the fallback when the upstream cannot serve the
	// client's protocol at all.
	ModePassthroughPreferred Mode = "passthrough_preferred"
	// ModePassthroughOnly never converts: a mismatch is a rejection.
	ModePassthroughOnly Mode = "passthrough_only"
)

// Action is the Plan outcome.
type Action string

const (
	ActionPassthrough Action = "passthrough"
	ActionConvert     Action = "convert"
	ActionReject      Action = "reject"
)

// Decision is the Plan outcome for one (client protocol, upstream
// capabilities, mode) triple. UpstreamProtocol is the protocol the request
// must be sent in: equal to ClientProtocol for passthrough, the best
// convertible protocol for convert, empty for reject.
type Decision struct {
	Action           Action
	ClientProtocol   Protocol
	UpstreamProtocol Protocol
	Reason           string
}

func (d Decision) Passthrough() bool { return d.Action == ActionPassthrough }
func (d Decision) Convert() bool     { return d.Action == ActionConvert }
func (d Decision) Reject() bool      { return d.Action == ActionReject }

// PlanRequest is the full input to PlanForRequest: the same triple as Plan
// plus whether the client asked for a stream.
type PlanRequest struct {
	Client Protocol
	Caps   Capabilities
	Mode   Mode
	// Stream rejects conversion. Streaming responses are converted by
	// aggregating the whole stream and re-encoding it (see
	// ConvertStreamResponse), which destroys incremental delivery and
	// bounds neither memory nor latency; the correct answer for a streaming
	// client on a mismatched upstream is a selection failure, not a
	// silently buffered stream. This is a correctness guardrail, not a
	// missing feature: passthrough streaming is unaffected.
	Stream bool
}

// PlanForRequest is Plan with the streaming guardrail applied.
func PlanForRequest(req PlanRequest) (Decision, error) {
	decision, err := Plan(req.Client, req.Caps, req.Mode)
	if err != nil || !req.Stream || !decision.Convert() {
		return decision, err
	}
	decision.Action = ActionReject
	decision.UpstreamProtocol = ""
	decision.Reason = fmt.Sprintf("upstream does not serve %s and streaming conversion is not supported", decision.ClientProtocol)
	return decision, nil
}

// Plan decides, for one request, whether to pass it through byte for byte,
// convert it to a protocol the upstream serves, or reject it.
//
// Passthrough is always preferred: if the upstream serves the client's
// protocol natively the answer is passthrough under both modes. Only when
// the upstream cannot serve the client's protocol does Plan consider
// conversion, picking the first served protocol in AllProtocols order
// (chat_completions, responses, messages, gemini). Under
// ModePassthroughOnly a mismatch rejects instead, which preserves the
// pre-conversion behaviour exactly.
func Plan(client Protocol, caps Capabilities, mode Mode) (Decision, error) {
	if !client.Valid() {
		return Decision{}, fmt.Errorf("protocolconvert: invalid client protocol %q", client)
	}
	client = canonical(client)
	if caps.Has(client) {
		return Decision{
			Action:           ActionPassthrough,
			ClientProtocol:   client,
			UpstreamProtocol: client,
			Reason:           "upstream serves the client protocol natively",
		}, nil
	}
	if mode == ModePassthroughOnly {
		return Decision{
			Action:         ActionReject,
			ClientProtocol: client,
			Reason:         fmt.Sprintf("upstream does not serve %s and conversion is disabled", client),
		}, nil
	}
	for _, candidate := range caps.Protocols() {
		return Decision{
			Action:           ActionConvert,
			ClientProtocol:   client,
			UpstreamProtocol: candidate,
			Reason:           fmt.Sprintf("upstream does not serve %s; converting to %s", client, candidate),
		}, nil
	}
	return Decision{
		Action:         ActionReject,
		ClientProtocol: client,
		Reason:         fmt.Sprintf("upstream serves no convertible protocol for %s", client),
	}, nil
}
