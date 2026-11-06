// Administrative reads and writes for the local secret-mask mapping file.
//
// The mapping file is a per-output-directory bbolt database owned by one
// process: serve opens it, holds the exclusive bbolt lock, and keeps the pairs it
// loaded in memory. These helpers exist for the offline `trajecta db
// secret-mapping` commands, which inspect and clean that file while serve is
// stopped. They reuse the same buckets, the same encode/decode helpers and the
// same keyed dedupe index as the runtime path; nothing here changes the layout.
package store

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/pkg/secretmask"
	bolt "go.etcd.io/bbolt"
)

// ErrSecretMappingStoreLocked is returned by the administrative open when
// another process (normally serve) holds the bbolt file lock. bbolt only allows
// one writer, so the CLI cannot open the same mapping file while the proxy runs.
var ErrSecretMappingStoreLocked = errors.New("secret mapping store is locked by another process; stop serve before running this command")

// ErrStopSecretMappingIteration lets an IterateMappings callback end the walk
// early without reporting a failure.
var ErrStopSecretMappingIteration = errors.New("stop secret mapping iteration")

// IsSecretMappingStoreLocked reports whether err is bbolt's exclusive-lock
// timeout, which is what a second process sees while serve holds the file.
func IsSecretMappingStoreLocked(err error) bool {
	return errors.Is(err, bolt.ErrTimeout)
}

// SecretMappingEntry is one mapping row as the administrative commands see it.
// A row whose value cannot be decrypted - the local key was rotated or lost -
// keeps its sequence and reports DecodeError with an empty Mapping.
type SecretMappingEntry struct {
	Seq       uint64             `json:"seq"`
	Mapping   secretmask.Mapping `json:"mapping"`
	Encrypted bool               `json:"encrypted"`
	// DecodeError is set when the row exists but its value cannot be decoded.
	DecodeError string `json:"decode_error,omitempty"`
}

// SecretMappingStats summarises the mapping file without changing it.
type SecretMappingStats struct {
	Path           string `json:"path"`
	FileExists     bool   `json:"file_exists"`
	FileSizeBytes  int64  `json:"file_size_bytes"`
	EntryCount     int    `json:"entry_count"`
	IndexCount     int    `json:"index_count"`
	EncryptedRows  int    `json:"encrypted_rows"`
	PlaintextRows  int    `json:"plaintext_rows"`
	UnreadableRows int    `json:"unreadable_rows"`
	// DanglingIndexEntries counts by_real entries whose mappings row is gone.
	// The runtime tolerates them (they are simply written again), but they are
	// the residue a deleted row leaves when the index entry cannot be matched.
	DanglingIndexEntries int    `json:"dangling_index_entries"`
	MinSeq               uint64 `json:"min_seq"`
	MaxSeq               uint64 `json:"max_seq"`
	// NextSeq is the bbolt meta next_seq value: the last allocated sequence
	// (the writer increments it before assigning a row).
	NextSeq uint64 `json:"next_seq"`
}

// Fingerprint returns a short, keyed digest of a real value for redacted
// display. It goes through the same HMAC key as the by_real index rather than a
// plain SHA-256: an unkeyed digest prefix of a low-entropy value (an eleven-digit
// mobile number, an identity number) can be confirmed by brute force from a
// terminal scrollback or a pasted ticket, which is exactly why the index itself
// is keyed. The digest is stable for one local key, so an operator can correlate
// a row across two listings; it changes after a key rotation.
func (s *SecretMappingStore) Fingerprint(real string) string {
	if s == nil || real == "" {
		return ""
	}
	sum := s.indexKey(real)
	if len(sum) > 8 {
		sum = sum[:8]
	}
	return hex.EncodeToString(sum)
}

