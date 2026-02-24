package grpcserver

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/backup/ent"
	"github.com/Wei-Shaw/sub2api/backup/ent/backupjob"
	"github.com/Wei-Shaw/sub2api/backup/internal/s3client"
	"github.com/Wei-Shaw/sub2api/backup/internal/store/entstore"
	backupv1 "github.com/Wei-Shaw/sub2api/backup/proto/backup/v1"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type Server struct {
	backupv1.UnimplementedBackupServiceServer
	store     *entstore.Store
	startedAt time.Time
	version   string
	notifier  queueNotifier
}

type queueNotifier interface {
	Notify()
}

func New(store *entstore.Store, version string, notifier queueNotifier) *Server {
	if strings.TrimSpace(version) == "" {
		version = "dev"
	}
	return &Server{
		store:     store,
		startedAt: time.Now(),
		version:   version,
		notifier:  notifier,
	}
}

func (s *Server) Health(_ context.Context, _ *backupv1.HealthRequest) (*backupv1.HealthResponse, error) {
	return &backupv1.HealthResponse{
		Status:        "SERVING",
		Version:       s.version,
		UptimeSeconds: int64(time.Since(s.startedAt).Seconds()),
	}, nil
}

func (s *Server) GetConfig(ctx context.Context, _ *backupv1.GetConfigRequest) (*backupv1.GetConfigResponse, error) {
	cfg, err := s.store.GetConfig(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "load config failed: %v", err)
	}
	return &backupv1.GetConfigResponse{Config: toProtoConfig(cfg)}, nil
}

func (s *Server) UpdateConfig(ctx context.Context, req *backupv1.UpdateConfigRequest) (*backupv1.UpdateConfigResponse, error) {
	if req == nil || req.GetConfig() == nil {
		return nil, status.Error(codes.InvalidArgument, "config is required")
	}
	cfg := req.GetConfig()
	if err := validateConfig(cfg); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	updated, err := s.store.UpdateConfig(ctx, fromProtoConfig(cfg))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "update config failed: %v", err)
	}
	return &backupv1.UpdateConfigResponse{Config: toProtoConfig(updated)}, nil
}

func (s *Server) ValidateS3(ctx context.Context, req *backupv1.ValidateS3Request) (*backupv1.ValidateS3Response, error) {
	if req == nil || req.GetS3() == nil {
		return nil, status.Error(codes.InvalidArgument, "s3 config is required")
	}
	s3Cfg := req.GetS3()
	if strings.TrimSpace(s3Cfg.GetBucket()) == "" {
		return nil, status.Error(codes.InvalidArgument, "s3.bucket is required")
	}
	if strings.TrimSpace(s3Cfg.GetRegion()) == "" {
		return nil, status.Error(codes.InvalidArgument, "s3.region is required")
	}

	client, err := s3client.New(ctx, s3client.Config{
		Endpoint:        strings.TrimSpace(s3Cfg.GetEndpoint()),
		Region:          strings.TrimSpace(s3Cfg.GetRegion()),
		AccessKeyID:     strings.TrimSpace(s3Cfg.GetAccessKeyId()),
		SecretAccessKey: strings.TrimSpace(s3Cfg.GetSecretAccessKey()),
		Bucket:          strings.TrimSpace(s3Cfg.GetBucket()),
		Prefix:          strings.Trim(strings.TrimSpace(s3Cfg.GetPrefix()), "/"),
		ForcePathStyle:  s3Cfg.GetForcePathStyle(),
		UseSSL:          s3Cfg.GetUseSsl(),
	})
	if err != nil {
		return &backupv1.ValidateS3Response{Ok: false, Message: err.Error()}, nil
	}

	_, err = client.Raw().HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(strings.TrimSpace(s3Cfg.GetBucket()))})
	if err != nil {
		return &backupv1.ValidateS3Response{Ok: false, Message: err.Error()}, nil
	}
	return &backupv1.ValidateS3Response{Ok: true, Message: "ok"}, nil
}

func (s *Server) CreateBackupJob(ctx context.Context, req *backupv1.CreateBackupJobRequest) (*backupv1.CreateBackupJobResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	backupType := strings.TrimSpace(req.GetBackupType())
	if !isValidBackupType(backupType) {
		return nil, status.Error(codes.InvalidArgument, "invalid backup_type")
	}

	job, created, err := s.store.CreateBackupJob(ctx, entstore.CreateBackupJobInput{
		BackupType:     backupType,
		UploadToS3:     req.GetUploadToS3(),
		TriggeredBy:    strings.TrimSpace(req.GetTriggeredBy()),
		IdempotencyKey: strings.TrimSpace(req.GetIdempotencyKey()),
	})
	if err != nil {
		return nil, status.Errorf(codes.Internal, "create backup job failed: %v", err)
	}
	if created && s.notifier != nil {
		s.notifier.Notify()
	}
	return &backupv1.CreateBackupJobResponse{Job: toProtoJob(job)}, nil
}

