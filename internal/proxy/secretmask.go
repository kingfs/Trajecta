package proxy

import (
	"time"

	"github.com/kingfs/Trajecta/internal/config"
	"github.com/kingfs/Trajecta/internal/recorder"
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
// The event names the rule and the offsets and carries no bytes. The cassette
// body next to it holds the original text, so duplicating the value here would
// only create a second copy to redact later.
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
