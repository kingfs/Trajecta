package sessionsteps

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/llm"
	"github.com/kingfs/Trajecta/pkg/observe"
	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// cassetteSpec is one synthetic recording: the request body, the response
// body, and the index row the database would hold for it.
type cassetteSpec struct {
	id         string
	time       time.Time
	duration   time.Duration
	ttft       time.Duration
	stream     bool
	provider   string
	endpoint   string
	operation  string
	prompt     int
	completion int
	cached     int
	reqBody    string
	resBody    string
	// minimalPrelude writes the prelude the recorder writes when
	// trace.minimal_cassette is on: no timing, no usage, no events. The entry
	// still carries the full header, exactly as the database does.
	minimalPrelude bool
	// brokenPath makes the entry point at a file that does not exist.
	brokenPath bool
}

func writeCassette(t *testing.T, dir string, spec cassetteSpec) store.LogEntry {
	t.Helper()

	provider := spec.provider
	if provider == "" {
		provider = llm.ProviderOpenAICompatible
	}
	endpoint := spec.endpoint
	if endpoint == "" {
		endpoint = "/v1/chat/completions"
	}
	operation := spec.operation
	if operation == "" {
		operation = llm.OperationChatCompletions
	}
	meta := recordfile.MetaData{
		RequestID:  spec.id,
		Time:       spec.time,
		Model:      "gpt-test",
		Provider:   provider,
		Operation:  operation,
		Endpoint:   endpoint,
		URL:        endpoint,
		Method:     "POST",
		StatusCode: 200,
		DurationMs: spec.duration.Milliseconds(),
		TTFTMs:     spec.ttft.Milliseconds(),
	}
	usage := recordfile.UsageInfo{
		PromptTokens:     spec.prompt,
		CompletionTokens: spec.completion,
		TotalTokens:      spec.prompt + spec.completion,
	}
	if spec.cached > 0 {
		usage.PromptTokenDetails = &recordfile.PromptTokenDetails{CachedTokens: spec.cached}
	}
	reqHead := "POST " + endpoint + " HTTP/1.1\r\nHost: example.com\r\nContent-Type: application/json\r\n\r\n"
	resHead := "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n"
	header := recordfile.RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta:    meta,
		Layout: recordfile.LayoutInfo{
			ReqHeaderLen: int64(len(reqHead)),
			ReqBodyLen:   int64(len(spec.reqBody)),
			ResHeaderLen: int64(len(resHead)),
			ResBodyLen:   int64(len(spec.resBody)),
			IsStream:     spec.stream,
		},
		Usage: usage,
	}

	preludeHeader := header
	if spec.minimalPrelude {
		// What minimalCassetteHeader leaves behind: identity plus layout only.
		preludeHeader.Meta.DurationMs = 0
		preludeHeader.Meta.TTFTMs = 0
		preludeHeader.Usage = recordfile.UsageInfo{}
	}
	prelude, err := recordfile.MarshalPrelude(preludeHeader, nil)
	if err != nil {
		t.Fatalf("MarshalPrelude() error = %v", err)
	}
	var content bytes.Buffer
	content.Write(prelude)
	content.WriteString(reqHead)
	content.WriteString(spec.reqBody)
	content.WriteString("\n")
	content.WriteString(resHead)
	content.WriteString(spec.resBody)
	path := filepath.Join(dir, spec.id+".http")
	if err := os.WriteFile(path, content.Bytes(), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if spec.brokenPath {
		path = filepath.Join(dir, "missing-"+spec.id+".http")
	}
	return store.LogEntry{ID: spec.id, LogPath: path, Header: header}
}

func chatRequest(messages string) string {
	return `{"model":"gpt-test","messages":[` + messages + `]}`
}

const (
	userHello = `{"role":"user","content":"hello"}`
	systemMsg = `{"role":"system","content":"you are a test"}`
	// The assistant turn that asked for a tool, replayed in the next request's
	// history exactly as a stateless client re-sends it.
	assistantToolCall = `{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"ls\"}"}}]}`
	toolResult        = `{"role":"tool","tool_call_id":"call_1","content":"file.txt"}`
)

// sse frames a chat-completions stream exactly as a cassette records it.
func sse(chunks ...string) string {
	var out bytes.Buffer
	for _, chunk := range chunks {
		out.WriteString("data: ")
		out.WriteString(chunk)
		out.WriteString("\n\n")
	}
	out.WriteString("data: [DONE]\n\n")
	return out.String()
}

