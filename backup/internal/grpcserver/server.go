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

func (s *Server) ListSourceProfiles(ctx context.Context, req *backupv1.ListSourceProfilesRequest) (*backupv1.ListSourceProfilesResponse, error) {
	sourceType := ""
	if req != nil {
		sourceType = strings.TrimSpace(req.GetSourceType())
	}
	if sourceType == "" {
		return nil, status.Error(codes.InvalidArgument, "source_type is required")
	}

	items, err := s.store.ListSourceProfiles(ctx, sourceType)
	if err != nil {
		if mapped := mapSourceProfileStoreError(err); mapped != nil {
			return nil, mapped
		}
		return nil, status.Errorf(codes.Internal, "list source profiles failed: %v", err)
	}

	out := make([]*backupv1.SourceProfile, 0, len(items))
	for _, item := range items {
		out = append(out, toProtoSourceProfile(item))
	}
	return &backupv1.ListSourceProfilesResponse{Items: out}, nil
}

func (s *Server) CreateSourceProfile(ctx context.Context, req *backupv1.CreateSourceProfileRequest) (*backupv1.CreateSourceProfileResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	if err := validateSourceProfileRequest(req.GetSourceType(), req.GetProfileId(), req.GetName(), req.GetConfig()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	profile, err := s.store.CreateSourceProfile(ctx, entstore.CreateSourceProfileInput{
		SourceType: strings.TrimSpace(req.GetSourceType()),
		ProfileID:  strings.TrimSpace(req.GetProfileId()),
		Name:       strings.TrimSpace(req.GetName()),
		Config:     fromProtoSourceConfig(req.GetConfig()),
		SetActive:  req.GetSetActive(),
	})
	if err != nil {
		if mapped := mapSourceProfileStoreError(err); mapped != nil {
			return nil, mapped
		}
		return nil, status.Errorf(codes.Internal, "create source profile failed: %v", err)
	}
	return &backupv1.CreateSourceProfileResponse{Profile: toProtoSourceProfile(profile)}, nil
}

func (s *Server) UpdateSourceProfile(ctx context.Context, req *backupv1.UpdateSourceProfileRequest) (*backupv1.UpdateSourceProfileResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	if err := validateSourceProfileRequest(req.GetSourceType(), req.GetProfileId(), req.GetName(), req.GetConfig()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	profile, err := s.store.UpdateSourceProfile(ctx, entstore.UpdateSourceProfileInput{
		SourceType: strings.TrimSpace(req.GetSourceType()),
		ProfileID:  strings.TrimSpace(req.GetProfileId()),
		Name:       strings.TrimSpace(req.GetName()),
		Config:     fromProtoSourceConfig(req.GetConfig()),
	})
	if err != nil {
		if mapped := mapSourceProfileStoreError(err); mapped != nil {
			return nil, mapped
		}
		return nil, status.Errorf(codes.Internal, "update source profile failed: %v", err)
	}
	return &backupv1.UpdateSourceProfileResponse{Profile: toProtoSourceProfile(profile)}, nil
}

func (s *Server) DeleteSourceProfile(ctx context.Context, req *backupv1.DeleteSourceProfileRequest) (*backupv1.DeleteSourceProfileResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	if strings.TrimSpace(req.GetSourceType()) == "" {
		return nil, status.Error(codes.InvalidArgument, "source_type is required")
	}
	if strings.TrimSpace(req.GetProfileId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "profile_id is required")
	}

	if err := s.store.DeleteSourceProfile(ctx, req.GetSourceType(), req.GetProfileId()); err != nil {
		if mapped := mapSourceProfileStoreError(err); mapped != nil {
			return nil, mapped
		}
		return nil, status.Errorf(codes.Internal, "delete source profile failed: %v", err)
	}
	return &backupv1.DeleteSourceProfileResponse{}, nil
}

func (s *Server) SetActiveSourceProfile(ctx context.Context, req *backupv1.SetActiveSourceProfileRequest) (*backupv1.SetActiveSourceProfileResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	if strings.TrimSpace(req.GetSourceType()) == "" {
		return nil, status.Error(codes.InvalidArgument, "source_type is required")
	}
	if strings.TrimSpace(req.GetProfileId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "profile_id is required")
	}

	profile, err := s.store.SetActiveSourceProfile(ctx, req.GetSourceType(), req.GetProfileId())
	if err != nil {
		if mapped := mapSourceProfileStoreError(err); mapped != nil {
			return nil, mapped
		}
		return nil, status.Errorf(codes.Internal, "set active source profile failed: %v", err)
	}
	return &backupv1.SetActiveSourceProfileResponse{Profile: toProtoSourceProfile(profile)}, nil
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

func (s *Server) ListS3Profiles(ctx context.Context, _ *backupv1.ListS3ProfilesRequest) (*backupv1.ListS3ProfilesResponse, error) {
	items, err := s.store.ListS3Profiles(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list s3 profiles failed: %v", err)
	}

	out := make([]*backupv1.S3Profile, 0, len(items))
	for _, item := range items {
		out = append(out, toProtoS3Profile(item))
	}
	return &backupv1.ListS3ProfilesResponse{Items: out}, nil
}

func (s *Server) CreateS3Profile(ctx context.Context, req *backupv1.CreateS3ProfileRequest) (*backupv1.CreateS3ProfileResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	if err := validateS3ProfileRequest(req.GetProfileId(), req.GetName(), req.GetS3()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	profile, err := s.store.CreateS3Profile(ctx, entstore.CreateS3ProfileInput{
		ProfileID: strings.TrimSpace(req.GetProfileId()),
		Name:      strings.TrimSpace(req.GetName()),
		S3:        fromProtoS3Config(req.GetS3()),
		SetActive: req.GetSetActive(),
	})
	if err != nil {
		if mapped := mapS3ProfileStoreError(err); mapped != nil {
			return nil, mapped
		}
		return nil, status.Errorf(codes.Internal, "create s3 profile failed: %v", err)
	}
	return &backupv1.CreateS3ProfileResponse{Profile: toProtoS3Profile(profile)}, nil
}

func (s *Server) UpdateS3Profile(ctx context.Context, req *backupv1.UpdateS3ProfileRequest) (*backupv1.UpdateS3ProfileResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	if err := validateS3ProfileRequest(req.GetProfileId(), req.GetName(), req.GetS3()); err != nil {
		return nil, status.Error(codes.InvalidArgument, err.Error())
	}

	profile, err := s.store.UpdateS3Profile(ctx, entstore.UpdateS3ProfileInput{
		ProfileID: strings.TrimSpace(req.GetProfileId()),
		Name:      strings.TrimSpace(req.GetName()),
		S3:        fromProtoS3Config(req.GetS3()),
	})
	if err != nil {
		if mapped := mapS3ProfileStoreError(err); mapped != nil {
			return nil, mapped
		}
		return nil, status.Errorf(codes.Internal, "update s3 profile failed: %v", err)
	}
	return &backupv1.UpdateS3ProfileResponse{Profile: toProtoS3Profile(profile)}, nil
}

func (s *Server) DeleteS3Profile(ctx context.Context, req *backupv1.DeleteS3ProfileRequest) (*backupv1.DeleteS3ProfileResponse, error) {
	if req == nil || strings.TrimSpace(req.GetProfileId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "profile_id is required")
	}

	err := s.store.DeleteS3Profile(ctx, req.GetProfileId())
	if err != nil {
		if mapped := mapS3ProfileStoreError(err); mapped != nil {
			return nil, mapped
		}
		return nil, status.Errorf(codes.Internal, "delete s3 profile failed: %v", err)
	}
	return &backupv1.DeleteS3ProfileResponse{}, nil
}

func (s *Server) SetActiveS3Profile(ctx context.Context, req *backupv1.SetActiveS3ProfileRequest) (*backupv1.SetActiveS3ProfileResponse, error) {
	if req == nil || strings.TrimSpace(req.GetProfileId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "profile_id is required")
	}

	profile, err := s.store.SetActiveS3Profile(ctx, req.GetProfileId())
	if err != nil {
		if mapped := mapS3ProfileStoreError(err); mapped != nil {
			return nil, mapped
		}
		return nil, status.Errorf(codes.Internal, "set active s3 profile failed: %v", err)
	}
	return &backupv1.SetActiveS3ProfileResponse{Profile: toProtoS3Profile(profile)}, nil
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
		S3ProfileID:    strings.TrimSpace(req.GetS3ProfileId()),
		PostgresID:     strings.TrimSpace(req.GetPostgresProfileId()),
		RedisID:        strings.TrimSpace(req.GetRedisProfileId()),
	})
	if err != nil {
		if ent.IsNotFound(err) {
			return nil, status.Error(codes.InvalidArgument, "source profile or s3 profile not found")
		}
		if mapped := mapSourceProfileStoreError(err); mapped != nil {
			return nil, mapped
		}
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

func validateS3ProfileRequest(profileID, name string, s3Cfg *backupv1.S3Config) error {
	if strings.TrimSpace(profileID) == "" {
		return errors.New("profile_id is required")
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("name is required")
	}
	if s3Cfg == nil {
		return errors.New("s3 config is required")
	}
	if s3Cfg.GetEnabled() {
		if strings.TrimSpace(s3Cfg.GetBucket()) == "" {
			return errors.New("s3.bucket is required")
		}
		if strings.TrimSpace(s3Cfg.GetRegion()) == "" {
			return errors.New("s3.region is required")
		}
	}
	return nil
}

func validateSourceProfileRequest(sourceType, profileID, name string, cfg *backupv1.SourceConfig) error {
	if strings.TrimSpace(sourceType) == "" {
		return errors.New("source_type is required")
	}
	if strings.TrimSpace(sourceType) != "postgres" && strings.TrimSpace(sourceType) != "redis" {
		return errors.New("source_type must be postgres or redis")
	}
	if strings.TrimSpace(profileID) == "" {
		return errors.New("profile_id is required")
	}
	if strings.TrimSpace(name) == "" {
		return errors.New("name is required")
	}
	if cfg == nil {
		return errors.New("source config is required")
	}
	return nil
}

func mapS3ProfileStoreError(err error) error {
	switch {
	case err == nil:
		return nil
	case ent.IsNotFound(err):
		return status.Error(codes.NotFound, "s3 profile not found")
	case ent.IsConstraintError(err):
		return status.Error(codes.AlreadyExists, "s3 profile already exists")
	case errors.Is(err, entstore.ErrS3ProfileRequired):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, entstore.ErrActiveS3Profile), errors.Is(err, entstore.ErrS3ProfileInUse):
		return status.Error(codes.FailedPrecondition, err.Error())
	default:
		return nil
	}
}

