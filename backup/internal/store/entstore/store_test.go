package entstore

import (
	"context"
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