func usageChunk(prompt, completion, cached int) string {
	details := ""
	if cached > 0 {
		details = `,"prompt_tokens_details":{"cached_tokens":` + itoa(cached) + `}`
	}
	return `{"id":"r1","object":"chat.completion.chunk","model":"gpt-test","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":` +
		itoa(prompt) + `,"completion_tokens":` + itoa(completion) + `,"total_tokens":` + itoa(prompt+completion) + details + `}}`
}

func toolCallResponse(prompt, completion, cached int) string {
	delta := `{"id":"r1","object":"chat.completion.chunk","model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"ls\"}"}}]},"finish_reason":null}]}`
	return sse(delta, usageChunk(prompt, completion, cached))
}

func finalResponse(text string, prompt, completion, cached int) string {
	delta := `{"id":"r2","object":"chat.completion.chunk","model":"gpt-test","choices":[{"index":0,"delta":{"role":"assistant","content":"` + text + `"},"finish_reason":null}]}`
	return sse(delta, usageChunk(prompt, completion, cached))
}

// finalJSON is the non-streamed wire shape.
func finalJSON(text string) string {
	return `{"id":"r2","object":"chat.completion","model":"gpt-test","choices":[{"index":0,"message":{"role":"assistant","content":"` + text + `"},"finish_reason":"stop"}],"usage":{"prompt_tokens":120,"completion_tokens":20,"total_tokens":140}}`
}

func itoa(value int) string { return strconv.Itoa(value) }

func TestBuildGroupsToolContinuationIntoOneTurn(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC)
	entries := []store.LogEntry{
		writeCassette(t, dir, cassetteSpec{
			id: "t1", time: base, duration: time.Second, ttft: 200 * time.Millisecond, stream: true,
			prompt: 100, completion: 10, cached: 90,
			reqBody: chatRequest(systemMsg + "," + userHello),
			resBody: toolCallResponse(100, 10, 90),
		}),
		writeCassette(t, dir, cassetteSpec{
			id: "t2", time: base.Add(3 * time.Second), duration: 2 * time.Second, ttft: 500 * time.Millisecond, stream: true,
			prompt: 120, completion: 20, cached: 110,
			reqBody: chatRequest(systemMsg + "," + userHello + "," + assistantToolCall + "," + toolResult),
			resBody: finalResponse("done", 120, 20, 110),
		}),
		writeCassette(t, dir, cassetteSpec{
			id: "t3", time: base.Add(10 * time.Second), duration: time.Second, ttft: 100 * time.Millisecond, stream: true,
			prompt: 130, completion: 5, cached: 120,
			reqBody: chatRequest(systemMsg + "," + userHello + "," + assistantToolCall + "," + toolResult + `,{"role":"user","content":"next"}`),
			resBody: finalResponse("second", 130, 5, 120),
		}),
	}

	view, err := Build(context.Background(), "sess-1", entries, Options{Now: fixedClock(base)})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(view.Turns) != 2 {
		t.Fatalf("turns = %d, want 2", len(view.Turns))
	}
	if got := len(view.Turns[0].Steps); got != 2 {
		t.Fatalf("turn 1 steps = %d, want 2 (the tool continuation belongs to the same turn)", got)
	}
	if view.Turns[0].Trigger.Kind != TriggerUser || view.Turns[0].Trigger.Text != "hello" {
		t.Fatalf("turn 1 trigger = %+v, want user/hello", view.Turns[0].Trigger)
	}
	if view.Turns[1].Trigger.Text != "next" {
		t.Fatalf("turn 2 trigger = %q, want next", view.Turns[1].Trigger.Text)
	}
	first := view.Turns[0].Steps[0]
	if len(first.Did.Tools) != 1 || first.Did.Tools[0].Name != "bash" || first.Did.Tools[0].Calls != 1 {
		t.Fatalf("step 1 tools = %+v, want one bash call", first.Did.Tools)
	}
	if first.Timing.ToolWaitMs == nil || *first.Timing.ToolWaitMs != 2000 {
		t.Fatalf("step 1 tool wait = %v, want 2000ms (the gap to the next request)", first.Timing.ToolWaitMs)
	}
	second := view.Turns[0].Steps[1]
	if second.Content == nil || len(second.Content.ToolResults) != 1 || second.Content.ToolResults[0].CallID != "call_1" {
		t.Fatalf("step 2 tool results = %+v, want the call_1 result", second.Content)
	}
	if second.Content.Answer == "" {
		t.Fatal("step 2 answer preview is empty")
	}
	// Timeline checks: the stream's TTFT separates the first byte from the
	// generation span.
	if first.Timing.TTFTMs == nil || *first.Timing.TTFTMs != 200 {
		t.Fatalf("step 1 ttft = %v, want 200", first.Timing.TTFTMs)
	}
	if first.Timing.DecodeMs == nil || *first.Timing.DecodeMs != 800 {
		t.Fatalf("step 1 decode = %v, want 800", first.Timing.DecodeMs)
	}
	if first.Timing.TPS == nil || *first.Timing.TPS != 12.5 {
		t.Fatalf("step 1 tps = %v, want 12.5", first.Timing.TPS)
	}
	if got := first.Usage.Context; got != 100 {
		t.Fatalf("step 1 context = %d, want 100 (input 10 + cache read 90)", got)
	}
	if got := first.Usage.Input; got != 10 {
		t.Fatalf("step 1 input = %d, want 10 (prompt_tokens is inclusive of the cache read)", got)
	}
	if got := first.Estimated; len(got) != 1 || got[0] != EstimatedTurnBoundary {
		t.Fatalf("step 1 estimated = %v, want just the turn boundary", got)
	}
}

