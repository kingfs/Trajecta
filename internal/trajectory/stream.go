package trajectory

import (
	"context"
	"encoding/json"
	"io"
)

// StreamOptions carries the session-level facts a streaming export knows before
// it reads any cassette. They are written into the header record and repeated in
// the final record so a consumer that only sees the tail remains self-describing.
type StreamOptions struct {
	// TraceCount is the total number of client-visible traces in the session.
	TraceCount int
	// IncludedTraces is how many traces this stream will actually visit.
	IncludedTraces int
	// Truncated reports that the caller capped the export below TraceCount.
	Truncated bool
	// TraceCap is the cap that produced IncludedTraces, or 0 for a full export.
	TraceCap int
}

// ExchangeSource yields the next recorded exchange in chronological order. It
// returns ok=false once the session is exhausted.
type ExchangeSource func() (Exchange, bool)

// NDJSON record types. Each line of a streaming export is exactly one of these,
// so a consumer can process the session without ever holding it in memory.
const (
	StreamRecordHeader  = "header"
	StreamRecordTrace   = "trace"
	StreamRecordMissing = "missing_tool_result"
	StreamRecordFinal   = "final"
)

// StreamHeader is the first NDJSON record.
type StreamHeader struct {
	Type           string `json:"type"`
	SchemaVersion  string `json:"schema_version"`
	SessionID      string `json:"session_id"`
	Agent          Agent  `json:"agent"`
	TraceCount     int    `json:"trace_count"`
	IncludedTraces int    `json:"included_traces"`
	Truncated      bool   `json:"truncated"`
	TraceCap       int    `json:"trace_cap,omitempty"`
}

// StreamTrace is one NDJSON record per consumed trace. Results holds tool
// results that were attached to steps from earlier traces; each carries the
// target step id so a consumer can merge it into the step stream.
type StreamTrace struct {
	Type     string         `json:"type"`
	TraceID  string         `json:"trace_id"`
	Time     string         `json:"time,omitempty"`
	Steps    []Step         `json:"steps"`
	Results  []StreamResult `json:"results,omitempty"`
	Warnings []Warning      `json:"warnings,omitempty"`
	Error    string         `json:"error,omitempty"`
}

// StreamResult is a tool result attached to an already emitted step.
type StreamResult struct {
	StepID       int            `json:"step_id"`
	SourceCallID string         `json:"source_call_id,omitempty"`
	Content      string         `json:"content"`
	Extra        map[string]any `json:"extra,omitempty"`
}

// StreamMissing reports a tool call that never received a recorded result. It is
// emitted after the last trace because only then is the gap known.
type StreamMissing struct {
	Type    string   `json:"type"`
	StepID  int      `json:"step_id"`
	TraceID string   `json:"trace_id,omitempty"`
	CallIDs []string `json:"call_ids"`
}

// StreamFinal is the last NDJSON record: the aggregate metrics and the same
// session-level extras the buffered response puts under `extra`.
type StreamFinal struct {
	Type         string         `json:"type"`
	Agent        Agent          `json:"agent"`
	FinalMetrics FinalMetrics   `json:"final_metrics"`
	Extra        map[string]any `json:"extra"`
}

// streamEmitter buffers exactly one trace at a time while the builder consumes
// it. Steps are copied out at the moment they are added, so a later
// compactFrom cannot change a record that has already been encoded.
type streamEmitter struct {
	traceID    string
	traceStart int
	steps      []Step
	results    []StreamResult
	warnings   []Warning
	missing    []StreamMissing
}

func (s *streamEmitter) beginTrace(traceID string, stepStart int) {
	s.traceID = traceID
	s.traceStart = stepStart
	s.steps = nil
	s.results = nil
	s.warnings = nil
}

func (s *streamEmitter) stepAdded(index int, step Step) {
	if index >= s.traceStart {
		s.steps = append(s.steps, step)
	}
}

func (s *streamEmitter) resultAdded(stepIndex int, result Result) {
	s.results = append(s.results, StreamResult{StepID: stepIndex + 1, SourceCallID: result.SourceCallID, Content: result.Content, Extra: result.Extra})
}

func (s *streamEmitter) warningAdded(w Warning) {
	s.warnings = append(s.warnings, w)
}

func (s *streamEmitter) record(ex Exchange) StreamTrace {
	steps := s.steps
	if steps == nil {
		steps = []Step{}
	}
	return StreamTrace{Type: StreamRecordTrace, TraceID: ex.TraceID, Time: timestamp(ex.Time), Steps: steps, Results: s.results, Warnings: s.warnings, Error: ex.Error}
}

// Stream writes an ATIF-v1.8 session trajectory as NDJSON: one JSON object per
// line, flushed as soon as the trace that produced it has been consumed. It
// reads cassettes through next one at a time, so peak memory is bounded by the
// largest single trace rather than the whole session, and it stops as soon as
// ctx is cancelled (a disconnected client).
func Stream(ctx context.Context, sessionID, sessionSource string, opts StreamOptions, next ExchangeSource, w io.Writer, flush func() error) error {
	encoder := json.NewEncoder(w)
	emit := func(record any) error {
		if err := encoder.Encode(record); err != nil {
			return err
		}
		if flush != nil {
			return flush()
		}
		return nil
	}

	emitter := &streamEmitter{}
	b := newBuilder(sessionID, sessionSource)
	b.stream = emitter

	if err := emit(StreamHeader{
		Type:           StreamRecordHeader,
		SchemaVersion:  SchemaVersion,
		SessionID:      sessionID,
		Agent:          b.trajectory.Agent,
		TraceCount:     opts.TraceCount,
		IncludedTraces: opts.IncludedTraces,
		Truncated:      opts.Truncated,
		TraceCap:       opts.TraceCap,
	}); err != nil {
		return err
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		ex, ok := next()
		if !ok {
			break
		}
		b.consume(ex)
		if err := emit(emitter.record(ex)); err != nil {
			return err
		}
		b.compactFrom(emitter.traceStart)
	}

	b.finish()
	if b.trajectory.Extra == nil {
		b.trajectory.Extra = map[string]any{}
	}
	b.trajectory.Extra["trace_count"] = opts.TraceCount
	b.trajectory.Extra["included_traces"] = opts.IncludedTraces
	b.trajectory.Extra["truncated"] = opts.Truncated
	if opts.TraceCap > 0 {
		b.trajectory.Extra["trace_cap"] = opts.TraceCap
	}
	for _, missing := range emitter.missing {
		if err := emit(missing); err != nil {
			return err
		}
	}
	return emit(StreamFinal{Type: StreamRecordFinal, Agent: b.trajectory.Agent, FinalMetrics: b.trajectory.FinalMetrics, Extra: b.trajectory.Extra})
}
