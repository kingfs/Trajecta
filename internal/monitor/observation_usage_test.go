package monitor

import (
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/observe"
	"github.com/kingfs/Trajecta/pkg/recordfile"
)

// The protocol view shows the exchange's own cost. The parsed body is the only
// source for cache creation and reasoning tokens, and the index row is the only
// source for timing once `trace.minimal_cassette` has cleared it from the
// prelude, so the two have to be layered rather than chosen between.

func TestObservationUsagePrefersTheParsedBody(t *testing.T) {
	entry := store.LogEntry{Header: recordfile.RecordHeader{
		Usage: recordfile.UsageInfo{PromptTokens: 1000, CompletionTokens: 20},
	}}
	observation := observe.TraceObservation{Usage: observe.ObservationUsage{
		InputTokens: 50, OutputTokens: 10, CacheReadTokens: 800, CacheCreationTokens: 150, ReasoningTokens: 4,
	}}
	view := observationUsageViewFor(observation, entry)
	if view == nil {
		t.Fatal("usage view is nil")
	}
	if view.InputTokens != 50 || view.CacheCreationTokens != 150 || view.ReasoningTokens != 4 {
		t.Fatalf("usage = %+v, want the parsed counters", view)
	}
}

func TestObservationUsageFallsBackToTheIndex(t *testing.T) {
	entry := store.LogEntry{Header: recordfile.RecordHeader{
		Usage: recordfile.UsageInfo{
			PromptTokens:       900,
			CompletionTokens:   30,
			TotalTokens:        930,
			PromptTokenDetails: &recordfile.PromptTokenDetails{CachedTokens: 700},
		},
	}}
	// A minimal cassette has no usage in the prelude, so the parser reports
	// nothing and the indexed row is what is left.
	view := observationUsageViewFor(observe.TraceObservation{}, entry)
	if view == nil {
		t.Fatal("usage view is nil, want the indexed counters")
	}
	if view.InputTokens != 900 || view.CacheReadTokens != 700 || view.OutputTokens != 30 {
		t.Fatalf("usage = %+v, want the indexed counters", view)
	}
	// With neither source there is nothing to show, and the client renders no
	// strip rather than a row of zeroes.
	if empty := observationUsageViewFor(observe.TraceObservation{}, store.LogEntry{}); empty != nil {
		t.Fatalf("usage = %+v, want nil when nothing was reported", empty)
	}
}

func TestObservationTimingsFillFromTheIndex(t *testing.T) {
	started := time.Date(2026, 5, 1, 8, 0, 0, 0, time.UTC)
	entry := store.LogEntry{Header: recordfile.RecordHeader{Meta: recordfile.MetaData{
		Time: started, DurationMs: 2500, TTFTMs: 400,
	}}}
	view := observationTimingsViewFor(observe.TraceObservation{}, entry)
	if view == nil {
		t.Fatal("timings view is nil")
	}
	if view.StartedAt != started || view.DurationMs != 2500 || view.TTFTMs != 400 {
		t.Fatalf("timings = %+v, want the indexed values", view)
	}

	// The parsed timings win when the cassette carries them.
	parsed := observe.TraceObservation{Timings: observe.ObservationTimings{DurationMs: 1000, TTFTMs: 100}}
	fromBody := observationTimingsViewFor(parsed, entry)
	if fromBody.DurationMs != 1000 || fromBody.TTFTMs != 100 {
		t.Fatalf("timings = %+v, want the parsed values", fromBody)
	}
	if none := observationTimingsViewFor(observe.TraceObservation{}, store.LogEntry{}); none != nil {
		t.Fatalf("timings = %+v, want nil when nothing was measured", none)
	}
}