// Stats reads the mapping file and reports its row counts, sequence range and
// size. It is a read-only transaction; it does not touch the background writer.
func (s *SecretMappingStore) Stats() (SecretMappingStats, error) {
	stats := SecretMappingStats{}
	if s == nil || s.db == nil {
		return stats, nil
	}
	stats.Path = s.db.Path()
	if stats.Path != "" {
		if info, err := os.Stat(stats.Path); err == nil && !info.IsDir() {
			stats.FileExists = true
			stats.FileSizeBytes = info.Size()
		}
	}
	err := s.db.View(func(tx *bolt.Tx) error {
		meta := tx.Bucket([]byte(secretMappingMetaBucket))
		mappings := tx.Bucket([]byte(secretMappingBucket))
		index := tx.Bucket([]byte(secretMappingIndexBucket))
		if meta != nil {
			stats.NextSeq = decodeSeq(meta.Get([]byte(secretMappingSeqKey)))
		}
		if mappings != nil {
			first := true
			cursor := mappings.Cursor()
			for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
				seq := decodeSeq(key)
				stats.EntryCount++
				if first {
					stats.MinSeq = seq
					first = false
				}
				stats.MaxSeq = seq
				if isEncryptedSecretMappingValue(value) {
					stats.EncryptedRows++
				} else {
					stats.PlaintextRows++
				}
				if _, err := s.decode(value); err != nil {
					stats.UnreadableRows++
				}
			}
		}
		if index != nil {
			cursor := index.Cursor()
			for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
				stats.IndexCount++
				if mappings == nil || len(value) != 8 || mappings.Get(value) == nil {
					stats.DanglingIndexEntries++
				}
			}
		}
		return nil
	})
	return stats, err
}

// IterateMappings walks the rows in insertion order (oldest first when
// newestFirst is false) and calls fn for each one. A row whose value cannot be
// decoded is still visited, with DecodeError set, so an operator can see it and
// delete it by sequence. Returning ErrStopSecretMappingIteration from fn ends the
// walk without an error.
func (s *SecretMappingStore) IterateMappings(newestFirst bool, fn func(SecretMappingEntry) error) error {
	if s == nil || s.db == nil || fn == nil {
		return nil
	}
	return s.db.View(func(tx *bolt.Tx) error {
		mappings := tx.Bucket([]byte(secretMappingBucket))
		if mappings == nil {
			return nil
		}
		cursor := mappings.Cursor()
		key, value := cursor.First()
		if newestFirst {
			key, value = cursor.Last()
		}
		for key != nil {
			if err := fn(s.secretMappingEntry(decodeSeq(key), value)); err != nil {
				if errors.Is(err, ErrStopSecretMappingIteration) {
					return nil
				}
				return err
			}
			if newestFirst {
				key, value = cursor.Prev()
			} else {
				key, value = cursor.Next()
			}
		}
		return nil
	})
}

// DeleteMappingBySeq removes the row with the given sequence and the by_real
// index entry that points at it. The second return value reports whether a row
// existed. A row that cannot be decrypted is still deleted; its index entry is
// found by scanning the index for the sequence because a key rotation changes the
// HMAC the index is keyed with.
func (s *SecretMappingStore) DeleteMappingBySeq(seq uint64) (SecretMappingEntry, bool, error) {
	if s == nil || s.db == nil {
		return SecretMappingEntry{}, false, errors.New("secret mapping store is not open")
	}
	entry := SecretMappingEntry{Seq: seq}
	found := false
	err := s.db.Update(func(tx *bolt.Tx) error {
		mappings := tx.Bucket([]byte(secretMappingBucket))
		index := tx.Bucket([]byte(secretMappingIndexBucket))
		if mappings == nil {
			return errors.New("secret mapping bucket is missing")
		}
		rowKey := seqKey(seq)
		value := mappings.Get(rowKey)
		if value == nil {
			return nil
		}
		entry = s.secretMappingEntry(seq, value)
		if err := mappings.Delete(rowKey); err != nil {
			return err
		}
		if err := deleteSecretMappingIndexEntries(index, [][]byte{rowKey}); err != nil {
			return err
		}
		found = true
		return nil
	})
	return entry, found, err
}

