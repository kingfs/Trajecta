package detector

import (
	"context"
	"log/slog"

	"github.com/kingfs/Trajecta/pkg/detect"
	"github.com/kingfs/Trajecta/pkg/secretmask"
	"github.com/kingfs/Trajecta/pkg/xir/hook"
)

// Masker is the hot-lane masking coordinator. It implements gateway.Masker: it
// collects maskable candidates from the enabled detection plugins and hands them
// to the Vault, which alone renders stand-ins and enforces every replacement
// invariant (equal length, opaque protection, sealed exclusion, idempotency,
// suppression). The detection set is pluggable; the replacement engine is not.
//
// A nil Vault (masking disabled by the master switch) makes Mask a no-op, so the
// coordinator can be installed unconditionally and the YAML switch still governs
// whether anything leaves the machine rewritten.
type Masker struct {
	vault *secretmask.Vault
	rt    *Runtime
	// maxScopeBytes bounds XIR projection on the hot path (0 = package default,
	// negative = unbounded).
	maxScopeBytes int
}

// NewMasker binds a Vault to the detection runtime. maxScopeBytes bounds the
// request body size that may be projected through an XIR codec.
func NewMasker(vault *secretmask.Vault, rt *Runtime, maxScopeBytes int) *Masker {
	return &Masker{vault: vault, rt: rt, maxScopeBytes: maxScopeBytes}
}

// Mask rewrites src using the enabled hot-lane detectors over the whole body
// (the byte fast path). It is equivalent to MaskWithProtocol with no protocol.
func (m *Masker) Mask(src []byte) ([]byte, []secretmask.MaskEvent) {
	return m.MaskWithProtocol(src, "")
}

// MaskWithProtocol rewrites src, applying the XIR-derived scope when a wire
// protocol is given and a hot-lane detector is enabled.
//
// Two scopes are in play, and they are deliberately independent:
//
//   - Sealed content: blocks a codec marks MustRoundTrip (signed thinking,
//     encrypted reasoning, redacted thinking) are never rewritten. The whole
//     body is still scanned exactly once; the Vault drops any candidate that
//     overlaps a sealed span. Scanning once keeps the detector's context window
//     identical to the byte path instead of slicing the buffer at every sealed
//     boundary, which would change scores near the cut.
//   - Fields: a scanner plugin that declares hook Points is fed only the spans
//     of those points, so a narrow algorithm never pays for the rest of the
//     body and never sees text it does not own. A plugin without Points keeps
//     whole-body coverage, which is what the builtin credential/PII scanner
//     uses: it must see every content position, not just the modeled ones.
//
// The chat-completions codec produces no sealed part at all, so a body-scoped
// only run skips the projection entirely and pays nothing for the IR.
//
// The method never fails: the builtin detector is total, and a third-party
// scanner error is logged and skipped. The Vault still refuses any candidate
// that is not maskable, overlaps another, sits in an opaque value, or overlaps
// sealed content.
func (m *Masker) MaskWithProtocol(src []byte, protocol string) ([]byte, []secretmask.MaskEvent) {
	if m == nil || m.vault == nil || len(src) == 0 {
		return src, nil
	}
	if m.rt != nil {
		m.rt.stats.hotCalls.Add(1)
	}
	snap := m.rt.Snapshot()
	if snap == nil {
		// A missing snapshot must never silently disable masking: fall back to
		// the builtin detector the vault already owns.
		masked, events := m.vault.Mask(src)
		if m.rt != nil {
			m.rt.stats.maskEvents.Add(uint64(len(events)))
		}
		return masked, events
	}
	if protocol == "" || !snap.hasIRScope() {
		if m.rt != nil {
			m.rt.stats.byteFallbacks.Add(1)
		}
		cands := snap.collectCandidates(src)
		masked, events := m.replacement(src, cands, nil)
		m.recordReplacement(cands, events)
		return masked, events
	}
	scope, outcome := ParseRequestScopeWithOutcome(protocol, src, ScopeOptions{
		MaxBytes:      m.maxScopeBytes,
		RequireFields: snap.hasHotFields(),
	})
	if m.rt != nil {
		m.rt.recordScopeOutcome(outcome)
	}
	if outcome != ScopeProjected {
		if m.rt != nil {
			m.rt.stats.byteFallbacks.Add(1)
		}
		cands := snap.collectCandidates(src)
		masked, events := m.replacement(src, cands, nil)
		m.recordReplacement(cands, events)
		return masked, events
	}
	sealed := scope.SealedSpans()
	if m.rt != nil {
		m.rt.stats.sealedSpans.Add(uint64(len(sealed)))
	}
	cands := snap.collectCandidatesScoped(src, scope)
	masked, events := m.replacement(src, cands, sealed)
	m.recordReplacement(cands, events)
	return masked, events
}

