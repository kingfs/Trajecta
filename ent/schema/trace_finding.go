package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type TraceFinding struct {
	ent.Schema
}

func (TraceFinding) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "trace_findings"}}
}

func (TraceFinding) Fields() []ent.Field {
	return []ent.Field{
		field.String("trace_id").NotEmpty(),
		field.String("finding_id").NotEmpty(),
		field.String("category").NotEmpty(),
		field.String("severity").NotEmpty(),
		field.Float("confidence").Default(0),
		field.String("title").Default(""),
		field.String("description").Default(""),
		field.String("evidence_path").Default(""),
		field.String("evidence_excerpt").Default(""),
		field.String("node_id").Default(""),
		field.String("detector").NotEmpty(),
		field.String("detector_version").NotEmpty(),
		field.Time("created_at").Default(time.Now),
	}
}

func (TraceFinding) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("trace_id", "finding_id").Unique(),
		index.Fields("trace_id", "severity", "category"),
		// overviewHighRiskFindings filters `WHERE severity IN ('critical',
		// 'high')` across all traces, and the index above only helps once the
		// trace id is known.
		index.Fields("severity", "created_at"),
	}
}
