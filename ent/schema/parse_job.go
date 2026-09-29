package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/dialect/entsql"
	"entgo.io/ent/schema"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type ParseJob struct {
	ent.Schema
}

func (ParseJob) Annotations() []schema.Annotation {
	return []schema.Annotation{entsql.Annotation{Table: "parse_jobs"}}
}

func (ParseJob) Fields() []ent.Field {
	return []ent.Field{
		field.String("trace_id").NotEmpty(),
		field.String("status").NotEmpty(),
		field.Int("attempts").Default(0),
		field.String("last_error").Default(""),
		field.Time("created_at").Default(time.Now),
		field.Time("updated_at").Default(time.Now),
	}
}

func (ParseJob) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status", "updated_at"),
		// Everything that reaches a parse job by trace id needs this one: the
		// monitor asks for a trace's job, and the derived-id repair and the
		// superseded-row guard look rows up by trace id alone. Status on its own
		// stays served by the index above.
		index.Fields("trace_id", "status"),
	}
}
