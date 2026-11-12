package detector

import (
	"context"
	"log/slog"

	"github.com/kingfs/Trajecta/pkg/detect"
	"github.com/kingfs/Trajecta/pkg/secretmask"
)

// Masker is the hot-lane masking coordinator. It implements gateway.Masker: it
// collects maskable candidates from the enabled detection plugins and hands them
// to the Vault, which alone renders stand-ins and enforces every replacement
// invariant (equal length, opaque protection, idempotency, suppression). The
// detection set is pluggable; the replacement engine is not.
//
// A nil Vault (masking disabled by the master switch) makes Mask a no-op, so the
// coordinator can be installed unconditionally and the YAML switch still governs
// whether anything leaves the machine rewritten.
type Masker struct {
	vault *secretmask.Vault
	rt    *Runtime
}

// NewMasker binds a Vault to the detection runtime.
func NewMasker(vault *secretmask.Vault, rt *Runtime) *Masker {
	return &Masker{vault: vault, rt: rt}
}

// Mask rewrites src using the enabled hot-lane detectors over the whole body
// (the byte fast path). It is equivalent to MaskWithProtocol with no protocol.
func (m *Masker) Mask(src []byte) ([]byte, []secretmask.MaskEvent) {
	return m.MaskWithProtocol(src, "")
}

// MaskWithProtocol rewrites src, applying XIR structural scoping when a wire
// protocol is given and a hot-lane detector is enabled. It parses the body to the
// XIR projection once and scans the whole body EXCEPT the sealed (encrypted
// reasoning / media) spans, so coverage matches the byte path while opaque
// content is excluded structurally rather than by a post-hoc byte filter.
// Findings are handed to the Vault, which remains the final opaque guard. If the
// body cannot be parsed (unknown protocol or decode failure) it falls back to the
// whole-body byte path. With no protocol it is the byte fast path with zero
// parsing overhead.
//
// Field-level point scoping is intentionally not used to narrow the hot scan
// universe: a credential in a non-projected position must still be masked. Points
// scope the advisory audit lane instead.
//
// It never fails: the builtin detector is total, and a third-party scanner error
// is logged and skipped. The Vault still refuses any candidate that is not
// maskable, overlaps another, or sits in an opaque value.
func (m *Masker) MaskWithProtocol(src []byte, protocol string) ([]byte, []secretmask.MaskEvent) {
	if m == nil || m.vault == nil || len(src) == 0 {
		return src, nil
	}
	snap := m.rt.Snapshot()
	var cands []secretmask.MaskCandidate
	scoped := false
	if snap != nil && protocol != "" && snap.hasIRScope() {
		if scope, ok := ParseRequestScope(protocol, src); ok {
			cands = snap.collectCandidatesIR(src, scope)
			scoped = true
		}
	}
	if !scoped {
		cands = snap.collectCandidates(src)
	}
	if len(cands) == 0 {
		return src, nil
	}
	return m.vault.MaskCandidates(src, cands)
}

// hasIRScope reports whether any hot-lane detector is enabled, which is what
// makes IR parsing worthwhile on the hot path (structural sealed exclusion).
func (s *Snapshot) hasIRScope() bool {
	return s.secretmaskHot || len(s.hotScanners) > 0
}

// collectCandidates gathers builtin and plugin findings over the whole body (the
// byte fast path). Builtin secretmask findings keep their RuleID so the Vault
// renders them from the exact compiled rule; plugin findings carry rule metadata
// in Meta.
func (s *Snapshot) collectCandidates(src []byte) []secretmask.MaskCandidate {
	if s == nil {
		return nil
	}
	var cands []secretmask.MaskCandidate
	if s.secretmaskHot {
		for _, f := range (secretmask.Detector{}).Scan(src) {
			if !s.secretmaskRuleAllowed(f) {
				continue
			}
			cands = append(cands, secretmask.MaskCandidate{Finding: f})
		}
	}
	for _, hs := range s.hotScanners {
		matches, err := hs.Scan(context.Background(), detect.Input{Raw: src})
		if err != nil {
			slog.Warn("Detector hot-lane scanner failed; skipping its matches", "scanner", hs.Name(), "error", err)
			continue
		}
		for _, m := range matches {
			if m.Metadata["maskable"] != "true" || m.Rule == "" {
				continue
			}
			cands = append(cands, candidateFromMatch(m))
		}
	}
	return cands
}

// collectCandidatesIR gathers findings over the whole body minus the sealed
// spans (RequestScope.ScannableSpans). This preserves the byte path's coverage —
// a credential in a non-projected position (JSON key, user/metadata field,
// JSON-only tool result) is still scanned — while excluding sealed/encrypted
// content structurally. The builtin detector scans each scannable span's raw
// bytes (offsetting findings back into the body) so it keeps its RuleID. The
// Vault remains the final opaque guard regardless.
func (s *Snapshot) collectCandidatesIR(src []byte, scope RequestScope) []secretmask.MaskCandidate {
	if s == nil {
		return nil
	}
	spans := scope.ScannableSpans()
	var cands []secretmask.MaskCandidate
	if s.secretmaskHot {
		for _, span := range spans {
			if span.Start < 0 || span.End > len(src) || span.End <= span.Start {
				continue
			}
			for _, f := range (secretmask.Detector{}).Scan(src[span.Start:span.End]) {
				if !s.secretmaskRuleAllowed(f) {
					continue
				}
				f.Start += span.Start
				f.End += span.Start
				cands = append(cands, secretmask.MaskCandidate{Finding: f})
			}
		}
	}
	for _, hs := range s.hotScanners {
		matches, err := hs.Scan(context.Background(), detect.Input{Raw: src, Spans: spans})
		if err != nil {
			slog.Warn("Detector hot-lane scanner failed; skipping its matches", "scanner", hs.Name(), "error", err)
			continue
		}
		for _, m := range matches {
			if m.Metadata["maskable"] != "true" || m.Rule == "" {
				continue
			}
			cands = append(cands, candidateFromMatch(m))
		}
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
