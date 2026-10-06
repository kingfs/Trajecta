package store

import (
	"testing"
	"time"
)

// TestUsageBucketGridsAreUTC pins the location of every derived bucket boundary.
//
// The bucket maps are keyed by time.Time, and a time.Time key carries its location: the recorded slots
// are built from `recorded_at` (UTC) with .UTC().Truncate(bucketSize), so the pre-created grid has to
// be UTC as well. Three of the five grids derived their bucket start from a reference time without
// naming the location - and one of those references is time.Now() when the window has no record at all
// - so under a non-UTC local zone (TZ=Asia/Shanghai here) every lookup missed and the API returned an
// all-zero timeline, with the boundaries also rendered with a local offset. analytics.go and
// overview.go already named it; the three outliers now do too, and this gate keeps all five honest.
func TestUsageBucketGridsAreUTC(t *testing.T) {
	dir := t.TempDir()
	st, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer st.Close()

	now := time.Now().UTC()
	writeModelLog(t, st, dir, "bucket-grid.http", "gpt-5", "/v1/responses", "POST", "channel-a", 200, 11, now.Add(-2*time.Hour))
	writeModelLog(t, st, dir, "bucket-grid-old.http", "gpt-5", "/v1/responses", "POST", "channel-a", 200, 5, now.Add(-30*time.Hour))

	const bucketSize = time.Hour
	const bucketCount = 4
	since := now.Add(-24 * time.Hour)

	t.Run("channel trends", func(t *testing.T) {
		trends, err := st.GetChannelUsageTrends("channel-a", since, bucketSize, bucketCount)
		if err != nil {
			t.Fatalf("GetChannelUsageTrends() error = %v", err)
		}
		assertBucketGrid(t, "GetChannelUsageTrends", trendStarts(trends), bucketSize, bucketCount)
	})

	t.Run("channel trends without any record", func(t *testing.T) {
		// The reference time here is time.Now(), which is the case a local-time reference breaks. The
		// upstream variant of this case needs a configured channel without logs, which GetUpstreamDetail
		// rejects before it reaches the timeline.
		trends, err := st.GetChannelUsageTrends("channel-empty", since, bucketSize, bucketCount)
		if err != nil {
			t.Fatalf("GetChannelUsageTrends(empty) error = %v", err)
		}
		assertBucketGrid(t, "GetChannelUsageTrends(empty)", trendStarts(trends), bucketSize, bucketCount)
	})

	t.Run("channel trends batch", func(t *testing.T) {
		batched, err := st.GetChannelUsageTrendsBatch(since, bucketSize, bucketCount)
		if err != nil {
			t.Fatalf("GetChannelUsageTrendsBatch() error = %v", err)
		}
		series, ok := batched["channel-a"]
		if !ok {
			t.Fatalf("GetChannelUsageTrendsBatch() has no series for channel-a: %#v", batched)
		}
		assertBucketGrid(t, "GetChannelUsageTrendsBatch", trendStarts(series), bucketSize, bucketCount)
	})

	t.Run("model detail trends", func(t *testing.T) {
		detail, err := st.GetModelDetailAnalytics("gpt-5", since, now.Truncate(24*time.Hour), bucketSize, bucketCount)
		if err != nil {
			t.Fatalf("GetModelDetailAnalytics() error = %v", err)
		}
		assertBucketGrid(t, "GetModelDetailAnalytics", trendStarts(detail.Trends), bucketSize, bucketCount)
	})

	t.Run("upstream timeline", func(t *testing.T) {
		detail, err := st.GetUpstreamDetail("channel-a", since, "", 10, bucketSize, bucketCount)
		if err != nil {
			t.Fatalf("GetUpstreamDetail() error = %v", err)
		}
		starts := make([]time.Time, 0, len(detail.Timeline))
		for _, item := range detail.Timeline {
			starts = append(starts, item.Time)
		}
		assertBucketGrid(t, "GetUpstreamDetail", starts, bucketSize, bucketCount)
	})

}

func trendStarts(trends []UsageTrendRecord) []time.Time {
	starts := make([]time.Time, 0, len(trends))
	for _, trend := range trends {
		starts = append(starts, trend.Time)
	}
	return starts
}

func assertBucketGrid(t *testing.T, label string, starts []time.Time, bucketSize time.Duration, want int) {
	t.Helper()
	if len(starts) != want {
		t.Fatalf("%s returned %d buckets, want %d", label, len(starts), want)
	}
	for index, start := range starts {
		if start.Location() != time.UTC {
			t.Fatalf("%s bucket %d = %s in location %q, want UTC: a non-UTC bucket key never matches a UTC-truncated recorded_at",
				label, index, start, start.Location())
		}
		if !start.Equal(start.Truncate(bucketSize)) {
			t.Fatalf("%s bucket %d = %s is not on a %s boundary", label, index, start, bucketSize)
		}
	}
	for index := 1; index < len(starts); index++ {
		if got := starts[index].Sub(starts[index-1]); got != bucketSize {
			t.Fatalf("%s bucket %d - %d = %s, want %s", label, index, index-1, got, bucketSize)
		}
	}
}
