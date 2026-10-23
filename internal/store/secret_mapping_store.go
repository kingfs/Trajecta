package store

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/kingfs/Trajecta/pkg/secretmask"
	bolt "go.etcd.io/bbolt"
)

// SecretMappingFileName is the file the persistent real-to-stand-in mapping
// lives in, under the trace output directory.
const SecretMappingFileName = "secret_mask_mappings.db"

const (
	secretMappingMetaBucket  = "meta"
	secretMappingBucket      = "mappings"
	secretMappingIndexBucket = "by_real"

	secretMappingVersionKey = "version"
	secretMappingSeqKey     = "next_seq"
	secretMappingVersion    = 1

	// secretMappingQueueSize bounds the pending appends. A request that adds a
	// mapping this far behind the writer writes synchronously instead, which is
	// slower for that one request but never drops the pair.
	secretMappingQueueSize = 1024
	// secretMappingBatchSize and secretMappingBatchDelay batch fsyncs: a burst of
	// new values costs one transaction instead of one per value.
	secretMappingBatchSize  = 128
	secretMappingBatchDelay = 25 * time.Millisecond
)

// ErrSecretMappingStoreClosed is returned by an append that races shutdown.
var ErrSecretMappingStoreClosed = errors.New("secret mapping store is closed")

// SecretMappingStore is the bbolt-backed durable side of a secretmask.Vault.
//
// It is an embedded key-value store rather than a table in the application
// database because the file is local to the proxy process and the mapping is
// local to the fakes that process rendered: the seed is per process, the
// mapping is only meaningful beside the cassettes it protects, and bbolt gives
// ordered keys, a single dirty-page fsync per batch and crash-safe commits
// without a migration step.
//
// Layout:
//
//	meta      version, next_seq
//	mappings  big-endian sequence -> encrypted JSON {real, fake}
//	by_real   HMAC(local key, real) -> sequence
//
// The sequence is the insertion order, so "the newest N" is the last N keys and
// no timestamp index is needed. Values are encrypted with the same local key as
// the credential mask audit table; the by_real key is an HMAC rather than a
// plain hash because an unkeyed digest of an 11-digit mobile number is
// brute-forceable from the file alone.
type SecretMappingStore struct {
	db *bolt.DB
	// owner supplies the current local key. Reading it through the Store rather
	// than capturing the *secretBox means a key rotation applies to mappings
	// written after it; rows written before the rotation stay sealed with the
	// old key, which is the documented boundary.
	owner *Store

	sendMu sync.RWMutex
	closed bool
	queue  chan secretmask.Mapping
	done   chan struct{}

	closeOnce sync.Once
	closeErr  error

	errMu    sync.Mutex
	writeErr error
}

// OpenSecretMappingStore opens or creates the mapping store at path. The parent
// directory is created if needed. The caller owns the returned store and must
// Close it.
func (s *Store) OpenSecretMappingStore(path string) (*SecretMappingStore, error) {
	if s == nil {
		return nil, errors.New("store is nil")
	}
	return openSecretMappingStore(path, s)
}

func openSecretMappingStore(path string, owner *Store) (*SecretMappingStore, error) {
	if owner == nil {
		return nil, errors.New("store is nil")
	}
	if path == "" {
		return nil, errors.New("secret mapping store path is empty")
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("create secret mapping directory: %w", err)
		}
	}
	db, err := bolt.Open(path, 0o600, &bolt.Options{Timeout: 2 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("open secret mapping store %s: %w", path, err)
	}
	// bolt.Open only applies the mode when it creates the file; tighten an
	// existing file copied in from a backup, because it holds real values.
	if err := os.Chmod(path, 0o600); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("secure secret mapping store %s: %w", path, err)
	}
	if err := db.Update(initSecretMappingBuckets); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize secret mapping store: %w", err)
	}
	s := &SecretMappingStore{
		db:    db,
		owner: owner,
		queue: make(chan secretmask.Mapping, secretMappingQueueSize),
		done:  make(chan struct{}),
	}
	go s.run()
	return s, nil
}

func initSecretMappingBuckets(tx *bolt.Tx) error {
	for _, name := range []string{secretMappingMetaBucket, secretMappingBucket, secretMappingIndexBucket} {
		if _, err := tx.CreateBucketIfNotExists([]byte(name)); err != nil {
			return err
		}
	}
	meta := tx.Bucket([]byte(secretMappingMetaBucket))
	if raw := meta.Get([]byte(secretMappingVersionKey)); raw != nil {
		if len(raw) != 4 || binary.BigEndian.Uint32(raw) != secretMappingVersion {
			return fmt.Errorf("unsupported secret mapping store version %x", raw)
		}
		return nil
	}
	var version [4]byte
	binary.BigEndian.PutUint32(version[:], secretMappingVersion)
	if err := meta.Put([]byte(secretMappingVersionKey), version[:]); err != nil {
		return err
	}
	return meta.Put([]byte(secretMappingSeqKey), seqKey(0))
}