func TestAnthropicCacheBucketsComeFromTheResponseBody(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC)
	request := `{"model":"claude-test","max_tokens":100,"system":"sys","messages":[{"role":"user","content":"hello"}]}`
	// Anthropic reports the cache buckets separately from input_tokens, so all
	// three add up to the prompt and the hit rate is read/total.
	response := `{"id":"msg_1","type":"message","role":"assistant","model":"claude-test",` +
		`"content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn",` +
		`"usage":{"input_tokens":50,"output_tokens":10,"cache_creation_input_tokens":150,"cache_read_input_tokens":800}}`
	entry := writeCassette(t, dir, cassetteSpec{
		id: "msg", time: base, duration: 2 * time.Second, stream: false,
		provider: llm.ProviderAnthropic, endpoint: "/v1/messages", operation: llm.OperationMessages,
		// The index row deliberately disagrees: the parsed body is authoritative
		// once a step has been read.
		prompt: 1000, completion: 10, cached: 800,
		reqBody: request, resBody: response,
	})
	view, err := Build(context.Background(), "sess", []store.LogEntry{entry}, Options{Now: fixedClock(base)})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	usage := view.Turns[0].Steps[0].Usage
	if usage.Input != 50 || usage.CacheRead != 800 || usage.CacheWrite != 150 {
		t.Fatalf("usage = %+v, want the disjoint Anthropic buckets", usage)
	}
	if usage.Context != 1000 {
		t.Fatalf("context = %d, want 1000", usage.Context)
	}
	if usage.CacheHit == nil || *usage.CacheHit != 0.8 {
		t.Fatalf("cache hit = %v, want 0.8", usage.CacheHit)
	}
}

func TestThroughputIsSuppressedWhenTheSpanIsUnmeasured(t *testing.T) {
	// A buffered stream: time to first byte is nearly the whole exchange, so the
	// derived generation span is a measurement artifact and the ratio must not
	// be reported.
	total := int64(1908)
	decode := int64(34)
	if got := tokensPerSecond(189, &decode, total); got != nil {
		t.Fatalf("throughput = %v, want nil for a 34 ms generation span", *got)
	}
	shared := int64(88)
	if got := tokensPerSecond(189, &shared, int64(1920)); got != nil {
		t.Fatalf("throughput = %v, want nil when the span is under a tenth of the exchange", *got)
	}
	real := int64(800)
	value := tokensPerSecond(800, &real, 1000)
	if value == nil || *value != 1000 {
		t.Fatalf("throughput = %v, want 1000 for a real 800 ms span", value)
	}
	if got := tokensPerSecond(10, nil, 1000); got != nil {
		t.Fatal("throughput must be nil without a span")
	}
}

