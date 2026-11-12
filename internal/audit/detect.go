package audit

import (
	"context"

	"github.com/kingfs/Trajecta/pkg/detect"
	detectsecretmask "github.com/kingfs/Trajecta/pkg/detect/secretmask"
	"github.com/kingfs/Trajecta/pkg/xir/audit"
)

// Detection is one advisory sensitive-value match in the audit projection.
//
// It deliberately carries no value: the raw cassette already has the bytes, and
// this view is served by the Monitor to any authenticated reader. Rule + span +
// path is enough to jump to the evidence without duplicating the secret.
type Detection struct {
	Section    string  `json:"section"`
	Path       string  `json:"path"`
	Rule       string  `json:"rule"`
	Start      int     `json:"start"`
	End        int     `json:"end"`
	Confidence float64 `json:"confidence"`
}

// detectObservation runs a pkg/detect engine over the text nodes of the XIR
// audit projection. The engine is supplied by the caller (the detection-plugin
// snapshot in production); a nil engine falls back to the builtin secretmask
// scanner so the projection still works without a plugin runtime.
//
// Opaque protocol material never reaches Node.Text by construction (sealed
// blocks and media bytes stay in Raw/Metadata), so this is a text-only lane.
func detectObservation(ctx context.Context, observation *audit.Observation, engine *detect.Engine) ([]Detection, error) {
	if observation == nil || len(observation.Nodes) == 0 {
		return nil, nil
	}
	if engine == nil {
		engine = detect.New(detectsecretmask.New())
	}
	var out []Detection
	for _, node := range observation.Nodes {
		if node.Text == "" {
			continue
		}
		matches, err := engine.Scan(ctx, detect.Input{
			Raw:      []byte(node.Text),
			Protocol: string(observation.Protocol),
			Meta:     map[string]string{"path": node.Path},
		})
		if err != nil {
			return nil, err
		}
		for _, match := range matches {
			out = append(out, Detection{
				Section:    node.Section,
				Path:       node.Path,
				Rule:       match.Rule,
				Start:      match.Start,
				End:        match.End,
				Confidence: match.Confidence,
			})
		}
	}
	return out, nil
}
