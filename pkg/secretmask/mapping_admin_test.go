package secretmask

import (
	"errors"
	"strings"
	"testing"
)

// The administrative methods below make memoryMappingStore satisfy
// MappingStoreAdmin. The durable bbolt store is covered by internal/store's
// tests; these only need the queue-like contract the Vault's delete path uses.
func (m *memoryMappingStore) IterateMappingRows(newestFirst bool, fn func(MappingRow) error) error {
	m.mu.Lock()
	rows := make([]MappingRow, 0, len(m.mappings))
	for i, mapping := range m.mappings {
		rows = append(rows, MappingRow{Seq: uint64(i + 1), Real: mapping.Real, Fake: mapping.Fake, Encrypted: true})
	}
	m.mu.Unlock()
	if newestFirst {
		for left, right := 0, len(rows)-1; left < right; left, right = left+1, right-1 {
			rows[left], rows[right] = rows[right], rows[left]
		}
	}
	for _, row := range rows {
		if err := fn(row); err != nil {
			return err
		}
	}
	return nil
}

func (m *memoryMappingStore) DeleteMappingRowBySeq(seq uint64) (MappingRow, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	index := int(seq) - 1
	if index < 0 || index >= len(m.mappings) {
		return MappingRow{}, false, nil
	}
	mapping := m.mappings[index]
	m.mappings = append(m.mappings[:index], m.mappings[index+1:]...)
	return MappingRow{Seq: seq, Real: mapping.Real, Fake: mapping.Fake, Encrypted: true}, true, nil
}

func (m *memoryMappingStore) MappingRowStats() (MappingAdminStats, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return MappingAdminStats{EntryCount: len(m.mappings), EncryptedRows: len(m.mappings)}, nil
}

func (m *memoryMappingStore) Flush() error { return nil }

// TestVaultDeleteMappingForgetsLoadedPair is the behaviour the Monitor's delete
// button relies on: removing the durable row is not enough, because a running
// Vault answers a known real value from byReal before it consults the store.
// After this call the pair is gone from both, and the next match renders a new
// one instead of resurrecting the old fake from memory.
func TestVaultDeleteMappingForgetsLoadedPair(t *testing.T) {
	store := &memoryMappingStore{}
	vault, err := NewVaultWithStore([]byte("seed-delete"), store, 100)
	if err != nil {
		t.Fatalf("NewVaultWithStore() error = %v", err)
	}
	defer vault.Close()

	body := `{"messages":[{"role":"user","content":"use ` + testPersistKey + `"}]}`
	masked, events := vault.Mask([]byte(body))
	if len(events) != 1 {
		t.Fatalf("Mask() produced %d events, want 1", len(events))
	}
	fake := string(events[0].StandIn)
	if !strings.Contains(string(masked), fake) {
		t.Fatal("masked body does not contain the stand-in")
	}
	if vault.Len() != 1 {
		t.Fatalf("vault length = %d, want 1", vault.Len())
	}

	row, found, err := vault.DeleteMappingBySeq(1)
	if err != nil {
		t.Fatalf("DeleteMappingBySeq() error = %v", err)
	}
	if !found {
		t.Fatal("DeleteMappingBySeq() found = false, want the recorded row")
	}
	if row.Real != testPersistKey || row.Fake != fake {
		t.Fatalf("deleted row = %+v, want the rendered pair", row)
	}
	if vault.Len() != 0 {
		t.Fatalf("vault length after delete = %d, want 0", vault.Len())
	}
	if vault.isFake([]byte(fake)) {
		t.Fatal("the deleted stand-in is still recognized as a fake")
	}
	if saved := store.snapshot(); len(saved) != 0 {
		t.Fatalf("store still holds %+v after delete", saved)
	}

	stats, err := vault.MappingStats()
	if err != nil {
		t.Fatalf("MappingStats() error = %v", err)
	}
	if stats.EntryCount != 0 {
		t.Fatalf("MappingStats() entry count = %d, want 0", stats.EntryCount)
	}
	if err := vault.ListMappings(true, func(MappingRow) error {
		t.Fatal("ListMappings() visited a row after the only pair was deleted")
		return nil
	}); err != nil {
		t.Fatalf("ListMappings() error = %v", err)
	}

	// The rule still matches, so the value is recognized again and gets a fresh
	// durable row. Deleting a mapping is not a suppression rule.
	_, again := vault.Mask([]byte(body))
	if len(again) != 1 {
		t.Fatalf("second Mask() produced %d events, want 1", len(again))
	}
	if vault.Len() != 1 {
		t.Fatalf("vault length after re-mask = %d, want 1", vault.Len())
	}
	if saved := store.snapshot(); len(saved) != 1 {
		t.Fatalf("store holds %+v after re-mask, want one pair", saved)
	}
}

func TestVaultMappingAdminUnavailable(t *testing.T) {
	vault := NewVault([]byte("seed-memory-only"))
	if admin := vault.MappingAdmin(); admin != nil {
		t.Fatalf("MappingAdmin() = %v, want nil for an in-memory vault", admin)
	}
	if err := vault.ListMappings(true, func(MappingRow) error { return nil }); !errors.Is(err, ErrMappingAdminUnavailable) {
		t.Fatalf("ListMappings() error = %v, want ErrMappingAdminUnavailable", err)
	}
	if _, err := vault.MappingStats(); !errors.Is(err, ErrMappingAdminUnavailable) {
		t.Fatalf("MappingStats() error = %v, want ErrMappingAdminUnavailable", err)
	}
	if _, _, err := vault.DeleteMappingBySeq(1); !errors.Is(err, ErrMappingAdminUnavailable) {
		t.Fatalf("DeleteMappingBySeq() error = %v, want ErrMappingAdminUnavailable", err)
	}
}
