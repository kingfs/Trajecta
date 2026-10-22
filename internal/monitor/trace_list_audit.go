package monitor

import (
	"strings"

	"github.com/kingfs/Trajecta/internal/store"
)

// The trace list has two per-row facts that do not come from the indexed log
// row: the audit tally and the user's opening text. Both are attached after the
// rows are built, so every endpoint that renders a RequestList gets the same
// numbers without teaching traceListItemFromEntry to query or read a cassette.

// attachTraceAudit fills the Audit field of every row (and of the upstream calls
// nested under it) with the tally of its persisted audit events. It runs one
// grouped query per source for the whole page instead of one per trace.
func attachTraceAudit(st *store.Store, lists ...[]traceListItem) error {
	if st == nil {
		return nil
	}
	ids := make([]string, 0, 16)
	seen := make(map[string]struct{}, 16)
	for _, items := range lists {
		eachTraceItem(items, func(item *traceListItem) {
			id := strings.TrimSpace(item.ID)
			if id == "" {
				return
			}
			if _, ok := seen[id]; ok {
				return
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		})
	}
	if len(ids) == 0 {
		return nil
	}

	findings, err := st.CountFindingsByTraceIDs(ids)
	if err != nil {
		return err
	}
	masks, err := st.CountCredentialMaskAuditsByTraceIDs(ids)
	if err != nil {
		return err
	}

	views := make(map[string]*traceAuditView, len(ids))
	for traceID, counts := range findings {
		view := ensureTraceAuditView(views, traceID)
		for _, count := range counts {
			view.Findings += count.Count
			switch {
			case isPIIFindingCategory(count.Category):
				view.PII += count.Count
			case isCredentialFindingCategory(count.Category):
				view.Credential += count.Count
			}
		}
	}
	for traceID, counts := range masks {
		view := ensureTraceAuditView(views, traceID)
		for _, count := range counts {
			view.Masked += count.Count
			if isPIIMaskEvent(count.Kind, count.Rule) {
				view.PII += count.Count
			} else {
				view.Credential += count.Count
			}
		}
	}

	for _, items := range lists {
		eachTraceItem(items, func(item *traceListItem) {
			view := views[item.ID]
			if view == nil {
				item.Audit = nil
				return
			}
			view.Total = view.Masked + view.Findings
			view.Sensitive = view.PII + view.Credential
			if view.Total == 0 {
				item.Audit = nil
				return
			}
			view.Other = view.Total - view.Sensitive
			if view.Other < 0 {
				view.Other = 0
			}
			item.Audit = view
		})
	}
	return nil
}

func ensureTraceAuditView(views map[string]*traceAuditView, traceID string) *traceAuditView {
	if view, ok := views[traceID]; ok {
		return view
	}
	view := &traceAuditView{}
	views[traceID] = view
	return view
}

// isPIIFindingCategory names the finding categories that carry personal data.
// The detector writes `pii_leak`; a credential written by a detector rather than
// rewritten by the masker is `credential_leak`.
func isPIIFindingCategory(category string) bool {
	return strings.EqualFold(strings.TrimSpace(category), "pii_leak")
}

func isCredentialFindingCategory(category string) bool {
	return strings.EqualFold(strings.TrimSpace(category), "credential_leak")
}

// isPIIMaskEvent classifies a credential_mask_audits row. The kind is the
// masker's own taxonomy; the rule prefix is the fallback for rows written before
// a kind was stored.
func isPIIMaskEvent(kind string, rule string) bool {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "national_id", "mobile":
		return true
	}
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(rule)), "pii.")
}

// eachTraceItem visits a slice and everything nested under it. The callback gets
// a pointer into the caller's backing array, so writes stick.
func eachTraceItem(items []traceListItem, visit func(*traceListItem)) {
	for i := range items {
		visit(&items[i])
		if len(items[i].UpstreamCalls) > 0 {
			eachTraceItem(items[i].UpstreamCalls, visit)
		}
	}
}