func (s *Server) ListBackupJobs(ctx context.Context, req *backupv1.ListBackupJobsRequest) (*backupv1.ListBackupJobsResponse, error) {
	if req == nil {
		req = &backupv1.ListBackupJobsRequest{}
	}
	statusFilter := strings.TrimSpace(req.GetStatus())
	if statusFilter != "" && !isValidBackupStatus(statusFilter) {
		return nil, status.Error(codes.InvalidArgument, "invalid status filter")
	}
	backupType := strings.TrimSpace(req.GetBackupType())
	if backupType != "" && !isValidBackupType(backupType) {
		return nil, status.Error(codes.InvalidArgument, "invalid backup_type filter")
	}

	out, err := s.store.ListBackupJobs(ctx, entstore.ListBackupJobsInput{
		PageSize:   req.GetPageSize(),
		PageToken:  req.GetPageToken(),
		Status:     statusFilter,
		BackupType: backupType,
	})
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "list backup jobs failed: %v", err)
	}

	items := make([]*backupv1.BackupJob, 0, len(out.Items))
	for _, item := range out.Items {
		items = append(items, toProtoJob(item))
	}
	return &backupv1.ListBackupJobsResponse{Items: items, NextPageToken: out.NextPageToken}, nil
}

func (s *Server) GetBackupJob(ctx context.Context, req *backupv1.GetBackupJobRequest) (*backupv1.GetBackupJobResponse, error) {
	if req == nil || strings.TrimSpace(req.GetJobId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "job_id is required")
	}
	job, err := s.store.GetBackupJob(ctx, req.GetJobId())
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, status.Error(codes.NotFound, "backup job not found")
		}
		return nil, status.Errorf(codes.Internal, "get backup job failed: %v", err)
	}
	return &backupv1.GetBackupJobResponse{Job: toProtoJob(job)}, nil
}

func validateConfig(cfg *backupv1.BackupConfig) error {
	sourceMode := strings.TrimSpace(cfg.GetSourceMode())
	if sourceMode != "direct" && sourceMode != "docker_exec" {
		return errors.New("source_mode must be direct or docker_exec")
	}
	if strings.TrimSpace(cfg.GetBackupRoot()) == "" {
		return errors.New("backup_root is required")
	}
	if cfg.GetRetentionDays() <= 0 {
		return errors.New("retention_days must be > 0")
	}
	if cfg.GetKeepLast() <= 0 {
		return errors.New("keep_last must be > 0")
	}
	if cfg.GetPostgres() == nil {
		return errors.New("postgres config is required")
	}
	if cfg.GetRedis() == nil {
		return errors.New("redis config is required")
	}
	if cfg.GetS3() == nil {
		return errors.New("s3 config is required")
	}
	return nil
}

func isValidBackupType(v string) bool {
	switch v {
	case backupjob.BackupTypePostgres.String(), backupjob.BackupTypeRedis.String(), backupjob.BackupTypeFull.String():
		return true
	default:
		return false
	}
}

func isValidBackupStatus(v string) bool {
	switch v {
	case backupjob.StatusQueued.String(),
		backupjob.StatusRunning.String(),
		backupjob.StatusSucceeded.String(),
		backupjob.StatusFailed.String(),
		backupjob.StatusPartialSucceeded.String():
		return true
	default:
		return false
	}
}

