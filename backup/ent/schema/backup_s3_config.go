package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

type BackupS3Config struct {
	ent.Schema
}

func (BackupS3Config) Fields() []ent.Field {
	return []ent.Field{
		field.String("profile_id").Default("default"),
		field.String("name").Default("默认账号"),
		field.Bool("is_active").Default(false),
		field.Bool("enabled").Default(false),
		field.String("endpoint").Default(""),
		field.String("region").Default(""),
		field.String("bucket").Default(""),
		field.String("access_key_id").Default(""),
		field.String("secret_access_key_encrypted").Optional().Sensitive(),
		field.String("prefix").Default(""),
		field.Bool("force_path_style").Default(false),
		field.Bool("use_ssl").Default(true),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}

func (BackupS3Config) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("profile_id").Unique(),
		index.Fields("is_active"),
	}
}
