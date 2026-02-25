package entstore

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/backup/ent"
	"github.com/Wei-Shaw/sub2api/backup/ent/backupjob"
	"github.com/Wei-Shaw/sub2api/backup/ent/backupjobevent"
	"github.com/Wei-Shaw/sub2api/backup/ent/backups3config"
	"github.com/Wei-Shaw/sub2api/backup/ent/backupsetting"
	"github.com/Wei-Shaw/sub2api/backup/ent/backupsourceconfig"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	_ "modernc.org/sqlite"
)

const (
	defaultSQLitePath  = "/tmp/sub2api-backupd.db"
	idempotencyWindow  = 10 * time.Minute
	defaultS3ProfileID = "default"
	defaultSourceID    = "default"
)

var (
	ErrS3ProfileInUse    = errors.New("s3 profile has queued/running jobs")
	ErrActiveS3Profile   = errors.New("active s3 profile cannot be deleted")
	ErrS3ProfileRequired = errors.New("s3 profile_id is required")
	ErrSourceTypeInvalid = errors.New("source_type must be postgres or redis")
	ErrSourceIDRequired  = errors.New("source profile_id is required")
	ErrSourceActive      = errors.New("active source profile cannot be deleted")
	ErrSourceInUse       = errors.New("source profile has queued/running jobs")
)

type SourceConfig struct {
	Host          string
	Port          int32
	User          string
	Password      string
	Database      string
	SSLMode       string
	Addr          string
	Username      string
	DB            int32
	ContainerName string
}

type S3Config struct {
	Enabled         bool
	Endpoint        string
	Region          string
	Bucket          string
	AccessKeyID     string
	SecretAccessKey string
	Prefix          string
	ForcePathStyle  bool
	UseSSL          bool
}

type ConfigSnapshot struct {
	SourceMode        string
	BackupRoot        string
	SQLitePath        string
	RetentionDays     int32
	KeepLast          int32
	Postgres          SourceConfig
	Redis             SourceConfig
	S3                S3Config
	ActivePostgresID  string
	ActiveRedisID     string
	ActiveS3ProfileID string
}

type SourceProfileSnapshot struct {
	SourceType         string
	ProfileID          string
	Name               string
	IsActive           bool
	Config             SourceConfig
	PasswordConfigured bool
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

type CreateSourceProfileInput struct {
	SourceType string
	ProfileID  string
	Name       string
	Config     SourceConfig
	SetActive  bool
}

type UpdateSourceProfileInput struct {
	SourceType string
	ProfileID  string
	Name       string
	Config     SourceConfig
}

type S3ProfileSnapshot struct {
	ProfileID                 string
	Name                      string
	IsActive                  bool
	S3                        S3Config
	SecretAccessKeyConfigured bool
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
}

type CreateS3ProfileInput struct {
	ProfileID string
	Name      string
	S3        S3Config
	SetActive bool
}

type UpdateS3ProfileInput struct {
	ProfileID string
	Name      string
	S3        S3Config
}

type CreateBackupJobInput struct {
	BackupType     string
	UploadToS3     bool
	TriggeredBy    string
	IdempotencyKey string
	S3ProfileID    string
	PostgresID     string
	RedisID        string
}

type ListBackupJobsInput struct {
	PageSize   int32
	PageToken  string
	Status     string
	BackupType string
}

type ListBackupJobsOutput struct {
	Items         []*ent.BackupJob
	NextPageToken string
}

type BackupArtifactSnapshot struct {
	LocalPath string
	SizeBytes int64
	SHA256    string
}

type BackupS3ObjectSnapshot struct {
	Bucket string
	Key    string
	ETag   string
}

type FinishBackupJobInput struct {
	JobID        string
	Status       string
	ErrorMessage string
	Artifact     *BackupArtifactSnapshot
	S3Object     *BackupS3ObjectSnapshot
}

type Store struct {
	client     *ent.Client
	sqlitePath string
}

func Open(ctx context.Context, sqlitePath string) (*Store, error) {
	path := normalizeSQLitePath(sqlitePath)
	dsn := sqliteDSN(path)

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}

	if _, err := db.ExecContext(ctx, "PRAGMA journal_mode=WAL;"); err != nil {
		_ = db.Close()
		return nil, err
	}
	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout=5000;"); err != nil {
		_ = db.Close()
		return nil, err
	}

	drv := entsql.OpenDB(dialect.SQLite, db)
	client := ent.NewClient(ent.Driver(drv))
	if err := client.Schema.Create(ctx); err != nil {
		_ = client.Close()
		return nil, err
	}

	store := &Store{client: client, sqlitePath: path}
	if err := store.ensureDefaults(ctx); err != nil {
		_ = client.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	if s == nil || s.client == nil {
		return nil
	}
	return s.client.Close()
}

func (s *Store) GetConfig(ctx context.Context) (*ConfigSnapshot, error) {
	if err := s.ensureDefaults(ctx); err != nil {
		return nil, err
	}

	setting, err := s.client.BackupSetting.Query().Order(ent.Asc(backupsetting.FieldID)).First(ctx)
	if err != nil {
		return nil, err
	}
	postgresCfg, err := s.getActiveSourceConfigEntity(ctx, backupsourceconfig.SourceTypePostgres.String())
	if err != nil {
		return nil, err
	}
	redisCfg, err := s.getActiveSourceConfigEntity(ctx, backupsourceconfig.SourceTypeRedis.String())
	if err != nil {
		return nil, err
	}
	s3Cfg, err := s.getActiveS3ConfigEntity(ctx)
	if err != nil {
		return nil, err
	}

	cfg := &ConfigSnapshot{
		SourceMode:    setting.SourceMode.String(),
		BackupRoot:    setting.BackupRoot,
		SQLitePath:    setting.SqlitePath,
		RetentionDays: int32(setting.RetentionDays),
		KeepLast:      int32(setting.KeepLast),
		Postgres: SourceConfig{
			Host:          postgresCfg.Host,
			Port:          int32(nillableInt(postgresCfg.Port)),
			User:          postgresCfg.Username,
			Password:      postgresCfg.PasswordEncrypted,
			Database:      postgresCfg.Database,
			SSLMode:       postgresCfg.SslMode,
			ContainerName: postgresCfg.ContainerName,
		},
		Redis: SourceConfig{
			Addr:          redisCfg.Addr,
			Username:      redisCfg.Username,
			Password:      redisCfg.PasswordEncrypted,
			DB:            int32(nillableInt(redisCfg.RedisDb)),
			ContainerName: redisCfg.ContainerName,
		},
		S3: S3Config{
			Enabled:         s3Cfg.Enabled,
			Endpoint:        s3Cfg.Endpoint,
			Region:          s3Cfg.Region,
			Bucket:          s3Cfg.Bucket,
			AccessKeyID:     s3Cfg.AccessKeyID,
			SecretAccessKey: s3Cfg.SecretAccessKeyEncrypted,
			Prefix:          s3Cfg.Prefix,
			ForcePathStyle:  s3Cfg.ForcePathStyle,
			UseSSL:          s3Cfg.UseSsl,
		},
		ActivePostgresID:  postgresCfg.ProfileID,
		ActiveRedisID:     redisCfg.ProfileID,
		ActiveS3ProfileID: s3Cfg.ProfileID,
	}
	return cfg, nil
}