func TestToolResultsAreTrimmedToTheRequestsTail(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC)
	// An agent loop re-sends the whole conversation on every request. Only the
	// results appended since the previous request belong to this step; the rest
	// is history the request echoed back.
	history := systemMsg + "," + userHello + "," + assistantToolCall + "," + toolResult
	entries := []store.LogEntry{
		writeCassette(t, dir, cassetteSpec{
			id: "t1", time: base, duration: time.Second, prompt: 100, completion: 10,
			reqBody: chatRequest(systemMsg + "," + userHello), resBody: toolCallResponse(100, 10, 0),
		}),
		writeCassette(t, dir, cassetteSpec{
			id: "t2", time: base.Add(2 * time.Second), duration: time.Second, prompt: 120, completion: 20,
			reqBody: chatRequest(history), resBody: finalResponse("done", 120, 20, 0),
		}),
		writeCassette(t, dir, cassetteSpec{
			id: "t3", time: base.Add(4 * time.Second), duration: time.Second, prompt: 130, completion: 5,
			reqBody: chatRequest(history + `,{"role":"assistant","content":"done"},{"role":"user","content":"next"}`),
			resBody: finalResponse("second", 130, 5, 0),
		}),
	}
	view, err := Build(context.Background(), "sess", entries, Options{Now: fixedClock(base)})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	steps := view.Turns[0].Steps
	if len(steps) != 2 {
		t.Fatalf("turn steps = %d, want 2", len(steps))
	}
	if got := len(steps[0].Content.ToolResults); got != 0 {
		t.Fatalf("first step results = %d, want 0: its predecessor is outside the window", got)
	}
	if got := len(steps[1].Content.ToolResults); got != 1 {
		t.Fatalf("second step results = %d, want only the appended one", got)
	}
	// The third request re-sends the same tool result; it must not be reported
	// again as if the tool had run twice.
	second := view.Turns[1].Steps[0]
	if got := len(second.Content.ToolResults); got != 0 {
		t.Fatalf("third step results = %d, want 0: the result is history there", got)
	}
}

func TestBuildNonStreamCarriesNoFirstToken(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC)
	// The recorder measures time to first byte, which for a non-streamed body
	// is the whole exchange. Reporting it as a first-token latency would make
	// the throughput column divide by roughly zero.
	entry := writeCassette(t, dir, cassetteSpec{
		id: "t1", time: base, duration: 20 * time.Second, ttft: 20 * time.Second, stream: false,
		prompt: 50, completion: 700, reqBody: chatRequest(userHello), resBody: finalResponse("ok", 10, 1, 0),
	})
	view, err := Build(context.Background(), "", []store.LogEntry{entry}, Options{Now: fixedClock(base)})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	step := view.Turns[0].Steps[0]
	if step.Timing.TTFTMs != nil {
		t.Fatalf("ttft = %v, want nil for a non-streamed exchange", *step.Timing.TTFTMs)
	}
	if step.Timing.TPS != nil {
		t.Fatalf("tps = %v, want nil when there is no generation span", *step.Timing.TPS)
	}
	if step.Timing.ModelMs != 20000 {
		t.Fatalf("model ms = %d, want the recorded duration", step.Timing.ModelMs)
	}
}

func TestUsageNormalizationAcrossProtocols(t *testing.T) {
	// OpenAI's prompt counter already contains the cache read.
	openAI := usageFromObservation(llm.ProviderOpenAICompatible, "/v1/chat/completions", observationUsage(1000, 100, 900, 0))
	if openAI.Input != 100 || openAI.CacheRead != 900 || openAI.Context != 1000 || openAI.Output != 100 {
		t.Fatalf("openai usage = %+v, want input 100 + read 900", openAI)
	}
	// Anthropic reports the cache buckets separately, so input is already the
	// uncached part and the creation bucket joins the context.
	anthropic := usageFromObservation(llm.ProviderAnthropic, "/v1/messages", observationUsage(50, 100, 800, 150))
	if anthropic.Input != 50 || anthropic.CacheRead != 800 || anthropic.CacheWrite != 150 || anthropic.Context != 1000 {
		t.Fatalf("anthropic usage = %+v, want 50 + 800 + 150", anthropic)
	}
	if anthropic.CacheHit == nil || *anthropic.CacheHit != 0.8 {
		t.Fatalf("anthropic cache hit = %v, want 0.8", anthropic.CacheHit)
	}
	// A context with nothing reported has no ratio to show.
	if hit := cacheHit(0, 0); hit != nil {
		t.Fatalf("cacheHit(0,0) = %v, want nil", *hit)
	}
}

