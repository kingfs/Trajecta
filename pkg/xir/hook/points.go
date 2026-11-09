package hook

// Lane selects the execution path a hook participates in.
//
// The two lanes have different budgets and failure expectations: the hot lane
// runs inside the forwarding path and must stay cheap, while the audit lane
// runs after the fact and may do slow detection.
type Lane uint8

const (
	// LaneHot is the forwarding path.
	LaneHot Lane = 1 << iota
	// LaneAudit is the asynchronous analysis path.
	LaneAudit
)

// laneMask is every lane bit this package knows about.
const laneMask = LaneHot | LaneAudit

// Has reports whether l contains any bit of other.
func (l Lane) Has(other Lane) bool { return l&other != 0 }

// Valid reports whether l is a non-empty set of known lane bits. A zero Lane
// is not a valid plan lane; Compile rejects it.
func (l Lane) Valid() bool { return l != 0 && l&^laneMask == 0 }

// Point names one place in an exchange where hooks may observe or rewrite.
//
// A Point is deliberately a string rather than an enum integer: it is stable
// configuration data and can appear in findings, logs and stored hook configs.
type Point string

const (
	PointRequestEnvelope   Point = "request.envelope"
	PointRequestSystem     Point = "request.system"
	PointRequestMessage    Point = "request.message"
	PointRequestToolDecl   Point = "request.tool_declaration"
	PointRequestToolCall   Point = "request.tool_call.arguments"
	PointRequestToolResult Point = "request.tool_result"
	PointRequestMetadata   Point = "request.metadata"

	PointResponseOutput     Point = "response.output"
	PointResponseReasoning  Point = "response.reasoning"
	PointResponseToolCall   Point = "response.tool_call.arguments"
	PointResponseToolResult Point = "response.tool_result"
	PointResponseUsage      Point = "response.usage"
	PointResponseError      Point = "response.error"

	PointStreamEvent       Point = "stream.event"
	PointStreamTextDelta   Point = "stream.delta.text"
	PointStreamReasonDelta Point = "stream.delta.reasoning"
	PointStreamToolArgs    Point = "stream.delta.tool_args"

	PointExchangeFinalize Point = "exchange.finalize"
)

// Readable aliases for constant names that spell out the full concept.
const (
	PointRequestToolDeclaration    = PointRequestToolDecl
	PointRequestToolCallArguments  = PointRequestToolCall
	PointResponseToolCallArguments = PointResponseToolCall
	PointStreamReasoningDelta      = PointStreamReasonDelta
)

// allPoints is the canonical point order. AllPoints returns a copy, so callers
// cannot corrupt it.
var allPoints = [...]Point{
	PointRequestEnvelope,
	PointRequestSystem,
	PointRequestMessage,
	PointRequestToolDecl,
	PointRequestToolCall,
	PointRequestToolResult,
	PointRequestMetadata,

	PointResponseOutput,
	PointResponseReasoning,
	PointResponseToolCall,
	PointResponseToolResult,
	PointResponseUsage,
	PointResponseError,

	PointStreamEvent,
	PointStreamTextDelta,
	PointStreamReasonDelta,
	PointStreamToolArgs,

	PointExchangeFinalize,
}

// AllPoints returns every defined point in a stable order. The returned slice
// is a fresh copy.
func AllPoints() []Point {
	out := make([]Point, len(allPoints))
	copy(out, allPoints[:])
	return out
}

// Valid reports whether p is one of the defined points.
func (p Point) Valid() bool {
	for _, known := range allPoints {
		if p == known {
			return true
		}
	}
	return false
}

// Needs describes which representations of an exchange a hook consumes. The
// gateway uses it to avoid decoding a semantic view or a stream when no
// matching hook asked for it.
type Needs uint8

const (
	// NeedsWire means the hook reads or edits the lossless wire view.
	NeedsWire Needs = 1 << iota
	// NeedsSemantic means the hook reads the typed Request/Response view.
	NeedsSemantic
	// NeedsStream means the hook consumes streamed events/deltas.
	NeedsStream
)

// needsMask is every needs bit this package knows about.
const needsMask = NeedsWire | NeedsSemantic | NeedsStream

// Has reports whether n contains any bit of other.
func (n Needs) Has(other Needs) bool { return n&other != 0 }

// Valid reports whether n contains only known bits. The zero Needs is valid:
// a hook may work from the event metadata alone.
func (n Needs) Valid() bool { return n&^needsMask == 0 }

// FailPolicy says what a Plan does when a hook returns an error or panics.
type FailPolicy uint8

const (
	// FailOpen skips the failed hook, records a diagnostic finding and keeps
	// running the remaining hooks. It is the zero value and suits observation
	// hooks whose failure must not break the exchange.
	FailOpen FailPolicy = iota
	// FailClosed aborts the plan and returns an error naming the hook. It suits
	// masking, policy and credential hooks whose failure must not be ignored.
	FailClosed
)

// Severity is a finding's importance. The values match pkg/observe so a hook
// finding can be projected into the existing audit model without translation.
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)
