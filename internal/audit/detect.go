package audit

import (
	"context"

	"github.com/kingfs/Trajecta/pkg/detect"
	detectsecretmask "github.com/kingfs/Trajecta/pkg/detect/secretmask"
	"github.com/kingfs/Trajecta/pkg/xir/audit"
	"github.com/kingfs/Trajecta/pkg/xir/hook"
)

// Detection is one advisory sensitive-value match in the audit projection.
//
// It deliberately carries no value: the raw cassette already has the bytes, and
// this view is served by the Monitor to any authenticated reader. Rule + span +
// path is enough to jump to the evidence without duplicating the secret. Point
// names the XIR field the match was found in, so a reader can see which plugin
// scope produced it.
type Detection struct {
	Section    string  `json:"section"`
	Path       string  `json:"path"`
	Point      string  `json:"point,omitempty"`
	Rule       string  `json:"rule"`
	Start      int     `json:"start"`
	End        int     `json:"end"`
	Confidence float64 `json:"confidence"`
}

// engineResolver returns the detection engine scoped to one XIR point. The
// detection-plugin snapshot supplies it in production; a nil resolver falls back
// to the builtin secretmask scanner on every point so the projection still works
// without a plugin runtime.
type engineResolver func(hook.Point) *detect.Engine

// detectObservation runs the point-scoped detection engines over the text nodes
// of the XIR audit projection. Each node is mapped to its XIR point (NodePoint)
// and scanned only by the plugins that declare that point. A sealed block maps
// to no point and carries no text, so encrypted reasoning and media are skipped
// structurally rather than by a detector having to recognise them.
func detectObservation(ctx context.Context, observation *audit.Observation, resolve engineResolver, suppressed func(rule string, value []byte) bool) ([]Detection, error) {
	if observation == nil || len(observation.Nodes) == 0 {
		return nil, nil
	}
	if resolve == nil {
		fallback := detect.New(detectsecretmask.New())
		resolve = func(hook.Point) *detect.Engine { return fallback }
	}
	var out []Detection
	for _, node := range observation.Nodes {
		if node.Text == "" {
			continue
		}
		point := NodePoint(node)
		if point == "" {
			continue
		}
		engine := resolve(point)
		if engine == nil {
			continue
		}
		matches, err := engine.Scan(ctx, detect.Input{
			Raw:      []byte(node.Text),
			Protocol: string(observation.Protocol),
			Meta:     map[string]string{"path": node.Path, "point": string(point)},
		})
		if err != nil {
			return nil, err
		}
		for _, match := range matches {
			if suppressed != nil && match.Start >= 0 && match.End <= len(node.Text) && match.End > match.Start {
				if suppressed(match.Rule, []byte(node.Text)[match.Start:match.End]) {
					continue
				}
			}
			out = append(out, Detection{
				Section:    node.Section,
				Path:       node.Path,
				Point:      string(point),
				Rule:       match.Rule,
				Start:      match.Start,
				End:        match.End,
				Confidence: match.Confidence,
			})
		}
	}
	return out, nil
}
