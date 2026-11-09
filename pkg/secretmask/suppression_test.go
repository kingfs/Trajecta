package secretmask

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// memorySuppressionStore wraps the mapping-only test store with the suppression
// contract, so one Vault can be built over both halves as the bbolt store does.
type memorySuppressionStore struct {
	*memoryMappingStore
	suppressions []Suppression
	nextID       uint64
}

func (m *memorySuppressionStore) LoadSuppressions() ([]Suppression, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Suppression(nil), m.suppressions...), nil
}

func (m *memorySuppressionStore) IterateSuppressions(newestFirst bool, fn func(Suppression) error) error {
	m.mu.Lock()
	rows := append([]Suppression(nil), m.suppressions...)
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

func (m *memorySuppressionStore) AppendSuppression(s Suppression) (Suppression, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.suppressions {
		if existing.Rule == s.Rule && existing.Real == s.Real {
			return existing, nil
		}
	}
	m.nextID++
	s.ID = m.nextID
	m.suppressions = append(m.suppressions, s)
	return s, nil
}

func (m *memorySuppressionStore) DeleteSuppressionByID(id uint64) (Suppression, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i, existing := range m.suppressions {
		if existing.ID == id {
			m.suppressions = append(m.suppressions[:i], m.suppressions[i+1:]...)
			return existing, true, nil
		}
	}
	return Suppression{}, false, nil
}

func newSuppressionTestVault(t *testing.T) (*Vault, *memorySuppressionStore) {
	t.Helper()
	store := &memorySuppressionStore{memoryMappingStore: &memoryMappingStore{}}
	vault, err := NewVaultWithStore([]byte("seed-suppression"), store, 100)
	if err != nil {
		t.Fatalf("NewVaultWithStore() error = %v", err)
	}
	t.Cleanup(func() { _ = vault.Close() })
	return vault, store
}

// TestVaultSuppressionSkipsMaskingAndMapping is the core contract: a suppressed
// rule/value pair produces no replacement, no audit event and no mapping row.
func TestVaultSuppressionSkipsMaskingAndMapping(t *testing.T) {
	vault, store := newSuppressionTestVault(t)
	body := `{"messages":[{"role":"user","content":"use ` + testPersistKey + `"}]}`

	entry, err := vault.AddSuppression("openai.project_key", testPersistKey, "false positive")
	if err != nil {
		t.Fatalf("AddSuppression() error = %v", err)
	}
	if entry.ID == 0 || entry.Rule != "openai.project_key" {
		t.Fatalf("AddSuppression() = %+v, want a stored rule-scoped entry", entry)
	}

	masked, events := vault.Mask([]byte(body))
	if len(events) != 0 {
		t.Fatalf("Mask() produced %d events for a suppressed value, want 0", len(events))
	}
	if string(masked) != body {
		t.Fatal("Mask() rewrote a suppressed body")
	}
	if vault.Len() != 0 {
		t.Fatalf("vault length = %d, want no mapping for a suppressed value", vault.Len())
	}
	if saved := store.snapshot(); len(saved) != 0 {
		t.Fatalf("store holds %+v, want no mapping for a suppressed value", saved)
	}

	// A different rule for the same value is a different decision and must still
	// mask. This is why the key includes the rule.
	other, err := vault.AddSuppression("anthropic.api_key", testPersistKey, "wrong rule")
	if err != nil {
		t.Fatalf("AddSuppression(other rule) error = %v", err)
	}
	if other.Rule != "anthropic.api_key" {
		t.Fatalf("AddSuppression(other rule) = %+v", other)
	}
	// Remove the exact rule so only the non-matching one remains.
	if _, found, err := vault.DeleteSuppression(entry.ID); err != nil || !found {
		t.Fatalf("DeleteSuppression() = found=%v err=%v, want the exact entry", found, err)
	}
	if _, events := vault.Mask([]byte(body)); len(events) != 1 {
		t.Fatalf("Mask() produced %d events with only another rule suppressed, want 1", len(events))
	}
}

func TestVaultWildcardSuppressionAndDelete(t *testing.T) {
	vault, _ := newSuppressionTestVault(t)
	body := `{"messages":[{"role":"user","content":"use ` + testPersistKey + `"}]}`

	entry, err := vault.AddSuppression("*", testPersistKey, "all rules")
	if err != nil {
		t.Fatalf("AddSuppression(*) error = %v", err)
	}
	if entry.Rule != "" {
		t.Fatalf("AddSuppression(*) rule = %q, want the all-rules wildcard", entry.Rule)
	}
	if _, events := vault.Mask([]byte(body)); len(events) != 0 {
		t.Fatalf("Mask() produced %d events under a wildcard suppression, want 0", len(events))
	}

	rows := 0
	if err := vault.ListSuppressions(true, func(row Suppression) error {
		rows++
		if row.Real != testPersistKey || row.Reason != "all rules" {
			t.Fatalf("ListSuppressions() row = %+v", row)
		}
		return nil
	}); err != nil {
		t.Fatalf("ListSuppressions() error = %v", err)
	}
	if rows != 1 {
		t.Fatalf("ListSuppressions() visited %d rows, want 1", rows)
	}

	removed, found, err := vault.DeleteSuppression(entry.ID)
	if err != nil || !found {
		t.Fatalf("DeleteSuppression() = found=%v err=%v, want the entry", found, err)
	}
	if removed.Real != testPersistKey {
		t.Fatalf("DeleteSuppression() removed = %+v", removed)
	}
	if _, events := vault.Mask([]byte(body)); len(events) != 1 {
		t.Fatalf("Mask() after deleting the suppression produced %d events, want 1", len(events))
	}
	if vault.Len() != 1 {
		t.Fatalf("vault length after re-enabling masking = %d, want 1", vault.Len())
	}
}

func TestVaultSuppressionIsLoadedFromStore(t *testing.T) {
	store := &memorySuppressionStore{memoryMappingStore: &memoryMappingStore{}}
	if _, err := store.AppendSuppression(Suppression{
		Rule:      "openai.project_key",
		Real:      testPersistKey,
		Reason:    "loaded",
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("AppendSuppression() error = %v", err)
	}
	vault, err := NewVaultWithStore([]byte("seed-restart"), store, 100)
	if err != nil {
		t.Fatalf("NewVaultWithStore() error = %v", err)
	}
	defer vault.Close()

	body := `{"messages":[{"role":"user","content":"use ` + testPersistKey + `"}]}`
	if _, events := vault.Mask([]byte(body)); len(events) != 0 {
		t.Fatalf("Mask() produced %d events after loading a suppression, want 0", len(events))
	}
}

func TestAddSuppressionValidation(t *testing.T) {
	vault, _ := newSuppressionTestVault(t)
	cases := []struct {
		name   string
		rule   string
		real   string
		reason string
	}{
		{name: "empty value", rule: "", real: ""},
		{name: "unknown rule", rule: "not.a.rule", real: "value"},
		{name: "report-only rule", rule: "access.url", real: "value"},
		{name: "oversized value", rule: "", real: strings.Repeat("x", MaxSuppressionValueBytes+1)},
		{name: "oversized reason", rule: "", real: "value", reason: strings.Repeat("x", MaxSuppressionReasonBytes+1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := vault.AddSuppression(tc.rule, tc.real, tc.reason); !errors.Is(err, ErrInvalidSuppression) {
				t.Fatalf("AddSuppression() error = %v, want ErrInvalidSuppression", err)
			}
		})
	}
}

func TestSuppressionUnavailableWithoutStore(t *testing.T) {
	vault := NewVault([]byte("seed-memory"))
	if _, err := vault.AddSuppression("", "value", ""); !errors.Is(err, ErrMappingAdminUnavailable) {
		t.Fatalf("AddSuppression() error = %v, want ErrMappingAdminUnavailable", err)
	}
	if _, _, err := vault.DeleteSuppression(1); !errors.Is(err, ErrMappingAdminUnavailable) {
		t.Fatalf("DeleteSuppression() error = %v, want ErrMappingAdminUnavailable", err)
	}
	if err := vault.ListSuppressions(true, func(Suppression) error { return nil }); !errors.Is(err, ErrMappingAdminUnavailable) {
		t.Fatalf("ListSuppressions() error = %v, want ErrMappingAdminUnavailable", err)
	}
}