func (s *Store) UpdateConfig(ctx context.Context, cfg ConfigSnapshot) (*ConfigSnapshot, error) {
	if err := s.ensureDefaults(ctx); err != nil {
		return nil, err
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	setting, err := tx.BackupSetting.Query().Order(ent.Asc(backupsetting.FieldID)).First(ctx)
	if err != nil {
		return nil, err
	}
	updatedSetting := tx.BackupSetting.UpdateOneID(setting.ID).
		SetSourceMode(backupsetting.SourceMode(cfg.SourceMode)).
		SetBackupRoot(strings.TrimSpace(cfg.BackupRoot)).
		SetRetentionDays(int(cfg.RetentionDays)).
		SetKeepLast(int(cfg.KeepLast)).
		SetSqlitePath(strings.TrimSpace(cfg.SQLitePath))
	if _, err = updatedSetting.Save(ctx); err != nil {
		return nil, err
	}

	if err = s.updateSourceConfigTx(
		ctx,
		tx,
		backupsourceconfig.SourceTypePostgres.String(),
		strings.TrimSpace(cfg.ActivePostgresID),
		cfg.Postgres,
	); err != nil {
		return nil, err
	}
	if err = s.updateSourceConfigTx(
		ctx,
		tx,
		backupsourceconfig.SourceTypeRedis.String(),
		strings.TrimSpace(cfg.ActiveRedisID),
		cfg.Redis,
	); err != nil {
		return nil, err
	}

	s3Entity, err := tx.BackupS3Config.Query().
		Where(backups3config.IsActiveEQ(true)).
		Order(ent.Asc(backups3config.FieldID)).
		First(ctx)
	if err != nil {
		return nil, err
	}
	s3Updater := tx.BackupS3Config.UpdateOneID(s3Entity.ID).
		SetEnabled(cfg.S3.Enabled).
		SetEndpoint(strings.TrimSpace(cfg.S3.Endpoint)).
		SetRegion(strings.TrimSpace(cfg.S3.Region)).
		SetBucket(strings.TrimSpace(cfg.S3.Bucket)).
		SetAccessKeyID(strings.TrimSpace(cfg.S3.AccessKeyID)).
		SetPrefix(strings.Trim(strings.TrimSpace(cfg.S3.Prefix), "/")).
		SetForcePathStyle(cfg.S3.ForcePathStyle).
		SetUseSsl(cfg.S3.UseSSL)
	if strings.TrimSpace(cfg.S3.SecretAccessKey) != "" {
		s3Updater.SetSecretAccessKeyEncrypted(strings.TrimSpace(cfg.S3.SecretAccessKey))
	}
	if _, err = s3Updater.Save(ctx); err != nil {
		return nil, err
	}

	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetConfig(ctx)
}

func (s *Store) ListS3Profiles(ctx context.Context) ([]*S3ProfileSnapshot, error) {
	if err := s.ensureDefaults(ctx); err != nil {
		return nil, err
	}

	items, err := s.client.BackupS3Config.Query().
		Order(ent.Desc(backups3config.FieldIsActive), ent.Asc(backups3config.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]*S3ProfileSnapshot, 0, len(items))
	for _, item := range items {
		out = append(out, toS3ProfileSnapshot(item))
	}
	return out, nil
}

func (s *Store) GetS3Profile(ctx context.Context, profileID string) (*S3ProfileSnapshot, error) {
	if err := s.ensureDefaults(ctx); err != nil {
		return nil, err
	}
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return nil, ErrS3ProfileRequired
	}
	entity, err := s.client.BackupS3Config.Query().
		Where(backups3config.ProfileIDEQ(profileID)).
		First(ctx)
	if err != nil {
		return nil, err
	}
	return toS3ProfileSnapshot(entity), nil
}