// LoadMappings returns at most limit recorded pairs, newest first; a limit of
// zero or less returns every pair. It is called once by a Vault at startup,
// before requests are served.
func (s *SecretMappingStore) LoadMappings(limit int) ([]secretmask.Mapping, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	loadAll := limit <= 0
	// Do not preallocate an operator-supplied limit directly: a mistyped
	// load_limit of a billion would reserve the slice before reading one row. A
	// load-all start grows the slice as it reads.
	capacity := limit
	if loadAll || capacity > 4096 {
		capacity = 4096
	}
	out := make([]secretmask.Mapping, 0, capacity)
	err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(secretMappingBucket))
		if bucket == nil {
			return nil
		}
		cursor := bucket.Cursor()
		for key, value := cursor.Last(); key != nil && (loadAll || len(out) < limit); key, value = cursor.Prev() {
			m, err := s.decode(value)
			if err != nil {
				// One unreadable row must not make the whole vault unusable: the
				// old mapping is lost, but masking still works and the rest of
				// the window loads.
				slog.Warn("Skipping unreadable secret mapping row", "error", err)
				continue
			}
			out = append(out, m)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// LookupMapping returns the pair recorded for real, if any. This is the read
// that keeps a fake stable when its pair is no longer in the loaded window.
func (s *SecretMappingStore) LookupMapping(real string) (secretmask.Mapping, bool, error) {
	if s == nil || s.db == nil || real == "" {
		return secretmask.Mapping{}, false, nil
	}
	var (
		found secretmask.Mapping
		ok    bool
	)
	err := s.db.View(func(tx *bolt.Tx) error {
		index := tx.Bucket([]byte(secretMappingIndexBucket))
		mappings := tx.Bucket([]byte(secretMappingBucket))
		if index == nil || mappings == nil {
			return nil
		}
		seq := index.Get(s.indexKey(real))
		if len(seq) != 8 {
			return nil
		}
		value := mappings.Get(seq)
		if value == nil {
			return nil
		}
		m, err := s.decode(value)
		if err != nil {
			return err
		}
		if m.Real != real {
			// A key collision is impossible with a 32-byte digest, but returning
			// someone else's fake would be worse than missing the record.
			return nil
		}
		found, ok = m, true
		return nil
	})
	return found, ok, err
}

// AppendMapping records one pair. It returns as soon as the pair is queued; the
// background writer batches queued pairs into one transaction. If the queue is
// full, the pair is written synchronously so it is never lost.
func (s *SecretMappingStore) AppendMapping(m secretmask.Mapping) error {
	if s == nil || s.db == nil {
		return nil
	}
	if m.Real == "" || m.Fake == "" || m.Real == m.Fake || len(m.Real) != len(m.Fake) {
		return fmt.Errorf("invalid secret mapping: real and stand-in must be non-empty and the same length")
	}
	s.sendMu.RLock()
	defer s.sendMu.RUnlock()
	if s.closed {
		return ErrSecretMappingStoreClosed
	}
	select {
	case s.queue <- m:
		return nil
	default:
		return s.writeMappings([]secretmask.Mapping{m})
	}
}

// Close stops the writer after flushing everything queued, then closes the file.
// It is idempotent and returns the first background write error, if any.
func (s *SecretMappingStore) Close() error {
	if s == nil {
		return nil
	}
	s.closeOnce.Do(func() {
		s.sendMu.Lock()
		if !s.closed {
			s.closed = true
			close(s.queue)
		}
		s.sendMu.Unlock()
		<-s.done
		if s.db != nil {
			s.closeErr = s.db.Close()
		}
		if err := s.lastWriteError(); err != nil {
			s.closeErr = errors.Join(s.closeErr, err)
		}
	})
	return s.closeErr
}

func (s *SecretMappingStore) run() {
	defer close(s.done)
	ticker := time.NewTicker(secretMappingBatchDelay)
	defer ticker.Stop()
	batch := make([]secretmask.Mapping, 0, secretMappingBatchSize)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		_ = s.writeMappings(batch)
		batch = batch[:0]
	}
	for {
		select {
		case m, ok := <-s.queue:
			if !ok {
				flush()
				return
			}
			batch = append(batch, m)
			if len(batch) >= secretMappingBatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// writeMappings commits a batch in one transaction, skipping a real value that
// is already recorded. Dedupe is by real value alone: the same credential used
// on a different day keeps the one pair it first got.
func (s *SecretMappingStore) writeMappings(batch []secretmask.Mapping) error {
	if s == nil || s.db == nil || len(batch) == 0 {
		return nil
	}
	err := s.db.Update(func(tx *bolt.Tx) error {
		meta := tx.Bucket([]byte(secretMappingMetaBucket))
		mappings := tx.Bucket([]byte(secretMappingBucket))
		index := tx.Bucket([]byte(secretMappingIndexBucket))
		if meta == nil || mappings == nil || index == nil {
			return errors.New("secret mapping buckets are missing")
		}
		seq := decodeSeq(meta.Get([]byte(secretMappingSeqKey)))
		for _, m := range batch {
			if m.Real == "" || m.Fake == "" || m.Real == m.Fake || len(m.Real) != len(m.Fake) {
				continue
			}
			key := s.indexKey(m.Real)
			if existing := index.Get(key); len(existing) == 8 && mappings.Get(existing) != nil {
				// The first pair wins. The vault reuses a recorded pair before it
				// renders, so this only happens for a value created twice in the
				// same instant, and keeping the first is what makes the mapping a
				// function of the real value. A dangling index entry (its row is
				// gone) does not count as recorded, so the pair is written again.
				continue
			}
			seq++
			encoded, err := s.encode(m)
			if err != nil {
				return err
			}
			if err := mappings.Put(seqKey(seq), encoded); err != nil {
				return err
			}
			if err := index.Put(key, seqKey(seq)); err != nil {
				return err
			}
		}
		return meta.Put([]byte(secretMappingSeqKey), seqKey(seq))
	})
	if err != nil {
		s.recordWriteError(err)
		slog.Warn("Failed to persist secret mask mappings", "count", len(batch), "error", err)
	}
	return err
}

func (s *SecretMappingStore) recordWriteError(err error) {
	if err == nil {
		return
	}
	s.errMu.Lock()
	s.writeErr = err
	s.errMu.Unlock()
}

func (s *SecretMappingStore) lastWriteError() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	return s.writeErr
}

// secretMappingPayload is the plaintext JSON sealed into a value. Only the pair
// is stored: the rule and the detection time belong to the audit row, and the
// conversion needs neither to recognize or restore a value.
type secretMappingPayload struct {
	Real string `json:"real"`
	Fake string `json:"fake"`
}

func (s *SecretMappingStore) encode(m secretmask.Mapping) ([]byte, error) {
	plain, err := json.Marshal(secretMappingPayload{
		Real: m.Real,
		Fake: m.Fake,
	})
	if err != nil {
		return nil, err
	}
	if box := s.secretBox(); box != nil && box.aead != nil {
		sealed, err := box.encryptSecretBytes(plain)
		if err != nil {
			return nil, err
		}
		return sealed, nil
	}
	return plain, nil
}

func (s *SecretMappingStore) decode(value []byte) (secretmask.Mapping, error) {
	plain := value
	if bytes.HasPrefix(value, []byte(secretEnvelopeV1)) {
		box := s.secretBox()
		if box == nil || box.aead == nil {
			return secretmask.Mapping{}, errors.New("mapping is encrypted but no local key is configured")
		}
		decrypted, err := box.decryptSecretBytes(value)
		if err != nil {
			return secretmask.Mapping{}, err
		}
		plain = decrypted
	}
	var payload secretMappingPayload
	if err := json.Unmarshal(plain, &payload); err != nil {
		return secretmask.Mapping{}, err
	}
	return secretmask.Mapping{Real: payload.Real, Fake: payload.Fake}, nil
}

// secretBox returns the store's current local key box, or nil when the store
// was opened without one.
func (s *SecretMappingStore) secretBox() *secretBox {
	if s == nil || s.owner == nil {
		return nil
	}
	return s.owner.secrets
}

// indexKey is the dedupe key for a real value. It is keyed by the local secret
// key when one exists, so the file does not contain a brute-forceable digest of
// a mobile number. Without a key the store is already plaintext, and an unkeyed
// digest is the best available.
func (s *SecretMappingStore) indexKey(real string) []byte {
	if box := s.secretBox(); box != nil && len(box.indexKey) > 0 {
		mac := hmac.New(sha256.New, box.indexKey)
		mac.Write([]byte("secretmask/mapping/index/v1"))
		mac.Write([]byte(real))
		return mac.Sum(nil)
	}
	sum := sha256.Sum256([]byte("secretmask/mapping/plain/v1" + real))
	return sum[:]
}

func seqKey(seq uint64) []byte {
	var key [8]byte
	binary.BigEndian.PutUint64(key[:], seq)
	return key[:]
}

func decodeSeq(raw []byte) uint64 {
	if len(raw) != 8 {
		return 0
	}
	return binary.BigEndian.Uint64(raw)
}