func TestTotalsCoverTheWholeSessionWhileTheWindowIsPartial(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC)
	var entries []store.LogEntry
	for index := 0; index < 5; index++ {
		entries = append(entries, writeCassette(t, dir, cassetteSpec{
			id:       fmt.Sprintf("t%d", index+1),
			time:     base.Add(time.Duration(index) * time.Minute),
			duration: time.Second,
			prompt:   100, completion: 10,
			reqBody: chatRequest(userHello),
			resBody: finalResponse("ok", 10, 1, 0),
		}))
	}
	view, err := Build(context.Background(), "sess", entries, Options{Limit: 2, Now: fixedClock(base)})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if view.Window.Returned != 2 || !view.Window.HasBefore || view.Window.HasAfter {
		t.Fatalf("window = %+v, want the last two steps with older history", view.Window)
	}
	if got := view.Totals.Steps; got != 5 {
		t.Fatalf("totals steps = %d, want the whole session (5)", got)
	}
	if got := view.Totals.Output; got != 50 {
		t.Fatalf("totals output = %d, want 50", got)
	}
	if view.Totals.Turns != nil {
		t.Fatalf("totals turns = %v, want nil when the window does not cover the session", *view.Totals.Turns)
	}
	// The window starts mid-session, so its first turn is a partial one.
	first := view.Turns[0].Steps[0]
	if !containsString(first.Estimated, EstimatedWindowPartial) {
		t.Fatalf("estimated = %v, want the window-partial marker", first.Estimated)
	}
	if got := first.GlobalIndex; got != 4 {
		t.Fatalf("first global index = %d, want 4 (steps 4 and 5)", got)
	}
}

func TestFocusEndsTheWindowAtTheTrace(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC)
	var entries []store.LogEntry
	for index := 0; index < 6; index++ {
		entries = append(entries, writeCassette(t, dir, cassetteSpec{
			id:       fmt.Sprintf("t%d", index+1),
			time:     base.Add(time.Duration(index) * time.Minute),
			duration: time.Second,
			prompt:   10, completion: 1,
			reqBody: chatRequest(userHello),
			resBody: finalResponse("ok", 10, 1, 0),
		}))
	}
	view, err := Build(context.Background(), "sess", entries, Options{Limit: 3, FocusTraceID: "t4", Now: fixedClock(base)})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if view.Window.FirstStep != 2 || view.Window.LastStep != 4 {
		t.Fatalf("window = %+v, want steps 2..4 ending at the focused trace", view.Window)
	}
	last := view.Turns[len(view.Turns)-1].Steps
	if got := last[len(last)-1].TraceID; got != "t4" {
		t.Fatalf("last step = %q, want t4", got)
	}

	// Paging backwards from that window keeps the newer steps out.
	page, err := Build(context.Background(), "sess", entries, Options{Limit: 2, BeforeTraceID: "t2", Now: fixedClock(base)})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if page.Window.FirstStep != 1 || page.Window.LastStep != 1 {
		t.Fatalf("page window = %+v, want only step 1 before t2", page.Window)
	}
	if !page.Window.HasAfter {
		t.Fatal("page has_after = false, want newer steps to remain reachable")
	}
}

func TestAfterCursorPagesForward(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC)
	var entries []store.LogEntry
	for index := 0; index < 6; index++ {
		entries = append(entries, writeCassette(t, dir, cassetteSpec{
			id:       fmt.Sprintf("t%d", index+1),
			time:     base.Add(time.Duration(index) * time.Minute),
			duration: time.Second,
			prompt:   10, completion: 1,
			reqBody: chatRequest(userHello),
			resBody: finalResponse("ok", 10, 1, 0),
		}))
	}
	view, err := Build(context.Background(), "sess", entries, Options{Limit: 2, AfterTraceID: "t2", Now: fixedClock(base)})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if view.Window.FirstStep != 3 || view.Window.LastStep != 4 {
		t.Fatalf("window = %+v, want steps 3..4 after t2", view.Window)
	}
	if !view.Window.HasAfter {
		t.Fatal("has_after = false, want the rest of the session to stay reachable")
	}
	// A cursor wins over the focus that the trace route always sets.
	focused, err := Build(context.Background(), "sess", entries, Options{Limit: 2, FocusTraceID: "t1", BeforeTraceID: "t5", Now: fixedClock(base)})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if focused.Window.FirstStep != 3 || focused.Window.LastStep != 4 {
		t.Fatalf("window = %+v, want the before cursor to beat the focus", focused.Window)
	}
}