func (s *Store) CreateS3Profile(ctx context.Context, input CreateS3ProfileInput) (*S3ProfileSnapshot, error) {
	if err := s.ensureDefaults(ctx); err != nil {
		return nil, err
	}

	profileID := strings.TrimSpace(input.ProfileID)
	if profileID == "" {
		return nil, ErrS3ProfileRequired
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, errors.New("s3 profile name is required")
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	activeCount, err := tx.BackupS3Config.Query().Where(backups3config.IsActiveEQ(true)).Count(ctx)
	if err != nil {
		return nil, err
	}
	setActive := input.SetActive || activeCount == 0
	if setActive {
		if _, err = tx.BackupS3Config.Update().
			Where(backups3config.IsActiveEQ(true)).
			SetIsActive(false).
			Save(ctx); err != nil {
			return nil, err
		}
	}

	builder := tx.BackupS3Config.Create().
		SetProfileID(profileID).
		SetName(name).
		SetIsActive(setActive).
		SetEnabled(input.S3.Enabled).
		SetEndpoint(strings.TrimSpace(input.S3.Endpoint)).
		SetRegion(strings.TrimSpace(input.S3.Region)).
		SetBucket(strings.TrimSpace(input.S3.Bucket)).
		SetAccessKeyID(strings.TrimSpace(input.S3.AccessKeyID)).
		SetPrefix(strings.Trim(strings.TrimSpace(input.S3.Prefix), "/")).
		SetForcePathStyle(input.S3.ForcePathStyle).
		SetUseSsl(input.S3.UseSSL)
	if secret := strings.TrimSpace(input.S3.SecretAccessKey); secret != "" {
		builder.SetSecretAccessKeyEncrypted(secret)
	}

	if _, err = builder.Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetS3Profile(ctx, profileID)
}

func (s *Store) UpdateS3Profile(ctx context.Context, input UpdateS3ProfileInput) (*S3ProfileSnapshot, error) {
	if err := s.ensureDefaults(ctx); err != nil {
		return nil, err
	}

	profileID := strings.TrimSpace(input.ProfileID)
	if profileID == "" {
		return nil, ErrS3ProfileRequired
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, errors.New("s3 profile name is required")
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	entity, err := tx.BackupS3Config.Query().
		Where(backups3config.ProfileIDEQ(profileID)).
		First(ctx)
	if err != nil {
		return nil, err
	}

	updater := tx.BackupS3Config.UpdateOneID(entity.ID).
		SetName(name).
		SetEnabled(input.S3.Enabled).
		SetEndpoint(strings.TrimSpace(input.S3.Endpoint)).
		SetRegion(strings.TrimSpace(input.S3.Region)).
		SetBucket(strings.TrimSpace(input.S3.Bucket)).
		SetAccessKeyID(strings.TrimSpace(input.S3.AccessKeyID)).
		SetPrefix(strings.Trim(strings.TrimSpace(input.S3.Prefix), "/")).
		SetForcePathStyle(input.S3.ForcePathStyle).
		SetUseSsl(input.S3.UseSSL)
	if secret := strings.TrimSpace(input.S3.SecretAccessKey); secret != "" {
		updater.SetSecretAccessKeyEncrypted(secret)
	}
	if _, err = updater.Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetS3Profile(ctx, profileID)
}

func (s *Store) DeleteS3Profile(ctx context.Context, profileID string) error {
	if err := s.ensureDefaults(ctx); err != nil {
		return err
	}

	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return ErrS3ProfileRequired
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	entity, err := tx.BackupS3Config.Query().
		Where(backups3config.ProfileIDEQ(profileID)).
		First(ctx)
	if err != nil {
		return err
	}
	if entity.IsActive {
		_ = tx.Rollback()
		return ErrActiveS3Profile
	}

	pendingCount, err := tx.BackupJob.Query().
		Where(
			backupjob.S3ProfileIDEQ(profileID),
			backupjob.UploadToS3EQ(true),
			backupjob.Or(
				backupjob.StatusEQ(backupjob.StatusQueued),
				backupjob.StatusEQ(backupjob.StatusRunning),
			),
		).
		Count(ctx)
	if err != nil {
		return err
	}
	if pendingCount > 0 {
		_ = tx.Rollback()
		return ErrS3ProfileInUse
	}

	if err = tx.BackupS3Config.DeleteOneID(entity.ID).Exec(ctx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetActiveS3Profile(ctx context.Context, profileID string) (*S3ProfileSnapshot, error) {
	if err := s.ensureDefaults(ctx); err != nil {
		return nil, err
	}

	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return nil, ErrS3ProfileRequired
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	entity, err := tx.BackupS3Config.Query().
		Where(backups3config.ProfileIDEQ(profileID)).
		First(ctx)
	if err != nil {
		return nil, err
	}

	if !entity.IsActive {
		if _, err = tx.BackupS3Config.Update().
			Where(backups3config.IsActiveEQ(true)).
			SetIsActive(false).
			Save(ctx); err != nil {
			return nil, err
		}
		if _, err = tx.BackupS3Config.UpdateOneID(entity.ID).
			SetIsActive(true).
			Save(ctx); err != nil {
			return nil, err
		}
	}

	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetS3Profile(ctx, profileID)
}

func (s *Store) ListSourceProfiles(ctx context.Context, sourceType string) ([]*SourceProfileSnapshot, error) {
	if err := s.ensureDefaults(ctx); err != nil {
		return nil, err
	}

	enumType, err := parseSourceType(sourceType)
	if err != nil {
		return nil, err
	}
	items, err := s.client.BackupSourceConfig.Query().
		Where(backupsourceconfig.SourceTypeEQ(enumType)).
		Order(ent.Desc(backupsourceconfig.FieldIsActive), ent.Asc(backupsourceconfig.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]*SourceProfileSnapshot, 0, len(items))
	for _, item := range items {
		out = append(out, toSourceProfileSnapshot(item))
	}
	return out, nil
}

func (s *Store) GetSourceProfile(ctx context.Context, sourceType, profileID string) (*SourceProfileSnapshot, error) {
	if err := s.ensureDefaults(ctx); err != nil {
		return nil, err
	}

	enumType, err := parseSourceType(sourceType)
	if err != nil {
		return nil, err
	}
	normalizedProfileID := strings.TrimSpace(profileID)
	if normalizedProfileID == "" {
		return nil, ErrSourceIDRequired
	}

	entity, err := s.client.BackupSourceConfig.Query().
		Where(
			backupsourceconfig.SourceTypeEQ(enumType),
			backupsourceconfig.ProfileIDEQ(normalizedProfileID),
		).
		First(ctx)
	if err != nil {
		return nil, err
	}
	return toSourceProfileSnapshot(entity), nil
}

func (s *Store) CreateSourceProfile(ctx context.Context, input CreateSourceProfileInput) (*SourceProfileSnapshot, error) {
	if err := s.ensureDefaults(ctx); err != nil {
		return nil, err
	}

	enumType, err := parseSourceType(input.SourceType)
	if err != nil {
		return nil, err
	}
	profileID := strings.TrimSpace(input.ProfileID)
	if profileID == "" {
		return nil, ErrSourceIDRequired
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, errors.New("source profile name is required")
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	activeCount, err := tx.BackupSourceConfig.Query().
		Where(
			backupsourceconfig.SourceTypeEQ(enumType),
			backupsourceconfig.IsActiveEQ(true),
		).
		Count(ctx)
	if err != nil {
		return nil, err
	}
	setActive := input.SetActive || activeCount == 0
	if setActive {
		if _, err = tx.BackupSourceConfig.Update().
			Where(backupsourceconfig.SourceTypeEQ(enumType), backupsourceconfig.IsActiveEQ(true)).
			SetIsActive(false).
			Save(ctx); err != nil {
			return nil, err
		}
	}

	create := tx.BackupSourceConfig.Create().
		SetSourceType(enumType).
		SetProfileID(profileID).
		SetName(name).
		SetIsActive(setActive)
	applySourceConfigCreate(create, enumType, input.Config)
	if _, err = create.Save(ctx); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetSourceProfile(ctx, enumType.String(), profileID)
}

func (s *Store) UpdateSourceProfile(ctx context.Context, input UpdateSourceProfileInput) (*SourceProfileSnapshot, error) {
	if err := s.ensureDefaults(ctx); err != nil {
		return nil, err
	}

	enumType, err := parseSourceType(input.SourceType)
	if err != nil {
		return nil, err
	}
	profileID := strings.TrimSpace(input.ProfileID)
	if profileID == "" {
		return nil, ErrSourceIDRequired
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return nil, errors.New("source profile name is required")
	}

	entity, err := s.client.BackupSourceConfig.Query().
		Where(
			backupsourceconfig.SourceTypeEQ(enumType),
			backupsourceconfig.ProfileIDEQ(profileID),
		).
		First(ctx)
	if err != nil {
		return nil, err
	}

	updater := s.client.BackupSourceConfig.UpdateOneID(entity.ID).
		SetName(name)
	applySourceConfigUpdate(updater, enumType, input.Config)
	if _, err = updater.Save(ctx); err != nil {
		return nil, err
	}
	return s.GetSourceProfile(ctx, enumType.String(), profileID)
}

func (s *Store) DeleteSourceProfile(ctx context.Context, sourceType, profileID string) error {
	if err := s.ensureDefaults(ctx); err != nil {
		return err
	}

	enumType, err := parseSourceType(sourceType)
	if err != nil {
		return err
	}
	normalizedProfileID := strings.TrimSpace(profileID)
	if normalizedProfileID == "" {
		return ErrSourceIDRequired
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	entity, err := tx.BackupSourceConfig.Query().
		Where(
			backupsourceconfig.SourceTypeEQ(enumType),
			backupsourceconfig.ProfileIDEQ(normalizedProfileID),
		).
		First(ctx)
	if err != nil {
		return err
	}
	if entity.IsActive {
		_ = tx.Rollback()
		return ErrSourceActive
	}

	inUseCount := 0
	switch enumType {
	case backupsourceconfig.SourceTypePostgres:
		inUseCount, err = tx.BackupJob.Query().
			Where(
				backupjob.PostgresProfileIDEQ(normalizedProfileID),
				backupjob.Or(
					backupjob.StatusEQ(backupjob.StatusQueued),
					backupjob.StatusEQ(backupjob.StatusRunning),
				),
			).
			Count(ctx)
	case backupsourceconfig.SourceTypeRedis:
		inUseCount, err = tx.BackupJob.Query().
			Where(
				backupjob.RedisProfileIDEQ(normalizedProfileID),
				backupjob.Or(
					backupjob.StatusEQ(backupjob.StatusQueued),
					backupjob.StatusEQ(backupjob.StatusRunning),
				),
			).
			Count(ctx)
	}
	if err != nil {
		return err
	}
	if inUseCount > 0 {
		_ = tx.Rollback()
		return ErrSourceInUse
	}

	if err = tx.BackupSourceConfig.DeleteOneID(entity.ID).Exec(ctx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) SetActiveSourceProfile(ctx context.Context, sourceType, profileID string) (*SourceProfileSnapshot, error) {
	if err := s.ensureDefaults(ctx); err != nil {
		return nil, err
	}

	enumType, err := parseSourceType(sourceType)
	if err != nil {
		return nil, err
	}
	normalizedProfileID := strings.TrimSpace(profileID)
	if normalizedProfileID == "" {
		return nil, ErrSourceIDRequired
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	entity, err := tx.BackupSourceConfig.Query().
		Where(
			backupsourceconfig.SourceTypeEQ(enumType),
			backupsourceconfig.ProfileIDEQ(normalizedProfileID),
		).
		First(ctx)
	if err != nil {
		return nil, err
	}

	if !entity.IsActive {
		if _, err = tx.BackupSourceConfig.Update().
			Where(backupsourceconfig.SourceTypeEQ(enumType), backupsourceconfig.IsActiveEQ(true)).
			SetIsActive(false).
			Save(ctx); err != nil {
			return nil, err
		}
		if _, err = tx.BackupSourceConfig.UpdateOneID(entity.ID).
			SetIsActive(true).
			Save(ctx); err != nil {
			return nil, err
		}
	}

	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return s.GetSourceProfile(ctx, enumType.String(), normalizedProfileID)
}

func (s *Store) CreateBackupJob(ctx context.Context, input CreateBackupJobInput) (*ent.BackupJob, bool, error) {
	if err := s.ensureDefaults(ctx); err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(input.TriggeredBy) == "" {
		input.TriggeredBy = "admin:unknown"
	}
	input.BackupType = strings.TrimSpace(input.BackupType)
	input.S3ProfileID = strings.TrimSpace(input.S3ProfileID)
	input.PostgresID = strings.TrimSpace(input.PostgresID)
	input.RedisID = strings.TrimSpace(input.RedisID)
	needsPostgres := backupTypeNeedsPostgres(input.BackupType)
	needsRedis := backupTypeNeedsRedis(input.BackupType)

	// 仅保留本次备份类型真正需要的来源配置，避免写入无关 profile 造成“被占用”误判。
	if !needsPostgres {
		input.PostgresID = ""
	}
	if !needsRedis {
		input.RedisID = ""
	}
	if !input.UploadToS3 {
		input.S3ProfileID = ""
	}

	if needsPostgres {
		resolvedID, resolveErr := s.resolveSourceProfileID(ctx, backupsourceconfig.SourceTypePostgres.String(), input.PostgresID)
		if resolveErr != nil {
			return nil, false, resolveErr
		}
		input.PostgresID = resolvedID
	}
	if needsRedis {
		resolvedID, resolveErr := s.resolveSourceProfileID(ctx, backupsourceconfig.SourceTypeRedis.String(), input.RedisID)
		if resolveErr != nil {
			return nil, false, resolveErr
		}
		input.RedisID = resolvedID
	}

	if input.S3ProfileID != "" {
		if _, err := s.client.BackupS3Config.Query().
			Where(backups3config.ProfileIDEQ(input.S3ProfileID)).
			First(ctx); err != nil {
			return nil, false, err
		}
	}
	now := time.Now()

	if strings.TrimSpace(input.IdempotencyKey) != "" {
		existing, err := s.client.BackupJob.Query().
			Where(
				backupjob.BackupTypeEQ(backupjob.BackupType(input.BackupType)),
				backupjob.TriggeredByEQ(input.TriggeredBy),
				backupjob.IdempotencyKeyEQ(strings.TrimSpace(input.IdempotencyKey)),
				backupjob.CreatedAtGTE(now.Add(-idempotencyWindow)),
			).
			Order(ent.Desc(backupjob.FieldCreatedAt), ent.Desc(backupjob.FieldID)).
			First(ctx)
		if err == nil {
			return existing, false, nil
		}
		if !ent.IsNotFound(err) {
			return nil, false, err
		}
	}

	jobID := generateJobID(now)
	builder := s.client.BackupJob.Create().
		SetJobID(jobID).
		SetBackupType(backupjob.BackupType(input.BackupType)).
		SetStatus(backupjob.StatusQueued).
		SetTriggeredBy(strings.TrimSpace(input.TriggeredBy)).
		SetUploadToS3(input.UploadToS3)
	if input.S3ProfileID != "" {
		builder.SetS3ProfileID(input.S3ProfileID)
	}
	if input.PostgresID != "" {
		builder.SetPostgresProfileID(input.PostgresID)
	}
	if input.RedisID != "" {
		builder.SetRedisProfileID(input.RedisID)
	}
	if strings.TrimSpace(input.IdempotencyKey) != "" {
		builder.SetIdempotencyKey(strings.TrimSpace(input.IdempotencyKey))
	}

	job, err := builder.Save(ctx)
	if err != nil {
		return nil, false, err
	}

	_, _ = s.client.BackupJobEvent.Create().
		SetBackupJobID(job.ID).
		SetEventType("state_change").
		SetMessage("job queued").
		Save(ctx)

	return job, true, nil
}

func (s *Store) AcquireNextQueuedJob(ctx context.Context) (*ent.BackupJob, error) {
	for {
		job, err := s.client.BackupJob.Query().
			Where(backupjob.StatusEQ(backupjob.StatusQueued)).
			Order(ent.Asc(backupjob.FieldCreatedAt), ent.Asc(backupjob.FieldID)).
			First(ctx)
		if err != nil {
			return nil, err
		}

		now := time.Now()
		affected, err := s.client.BackupJob.Update().
			Where(
				backupjob.IDEQ(job.ID),
				backupjob.StatusEQ(backupjob.StatusQueued),
			).
			SetStatus(backupjob.StatusRunning).
			SetStartedAt(now).
			ClearFinishedAt().
			ClearErrorMessage().
			Save(ctx)
		if err != nil {
			return nil, err
		}
		if affected == 0 {
			// 并发下被其他 worker 抢占时继续重试下一条 queued 任务。
			continue
		}

		updated, err := s.client.BackupJob.Query().Where(backupjob.IDEQ(job.ID)).First(ctx)
		if err != nil {
			return nil, err
		}

		if err := s.appendJobEventByEntityID(ctx, updated.ID, backupjobevent.LevelInfo, "state_change", "job started", ""); err != nil {
			return nil, err
		}
		return updated, nil
	}
}

func (s *Store) FinishBackupJob(ctx context.Context, input FinishBackupJobInput) (*ent.BackupJob, error) {
	jobID := strings.TrimSpace(input.JobID)
	if jobID == "" {
		return nil, errors.New("job_id is required")
	}
	status, err := parseBackupStatus(strings.TrimSpace(input.Status))
	if err != nil {
		return nil, err
	}

	job, err := s.GetBackupJob(ctx, jobID)
	if err != nil {
		return nil, err
	}

	updater := s.client.BackupJob.UpdateOneID(job.ID).
		SetStatus(status).
		SetFinishedAt(time.Now())
	if strings.TrimSpace(input.ErrorMessage) != "" {
		updater.SetErrorMessage(strings.TrimSpace(input.ErrorMessage))
	} else {
		updater.ClearErrorMessage()
	}
	if input.Artifact != nil {
		updater.SetArtifactLocalPath(strings.TrimSpace(input.Artifact.LocalPath))
		updater.SetArtifactSha256(strings.TrimSpace(input.Artifact.SHA256))
		updater.SetNillableArtifactSizeBytes(&input.Artifact.SizeBytes)
	}
	if input.S3Object != nil {
		updater.SetS3Bucket(strings.TrimSpace(input.S3Object.Bucket))
		updater.SetS3Key(strings.TrimSpace(input.S3Object.Key))
		updater.SetS3Etag(strings.TrimSpace(input.S3Object.ETag))
	}
	updated, err := updater.Save(ctx)
	if err != nil {
		return nil, err
	}

	eventLevel := backupjobevent.LevelInfo
	if status == backupjob.StatusFailed {
		eventLevel = backupjobevent.LevelError
	} else if status == backupjob.StatusPartialSucceeded {
		eventLevel = backupjobevent.LevelWarning
	}
	message := fmt.Sprintf("job finished: %s", status.String())
	if strings.TrimSpace(input.ErrorMessage) != "" {
		message = message + ": " + strings.TrimSpace(input.ErrorMessage)
	}
	if err := s.appendJobEventByEntityID(ctx, updated.ID, eventLevel, "state_change", message, ""); err != nil {
		return nil, err
	}
	return updated, nil
}

func (s *Store) AppendJobEvent(ctx context.Context, jobID, level, eventType, message, payload string) error {
	job, err := s.GetBackupJob(ctx, jobID)
	if err != nil {
		return err
	}
	lv, err := parseEventLevel(level)
	if err != nil {
		return err
	}
	return s.appendJobEventByEntityID(
		ctx,
		job.ID,
		lv,
		strings.TrimSpace(eventType),
		strings.TrimSpace(message),
		strings.TrimSpace(payload),
	)
}

func (s *Store) RequeueRunningJobs(ctx context.Context) (int, error) {
	jobs, err := s.client.BackupJob.Query().
		Where(backupjob.StatusEQ(backupjob.StatusRunning)).
		All(ctx)
	if err != nil {
		return 0, err
	}
	if len(jobs) == 0 {
		return 0, nil
	}

	ids := make([]int, 0, len(jobs))
	for _, item := range jobs {
		ids = append(ids, item.ID)
	}
	affected, err := s.client.BackupJob.Update().
		Where(backupjob.IDIn(ids...)).
		SetStatus(backupjob.StatusQueued).
		ClearFinishedAt().
		SetErrorMessage("job requeued after backupd restart").
		Save(ctx)
	if err != nil {
		return 0, err
	}

	for _, item := range jobs {
		_ = s.appendJobEventByEntityID(
			ctx,
			item.ID,
			backupjobevent.LevelWarning,
			"state_change",
			"job requeued after backupd restart",
			"",
		)
	}
	return affected, nil
}

func (s *Store) ListFinishedJobsForRetention(ctx context.Context) ([]*ent.BackupJob, error) {
	return s.client.BackupJob.Query().
		Where(
			backupjob.Or(
				backupjob.StatusEQ(backupjob.StatusSucceeded),
				backupjob.StatusEQ(backupjob.StatusPartialSucceeded),
			),
			backupjob.ArtifactLocalPathNEQ(""),
		).
		Order(ent.Desc(backupjob.FieldFinishedAt), ent.Desc(backupjob.FieldID)).
		All(ctx)
}

func (s *Store) ListBackupJobs(ctx context.Context, input ListBackupJobsInput) (*ListBackupJobsOutput, error) {
	pageSize := int(input.PageSize)
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 200 {
		pageSize = 200
	}
	offset, err := decodePageToken(input.PageToken)
	if err != nil {
		return nil, err
	}

	query := s.client.BackupJob.Query()
	if strings.TrimSpace(input.Status) != "" {
		query = query.Where(backupjob.StatusEQ(backupjob.Status(strings.TrimSpace(input.Status))))
	}
	if strings.TrimSpace(input.BackupType) != "" {
		query = query.Where(backupjob.BackupTypeEQ(backupjob.BackupType(strings.TrimSpace(input.BackupType))))
	}

	items, err := query.
		Order(ent.Desc(backupjob.FieldCreatedAt), ent.Desc(backupjob.FieldID)).
		Offset(offset).
		Limit(pageSize).
		All(ctx)
	if err != nil {
		return nil, err
	}

	nextToken := ""
	if len(items) == pageSize {
		nextToken = encodePageToken(offset + len(items))
	}
	return &ListBackupJobsOutput{Items: items, NextPageToken: nextToken}, nil
}

func (s *Store) GetBackupJob(ctx context.Context, jobID string) (*ent.BackupJob, error) {
	return s.client.BackupJob.Query().Where(backupjob.JobIDEQ(strings.TrimSpace(jobID))).First(ctx)
}

func (s *Store) getSourceConfigEntity(ctx context.Context, sourceType, profileID string) (*ent.BackupSourceConfig, error) {
	enumType, err := parseSourceType(sourceType)
	if err != nil {
		return nil, err
	}
	normalizedProfileID := strings.TrimSpace(profileID)
	if normalizedProfileID == "" {
		return nil, ErrSourceIDRequired
	}
	return s.client.BackupSourceConfig.Query().
		Where(
			backupsourceconfig.SourceTypeEQ(enumType),
			backupsourceconfig.ProfileIDEQ(normalizedProfileID),
		).
		First(ctx)
}

func (s *Store) getActiveSourceConfigEntity(ctx context.Context, sourceType string) (*ent.BackupSourceConfig, error) {
	enumType, err := parseSourceType(sourceType)
	if err != nil {
		return nil, err
	}
	entity, err := s.client.BackupSourceConfig.Query().
		Where(
			backupsourceconfig.SourceTypeEQ(enumType),
			backupsourceconfig.IsActiveEQ(true),
		).
		Order(ent.Asc(backupsourceconfig.FieldID)).
		First(ctx)
	if err == nil {
		return entity, nil
	}
	if !ent.IsNotFound(err) {
		return nil, err
	}
	return s.client.BackupSourceConfig.Query().
		Where(backupsourceconfig.SourceTypeEQ(enumType)).
		Order(ent.Asc(backupsourceconfig.FieldID)).
		First(ctx)
}

func (s *Store) getActiveS3ConfigEntity(ctx context.Context) (*ent.BackupS3Config, error) {
	entity, err := s.client.BackupS3Config.Query().
		Where(backups3config.IsActiveEQ(true)).
		Order(ent.Asc(backups3config.FieldID)).
		First(ctx)
	if err == nil {
		return entity, nil
	}
	if !ent.IsNotFound(err) {
		return nil, err
	}
	return s.client.BackupS3Config.Query().Order(ent.Asc(backups3config.FieldID)).First(ctx)
}

func toS3ProfileSnapshot(entity *ent.BackupS3Config) *S3ProfileSnapshot {
	if entity == nil {
		return &S3ProfileSnapshot{}
	}
	return &S3ProfileSnapshot{
		ProfileID: entity.ProfileID,
		Name:      entity.Name,
		IsActive:  entity.IsActive,
		S3: S3Config{
			Enabled:         entity.Enabled,
			Endpoint:        entity.Endpoint,
			Region:          entity.Region,
			Bucket:          entity.Bucket,
			AccessKeyID:     entity.AccessKeyID,
			SecretAccessKey: entity.SecretAccessKeyEncrypted,
			Prefix:          entity.Prefix,
			ForcePathStyle:  entity.ForcePathStyle,
			UseSSL:          entity.UseSsl,
		},
		SecretAccessKeyConfigured: strings.TrimSpace(entity.SecretAccessKeyEncrypted) != "",
		CreatedAt:                 entity.CreatedAt,
		UpdatedAt:                 entity.UpdatedAt,
	}
}

func toSourceProfileSnapshot(entity *ent.BackupSourceConfig) *SourceProfileSnapshot {
	if entity == nil {
		return &SourceProfileSnapshot{}
	}
	config := SourceConfig{
		Host:          entity.Host,
		Port:          int32(nillableInt(entity.Port)),
		User:          entity.Username,
		Username:      entity.Username,
		Password:      entity.PasswordEncrypted,
		Database:      entity.Database,
		SSLMode:       entity.SslMode,
		Addr:          entity.Addr,
		DB:            int32(nillableInt(entity.RedisDb)),
		ContainerName: entity.ContainerName,
	}
	return &SourceProfileSnapshot{
		SourceType:         entity.SourceType.String(),
		ProfileID:          entity.ProfileID,
		Name:               entity.Name,
		IsActive:           entity.IsActive,
		Config:             config,
		PasswordConfigured: strings.TrimSpace(entity.PasswordEncrypted) != "",
		CreatedAt:          entity.CreatedAt,
		UpdatedAt:          entity.UpdatedAt,
	}
}

func (s *Store) appendJobEventByEntityID(ctx context.Context, backupJobID int, level backupjobevent.Level, eventType, message, payload string) error {
	eventBuilder := s.client.BackupJobEvent.Create().
		SetBackupJobID(backupJobID).
		SetLevel(level).
		SetEventType(defaultIfBlank(eventType, "state_change")).
		SetMessage(defaultIfBlank(message, "event"))
	if strings.TrimSpace(payload) != "" {
		eventBuilder.SetPayload(strings.TrimSpace(payload))
	}
	_, err := eventBuilder.Save(ctx)
	return err
}

func (s *Store) updateSourceConfigTx(ctx context.Context, tx *ent.Tx, sourceType, profileID string, cfg SourceConfig) error {
	entity, enumType, err := s.resolveSourceEntityTx(ctx, tx, sourceType, profileID)
	if err != nil {
		return err
	}

	updater := tx.BackupSourceConfig.UpdateOneID(entity.ID)
	applySourceConfigUpdate(updater, enumType, cfg)
	_, err = updater.Save(ctx)
	return err
}

func (s *Store) resolveSourceEntityTx(
	ctx context.Context,
	tx *ent.Tx,
	sourceType,
	profileID string,
) (*ent.BackupSourceConfig, backupsourceconfig.SourceType, error) {
	enumType, err := parseSourceType(sourceType)
	if err != nil {
		return nil, "", err
	}
	normalizedProfileID := strings.TrimSpace(profileID)
	if normalizedProfileID != "" {
		entity, queryErr := tx.BackupSourceConfig.Query().
			Where(
				backupsourceconfig.SourceTypeEQ(enumType),
				backupsourceconfig.ProfileIDEQ(normalizedProfileID),
			).
			First(ctx)
		return entity, enumType, queryErr
	}

	entity, queryErr := tx.BackupSourceConfig.Query().
		Where(
			backupsourceconfig.SourceTypeEQ(enumType),
			backupsourceconfig.IsActiveEQ(true),
		).
		Order(ent.Asc(backupsourceconfig.FieldID)).
		First(ctx)
	if queryErr == nil {
		return entity, enumType, nil
	}
	if !ent.IsNotFound(queryErr) {
		return nil, "", queryErr
	}
	entity, queryErr = tx.BackupSourceConfig.Query().
		Where(backupsourceconfig.SourceTypeEQ(enumType)).
		Order(ent.Asc(backupsourceconfig.FieldID)).
		First(ctx)
	return entity, enumType, queryErr
}

func applySourceConfigCreate(builder *ent.BackupSourceConfigCreate, sourceType backupsourceconfig.SourceType, cfg SourceConfig) {
	applySourceConfigCore(sourceType, cfg, func(host, username, database, sslMode, addr, containerName string, port, redisDB *int) {
		builder.
			SetHost(host).
			SetUsername(username).
			SetDatabase(database).
			SetSslMode(sslMode).
			SetAddr(addr).
			SetContainerName(containerName)
		if port != nil {
			builder.SetPort(*port)
		}
		if redisDB != nil {
			builder.SetRedisDb(*redisDB)
		}
	})
	if password := strings.TrimSpace(cfg.Password); password != "" {
		builder.SetPasswordEncrypted(password)
	}
}

func applySourceConfigUpdate(builder *ent.BackupSourceConfigUpdateOne, sourceType backupsourceconfig.SourceType, cfg SourceConfig) {
	applySourceConfigCore(sourceType, cfg, func(host, username, database, sslMode, addr, containerName string, port, redisDB *int) {
		builder.
			SetHost(host).
			SetUsername(username).
			SetDatabase(database).
			SetSslMode(sslMode).
			SetAddr(addr).
			SetContainerName(containerName)
		if port != nil {
			builder.SetPort(*port)
		} else {
			builder.ClearPort()
		}
		if redisDB != nil {
			builder.SetRedisDb(*redisDB)
		} else {
			builder.ClearRedisDb()
		}
	})
	if password := strings.TrimSpace(cfg.Password); password != "" {
		builder.SetPasswordEncrypted(password)
	}
}

func applySourceConfigCore(
	sourceType backupsourceconfig.SourceType,
	cfg SourceConfig,
	apply func(host, username, database, sslMode, addr, containerName string, port, redisDB *int),
) {
	host := strings.TrimSpace(cfg.Host)
	username := strings.TrimSpace(cfg.User)
	if username == "" {
		username = strings.TrimSpace(cfg.Username)
	}
	database := strings.TrimSpace(cfg.Database)
	sslMode := strings.TrimSpace(cfg.SSLMode)
	addr := strings.TrimSpace(cfg.Addr)
	containerName := strings.TrimSpace(cfg.ContainerName)

	var portPtr *int
	if cfg.Port > 0 {
		portValue := int(cfg.Port)
		portPtr = &portValue
	}
	var redisDBPtr *int
	if cfg.DB >= 0 {
		dbValue := int(cfg.DB)
		redisDBPtr = &dbValue
	}

	switch sourceType {
	case backupsourceconfig.SourceTypePostgres:
		if host == "" {
			host = "127.0.0.1"
		}
		if username == "" {
			username = "postgres"
		}
		if database == "" {
			database = "sub2api"
		}
		if sslMode == "" {
			sslMode = "disable"
		}
		if portPtr == nil {
			portValue := 5432
			portPtr = &portValue
		}
	case backupsourceconfig.SourceTypeRedis:
		if addr == "" {
			addr = "127.0.0.1:6379"
		}
		if redisDBPtr == nil {
			dbValue := 0
			redisDBPtr = &dbValue
		}
	}

	apply(host, username, database, sslMode, addr, containerName, portPtr, redisDBPtr)
}

func (s *Store) ensureDefaults(ctx context.Context) error {
	if _, err := s.client.BackupSetting.Query().First(ctx); err != nil {
		if !ent.IsNotFound(err) {
			return err
		}
		if _, err := s.client.BackupSetting.Create().
			SetSourceMode(backupsetting.SourceModeDirect).
			SetBackupRoot("/var/lib/sub2api/backups").
			SetRetentionDays(7).
			SetKeepLast(30).
			SetSqlitePath(s.sqlitePath).
			Save(ctx); err != nil {
			return err
		}
	}

	if err := s.ensureSourceDefaultsByType(ctx, backupsourceconfig.SourceTypePostgres, "默认 PostgreSQL", SourceConfig{
		Host:     "127.0.0.1",
		Port:     5432,
		User:     "postgres",
		Database: "sub2api",
		SSLMode:  "disable",
	}); err != nil {
		return err
	}
	if err := s.ensureSourceDefaultsByType(ctx, backupsourceconfig.SourceTypeRedis, "默认 Redis", SourceConfig{
		Addr: "127.0.0.1:6379",
		DB:   0,
	}); err != nil {
		return err
	}

	profiles, err := s.client.BackupS3Config.Query().
		Order(ent.Asc(backups3config.FieldID)).
		All(ctx)
	if err != nil {
		return err
	}
	if len(profiles) == 0 {
		_, err = s.client.BackupS3Config.Create().
			SetProfileID(defaultS3ProfileID).
			SetName("默认账号").
			SetIsActive(true).
			SetEnabled(false).
			SetEndpoint("").
			SetRegion("").
			SetBucket("").
			SetAccessKeyID("").
			SetPrefix("").
			SetForcePathStyle(false).
			SetUseSsl(true).
			Save(ctx)
		return err
	}

	used := make(map[string]struct{}, len(profiles))
	normalizedIDs := make([]string, len(profiles))
	normalizedNames := make([]string, len(profiles))
	activeIndex := -1
	needFix := false

	for idx, profile := range profiles {
		profileID := strings.TrimSpace(profile.ProfileID)
		if profileID == "" {
			needFix = true
			if idx == 0 {
				profileID = defaultS3ProfileID
			} else {
				profileID = fmt.Sprintf("profile-%d", profile.ID)
			}
		}
		base := profileID
		seq := 2
		for {
			if _, exists := used[profileID]; !exists {
				break
			}
			needFix = true
			profileID = fmt.Sprintf("%s-%d", base, seq)
			seq++
		}
		used[profileID] = struct{}{}
		normalizedIDs[idx] = profileID

		name := strings.TrimSpace(profile.Name)
		if name == "" {
			needFix = true
			name = profileID
		}
		normalizedNames[idx] = name

		if profile.IsActive {
			if activeIndex == -1 {
				activeIndex = idx
			} else {
				needFix = true
			}
		}
	}
	if activeIndex == -1 {
		needFix = true
		activeIndex = 0
	}
	if !needFix {
		return nil
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	for idx, profile := range profiles {
		changed := false
		updater := tx.BackupS3Config.UpdateOneID(profile.ID)

		if profile.ProfileID != normalizedIDs[idx] {
			updater.SetProfileID(normalizedIDs[idx])
			changed = true
		}
		if strings.TrimSpace(profile.Name) != normalizedNames[idx] {
			updater.SetName(normalizedNames[idx])
			changed = true
		}
		shouldActive := idx == activeIndex
		if profile.IsActive != shouldActive {
			updater.SetIsActive(shouldActive)
			changed = true
		}
		if !changed {
			continue
		}
		if _, err = updater.Save(ctx); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (s *Store) ensureSourceDefaultsByType(
	ctx context.Context,
	sourceType backupsourceconfig.SourceType,
	defaultName string,
	defaultCfg SourceConfig,
) error {
	items, err := s.client.BackupSourceConfig.Query().
		Where(backupsourceconfig.SourceTypeEQ(sourceType)).
		Order(ent.Asc(backupsourceconfig.FieldID)).
		All(ctx)
	if err != nil {
		return err
	}

	if len(items) == 0 {
		builder := s.client.BackupSourceConfig.Create().
			SetSourceType(sourceType).
			SetProfileID(defaultSourceID).
			SetName(defaultName).
			SetIsActive(true)
		applySourceConfigCreate(builder, sourceType, defaultCfg)
		_, err = builder.Save(ctx)
		return err
	}

	used := make(map[string]struct{}, len(items))
	normalizedIDs := make([]string, len(items))
	normalizedNames := make([]string, len(items))
	activeIndex := -1
	needFix := false

	for idx, item := range items {
		profileID := strings.TrimSpace(item.ProfileID)
		if profileID == "" {
			needFix = true
			if idx == 0 {
				profileID = defaultSourceID
			} else {
				profileID = fmt.Sprintf("profile-%d", item.ID)
			}
		}
		base := profileID
		seq := 2
		for {
			if _, exists := used[profileID]; !exists {
				break
			}
			needFix = true
			profileID = fmt.Sprintf("%s-%d", base, seq)
			seq++
		}
		used[profileID] = struct{}{}
		normalizedIDs[idx] = profileID

		name := strings.TrimSpace(item.Name)
		if name == "" {
			needFix = true
			name = profileID
		}
		normalizedNames[idx] = name

		if item.IsActive {
			if activeIndex == -1 {
				activeIndex = idx
			} else {
				needFix = true
			}
		}
	}
	if activeIndex == -1 {
		needFix = true
		activeIndex = 0
	}
	if !needFix {
		return nil
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	for idx, item := range items {
		changed := false
		updater := tx.BackupSourceConfig.UpdateOneID(item.ID)

		if item.ProfileID != normalizedIDs[idx] {
			updater.SetProfileID(normalizedIDs[idx])
			changed = true
		}
		if strings.TrimSpace(item.Name) != normalizedNames[idx] {
			updater.SetName(normalizedNames[idx])
			changed = true
		}
		shouldActive := idx == activeIndex
		if item.IsActive != shouldActive {
			updater.SetIsActive(shouldActive)
			changed = true
		}
		if !changed {
			continue
		}
		if _, err = updater.Save(ctx); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

func parseSourceType(sourceType string) (backupsourceconfig.SourceType, error) {
	switch strings.TrimSpace(sourceType) {
	case backupsourceconfig.SourceTypePostgres.String():
		return backupsourceconfig.SourceTypePostgres, nil
	case backupsourceconfig.SourceTypeRedis.String():
		return backupsourceconfig.SourceTypeRedis, nil
	default:
		return "", ErrSourceTypeInvalid
	}
}

func (s *Store) resolveSourceProfileID(ctx context.Context, sourceType, requestedProfileID string) (string, error) {
	requestedProfileID = strings.TrimSpace(requestedProfileID)
	if requestedProfileID != "" {
		entity, err := s.getSourceConfigEntity(ctx, sourceType, requestedProfileID)
		if err != nil {
			return "", err
		}
		return entity.ProfileID, nil
	}

	entity, err := s.getActiveSourceConfigEntity(ctx, sourceType)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(entity.ProfileID), nil
}

func backupTypeNeedsPostgres(backupType string) bool {
	switch strings.TrimSpace(backupType) {
	case backupjob.BackupTypePostgres.String(), backupjob.BackupTypeFull.String():
		return true
	default:
		return false
	}
}

func backupTypeNeedsRedis(backupType string) bool {
	switch strings.TrimSpace(backupType) {
	case backupjob.BackupTypeRedis.String(), backupjob.BackupTypeFull.String():
		return true
	default:
		return false
	}
}

func normalizeSQLitePath(sqlitePath string) string {
	path := strings.TrimSpace(sqlitePath)
	if path == "" {
		return defaultSQLitePath
	}
	return path
}

func sqliteDSN(path string) string {
	dsn := path
	if !strings.HasPrefix(path, "file:") {
		dsn = "file:" + path
	}

	params := make([]string, 0, 2)
	if !strings.Contains(dsn, "_fk=1") {
		params = append(params, "_fk=1")
	}
	if !strings.Contains(dsn, "_pragma=foreign_keys(1)") && !strings.Contains(dsn, "_pragma=foreign_keys%281%29") {
		params = append(params, "_pragma=foreign_keys(1)")
	}
	if len(params) == 0 {
		return dsn
	}
	separator := "?"
	if strings.Contains(dsn, "?") {
		separator = "&"
	}
	return dsn + separator + strings.Join(params, "&")
}

func nillableInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

func generateJobID(now time.Time) string {
	timestamp := now.UTC().Format("20060102_150405")
	suffix := strconv.FormatInt(now.UnixNano()%0xffffff, 16)
	if len(suffix) < 6 {
		suffix = strings.Repeat("0", 6-len(suffix)) + suffix
	}
	return fmt.Sprintf("bk_%s_%s", timestamp, suffix)
}

func decodePageToken(token string) (int, error) {
	t := strings.TrimSpace(token)
	if t == "" {
		return 0, nil
	}
	raw, err := base64.StdEncoding.DecodeString(t)
	if err != nil {
		return 0, err
	}
	offset, err := strconv.Atoi(string(raw))
	if err != nil {
		return 0, err
	}
	if offset < 0 {
		return 0, errors.New("negative page token")
	}
	return offset, nil
}

func encodePageToken(offset int) string {
	if offset <= 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString([]byte(strconv.Itoa(offset)))
}

func parseBackupStatus(v string) (backupjob.Status, error) {
	switch strings.TrimSpace(v) {
	case backupjob.StatusQueued.String():
		return backupjob.StatusQueued, nil
	case backupjob.StatusRunning.String():
		return backupjob.StatusRunning, nil
	case backupjob.StatusSucceeded.String():
		return backupjob.StatusSucceeded, nil
	case backupjob.StatusFailed.String():
		return backupjob.StatusFailed, nil
	case backupjob.StatusPartialSucceeded.String():
		return backupjob.StatusPartialSucceeded, nil
	default:
		return "", fmt.Errorf("invalid backup status: %s", v)
	}
}

func parseEventLevel(v string) (backupjobevent.Level, error) {
	switch strings.TrimSpace(v) {
	case "", backupjobevent.LevelInfo.String():
		return backupjobevent.LevelInfo, nil
	case backupjobevent.LevelWarning.String():
		return backupjobevent.LevelWarning, nil
	case backupjobevent.LevelError.String():
		return backupjobevent.LevelError, nil
	default:
		return "", fmt.Errorf("invalid event level: %s", v)
	}
}

func defaultIfBlank(v, fallback string) string {
	trimmed := strings.TrimSpace(v)
	if trimmed == "" {
		return fallback
	}
	return trimmed
}
