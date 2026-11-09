// Live administrative reads and deletes for a running secret-mapping store.
//
// The offline `trajecta db secret-mapping` commands open the bbolt file
// themselves, which only works while serve is stopped. These methods are the
// in-process counterpart: they use the store the proxy already holds open, so
// the Monitor can list the global pairs and delete one without a restart. They
// are ordinary methods on the runtime store rather than another reader of the
// file because bbolt permits exactly one process to hold it.
package store

import (
	"errors"

	"github.com/kingfs/Trajecta/pkg/secretmask"
)

// IterateMappingRows is the admin-facing view of IterateMappings. It exposes the
// insertion sequence and decryption state while keeping the encrypted payload
// itself inside the store.
func (s *SecretMappingStore) IterateMappingRows(newestFirst bool, fn func(secretmask.MappingRow) error) error {
	if fn == nil {
		return errors.New("secret mapping row callback is nil")
	}
	return s.IterateMappings(newestFirst, func(entry SecretMappingEntry) error {
		return fn(mappingRowOf(entry))
	})
}

// DeleteMappingRowBySeq flushes the append queue and then removes the row with
// the given sequence. The flush is load-bearing: a pair that is still in the
// writer's batch buffer would be committed after the delete and recreate the
// row. The Vault serializes this against rendering a new pair for the same real
// value, so the barrier sees a stable queue.
func (s *SecretMappingStore) DeleteMappingRowBySeq(seq uint64) (secretmask.MappingRow, bool, error) {
	if s == nil || s.db == nil {
		return secretmask.MappingRow{}, false, errors.New("secret mapping store is not open")
	}
	if err := s.Flush(); err != nil {
		return secretmask.MappingRow{}, false, err
	}
	entry, found, err := s.DeleteMappingBySeq(seq)
	if err != nil {
		return secretmask.MappingRow{}, false, err
	}
	return mappingRowOf(entry), found, nil
}

// MappingRowStats is the admin-facing subset of Stats.
func (s *SecretMappingStore) MappingRowStats() (secretmask.MappingAdminStats, error) {
	stats, err := s.Stats()
	if err != nil {
		return secretmask.MappingAdminStats{}, err
	}
	return secretmask.MappingAdminStats{
		Path:           stats.Path,
		FileSizeBytes:  stats.FileSizeBytes,
		EntryCount:     stats.EntryCount,
		EncryptedRows:  stats.EncryptedRows,
		PlaintextRows:  stats.PlaintextRows,
		UnreadableRows: stats.UnreadableRows,
	}, nil
}

func mappingRowOf(entry SecretMappingEntry) secretmask.MappingRow {
	return secretmask.MappingRow{
		Seq:         entry.Seq,
		Real:        entry.Mapping.Real,
		Fake:        entry.Mapping.Fake,
		Encrypted:   entry.Encrypted,
		DecodeError: entry.DecodeError,
	}
}