func mapSourceProfileStoreError(err error) error {
	switch {
	case err == nil:
		return nil
	case ent.IsNotFound(err):
		return status.Error(codes.NotFound, "source profile not found")
	case ent.IsConstraintError(err):
		return status.Error(codes.AlreadyExists, "source profile already exists")
	case errors.Is(err, entstore.ErrSourceTypeInvalid), errors.Is(err, entstore.ErrSourceIDRequired):
		return status.Error(codes.InvalidArgument, err.Error())
	case errors.Is(err, entstore.ErrSourceActive), errors.Is(err, entstore.ErrSourceInUse):
		return status.Error(codes.FailedPrecondition, err.Error())
	default:
		return nil
	}
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
	s3Cfg := cfg.GetS3()
	return entstore.ConfigSnapshot{
		SourceMode:        strings.TrimSpace(cfg.GetSourceMode()),
		BackupRoot:        strings.TrimSpace(cfg.GetBackupRoot()),
		SQLitePath:        strings.TrimSpace(cfg.GetSqlitePath()),
		RetentionDays:     cfg.GetRetentionDays(),
		KeepLast:          cfg.GetKeepLast(),
		Postgres:          fromProtoSourceConfig(cfg.GetPostgres()),
		Redis:             fromProtoSourceConfig(cfg.GetRedis()),
		S3:                fromProtoS3Config(s3Cfg),
		ActivePostgresID:  strings.TrimSpace(cfg.GetActivePostgresProfileId()),
		ActiveRedisID:     strings.TrimSpace(cfg.GetActiveRedisProfileId()),
		ActiveS3ProfileID: strings.TrimSpace(cfg.GetActiveS3ProfileId()),
	}
}

