package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kingfs/Trajecta/pkg/secretmask"
	bolt "go.etcd.io/bbolt"
)

func adminTestPair(tag string, realFill string, fakeFill string) secretmask.Mapping {
	return secretmask.Mapping{
		Real: tag + strings.Repeat(realFill, 64),
		Fake: tag + strings.Repeat(fakeFill, 64),
	}
}

// seedAdminTestMappings appends pairs to a fresh mapping file and closes it, so
// the administrative tests reopen a file that is already on disk.
func seedAdminTestMappings(t *testing.T, st *Store, pairs []secretmask.Mapping) string {
	t.Helper()
	ms, path := openTestSecretMappingStore(t, st, st.OutputDir())
	for _, pair := range pairs {
		if err := ms.AppendMapping(pair); err != nil {
			t.Fatalf("AppendMapping(%q) error = %v", pair.Real, err)
		}
	}
	if err := ms.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	return path
}

func openAdminTestMappingStore(t *testing.T, st *Store, path string) *SecretMappingStore {
	t.Helper()
	ms, err := st.OpenSecretMappingStore(path)
	if err != nil {
		t.Fatalf("reopen OpenSecretMappingStore() error = %v", err)
	}
	return ms
}

// TestSecretMappingAdminStatsAndIterate pins the read side the stats and list
// commands depend on: counts, the sequence range, and the two iteration
// directions over the same rows.
func TestSecretMappingAdminStatsAndIterate(t *testing.T) {
	st := configTransactionTestStore(t)
	first := adminTestPair("adm-one-", "A", "a")
	second := adminTestPair("adm-two-", "B", "b")
	third := adminTestPair("adm-three-", "C", "c")
	path := seedAdminTestMappings(t, st, []secretmask.Mapping{first, second, third})

	admin := openAdminTestMappingStore(t, st, path)
	defer admin.Close()

	stats, err := admin.Stats()
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	if !stats.FileExists || stats.FileSizeBytes <= 0 {
		t.Fatalf("Stats() file = exists=%v size=%d, want an existing non-empty file", stats.FileExists, stats.FileSizeBytes)
	}
	if stats.EntryCount != 3 || stats.IndexCount != 3 {
		t.Fatalf("Stats() entries/index = %d/%d, want 3/3", stats.EntryCount, stats.IndexCount)
	}
	if stats.MinSeq != 1 || stats.MaxSeq != 3 || stats.NextSeq != 3 {
		t.Fatalf("Stats() seq range = %d..%d last=%d, want 1..3 last=3", stats.MinSeq, stats.MaxSeq, stats.NextSeq)
	}
	if stats.UnreadableRows != 0 || stats.DanglingIndexEntries != 0 {
		t.Fatalf("Stats() unreadable/dangling = %d/%d, want 0/0", stats.UnreadableRows, stats.DanglingIndexEntries)
	}
	if st.SecretStorageMode() == "encrypted-local" {
		if stats.EncryptedRows != 3 || stats.PlaintextRows != 0 {
			t.Fatalf("Stats() encrypted/plaintext = %d/%d, want 3/0", stats.EncryptedRows, stats.PlaintextRows)
		}
	}

	var oldestOrder []uint64
	if err := admin.IterateMappings(false, func(entry SecretMappingEntry) error {
		oldestOrder = append(oldestOrder, entry.Seq)
		return nil
	}); err != nil {
		t.Fatalf("IterateMappings(oldest) error = %v", err)
	}
	if len(oldestOrder) != 3 || oldestOrder[0] != 1 || oldestOrder[2] != 3 {
		t.Fatalf("oldest-first order = %v, want [1 2 3]", oldestOrder)
	}

	var newestOrder []uint64
	if err := admin.IterateMappings(true, func(entry SecretMappingEntry) error {
		newestOrder = append(newestOrder, entry.Seq)
		return nil
	}); err != nil {
		t.Fatalf("IterateMappings(newest) error = %v", err)
	}
	if len(newestOrder) != 3 || newestOrder[0] != 3 || newestOrder[2] != 1 {
		t.Fatalf("newest-first order = %v, want [3 2 1]", newestOrder)
	}

	visited := 0
	if err := admin.IterateMappings(false, func(entry SecretMappingEntry) error {
		visited++
		return ErrStopSecretMappingIteration
	}); err != nil {
		t.Fatalf("IterateMappings(stop) error = %v", err)
	}
	if visited != 1 {
		t.Fatalf("IterateMappings(stop) visited %d rows, want 1", visited)
	}
}

