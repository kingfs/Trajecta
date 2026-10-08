package trajectory

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// DefaultCacheDirName is the subdirectory of the configured trace output
// directory that holds built session trajectories. It is deliberately not
// `*.http`, so the cassette indexer ignores it.
const DefaultCacheDirName = "trajectory-cache"

const (
	// DefaultCacheMaxEntries bounds how many session trajectories stay on disk.
	DefaultCacheMaxEntries = 512
	// DefaultCacheMaxBytes bounds the total size of the cache.
	DefaultCacheMaxBytes = int64(512 << 20)
	cacheEntrySuffix     = ".atif.json"
)

// Cache is a bounded on-disk store of built session trajectories.
//
// The key is (session_id, last_trace_id, trace_count, limit). A session only
// ever appends recorded traces, and a log row for it is immutable once written,
// so the newest trace id and the trace count change exactly when a new trace
// would change the reconstructed trajectory. The cache therefore needs no
// invalidation logic: an entry whose key does not match the current session
// state simply misses, and a match proves the trajectory was built from the
// same trace sequence. The `limit` is part of the key so a capped default view
// can never be served for a request that asked for the complete export.
//
// A zero-value (or nil) Cache, or one with an empty Dir, is disabled.
type Cache struct {
	Dir        string
	MaxEntries int
	MaxBytes   int64
}

// NewCache returns a cache rooted at dir. An empty dir disables the cache.
func NewCache(dir string) *Cache {
	return &Cache{Dir: strings.TrimSpace(dir)}
}

// CacheKey derives the collision-resistant file key for one session state.
func CacheKey(sessionID, lastTraceID string, traceCount, limit int) string {
	sum := sha256.Sum256([]byte(sessionID + "\x00" + lastTraceID + "\x00" + strconv.Itoa(traceCount) + "\x00" + strconv.Itoa(limit)))
	return hex.EncodeToString(sum[:])
}

func (c *Cache) path(key string) string {
	return filepath.Join(c.Dir, key+cacheEntrySuffix)
}

func (c *Cache) maxEntries() int {
	if c.MaxEntries > 0 {
		return c.MaxEntries
	}
	return DefaultCacheMaxEntries
}

func (c *Cache) maxBytes() int64 {
	if c.MaxBytes > 0 {
		return c.MaxBytes
	}
	return DefaultCacheMaxBytes
}

// Load returns the cached trajectory bytes for key. A missing, empty or
// obviously truncated entry is a miss, so the caller rebuilds from cassettes.
func (c *Cache) Load(key string) ([]byte, bool) {
	if c == nil || c.Dir == "" || key == "" {
		return nil, false
	}
	data, err := os.ReadFile(c.path(key))
	if err != nil || len(data) < 2 || data[0] != '{' || data[len(data)-1] != '}' {
		return nil, false
	}
	return data, true
}

// Store writes data for key. The write goes to a temporary file in the same
// directory and is then renamed over the destination, so a concurrent reader
// either sees a previous complete entry or the new complete entry, never a
// partial file. The entry is skipped when it alone exceeds the byte budget, and
// the cache is swept afterwards so it cannot grow without limit.
func (c *Cache) Store(key string, data []byte) error {
	if c == nil || c.Dir == "" || key == "" || len(data) == 0 {
		return nil
	}
	if int64(len(data)) > c.maxBytes() {
		return nil
	}
	if err := os.MkdirAll(c.Dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(c.Dir, "tmp-*.atif.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, c.path(key)); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	c.sweep()
	return nil
}

type cacheEntry struct {
	name    string
	size    int64
	modTime time.Time
}

// sweep evicts the least recently written entries until both the entry-count
// and byte budgets hold. It never touches temporary files another writer may be
// filling in; only fully named entries are candidates.
func (c *Cache) sweep() {
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		return
	}
	items := make([]cacheEntry, 0, len(entries))
	var total int64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), cacheEntrySuffix) || strings.HasPrefix(entry.Name(), "tmp-") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		items = append(items, cacheEntry{name: entry.Name(), size: info.Size(), modTime: info.ModTime()})
		total += info.Size()
	}
	maxEntries := c.maxEntries()
	maxBytes := c.maxBytes()
	if len(items) <= maxEntries && total <= maxBytes {
		return
	}
	sort.Slice(items, func(i, j int) bool { return items[i].modTime.After(items[j].modTime) })
	kept := int64(0)
	for i, item := range items {
		if i < maxEntries && kept+item.size <= maxBytes {
			kept += item.size
			continue
		}
		_ = os.Remove(filepath.Join(c.Dir, item.name))
	}
}
