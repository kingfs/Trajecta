package proxy

import (
	"time"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/recorder"
	"github.com/kingfs/Trajecta/internal/store"
	"github.com/kingfs/Trajecta/pkg/secretmask"
)

// newSecretVault returns the process-wide credential vault when masking is
// enabled, and nil otherwise.
//
// One vault serves every request so that a credential keeps the same stand-in
// across a conversation; a model that saw one fake and is handed a different one
// on the next turn would contradict itself, and a provider caching on the
// request prefix would miss. The seed is random per process, so the mapping
// exists only in memory and a stand-in read off the wire cannot be reversed
// without this process.
func newSecretVault(cfg *config.Config) *secretmask.Vault {
	if cfg == nil || !cfg.SecretMasking.Enabled {
		return nil
	}
	return secretmask.NewVault(nil)
}

// secretMaskEvents renders one record-file event per replaced value.
//
// The event names the rule and the offsets and carries no bytes, so the cassette
// stays a record of the exchange rather than a second copy of the credential:
// the body beside it already holds the original text, and the prelude is copied
// into the trace index, served to the monitor and exported. The pair an auditor
// has to review goes to the credential mask audit table instead, which is what
// secretMaskAuditRecords builds.
func secretMaskEvents(events []secretmask.MaskEvent, at time.Time) []recorder.RecordEvent {
	if len(events) == 0 {
		return nil
	}
	out := make([]recorder.RecordEvent, 0, len(events))
	for _, e := range events {
		out = append(out, recorder.RecordEvent{
			Type:       "secret.masked",
			Time:       at,
			Attributes: e.Attributes(),
		})
	}
	return out
}

// secretMaskAuditRecords renders the same replacements as rows for the credential
// mask audit table, where the original and the stand-in are kept together.
//
// This is the only place the pair is carried forward, and it is deliberately a
// separate rendering from secretMaskEvents: a RecordEvent is serialized into the
// cassette prelude, and these values must never be.
//
// TraceID is left empty because PrepareLogFile runs before the trace is indexed.
// The recorder fills it in when the recording is finalized, which is also the
// moment the row is written.
func secretMaskAuditRecords(events []secretmask.MaskEvent) []store.CredentialMaskAuditRecord {
	if len(events) == 0 {
		return nil
	}
	out := make([]store.CredentialMaskAuditRecord, 0, len(events))
	for i, e := range events {
		original, standIn := e.AuditValues()
		out = append(out, store.CredentialMaskAuditRecord{
			// The ordinal is the position in the mask-event list, which is what
			// the caller's (trace, ordinal) key is built from and is stable for a
			// given body because the detector is deterministic.
			EventIndex: i,
			Rule:       e.RuleName(),
			Vendor:     e.Vendor(),
			Kind:       e.Kind(),
			Confidence: e.Confidence.String(),
			Score:      e.Score,
			Reasons:    e.Reasons.String(),
			Start:      e.Start,
			End:        e.End,
			Original:   original,
			StandIn:    standIn,
		})
	}
	return out
}

// secretRestoredEvent records that a response carried stand-ins. Without it the
// restore leaves no trace: the body handed to the client is byte-identical to
// what a session without masking would have produced.
func secretRestoredEvent(count int, at time.Time) recorder.RecordEvent {
	return recorder.RecordEvent{
		Type: "secret.restored",
		Time: at,
		Attributes: map[string]interface{}{
			"count":      count,
			"confidence": "exact",
		},
	}
}
