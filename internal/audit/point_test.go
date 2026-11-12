package audit

import (
	"context"
	"testing"

	"github.com/kingfs/Trajecta/pkg/detect"
	detectsecretmask "github.com/kingfs/Trajecta/pkg/detect/secretmask"
	"github.com/kingfs/Trajecta/pkg/xir"
	"github.com/kingfs/Trajecta/pkg/xir/audit"
	"github.com/kingfs/Trajecta/pkg/xir/hook"
)

func TestNodePoint(t *testing.T) {
	cases := []struct {
		name string
		node audit.Node
		want hook.Point
	}{
		{"sealed request", audit.Node{Section: audit.SectionRequest, Kind: string(xir.PartSealed)}, ""},
		{"sealed response", audit.Node{Section: audit.SectionResponse, Kind: string(xir.PartSealed)}, ""},
		{"request tool call", audit.Node{Section: audit.SectionRequest, Kind: string(xir.PartToolCall)}, hook.PointRequestToolCall},
		{"response tool call", audit.Node{Section: audit.SectionResponse, Kind: string(xir.PartToolCall)}, hook.PointResponseToolCall},
		{"request tool result", audit.Node{Section: audit.SectionRequest, Kind: string(xir.PartToolResult)}, hook.PointRequestToolResult},
		{"response reasoning", audit.Node{Section: audit.SectionResponse, Kind: string(xir.PartReasoning)}, hook.PointResponseReasoning},
		{"tool declaration", audit.Node{Section: audit.SectionRequest, Kind: "tool"}, hook.PointRequestToolDecl},
		{"system message", audit.Node{Section: audit.SectionRequest, Kind: "message", Role: xir.RoleSystem}, hook.PointRequestSystem},
		{"user message", audit.Node{Section: audit.SectionRequest, Kind: "message", Role: xir.RoleUser}, hook.PointRequestMessage},
		{"request text", audit.Node{Section: audit.SectionRequest, Kind: string(xir.PartText), Role: xir.RoleUser}, hook.PointRequestMessage},
		{"response text", audit.Node{Section: audit.SectionResponse, Kind: string(xir.PartText)}, hook.PointResponseOutput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := NodePoint(tc.node); got != tc.want {
				t.Fatalf("NodePoint = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDetectObservationRespectsPointScope(t *testing.T) {
	const key = "AKIAIOSFODNN7EXAMPLE"
	observation := &audit.Observation{
		Protocol: "chat_completions",
		Nodes: []audit.Node{
			{Section: audit.SectionRequest, Path: "$.messages[0]", Kind: string(xir.PartText), Role: xir.RoleUser, Text: "key " + key},
			{Section: audit.SectionResponse, Path: "$.output[0]", Kind: string(xir.PartText), Text: "key " + key},
		},
	}
	// Only the request.message point has an engine; response.output has none.
	secret := detect.New(detectsecretmask.New())
	resolve := func(point hook.Point) *detect.Engine {
		if point == hook.PointRequestMessage {
			return secret
		}
		return nil
	}
	got, err := detectObservation(context.Background(), observation, resolve, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 scoped detection, got %d: %+v", len(got), got)
	}
	if got[0].Point != string(hook.PointRequestMessage) || got[0].Section != audit.SectionRequest {
		t.Fatalf("detection point/section = %q/%q", got[0].Point, got[0].Section)
	}
}

func TestDetectObservationSkipsSealed(t *testing.T) {
	observation := &audit.Observation{
		Protocol: "responses",
		Nodes: []audit.Node{
			// A sealed encrypted-reasoning block: no Text, only Raw. It must
			// never be scanned even though its bytes contain a key shape.
			{Section: audit.SectionResponse, Path: "$.output[0]", Kind: string(xir.PartSealed), Raw: []byte(`{"encrypted_content":"AKIAIOSFODNN7EXAMPLE"}`)},
		},
	}
	secret := detect.New(detectsecretmask.New())
	resolve := func(hook.Point) *detect.Engine { return secret }
	got, err := detectObservation(context.Background(), observation, resolve, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("sealed content must not be detected, got %+v", got)
	}
}
