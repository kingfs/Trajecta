package secretmask

import (
	"fmt"
	"strings"
	"sync"
	"testing"
)

// testPersistKey is a shape-legal OpenAI project key: strong enough to be
// rewritten without any surrounding context, and long enough that a fake is
// easy to compare exactly.
const testPersistKey = "sk-proj-" + "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA" +
	"T3BlbkFJ" + "BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB"

// memoryMappingStore is the MappingStore contract in memory. It dedupes by Real,
// like the bbolt store, and keeps the data across Close so a test can simulate a
// restart by building a second Vault over the same store.
type memoryMappingStore struct {
	mu       sync.Mutex
	mappings []Mapping
	appends  int
	loads    int
	closed   int
}

func (m *memoryMappingStore) LoadMappings(limit int) ([]Mapping, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.loads++
	if limit <= 0 || limit > len(m.mappings) {
		limit = len(m.mappings)
	}
	out := make([]Mapping, 0, limit)
	for i := len(m.mappings) - 1; i >= 0 && len(out) < limit; i-- {
		out = append(out, m.mappings[i])
	}
	return out, nil
}

func (m *memoryMappingStore) LookupMapping(real string) (Mapping, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, mapping := range m.mappings {
		if mapping.Real == real {
			return mapping, true, nil
		}
	}
	return Mapping{}, false, nil
}

func (m *memoryMappingStore) AppendMapping(mapping Mapping) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.appends++
	for _, existing := range m.mappings {
		if existing.Real == mapping.Real {
			return nil
		}
	}
	m.mappings = append(m.mappings, mapping)
	return nil
}

func (m *memoryMappingStore) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed++
	return nil
}

func (m *memoryMappingStore) snapshot() []Mapping {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Mapping(nil), m.mappings...)
}

func maskStandIn(t *testing.T, v *Vault, body string) (string, []MaskEvent) {
	t.Helper()
	out, events := v.Mask([]byte(body))
	return string(out), events
}

// TestVaultKeepsFakeAcrossRestart is the failure this store exists for: a value
// seen before the restart must render to the fake the model already holds, and
// the fake itself must be left alone when it comes back in the next request.
func TestVaultKeepsFakeAcrossRestart(t *testing.T) {
	store := &memoryMappingStore{}
	before, err := NewVaultWithStore([]byte("seed-before"), store, 100)
	if err != nil {
		t.Fatalf("NewVaultWithStore() error = %v", err)
	}
	body := `{"messages":[{"role":"user","content":"use ` + testPersistKey + `"}]}`
	masked, events := maskStandIn(t, before, body)
	if len(events) != 1 {
		t.Fatalf("first mask produced %d events, want 1", len(events))
	}
	firstFake := string(events[0].StandIn)
	if firstFake == testPersistKey {
		t.Fatal("the real key was not rewritten")
	}
	if strings.Contains(masked, testPersistKey) {
		t.Fatal("the masked body still contains the real key")
	}
	if err := before.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if store.closed != 1 {
		t.Fatalf("store closed %d times, want 1", store.closed)
	}
	saved := store.snapshot()
	if len(saved) != 1 || saved[0].Real != testPersistKey || saved[0].Fake != firstFake {
		t.Fatalf("store holds %+v, want one pair with the rendered fake", saved)
	}

	// A different seed must not matter: the recorded pair is loaded, so the real
	// value still renders to the first fake.
	after, err := NewVaultWithStore([]byte("seed-after"), store, 100)
	if err != nil {
		t.Fatalf("NewVaultWithStore() error = %v", err)
	}
	defer after.Close()
	if after.Len() != 1 {
		t.Fatalf("loaded %d mappings, want 1", after.Len())
	}
	maskedAgain, eventsAgain := maskStandIn(t, after, body)
	if len(eventsAgain) != 1 {
		t.Fatalf("second mask produced %d events, want 1", len(eventsAgain))
	}
	if got := string(eventsAgain[0].StandIn); got != firstFake {
		t.Fatalf("stand-in changed across restart: got %q, want %q", got, firstFake)
	}
	if maskedAgain != masked {
		t.Fatalf("masked body changed across restart")
	}

	// The fake the model still holds is not a credential any more: masking it
	// must leave it byte for byte, or every turn would render a new fake.
	echoed, echoedEvents := maskStandIn(t, after, `{"content":"`+firstFake+`"}`)
	if len(echoedEvents) != 0 {
		t.Fatalf("masking a recorded fake produced %d events, want 0", len(echoedEvents))
	}
	if echoed != `{"content":"`+firstFake+`"}` {
		t.Fatalf("masking a recorded fake changed the body: %q", echoed)
	}
	if got := store.appends; got != 1 {
		t.Fatalf("store received %d appends, want 1", got)
	}
}

