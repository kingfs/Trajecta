package sessionsteps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/observe"
)

// Cache is the throwaway derived-data cache a caller may pass in. Its contents
// are never authoritative: deleting them costs one rebuild, and the key
// carries BuilderVersion so a rule change cannot serve a stale shape.
//
// internal/trajectory.Cache already satisfies this interface; the read model
// does not own a storage mechanism of its own.
type Cache interface {
	Load(key string) ([]byte, bool)
	Store(key string, data []byte) error
}

// Options bounds one build.
type Options struct {
	// Limit is the number of steps to return; zero means DefaultLimit.
	Limit int
	// BeforeTraceID pages backwards: the window ends just before this trace.
	BeforeTraceID string
	// FocusTraceID ends the window at this trace, so opening a trace deep in a
	// session shows the steps that led to it.
	FocusTraceID string
	// Budget bounds the cassette pass; zero means DefaultBudget.
	Budget time.Duration
	// Workers bounds concurrent cassette reads; zero means defaultWorkers.
	Workers int
	// Registry selects the observation parsers; nil means the default set.
	Registry *observe.Registry
	// Cache, when non-nil, stores the assembled view by window.
	Cache Cache
	// Now is the clock, injectable for tests.
	Now func() time.Time
}

// Build assembles one window of the session's step model.
//
// The numeric half comes from the indexed rows alone, so it is always
// available and never depends on a cassette being readable. The content half -
// turn/step grouping and the ledger previews - needs the cassettes, and is
// bounded by Budget: a step that misses the budget keeps its numbers and
// reports no content.
func Build(ctx context.Context, sessionID string, entries []store.LogEntry, opts Options) (View, error) {
	opts = normalizeOptions(opts)
	ordered := orderEntries(entries)
	view := View{
		SessionID:      sessionID,
		BuilderVersion: BuilderVersion,
		GeneratedAt:    opts.Now().UTC(),
		FocusTraceID:   strings.TrimSpace(opts.FocusTraceID),
	}
	if len(ordered) == 0 {
		view.Warnings = append(view.Warnings, Warning{Code: WarningNoSession, Message: "the session has no recorded requests"})
		view.Totals.Turns = intPtr(0)
		return view, nil
	}
	key := cacheKey(sessionID, ordered, opts)
	if opts.Cache != nil && key != "" {
		if data, ok := opts.Cache.Load(key); ok {
			var cached View
			if err := json.Unmarshal(data, &cached); err == nil && cached.BuilderVersion == BuilderVersion {
				return cached, nil
			}
		}
	}

	view.Totals = totalsFromEntries(ordered)
	windowStart, windowEnd := selectWindow(ordered, opts)
	view.Window = Window{
		Returned:  windowEnd - windowStart,
		BudgetMS:  int(opts.Budget / time.Millisecond),
		HasBefore: windowStart > 0,
		HasAfter:  windowEnd < len(ordered),
	}
	if view.Window.Returned > 0 {
		view.Window.FirstStep = windowStart + 1
		view.Window.LastStep = windowEnd
	}

	items := make([]stepInput, 0, view.Window.Returned)
	for _, entry := range ordered[windowStart:windowEnd] {
		items = append(items, stepInput{entry: entry})
	}
	extractAll(ctx, items, opts)
	unreadable := 0
	for index := range items {
		if !items[index].facts.parsed {
			view.Window.ContentPending++
			if items[index].facts.readFailed {
				unreadable++
			}
		}
	}
	if view.Window.ContentPending > 0 {
		view.Warnings = append(view.Warnings, Warning{
			Code:    WarningContentPending,
			Message: "some steps were not parsed within the cassette budget, so their content is missing",
			Count:   view.Window.ContentPending,
		})
	}
	if unreadable > 0 {
		view.Warnings = append(view.Warnings, Warning{
			Code:    WarningUnreadable,
			Message: "some recorded cassettes could not be read; those steps show numbers only",
			Count:   unreadable,
		})
	}

	view.Turns = buildTurns(items, windowStart)
	if !view.Window.HasBefore && !view.Window.HasAfter {
		view.Totals.Turns = intPtr(len(view.Turns))
	}
	if opts.Cache != nil && key != "" {
		if data, err := json.Marshal(view); err == nil {
			_ = opts.Cache.Store(key, data)
		}
	}
	return view, nil
}

type stepInput struct {
	entry store.LogEntry
	facts facts
}