// DeleteMappingByReal removes the row recorded for real and its by_real index
// entry. The second return value reports whether a row existed. The lookup first
// uses the keyed index and falls back to decoding the rows, because a rotated
// local key changes the HMAC the older rows were indexed with.
func (s *SecretMappingStore) DeleteMappingByReal(real string) (SecretMappingEntry, bool, error) {
	if s == nil || s.db == nil {
		return SecretMappingEntry{}, false, errors.New("secret mapping store is not open")
	}
	if strings.TrimSpace(real) == "" {
		return SecretMappingEntry{}, false, errors.New("secret mapping real value is empty")
	}
	entry := SecretMappingEntry{}
	found := false
	err := s.db.Update(func(tx *bolt.Tx) error {
		mappings := tx.Bucket([]byte(secretMappingBucket))
		index := tx.Bucket([]byte(secretMappingIndexBucket))
		if mappings == nil {
			return errors.New("secret mapping bucket is missing")
		}
		// Fast path: the by_real index names the row directly.
		if index != nil {
			if seqRaw := index.Get(s.indexKey(real)); len(seqRaw) == 8 {
				seq := decodeSeq(seqRaw)
				rowKey := seqKey(seq)
				if value := mappings.Get(rowKey); value != nil {
					candidate := s.secretMappingEntry(seq, value)
					if candidate.DecodeError == "" && candidate.Mapping.Real == real {
						if err := mappings.Delete(rowKey); err != nil {
							return err
						}
						if err := deleteSecretMappingIndexEntries(index, [][]byte{rowKey}); err != nil {
							return err
						}
						entry, found = candidate, true
						return nil
					}
				}
			}
		}
		// Fallback: a rotated local key means the index HMAC no longer matches
		// the recorded value, so scan the decoded rows instead.
		entries, rowKeys := s.scanSecretMappingMatches(mappings, func(m secretmask.Mapping) bool {
			return m.Real == real
		})
		if len(entries) == 0 {
			return nil
		}
		for _, rowKey := range rowKeys {
			if err := mappings.Delete(rowKey); err != nil {
				return err
			}
		}
		if err := deleteSecretMappingIndexEntries(index, rowKeys); err != nil {
			return err
		}
		entry, found = entries[0], true
		return nil
	})
	return entry, found, err
}

// DeleteMappingsMatching removes every row whose decoded pair satisfies match
// and returns the deleted entries. An unreadable row cannot be matched by value
// and is left in place; delete it by sequence instead.
func (s *SecretMappingStore) DeleteMappingsMatching(match func(secretmask.Mapping) bool) ([]SecretMappingEntry, error) {
	if s == nil || s.db == nil {
		return nil, errors.New("secret mapping store is not open")
	}
	if match == nil {
		return nil, errors.New("secret mapping match predicate is nil")
	}
	var deleted []SecretMappingEntry
	err := s.db.Update(func(tx *bolt.Tx) error {
		mappings := tx.Bucket([]byte(secretMappingBucket))
		index := tx.Bucket([]byte(secretMappingIndexBucket))
		if mappings == nil {
			return errors.New("secret mapping bucket is missing")
		}
		entries, rowKeys := s.scanSecretMappingMatches(mappings, match)
		if len(entries) == 0 {
			return nil
		}
		for _, rowKey := range rowKeys {
			if err := mappings.Delete(rowKey); err != nil {
				return err
			}
		}
		if err := deleteSecretMappingIndexEntries(index, rowKeys); err != nil {
			return err
		}
		deleted = entries
		return nil
	})
	return deleted, err
}

// scanSecretMappingMatches decodes every row and collects the ones match
// accepts, together with their raw bucket keys. The caller owns the read/write
// transaction that supplies mappings.
func (s *SecretMappingStore) scanSecretMappingMatches(mappings *bolt.Bucket, match func(secretmask.Mapping) bool) ([]SecretMappingEntry, [][]byte) {
	var entries []SecretMappingEntry
	var rowKeys [][]byte
	cursor := mappings.Cursor()
	for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
		entry := s.secretMappingEntry(decodeSeq(key), value)
		if entry.DecodeError != "" {
			continue
		}
		if !match(entry.Mapping) {
			continue
		}
		entries = append(entries, entry)
		rowKeys = append(rowKeys, append([]byte(nil), key...))
	}
	return entries, rowKeys
}