// recordReplacement updates the counters shared by both scan paths.
func (m *Masker) recordReplacement(cands []secretmask.MaskCandidate, events []secretmask.MaskEvent) {
	if m.rt == nil {
		return
	}
	m.rt.stats.maskCandidates.Add(uint64(len(cands)))
	m.rt.stats.maskEvents.Add(uint64(len(events)))
}

// replacement renders the candidates, excluding the sealed spans when there are
// any.
func (m *Masker) replacement(src []byte, cands []secretmask.MaskCandidate, sealed []detect.Span) ([]byte, []secretmask.MaskEvent) {
	if len(cands) == 0 {
		return src, nil
	}
	if len(sealed) == 0 {
		return m.vault.MaskCandidates(src, cands)
	}
	excluded := make([]secretmask.Span, len(sealed))
	for i, span := range sealed {
		excluded[i] = secretmask.Span{Start: span.Start, End: span.End}
	}
	return m.vault.MaskCandidatesExcluding(src, cands, excluded)
}

// hasIRScope reports whether any hot-lane detector is enabled, which is what
// makes IR parsing worthwhile on the hot path (sealed exclusion and/or fields).
func (s *Snapshot) hasIRScope() bool {
	return s.secretmaskHot || len(s.hotScanners) > 0
}

// hasHotFields reports whether an enabled hot-lane scanner declares hook Points,
// i.e. whether the hot path needs the per-point field projection.
func (s *Snapshot) hasHotFields() bool {
	if s == nil {
		return false
	}
	if s.secretmaskHot && s.smSettings.HotScan == "fields" {
		return true
	}
	for _, hs := range s.hotScanners {
		if len(hs.points) > 0 {
			return true
		}
	}
	return false
}

// collectCandidates gathers every enabled hot detector's findings over the whole
// body (the byte fast path). Builtin secretmask findings keep their RuleID so
// the Vault renders them from the exact compiled rule; plugin findings carry
// rule metadata in Meta.
func (s *Snapshot) collectCandidates(src []byte) []secretmask.MaskCandidate {
	if s == nil {
		return nil
	}
	cands := s.builtinCandidates(src)
	for _, hs := range s.hotScanners {
		cands = append(cands, s.scanHotScanner(hs, src, nil)...)
	}
	return cands
}

// collectCandidatesScoped is the IR-scoped collection. The builtin detector
// still scans the whole body (coverage is its contract); a plugin with Points
// scans only its own fields. The sealed spans are not removed from the body
// here: the Vault drops any candidate that overlaps one, which keeps every
// detector's context window intact.
func (s *Snapshot) collectCandidatesScoped(src []byte, scope RequestScope) []secretmask.MaskCandidate {
	if s == nil {
		return nil
	}
	cands := s.builtinCandidatesScoped(src, scope)
	for _, hs := range s.hotScanners {
		var spans []detect.Span
		if len(hs.points) > 0 {
			if s.stats != nil {
				s.stats.fieldRuns.Add(1)
			}
			spans = scope.FieldsFor(hs.points)
			if len(spans) == 0 {
				continue
			}
		}
		cands = append(cands, s.scanHotScanner(hs, src, spans)...)
	}
	return cands
}

// builtinCandidates runs the builtin credential/PII detector over the whole
// body and applies the plugin's rule-level settings.
func (s *Snapshot) builtinCandidates(src []byte) []secretmask.MaskCandidate {
	if !s.secretmaskHot {
		return nil
	}
	return s.filterBuiltin((secretmask.Detector{}).Scan(src))
}

