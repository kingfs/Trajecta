package store

import (
	"strings"
	"time"
)

// RoutingSummaryBucket is one distinct combination of the routing facts the
// Monitor's routing summary reports, with the number of traces that had it.
type RoutingSummaryBucket struct {
	SelectedUpstreamID       string
	RouteTargetID            string
	ChannelID                string
	CredentialID             string
	StickyStatus             string
	StickyPreviousUpstreamID string
	RoutingFailureReason     string
	TraceCount               int64
}

// RoutingSummary is the aggregate behind the Monitor's routing page.
type RoutingSummary struct {
	// TotalTraces counts the traces in the window, with the model filter applied.
	TotalTraces int64
	// EventfulTraces counts the traces that recorded a routing decision, which is
	// every trace written since the routing columns were added.
	EventfulTraces int64
	// Buckets holds one entry per distinct routing-fact combination. It is tiny -
	// it is bounded by the number of distinct upstreams, channels, credentials and
	// failure reasons, not by the number of traces - so the read path can afford
	// to fold it in Go and keep the SQL portable to SQLite.
	Buckets []RoutingSummaryBucket
}

// LegacyOrMissingEvents counts traces with no recorded routing facts. Traces
// written before the routing columns existed land here, as do the rare traces
// whose request never reached routing at all.
func (s RoutingSummary) LegacyOrMissingEvents() int64 {
	return s.TotalTraces - s.EventfulTraces
}

// RoutingSummary aggregates the routing decisions recorded in the window.
//
// The summary used to walk every trace in its window, open each cassette and
// parse its prelude events. A random read on the deployment's rotational disk
// measures 57 ms and is dominated by the seek rather than the bytes, so the
// default "today" window took 49 s and the endpoint was effectively unusable.
// The routing facts themselves are five short strings per trace, so they are
// mirrored into the indexed row when the cassette is finalised and re-derived
// from the prelude on a re-index; this query then groups over `logs`, which
// already carries recorded_at and model indexes.
//
// The counts are per trace rather than per routing event. The event walk counted
// `routing.selection` and `routing.selected` separately even though a single
// request emits both for the same target, so a trace that selected one upstream
// contributed two to that upstream's total. Counting each trace once is what the
// page's labels claim and what an operator reads them as.
func (s *Store) RoutingSummary(since time.Time, model string) (RoutingSummary, error) {
	clauses := []string{"1 = 1"}
	args := make([]any, 0, 2)
	if !since.IsZero() {
		clauses = append(clauses, "recorded_at >= ?")
		args = append(args, since.UTC().Format(timeLayout))
	}
	if model = strings.TrimSpace(model); model != "" {
		clauses = append(clauses, `LOWER(model) LIKE LOWER(?) ESCAPE '\'`)
		args = append(args, "%"+escapeLike(model)+"%")
	}

	rows, err := s.db.Query(`
		SELECT
			selected_upstream_id, route_target_id, channel_id, credential_id,
			sticky_status, sticky_previous_upstream_id, routing_failure_reason,
			COUNT(*)
		FROM logs
		WHERE `+strings.Join(clauses, " AND ")+`
		GROUP BY 1, 2, 3, 4, 5, 6, 7
	`, args...)
	if err != nil {
		return RoutingSummary{}, err
	}
	defer rows.Close()

	summary := RoutingSummary{}
	for rows.Next() {
		var bucket RoutingSummaryBucket
		if err := rows.Scan(
			&bucket.SelectedUpstreamID,
			&bucket.RouteTargetID,
			&bucket.ChannelID,
			&bucket.CredentialID,
			&bucket.StickyStatus,
			&bucket.StickyPreviousUpstreamID,
			&bucket.RoutingFailureReason,
			&bucket.TraceCount,
		); err != nil {
			return RoutingSummary{}, err
		}
		summary.TotalTraces += bucket.TraceCount
		if bucket.hasRoutingFacts() {
			summary.EventfulTraces += bucket.TraceCount
		}
		summary.Buckets = append(summary.Buckets, bucket)
	}
	if err := rows.Err(); err != nil {
		return RoutingSummary{}, err
	}
	return summary, nil
}

// hasRoutingFacts reports whether a bucket carries any recorded routing decision.
// StickyPreviousUpstreamID is deliberately absent: the write path only records it
// alongside StickyStatus, so it cannot be the only populated column.
func (b RoutingSummaryBucket) hasRoutingFacts() bool {
	return b.SelectedUpstreamID != "" ||
		b.RouteTargetID != "" ||
		b.ChannelID != "" ||
		b.CredentialID != "" ||
		b.StickyStatus != "" ||
		b.RoutingFailureReason != ""
}
