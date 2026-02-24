package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type BackupJobEvent struct {
	ent.Schema
}

func (BackupJobEvent) Fields() []ent.Field {
	return []ent.Field{
		field.Int("backup_job_id"),
		field.Enum("level").Values("info", "warning", "error").Default("info"),
		field.String("event_type").Default("state_change"),
		field.String("message"),
		field.String("payload").Optional(),
		field.Time("event_time").Default(time.Now),
		field.Time("created_at").Default(time.Now).Immutable(),
	}
}

func (BackupJobEvent) Edges() []ent.Edge {
	return []ent.Edge{
		edge.To("job", BackupJob.Type).Field("backup_job_id").Unique().Required(),
	}
}

func (BackupJobEvent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("backup_job_id", "event_time"),
	}
}