// TestSecretMappingAdminDeletes pins the three selectors and, importantly, that
// the by_real index is cleaned with the row so the file does not accumulate
// dangling index entries.
func TestSecretMappingAdminDeletes(t *testing.T) {
	st := configTransactionTestStore(t)
	one := adminTestPair("adm-del-one-", "A", "a")
	two := adminTestPair("adm-del-two-", "B", "b")
	three := adminTestPair("adm-del-three-", "C", "c")
	four := adminTestPair("adm-del-four-", "D", "d")
	path := seedAdminTestMappings(t, st, []secretmask.Mapping{one, two, three, four})

	admin := openAdminTestMappingStore(t, st, path)
	defer admin.Close()

	deleted, found, err := admin.DeleteMappingBySeq(2)
	if err != nil || !found {
		t.Fatalf("DeleteMappingBySeq(2) = found=%v err=%v, want found", found, err)
	}
	if deleted.Mapping.Real != two.Real {
		t.Fatalf("DeleteMappingBySeq(2) real = %q, want the second pair", deleted.Mapping.Real)
	}
	if _, found, err := admin.LookupMapping(two.Real); err != nil || found {
		t.Fatalf("LookupMapping(deleted) = found=%v err=%v, want not found", found, err)
	}

	deleted, found, err = admin.DeleteMappingByReal(three.Real)
	if err != nil || !found {
		t.Fatalf("DeleteMappingByReal() = found=%v err=%v, want found", found, err)
	}
	if deleted.Seq != 3 {
		t.Fatalf("DeleteMappingByReal() seq = %d, want 3", deleted.Seq)
	}

	removed, err := admin.DeleteMappingsMatching(func(m secretmask.Mapping) bool {
		return strings.Contains(m.Real, "adm-del-four-")
	})
	if err != nil {
		t.Fatalf("DeleteMappingsMatching() error = %v", err)
	}
	if len(removed) != 1 || removed[0].Mapping.Real != four.Real {
		t.Fatalf("DeleteMappingsMatching() removed %+v, want only the fourth pair", removed)
	}

	stats, err := admin.Stats()
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	if stats.EntryCount != 1 || stats.IndexCount != 1 {
		t.Fatalf("Stats() entries/index after deletes = %d/%d, want 1/1", stats.EntryCount, stats.IndexCount)
	}
	if stats.DanglingIndexEntries != 0 {
		t.Fatalf("Stats() dangling = %d, want 0 after deleting through the API", stats.DanglingIndexEntries)
	}
	if _, found, err := admin.LookupMapping(one.Real); err != nil || !found {
		t.Fatalf("LookupMapping(kept) = found=%v err=%v, want the surviving pair", found, err)
	}

	if _, found, err := admin.DeleteMappingBySeq(99); err != nil || found {
		t.Fatalf("DeleteMappingBySeq(missing) = found=%v err=%v, want not found", found, err)
	}
	if _, found, err := admin.DeleteMappingByReal("no-such-real"); err != nil || found {
		t.Fatalf("DeleteMappingByReal(missing) = found=%v err=%v, want not found", found, err)
	}
	if _, err := admin.DeleteMappingsMatching(nil); err == nil {
		t.Fatal("DeleteMappingsMatching(nil) error = nil, want a rejection")
	}
}

