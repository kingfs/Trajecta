package secretmask

// DefaultMappingLoadLimit is zero: a Vault loads every recorded pair at
// startup. bbolt iterates the file in insertion order and the pair is small, so
// the cost is bounded by the number of distinct sensitive values the
// installation has ever seen, not by request volume; holding them all removes
// the window in which an old stand-in could be mistaken for a new value, and a
// load-all vault does not evict, so memory grows with the number of distinct
// values the file already holds. Set a positive limit to trade that guarantee
// for a smaller, bounded footprint on an installation with an unusually large
// number of distinct values.
const DefaultMappingLoadLimit = 0

// Mapping is one persisted real-to-stand-in pair.
//
// The pair is what makes a stand-in survive a restart: the Vault renders a fake
// from a per-process seed, so without the record a value seen before the restart
// would render to a different fake and a fake sitting in the model's context
// would be masked a second time. Nothing else is stored: the rule and the time
// belong to the audit row, and adding them here would not help recognize or
// restore the value.
type Mapping struct {
	// Real is the credential or personal-data value as the client sent it.
	Real string
	// Fake is the format-preserving stand-in that left the machine.
	Fake string
}

// MappingStore is the durable side of a Vault. The Vault keeps the hot path in
// memory; the store is consulted only when a real value is not yet known, and
// new pairs are appended in the background.
//
// LoadMappings returns at most limit mappings, newest first; a limit of zero or
// less means every pair. The newest-first order is what lets a Vault evict the
// oldest first while keeping the recent window that a restarted process needs.
//
// LookupMapping returns the recorded pair for one real value. A Vault uses it
// before rendering a new fake, so a value whose mapping has fallen out of the
// loaded window keeps the same fake across restarts and evictions.
//
// AppendMapping records a new pair; the implementation may do so asynchronously.
// A pair whose Real is already recorded must be kept, not duplicated: the caller
// already rendered and returned its fake.
//
// Close flushes pending appends and releases the store. A Vault owns its store
// and closes it from Vault.Close.
type MappingStore interface {
	LoadMappings(limit int) ([]Mapping, error)
	LookupMapping(real string) (Mapping, bool, error)
	AppendMapping(m Mapping) error
	Close() error
}

// MappingRow is one recorded pair as an administrative read surface sees it.
//
// Seq is the store's insertion sequence; it is what the Monitor page uses as the
// row identity so a delete request does not have to carry the real value back in
// a URL. DecodeError is set when the row exists but its value cannot be read
// with the current local key; such a row is still listed so an operator can see
// it, but it holds no pair to display or compare.
type MappingRow struct {
	Seq         uint64
	Real        string
	Fake        string
	Encrypted   bool
	DecodeError string
}

// MappingAdminStats summarises the durable mapping store for an operator-facing
// page. It is deliberately a subset of the store's own statistics: the Monitor
// cares about the file, how many pairs it holds and whether any row is
// unreadable, not about bucket layout details.
type MappingAdminStats struct {
	Path           string
	FileSizeBytes  int64
	EntryCount     int
	EncryptedRows  int
	PlaintextRows  int
	UnreadableRows int
}

// MappingStoreAdmin is the optional administrative extension of MappingStore.
//
// The hot path only needs the four methods above; a store that also implements
// this interface can be listed and pruned while the proxy is running. The Vault
// exposes it through MappingAdmin and coordinates deletion with its in-memory
// tables, so a deleted pair stops being recognized without a restart.
type MappingStoreAdmin interface {
	IterateMappingRows(newestFirst bool, fn func(MappingRow) error) error
	DeleteMappingRowBySeq(seq uint64) (MappingRow, bool, error)
	MappingRowStats() (MappingAdminStats, error)
	// Flush commits pairs still sitting in an asynchronous append queue, so a
	// listing taken right after masking sees the pair the proxy just rendered.
	Flush() error
}
