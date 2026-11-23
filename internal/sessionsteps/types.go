// Package sessionsteps builds the on-demand read model behind the trace detail
// page's trajectory (轨迹) and statistics (统计) views.
//
// Nothing this package produces is persisted. Raw .http cassettes stay the
// source of truth for content and the logs table stays the index for the
// numbers a list query needs; everything derived here - turn/step grouping,
// triggers, tool pairing, previews - is recomputed per view. A caller may hand
// in a throwaway cache, but the database is deliberately not a sink for any of
// it: the grouping rules will change, and a stored copy would then need a
// backfill for data that can always be recomputed.
package sessionsteps

import "time"

const (
	// BuilderVersion is bumped whenever the derived shape or the inference
	// rules change. It is part of the throwaway cache key, so a rule change
	// invalidates old entries without touching the database.
	//
	// Bump it when a *derived value* changes, not only the shape: a cached entry
	// survives a redeploy, so a rule that changes a number without changing the
	// schema would otherwise keep serving the old number until the window moved.
	//
	// 2: tool results are trimmed to the request tail (a version 1 entry held
	// the whole re-sent history and was megabytes per window).
	// 3: throughput is suppressed when the derived generation span is too small
	// to measure.
	BuilderVersion = 3

	// DefaultLimit is the number of steps one view returns. A long agent
	// session is hundreds of exchanges; returning all of them at once costs a
	// cassette read each and a large JSON body the page never renders.
	DefaultLimit = 100
	// MaxLimit bounds what a caller may ask for.
	MaxLimit = 500
	// DefaultBudget bounds how long the cassette pass may take. Steps that miss
	// it keep their numbers (which come from the database) and report no
	// content, which is the same degradation the trace list previews use.
	//
	// It is larger than the trace list's two seconds because this read is an
	// explicit action - a reader opened the tab and is waiting for it - while a
	// list preview only decorates rows that are already useful.
	DefaultBudget = 4 * time.Second
	// defaultWorkers bounds concurrent cassette reads inside one build.
	defaultWorkers = 4
	// maxPreviewRunes bounds how much of one text lands in the response. The
	// full text stays in the cassette and is served by the existing trace
	// endpoints when a reader opens a row.
	maxPreviewRunes       = 512
	maxAnswerPreviewRunes = 2048
	maxTriggerRunes       = 120
)

// Trigger kinds name where a turn's first step came from.
const (
	TriggerUser       = "user"
	TriggerToolResult = "tool_result"
	TriggerRetry      = "retry"
	TriggerAuto       = "auto"
	TriggerUnknown    = "unknown"
)

// Estimated markers explain which numbers are inferred rather than measured.
const (
	// EstimatedTurnBoundary marks a turn start inferred from the exchange
	// sequence: the proxy sees no agent events, so the boundary is a rule, not
	// a fact.
	EstimatedTurnBoundary = "turn_boundary"
	// EstimatedWindowPartial marks the first step of a window that starts in
	// the middle of a turn.
	EstimatedWindowPartial = "window_partial"
	// EstimatedContentPending marks a step whose cassette was not read (budget
	// or an unreadable file), so content and cache-write detail are absent.
	EstimatedContentPending = "content_pending"
	// EstimatedDuplicate marks a request whose conversation is byte-identical
	// to the previous one: a client retry rather than a new step of reasoning.
	EstimatedDuplicate = "duplicate_request"
)

// View is one window of a session's step model.
type View struct {
	SessionID      string    `json:"session_id,omitempty"`
	BuilderVersion int       `json:"builder_version"`
	GeneratedAt    time.Time `json:"generated_at"`
	FocusTraceID   string    `json:"focus_trace_id,omitempty"`
	Window         Window    `json:"window"`
	Totals         Totals    `json:"totals"`
	Turns          []Turn    `json:"turns"`
	Warnings       []Warning `json:"warnings,omitempty"`
}

// Window describes which slice of the session the turns cover.
type Window struct {
	Returned int `json:"returned"`
	// FirstStep and LastStep are the 1-based session-wide step numbers in the
	// window; 0 when the window is empty.
	FirstStep int `json:"first_step"`
	LastStep  int `json:"last_step"`
	// HasBefore and HasAfter tell the reader whether older or newer steps exist
	// outside the window; the UI pages back with ?before=<trace id>.
	HasBefore bool `json:"has_before"`
	HasAfter  bool `json:"has_after"`
	// ContentPending counts the steps whose cassette was not read.
	ContentPending int `json:"content_pending"`
	// BudgetMS is the cassette budget this view was built with.
	BudgetMS int `json:"budget_ms"`
}

// Totals aggregates the whole session, not just the window, because the
// numbers a viewer compares a step against are the session's.
type Totals struct {
	// Turns is only known when the window covers the whole session: counting
	// turns needs the step grouping, which needs the cassettes.
	Turns      *int     `json:"turns"`
	Steps      int      `json:"steps"`
	Input      int      `json:"input"`
	CacheRead  int      `json:"cache_read"`
	CacheWrite int      `json:"cache_write"`
	Context    int      `json:"context"`
	Output     int      `json:"output"`
	CacheHit   *float64 `json:"cache_hit"`
	// TTFTMs sums only the steps whose time to first byte was measured.
	TTFTMs  int64 `json:"ttft_ms"`
	ModelMs int64 `json:"model_ms"`
	// WallMs is the span from the session's first request to its last response.
	WallMs int64 `json:"wall_ms"`
}