// TestSecretMappingAdminDeleteByRealWithoutIndex covers the fallback scan: a
// missing index entry must not make the row undeletable, because the row is the
// source of truth and the index is only a cache.
func TestSecretMappingAdminDeleteByRealWithoutIndex(t *testing.T) {
	st := configTransactionTestStore(t)
	pair := adminTestPair("adm-noindex-", "A", "a")
	path := seedAdminTestMappings(t, st, []secretmask.Mapping{pair})

	admin := openAdminTestMappingStore(t, st, path)
	defer admin.Close()

	if err := admin.db.Update(func(tx *bolt.Tx) error {
		index := tx.Bucket([]byte(secretMappingIndexBucket))
		if index == nil {
			t.Fatal("by_real bucket is missing")
		}
		return index.Delete(admin.indexKey(pair.Real))
	}); err != nil {
		t.Fatalf("remove index entry error = %v", err)
	}

	if _, found, err := admin.DeleteMappingByReal(pair.Real); err != nil || !found {
		t.Fatalf("DeleteMappingByReal() without index = found=%v err=%v, want found via the fallback scan", found, err)
	}
	if _, found, err := admin.LookupMapping(pair.Real); err != nil || found {
		t.Fatalf("LookupMapping(deleted) = found=%v err=%v, want not found", found, err)
	}
}

// TestSecretMappingAdminCompact pins that compaction keeps every live pair,
// writes a backup and leaves a file the runtime can reopen.
func TestSecretMappingAdminCompact(t *testing.T) {
	st := configTransactionTestStore(t)
	pairs := make([]secretmask.Mapping, 0, 200)
	for i := 0; i < 200; i++ {
		prefix := "adm-keep-"
		if i < 150 {
			prefix = "adm-compact-drop-"
		}
		pairs = append(pairs, secretmask.Mapping{
			Real: prefix + fmt.Sprintf("%04d", i) + strings.Repeat("R", 40),
			Fake: prefix + fmt.Sprintf("%04d", i) + strings.Repeat("F", 40),
		})
	}
	path := seedAdminTestMappings(t, st, pairs)

	admin := openAdminTestMappingStore(t, st, path)
	removed, err := admin.DeleteMappingsMatching(func(m secretmask.Mapping) bool {
		return strings.HasPrefix(m.Real, "adm-compact-drop-")
	})
	if err != nil {
		t.Fatalf("DeleteMappingsMatching() error = %v", err)
	}
	if len(removed) != 150 {
		t.Fatalf("removed %d pairs, want 150", len(removed))
	}
	if err := admin.Close(); err != nil {
		t.Fatalf("Close() before compact error = %v", err)
	}

	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(before) error = %v", err)
	}
	result, err := CompactSecretMappingFile(path)
	if err != nil {
		t.Fatalf("CompactSecretMappingFile() error = %v", err)
	}
	if result.Entries != 50 || result.IndexEntries != 50 {
		t.Fatalf("compact copied entries/index = %d/%d, want 50/50", result.Entries, result.IndexEntries)
	}
	if result.FileSizeBefore != before.Size() || result.FileSizeAfter <= 0 {
		t.Fatalf("compact sizes before/after = %d/%d, want before=%d and a non-empty result", result.FileSizeBefore, result.FileSizeAfter, before.Size())
	}
	backup, err := os.Stat(result.BackupPath)
	if err != nil {
		t.Fatalf("Stat(backup) error = %v", err)
	}
	if backup.Size() != before.Size() {
		t.Fatalf("backup size = %d, want the original file size %d", backup.Size(), before.Size())
	}

	reopened := openAdminTestMappingStore(t, st, path)
	defer reopened.Close()
	stats, err := reopened.Stats()
	if err != nil {
		t.Fatalf("Stats(after compact) error = %v", err)
	}
	if stats.EntryCount != 50 || stats.IndexCount != 50 || stats.UnreadableRows != 0 || stats.DanglingIndexEntries != 0 {
		t.Fatalf("stats after compact = %+v, want 50 readable entries with a clean index", stats)
	}
	if stats.MaxSeq != 200 || stats.NextSeq != 200 {
		t.Fatalf("sequence range after compact = max %d last %d, want 200/200", stats.MaxSeq, stats.NextSeq)
	}

	// A second run must refuse rather than clobber the backup it just wrote.
	if _, err := CompactSecretMappingFile(path); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second CompactSecretMappingFile() error = %v, want an existing-backup rejection", err)
	}
}

