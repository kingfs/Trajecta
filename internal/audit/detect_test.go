package audit

import (
	"context"
	"testing"

	"github.com/kingfs/Trajecta/pkg/xir/audit"
)

func TestDetectObservationReportsRuleAndSpanWithoutValue(t *testing.T) {
	observation := &audit.Observation{
		Protocol: "chat_completions",
		Nodes: []audit.Node{
			{Section: audit.SectionRequest, Path: "$.messages[0]", Kind: "text", Text: "prefix OPENAI_API_KEY=abcdefghijklmnopqrstuvwxyz123456 suffix"},
		},
	}
	got, err := detectObservation(context.Background(), observation, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("no detections")
	}
	found := false
	for _, detection := range got {
		if detection.Rule == "" || detection.End <= detection.Start {
			t.Fatalf("bad detection: %+v", detection)
		}
		if detection.Path != "$.messages[0]" || detection.Section != audit.SectionRequest {
			t.Fatalf("bad location: %+v", detection)
		}
		found = true
	}
	if !found {
		t.Fatal("no valid detection")
	}
}

// The operator's suppression list applies to the advisory lane as well as to
// the rewrite: a suppressed false positive must not be reported here either.
func TestDetectObservationHonoursSuppression(t *testing.T) {
	observation := &audit.Observation{
		Protocol: "chat_completions",
		Nodes: []audit.Node{
			{Section: audit.SectionRequest, Path: "$.messages[0]", Kind: "text", Text: "key AKIAIOSFODNN7EXAMPLE"},
		},
	}
	all, err := detectObservation(context.Background(), observation, nil, nil)
	if err != nil || len(all) == 0 {
		t.Fatalf("baseline detections = %d, err = %v", len(all), err)
	}
	rule := all[0].Rule
	suppressed := func(candidate string, value []byte) bool {
		return candidate == rule && string(value) == "AKIAIOSFODNN7EXAMPLE"
	}
	got, err := detectObservation(context.Background(), observation, nil, suppressed)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("suppressed value was still reported: %+v", got)
	}
}
