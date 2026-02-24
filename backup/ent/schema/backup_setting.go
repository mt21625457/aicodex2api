package schema

import (
	"time"

	"entgo.io/ent"
	"entgo.io/ent/schema/field"
)

type BackupSetting struct {
	ent.Schema
}

func (BackupSetting) Fields() []ent.Field {
	return []ent.Field{
		field.Enum("source_mode").Values("direct", "docker_exec").Default("direct"),
		field.String("backup_root").Default("/var/lib/sub2api/backups"),
		field.Int("retention_days").Default(7),
		field.Int("keep_last").Default(30),
		field.String("sqlite_path").Default("/var/lib/sub2api/backupd.db"),
		field.Time("created_at").Default(time.Now).Immutable(),
		field.Time("updated_at").Default(time.Now).UpdateDefault(time.Now),
	}
}
