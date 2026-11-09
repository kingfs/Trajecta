// The durable suppression list: "this rule and value are a false positive".
//
// It lives in the same bbolt file as the real-to-fake mapping because both are
// local policy read by the same running Vault. The buckets are additive; a file
// written before this feature simply gets them created on open.
package store

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"hash"
	"log/slog"
	"strings"
	"time"

	"github.com/kingfs/Trajecta/pkg/secretmask"
	bolt "go.etcd.io/bbolt"
)

// secretSuppressionPayload is the plaintext sealed into one suppression row.
// ID and DecodeError are not stored: the ID is the bucket key and the decode
// error is a property of the read.
type secretSuppressionPayload struct {
	Rule      string    `json:"rule"`
	Real      string    `json:"real"`
	Reason    string    `json:"reason,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// LoadSuppressions returns every readable suppression at startup. A row sealed
// with a key that is no longer configured is skipped with a warning: it cannot
// match anything, but the administrative listing still shows it and allows a
// delete.
func (s *SecretMappingStore) LoadSuppressions() ([]secretmask.Suppression, error) {
	if s == nil || s.db == nil {
		return nil, nil
	}
	out := make([]secretmask.Suppression, 0, 16)
	err := s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(secretSuppressionBucket))
		if bucket == nil {
			return nil
		}
		cursor := bucket.Cursor()
		for key, value := cursor.First(); key != nil; key, value = cursor.Next() {
			supp, err := s.decodeSuppression(value)
			if err != nil {
				slog.Warn("Skipping unreadable secret mask suppression row", "error", err)
				continue
			}
			supp.ID = decodeSeq(key)
			out = append(out, supp)
		}
		return nil
	})
	return out, err
}

// IterateSuppressions walks the rows in insertion order, or newest first. An
// unreadable row is still visited, with DecodeError set, so an operator can see
// and delete it by ID.
func (s *SecretMappingStore) IterateSuppressions(newestFirst bool, fn func(secretmask.Suppression) error) error {
	if s == nil || s.db == nil || fn == nil {
		return nil
	}
	s.sendMu.RLock()
	defer s.sendMu.RUnlock()
	if s.closed {
		return ErrSecretMappingStoreClosed
	}
	return s.db.View(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(secretSuppressionBucket))
		if bucket == nil {
			return nil
		}
		cursor := bucket.Cursor()
		key, value := cursor.First()
		if newestFirst {
			key, value = cursor.Last()
		}
		for key != nil {
			if err := fn(s.suppressionEntry(decodeSeq(key), value)); err != nil {
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

// AppendSuppression records one entry synchronously and returns what is stored.
// The pair (rule, real) is deduplicated: adding the same false positive twice
// keeps the first reason and creation time. An unreadable row for the same pair
// is overwritten in place so the ID stays stable.
func (s *SecretMappingStore) AppendSuppression(supp secretmask.Suppression) (secretmask.Suppression, error) {
	if s == nil || s.db == nil {
		return secretmask.Suppression{}, errors.New("secret mapping store is not open")
	}
	if supp.Real == "" {
		return secretmask.Suppression{}, errors.New("suppression value is empty")
	}
	s.sendMu.RLock()
	defer s.sendMu.RUnlock()
	if s.closed {
		return secretmask.Suppression{}, ErrSecretMappingStoreClosed
	}
	supp.Rule = strings.TrimSpace(supp.Rule)
	if supp.Rule == "*" {
		supp.Rule = ""
	}
	supp.Reason = strings.TrimSpace(supp.Reason)
	if supp.CreatedAt.IsZero() {
		supp.CreatedAt = time.Now().UTC()
	}

	stored := secretmask.Suppression{}
	err := s.db.Update(func(tx *bolt.Tx) error {
		meta := tx.Bucket([]byte(secretMappingMetaBucket))
		bucket := tx.Bucket([]byte(secretSuppressionBucket))
		index := tx.Bucket([]byte(secretSuppressionIndexBucket))
		if meta == nil || bucket == nil || index == nil {
			return errors.New("secret suppression buckets are missing")
		}
		pairKey := s.suppressionIndexKey(supp.Rule, supp.Real)
		if seqRaw := index.Get(pairKey); len(seqRaw) == 8 {
			seq := decodeSeq(seqRaw)
			if value := bucket.Get(seqRaw); value != nil {
				existing, decodeErr := s.decodeSuppression(value)
				if decodeErr == nil {
					existing.ID = seq
					stored = existing
					return nil
				}
				// The row is unreadable (typically a rotated local key). Replacing
				// it under the same sequence repairs the pair without leaking a
				// dangling index entry.
				supp.ID = seq
				encoded, err := s.encodeSuppression(supp)
				if err != nil {
					return err
				}
				if err := bucket.Put(seqRaw, encoded); err != nil {
					return err
				}
				stored = supp
				return nil
			}
		}

		seq := decodeSeq(meta.Get([]byte(secretSuppressionSeqKey))) + 1
		supp.ID = seq
		encoded, err := s.encodeSuppression(supp)
		if err != nil {
			return err
		}
		rowKey := seqKey(seq)
		if err := bucket.Put(rowKey, encoded); err != nil {
			return err
		}
		if err := index.Put(pairKey, rowKey); err != nil {
			return err
		}
		if err := meta.Put([]byte(secretSuppressionSeqKey), seqKey(seq)); err != nil {
			return err
		}
		stored = supp
		return nil
	})
	return stored, err
}

// DeleteSuppressionByID removes one row and the pair index entry that points at
// it. A row that cannot be decrypted is still deleted; its index entry is found
// by scanning the index for the sequence, because a key rotation changes the
// HMAC the entry was written with.
func (s *SecretMappingStore) DeleteSuppressionByID(id uint64) (secretmask.Suppression, bool, error) {
	if s == nil || s.db == nil {
		return secretmask.Suppression{}, false, errors.New("secret mapping store is not open")
	}
	s.sendMu.RLock()
	defer s.sendMu.RUnlock()
	if s.closed {
		return secretmask.Suppression{}, false, ErrSecretMappingStoreClosed
	}
	removed := secretmask.Suppression{ID: id}
	found := false
	err := s.db.Update(func(tx *bolt.Tx) error {
		bucket := tx.Bucket([]byte(secretSuppressionBucket))
		index := tx.Bucket([]byte(secretSuppressionIndexBucket))
		if bucket == nil {
			return errors.New("secret suppression bucket is missing")
		}
		rowKey := seqKey(id)
		value := bucket.Get(rowKey)
		if value == nil {
			return nil
		}
		removed = s.suppressionEntry(id, value)
		if err := bucket.Delete(rowKey); err != nil {
			return err
		}
		if err := deleteSecretMappingIndexEntries(index, [][]byte{rowKey}); err != nil {
			return err
		}
		found = true
		return nil
	})
	return removed, found, err
}

func (s *SecretMappingStore) suppressionEntry(id uint64, value []byte) secretmask.Suppression {
	supp := secretmask.Suppression{ID: id}
	decoded, err := s.decodeSuppression(value)
	if err != nil {
		supp.DecodeError = err.Error()
		return supp
	}
	supp.Rule = decoded.Rule
	supp.Real = decoded.Real
	supp.Reason = decoded.Reason
	supp.CreatedAt = decoded.CreatedAt
	return supp
}

func (s *SecretMappingStore) encodeSuppression(supp secretmask.Suppression) ([]byte, error) {
	plain, err := json.Marshal(secretSuppressionPayload{
		Rule:      supp.Rule,
		Real:      supp.Real,
		Reason:    supp.Reason,
		CreatedAt: supp.CreatedAt,
	})
	if err != nil {
		return nil, err
	}
	if box := s.secretBox(); box != nil && box.aead != nil {
		return box.encryptSecretBytes(plain)
	}
	return plain, nil
}

func (s *SecretMappingStore) decodeSuppression(value []byte) (secretmask.Suppression, error) {
	plain := value
	if bytes.HasPrefix(value, []byte(secretEnvelopeV1)) {
		box := s.secretBox()
		if box == nil || box.aead == nil {
			return secretmask.Suppression{}, errors.New("suppression is encrypted but no local key is configured")
		}
		decrypted, err := box.decryptSecretBytes(value)
		if err != nil {
			return secretmask.Suppression{}, err
		}
		plain = decrypted
	}
	var payload secretSuppressionPayload
	if err := json.Unmarshal(plain, &payload); err != nil {
		return secretmask.Suppression{}, err
	}
	return secretmask.Suppression{
		Rule:      payload.Rule,
		Real:      payload.Real,
		Reason:    payload.Reason,
		CreatedAt: payload.CreatedAt,
	}, nil
}

// suppressionIndexKey is the dedupe/lookup key for (rule, real). It is keyed by
// the local secret key when one exists, like by_real: the rule is not secret,
// but the value can be a mobile number or an identity number. The length
// prefixes make the combination unambiguous.
func (s *SecretMappingStore) suppressionIndexKey(rule, real string) []byte {
	box := s.secretBox()
	if box != nil && len(box.indexKey) > 0 {
		mac := hmac.New(sha256.New, box.indexKey)
		mac.Write([]byte("secretmask/suppression/index/v1"))
		writeSuppressionKeyParts(mac, rule, real)
		return mac.Sum(nil)
	}
	digest := sha256.New()
	digest.Write([]byte("secretmask/suppression/plain/v1"))
	writeSuppressionKeyParts(digest, rule, real)
	return digest.Sum(nil)
}

func writeSuppressionKeyParts(h hash.Hash, rule, real string) {
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(len(rule)))
	h.Write(size[:])
	h.Write([]byte(rule))
	binary.BigEndian.PutUint32(size[:], uint32(len(real)))
	h.Write(size[:])
	h.Write([]byte(real))
}

// compile-time checks: the runtime store is both the mapping and the
// suppression store. The interface assertions keep a signature drift from
// surfacing only when a Vault type-asserts at startup.
var (
	_ secretmask.MappingStore     = (*SecretMappingStore)(nil)
	_ secretmask.SuppressionStore = (*SecretMappingStore)(nil)
)
