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
	got, err := detectObservation(context.Background(), observation)
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