// TestVaultKeepsMobileFakeAcrossRestart is the personal-data shape of the same
// bug. A mobile stand-in is itself a well-formed number with an allocated head,
// so the detector proposes it again on the next turn; only the loaded byFake
// index keeps it from being rendered a second time.
func TestVaultKeepsMobileFakeAcrossRestart(t *testing.T) {
	store := &memoryMappingStore{}
	const real = "13800138000"
	body := `{"phone":"` + real + `"}`

	before, err := NewVaultWithStore([]byte("mobile-seed-before"), store, 100)
	if err != nil {
		t.Fatalf("NewVaultWithStore() error = %v", err)
	}
	masked, events := maskStandIn(t, before, body)
	if len(events) != 1 {
		t.Fatalf("first mask produced %d events, want 1", len(events))
	}
	fake := string(events[0].StandIn)
	if fake == real {
		t.Fatal("the real mobile number was not rewritten")
	}
	if !validCNMobilePrefix([]byte(fake)) || len(fake) != len(real) {
		t.Fatalf("stand-in %q is not a same-length, shape-valid mobile number", fake)
	}
	if masked != `{"phone":"`+fake+`"}` {
		t.Fatalf("masked body = %q", masked)
	}
	if err := before.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	after, err := NewVaultWithStore([]byte("mobile-seed-after"), store, 100)
	if err != nil {
		t.Fatalf("NewVaultWithStore() error = %v", err)
	}
	defer after.Close()
	fakeBody := `{"phone":"` + fake + `"}`
	echoed, echoedEvents := maskStandIn(t, after, fakeBody)
	if len(echoedEvents) != 0 {
		t.Fatalf("masking a recorded mobile fake produced %d events, want 0", len(echoedEvents))
	}
	if echoed != fakeBody {
		t.Fatalf("masking a recorded mobile fake changed the body: %q", echoed)
	}
	if got := string(after.Restore([]byte(fakeBody))); got != body {
		t.Fatalf("Restore() = %q, want %q", got, body)
	}

	again, againEvents := maskStandIn(t, after, body)
	if len(againEvents) != 1 {
		t.Fatalf("second mask produced %d events, want 1", len(againEvents))
	}
	if got := string(againEvents[0].StandIn); got != fake {
		t.Fatalf("mobile stand-in changed across restart: got %q, want %q", got, fake)
	}
	if again != masked {
		t.Fatalf("masked mobile body changed across restart")
	}
}

// TestVaultLooksUpPairOutsideLoadedWindow pins that the load limit bounds memory,
// not correctness: a real value whose pair is older than the window is looked up
// before it is rendered, so it keeps its recorded fake.
func TestVaultLooksUpPairOutsideLoadedWindow(t *testing.T) {
	store := &memoryMappingStore{}
	older := Mapping{Real: testPersistKey, Fake: "sk-proj-" + strings.Repeat("C", 58) + "T3BlbkFJ" + strings.Repeat("D", 58)}
	newerKey := "AIza" + "SyA1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Q"
	newer := Mapping{Real: newerKey, Fake: "AIza" + "SyA1b2C3d4E5f6G7h8I9j0K1l2M3n4O5p6Z"}
	if err := store.AppendMapping(older); err != nil {
		t.Fatalf("AppendMapping(older) error = %v", err)
	}
	if err := store.AppendMapping(newer); err != nil {
		t.Fatalf("AppendMapping(newer) error = %v", err)
	}

	v, err := NewVaultWithStore([]byte("seed"), store, 1)
	if err != nil {
		t.Fatalf("NewVaultWithStore() error = %v", err)
	}
	defer v.Close()
	if v.Len() != 1 {
		t.Fatalf("loaded %d mappings, want only the newest", v.Len())
	}

	masked, events := maskStandIn(t, v, `{"content":"`+testPersistKey+`"}`)
	if len(events) != 1 {
		t.Fatalf("mask produced %d events, want 1", len(events))
	}
	if got := string(events[0].StandIn); got != older.Fake {
		t.Fatalf("stand-in = %q, want the recorded %q", got, older.Fake)
	}
	if !strings.Contains(masked, older.Fake) {
		t.Fatalf("masked body does not contain the recorded stand-in")
	}
	if store.appends != 2 {
		t.Fatalf("store received %d appends, want 2 (the lookup must not append)", store.appends)
	}
}

