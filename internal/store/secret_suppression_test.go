package store

import (
	"encoding/binary"
	"path/filepath"
	"testing"

	"github.com/kingfs/Trajecta/pkg/secretmask"
	bolt "go.etcd.io/bbolt"
)

// TestSecretSuppressionStoreRoundTrip pins the durable contract the Vault and
// the Monitor rely on: one row per (rule, real), readable after reopen, and a
// delete that also removes the pair index so the same false positive can be
// added again with a fresh ID.
func TestSecretSuppressionStoreRoundTrip(t *testing.T) {
	st := configTransactionTestStore(t)
	ms, path := openTestSecretMappingStore(t, st, st.OutputDir())
	if err := ms.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	admin := openAdminTestMappingStore(t, st, path)
	defer admin.Close()

	first, err := admin.AppendSuppression(secretmask.Suppression{
		Rule:   "openai.project_key",
		Real:   "sk-test-suppression-value",
		Reason: "reviewed false positive",
	})
	if err != nil {
		t.Fatalf("AppendSuppression() error = %v", err)
	}
	if first.ID == 0 || first.Rule != "openai.project_key" || first.CreatedAt.IsZero() {
		t.Fatalf("AppendSuppression() = %+v, want an identified, timestamped row", first)
	}
	duplicate, err := admin.AppendSuppression(secretmask.Suppression{
		Rule:   "openai.project_key",
		Real:   "sk-test-suppression-value",
		Reason: "a second reason",
	})
	if err != nil {
		t.Fatalf("AppendSuppression(duplicate) error = %v", err)
	}
	if duplicate.ID != first.ID || duplicate.Reason != "reviewed false positive" {
		t.Fatalf("duplicate = %+v, want the first row unchanged", duplicate)
	}

	loaded, err := admin.LoadSuppressions()
	if err != nil {
		t.Fatalf("LoadSuppressions() error = %v", err)
	}
	if len(loaded) != 1 || loaded[0].Real != first.Real || loaded[0].Reason != first.Reason {
		t.Fatalf("LoadSuppressions() = %+v, want the one stored row", loaded)
	}

	// A wildcard rule is stored as the empty rule, which is what the Vault uses
	// for "every rule".
	wild, err := admin.AppendSuppression(secretmask.Suppression{Rule: "*", Real: "anything"})
	if err != nil {
		t.Fatalf("AppendSuppression(wildcard) error = %v", err)
	}
	if wild.Rule != "" {
		t.Fatalf("wildcard rule = %q, want empty", wild.Rule)
	}
	var ids []uint64
	if err := admin.IterateSuppressions(true, func(row secretmask.Suppression) error {
		ids = append(ids, row.ID)
		return nil
	}); err != nil {
		t.Fatalf("IterateSuppressions() error = %v", err)
	}
	if len(ids) != 2 || ids[0] != wild.ID || ids[1] != first.ID {
		t.Fatalf("newest-first IDs = %v, want [%d %d]", ids, wild.ID, first.ID)
	}

	removed, found, err := admin.DeleteSuppressionByID(first.ID)
	if err != nil || !found {
		t.Fatalf("DeleteSuppressionByID() = found=%v err=%v, want the row", found, err)
	}
	if removed.Real != first.Real {
		t.Fatalf("DeleteSuppressionByID() removed = %+v", removed)
	}
	if _, found, err := admin.DeleteSuppressionByID(first.ID); err != nil || found {
		t.Fatalf("second DeleteSuppressionByID() = found=%v err=%v, want not found", found, err)
	}

	// The pair index must be gone too, or a re-add would silently reuse a
	// deleted sequence.
	again, err := admin.AppendSuppression(secretmask.Suppression{
		Rule: "openai.project_key",
		Real: first.Real,
	})
	if err != nil {
		t.Fatalf("AppendSuppression(re-add) error = %v", err)
	}
	if again.ID == first.ID {
		t.Fatalf("re-add ID = %d, want a fresh sequence after the delete", again.ID)
	}
	if loaded, err := admin.LoadSuppressions(); err != nil || len(loaded) != 2 {
		t.Fatalf("LoadSuppressions(after re-add) = %+v, err=%v, want two rows", loaded, err)
	}
}

