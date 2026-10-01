package store

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/pkg/observe"
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
func BenchmarkOverviewTimeline(b *testing.B) {
	st, err := New(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()

	// 2,000 rows spread over the 24 hourly buckets the dashboard renders, which is
	// the row count the timeline loop scans, parses and buckets in Go.
	const rows = 2000
	dir := b.TempDir()
	now := time.Now().UTC()
	header := benchmarkStoreHeader()
	for i := 0; i < rows; i++ {
		path := filepath.Join(dir, fmt.Sprintf("timeline-%05d.http", i))
		if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
			b.Fatal(err)
		}
		header.Meta.RequestID = fmt.Sprintf("timeline-%05d", i)
		header.Meta.Time = now.Add(-time.Duration(i%24) * time.Hour)
		header.Meta.StatusCode = 200
		header.Meta.Error = ""
		if i%20 == 0 {
			header.Meta.StatusCode = 500
			header.Meta.Error = "upstream error"
		}
		header.Usage.TotalTokens = 42
		if err := st.UpsertLog(path, header); err != nil {
			b.Fatal(err)
		}
	}
	whereSQL, whereArgs := overviewLogWhere(now.Add(-24 * time.Hour))
	opts := OverviewOptions{BucketSize: time.Hour, BucketCount: 24}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		items, err := st.overviewTimeline(whereSQL, whereArgs, opts)
		if err != nil {
			b.Fatal(err)
		}
		if len(items) != 24 {
			b.Fatalf("len(items) = %d, want 24", len(items))
		}
	}
}

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

// BenchmarkUpstreamAnalytics measures what the analytics page needs per upstream:
// the per-upstream form ranks the models, the last model, the recent errors and
// the recent failures once per upstream, the batched form once for all of them.
func BenchmarkUpstreamAnalytics(b *testing.B) {
	st, err := New(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()

	const upstreams = 8
	const logsPerUpstream = 30
	dir := b.TempDir()
	now := time.Now().UTC()
	header := benchmarkStoreHeader()
	for u := 0; u < upstreams; u++ {
		upstreamID := fmt.Sprintf("bench-upstream-%02d", u)
		for i := 0; i < logsPerUpstream; i++ {
			path := filepath.Join(dir, fmt.Sprintf("%s-%d.http", upstreamID, i))
			if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
				b.Fatal(err)
			}
			header.Meta.RequestID = fmt.Sprintf("%s-%d", upstreamID, i)
			header.Meta.Model = fmt.Sprintf("bench-model-%d", i%5)
			header.Meta.Time = now.Add(-time.Duration(i) * time.Minute)
			header.Meta.StatusCode = 200
			if i%4 == 0 {
				header.Meta.StatusCode = 500
			}
			header.Meta.SelectedUpstreamID = upstreamID
			header.Usage.TotalTokens = 10
			if err := st.UpsertLog(path, header); err != nil {
				b.Fatal(err)
			}
		}
	}
	since := now.Add(-24 * time.Hour)
	ids := make([]string, 0, upstreams)
	for u := 0; u < upstreams; u++ {
		ids = append(ids, fmt.Sprintf("bench-upstream-%02d", u))
	}

	b.Run("per-upstream", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			for _, upstreamID := range ids {
				if _, _, err := st.upstreamModelCoverage(upstreamID, 5, since, ""); err != nil {
					b.Fatal(err)
				}
				if _, err := st.upstreamRecentErrors(upstreamID, 3, since, ""); err != nil {
					b.Fatal(err)
				}
				if _, err := st.upstreamRecentFailures(upstreamID, 3, since, ""); err != nil {
					b.Fatal(err)
				}
			}
		}
	})
	b.Run("batched", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := st.upstreamModelCoverageAll(5, since, ""); err != nil {
				b.Fatal(err)
			}
			if _, err := st.upstreamRecentErrorsAll(3, since, ""); err != nil {
				b.Fatal(err)
			}
			if _, err := st.upstreamRecentFailuresAll(3, since, ""); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkUpsertChannelModels measures a model re-discovery, which rewrites the
// channel model rows, their catalog entries and reads the stored rows back. The
// per-model arm is the upsert loop the discovery used, the batched arm the
// grouped write.
func BenchmarkUpsertChannelModels(b *testing.B) {
	st, err := New(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()

	const channelID = "bench-channel"
	const models = 60
	now := time.Now().UTC()
	records := make([]ChannelModelRecord, 0, models)
	for i := 0; i < models; i++ {
		records = append(records, ChannelModelRecord{
			Model:       fmt.Sprintf("bench-model-%03d", i),
			DisplayName: fmt.Sprintf("Bench Model %03d", i),
			Source:      "discovered",
			Enabled:     true,
			LastSeenAt:  now,
			LastProbeAt: now,
		})
	}

	b.Run("per-model", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			for _, record := range records {
				if _, err := st.UpsertChannelModel(channelID, record); err != nil {
					b.Fatal(err)
				}
			}
		}
	})
	b.Run("batched", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := st.UpsertChannelModels(channelID, records); err != nil {
				b.Fatal(err)
			}
		}
	})
}

// BenchmarkSaveFindings measures a session reanalysis write. The row-by-row form
// this replaced issued one INSERT per finding (120 statements here); the batched
// form issues one DELETE plus one INSERT per 69-row parameter chunk.
func BenchmarkSaveFindings(b *testing.B) {
	st, err := New(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()

	const findings = 120
	items := make([]observe.Finding, 0, findings)
	for i := 0; i < findings; i++ {
		items = append(items, observe.Finding{
			ID:              fmt.Sprintf("finding-%03d", i),
			Category:        "bench",
			Severity:        observe.SeverityLow,
			Confidence:      0.5,
			Title:           fmt.Sprintf("finding %03d", i),
			EvidenceExcerpt: "evidence",
			Detector:        "bench",
			DetectorVersion: "1",
		})
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := st.SaveFindings("bench-trace-findings", items); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkSaveObservationNodes measures the semantic-node half of a reparse. The
// row-by-row form this replaced issued one INSERT per node (200 statements here);
// the batched form issues one INSERT per 64-node parameter chunk.
func BenchmarkSaveObservationNodes(b *testing.B) {
	st, err := New(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer st.Close()

	const nodes = 200
	roots := make([]observe.SemanticNode, 0, nodes)
	for i := 0; i < nodes; i++ {
		roots = append(roots, observe.SemanticNode{
			ID:             fmt.Sprintf("node-%03d", i),
			ProviderType:   "message",
			NormalizedType: observe.NodeMessage,
			Role:           "assistant",
			Path:           fmt.Sprintf("$.choices[%d].message", i),
			Index:          i,
			Text:           fmt.Sprintf("node text %03d", i),
		})
	}
	obs := observe.TraceObservation{
		TraceID:       "bench-trace-nodes",
		Provider:      "openai_compatible",
		Operation:     "chat.completions",
		Model:         "gpt-test",
		Parser:        "bench",
		ParserVersion: "1",
		Status:        observe.ParseStatusParsed,
		Response:      observe.ObservationResponse{Nodes: roots},
	}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := st.SaveObservation(obs); err != nil {
			b.Fatal(err)
		}
	}
}
