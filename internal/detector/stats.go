package detector

import "sync/atomic"

// ScopeOutcome is why the hot path did or did not project a request body. It is
// reported by the detector stats endpoint so an operator can see which path a
// deployment actually takes instead of inferring it from the config key.
type ScopeOutcome string

const (
	ScopeProjected       ScopeOutcome = "projected"
	ScopeUnknownProtocol ScopeOutcome = "unknown_protocol"
	ScopeEmptyBody       ScopeOutcome = "empty_body"
	ScopeNoMarker        ScopeOutcome = "no_sealed_marker"
	ScopeOverSize        ScopeOutcome = "over_size_bound"
	ScopeParseFailed     ScopeOutcome = "parse_failed"
	ScopeDecodeFailed    ScopeOutcome = "decode_failed"
)

// runtimeStats is the hot path's process-lifetime counter set. The counters are
// atomics because every forwarding goroutine updates them; Stats() returns a
// plain snapshot for the admin API.
type runtimeStats struct {
	hotCalls         atomic.Uint64
	irProjected      atomic.Uint64
	irUnknownProto   atomic.Uint64
	irEmptyBody      atomic.Uint64
	irNoMarker       atomic.Uint64
	irOverSize       atomic.Uint64
	irParseFailed    atomic.Uint64
	irDecodeFailed   atomic.Uint64
	byteFallbacks    atomic.Uint64
	fieldRuns        atomic.Uint64
	sealedSpans      atomic.Uint64
	maskCandidates   atomic.Uint64
	maskEvents       atomic.Uint64
	scannerErrors    atomic.Uint64
	scopeSizeCapHits atomic.Uint64
}

// StatsSnapshot is the JSON shape of Runtime.Stats.
type StatsSnapshot struct {
	HotCalls       uint64 `json:"hot_calls"`
	IRProjected    uint64 `json:"ir_projected"`
	IRNoMarker     uint64 `json:"ir_skipped_no_marker"`
	IROverSize     uint64 `json:"ir_skipped_over_size"`
	IRParseFailed  uint64 `json:"ir_parse_failed"`
	IRDecodeFailed uint64 `json:"ir_decode_failed"`
	IROtherSkip    uint64 `json:"ir_skipped_other"`
	ByteFallbacks  uint64 `json:"byte_fallbacks"`
	FieldRuns      uint64 `json:"field_runs"`
	SealedSpans    uint64 `json:"sealed_spans"`
	MaskCandidates uint64 `json:"mask_candidates"`
	MaskEvents     uint64 `json:"mask_events"`
	ScannerErrors  uint64 `json:"scanner_errors"`
	SizeCapHits    uint64 `json:"size_cap_hits"`
}

// Stats returns a snapshot of the hot-path counters.
func (r *Runtime) Stats() StatsSnapshot {
	if r == nil {
		return StatsSnapshot{}
	}
	s := &r.stats
	return StatsSnapshot{
		HotCalls:       s.hotCalls.Load(),
		IRProjected:    s.irProjected.Load(),
		IRNoMarker:     s.irNoMarker.Load(),
		IROverSize:     s.irOverSize.Load(),
		IRParseFailed:  s.irParseFailed.Load(),
		IRDecodeFailed: s.irDecodeFailed.Load(),
		IROtherSkip:    s.irUnknownProto.Load() + s.irEmptyBody.Load(),
		ByteFallbacks:  s.byteFallbacks.Load(),
		FieldRuns:      s.fieldRuns.Load(),
		SealedSpans:    s.sealedSpans.Load(),
		MaskCandidates: s.maskCandidates.Load(),
		MaskEvents:     s.maskEvents.Load(),
		ScannerErrors:  s.scannerErrors.Load(),
		SizeCapHits:    s.scopeSizeCapHits.Load(),
	}
}

// recordScopeOutcome attributes one projection decision.
func (r *Runtime) recordScopeOutcome(outcome ScopeOutcome) {
	if r == nil {
		return
	}
	s := &r.stats
	switch outcome {
	case ScopeProjected:
		s.irProjected.Add(1)
	case ScopeNoMarker:
		s.irNoMarker.Add(1)
	case ScopeOverSize:
		s.irOverSize.Add(1)
		s.scopeSizeCapHits.Add(1)
	case ScopeParseFailed:
		s.irParseFailed.Add(1)
	case ScopeDecodeFailed:
		s.irDecodeFailed.Add(1)
	case ScopeUnknownProtocol:
		s.irUnknownProto.Add(1)
	case ScopeEmptyBody:
		s.irEmptyBody.Add(1)
	}
}