func (s *SecretMappingStore) secretMappingEntry(seq uint64, value []byte) SecretMappingEntry {
	entry := SecretMappingEntry{
		Seq:       seq,
		Encrypted: isEncryptedSecretMappingValue(value),
	}
	mapping, err := s.decode(value)
	if err != nil {
		entry.DecodeError = err.Error()
		return entry
	}
	entry.Mapping = mapping
	return entry
}

func isEncryptedSecretMappingValue(value []byte) bool {
	return bytes.HasPrefix(value, []byte(secretEnvelopeV1))
}

// deleteSecretMappingIndexEntries removes every by_real entry whose value names
// one of rowKeys. Matching by value rather than recomputing the HMAC is what
// still cleans the index after a local-key rotation.
func deleteSecretMappingIndexEntries(index *bolt.Bucket, rowKeys [][]byte) error {
	if index == nil || len(rowKeys) == 0 {
		return nil
	}
	wanted := make(map[string]struct{}, len(rowKeys))
	for _, rowKey := range rowKeys {
		wanted[string(rowKey)] = struct{}{}
	}
	var stale [][]byte
	cursor := index.Cursor()
	for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
		if _, ok := wanted[string(value)]; ok {
			stale = append(stale, append([]byte(nil), key...))
		}
	}
	for _, key := range stale {
		if err := index.Delete(key); err != nil {
			return err
		}
	}
	return nil
}

// SecretMappingCompactResult reports what CompactSecretMappingFile did.
type SecretMappingCompactResult struct {
	Path           string `json:"path"`
	BackupPath     string `json:"backup_path,omitempty"`
	Entries        int    `json:"entries"`
	IndexEntries   int    `json:"index_entries"`
	FileSizeBefore int64  `json:"file_size_before"`
	FileSizeAfter  int64  `json:"file_size_after"`
}

