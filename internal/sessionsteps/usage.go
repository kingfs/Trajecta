package sessionsteps

import (
	"strings"

	"github.com/kingfs/Trajecta/pkg/llm"
	"github.com/kingfs/Trajecta/pkg/observe"
)

// usageFromObservation normalizes one parsed exchange's usage. Anthropic
// reports input_tokens without the cache buckets; every other family we record
// reports prompt_tokens with the cached part already included. Collapsing both
// onto disjoint buckets is what makes the context column and the hit rate mean
// the same thing for either protocol.
func usageFromObservation(provider, endpoint string, u observe.ObservationUsage) Usage {
	anthropic := isAnthropic(provider, endpoint)
	read := nonNegative(u.CacheReadTokens)
	write := 0
	input := nonNegative(u.InputTokens)
	if anthropic {
		write = nonNegative(u.CacheCreationTokens)
	} else {
		// prompt_tokens is inclusive: subtract what the cache served, and never
		// let a provider that over-reports go negative.
		input = nonNegative(input - read)
	}
	return Usage{
		Input:      input,
		CacheRead:  read,
		CacheWrite: write,
		Context:    input + read + write,
		Output:     nonNegative(u.OutputTokens),
		CacheHit:   cacheHit(read, input+read+write),
	}
}

// usageFromHeader is the fallback for a step whose cassette was not read. The
// indexed prompt/completion/cached counters are enough for the numeric table;
// only the cache-write bucket is missing, and the step is marked
// content_pending so a reader knows the detail is not there.
func usageFromHeader(provider, endpoint string, prompt, completion, cached int) Usage {
	read := nonNegative(cached)
	// The indexed prompt counter is inclusive for every protocol we record, so
	// the cache-served part is subtracted rather than reported twice. An
	// unparsed exchange cannot tell us how much of the prompt was a cache
	// write, so that bucket stays zero and the step says so.
	input := nonNegative(nonNegative(prompt) - read)
	return Usage{
		Input:     input,
		CacheRead: read,
		Context:   input + read,
		Output:    nonNegative(completion),
		CacheHit:  cacheHit(read, input+read),
	}
}

// cacheHit is cacheRead over the whole prompt, clamped to [0,1]. A step with no
// reported prompt has no ratio to show, and nil (not zero) is what the UI
// renders as "—".
func cacheHit(read, context int) *float64 {
	if context <= 0 {
		return nil
	}
	ratio := float64(read) / float64(context)
	if ratio > 1 {
		ratio = 1
	}
	if ratio < 0 {
		ratio = 0
	}
	return &ratio
}

// tokensPerSecond divides the generated tokens by the generation span.
//
// The span is derived, not measured: it is the recorded duration minus the time
// to first byte. When those two are nearly equal - a buffered stream, or an
// upstream that sends the whole body at the end - the subtraction leaves a
// handful of milliseconds and the ratio becomes meaningless (a 189-token answer
// "in 34 ms" is 5,559 tok/s, which no model produces). Those cases report no
// rate rather than a confident wrong one.
func tokensPerSecond(output int, decodeMs *int64, totalMs int64) *float64 {
	if decodeMs == nil || output <= 0 {
		return nil
	}
	if *decodeMs < minDecodeMs {
		return nil
	}
	if totalMs > 0 && *decodeMs*10 < totalMs {
		return nil
	}
	value := float64(output) / (float64(*decodeMs) / 1000)
	return &value
}

// minDecodeMs is the shortest generation span the throughput column trusts.
const minDecodeMs int64 = 50

// isAnthropic reports whether the exchange speaks Anthropic's usage shape.
// The endpoint is authoritative: a relay channel can record any provider name
// while still serving /v1/messages.
func isAnthropic(provider, endpoint string) bool {
	if strings.EqualFold(strings.TrimSpace(provider), llm.ProviderAnthropic) {
		return true
	}
	return strings.TrimSuffix(llm.NormalizeEndpoint(endpoint), "/") == "/v1/messages"
}

// nonNegative clamps a counter a provider may have sent as a negative value.
func nonNegative(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

// mergeTotals adds one step's usage into the session totals.
func mergeTotals(t *Totals, u Usage) {
	t.Input += u.Input
	t.CacheRead += u.CacheRead
	t.CacheWrite += u.CacheWrite
	t.Context += u.Context
	t.Output += u.Output
}

// finishTotals derives the values that need the accumulated sums: the hit rate
// is token-weighted, never the average of per-step ratios.
func finishTotals(t *Totals) {
	t.CacheHit = cacheHit(t.CacheRead, t.Context)
}