// TestVaultLoadLimitKeepsNewest pins the startup bound: the newest pairs are
// loaded and the older ones stay on disk for the lookup path.
func TestVaultLoadLimitKeepsNewest(t *testing.T) {
	store := &memoryMappingStore{}
	pairs := []Mapping{
		{Real: "real-one-0000000000", Fake: "fake-one-0000000000"},
		{Real: "real-two-0000000000", Fake: "fake-two-0000000000"},
		{Real: "real-three-000000000", Fake: "fake-three-000000000"},
	}
	for _, pair := range pairs {
		if err := store.AppendMapping(pair); err != nil {
			t.Fatalf("AppendMapping() error = %v", err)
		}
	}
	v, err := NewVaultWithStore([]byte("seed"), store, 2)
	if err != nil {
		t.Fatalf("NewVaultWithStore() error = %v", err)
	}
	defer v.Close()
	if v.Len() != 2 {
		t.Fatalf("loaded %d mappings, want 2", v.Len())
	}
	if v.isFake([]byte(pairs[0].Fake)) {
		t.Fatal("the oldest pair was loaded despite the limit")
	}
	if !v.isFake([]byte(pairs[2].Fake)) {
		t.Fatal("the newest pair was not loaded")
	}
}

// TestVaultSkipsMalformedMappings pins that a bad pair cannot break the length
// invariant or make Restore return another value's real bytes.
func TestVaultSkipsMalformedMappings(t *testing.T) {
	store := &memoryMappingStore{}
	store.mappings = []Mapping{
		{Real: "short", Fake: "longer-than-real"},
		{Real: "same-value", Fake: "same-value"},
		{Real: "shared-fake", Fake: "copied-fake"},
		{Real: "other-fake2", Fake: "copied-fake"},
	}
	v, err := NewVaultWithStore([]byte("seed"), store, 10)
	if err != nil {
		t.Fatalf("NewVaultWithStore() error = %v", err)
	}
	defer v.Close()
	if v.Len() != 1 {
		t.Fatalf("loaded %d mappings, want only the first valid one", v.Len())
	}
	if !v.isFake([]byte("copied-fake")) {
		t.Fatal("the first valid fake was not indexed")
	}
}

// TestVaultCloseIdempotent pins that a deferred Close next to an explicit one
// is harmless and that a vault without a store has nothing to close.
func TestVaultCloseIdempotent(t *testing.T) {
	store := &memoryMappingStore{}
	v, err := NewVaultWithStore(nil, store, 10)
	if err != nil {
		t.Fatalf("NewVaultWithStore() error = %v", err)
	}
	if err := v.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	if err := v.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if store.closed != 1 {
		t.Fatalf("store closed %d times, want 1", store.closed)
	}
	if err := NewVault(nil).Close(); err != nil {
		t.Fatalf("in-memory vault Close() error = %v", err)
	}
}

// TestVaultLoadsEveryMappingByDefault pins the zero-limit default: every
// recorded pair is loaded and kept resident, so a new value does not evict the
// oldest loaded pair.
func TestVaultLoadsEveryMappingByDefault(t *testing.T) {
	store := &memoryMappingStore{}
	const count = 8
	reals := make([]string, count)
	fakes := make([]string, count)
	for i := 0; i < count; i++ {
		suffix := fmt.Sprintf("%02d", i)
		reals[i] = "sk-proj-" + strings.Repeat("A", 60) + suffix + "T3BlbkFJ" + strings.Repeat("B", 60)
		fakes[i] = "sk-proj-" + strings.Repeat("C", 60) + suffix + "T3BlbkFJ" + strings.Repeat("D", 60)
		if err := store.AppendMapping(Mapping{Real: reals[i], Fake: fakes[i]}); err != nil {
			t.Fatalf("AppendMapping(%d) error = %v", i, err)
		}
	}

	v, err := NewVaultWithStore([]byte("load-all-seed"), store, 0)
	if err != nil {
		t.Fatalf("NewVaultWithStore() error = %v", err)
	}
	defer v.Close()
	if v.Len() != count {
		t.Fatalf("loaded %d mappings, want %d", v.Len(), count)
	}

	// Load-all disables eviction, so a new value must not evict a loaded pair.
	v.fakeFor(&ruleTable[RuleOpenAIProject], []byte(testPersistKey))
	if v.Len() != count+1 {
		t.Fatalf("after a new value the vault holds %d mappings, want %d", v.Len(), count+1)
	}
	for i := range reals {
		if got := v.fakeFor(&ruleTable[RuleOpenAIProject], []byte(reals[i])); got != fakes[i] {
			t.Fatalf("loaded pair %d changed: got %q, want %q", i, got, fakes[i])
		}
	}
}