func fromProtoSourceConfig(sourceCfg *backupv1.SourceConfig) entstore.SourceConfig {
	if sourceCfg == nil {
		return entstore.SourceConfig{}
	}
	return entstore.SourceConfig{
		Host:          strings.TrimSpace(sourceCfg.GetHost()),
		Port:          sourceCfg.GetPort(),
		User:          strings.TrimSpace(sourceCfg.GetUser()),
		Username:      strings.TrimSpace(sourceCfg.GetUsername()),
		Password:      strings.TrimSpace(sourceCfg.GetPassword()),
		Database:      strings.TrimSpace(sourceCfg.GetDatabase()),
		SSLMode:       strings.TrimSpace(sourceCfg.GetSslMode()),
		Addr:          strings.TrimSpace(sourceCfg.GetAddr()),
		DB:            sourceCfg.GetDb(),
		ContainerName: strings.TrimSpace(sourceCfg.GetContainerName()),
	}
}

func fromProtoS3Config(s3Cfg *backupv1.S3Config) entstore.S3Config {
	if s3Cfg == nil {
		return entstore.S3Config{}
	}
	return entstore.S3Config{
		Enabled:         s3Cfg.GetEnabled(),
		Endpoint:        strings.TrimSpace(s3Cfg.GetEndpoint()),
		Region:          strings.TrimSpace(s3Cfg.GetRegion()),
		Bucket:          strings.TrimSpace(s3Cfg.GetBucket()),
		AccessKeyID:     strings.TrimSpace(s3Cfg.GetAccessKeyId()),
		SecretAccessKey: strings.TrimSpace(s3Cfg.GetSecretAccessKey()),
		Prefix:          strings.Trim(strings.TrimSpace(s3Cfg.GetPrefix()), "/"),
		ForcePathStyle:  s3Cfg.GetForcePathStyle(),
		UseSSL:          s3Cfg.GetUseSsl(),
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
		Postgres:      toProtoSourceConfig(cfg.Postgres),
		Redis:         toProtoSourceConfig(cfg.Redis),
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
		ActivePostgresProfileId: cfg.ActivePostgresID,
		ActiveRedisProfileId:    cfg.ActiveRedisID,
		ActiveS3ProfileId:       cfg.ActiveS3ProfileID,
	}
}

