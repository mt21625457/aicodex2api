package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/edge"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type BackupJob struct {
	ent.Schema
}

func (BackupJob) Fields() []ent.Field {
	return []ent.Field{
		field.String("job_id").Unique(),
		field.Enum("backup_type").Values("postgres", "redis", "full"),
		field.Enum("status").Values("queued", "running", "succeeded", "failed", "partial_succeeded").Default("queued"),
		field.String("triggered_by").Default("system"),
		field.String("idempotency_key").Optional(),
		field.Bool("upload_to_s3").Default(false),
		field.Time("started_at").Optional().Nillable(),
		field.Time("finished_at").Optional().Nillable(),
		field.String("error_message").Optional(),
		field.String("artifact_local_path").Optional(),
		field.Int64("artifact_size_bytes").Optional().Nillable(),
		field.String("artifact_sha256").Optional(),
		field.String("s3_bucket").Optional(),
		field.String("s3_key").Optional(),
		field.String("s3_etag").Optional(),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (BackupJob) Edges() []ent.Edge {
	return []ent.Edge{
		edge.From("events", BackupJobEvent.Type).Ref("job"),
	}
}

func (BackupJob) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("status", "created_at"),
		index.Fields("backup_type", "created_at"),
		index.Fields("idempotency_key"),
	}
}
