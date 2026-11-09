package hook_test

import (
	"reflect"
	"testing"

	"github.com/kingfs/Trajecta/pkg/xir/hook"
)

func TestLaneHas(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		lane hook.Lane
		has  hook.Lane
		want bool
	}{
		{"hot has hot", hook.LaneHot, hook.LaneHot, true},
		{"hot has audit", hook.LaneHot, hook.LaneAudit, false},
		{"audit has audit", hook.LaneAudit, hook.LaneAudit, true},
		{"both has audit", hook.LaneHot | hook.LaneAudit, hook.LaneAudit, true},
		{"both has both", hook.LaneHot | hook.LaneAudit, hook.LaneHot | hook.LaneAudit, true},
		{"zero has hot", 0, hook.LaneHot, false},
		{"hot has zero", hook.LaneHot, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.lane.Has(tc.has); got != tc.want {
				t.Fatalf("%v.Has(%v) = %v, want %v", tc.lane, tc.has, got, tc.want)
			}
		})
	}
}

func TestLaneValid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		lane hook.Lane
		want bool
	}{
		{hook.LaneHot, true},
		{hook.LaneAudit, true},
		{hook.LaneHot | hook.LaneAudit, true},
		{0, false},
		{hook.Lane(4), false},
		{hook.LaneHot | hook.Lane(8), false},
	}
	for _, tc := range cases {
		if got := tc.lane.Valid(); got != tc.want {
			t.Fatalf("Lane(%d).Valid() = %v, want %v", tc.lane, got, tc.want)
		}
	}
}

func TestAllPoints(t *testing.T) {
	t.Parallel()
	want := []hook.Point{
		"request.envelope",
		"request.system",
		"request.message",
		"request.tool_declaration",
		"request.tool_call.arguments",
		"request.tool_result",
		"request.metadata",
		"response.output",
		"response.reasoning",
		"response.tool_call.arguments",
		"response.tool_result",
		"response.usage",
		"response.error",
		"stream.event",
		"stream.delta.text",
		"stream.delta.reasoning",
		"stream.delta.tool_args",
		"exchange.finalize",
	}
	got := hook.AllPoints()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AllPoints() = %v, want %v", got, want)
	}

	seen := make(map[hook.Point]bool, len(got))
	for _, point := range got {
		if !point.Valid() {
			t.Fatalf("AllPoints() returned invalid point %q", point)
		}
		if seen[point] {
			t.Fatalf("AllPoints() returned duplicate point %q", point)
		}
		seen[point] = true
	}

	// AllPoints returns a fresh copy on every call.
	got[0] = "mutated"
	if again := hook.AllPoints(); again[0] != hook.PointRequestEnvelope {
		t.Fatalf("AllPoints() shares its backing slice: second call starts with %q", again[0])
	}
}

func TestPointValid(t *testing.T) {
	t.Parallel()
	if hook.Point("").Valid() {
		t.Fatal("empty point is valid")
	}
	if hook.Point("request.egress").Valid() {
		t.Fatal("undefined point is valid")
	}
	if !hook.PointRequestEnvelope.Valid() {
		t.Fatal("PointRequestEnvelope is not valid")
	}
	if !hook.PointExchangeFinalize.Valid() {
		t.Fatal("PointExchangeFinalize is not valid")
	}
}

func TestAliasesMatchCanonicalConstants(t *testing.T) {
	t.Parallel()
	pairs := []struct {
		alias     hook.Point
		canonical hook.Point
	}{
		{hook.PointRequestToolDeclaration, hook.PointRequestToolDecl},
		{hook.PointRequestToolCallArguments, hook.PointRequestToolCall},
		{hook.PointResponseToolCallArguments, hook.PointResponseToolCall},
		{hook.PointStreamReasoningDelta, hook.PointStreamReasonDelta},
	}
	for _, pair := range pairs {
		if pair.alias != pair.canonical {
			t.Fatalf("alias %q != canonical %q", pair.alias, pair.canonical)
		}
	}
}

func TestNeedsHas(t *testing.T) {
	t.Parallel()
	both := hook.NeedsWire | hook.NeedsSemantic
	if !both.Has(hook.NeedsWire) || !both.Has(hook.NeedsSemantic) {
		t.Fatal("Needs.Has missed a set bit")
	}
	if both.Has(hook.NeedsStream) {
		t.Fatal("Needs.Has matched an unset bit")
	}
	if (hook.Needs(0)).Has(hook.NeedsWire) {
		t.Fatal("zero Needs matched a bit")
	}
	if !both.Valid() {
		t.Fatal("combined Needs is not valid")
	}
	if hook.Needs(0).Valid() != true {
		t.Fatal("zero Needs should be valid")
	}
	if hook.Needs(8).Valid() {
		t.Fatal("unknown Needs bit is valid")
	}
}

func TestSeverityValues(t *testing.T) {
	t.Parallel()
	got := []hook.Severity{
		hook.SeverityInfo,
		hook.SeverityLow,
		hook.SeverityMedium,
		hook.SeverityHigh,
		hook.SeverityCritical,
	}
	want := []hook.Severity{"info", "low", "medium", "high", "critical"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("severity values = %v, want %v", got, want)
	}
}
