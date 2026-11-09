// Package secretmask adapts pkg/secretmask's credential/PII detector to the
// generic pkg/detect Scanner interface.
//
// The adapter scans only the spans the caller declares. That is how a protocol
// layer excludes opaque fields (encrypted reasoning, image payloads, signatures)
// without teaching pkg/secretmask about JSON or protocol schemas. When Input.Spans
// is empty the whole buffer is scanned.
package secretmask

import (
	"context"
	"fmt"

	"github.com/kingfs/Trajecta/pkg/detect"
	"github.com/kingfs/Trajecta/pkg/secretmask"
)

// Scanner implements detect.Scanner using the stateless secretmask.Detector.
type Scanner struct {
	detector secretmask.Detector
}

// New returns a Scanner. The zero value is also usable.
func New() *Scanner { return &Scanner{} }

// Name is the stable scanner identifier.
func (s *Scanner) Name() string { return "secretmask" }

// Scan returns one Match per secretmask finding, with absolute offsets into
// Input.Raw. Findings are not rewritten here; pkg/secretmask's maskable band is
// carried in Metadata["maskable"] for the hook/rewrite layer to decide.
func (s *Scanner) Scan(ctx context.Context, in detect.Input) ([]detect.Match, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
	}
	if len(in.Raw) == 0 {
		return nil, nil
	}
	spans := in.Spans
	if len(spans) == 0 {
		spans = []detect.Span{{Start: 0, End: len(in.Raw)}}
	}
	var out []detect.Match
	for _, scope := range spans {
		if ctx != nil {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if !scope.Valid() || scope.Start < 0 || scope.End > len(in.Raw) || scope.Start > scope.End {
			continue
		}
		findings := s.detector.Scan(in.Raw[scope.Start:scope.End])
		for _, finding := range findings {
			rule, _ := finding.Rule()
			meta := map[string]string{
				"vendor":   rule.Vendor,
				"maskable": fmt.Sprintf("%t", finding.Maskable()),
				"reasons":  finding.Reasons.String(),
				"score":    fmt.Sprintf("%d", finding.Score),
			}
			if rule.Name != "" {
				meta["rule"] = rule.Name
			}
			out = append(out, detect.Match{
				Span: detect.Span{
					Start: scope.Start + finding.Start,
					End:   scope.Start + finding.End,
					Path:  scope.Path,
					Kind:  scope.Kind,
				},
				Rule:       rule.Name,
				Confidence: confidence(finding.Confidence),
				Value:      string(finding.Bytes(in.Raw[scope.Start:scope.End])),
				Metadata:   meta,
			})
		}
	}
	return out, nil
}

func confidence(c secretmask.Confidence) float64 {
	switch c {
	case secretmask.ConfidenceHigh:
		return 1.0
	case secretmask.ConfidenceMedium:
		return 0.7
	default:
		return 0.4
	}
}

var _ detect.Scanner = (*Scanner)(nil)
