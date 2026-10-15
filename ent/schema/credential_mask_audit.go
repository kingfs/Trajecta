package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// CredentialMaskAudit stores one credential replacement: the value that was
// found in a request body and the format-preserving stand-in that left the
// machine in its place.
//
// It exists because the cassette prelude deliberately records only that a
// replacement happened - rule, offsets, score - so a content audit had nowhere
// to read the pair. The cassette is still the source of truth for the exchange;
// this table is the index an auditor queries.
//
// original_value and stand_in_value hold ciphertext when the local secret key is
// configured and plaintext when it is not, which is why the row has to report
// its storage mode rather than let a reader assume one.
type CredentialMaskAudit struct {
	ent.Schema
}

func (CredentialMaskAudit) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "credential_mask_audits"}}
}

func (CredentialMaskAudit) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").NotEmpty().Immutable(),
		field.String("trace_id").Default(""),
		// event_index orders the rows of one trace the way the prelude listed
		// them, so a re-index upserts instead of duplicating.
		field.Int("event_index").Default(0),
		field.String("rule").Default(""),
		field.String("vendor").Default(""),
		field.String("kind").Default(""),
		field.String("confidence").Default(""),
		field.Int("score").Default(0),
		field.String("reasons").Default(""),
		field.Int("start_offset").Default(0),
		field.Int("end_offset").Default(0),
		field.Int("value_length").Default(0),
		field.String("original_value").Default(""),
		field.String("stand_in_value").Default(""),
		field.String("storage_mode").Default(""),
		field.Time("detected_at").Default(time.Now),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (CredentialMaskAudit) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("trace_id", "event_index").Unique(),
		index.Fields("detected_at", "id"),
		index.Fields("rule", "detected_at"),
	}
}