// Turn is one user goal: everything the agent did before handing control back.
type Turn struct {
	Index     int       `json:"index"`
	StartedAt time.Time `json:"started_at"`
	EndedAt   time.Time `json:"ended_at"`
	WallMs    int64     `json:"wall_ms"`
	Trigger   Trigger   `json:"trigger"`
	Steps     []Step    `json:"steps"`
}

// Trigger is what opened a turn.
type Trigger struct {
	Kind  string `json:"kind"`
	Text  string `json:"text,omitempty"`
	Chars int    `json:"chars,omitempty"`
}

// Step is one client-visible model exchange.
type Step struct {
	// Index is 1-based within the turn; GlobalIndex is 1-based within the
	// session.
	Index       int       `json:"index"`
	GlobalIndex int       `json:"global_index"`
	TraceID     string    `json:"trace_id"`
	Model       string    `json:"model,omitempty"`
	Provider    string    `json:"provider,omitempty"`
	Status      int       `json:"status"`
	IsStream    bool      `json:"is_stream"`
	StartedAt   time.Time `json:"started_at"`
	EndedAt     time.Time `json:"ended_at"`
	Usage       Usage     `json:"usage"`
	Timing      Timing    `json:"timing"`
	Did         Did       `json:"did"`
	// Content is null when the cassette was not read; the numbers above stay
	// valid because they come from the index.
	Content   *Content `json:"content,omitempty"`
	Estimated []string `json:"estimated,omitempty"`
}

// Usage is the protocol-neutral token accounting for one step.
//
// The buckets are disjoint: Input excludes both cache buckets, so Context is
// their sum and a hit rate is a real ratio. Providers disagree about this -
// OpenAI-family prompt_tokens already includes the cached part while Anthropic
// reports the cache buckets separately - and Normalize is where the two shapes
// become one.
type Usage struct {
	Input      int      `json:"input"`
	CacheRead  int      `json:"cache_read"`
	CacheWrite int      `json:"cache_write"`
	Context    int      `json:"context"`
	Output     int      `json:"output"`
	CacheHit   *float64 `json:"cache_hit"`
}

// Timing is the per-step latency split. Pointers are nil when the value cannot
// be derived, which is distinct from a real zero.
type Timing struct {
	// TTFTMs is time to first byte. A non-streaming exchange has no separate
	// first byte, so it is nil rather than equal to the duration.
	TTFTMs *int64 `json:"ttft_ms"`
	// ModelMs is the upstream exchange duration.
	ModelMs int64 `json:"model_ms"`
	// DecodeMs is ModelMs minus TTFTMs: the generation span.
	DecodeMs *int64 `json:"decode_ms"`
	// TPS is Output divided by DecodeMs. Nil when either side is unusable.
	TPS *float64 `json:"tps"`
	// ToolWaitMs is the gap to the next step, and only set on a step that
	// asked for tools: the proxy cannot see the tool run, so this is the time
	// the client spent between the response and the next request.
	ToolWaitMs *int64 `json:"tool_wait_ms"`
	// WallMs is the whole step span, upstream duration included.
	WallMs int64 `json:"wall_ms"`
}

// Did summarizes what the step did, in the vocabulary of the statistics table.
type Did struct {
	Tools       []ToolCount `json:"tools,omitempty"`
	Calls       int         `json:"calls"`
	AnswerChars int         `json:"answer_chars"`
}

// ToolCount is one tool name and how often the step called it.
type ToolCount struct {
	Name  string `json:"name"`
	Calls int    `json:"calls"`
}

// Content holds the previews the ledger renders. Every string is truncated;
// the row links to the trace for the full payload.
type Content struct {
	ToolCalls    []ToolCallView   `json:"tool_calls,omitempty"`
	ToolResults  []ToolResultView `json:"tool_results,omitempty"`
	Answer       string           `json:"answer,omitempty"`
	AnswerChars  int              `json:"answer_chars"`
	RequestChars int              `json:"request_chars"`
}

// ToolCallView is one tool the model asked for.
type ToolCallView struct {
	ID       string `json:"id,omitempty"`
	Name     string `json:"name"`
	Args     string `json:"args,omitempty"`
	ArgsChar int    `json:"args_chars,omitempty"`
}

// ToolResultView is one tool result that came back in this step's request.
type ToolResultView struct {
	CallID  string `json:"call_id,omitempty"`
	Name    string `json:"name,omitempty"`
	Text    string `json:"text,omitempty"`
	Chars   int    `json:"chars"`
	IsError bool   `json:"is_error,omitempty"`
}

// Warning explains a view-wide degradation, so the page can say why it is
// showing less than a reader expects.
type Warning struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Count   int    `json:"count,omitempty"`
}

// Warning codes.
const (
	WarningContentPending = "content_pending"
	WarningUnreadable     = "cassette_unreadable"
	WarningNoSession      = "no_session"
)
