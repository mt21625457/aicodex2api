package entstore

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/backup/ent/backupjob"
	"github.com/stretchr/testify/require"
)

func TestStore_CreateAcquireFinishBackupJob(t *testing.T) {
	store := openTestStore(t)

	job, created, err := store.CreateBackupJob(context.Background(), CreateBackupJobInput{
		BackupType:     backupjob.BackupTypePostgres.String(),
		UploadToS3:     true,
		TriggeredBy:    "admin:1",
		IdempotencyKey: "idem-1",
	})
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, backupjob.StatusQueued, job.Status)

	acquired, err := store.AcquireNextQueuedJob(context.Background())
	require.NoError(t, err)
	require.Equal(t, job.JobID, acquired.JobID)
	require.Equal(t, backupjob.StatusRunning, acquired.Status)
	require.NotNil(t, acquired.StartedAt)

	size := int64(1024)
	finished, err := store.FinishBackupJob(context.Background(), FinishBackupJobInput{
		JobID:  acquired.JobID,
		Status: backupjob.StatusSucceeded.String(),
		Artifact: &BackupArtifactSnapshot{
			LocalPath: "/tmp/demo/bundle.tar.gz",
			SizeBytes: size,
			SHA256:    "sha256-demo",
		},
		S3Object: &BackupS3ObjectSnapshot{
			Bucket: "bucket-demo",
			Key:    "demo/key",
			ETag:   "etag-demo",
		},
	})
	require.NoError(t, err)
	require.Equal(t, backupjob.StatusSucceeded, finished.Status)
	require.NotNil(t, finished.FinishedAt)
	require.Equal(t, "/tmp/demo/bundle.tar.gz", finished.ArtifactLocalPath)
	require.Equal(t, "sha256-demo", finished.ArtifactSha256)
	require.NotNil(t, finished.ArtifactSizeBytes)
	require.Equal(t, size, *finished.ArtifactSizeBytes)
	require.Equal(t, "bucket-demo", finished.S3Bucket)
	require.Equal(t, "demo/key", finished.S3Key)
}

func TestStore_CreateBackupJob_Idempotency(t *testing.T) {
	store := openTestStore(t)

	first, created, err := store.CreateBackupJob(context.Background(), CreateBackupJobInput{
		BackupType:     backupjob.BackupTypeRedis.String(),
		UploadToS3:     false,
		TriggeredBy:    "admin:2",
		IdempotencyKey: "idem-same",
	})
	require.NoError(t, err)
	require.True(t, created)

	second, created, err := store.CreateBackupJob(context.Background(), CreateBackupJobInput{
		BackupType:     backupjob.BackupTypeRedis.String(),
		UploadToS3:     false,
		TriggeredBy:    "admin:2",
		IdempotencyKey: "idem-same",
	})
	require.NoError(t, err)
	require.False(t, created)
	require.Equal(t, first.JobID, second.JobID)
}

func TestStore_RequeueRunningJobs(t *testing.T) {
	store := openTestStore(t)

	_, _, err := store.CreateBackupJob(context.Background(), CreateBackupJobInput{
		BackupType:  backupjob.BackupTypeFull.String(),
		UploadToS3:  false,
		TriggeredBy: "admin:3",
	})
	require.NoError(t, err)

	acquired, err := store.AcquireNextQueuedJob(context.Background())
	require.NoError(t, err)
	require.Equal(t, backupjob.StatusRunning, acquired.Status)

	count, err := store.RequeueRunningJobs(context.Background())
	require.NoError(t, err)
	require.Equal(t, 1, count)

	job, err := store.GetBackupJob(context.Background(), acquired.JobID)
	require.NoError(t, err)
	require.Equal(t, backupjob.StatusQueued, job.Status)
	require.Equal(t, "job requeued after backupd restart", job.ErrorMessage)
}