func toProtoSourceConfig(cfg entstore.SourceConfig) *backupv1.SourceConfig {
	return &backupv1.SourceConfig{
		Host:          cfg.Host,
		Port:          cfg.Port,
		User:          cfg.User,
		Password:      cfg.Password,
		Database:      cfg.Database,
		SslMode:       cfg.SSLMode,
		Addr:          cfg.Addr,
		Username:      cfg.Username,
		Db:            cfg.DB,
		ContainerName: cfg.ContainerName,
	}
}

func toProtoS3Profile(profile *entstore.S3ProfileSnapshot) *backupv1.S3Profile {
	if profile == nil {
		return &backupv1.S3Profile{}
	}
	out := &backupv1.S3Profile{
		ProfileId:                 profile.ProfileID,
		Name:                      profile.Name,
		IsActive:                  profile.IsActive,
		SecretAccessKeyConfigured: profile.SecretAccessKeyConfigured,
		S3: &backupv1.S3Config{
			Enabled:        profile.S3.Enabled,
			Endpoint:       profile.S3.Endpoint,
			Region:         profile.S3.Region,
			Bucket:         profile.S3.Bucket,
			AccessKeyId:    profile.S3.AccessKeyID,
			Prefix:         profile.S3.Prefix,
			ForcePathStyle: profile.S3.ForcePathStyle,
			UseSsl:         profile.S3.UseSSL,
		},
	}
	if !profile.CreatedAt.IsZero() {
		out.CreatedAt = profile.CreatedAt.UTC().Format(time.RFC3339)
	}
	if !profile.UpdatedAt.IsZero() {
		out.UpdatedAt = profile.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return out
}

func toProtoSourceProfile(profile *entstore.SourceProfileSnapshot) *backupv1.SourceProfile {
	if profile == nil {
		return &backupv1.SourceProfile{}
	}
	out := &backupv1.SourceProfile{
		SourceType:         profile.SourceType,
		ProfileId:          profile.ProfileID,
		Name:               profile.Name,
		IsActive:           profile.IsActive,
		Config:             toProtoSourceConfig(profile.Config),
		PasswordConfigured: profile.PasswordConfigured,
	}
	if out.GetConfig() != nil {
		out.Config.Password = ""
	}
	if !profile.CreatedAt.IsZero() {
		out.CreatedAt = profile.CreatedAt.UTC().Format(time.RFC3339)
	}
	if !profile.UpdatedAt.IsZero() {
		out.UpdatedAt = profile.UpdatedAt.UTC().Format(time.RFC3339)
	}
	return out
}

func toProtoJob(job *ent.BackupJob) *backupv1.BackupJob {
	if job == nil {
		return &backupv1.BackupJob{}
	}
	out := &backupv1.BackupJob{
		JobId:             job.JobID,
		BackupType:        job.BackupType.String(),
		Status:            job.Status.String(),
		TriggeredBy:       job.TriggeredBy,
		IdempotencyKey:    job.IdempotencyKey,
		UploadToS3:        job.UploadToS3,
		S3ProfileId:       job.S3ProfileID,
		PostgresProfileId: job.PostgresProfileID,
		RedisProfileId:    job.RedisProfileID,
		ErrorMessage:      job.ErrorMessage,
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
