package secretmask

import "errors"

// ErrMappingAdminUnavailable is returned by the administrative mapping reads
// when secret masking is off or the vault has no durable mapping store. It is a
// configuration answer, not a failure: the Monitor page reports that the global
// file is not in use rather than pretending there are no pairs.
var ErrMappingAdminUnavailable = errors.New("secret mapping administration is unavailable: secret masking or the durable mapping store is disabled")

// MappingAdmin returns the durable store's administrative surface, or nil when
// this vault is purely in-memory. The returned interface is the same store the
// hot path uses, so a listing sees the file the next process will load.
func (v *Vault) MappingAdmin() MappingStoreAdmin {
	if v == nil || v.store == nil {
		return nil
	}
	admin, _ := v.store.(MappingStoreAdmin)
	return admin
}

// ListMappings walks the recorded pairs, newest first when requested. It reads
// the durable store rather than the loaded window: with a positive
// mapping_store.load_limit the file can hold pairs this process did not load,
// and the page is meant to show the global mapping, not the resident subset.
func (v *Vault) ListMappings(newestFirst bool, fn func(MappingRow) error) error {
	admin := v.MappingAdmin()
	if admin == nil {
		return ErrMappingAdminUnavailable
	}
	return admin.IterateMappingRows(newestFirst, fn)
}

// MappingStats reports the durable file's size and row counts.
func (v *Vault) MappingStats() (MappingAdminStats, error) {
	admin := v.MappingAdmin()
	if admin == nil {
		return MappingAdminStats{}, ErrMappingAdminUnavailable
	}
	return admin.MappingRowStats()
}

// FlushMappings commits any append still queued by the durable store. A listing
// taken immediately after a request would otherwise miss the pair that request
// just rendered, because AppendMapping returns before the batch write.
func (v *Vault) FlushMappings() error {
	admin := v.MappingAdmin()
	if admin == nil {
		return ErrMappingAdminUnavailable
	}
	return admin.Flush()
}

// DeleteMappingBySeq removes one pair from the durable store and from this
// vault's in-memory tables.
//
// The memory half is what makes the delete immediate. A running Vault answers a
// known real value from byReal before it ever consults the store, so deleting
// only the bbolt row would leave the pair active until the next restart - the
// documented limitation of the offline `trajecta db secret-mapping delete`
// command, which cannot reach a running process. The createMu lock serializes
// this against the slow path of fakeFor, so a concurrent request either sees the
// pair before deletion or renders a fresh one afterwards, never a half-deleted
// state. A pair deleted here can be recreated by the next request that carries
// the same value: this removes the recorded mapping, not the detector rule.
func (v *Vault) DeleteMappingBySeq(seq uint64) (MappingRow, bool, error) {
	admin := v.MappingAdmin()
	if admin == nil {
		return MappingRow{}, false, ErrMappingAdminUnavailable
	}
	v.createMu.Lock()
	defer v.createMu.Unlock()

	row, found, err := admin.DeleteMappingRowBySeq(seq)
	if err != nil || !found {
		return row, found, err
	}
	// An unreadable row could never have been loaded into memory, so there is
	// nothing to forget for it; the store delete is still reported.
	if row.DecodeError == "" && row.Real != "" {
		v.mu.Lock()
		v.forgetLocked(row.Real)
		v.mu.Unlock()
	}
	return row, true, nil
}

// forgetLocked removes one pair from the in-memory maps and restore index. The
// caller holds v.mu. It mirrors evictLocked without the capacity check and keeps
// fakeLen cached the same way.
func (v *Vault) forgetLocked(real string) {
	fake, ok := v.byReal[real]
	if !ok {
		return
	}
	delete(v.byReal, real)
	for _, variant := range wireVariants(real, fake) {
		v.removeWireLocked(variant.fake)
	}
	for i, existing := range v.order {
		if existing == real {
			v.order = append(v.order[:i], v.order[i+1:]...)
			break
		}
	}
	if len(fake) == v.fakeLen {
		v.fakeLen = 0
		for _, f := range v.byFake {
			if len(f) > v.fakeLen {
				v.fakeLen = len(f)
			}
		}
	}
}