func TestStore_UpdateConfig_KeepSecretWhenEmpty(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	cfg, err := store.GetConfig(ctx)
	require.NoError(t, err)
	cfg.SourceMode = "direct"
	cfg.BackupRoot = filepath.Join(t.TempDir(), "backups")
	cfg.SQLitePath = filepath.Join(t.TempDir(), "meta.db")
	cfg.RetentionDays = 7
	cfg.KeepLast = 30
	cfg.Postgres.Password = "pg-secret"
	cfg.Redis.Password = "redis-secret"
	cfg.S3.SecretAccessKey = "s3-secret"
	cfg.S3.Region = "us-east-1"
	cfg.S3.Bucket = "demo-bucket"
	cfg.S3.AccessKeyID = "demo-ak"
	_, err = store.UpdateConfig(ctx, *cfg)
	require.NoError(t, err)

	cfg2, err := store.GetConfig(ctx)
	require.NoError(t, err)
	cfg2.Postgres.Password = ""
	cfg2.Redis.Password = ""
	cfg2.S3.SecretAccessKey = ""
	_, err = store.UpdateConfig(ctx, *cfg2)
	require.NoError(t, err)

	finalCfg, err := store.GetConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, "pg-secret", finalCfg.Postgres.Password)
	require.Equal(t, "redis-secret", finalCfg.Redis.Password)
	require.Equal(t, "s3-secret", finalCfg.S3.SecretAccessKey)
}

func TestStore_S3ProfilesLifecycle(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	initialProfiles, err := store.ListS3Profiles(ctx)
	require.NoError(t, err)
	require.Len(t, initialProfiles, 1)
	require.Equal(t, defaultS3ProfileID, initialProfiles[0].ProfileID)
	require.True(t, initialProfiles[0].IsActive)

	created, err := store.CreateS3Profile(ctx, CreateS3ProfileInput{
		ProfileID: "archive",
		Name:      "归档账号",
		S3: S3Config{
			Enabled:         true,
			Region:          "us-east-1",
			Bucket:          "archive-bucket",
			AccessKeyID:     "archive-ak",
			SecretAccessKey: "archive-sk",
			UseSSL:          true,
		},
		SetActive: false,
	})
	require.NoError(t, err)
	require.Equal(t, "archive", created.ProfileID)
	require.False(t, created.IsActive)
	require.True(t, created.SecretAccessKeyConfigured)

	updated, err := store.UpdateS3Profile(ctx, UpdateS3ProfileInput{
		ProfileID: "archive",
		Name:      "归档账号-更新",
		S3: S3Config{
			Enabled:         true,
			Region:          "us-east-1",
			Bucket:          "archive-bucket-updated",
			AccessKeyID:     "archive-ak-2",
			SecretAccessKey: "",
			UseSSL:          true,
		},
	})
	require.NoError(t, err)
	require.Equal(t, "归档账号-更新", updated.Name)
	require.Equal(t, "archive-ak-2", updated.S3.AccessKeyID)
	require.Equal(t, "archive-sk", updated.S3.SecretAccessKey)

	active, err := store.SetActiveS3Profile(ctx, "archive")
	require.NoError(t, err)
	require.True(t, active.IsActive)

	cfg, err := store.GetConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, "archive", cfg.ActiveS3ProfileID)
	require.Equal(t, "archive-bucket-updated", cfg.S3.Bucket)

	err = store.DeleteS3Profile(ctx, "archive")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrActiveS3Profile))

	_, err = store.SetActiveS3Profile(ctx, defaultS3ProfileID)
	require.NoError(t, err)
	require.NoError(t, store.DeleteS3Profile(ctx, "archive"))
}

func TestStore_DeleteS3ProfileInUse(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	_, err := store.CreateS3Profile(ctx, CreateS3ProfileInput{
		ProfileID: "for-job",
		Name:      "任务账号",
		S3: S3Config{
			Enabled: true,
			Region:  "us-east-1",
			Bucket:  "job-bucket",
			UseSSL:  true,
		},
	})
	require.NoError(t, err)

	_, _, err = store.CreateBackupJob(ctx, CreateBackupJobInput{
		BackupType:  backupjob.BackupTypePostgres.String(),
		UploadToS3:  true,
		TriggeredBy: "admin:9",
		S3ProfileID: "for-job",
	})
	require.NoError(t, err)

	err = store.DeleteS3Profile(ctx, "for-job")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrS3ProfileInUse))
}

