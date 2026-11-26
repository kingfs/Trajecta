package main

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/recordfile"
)

func TestRunTraceRetentionRemovesExpiredInRootCassetteOnly(t *testing.T) {
	root := t.TempDir()
	traceStore, err := newTestStore(root)
	if err != nil {
		t.Fatalf("newTestStore() error = %v", err)
	}
	defer traceStore.Close()

	oldPath := filepath.Join(root, "old.http")
	recentPath := filepath.Join(root, "recent.http")
	outsidePath := filepath.Join(t.TempDir(), "outside.http")
	for _, path := range []string{oldPath, recentPath, outsidePath} {
		if err := os.WriteFile(path, []byte("cassette payload"), 0o600); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", path, err)
		}
	}
	insert := func(path, requestID string, recordedAt time.Time) {
		t.Helper()
		header := recordfile.RecordHeader{Version: "LLM_PROXY_V3"}
		header.Meta.RequestID = requestID
		header.Meta.Time = recordedAt.UTC()
		header.Meta.Model = "gpt-retention-test"
		header.Meta.URL = "https://api.openai.com/v1/chat/completions"
		header.Meta.Method = http.MethodPost
		header.Meta.StatusCode = http.StatusOK
		if err := traceStore.UpsertLogWithGrouping(path, header, store.GroupingInfo{}); err != nil {
			t.Fatalf("UpsertLogWithGrouping(%q) error = %v", path, err)
		}
	}
	insert(oldPath, "retention-old", time.Now().Add(-10*24*time.Hour))
	insert(recentPath, "retention-recent", time.Now())
	insert(outsidePath, "retention-outside", time.Now().Add(-10*24*time.Hour))

	if err := runTraceRetention(context.Background(), traceStore, root, 7); err != nil {
		t.Fatalf("runTraceRetention() error = %v", err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("expired in-root cassette Stat() error = %v, want not-exist", err)
	}
	if _, err := os.Stat(recentPath); err != nil {
		t.Fatalf("recent cassette was removed: %v", err)
	}
	if _, err := os.Stat(outsidePath); err != nil {
		t.Fatalf("out-of-root cassette was touched: %v", err)
	}
	if _, err := traceStore.GetByRequestID("retention-old"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expired trace GetByRequestID() error = %v, want sql.ErrNoRows", err)
	}
	if _, err := traceStore.GetByRequestID("retention-recent"); err != nil {
		t.Fatalf("recent trace GetByRequestID() error = %v", err)
	}
	if _, err := traceStore.GetByRequestID("retention-outside"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("out-of-root trace GetByRequestID() error = %v, want sql.ErrNoRows", err)
	}
}
