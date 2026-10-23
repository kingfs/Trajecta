package secretmask

// DefaultMappingLoadLimit is zero: a Vault loads every recorded pair at
// startup. bbolt iterates the file in insertion order and the pair is small, so
// the cost is bounded by the number of distinct sensitive values the
// installation has ever seen, not by request volume; holding them all removes
// the window in which an old stand-in could be mistaken for a new value. Set a
// positive limit to trade that window for a smaller startup footprint on an
// installation with an unusually large number of distinct values.
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
