package secretmask

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	// MaxSuppressionValueBytes bounds one suppression value. Credentials are
	// shorter by orders of magnitude; the cap exists so a pasted document cannot
	// turn the bbolt row and the in-memory key into an unbounded allocation.
	MaxSuppressionValueBytes = 8192
	// MaxSuppressionReasonBytes bounds the note an operator attaches to a
	// suppression. The note is for the later review of the list, not for data.
	MaxSuppressionReasonBytes = 1024
)

// ErrInvalidSuppression marks a suppression the Vault refuses to record:
// empty/oversized value, oversized reason, or a rule name that does not exist or
// is report-only. The Monitor maps it to a 400 with the wrapped detail.
var ErrInvalidSuppression = errors.New("invalid secret mask suppression")

// Suppression is one "this rule and value are a false positive" entry.
//
// Rule is the stable rule name (`openai.project_key`, `env.secret_assignment`,
// ...) or the empty string for every rule. Real is the exact bytes the detector
// matched. Reason is an optional operator note kept for the later review of the
// list. CreatedAt is informational; matching does not depend on it.
//
// DecodeError is not stored: the administrative listing sets it when an existing
// row cannot be read with the current local key. Such a row is still listed and
// can be deleted, but it does not participate in masking.
type Suppression struct {
	ID          uint64
	Rule        string
	Real        string
	Reason      string
	CreatedAt   time.Time
	DecodeError string
}

// SuppressionStore is the durable side of the suppression list. It lives in the
// same local bbolt file as the real-to-fake mapping, so a suppression decides
// masking after a restart without a second database.
//
// LoadSuppressions is called once at Vault startup; the Vault keeps the set in
// memory and checks it on the mask path. Append and Delete are synchronous: they
// are operator actions, not request-path work. IterateSuppressions is the
// administrative listing.
type SuppressionStore interface {
	LoadSuppressions() ([]Suppression, error)
	IterateSuppressions(newestFirst bool, fn func(Suppression) error) error
	AppendSuppression(s Suppression) (Suppression, error)
	DeleteSuppressionByID(id uint64) (Suppression, bool, error)
}

// suppressionPairKey keys the in-memory set. The length prefix makes the join
// unambiguous even if a rule name or value contains the separator byte; rule
// names are short ASCII identifiers, but a value may be arbitrary bytes.
func suppressionPairKey(rule, real string) string {
	return strconv.Itoa(len(rule)) + ":" + rule + real
}

// suppressionHit reports whether the set suppresses this rule/value pair. An
// entry with an empty rule suppresses every rule for the value; a rule-specific
// entry suppresses only that rule.
func suppressionHit(set map[string]struct{}, rule string, real []byte) bool {
	if len(set) == 0 {
		return false
	}
	value := string(real)
	if _, ok := set[suppressionPairKey(rule, value)]; ok {
		return true
	}
	_, ok := set[suppressionPairKey("", value)]
	return ok
}

// loadSuppressions installs the durable rows in the in-memory set. A pair with
// no real value is ignored: it could never match.
func (v *Vault) loadSuppressions(list []Suppression) {
	next := make(map[string]struct{}, len(list))
	for _, s := range list {
		if s.Real == "" {
			continue
		}
		next[suppressionPairKey(s.Rule, s.Real)] = struct{}{}
	}
	v.mu.Lock()
	v.suppressions = next
	v.mu.Unlock()
}

// suppressionStore returns the durable suppression surface, or nil when the
// vault is in-memory only or its store does not implement it.
func (v *Vault) suppressionStore() SuppressionStore {
	if v == nil || v.store == nil {
		return nil
	}
	store, _ := v.store.(SuppressionStore)
	return store
}

// AddSuppression persists one false-positive entry and installs it in memory so
// the next request stops masking it. The rule is validated against the maskable
// rule table: a typo would otherwise create an entry that can never match, which
// looks like a working suppression until the value is masked again. An empty
// rule - or "*" - means every rule.
//
// The durable row is written first. A failed write leaves memory unchanged, so
// the page cannot report a suppression that a restart would forget.
func (v *Vault) AddSuppression(rule, real, reason string) (Suppression, error) {
	store := v.suppressionStore()
	if store == nil {
		return Suppression{}, ErrMappingAdminUnavailable
	}
	rule = strings.TrimSpace(rule)
	if rule == "*" {
		rule = ""
	}
	if rule != "" {
		meta, ok := RuleByName(rule)
		if !ok || meta.ReportOnly {
			return Suppression{}, fmt.Errorf("%w: unknown or unmaskable rule %q", ErrInvalidSuppression, rule)
		}
	}
	if real == "" {
		return Suppression{}, fmt.Errorf("%w: value is empty", ErrInvalidSuppression)
	}
	if len(real) > MaxSuppressionValueBytes {
		return Suppression{}, fmt.Errorf("%w: value is longer than %d bytes", ErrInvalidSuppression, MaxSuppressionValueBytes)
	}
	reason = strings.TrimSpace(reason)
	if len(reason) > MaxSuppressionReasonBytes {
		return Suppression{}, fmt.Errorf("%w: reason is longer than %d bytes", ErrInvalidSuppression, MaxSuppressionReasonBytes)
	}

	stored, err := store.AppendSuppression(Suppression{
		Rule:      rule,
		Real:      real,
		Reason:    reason,
		CreatedAt: time.Now().UTC(),
	})
	if err != nil {
		return Suppression{}, err
	}
	v.mu.Lock()
	next := make(map[string]struct{}, len(v.suppressions)+1)
	for key := range v.suppressions {
		next[key] = struct{}{}
	}
	next[suppressionPairKey(stored.Rule, stored.Real)] = struct{}{}
	v.suppressions = next
	v.mu.Unlock()
	return stored, nil
}

// DeleteSuppression removes one durable entry and drops it from the in-memory
// set. A row with a decode error never entered memory, so there is nothing to
// forget for it; the durable delete is still reported.
func (v *Vault) DeleteSuppression(id uint64) (Suppression, bool, error) {
	store := v.suppressionStore()
	if store == nil {
		return Suppression{}, false, ErrMappingAdminUnavailable
	}
	removed, found, err := store.DeleteSuppressionByID(id)
	if err != nil || !found {
		return removed, found, err
	}
	if removed.DecodeError == "" && removed.Real != "" {
		v.mu.Lock()
		next := make(map[string]struct{}, len(v.suppressions))
		for key := range v.suppressions {
			next[key] = struct{}{}
		}
		delete(next, suppressionPairKey(removed.Rule, removed.Real))
		v.suppressions = next
		v.mu.Unlock()
	}
	return removed, true, nil
}

// ListSuppressions walks the durable list, newest first when requested. The
// Monitor reads this rather than the in-memory set so the page can also show an
// unreadable row and its note.
func (v *Vault) ListSuppressions(newestFirst bool, fn func(Suppression) error) error {
	store := v.suppressionStore()
	if store == nil {
		return ErrMappingAdminUnavailable
	}
	return store.IterateSuppressions(newestFirst, fn)
}
