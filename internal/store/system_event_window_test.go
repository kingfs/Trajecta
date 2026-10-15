package store

import (
	"strings"
	"testing"
	"time"
)

// The event feed's 今天/近 7 天/近 30 天 control is a bound on `system_events.last_seen_at`,
// and this is the one table in the store whose timestamps are compared without a
// formatter on either side: `logs.recorded_at` is written as a formatted string
// and compared as one, but a system event hands the driver a time.Time and lets
// it decide what the column holds. On SQLite the driver stores
// `2026-10-15 07:23:35.78 +0000 UTC`, so the boundary this test exercises used to
// be bound as RFC 3339 and the two strings were compared byte by byte: the first
// difference is the separator before the zone, ' ' below 'T', which drops every
// row sharing the boundary's calendar date. The six-hour window below puts its
// boundary on the same date as the two-hour-old event, which is the shape that
// failed: the list and the counters above it both answered zero. Postgres compares
// instants and was never affected, and no test caught it because every existing
// test of this table passes a zero time, which means "no bound at all".
func TestSystemEventWindowFiltersByInstant(t *testing.T) {
	st := configTransactionTestStore(t)
	now := time.Now().UTC()

	recent, err := st.UpsertSystemEvent(SystemEvent{
		Fingerprint: "parser:trace-recent:bad-json",
		Source:      "parser",
		Category:    "parse_failure",
		Severity:    "error",
		Title:       "Parse failed",
		Message:     "bad json",
		TraceID:     "trace-recent",
		LastSeenAt:  now.Add(-2 * time.Hour),
	})
	if err != nil {
		t.Fatalf("UpsertSystemEvent(recent) error = %v", err)
	}
	if _, err := st.UpsertSystemEvent(SystemEvent{
		Fingerprint: "parser:trace-old:bad-json",
		Source:      "parser",
		Category:    "parse_failure",
		Severity:    "error",
		Title:       "Parse failed",
		Message:     "bad json",
		TraceID:     "trace-old",
		LastSeenAt:  now.Add(-40 * time.Hour),
	}); err != nil {
		t.Fatalf("UpsertSystemEvent(old) error = %v", err)
	}

	// Six hours back reaches the two-hour-old event and not the forty-hour-old
	// one, and it is the same window the list and the counters above it answer,
	// so the two have to agree or the page contradicts its own total.
	since := now.Add(-6 * time.Hour)
	page, err := st.ListSystemEvents(SystemEventFilter{Since: since, Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("ListSystemEvents(since) error = %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 {
		t.Fatalf("page in window = %d items / total %d, want 1", len(page.Items), page.Total)
	}
	if page.Items[0].ID != recent.ID {
		t.Fatalf("page in window holds %q, want the recent event %q", page.Items[0].ID, recent.ID)
	}

	summary, err := st.SystemEventSummary(since)
	if err != nil {
		t.Fatalf("SystemEventSummary(since) error = %v", err)
	}
	if summary.Total != 1 || summary.Unread != 1 || summary.Error != 1 {
		t.Fatalf("summary in window = %+v, want one unread error", summary)
	}

	// A window that reaches past both events has to include both, which is the
	// half a "return everything" bug would still pass.
	wider := now.Add(-72 * time.Hour)
	page, err = st.ListSystemEvents(SystemEventFilter{Since: wider, Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("ListSystemEvents(wider) error = %v", err)
	}
	if page.Total != 2 {
		t.Fatalf("total in wider window = %d, want 2", page.Total)
	}
	summary, err = st.SystemEventSummary(wider)
	if err != nil {
		t.Fatalf("SystemEventSummary(wider) error = %v", err)
	}
	if summary.Total != 2 {
		t.Fatalf("summary in wider window = %+v, want both events", summary)
	}
}

// A window comparison over a text column is only ordered by instant while every
// row carries one offset: a row written in a fixed zone would be compared by its
// wall clock, so an event eleven hours west would sort below a six-hour boundary
// even though its instant is one hour old. The write path normalizes, and this
// pins both the outcome (the event is in the window) and the mechanism (the
// stored offset is zero), so a future writer cannot quietly reintroduce it.
func TestSystemEventWindowIgnoresTheWriterTimezone(t *testing.T) {
	st := configTransactionTestStore(t)
	now := time.Now().UTC()
	// Samoa Standard Time, which is UTC-11 and, unlike a long made-up name, is
	// short enough for the zone-abbreviation the reader's layouts accept.
	west := time.FixedZone("SST", -11*60*60)
	want := now.Add(-1 * time.Hour)

	event, err := st.UpsertSystemEvent(SystemEvent{
		Fingerprint: "parser:trace-west:bad-json",
		Source:      "parser",
		Category:    "parse_failure",
		Severity:    "error",
		Title:       "Parse failed",
		Message:     "bad json",
		TraceID:     "trace-west",
		LastSeenAt:  want.In(west),
	})
	if err != nil {
		t.Fatalf("UpsertSystemEvent(west) error = %v", err)
	}

	page, err := st.ListSystemEvents(SystemEventFilter{Since: now.Add(-6 * time.Hour), Page: 1, PageSize: 10})
	if err != nil {
		t.Fatalf("ListSystemEvents(since) error = %v", err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != event.ID {
		t.Fatalf("page = %d items / total %d, want the event written in %s", len(page.Items), page.Total, west)
	}
	if got := page.Items[0].LastSeenAt; !got.Equal(want) {
		t.Fatalf("last_seen_at = %s, want the instant %s", got, want)
	}

	// Read the raw text rather than the round-tripped value: the point is what
	// the window comparison sees, and it is compared as a string. timeParse is
	// what the reader uses, so this stays true if the stored layout changes.
	var stored string
	if err := st.db.QueryRow(`SELECT last_seen_at FROM system_events WHERE fingerprint = ?`, event.Fingerprint).Scan(&stored); err != nil {
		t.Fatalf("read stored last_seen_at error = %v", err)
	}
	if strings.TrimSpace(stored) == "" {
		t.Fatal("stored last_seen_at is empty")
	}
	parsed, err := timeParse(stored)
	if err != nil {
		t.Fatalf("timeParse(%q) error = %v", stored, err)
	}
	if _, offset := parsed.Zone(); offset != 0 {
		t.Fatalf("stored last_seen_at = %q, offset %d, want a zero offset", stored, offset)
	}
}