// extractAll reads the window's cassettes with a small worker pool and a shared
// deadline. The order of items is preserved because the turn inference walks
// them in sequence; only the reads are concurrent.
func extractAll(ctx context.Context, items []stepInput, opts Options) {
	if len(items) == 0 {
		return
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Budget)
		defer cancel()
	}
	workers := opts.Workers
	if workers > len(items) {
		workers = len(items)
	}
	if workers < 1 {
		workers = 1
	}
	jobs := make(chan int)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				if ctx.Err() != nil {
					return
				}
				items[index].facts = readFacts(ctx, opts.Registry, items[index].entry)
			}
		}()
	}
dispatch:
	for index := range items {
		select {
		case <-ctx.Done():
			break dispatch
		case jobs <- index:
		}
	}
	close(jobs)
	wg.Wait()
}

// buildTurns groups the window's steps. The proxy sees HTTP exchanges, not
// agent events, so the boundary is a rule with one strong signal: a step that
// asked for tools hands control to the client, and the turn continues only
// when the next request carries tool results back.
//
// The rules, in order:
//
//  1. the first step of the window opens a turn;
//  2. a step after a response without tool calls opens a turn, because the
//     agent had handed control back to whoever sent the next request;
//  3. a step whose request carries tool results continues the turn;
//  4. a step that changed the user message instead of returning tool results
//     opens a turn: that is steering, not a tool continuation;
//  5. anything else (an identical or truncated history) continues the turn;
//  6. a step whose cassette was not read continues the turn, because there is
//     no evidence for a boundary and inventing one is worse than merging them.
func buildTurns(items []stepInput, windowStart int) []Turn {
	turns := make([]Turn, 0, 4)
	previousHadToolCalls := false
	previousSigs := []messageSig(nil)
	previousLastUser := [16]byte{}
	for index := range items {
		item := &items[index]
		sigs := item.facts.messages
		lastUser, hasLastUser := lastUserSig(sigs)
		newTurn := false
		duplicate := previousSigs != nil && len(sigs) > 0 && sameSignatures(sigs, previousSigs)
		switch {
		case len(turns) == 0:
			newTurn = true
		case !item.facts.parsed:
			newTurn = false
		case duplicate:
			// A byte-identical resend is a retry of the previous step, not a
			// new goal, even when the previous response asked for no tools.
			newTurn = false
		case !previousHadToolCalls:
			newTurn = true
		case item.facts.hasToolResult:
			newTurn = false
		case hasLastUser && lastUser != previousLastUser:
			newTurn = true
		default:
			newTurn = false
		}

		step := buildStep(item, index, windowStart, sigs, previousSigs)
		if newTurn {
			// The first step of a window that starts mid-session has no
			// predecessor to diff against, so its trigger is unknown rather than
			// borrowed from whatever user message the window happens to contain.
			truncated := windowStart > 0 && index == 0
			step.Index = 1
			turns = append(turns, Turn{
				Index:     len(turns) + 1,
				StartedAt: step.StartedAt,
				EndedAt:   step.EndedAt,
				Trigger:   triggerFor(item, hasLastUser, lastUser, previousLastUser, previousSigs != nil && !truncated, truncated),
				Steps:     []Step{step},
			})
		} else {
			last := &turns[len(turns)-1]
			step.Index = len(last.Steps) + 1
			last.Steps = append(last.Steps, step)
			last.EndedAt = step.EndedAt
		}
		previousHadToolCalls = len(item.facts.toolCalls) > 0
		previousSigs = sigs
		if hasLastUser {
			previousLastUser = lastUser
		}
	}
	for index := range turns {
		turn := &turns[index]
		turn.WallMs = turn.EndedAt.Sub(turn.StartedAt).Milliseconds()
		if turn.WallMs < 0 {
			turn.WallMs = 0
		}
		if len(turn.Steps) > 0 {
			turn.Steps[0].Estimated = appendUnique(turn.Steps[0].Estimated, EstimatedTurnBoundary)
		}
	}
	// A window that starts mid-turn cannot know the turn's real first step.
	if len(turns) > 0 && windowStart > 0 {
		turns[0].Steps[0].Estimated = appendUnique(turns[0].Steps[0].Estimated, EstimatedWindowPartial)
	}
	applyToolWaits(turns)
	return turns
}

// applyToolWaits measures the client-side segment: a step that asked for tools
// is followed by a gap the proxy cannot see into, because the tool runs
// outside it. The gap is attributed to the step that issued the call; the last
// step of a window has no successor to subtract, so it keeps nil rather than
// pretending the wait was zero.
func applyToolWaits(turns []Turn) {
	steps := make([]*Step, 0, 8)
	for turnIndex := range turns {
		for stepIndex := range turns[turnIndex].Steps {
			steps = append(steps, &turns[turnIndex].Steps[stepIndex])
		}
	}
	for index := 0; index+1 < len(steps); index++ {
		step := steps[index]
		if step.Did.Calls == 0 {
			continue
		}
		wait := steps[index+1].StartedAt.Sub(step.EndedAt).Milliseconds()
		if wait < 0 {
			wait = 0
		}
		step.Timing.ToolWaitMs = &wait
	}
}