// CompactSecretMappingFile rebuilds the bbolt mapping file into a fresh file,
// keeping every live row and the meta sequence range, then swaps it in beside a
// `.bak` copy. bbolt never shrinks a file after keys are deleted, so this is the
// only way to reclaim the space the deleted pairs occupied.
//
// It copies the bucket entries as raw bytes and does not decrypt anything, so
// it works without the local key. The caller must have closed every other handle
// on the file: bbolt's exclusive lock is taken here with the same two-second
// timeout, which also means a running serve makes the call fail with the locked
// error.
func CompactSecretMappingFile(path string) (SecretMappingCompactResult, error) {
	result := SecretMappingCompactResult{Path: path}
	if strings.TrimSpace(path) == "" {
		return result, errors.New("secret mapping store path is empty")
	}
	before, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return result, fmt.Errorf("secret mapping file %s does not exist", path)
		}
		return result, err
	}
	if before.IsDir() {
		return result, fmt.Errorf("secret mapping path %s is a directory", path)
	}
	result.FileSizeBefore = before.Size()

	backupPath := path + ".bak"
	if _, err := os.Stat(backupPath); err == nil {
		return result, fmt.Errorf("backup file %s already exists; move or remove it before compacting", backupPath)
	} else if !os.IsNotExist(err) {
		return result, err
	}

	tempPath := filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+".compact.tmp")
	if err := os.Remove(tempPath); err != nil && !os.IsNotExist(err) {
		return result, err
	}

	source, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		if IsSecretMappingStoreLocked(err) {
			return result, fmt.Errorf("%w: open secret mapping file %s: %w", ErrSecretMappingStoreLocked, path, err)
		}
		return result, fmt.Errorf("open secret mapping file %s: %w", path, err)
	}
	closed := false
	defer func() {
		if !closed {
			_ = source.Close()
		}
	}()

	target, err := bolt.Open(tempPath, 0o600, nil)
	if err != nil {
		return result, fmt.Errorf("create compacted secret mapping file: %w", err)
	}
	if err := copySecretMappingFile(source, target, &result); err != nil {
		_ = target.Close()
		_ = os.Remove(tempPath)
		return result, err
	}
	if err := target.Close(); err != nil {
		_ = os.Remove(tempPath)
		return result, err
	}
	if err := source.Close(); err != nil {
		closed = true
		_ = os.Remove(tempPath)
		return result, err
	}
	closed = true

	// The backup path is checked above, so the rename pair cannot clobber an
	// existing file. Rename the live file aside first and put it back if the
	// second rename fails, so a failure never leaves the operator without a
	// mapping file.
	if err := os.Rename(path, backupPath); err != nil {
		_ = os.Remove(tempPath)
		return result, fmt.Errorf("move secret mapping file aside: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Rename(backupPath, path)
		_ = os.Remove(tempPath)
		return result, fmt.Errorf("install compacted secret mapping file: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return result, fmt.Errorf("secure compacted secret mapping file: %w", err)
	}
	if err := os.Chmod(backupPath, 0o600); err != nil {
		return result, fmt.Errorf("secure secret mapping backup: %w", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		return result, err
	}
	result.FileSizeAfter = after.Size()
	result.BackupPath = backupPath
	return result, nil
}

func copySecretMappingFile(source, target *bolt.DB, result *SecretMappingCompactResult) error {
	return source.View(func(src *bolt.Tx) error {
		return target.Update(func(dst *bolt.Tx) error {
			for _, name := range []string{secretMappingMetaBucket, secretMappingBucket, secretMappingIndexBucket} {
				if _, err := dst.CreateBucketIfNotExists([]byte(name)); err != nil {
					return err
				}
			}
			copyBucket := func(bucketName string, count *int) error {
				srcBucket := src.Bucket([]byte(bucketName))
				dstBucket := dst.Bucket([]byte(bucketName))
				if srcBucket == nil || dstBucket == nil {
					return nil
				}
				cursor := srcBucket.Cursor()
				for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
					if value == nil {
						// A nested bucket would need its own recursive copy; the
						// mapping layout has none, and silently skipping one is
						// safer than writing a partial copy.
						continue
					}
					if err := dstBucket.Put(key, value); err != nil {
						return err
					}
					if count != nil {
						*count++
					}
				}
				return nil
			}
			if err := copyBucket(secretMappingMetaBucket, nil); err != nil {
				return err
			}
			if err := copyBucket(secretMappingBucket, &result.Entries); err != nil {
				return err
			}
			return copyBucket(secretMappingIndexBucket, &result.IndexEntries)
		})
	})
}

// DeleteCredentialMaskAudits removes the audit rows matching filter and reports
// how many rows were deleted. It never touches system_events: the event-feed
// notification the masking raised is a separate record and stays in place.
//
// The filter must be non-empty so a mistyped command cannot erase the whole
// audit table.
func (s *Store) DeleteCredentialMaskAudits(filter CredentialMaskAuditFilter) (int, error) {
	if s == nil || s.db == nil {
		return 0, errors.New("store is not open")
	}
	where := make([]string, 0, 4)
	args := make([]any, 0, 4)
	if traceID := strings.TrimSpace(filter.TraceID); traceID != "" {
		where = append(where, "trace_id = ?")
		args = append(args, traceID)
	}
	if rule := strings.TrimSpace(filter.Rule); rule != "" {
		where = append(where, "rule = ?")
		args = append(args, rule)
	}
	if !filter.Since.IsZero() {
		where = append(where, "detected_at >= ?")
		args = append(args, filter.Since.UTC())
	}
	if !filter.Until.IsZero() {
		where = append(where, "detected_at <= ?")
		args = append(args, filter.Until.UTC())
	}
	if len(where) == 0 {
		return 0, errors.New("refusing to delete credential mask audits without a trace_id, rule, since or until filter")
	}
	// s.db.Exec rebinds the placeholders for Postgres, so the same statement is
	// valid on SQLite and Postgres.
	execResult, err := s.db.Exec(`DELETE FROM credential_mask_audits WHERE `+strings.Join(where, " AND "), args...)
	if err != nil {
		return 0, err
	}
	deleted, err := execResult.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(deleted), nil
}