// TestSecretMappingAdminLockReportsStopServe pins the message the CLI shows when
// serve is running: a second open must be recognisable as the exclusive lock, and
// compaction must report the same condition.
func TestSecretMappingAdminLockReportsStopServe(t *testing.T) {
	st := configTransactionTestStore(t)
	path := seedAdminTestMappings(t, st, []secretmask.Mapping{adminTestPair("adm-lock-", "A", "a")})

	admin := openAdminTestMappingStore(t, st, path)
	defer admin.Close()

	if _, err := st.OpenSecretMappingStore(path); err == nil {
		t.Fatal("second OpenSecretMappingStore() error = nil, want the bbolt lock timeout")
	} else if !IsSecretMappingStoreLocked(err) {
		t.Fatalf("second open error = %v, want IsSecretMappingStoreLocked", err)
	}
	if _, err := CompactSecretMappingFile(path); err == nil || !IsSecretMappingStoreLocked(err) {
		t.Fatalf("CompactSecretMappingFile() while locked error = %v, want IsSecretMappingStoreLocked", err)
	}
}

// TestSecretMappingFingerprintIsKeyed pins the redaction contract the CLI
// relies on: the fingerprint printed for a real value must be keyed, not a
// plain SHA-256 of a low-entropy value. Two stores with different local keys
// must disagree on the same value, and one store must be stable.
func TestSecretMappingFingerprintIsKeyed(t *testing.T) {
	t.Parallel()

	openStore := func(t *testing.T) (*SecretMappingStore, string) {
		t.Helper()
		outputDir := t.TempDir()
		st, err := New(outputDir)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		t.Cleanup(func() { _ = st.Close() })
		ms, err := st.OpenSecretMappingStore(filepath.Join(outputDir, SecretMappingFileName))
		if err != nil {
			t.Fatalf("OpenSecretMappingStore() error = %v", err)
		}
		t.Cleanup(func() { _ = ms.Close() })
		return ms, outputDir
	}

	first, _ := openStore(t)
	second, _ := openStore(t)
	const real = "13800138000"
	got := first.Fingerprint(real)
	if got == "" || len(got) != 16 {
		t.Fatalf("Fingerprint() = %q, want 16 hex characters", got)
	}
	if again := first.Fingerprint(real); again != got {
		t.Fatalf("Fingerprint() is not stable: %q vs %q", got, again)
	}
	if other := second.Fingerprint(real); other == got {
		t.Fatalf("two local keys produced the same fingerprint %q; the digest is not keyed", got)
	}
	plain := sha256.Sum256([]byte(real))
	if got == hex.EncodeToString(plain[:8]) {
		t.Fatal("Fingerprint() equals the plain SHA-256 prefix")
	}
}

// TestDeleteCredentialMaskAuditsSQLite pins the purge path: only the filtered
// rows are deleted, and the system_events notification is left in place.
func TestDeleteCredentialMaskAuditsSQLite(t *testing.T) {
	st := configTransactionTestStore(t)
	base := time.Date(2026, 11, 1, 8, 0, 0, 0, time.UTC)
	records := []CredentialMaskAuditRecord{
		{TraceID: "trace-purge-a", EventIndex: 0, Rule: "env.secret_assignment", Kind: "api_key", Original: "real-a", StandIn: "fake-a", DetectedAt: base},
		{TraceID: "trace-purge-b", EventIndex: 0, Rule: "env.secret_assignment", Kind: "api_key", Original: "real-b", StandIn: "fake-b", DetectedAt: base.Add(time.Hour)},
		{TraceID: "trace-purge-c", EventIndex: 0, Rule: "openai.project_key", Kind: "api_key", Original: "real-c", StandIn: "fake-c", DetectedAt: base.Add(2 * time.Hour)},
	}
	if err := st.UpsertCredentialMaskAudits(records); err != nil {
		t.Fatalf("UpsertCredentialMaskAudits() error = %v", err)
	}
	var eventsBefore int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM system_events`).Scan(&eventsBefore); err != nil {
		t.Fatalf("count system_events error = %v", err)
	}

	deleted, err := st.DeleteCredentialMaskAudits(CredentialMaskAuditFilter{
		Rule:  "env.secret_assignment",
		Since: base.Add(-time.Minute),
		Until: base.Add(30 * time.Minute),
	})
	if err != nil {
		t.Fatalf("DeleteCredentialMaskAudits() error = %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want the one row inside the window", deleted)
	}
	page, err := st.ListCredentialMaskAudits(CredentialMaskAuditFilter{})
	if err != nil {
		t.Fatalf("ListCredentialMaskAudits() error = %v", err)
	}
	if page.Total != 2 {
		t.Fatalf("remaining audit rows = %d, want 2", page.Total)
	}
	var eventsAfter int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM system_events`).Scan(&eventsAfter); err != nil {
		t.Fatalf("count system_events after error = %v", err)
	}
	if eventsAfter != eventsBefore {
		t.Fatalf("system_events count = %d -> %d, want the notification rows untouched", eventsBefore, eventsAfter)
	}
	if _, err := st.DeleteCredentialMaskAudits(CredentialMaskAuditFilter{}); err == nil {
		t.Fatal("DeleteCredentialMaskAudits(empty filter) error = nil, want a refusal")
	}
}

