package store

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/pkg/recordfile"
)

func BenchmarkUpsertLogWithGrouping(b *testing.B) {
	st, err := New(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()

	traceDir := b.TempDir()
	header := benchmarkStoreHeader()
	grouping := GroupingInfo{
		SessionID:       "bench-session",
		SessionSource:   "codex",
		WindowID:        "bench-session:0",
		ClientRequestID: "bench-request",
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		path := filepath.Join(traceDir, fmt.Sprintf("trace-%d.http", i))
		if err := os.WriteFile(path, []byte("POST /v1/responses HTTP/1.1\r\nHost: example.com\r\n\r\n{}\nHTTP/1.1 200 OK\r\n\r\n{}"), 0o644); err != nil {
			b.Fatal(err)
		}
		header.Meta.RequestID = fmt.Sprintf("bench-%d", i)
		if err := st.UpsertLogWithGrouping(path, header, grouping); err != nil {
			b.Fatal(err)
		}
	}
}

func benchmarkStoreHeader() recordfile.RecordHeader {
	return recordfile.RecordHeader{
		Version: "LLM_PROXY_V3",
		Meta: recordfile.MetaData{
			RequestID:               "bench",
			Time:                    time.Date(2026, 4, 24, 10, 0, 0, 0, time.UTC),
			Model:                   "gpt-5",
			Provider:                "openai",
			Operation:               "responses",
			Endpoint:                "/v1/responses",
			URL:                     "/v1/responses",
			Method:                  "POST",
			StatusCode:              200,
			DurationMs:              1200,
			TTFTMs:                  120,
			ContentLength:           256,
			SelectedUpstreamID:      "default",
			SelectedUpstreamBaseURL: "https://api.openai.com/v1",
			RoutingPolicy:           "p2c",
			RoutingScore:            0.1,
			RoutingCandidateCount:   1,
		},
		Layout: recordfile.LayoutInfo{
			ReqHeaderLen: 64,
			ReqBodyLen:   2,
			ResHeaderLen: 32,
			ResBodyLen:   2,
		},
		Usage: recordfile.UsageInfo{
			PromptTokens:     16,
			CompletionTokens: 8,
			TotalTokens:      24,
		},
	}
}

// BenchmarkSyncSingleSessionVault measures a full vault walk that indexes many
// recordings of one session: the derived tables are rebuilt from whole sessions
// and hour buckets, so this is the shape that pays for a per-file refresh.
func BenchmarkSyncSingleSessionVault(b *testing.B) {
	const files = 200
	dir := b.TempDir()
	base := time.Date(2026, 4, 16, 8, 0, 0, 0, time.UTC)
	for i := 0; i < files; i++ {
		writeSyncCassetteForTest(
			b,
			dir,
			fmt.Sprintf("trace-%03d.http", i),
			"bench-session",
			base.Add(time.Duration(i)*time.Second),
			200,
			20,
			10,
			true,
		)
	}

	st, err := New(dir)
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		if err := st.Reset(); err != nil {
			b.Fatal(err)
		}
		for _, table := range []string{"overview_metric_bucket_members", "overview_metric_buckets", "session_summaries"} {
			if _, err := st.db.Exec(`DELETE FROM ` + table); err != nil {
				b.Fatal(err)
			}
		}
		b.StartTimer()

		if err := st.Sync(); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkModelCatalogUsageSummaries measures the aggregate the model catalog
// page needs per catalog entry: the per-key form issues one query per model per
// window, the grouped form issues one query per window.
func BenchmarkModelCatalogUsageSummaries(b *testing.B) {
	st, err := New(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()

	const models = 40
	const logsPerModel = 8
	dir := b.TempDir()
	now := time.Now().UTC()
	header := benchmarkStoreHeader()
	for m := 0; m < models; m++ {
		model := fmt.Sprintf("bench-model-%02d", m)
		for i := 0; i < logsPerModel; i++ {
			path := filepath.Join(dir, fmt.Sprintf("%s-%d.http", model, i))
			if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
				b.Fatal(err)
			}
			header.Meta.RequestID = fmt.Sprintf("%s-%d", model, i)
			header.Meta.Model = model
			header.Meta.Time = now.Add(-time.Duration(i) * time.Minute)
			header.Usage.TotalTokens = 10
			if err := st.UpsertLog(path, header); err != nil {
				b.Fatal(err)
			}
		}
	}
	since := now.Add(-24 * time.Hour)
	names := make([]string, 0, models)
	for m := 0; m < models; m++ {
		names = append(names, fmt.Sprintf("bench-model-%02d", m))
	}

	b.Run("single-key", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			for _, name := range names {
				if _, err := st.usageSummary("model = ?", []any{name}, since); err != nil {
					b.Fatal(err)
				}
			}
		}
	})
	b.Run("grouped", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := st.usageSummariesByModel(since); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkChannelUsageAnalytics measures what the channel list page needs per
// configured channel: the per-channel form asks for one summary and one trend
// series per channel, the batched form asks for both in one grouped pass each.
func BenchmarkChannelUsageAnalytics(b *testing.B) {
	st, err := New(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()

	const channels = 12
	const logsPerChannel = 20
	dir := b.TempDir()
	now := time.Now().UTC()
	header := benchmarkStoreHeader()
	for c := 0; c < channels; c++ {
		channelID := fmt.Sprintf("bench-channel-%02d", c)
		for i := 0; i < logsPerChannel; i++ {
			path := filepath.Join(dir, fmt.Sprintf("%s-%d.http", channelID, i))
			if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
				b.Fatal(err)
			}
			header.Meta.RequestID = fmt.Sprintf("%s-%d", channelID, i)
			header.Meta.Model = fmt.Sprintf("bench-model-%d", i%4)
			header.Meta.Time = now.Add(-time.Duration(i%10) * time.Hour)
			header.Meta.SelectedUpstreamID = channelID
			header.Usage.TotalTokens = 10
			if err := st.UpsertLog(path, header); err != nil {
				b.Fatal(err)
			}
		}
	}
	since := now.Add(-24 * time.Hour)
	ids := make([]string, 0, channels)
	for c := 0; c < channels; c++ {
		ids = append(ids, fmt.Sprintf("bench-channel-%02d", c))
	}

	b.Run("per-channel", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			for _, channelID := range ids {
				if _, err := st.GetChannelUsageSummary(channelID, since); err != nil {
					b.Fatal(err)
				}
				if _, err := st.GetChannelUsageTrends(channelID, since, time.Hour, 24); err != nil {
					b.Fatal(err)
				}
			}
		}
	})
	b.Run("batched", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := st.GetChannelUsageSummaries(since); err != nil {
				b.Fatal(err)
			}
			if _, err := st.GetChannelUsageTrendsBatch(since, time.Hour, 24); err != nil {
				b.Fatal(err)
			}
		}
	})
}
