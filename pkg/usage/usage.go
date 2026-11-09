// Package usage defines the protocol-neutral token usage model shared by the
// Exchange IR, recorders and audit consumers.
//
// The model keeps the provider-reported counters separate instead of folding
// them into one total: cache reads, cache writes, reasoning tokens and the
// two Anthropic cache-write TTLs are billed and displayed differently. Totals
// are derived, never authoritative input.
package usage

import "strings"

// Usage is one provider-reported usage snapshot. A zero value means "not
// reported", not "zero tokens": a provider that omits a counter must not erase
// an earlier value when snapshots are merged.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`

	ReasoningTokens int `json:"reasoning_tokens,omitempty"`

	// CacheReadTokens are prompt tokens served from the provider prompt cache.
	CacheReadTokens int `json:"cache_read_tokens,omitempty"`
	// CacheCreationTokens are prompt tokens written to the cache. The 5m/1h
	// split is only present for Anthropic-style providers.
	CacheCreationTokens   int `json:"cache_creation_tokens,omitempty"`
	CacheCreation5mTokens int `json:"cache_creation_5m_tokens,omitempty"`
	CacheCreation1hTokens int `json:"cache_creation_1h_tokens,omitempty"`

	// Source names the wire shape the counters came from (chat, responses,
	// messages, gemini) so audit code can distinguish a provider's own count
	// from a local estimate.
	Source string `json:"source,omitempty"`
	// Estimated marks a locally derived count (for example a stream that never
	// reported usage). It is not a provider fact.
	Estimated bool `json:"estimated,omitempty"`
}

// Normalize fills TotalTokens when a provider reported only input/output. It
// never fabricates input or output counters.
func (u Usage) Normalize() Usage {
	if u.TotalTokens == 0 && (u.InputTokens > 0 || u.OutputTokens > 0) {
		u.TotalTokens = u.InputTokens + u.OutputTokens
	}
	return u
}

// PromptTokens is every token the prompt consumed, including cache reads and
// cache writes. OpenAI's input count normally already includes the cache
// portion; Anthropic's input count excludes it. Callers must use the
// provider's own semantics when they need a single prompt figure.
func (u Usage) PromptTokens() int {
	return u.InputTokens + u.CacheReadTokens + u.CacheCreationTokens
}

// Empty reports whether no counter was reported at all.
func (u Usage) Empty() bool {
	return u.InputTokens == 0 && u.OutputTokens == 0 && u.TotalTokens == 0 &&
		u.ReasoningTokens == 0 && u.CacheReadTokens == 0 && u.CacheCreationTokens == 0 &&
		u.CacheCreation5mTokens == 0 && u.CacheCreation1hTokens == 0
}

// Merge overlays b on a using the non-zero rule: a non-zero b field wins, a
// zero b field never erases an earlier count, and totals are recomputed from
// the merged input/output counters. Source follows the later snapshot when it
// names one; Estimated is true if either side was estimated.
//
// Merge is used for streamed usage snapshots, where a final event may repeat
// earlier counters and some intermediate events may carry zeros. It is
// idempotent for identical snapshots.
func Merge(a, b Usage) Usage {
	out := a
	if b.InputTokens != 0 {
		out.InputTokens = b.InputTokens
	}
	if b.OutputTokens != 0 {
		out.OutputTokens = b.OutputTokens
	}
	if b.TotalTokens != 0 {
		out.TotalTokens = b.TotalTokens
	}
	if b.ReasoningTokens != 0 {
		out.ReasoningTokens = b.ReasoningTokens
	}
	if b.CacheReadTokens != 0 {
		out.CacheReadTokens = b.CacheReadTokens
	}
	if b.CacheCreationTokens != 0 {
		out.CacheCreationTokens = b.CacheCreationTokens
	}
	if b.CacheCreation5mTokens != 0 {
		out.CacheCreation5mTokens = b.CacheCreation5mTokens
	}
	if b.CacheCreation1hTokens != 0 {
		out.CacheCreation1hTokens = b.CacheCreation1hTokens
	}
	if strings.TrimSpace(b.Source) != "" {
		out.Source = b.Source
	}
	out.Estimated = a.Estimated || b.Estimated

	// A provider total without input/output detail is kept. When detail
	// exists, the total is derived from the merged input/output counters:
	// streaming chunks often repeat a stale total, and a derived total is the
	// one the final counters support.
	if out.InputTokens != 0 || out.OutputTokens != 0 {
		out.TotalTokens = out.InputTokens + out.OutputTokens
	}
	return out
}

// Add is Merge under a narrower name for callers that accumulate a stream.
func Add(a, b Usage) Usage { return Merge(a, b) }