// buildStep turns one cassette's facts plus the index row into a step.
func buildStep(item *stepInput, index, windowStart int, sigs, previousSigs []messageSig) Step {
	meta := item.entry.Header.Meta
	duration := meta.DurationMs
	if duration < 0 {
		duration = 0
	}
	start := meta.Time
	end := start.Add(time.Duration(duration) * time.Millisecond)
	step := Step{
		GlobalIndex: windowStart + index + 1,
		TraceID:     item.entry.ID,
		Model:       meta.Model,
		Provider:    meta.Provider,
		Status:      meta.StatusCode,
		IsStream:    item.entry.Header.Layout.IsStream,
		StartedAt:   start,
		EndedAt:     end,
		Did:         Did{Tools: []ToolCount{}},
	}
	if item.facts.parsed {
		step.Usage = usageFromObservation(meta.Provider, meta.Endpoint, item.facts.usage)
		step.Content = &Content{
			ToolCalls:    item.facts.toolCalls,
			ToolResults:  addedToolResults(item.facts.toolResults, previousSigs, sigs),
			Answer:       item.facts.answer,
			AnswerChars:  item.facts.answerChars,
			RequestChars: item.facts.requestChars,
		}
		step.Did.AnswerChars = item.facts.answerChars
		step.Did.Tools = item.facts.tools
		if step.Did.Tools == nil {
			step.Did.Tools = []ToolCount{}
		}
		step.Did.Calls = len(item.facts.toolCalls)
	} else {
		usage := item.entry.Header.Usage
		cached := 0
		if usage.PromptTokenDetails != nil {
			cached = usage.PromptTokenDetails.CachedTokens
		}
		step.Usage = usageFromHeader(meta.Provider, meta.Endpoint, usage.PromptTokens, usage.CompletionTokens, cached)
		step.Estimated = appendUnique(step.Estimated, EstimatedContentPending)
	}

	// Time to first byte is only a first-token proxy for a stream. A
	// non-streaming exchange writes its body once, so the recorded value is
	// the whole duration and says nothing.
	if step.IsStream && meta.TTFTMs > 0 && meta.TTFTMs <= duration {
		ttft := meta.TTFTMs
		step.Timing.TTFTMs = &ttft
		decode := duration - meta.TTFTMs
		step.Timing.DecodeMs = &decode
		step.Timing.TPS = tokensPerSecond(step.Usage.Output, step.Timing.DecodeMs, duration)
	}
	step.Timing.ModelMs = duration
	step.Timing.WallMs = duration

	// A byte-identical resend is a client retry, not a new step of reasoning.
	if previousSigs != nil && len(sigs) > 0 && sameSignatures(sigs, previousSigs) {
		step.Estimated = appendUnique(step.Estimated, EstimatedDuplicate)
	}
	return step
}

// addedToolResults keeps only the tool results this request appended.
//
// Every request of an agent loop re-sends the whole conversation, so a step's
// request contains all the earlier results too. Keeping the tail - everything
// after the message prefix shared with the previous step - is what makes one
// window cost kilobytes instead of megabytes, and it is also the only set that
// means "the tool returned in this step". The first step of a window has no
// predecessor to diff against, so it reports no results: their calls are
// outside the window and nothing could be attached to them anyway.
func addedToolResults(results []toolResultFact, previousSigs, sigs []messageSig) []ToolResultView {
	if previousSigs == nil {
		return nil
	}
	common := commonPrefix(previousSigs, sigs)
	out := make([]ToolResultView, 0, len(results))
	for _, result := range results {
		if result.messageIndex < common {
			continue
		}
		out = append(out, result.view)
	}
	return out
}

// commonPrefix returns how many leading messages two requests share.
func commonPrefix(left, right []messageSig) int {
	limit := len(left)
	if len(right) < limit {
		limit = len(right)
	}
	for index := 0; index < limit; index++ {
		if left[index] != right[index] {
			return index
		}
	}
	return limit
}

// triggerFor names what opened a turn.
func triggerFor(item *stepInput, hasLastUser bool, lastUser, previousLastUser [16]byte, hasPrevious, truncated bool) Trigger {
	if !item.facts.parsed || truncated {
		return Trigger{Kind: TriggerUnknown}
	}
	if hasLastUser && (!hasPrevious || lastUser != previousLastUser) {
		return Trigger{Kind: TriggerUser, Text: item.facts.lastUserText, Chars: len(item.facts.lastUserText)}
	}
	if item.facts.hasToolResult {
		return Trigger{Kind: TriggerToolResult}
	}
	return Trigger{Kind: TriggerAuto}
}

