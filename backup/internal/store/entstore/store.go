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
	defaultSQLitePath = "/tmp/sub2api-backupd.db"
	idempotencyWindow = 10 * time.Minute
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
	SourceMode    string
	BackupRoot    string
	SQLitePath    string
	RetentionDays int32
	KeepLast      int32
	Postgres      SourceConfig
	Redis         SourceConfig
	S3            S3Config
}

type CreateBackupJobInput struct {
	BackupType     string
	UploadToS3     bool
	TriggeredBy    string
	IdempotencyKey string
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
	postgresCfg, err := s.getSourceConfig(ctx, backupsourceconfig.SourceTypePostgres)
	if err != nil {
		return nil, err
	}
	redisCfg, err := s.getSourceConfig(ctx, backupsourceconfig.SourceTypeRedis)
	if err != nil {
		return nil, err
	}
	s3Cfg, err := s.client.BackupS3Config.Query().Order(ent.Asc(backups3config.FieldID)).First(ctx)
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

	if err = s.upsertSourceConfigTx(ctx, tx, backupsourceconfig.SourceTypePostgres, cfg.Postgres); err != nil {
		return nil, err
	}
	if err = s.upsertSourceConfigTx(ctx, tx, backupsourceconfig.SourceTypeRedis, cfg.Redis); err != nil {
		return nil, err
	}

	s3Entity, err := tx.BackupS3Config.Query().Order(ent.Asc(backups3config.FieldID)).First(ctx)
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

func (s *Store) CreateBackupJob(ctx context.Context, input CreateBackupJobInput) (*ent.BackupJob, bool, error) {
	if strings.TrimSpace(input.TriggeredBy) == "" {
		input.TriggeredBy = "admin:unknown"
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
	job, err := s.client.BackupJob.Query().
		Where(backupjob.StatusEQ(backupjob.StatusQueued)).
		Order(ent.Asc(backupjob.FieldCreatedAt), ent.Asc(backupjob.FieldID)).
		First(ctx)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	updated, err := s.client.BackupJob.UpdateOneID(job.ID).
		SetStatus(backupjob.StatusRunning).
		SetStartedAt(now).
		ClearFinishedAt().
		ClearErrorMessage().
		Save(ctx)
	if err != nil {
		return nil, err
	}

	if err := s.appendJobEventByEntityID(ctx, updated.ID, backupjobevent.LevelInfo, "state_change", "job started", ""); err != nil {
		return nil, err
	}
	return updated, nil
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

func (s *Store) getSourceConfig(ctx context.Context, sourceType backupsourceconfig.SourceType) (*ent.BackupSourceConfig, error) {
	return s.client.BackupSourceConfig.Query().Where(backupsourceconfig.SourceTypeEQ(sourceType)).First(ctx)
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

func (s *Store) upsertSourceConfigTx(ctx context.Context, tx *ent.Tx, sourceType backupsourceconfig.SourceType, cfg SourceConfig) error {
	entity, err := tx.BackupSourceConfig.Query().Where(backupsourceconfig.SourceTypeEQ(sourceType)).First(ctx)
	if err != nil {
		return err
	}

	updater := tx.BackupSourceConfig.UpdateOneID(entity.ID).
		SetHost(strings.TrimSpace(cfg.Host)).
		SetPort(int(cfg.Port)).
		SetUsername(strings.TrimSpace(cfg.User)).
		SetDatabase(strings.TrimSpace(cfg.Database)).
		SetSslMode(strings.TrimSpace(cfg.SSLMode)).
		SetAddr(strings.TrimSpace(cfg.Addr)).
		SetRedisDb(int(cfg.DB)).
		SetContainerName(strings.TrimSpace(cfg.ContainerName))
	if strings.TrimSpace(cfg.Username) != "" {
		updater.SetUsername(strings.TrimSpace(cfg.Username))
	}
	if strings.TrimSpace(cfg.Password) != "" {
		updater.SetPasswordEncrypted(strings.TrimSpace(cfg.Password))
	}
	_, err = updater.Save(ctx)
	return err
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

	if _, err := s.getSourceConfig(ctx, backupsourceconfig.SourceTypePostgres); err != nil {
		if !ent.IsNotFound(err) {
			return err
		}
		if _, err := s.client.BackupSourceConfig.Create().
			SetSourceType(backupsourceconfig.SourceTypePostgres).
			SetHost("127.0.0.1").
			SetPort(5432).
			SetUsername("postgres").
			SetDatabase("sub2api").
			SetSslMode("disable").
			SetContainerName("").
			Save(ctx); err != nil {
			return err
		}
	}

	if _, err := s.getSourceConfig(ctx, backupsourceconfig.SourceTypeRedis); err != nil {
		if !ent.IsNotFound(err) {
			return err
		}
		if _, err := s.client.BackupSourceConfig.Create().
			SetSourceType(backupsourceconfig.SourceTypeRedis).
			SetAddr("127.0.0.1:6379").
			SetRedisDb(0).
			SetContainerName("").
			Save(ctx); err != nil {
			return err
		}
	}

	if _, err := s.client.BackupS3Config.Query().First(ctx); err != nil {
		if !ent.IsNotFound(err) {
			return err
		}
		if _, err := s.client.BackupS3Config.Create().
			SetEnabled(false).
			SetEndpoint("").
			SetRegion("").
			SetBucket("").
			SetAccessKeyID("").
			SetPrefix("").
			SetForcePathStyle(false).
			SetUseSsl(true).
			Save(ctx); err != nil {
			return err
		}
	}
	return nil
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
