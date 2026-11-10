package monitor

import (
	"testing"
	"time"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

func TestBuildTimelineEventViewsFallsBackToRawSections(t *testing.T) {
	parsed := &ParsedData{
		Header: recordfile.RecordHeader{Meta: recordfile.MetaData{
			Time:       time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC),
			DurationMs: 1200,
		}},
		ChatMessages: []ChatMessage{{Role: "user", Content: "hi"}},
		AIContent:    "hello",
	}
	if events := timelineEvents(parsed); len(events) != 2 || events[0].Type != "request" || events[1].Type != "response" {
		t.Fatalf("timelineEvents = %+v", events)
	}
	views := buildTimelineEventViews(parsed)
	if len(views) != 2 {
		t.Fatalf("views = %+v", views)
	}
	if views[0]["type"] != "request" || views[1]["type"] != "response" {
		t.Fatalf("types = %v/%v", views[0]["type"], views[1]["type"])
	}
	if len(views[0]["timeline_items"].([]timelineItemView)) == 0 {
		t.Fatal("request timeline has no items")
	}
}