func TestStore_SourceProfilesLifecycle(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	initialPG, err := store.ListSourceProfiles(ctx, "postgres")
	require.NoError(t, err)
	require.Len(t, initialPG, 1)
	require.Equal(t, defaultSourceID, initialPG[0].ProfileID)
	require.True(t, initialPG[0].IsActive)

	created, err := store.CreateSourceProfile(ctx, CreateSourceProfileInput{
		SourceType: "postgres",
		ProfileID:  "pg-reporting",
		Name:       "报表库",
		Config: SourceConfig{
			Host:     "10.0.0.10",
			Port:     15432,
			User:     "report_user",
			Password: "secret",
			Database: "reporting",
			SSLMode:  "require",
		},
		SetActive: false,
	})
	require.NoError(t, err)
	require.Equal(t, "pg-reporting", created.ProfileID)
	require.False(t, created.IsActive)
	require.True(t, created.PasswordConfigured)

	active, err := store.SetActiveSourceProfile(ctx, "postgres", "pg-reporting")
	require.NoError(t, err)
	require.True(t, active.IsActive)

	cfg, err := store.GetConfig(ctx)
	require.NoError(t, err)
	require.Equal(t, "pg-reporting", cfg.ActivePostgresID)
	require.Equal(t, "10.0.0.10", cfg.Postgres.Host)
	require.Equal(t, int32(15432), cfg.Postgres.Port)

	err = store.DeleteSourceProfile(ctx, "postgres", "pg-reporting")
	require.Error(t, err)
	require.True(t, errors.Is(err, ErrSourceActive))

	_, err = store.SetActiveSourceProfile(ctx, "postgres", defaultSourceID)
	require.NoError(t, err)
	require.NoError(t, store.DeleteSourceProfile(ctx, "postgres", "pg-reporting"))
}

func TestStore_CreateBackupJob_WithSelectedSourceProfiles(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	_, err := store.CreateSourceProfile(ctx, CreateSourceProfileInput{
		SourceType: "postgres",
		ProfileID:  "pg-custom",
		Name:       "自定义PG",
		Config: SourceConfig{
			Host:     "127.0.0.2",
			Port:     6432,
			User:     "custom_user",
			Database: "custom_db",
			SSLMode:  "disable",
		},
	})
	require.NoError(t, err)

	_, err = store.CreateSourceProfile(ctx, CreateSourceProfileInput{
		SourceType: "redis",
		ProfileID:  "redis-custom",
		Name:       "自定义Redis",
		Config: SourceConfig{
			Addr: "127.0.0.3:6380",
			DB:   5,
		},
	})
	require.NoError(t, err)

	job, created, err := store.CreateBackupJob(ctx, CreateBackupJobInput{
		BackupType:  backupjob.BackupTypeFull.String(),
		TriggeredBy: "admin:10",
		PostgresID:  "pg-custom",
		RedisID:     "redis-custom",
	})
	require.NoError(t, err)
	require.True(t, created)
	require.Equal(t, "pg-custom", job.PostgresProfileID)
	require.Equal(t, "redis-custom", job.RedisProfileID)
}

func TestStore_CreateBackupJob_IgnoreUnusedProfilesAndS3(t *testing.T) {
	store := openTestStore(t)
	ctx := context.Background()

	pgJob, created, err := store.CreateBackupJob(ctx, CreateBackupJobInput{
		BackupType:  backupjob.BackupTypePostgres.String(),
		TriggeredBy: "admin:11",
		RedisID:     "redis-should-be-ignored",
	})
	require.NoError(t, err)
	require.True(t, created)
	require.Empty(t, pgJob.RedisProfileID)
	require.NotEmpty(t, pgJob.PostgresProfileID)

	redisJob, created, err := store.CreateBackupJob(ctx, CreateBackupJobInput{
		BackupType:  backupjob.BackupTypeRedis.String(),
		TriggeredBy: "admin:12",
		PostgresID:  "postgres-should-be-ignored",
	})
	require.NoError(t, err)
	require.True(t, created)
	require.Empty(t, redisJob.PostgresProfileID)
	require.NotEmpty(t, redisJob.RedisProfileID)

	noS3Job, created, err := store.CreateBackupJob(ctx, CreateBackupJobInput{
		BackupType:  backupjob.BackupTypePostgres.String(),
		TriggeredBy: "admin:13",
		UploadToS3:  false,
		S3ProfileID: "missing-profile",
	})
	require.NoError(t, err)
	require.True(t, created)
	require.Empty(t, noS3Job.S3ProfileID)

	_, _, err = store.CreateBackupJob(ctx, CreateBackupJobInput{
		BackupType:  backupjob.BackupTypePostgres.String(),
		TriggeredBy: "admin:14",
		UploadToS3:  true,
		S3ProfileID: "missing-profile",
	})
	require.Error(t, err)
}

func openTestStore(t *testing.T) *Store {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "backupd-test-"+time.Now().Format("150405.000")+".db")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	store, err := Open(ctx, dbPath)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = store.Close()
	})
	return store
}