// selectWindow returns the half-open [start,end) range of entries to render.
func selectWindow(entries []store.LogEntry, opts Options) (int, int) {
	end := len(entries)
	if before := strings.TrimSpace(opts.BeforeTraceID); before != "" {
		if at := indexOfTrace(entries, before); at >= 0 {
			end = at
		}
	} else if focus := strings.TrimSpace(opts.FocusTraceID); focus != "" {
		if at := indexOfTrace(entries, focus); at >= 0 {
			end = at + 1
		}
	}
	if end < 0 {
		end = 0
	}
	if end > len(entries) {
		end = len(entries)
	}
	start := end - opts.Limit
	if start < 0 {
		start = 0
	}
	return start, end
}

func indexOfTrace(entries []store.LogEntry, traceID string) int {
	for index := range entries {
		if entries[index].ID == traceID {
			return index
		}
	}
	return -1
}

// orderEntries sorts oldest first and drops duplicates, which is the order the
// turn inference and every reader depend on.
func orderEntries(entries []store.LogEntry) []store.LogEntry {
	ordered := make([]store.LogEntry, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if entry.ID == "" {
			continue
		}
		if _, ok := seen[entry.ID]; ok {
			continue
		}
		seen[entry.ID] = struct{}{}
		ordered = append(ordered, entry)
	}
	sort.SliceStable(ordered, func(i, j int) bool {
		left, right := ordered[i].Header.Meta.Time, ordered[j].Header.Meta.Time
		if left.Equal(right) {
			return ordered[i].ID < ordered[j].ID
		}
		return left.Before(right)
	})
	return ordered
}

// totalsFromEntries aggregates the whole session from the index rows alone.
func totalsFromEntries(entries []store.LogEntry) Totals {
	totals := Totals{Steps: len(entries)}
	for _, entry := range entries {
		usage := entry.Header.Usage
		cached := 0
		if usage.PromptTokenDetails != nil {
			cached = usage.PromptTokenDetails.CachedTokens
		}
		stepUsage := usageFromHeader(entry.Header.Meta.Provider, entry.Header.Meta.Endpoint, usage.PromptTokens, usage.CompletionTokens, cached)
		mergeTotals(&totals, stepUsage)
		totals.ModelMs += entry.Header.Meta.DurationMs
		if entry.Header.Layout.IsStream && entry.Header.Meta.TTFTMs > 0 && entry.Header.Meta.TTFTMs <= entry.Header.Meta.DurationMs {
			totals.TTFTMs += entry.Header.Meta.TTFTMs
		}
	}
	if len(entries) > 0 {
		first := entries[0].Header.Meta.Time
		last := entries[len(entries)-1]
		end := last.Header.Meta.Time.Add(time.Duration(last.Header.Meta.DurationMs) * time.Millisecond)
		totals.WallMs = end.Sub(first).Milliseconds()
		if totals.WallMs < 0 {
			totals.WallMs = 0
		}
	}
	finishTotals(&totals)
	return totals
}

func lastUserSig(sigs []messageSig) ([16]byte, bool) {
	for index := len(sigs) - 1; index >= 0; index-- {
		if sigs[index].Role == "user" {
			return sigs[index].Hash, true
		}
	}
	return [16]byte{}, false
}

func sameSignatures(left, right []messageSig) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func appendUnique(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func intPtr(value int) *int { return &value }

func normalizeOptions(opts Options) Options {
	if opts.Limit <= 0 {
		opts.Limit = DefaultLimit
	}
	if opts.Limit > MaxLimit {
		opts.Limit = MaxLimit
	}
	if opts.Budget <= 0 {
		opts.Budget = DefaultBudget
	}
	if opts.Workers <= 0 {
		opts.Workers = defaultWorkers
	}
	if opts.Registry == nil {
		opts.Registry = observe.NewDefaultRegistry()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return opts
}

// cacheKey identifies one window of one session. The count and the last trace
// make an appended request miss the cache without the writer having to
// invalidate anything.
func cacheKey(sessionID string, entries []store.LogEntry, opts Options) string {
	if len(entries) == 0 {
		return ""
	}
	last := entries[len(entries)-1].ID
	raw := fmt.Sprintf("%s\x00%d\x00%d\x00%s\x00%s\x00%d\x00%s",
		sessionID, BuilderVersion, opts.Limit, opts.BeforeTraceID, opts.FocusTraceID, len(entries), last)
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