// TestDeleteCredentialMaskAuditsPostgres keeps the purge statement honest on
// Postgres, where the store rebinds the "?" placeholders.
func TestDeleteCredentialMaskAuditsPostgres(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TRAJECTA_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("set TRAJECTA_TEST_POSTGRES_DSN to a disposable Postgres test database DSN")
	}
	dir := t.TempDir()
	st, err := NewWithDatabaseOptions(dir, "postgres", dsn, 4, 4, DatabaseOptions{AutoMigrate: false})
	if err != nil {
		t.Fatalf("NewWithDatabaseOptions(postgres) error = %v", err)
	}
	defer st.Close()

	marker := "purgepg" + time.Now().UTC().Format("20060102150405.000000000")
	record := CredentialMaskAuditRecord{
		TraceID:    marker,
		EventIndex: 0,
		Rule:       marker + ".rule",
		Kind:       "api_key",
		Original:   marker + "-real",
		StandIn:    marker + "-fake",
		DetectedAt: time.Now().UTC(),
	}
	if err := st.UpsertCredentialMaskAudits([]CredentialMaskAuditRecord{record}); err != nil {
		t.Fatalf("UpsertCredentialMaskAudits(postgres) error = %v", err)
	}
	deleted, err := st.DeleteCredentialMaskAudits(CredentialMaskAuditFilter{TraceID: marker})
	if err != nil {
		t.Fatalf("DeleteCredentialMaskAudits(postgres) error = %v", err)
	}
	if deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted)
	}
	page, err := st.ListCredentialMaskAudits(CredentialMaskAuditFilter{TraceID: marker})
	if err != nil {
		t.Fatalf("ListCredentialMaskAudits(postgres) error = %v", err)
	}
	if page.Total != 0 {
		t.Fatalf("remaining rows = %d, want 0", page.Total)
	}
}

// TestSecretMappingAdminDeleteKeepsUnreadableRows pins the reporting limit: a
// row that cannot be decrypted is visible to by-value deletion as unreadable and
// stays until it is removed by sequence.
func TestSecretMappingAdminDeleteKeepsUnreadableRows(t *testing.T) {
	st := configTransactionTestStore(t)
	if st.SecretStorageMode() != "encrypted-local" {
		t.Skipf("secret storage mode is %q, not encrypted-local", st.SecretStorageMode())
	}
	pair := adminTestPair("adm-unreadable-", "A", "a")
	path := seedAdminTestMappings(t, st, []secretmask.Mapping{pair})

	admin := openAdminTestMappingStore(t, st, path)
	defer admin.Close()

	if err := admin.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(secretMappingBucket))
		if bucket == nil {
			return errors.New("mappings bucket is missing")
		}
		return bucket.Put(seqKey(1), []byte("tlsec:v1:not-a-real-envelope"))
	}); err != nil {
		t.Fatalf("corrupt mapping row error = %v", err)
	}

	stats, err := admin.Stats()
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	if stats.UnreadableRows != 1 {
		t.Fatalf("unreadable rows = %d, want 1", stats.UnreadableRows)
	}
	if _, found, err := admin.DeleteMappingByReal(pair.Real); err != nil || found {
		t.Fatalf("DeleteMappingByReal(unreadable) = found=%v err=%v, want no match", found, err)
	}
	if _, found, err := admin.DeleteMappingBySeq(1); err != nil || !found {
		t.Fatalf("DeleteMappingBySeq(unreadable) = found=%v err=%v, want deleted by sequence", found, err)
	}
}
