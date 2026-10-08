package trajectory

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCacheKeyChangesWithAppendOnlySession(t *testing.T) {
	base := CacheKey("session-1", "trace-100", 100, 500)
	if base != CacheKey("session-1", "trace-100", 100, 500) {
		t.Fatal("cache key is not stable for the same session state")
	}
	if base == CacheKey("session-1", "trace-101", 101, 500) {
		t.Fatal("appending a trace must change the cache key")
	}
	if base == CacheKey("session-1", "trace-100", 101, 500) {
		t.Fatal("a changed trace count must change the cache key")
	}
	if base == CacheKey("session-1", "trace-100", 100, 0) {
		t.Fatal("the limit must be part of the cache key so full exports never reuse a capped entry")
	}
	if base == CacheKey("session-2", "trace-100", 100, 500) {
		t.Fatal("the session id must be part of the cache key")
	}
}

func TestCacheStoreLoadAndMiss(t *testing.T) {
	dir := t.TempDir()
	cache := NewCache(dir)
	key := CacheKey("session-1", "trace-1", 1, 500)
	if _, ok := cache.Load(key); ok {
		t.Fatal("empty cache reported a hit")
	}
	data := []byte(`{"schema_version":"ATIF-v1.8"}`)
	if err := cache.Store(key, data); err != nil {
		t.Fatal(err)
	}
	got, ok := cache.Load(key)
	if !ok || string(got) != string(data) {
		t.Fatalf("Load() = %q, %v", got, ok)
	}
	if _, ok := cache.Load(CacheKey("session-1", "trace-2", 2, 500)); ok {
		t.Fatal("a different session state hit the cached entry")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "tmp-") {
			t.Fatalf("temporary file left behind: %s", entry.Name())
		}
	}
}

func TestCacheEvictsBeyondBounds(t *testing.T) {
	dir := t.TempDir()
	cache := &Cache{Dir: dir, MaxEntries: 3}
	for i := 0; i < 10; i++ {
		key := CacheKey("session-1", fmt.Sprintf("trace-%d", i), i, 500)
		if err := cache.Store(key, []byte(`{"step":1}`)); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	kept := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), cacheEntrySuffix) {
			kept++
		}
	}
	if kept > 3 {
		t.Fatalf("cache keeps %d entries, want at most 3", kept)
	}
}

func TestCacheRejectsTruncatedEntry(t *testing.T) {
	dir := t.TempDir()
	cache := NewCache(dir)
	key := CacheKey("session-1", "trace-1", 1, 500)
	if err := os.WriteFile(filepath.Join(dir, key+cacheEntrySuffix), []byte(`{"schema_version":`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, ok := cache.Load(key); ok {
		t.Fatal("a truncated entry was served as a hit")
	}
}