func TestUnreadableCassetteKeepsTheNumbers(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC)
	entry := writeCassette(t, dir, cassetteSpec{
		id: "t1", time: base, duration: time.Second, ttft: 100 * time.Millisecond, stream: true,
		prompt: 100, completion: 10, cached: 90,
		reqBody: chatRequest(userHello), resBody: finalResponse("ok", 100, 10, 90),
		brokenPath: true,
	})
	view, err := Build(context.Background(), "sess", []store.LogEntry{entry}, Options{Now: fixedClock(base)})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	step := view.Turns[0].Steps[0]
	if step.Content != nil {
		t.Fatalf("content = %+v, want nil for a step whose cassette is gone", step.Content)
	}
	if step.Usage.Context != 100 || step.Usage.Output != 10 {
		t.Fatalf("usage = %+v, want the indexed counters to survive", step.Usage)
	}
	if !containsString(step.Estimated, EstimatedContentPending) {
		t.Fatalf("estimated = %v, want the content-pending marker", step.Estimated)
	}
	if len(view.Warnings) == 0 {
		t.Fatal("warnings are empty, want a cassette-unreadable warning")
	}
}

func TestMinimalPreludeStillReportsTimingFromTheIndex(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC)
	entry := writeCassette(t, dir, cassetteSpec{
		id: "t1", time: base, duration: time.Second, ttft: 300 * time.Millisecond, stream: true,
		prompt: 40, completion: 8,
		reqBody: chatRequest(userHello), resBody: finalResponse("ok", 40, 8, 0),
		minimalPrelude: true,
	})
	view, err := Build(context.Background(), "sess", []store.LogEntry{entry}, Options{Now: fixedClock(base)})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	step := view.Turns[0].Steps[0]
	if step.Timing.ModelMs != 1000 {
		t.Fatalf("model ms = %d, want 1000 from the index row", step.Timing.ModelMs)
	}
	if step.Timing.TTFTMs == nil || *step.Timing.TTFTMs != 300 {
		t.Fatalf("ttft = %v, want 300 from the index row", step.Timing.TTFTMs)
	}
	if step.Content == nil || step.Content.Answer == "" {
		t.Fatal("content is missing even though the cassette holds the bodies")
	}
}

func TestDuplicateRequestIsMarkedAsRetry(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC)
	body := chatRequest(userHello)
	entries := []store.LogEntry{
		writeCassette(t, dir, cassetteSpec{id: "t1", time: base, duration: time.Second, prompt: 10, completion: 1, reqBody: body, resBody: finalJSON("a")}),
		writeCassette(t, dir, cassetteSpec{id: "t2", time: base.Add(time.Second), duration: time.Second, prompt: 10, completion: 1, reqBody: body, resBody: finalJSON("a")}),
	}
	view, err := Build(context.Background(), "sess", entries, Options{Now: fixedClock(base)})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(view.Turns) != 1 || len(view.Turns[0].Steps) != 2 {
		t.Fatalf("turns = %+v, want one turn with two steps", view.Turns)
	}
	if !containsString(view.Turns[0].Steps[1].Estimated, EstimatedDuplicate) {
		t.Fatalf("estimated = %v, want the duplicate marker", view.Turns[0].Steps[1].Estimated)
	}
}