// builtinCandidatesScoped is the IR-path variant. In the default body mode it
// is the whole-body scan; in fields mode it scans only the merged content-field
// spans of the projection, so the JSON envelope (model name, keys, unprojected
// siblings) is never fed to the DFA. Sealed spans never appear in the field map,
// so they are excluded by construction.
func (s *Snapshot) builtinCandidatesScoped(src []byte, scope RequestScope) []secretmask.MaskCandidate {
	if !s.secretmaskHot {
		return nil
	}
	if s.smSettings.HotScan != "fields" {
		return s.filterBuiltin((secretmask.Detector{}).Scan(src))
	}
	spans := scope.FieldsFor(hook.AllPoints())
	var cands []secretmask.MaskCandidate
	for _, span := range spans {
		if span.Start < 0 || span.End > len(src) || span.End <= span.Start {
			continue
		}
		for _, f := range (secretmask.Detector{}).Scan(src[span.Start:span.End]) {
			f.Start += span.Start
			f.End += span.Start
			if !s.secretmaskRuleAllowed(f) {
				continue
			}
			cands = append(cands, secretmask.MaskCandidate{Finding: f})
		}
	}
	return cands
}

// filterBuiltin applies the plugin's rule-level settings to native findings.
func (s *Snapshot) filterBuiltin(findings []secretmask.Finding) []secretmask.MaskCandidate {
	var cands []secretmask.MaskCandidate
	for _, f := range findings {
		if !s.secretmaskRuleAllowed(f) {
			continue
		}
		cands = append(cands, secretmask.MaskCandidate{Finding: f})
	}
	return cands
}

// scanHotScanner runs one third-party hot scanner over spans (nil means the
// whole body) and converts its maskable matches into Vault candidates.
func (s *Snapshot) scanHotScanner(hs hotScanner, src []byte, spans []detect.Span) []secretmask.MaskCandidate {
	matches, err := hs.scanner.Scan(context.Background(), detect.Input{Raw: src, Spans: spans})
	if err != nil {
		if s.stats != nil {
			s.stats.scannerErrors.Add(1)
		}
		slog.Warn("Detector hot-lane scanner failed; skipping its matches", "scanner", hs.scanner.Name(), "error", err)
		return nil
	}
	var cands []secretmask.MaskCandidate
	for _, m := range matches {
		if m.Metadata["maskable"] != "true" || m.Rule == "" {
			continue
		}
		cands = append(cands, candidateFromMatch(m))
	}
	return cands
}

// secretmaskRuleAllowed applies the builtin plugin's rule-level Settings to a
// native finding.
func (s *Snapshot) secretmaskRuleAllowed(f secretmask.Finding) bool {
	rule, ok := f.Rule()
	if !ok {
		return false
	}
	if s.smSettings.DisablePII && rule.Kind.PII() {
		return false
	}
	return !s.smDisabled[rule.Name]
}

// candidateFromMatch converts a plugin scanner match into a Vault candidate. The
// match's rule name becomes the render/audit identity; kind defaults to a generic
// credential unless the scanner declares one. This is the extension point a
// third-party hot-lane detector uses; the builtin secretmask plugin does not go
// through it.
func candidateFromMatch(m detect.Match) secretmask.MaskCandidate {
	meta := secretmask.Rule{Name: m.Rule, Kind: secretmask.KindGenericKey}
	if k, ok := m.Metadata["kind"]; ok {
		meta.Kind = kindFromString(k)
	}
	score := int(m.Confidence * 100)
	return secretmask.MaskCandidate{
		Finding: secretmask.Finding{
			Span:       secretmask.Span{Start: m.Start, End: m.End},
			Score:      score,
			Confidence: secretmask.ConfidenceFor(score),
			Reasons:    secretmask.ReasonGenericShape,
		},
		Meta: meta,
	}
}

// kindFromString maps a kind label to a secretmask.Kind, defaulting to a generic
// credential for an unknown label so a plugin cannot accidentally select the
// special PII re-rendering paths.
func kindFromString(s string) secretmask.Kind {
	switch s {
	case "api_key":
		return secretmask.KindAPIKey
	case "access_token":
		return secretmask.KindAccessToken
	case "private_key":
		return secretmask.KindPrivateKey
	case "generic_key":
		return secretmask.KindGenericKey
	default:
		return secretmask.KindGenericKey
	}
}
