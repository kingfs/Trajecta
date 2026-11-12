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

// Mask rewrites src using the enabled hot-lane detectors. It never fails: the
// builtin detector is total, and a third-party scanner error is logged and
// skipped (its matches are simply not offered). The Vault still refuses any
// candidate that is not maskable, overlaps another, or sits in an opaque value.
func (m *Masker) Mask(src []byte) ([]byte, []secretmask.MaskEvent) {
	if m == nil || m.vault == nil || len(src) == 0 {
		return src, nil
	}
	snap := m.rt.Snapshot()
	cands := snap.collectCandidates(src)
	if len(cands) == 0 {
		return src, nil
	}
	return m.vault.MaskCandidates(src, cands)
}

// collectCandidates gathers builtin and plugin findings for one body. Builtin
// secretmask findings keep their RuleID so the Vault renders them from the exact
// compiled rule; plugin findings carry rule metadata in Meta.
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
	for _, scanner := range s.hotScanners {
		matches, err := scanner.Scan(context.Background(), detect.Input{Raw: src})
		if err != nil {
			slog.Warn("Detector hot-lane scanner failed; skipping its matches", "scanner", scanner.Name(), "error", err)
			continue
		}
		for _, m := range matches {
			if m.Metadata["maskable"] != "true" {
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