// TestSecretSuppressionSurvivesCompact is the data-loss guard: compaction
// rebuilds the file bucket by bucket, and the suppression buckets are not part
// of the original mapping layout. Forgetting them would silently drop the list
// the next time an operator compacted the file.
func TestSecretSuppressionSurvivesCompact(t *testing.T) {
	st := configTransactionTestStore(t)
	ms, path := openTestSecretMappingStore(t, st, st.OutputDir())
	if _, err := ms.AppendSuppression(secretmask.Suppression{
		Rule:   "env.secret_assignment",
		Real:   "a-value-that-looked-like-a-code-fragment",
		Reason: "the reported false positive",
	}); err != nil {
		t.Fatalf("AppendSuppression() error = %v", err)
	}
	if err := ms.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	if _, err := CompactSecretMappingFile(path); err != nil {
		t.Fatalf("CompactSecretMappingFile() error = %v", err)
	}
	reopened := openAdminTestMappingStore(t, st, filepath.Clean(path))
	defer reopened.Close()
	loaded, err := reopened.LoadSuppressions()
	if err != nil {
		t.Fatalf("LoadSuppressions(after compact) error = %v", err)
	}
	if len(loaded) != 1 || loaded[0].Rule != "env.secret_assignment" || loaded[0].Reason != "the reported false positive" {
		t.Fatalf("LoadSuppressions(after compact) = %+v, want the suppressed entry", loaded)
	}
}

// TestSecretSuppressionBucketsUpgradeLegacyFile pins the additive upgrade: a
// version-1 file created before this feature only has the three mapping buckets.
// Opening it must create the suppression buckets and initialise their sequence,
// without changing the mapping layout or requiring a migration.
func TestSecretSuppressionBucketsUpgradeLegacyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := bolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatalf("bolt.Open() error = %v", err)
	}
	err = db.Update(func(tx *bolt.Tx) error {
		meta, err := tx.CreateBucketIfNotExists([]byte(secretMappingMetaBucket))
		if err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists([]byte(secretMappingBucket)); err != nil {
			return err
		}
		if _, err := tx.CreateBucketIfNotExists([]byte(secretMappingIndexBucket)); err != nil {
			return err
		}
		var version [4]byte
		binary.BigEndian.PutUint32(version[:], secretMappingVersion)
		if err := meta.Put([]byte(secretMappingVersionKey), version[:]); err != nil {
			return err
		}
		return meta.Put([]byte(secretMappingSeqKey), seqKey(0))
	})
	if err != nil {
		t.Fatalf("seed legacy file: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close(legacy) error = %v", err)
	}

	st := configTransactionTestStore(t)
	ms, err := st.OpenSecretMappingStore(path)
	if err != nil {
		t.Fatalf("OpenSecretMappingStore(legacy) error = %v", err)
	}
	defer ms.Close()

	entry, err := ms.AppendSuppression(secretmask.Suppression{Rule: "", Real: "legacy-value", Reason: "upgrade"})
	if err != nil {
		t.Fatalf("AppendSuppression() after upgrade error = %v", err)
	}
	if entry.ID != 1 {
		t.Fatalf("suppression ID = %d, want 1 from a freshly initialised sequence", entry.ID)
	}
	loaded, err := ms.LoadSuppressions()
	if err != nil {
		t.Fatalf("LoadSuppressions() error = %v", err)
	}
	if len(loaded) != 1 || loaded[0].Real != "legacy-value" {
		t.Fatalf("LoadSuppressions() = %+v, want the upgraded entry", loaded)
	}
}
