package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type BackupSourceConfig struct {
	ent.Schema
}

func (BackupSourceConfig) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("source_type").Values("postgres", "redis"),
		field.String("host").Optional(),
		field.Int("port").Optional().Nillable(),
		field.String("username").Optional(),
		field.String("password_encrypted").Optional().Sensitive(),
		field.String("database").Optional(),
		field.String("ssl_mode").Optional(),
		field.String("addr").Optional(),
		field.Int("redis_db").Optional().Nillable(),
		field.String("container_name").Default(""),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (BackupSourceConfig) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("source_type").Unique(),
	}
}