func fromProtoConfig(cfg *backupv1.BackupConfig) entstore.ConfigSnapshot {
	postgres := cfg.GetPostgres()
	redis := cfg.GetRedis()
	s3Cfg := cfg.GetS3()
	return entstore.ConfigSnapshot{
		SourceMode:    strings.TrimSpace(cfg.GetSourceMode()),
		BackupRoot:    strings.TrimSpace(cfg.GetBackupRoot()),
		SQLitePath:    strings.TrimSpace(cfg.GetSqlitePath()),
		RetentionDays: cfg.GetRetentionDays(),
		KeepLast:      cfg.GetKeepLast(),
		Postgres: entstore.SourceConfig{
			Host:          strings.TrimSpace(postgres.GetHost()),
			Port:          postgres.GetPort(),
			User:          strings.TrimSpace(postgres.GetUser()),
			Password:      strings.TrimSpace(postgres.GetPassword()),
			Database:      strings.TrimSpace(postgres.GetDatabase()),
			SSLMode:       strings.TrimSpace(postgres.GetSslMode()),
			ContainerName: strings.TrimSpace(postgres.GetContainerName()),
		},
		Redis: entstore.SourceConfig{
			Addr:          strings.TrimSpace(redis.GetAddr()),
			Username:      strings.TrimSpace(redis.GetUsername()),
			Password:      strings.TrimSpace(redis.GetPassword()),
			DB:            redis.GetDb(),
			ContainerName: strings.TrimSpace(redis.GetContainerName()),
		},
		S3: entstore.S3Config{
			Enabled:         s3Cfg.GetEnabled(),
			Endpoint:        strings.TrimSpace(s3Cfg.GetEndpoint()),
			Region:          strings.TrimSpace(s3Cfg.GetRegion()),
			Bucket:          strings.TrimSpace(s3Cfg.GetBucket()),
			AccessKeyID:     strings.TrimSpace(s3Cfg.GetAccessKeyId()),
			SecretAccessKey: strings.TrimSpace(s3Cfg.GetSecretAccessKey()),
			Prefix:          strings.Trim(strings.TrimSpace(s3Cfg.GetPrefix()), "/"),
			ForcePathStyle:  s3Cfg.GetForcePathStyle(),
			UseSSL:          s3Cfg.GetUseSsl(),
		},
	}
}

func toProtoConfig(cfg *entstore.ConfigSnapshot) *backupv1.BackupConfig {
	if cfg == nil {
		return &backupv1.BackupConfig{}
	}
	return &backupv1.BackupConfig{
		SourceMode:    cfg.SourceMode,
		BackupRoot:    cfg.BackupRoot,
		SqlitePath:    cfg.SQLitePath,
		RetentionDays: cfg.RetentionDays,
		KeepLast:      cfg.KeepLast,
		Postgres: &backupv1.SourceConfig{
			Host:          cfg.Postgres.Host,
			Port:          cfg.Postgres.Port,
			User:          cfg.Postgres.User,
			Password:      cfg.Postgres.Password,
			Database:      cfg.Postgres.Database,
			SslMode:       cfg.Postgres.SSLMode,
			ContainerName: cfg.Postgres.ContainerName,
		},
		Redis: &backupv1.SourceConfig{
			Addr:          cfg.Redis.Addr,
			Username:      cfg.Redis.Username,
			Password:      cfg.Redis.Password,
			Db:            cfg.Redis.DB,
			ContainerName: cfg.Redis.ContainerName,
		},
		S3: &backupv1.S3Config{
			Enabled:         cfg.S3.Enabled,
			Endpoint:        cfg.S3.Endpoint,
			Region:          cfg.S3.Region,
			Bucket:          cfg.S3.Bucket,
			AccessKeyId:     cfg.S3.AccessKeyID,
			SecretAccessKey: cfg.S3.SecretAccessKey,
			Prefix:          cfg.S3.Prefix,
			ForcePathStyle:  cfg.S3.ForcePathStyle,
			UseSsl:          cfg.S3.UseSSL,
		},
	}
}

func toProtoJob(job *ent.BackupJob) *backupv1.BackupJob {
	if job == nil {
		return &backupv1.BackupJob{}
	}
	out := &backupv1.BackupJob{
		JobId:          job.JobID,
		BackupType:     job.BackupType.String(),
		Status:         job.Status.String(),
		TriggeredBy:    job.TriggeredBy,
		IdempotencyKey: job.IdempotencyKey,
		UploadToS3:     job.UploadToS3,
		ErrorMessage:   job.ErrorMessage,
		Artifact: &backupv1.BackupArtifact{
			LocalPath: job.ArtifactLocalPath,
			SizeBytes: nillableInt64(job.ArtifactSizeBytes),
			Sha256:    job.ArtifactSha256,
		},
		S3Object: &backupv1.BackupS3Object{
			Bucket: job.S3Bucket,
			Key:    job.S3Key,
			Etag:   job.S3Etag,
		},
	}
	if job.StartedAt != nil {
		out.StartedAt = job.StartedAt.UTC().Format(time.RFC3339)
	}
	if job.FinishedAt != nil {
		out.FinishedAt = job.FinishedAt.UTC().Format(time.RFC3339)
	}
	return out
}

func nillableInt64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}
