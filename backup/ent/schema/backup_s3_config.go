package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

type BackupS3Config struct {
	ent.Schema
}

func (BackupS3Config) Fields() []ent.Field {
	return []ent.Field{
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