func TestCacheServesTheSecondBuild(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC)
	entry := writeCassette(t, dir, cassetteSpec{
		id: "t1", time: base, duration: time.Second, prompt: 10, completion: 1,
		reqBody: chatRequest(userHello), resBody: finalResponse("ok", 10, 1, 0),
	})
	cache := &countingCache{entries: map[string][]byte{}}

	first, err := Build(context.Background(), "sess", []store.LogEntry{entry}, Options{Cache: cache, Now: fixedClock(base)})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if cache.stores != 1 {
		t.Fatalf("stores = %d, want 1", cache.stores)
	}
	// Removing the cassette proves the second view came from the cache: a
	// rebuild would lose every preview.
	if err := os.Remove(entry.LogPath); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	second, err := Build(context.Background(), "sess", []store.LogEntry{entry}, Options{Cache: cache, Now: fixedClock(base)})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if second.Window.ContentPending != 0 {
		t.Fatalf("content pending = %d, want the cached view with no pending content", second.Window.ContentPending)
	}
	if second.Totals.Steps != first.Totals.Steps {
		t.Fatalf("cached totals differ: %+v vs %+v", first.Totals, second.Totals)
	}
}

func TestBudgetLeavesLaterStepsWithNumbersOnly(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC)
	var entries []store.LogEntry
	for index := 0; index < 6; index++ {
		entries = append(entries, writeCassette(t, dir, cassetteSpec{
			id:       fmt.Sprintf("t%d", index+1),
			time:     base.Add(time.Duration(index) * time.Minute),
			duration: time.Second,
			prompt:   100, completion: 10,
			reqBody: chatRequest(userHello),
			resBody: finalResponse("ok", 100, 10, 0),
		}))
	}
	// One read costs more than half the budget, so with a single worker the
	// later steps cannot be reached in time. This is the degradation the page
	// reports as "N steps unparsed": the numbers stay, the content does not.
	readFactsFn = func(ctx context.Context, registry *observe.Registry, entry store.LogEntry) facts {
		time.Sleep(60 * time.Millisecond)
		return readFacts(ctx, registry, entry)
	}
	t.Cleanup(func() { readFactsFn = readFacts })

	view, err := Build(context.Background(), "sess", entries, Options{
		Budget:  100 * time.Millisecond,
		Workers: 1,
		Now:     fixedClock(base),
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if view.Window.ContentPending == 0 {
		t.Fatal("content_pending = 0, want the budget to leave later steps unparsed")
	}
	if view.Window.ContentPending == len(entries) {
		t.Fatal("content_pending covers every step, want at least one parsed read")
	}
	if view.Totals.Steps != len(entries) {
		t.Fatalf("totals.steps = %d, want the whole session from the index rows", view.Totals.Steps)
	}
	var unparsed int
	for _, turn := range view.Turns {
		for _, step := range turn.Steps {
			if step.Content == nil {
				unparsed++
				if !containsString(step.Estimated, EstimatedContentPending) {
					t.Fatalf("step %d estimated = %v, want the content-pending marker", step.GlobalIndex, step.Estimated)
				}
				if step.Usage.Context == 0 && step.Usage.Output == 0 {
					t.Fatalf("step %d lost its indexed numbers: %+v", step.GlobalIndex, step.Usage)
				}
			}
		}
	}
	if unparsed != view.Window.ContentPending {
		t.Fatalf("unparsed steps = %d, window.content_pending = %d", unparsed, view.Window.ContentPending)
	}
	var warned bool
	for _, warning := range view.Warnings {
		if warning.Code == WarningContentPending {
			warned = true
		}
	}
	if !warned {
		t.Fatalf("warnings = %+v, want the content-pending warning", view.Warnings)
	}
}

func TestBuildWithoutEntriesWarnsInsteadOfFailing(t *testing.T) {
	view, err := Build(context.Background(), "sess-empty", nil, Options{})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(view.Turns) != 0 {
		t.Fatalf("turns = %d, want 0", len(view.Turns))
	}
	if len(view.Warnings) == 0 || view.Warnings[0].Code != WarningNoSession {
		t.Fatalf("warnings = %+v, want the empty-session warning", view.Warnings)
	}
}

type countingCache struct {
	entries map[string][]byte
	stores  int
}

func (c *countingCache) Load(key string) ([]byte, bool) {
	data, ok := c.entries[key]
	return data, ok
}

func (c *countingCache) Store(key string, data []byte) error {
	c.stores++
	c.entries[key] = data
	return nil
}

func fixedClock(at time.Time) func() time.Time {
	return func() time.Time { return at }
}

func observationUsage(input, output, read, write int) observe.ObservationUsage {
	return observe.ObservationUsage{
		InputTokens:         input,
		OutputTokens:        output,
		TotalTokens:         input + output,
		CacheReadTokens:     read,
		CacheCreationTokens: write,
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
