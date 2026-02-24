package service

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	backupv1 "github.com/Wei-Shaw/sub2api/internal/backup/proto/backup/v1"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	grpcstatus "google.golang.org/grpc/status"
)

const (
	backupInvalidArgumentReason  = "BACKUP_INVALID_ARGUMENT"
	backupResourceNotFoundReason = "BACKUP_RESOURCE_NOT_FOUND"
	backupResourceConflictReason = "BACKUP_RESOURCE_CONFLICT"
	backupFailedPrecondition     = "BACKUP_FAILED_PRECONDITION"
	backupAgentTimeoutReason     = "BACKUP_AGENT_TIMEOUT"
	backupAgentInternalReason    = "BACKUP_AGENT_INTERNAL"
	defaultBackupRPCTimeout      = 8 * time.Second
)

type DataManagementPostgresConfig struct {
	Host               string `json:"host"`
	Port               int32  `json:"port"`
	User               string `json:"user"`
	Password           string `json:"password,omitempty"`
	PasswordConfigured bool   `json:"password_configured"`
	Database           string `json:"database"`
	SSLMode            string `json:"ssl_mode"`
	ContainerName      string `json:"container_name"`
}

type DataManagementRedisConfig struct {
	Addr               string `json:"addr"`
	Username           string `json:"username"`
	Password           string `json:"password,omitempty"`
	PasswordConfigured bool   `json:"password_configured"`
	DB                 int32  `json:"db"`
	ContainerName      string `json:"container_name"`
}

type DataManagementS3Config struct {
	Enabled                   bool   `json:"enabled"`
	Endpoint                  string `json:"endpoint"`
	Region                    string `json:"region"`
	Bucket                    string `json:"bucket"`
	AccessKeyID               string `json:"access_key_id"`
	SecretAccessKey           string `json:"secret_access_key,omitempty"`
	SecretAccessKeyConfigured bool   `json:"secret_access_key_configured"`
	Prefix                    string `json:"prefix"`
	ForcePathStyle            bool   `json:"force_path_style"`
	UseSSL                    bool   `json:"use_ssl"`
}

type DataManagementConfig struct {
	SourceMode        string                       `json:"source_mode"`
	BackupRoot        string                       `json:"backup_root"`
	SQLitePath        string                       `json:"sqlite_path,omitempty"`
	RetentionDays     int32                        `json:"retention_days"`
	KeepLast          int32                        `json:"keep_last"`
	ActivePostgresID  string                       `json:"active_postgres_profile_id"`
	ActiveRedisID     string                       `json:"active_redis_profile_id"`
	Postgres          DataManagementPostgresConfig `json:"postgres"`
	Redis             DataManagementRedisConfig    `json:"redis"`
	S3                DataManagementS3Config       `json:"s3"`
	ActiveS3ProfileID string                       `json:"active_s3_profile_id"`
}

type DataManagementTestS3Result struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

type DataManagementCreateBackupJobInput struct {
	BackupType     string
	UploadToS3     bool
	TriggeredBy    string
	IdempotencyKey string
	S3ProfileID    string
	PostgresID     string
	RedisID        string
}

type DataManagementListBackupJobsInput struct {
	PageSize   int32
	PageToken  string
	Status     string
	BackupType string
}

type DataManagementArtifactInfo struct {
	LocalPath string `json:"local_path"`
	SizeBytes int64  `json:"size_bytes"`
	SHA256    string `json:"sha256"`
}

type DataManagementS3ObjectInfo struct {
	Bucket string `json:"bucket"`
	Key    string `json:"key"`
	ETag   string `json:"etag"`
}

type DataManagementBackupJob struct {
	JobID          string                     `json:"job_id"`
	BackupType     string                     `json:"backup_type"`
	Status         string                     `json:"status"`
	TriggeredBy    string                     `json:"triggered_by"`
	IdempotencyKey string                     `json:"idempotency_key,omitempty"`
	UploadToS3     bool                       `json:"upload_to_s3"`
	S3ProfileID    string                     `json:"s3_profile_id,omitempty"`
	PostgresID     string                     `json:"postgres_profile_id,omitempty"`
	RedisID        string                     `json:"redis_profile_id,omitempty"`
	StartedAt      string                     `json:"started_at,omitempty"`
	FinishedAt     string                     `json:"finished_at,omitempty"`
	ErrorMessage   string                     `json:"error_message,omitempty"`
	Artifact       DataManagementArtifactInfo `json:"artifact"`
	S3Object       DataManagementS3ObjectInfo `json:"s3"`
}

type DataManagementSourceProfile struct {
	SourceType         string                     `json:"source_type"`
	ProfileID          string                     `json:"profile_id"`
	Name               string                     `json:"name"`
	IsActive           bool                       `json:"is_active"`
	Config             DataManagementSourceConfig `json:"config"`
	PasswordConfigured bool                       `json:"password_configured"`
	CreatedAt          string                     `json:"created_at,omitempty"`
	UpdatedAt          string                     `json:"updated_at,omitempty"`
}

type DataManagementSourceConfig struct {
	Host          string `json:"host"`
	Port          int32  `json:"port"`
	User          string `json:"user"`
	Password      string `json:"password,omitempty"`
	Database      string `json:"database"`
	SSLMode       string `json:"ssl_mode"`
	Addr          string `json:"addr"`
	Username      string `json:"username"`
	DB            int32  `json:"db"`
	ContainerName string `json:"container_name"`
}

type DataManagementCreateSourceProfileInput struct {
	SourceType string
	ProfileID  string
	Name       string
	Config     DataManagementSourceConfig
	SetActive  bool
}

type DataManagementUpdateSourceProfileInput struct {
	SourceType string
	ProfileID  string
	Name       string
	Config     DataManagementSourceConfig
}

type DataManagementS3Profile struct {
	ProfileID                 string                 `json:"profile_id"`
	Name                      string                 `json:"name"`
	IsActive                  bool                   `json:"is_active"`
	S3                        DataManagementS3Config `json:"s3"`
	SecretAccessKeyConfigured bool                   `json:"secret_access_key_configured"`
	CreatedAt                 string                 `json:"created_at,omitempty"`
	UpdatedAt                 string                 `json:"updated_at,omitempty"`
}

type DataManagementCreateS3ProfileInput struct {
	ProfileID string
	Name      string
	S3        DataManagementS3Config
	SetActive bool
}

type DataManagementUpdateS3ProfileInput struct {
	ProfileID string
	Name      string
	S3        DataManagementS3Config
}

type DataManagementListBackupJobsResult struct {
	Items         []DataManagementBackupJob `json:"items"`
	NextPageToken string                    `json:"next_page_token,omitempty"`
}

func (s *DataManagementService) GetConfig(ctx context.Context) (DataManagementConfig, error) {
	var resp *backupv1.GetConfigResponse
	err := s.withClient(ctx, func(callCtx context.Context, client backupv1.BackupServiceClient) error {
		var callErr error
		resp, callErr = client.GetConfig(callCtx, &backupv1.GetConfigRequest{})
		return callErr
	})
	if err != nil {
		return DataManagementConfig{}, err
	}
	return mapProtoConfig(resp.GetConfig()), nil
}

func (s *DataManagementService) UpdateConfig(ctx context.Context, cfg DataManagementConfig) (DataManagementConfig, error) {
	if err := validateDataManagementConfig(cfg); err != nil {
		return DataManagementConfig{}, err
	}

	var resp *backupv1.UpdateConfigResponse
	err := s.withClient(ctx, func(callCtx context.Context, client backupv1.BackupServiceClient) error {
		var callErr error
		resp, callErr = client.UpdateConfig(callCtx, &backupv1.UpdateConfigRequest{Config: mapToProtoConfig(cfg)})
		return callErr
	})
	if err != nil {
		return DataManagementConfig{}, err
	}
	return mapProtoConfig(resp.GetConfig()), nil
}

func (s *DataManagementService) ListSourceProfiles(ctx context.Context, sourceType string) ([]DataManagementSourceProfile, error) {
	sourceType = strings.TrimSpace(sourceType)
	if sourceType != "postgres" && sourceType != "redis" {
		return nil, infraerrors.BadRequest(backupInvalidArgumentReason, "source_type must be postgres or redis")
	}

	var resp *backupv1.ListSourceProfilesResponse
	err := s.withClient(ctx, func(callCtx context.Context, client backupv1.BackupServiceClient) error {
		var callErr error
		resp, callErr = client.ListSourceProfiles(callCtx, &backupv1.ListSourceProfilesRequest{
			SourceType: sourceType,
		})
		return callErr
	})
	if err != nil {
		return nil, err
	}

	items := make([]DataManagementSourceProfile, 0, len(resp.GetItems()))
	for _, item := range resp.GetItems() {
		items = append(items, mapProtoSourceProfile(item))
	}
	return items, nil
}

func (s *DataManagementService) CreateSourceProfile(ctx context.Context, input DataManagementCreateSourceProfileInput) (DataManagementSourceProfile, error) {
	if err := validateSourceProfileInput(input.SourceType, input.ProfileID, input.Name); err != nil {
		return DataManagementSourceProfile{}, err
	}

	var resp *backupv1.CreateSourceProfileResponse
	err := s.withClient(ctx, func(callCtx context.Context, client backupv1.BackupServiceClient) error {
		var callErr error
		resp, callErr = client.CreateSourceProfile(callCtx, &backupv1.CreateSourceProfileRequest{
			SourceType: strings.TrimSpace(input.SourceType),
			ProfileId:  strings.TrimSpace(input.ProfileID),
			Name:       strings.TrimSpace(input.Name),
			Config:     mapToProtoSourceConfig(input.Config),
			SetActive:  input.SetActive,
		})
		return callErr
	})
	if err != nil {
		return DataManagementSourceProfile{}, err
	}
	return mapProtoSourceProfile(resp.GetProfile()), nil
}

func (s *DataManagementService) UpdateSourceProfile(ctx context.Context, input DataManagementUpdateSourceProfileInput) (DataManagementSourceProfile, error) {
	if err := validateSourceProfileInput(input.SourceType, input.ProfileID, input.Name); err != nil {
		return DataManagementSourceProfile{}, err
	}

	var resp *backupv1.UpdateSourceProfileResponse
	err := s.withClient(ctx, func(callCtx context.Context, client backupv1.BackupServiceClient) error {
		var callErr error
		resp, callErr = client.UpdateSourceProfile(callCtx, &backupv1.UpdateSourceProfileRequest{
			SourceType: strings.TrimSpace(input.SourceType),
			ProfileId:  strings.TrimSpace(input.ProfileID),
			Name:       strings.TrimSpace(input.Name),
			Config:     mapToProtoSourceConfig(input.Config),
		})
		return callErr
	})
	if err != nil {
		return DataManagementSourceProfile{}, err
	}
	return mapProtoSourceProfile(resp.GetProfile()), nil
}

func (s *DataManagementService) DeleteSourceProfile(ctx context.Context, sourceType, profileID string) error {
	sourceType = strings.TrimSpace(sourceType)
	profileID = strings.TrimSpace(profileID)
	if sourceType != "postgres" && sourceType != "redis" {
		return infraerrors.BadRequest(backupInvalidArgumentReason, "source_type must be postgres or redis")
	}
	if profileID == "" {
		return infraerrors.BadRequest(backupInvalidArgumentReason, "profile_id is required")
	}

	return s.withClient(ctx, func(callCtx context.Context, client backupv1.BackupServiceClient) error {
		_, err := client.DeleteSourceProfile(callCtx, &backupv1.DeleteSourceProfileRequest{
			SourceType: sourceType,
			ProfileId:  profileID,
		})
		return err
	})
}

func (s *DataManagementService) SetActiveSourceProfile(ctx context.Context, sourceType, profileID string) (DataManagementSourceProfile, error) {
	sourceType = strings.TrimSpace(sourceType)
	profileID = strings.TrimSpace(profileID)
	if sourceType != "postgres" && sourceType != "redis" {
		return DataManagementSourceProfile{}, infraerrors.BadRequest(backupInvalidArgumentReason, "source_type must be postgres or redis")
	}
	if profileID == "" {
		return DataManagementSourceProfile{}, infraerrors.BadRequest(backupInvalidArgumentReason, "profile_id is required")
	}

	var resp *backupv1.SetActiveSourceProfileResponse
	err := s.withClient(ctx, func(callCtx context.Context, client backupv1.BackupServiceClient) error {
		var callErr error
		resp, callErr = client.SetActiveSourceProfile(callCtx, &backupv1.SetActiveSourceProfileRequest{
			SourceType: sourceType,
			ProfileId:  profileID,
		})
		return callErr
	})
	if err != nil {
		return DataManagementSourceProfile{}, err
	}
	return mapProtoSourceProfile(resp.GetProfile()), nil
}

func (s *DataManagementService) ValidateS3(ctx context.Context, cfg DataManagementS3Config) (DataManagementTestS3Result, error) {
	if err := validateS3Config(cfg); err != nil {
		return DataManagementTestS3Result{}, err
	}

	var resp *backupv1.ValidateS3Response
	err := s.withClient(ctx, func(callCtx context.Context, client backupv1.BackupServiceClient) error {
		var callErr error
		resp, callErr = client.ValidateS3(callCtx, &backupv1.ValidateS3Request{
			S3: &backupv1.S3Config{
				Enabled:         cfg.Enabled,
				Endpoint:        strings.TrimSpace(cfg.Endpoint),
				Region:          strings.TrimSpace(cfg.Region),
				Bucket:          strings.TrimSpace(cfg.Bucket),
				AccessKeyId:     strings.TrimSpace(cfg.AccessKeyID),
				SecretAccessKey: strings.TrimSpace(cfg.SecretAccessKey),
				Prefix:          strings.Trim(strings.TrimSpace(cfg.Prefix), "/"),
				ForcePathStyle:  cfg.ForcePathStyle,
				UseSsl:          cfg.UseSSL,
			},
		})
		return callErr
	})
	if err != nil {
		return DataManagementTestS3Result{}, err
	}
	return DataManagementTestS3Result{OK: resp.GetOk(), Message: resp.GetMessage()}, nil
}

func (s *DataManagementService) ListS3Profiles(ctx context.Context) ([]DataManagementS3Profile, error) {
	var resp *backupv1.ListS3ProfilesResponse
	err := s.withClient(ctx, func(callCtx context.Context, client backupv1.BackupServiceClient) error {
		var callErr error
		resp, callErr = client.ListS3Profiles(callCtx, &backupv1.ListS3ProfilesRequest{})
		return callErr
	})
	if err != nil {
		return nil, err
	}

	items := make([]DataManagementS3Profile, 0, len(resp.GetItems()))
	for _, item := range resp.GetItems() {
		items = append(items, mapProtoS3Profile(item))
	}
	return items, nil
}

func (s *DataManagementService) CreateS3Profile(ctx context.Context, input DataManagementCreateS3ProfileInput) (DataManagementS3Profile, error) {
	if err := validateS3ProfileInput(input.ProfileID, input.Name, input.S3); err != nil {
		return DataManagementS3Profile{}, err
	}

	var resp *backupv1.CreateS3ProfileResponse
	err := s.withClient(ctx, func(callCtx context.Context, client backupv1.BackupServiceClient) error {
		var callErr error
		resp, callErr = client.CreateS3Profile(callCtx, &backupv1.CreateS3ProfileRequest{
			ProfileId: strings.TrimSpace(input.ProfileID),
			Name:      strings.TrimSpace(input.Name),
			S3:        mapToProtoS3Config(input.S3),
			SetActive: input.SetActive,
		})
		return callErr
	})
	if err != nil {
		return DataManagementS3Profile{}, err
	}
	return mapProtoS3Profile(resp.GetProfile()), nil
}

func (s *DataManagementService) UpdateS3Profile(ctx context.Context, input DataManagementUpdateS3ProfileInput) (DataManagementS3Profile, error) {
	if err := validateS3ProfileInput(input.ProfileID, input.Name, input.S3); err != nil {
		return DataManagementS3Profile{}, err
	}

	var resp *backupv1.UpdateS3ProfileResponse
	err := s.withClient(ctx, func(callCtx context.Context, client backupv1.BackupServiceClient) error {
		var callErr error
		resp, callErr = client.UpdateS3Profile(callCtx, &backupv1.UpdateS3ProfileRequest{
			ProfileId: strings.TrimSpace(input.ProfileID),
			Name:      strings.TrimSpace(input.Name),
			S3:        mapToProtoS3Config(input.S3),
		})
		return callErr
	})
	if err != nil {
		return DataManagementS3Profile{}, err
	}
	return mapProtoS3Profile(resp.GetProfile()), nil
}

func (s *DataManagementService) DeleteS3Profile(ctx context.Context, profileID string) error {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return infraerrors.BadRequest(backupInvalidArgumentReason, "profile_id is required")
	}

	return s.withClient(ctx, func(callCtx context.Context, client backupv1.BackupServiceClient) error {
		_, err := client.DeleteS3Profile(callCtx, &backupv1.DeleteS3ProfileRequest{ProfileId: profileID})
		return err
	})
}

func (s *DataManagementService) SetActiveS3Profile(ctx context.Context, profileID string) (DataManagementS3Profile, error) {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		return DataManagementS3Profile{}, infraerrors.BadRequest(backupInvalidArgumentReason, "profile_id is required")
	}

	var resp *backupv1.SetActiveS3ProfileResponse
	err := s.withClient(ctx, func(callCtx context.Context, client backupv1.BackupServiceClient) error {
		var callErr error
		resp, callErr = client.SetActiveS3Profile(callCtx, &backupv1.SetActiveS3ProfileRequest{ProfileId: profileID})
		return callErr
	})
	if err != nil {
		return DataManagementS3Profile{}, err
	}
	return mapProtoS3Profile(resp.GetProfile()), nil
}

func (s *DataManagementService) CreateBackupJob(ctx context.Context, input DataManagementCreateBackupJobInput) (DataManagementBackupJob, error) {
	var resp *backupv1.CreateBackupJobResponse
	err := s.withClient(ctx, func(callCtx context.Context, client backupv1.BackupServiceClient) error {
		var callErr error
		resp, callErr = client.CreateBackupJob(callCtx, &backupv1.CreateBackupJobRequest{
			BackupType:        strings.TrimSpace(input.BackupType),
			UploadToS3:        input.UploadToS3,
			TriggeredBy:       strings.TrimSpace(input.TriggeredBy),
			IdempotencyKey:    strings.TrimSpace(input.IdempotencyKey),
			S3ProfileId:       strings.TrimSpace(input.S3ProfileID),
			PostgresProfileId: strings.TrimSpace(input.PostgresID),
			RedisProfileId:    strings.TrimSpace(input.RedisID),
		})
		return callErr
	})
	if err != nil {
		return DataManagementBackupJob{}, err
	}
	return mapProtoJob(resp.GetJob()), nil
}

func (s *DataManagementService) ListBackupJobs(ctx context.Context, input DataManagementListBackupJobsInput) (DataManagementListBackupJobsResult, error) {
	var resp *backupv1.ListBackupJobsResponse
	err := s.withClient(ctx, func(callCtx context.Context, client backupv1.BackupServiceClient) error {
		var callErr error
		resp, callErr = client.ListBackupJobs(callCtx, &backupv1.ListBackupJobsRequest{
			PageSize:   input.PageSize,
			PageToken:  strings.TrimSpace(input.PageToken),
			Status:     strings.TrimSpace(input.Status),
			BackupType: strings.TrimSpace(input.BackupType),
		})
		return callErr
	})
	if err != nil {
		return DataManagementListBackupJobsResult{}, err
	}

	items := make([]DataManagementBackupJob, 0, len(resp.GetItems()))
	for _, item := range resp.GetItems() {
		items = append(items, mapProtoJob(item))
	}
	return DataManagementListBackupJobsResult{Items: items, NextPageToken: resp.GetNextPageToken()}, nil
}

func (s *DataManagementService) GetBackupJob(ctx context.Context, jobID string) (DataManagementBackupJob, error) {
	var resp *backupv1.GetBackupJobResponse
	err := s.withClient(ctx, func(callCtx context.Context, client backupv1.BackupServiceClient) error {
		var callErr error
		resp, callErr = client.GetBackupJob(callCtx, &backupv1.GetBackupJobRequest{JobId: strings.TrimSpace(jobID)})
		return callErr
	})
	if err != nil {
		return DataManagementBackupJob{}, err
	}
	return mapProtoJob(resp.GetJob()), nil
}

func (s *DataManagementService) withClient(ctx context.Context, call func(context.Context, backupv1.BackupServiceClient) error) error {
	if err := s.EnsureAgentEnabled(ctx); err != nil {
		return err
	}

	socketPath := s.SocketPath()
	dialCtx, dialCancel := context.WithTimeout(ctx, s.dialTimeout)
	defer dialCancel()

	conn, err := grpc.DialContext(
		dialCtx,
		"unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			dialer := net.Dialer{Timeout: s.dialTimeout}
			return dialer.DialContext(ctx, "unix", socketPath)
		}),
	)
	if err != nil {
		return ErrBackupAgentUnavailable.WithMetadata(map[string]string{"socket_path": socketPath}).WithCause(err)
	}
	defer func() {
		_ = conn.Close()
	}()

	client := backupv1.NewBackupServiceClient(conn)
	callCtx, callCancel := context.WithTimeout(ctx, defaultBackupRPCTimeout)
	defer callCancel()
	if requestID := requestIDFromContext(ctx); requestID != "" {
		callCtx = metadata.AppendToOutgoingContext(callCtx, "x-request-id", requestID)
	}

	if err := call(callCtx, client); err != nil {
		return mapBackupGRPCError(err, socketPath)
	}
	return nil
}

func mapBackupGRPCError(err error, socketPath string) error {
	if err == nil {
		return nil
	}

	st, ok := grpcstatus.FromError(err)
	if !ok {
		return infraerrors.InternalServer(backupAgentInternalReason, "backup agent call failed").
			WithMetadata(map[string]string{"socket_path": socketPath}).
			WithCause(err)
	}

	switch st.Code() {
	case codes.InvalidArgument:
		return infraerrors.BadRequest(backupInvalidArgumentReason, st.Message())
	case codes.AlreadyExists:
		return infraerrors.New(409, backupResourceConflictReason, st.Message())
	case codes.NotFound:
		return infraerrors.NotFound(backupResourceNotFoundReason, st.Message())
	case codes.FailedPrecondition:
		return infraerrors.New(412, backupFailedPrecondition, st.Message())
	case codes.Unavailable:
		return infraerrors.ServiceUnavailable(BackupAgentUnavailableReason, st.Message()).
			WithMetadata(map[string]string{"socket_path": socketPath})
	case codes.DeadlineExceeded:
		return infraerrors.GatewayTimeout(backupAgentTimeoutReason, st.Message())
	default:
		return infraerrors.InternalServer(backupAgentInternalReason, st.Message()).
			WithMetadata(map[string]string{
				"socket_path": socketPath,
				"grpc_code":   st.Code().String(),
			})
	}
}

func mapProtoConfig(cfg *backupv1.BackupConfig) DataManagementConfig {
	if cfg == nil {
		return DataManagementConfig{}
	}
	postgres := cfg.GetPostgres()
	redis := cfg.GetRedis()
	s3Cfg := cfg.GetS3()
	return DataManagementConfig{
		SourceMode:        cfg.GetSourceMode(),
		BackupRoot:        cfg.GetBackupRoot(),
		SQLitePath:        cfg.GetSqlitePath(),
		RetentionDays:     cfg.GetRetentionDays(),
		KeepLast:          cfg.GetKeepLast(),
		ActivePostgresID:  cfg.GetActivePostgresProfileId(),
		ActiveRedisID:     cfg.GetActiveRedisProfileId(),
		ActiveS3ProfileID: cfg.GetActiveS3ProfileId(),
		Postgres: DataManagementPostgresConfig{
			Host:               postgres.GetHost(),
			Port:               postgres.GetPort(),
			User:               postgres.GetUser(),
			PasswordConfigured: strings.TrimSpace(postgres.GetPassword()) != "",
			Database:           postgres.GetDatabase(),
			SSLMode:            postgres.GetSslMode(),
			ContainerName:      postgres.GetContainerName(),
		},
		Redis: DataManagementRedisConfig{
			Addr:               redis.GetAddr(),
			Username:           redis.GetUsername(),
			PasswordConfigured: strings.TrimSpace(redis.GetPassword()) != "",
			DB:                 redis.GetDb(),
			ContainerName:      redis.GetContainerName(),
		},
		S3: DataManagementS3Config{
			Enabled:                   s3Cfg.GetEnabled(),
			Endpoint:                  s3Cfg.GetEndpoint(),
			Region:                    s3Cfg.GetRegion(),
			Bucket:                    s3Cfg.GetBucket(),
			AccessKeyID:               s3Cfg.GetAccessKeyId(),
			SecretAccessKeyConfigured: strings.TrimSpace(s3Cfg.GetSecretAccessKey()) != "",
			Prefix:                    s3Cfg.GetPrefix(),
			ForcePathStyle:            s3Cfg.GetForcePathStyle(),
			UseSSL:                    s3Cfg.GetUseSsl(),
		},
	}
}

func mapToProtoConfig(cfg DataManagementConfig) *backupv1.BackupConfig {
	return &backupv1.BackupConfig{
		SourceMode:              strings.TrimSpace(cfg.SourceMode),
		BackupRoot:              strings.TrimSpace(cfg.BackupRoot),
		SqlitePath:              strings.TrimSpace(cfg.SQLitePath),
		RetentionDays:           cfg.RetentionDays,
		KeepLast:                cfg.KeepLast,
		ActivePostgresProfileId: strings.TrimSpace(cfg.ActivePostgresID),
		ActiveRedisProfileId:    strings.TrimSpace(cfg.ActiveRedisID),
		ActiveS3ProfileId:       strings.TrimSpace(cfg.ActiveS3ProfileID),
		Postgres: &backupv1.SourceConfig{
			Host:          strings.TrimSpace(cfg.Postgres.Host),
			Port:          cfg.Postgres.Port,
			User:          strings.TrimSpace(cfg.Postgres.User),
			Password:      strings.TrimSpace(cfg.Postgres.Password),
			Database:      strings.TrimSpace(cfg.Postgres.Database),
			SslMode:       strings.TrimSpace(cfg.Postgres.SSLMode),
			ContainerName: strings.TrimSpace(cfg.Postgres.ContainerName),
		},
		Redis: &backupv1.SourceConfig{
			Addr:          strings.TrimSpace(cfg.Redis.Addr),
			Username:      strings.TrimSpace(cfg.Redis.Username),
			Password:      strings.TrimSpace(cfg.Redis.Password),
			Db:            cfg.Redis.DB,
			ContainerName: strings.TrimSpace(cfg.Redis.ContainerName),
		},
		S3: mapToProtoS3Config(cfg.S3),
	}
}

func mapToProtoS3Config(cfg DataManagementS3Config) *backupv1.S3Config {
	return &backupv1.S3Config{
		Enabled:         cfg.Enabled,
		Endpoint:        strings.TrimSpace(cfg.Endpoint),
		Region:          strings.TrimSpace(cfg.Region),
		Bucket:          strings.TrimSpace(cfg.Bucket),
		AccessKeyId:     strings.TrimSpace(cfg.AccessKeyID),
		SecretAccessKey: strings.TrimSpace(cfg.SecretAccessKey),
		Prefix:          strings.Trim(strings.TrimSpace(cfg.Prefix), "/"),
		ForcePathStyle:  cfg.ForcePathStyle,
		UseSsl:          cfg.UseSSL,
	}
}

func mapToProtoSourceConfig(cfg DataManagementSourceConfig) *backupv1.SourceConfig {
	return &backupv1.SourceConfig{
		Host:          strings.TrimSpace(cfg.Host),
		Port:          cfg.Port,
		User:          strings.TrimSpace(cfg.User),
		Password:      strings.TrimSpace(cfg.Password),
		Database:      strings.TrimSpace(cfg.Database),
		SslMode:       strings.TrimSpace(cfg.SSLMode),
		Addr:          strings.TrimSpace(cfg.Addr),
		Username:      strings.TrimSpace(cfg.Username),
		Db:            cfg.DB,
		ContainerName: strings.TrimSpace(cfg.ContainerName),
	}
}

func mapProtoS3Profile(profile *backupv1.S3Profile) DataManagementS3Profile {
	if profile == nil {
		return DataManagementS3Profile{}
	}
	s3Cfg := profile.GetS3()
	if s3Cfg == nil {
		s3Cfg = &backupv1.S3Config{}
	}
	return DataManagementS3Profile{
		ProfileID: profile.GetProfileId(),
		Name:      profile.GetName(),
		IsActive:  profile.GetIsActive(),
		S3: DataManagementS3Config{
			Enabled:                   s3Cfg.GetEnabled(),
			Endpoint:                  s3Cfg.GetEndpoint(),
			Region:                    s3Cfg.GetRegion(),
			Bucket:                    s3Cfg.GetBucket(),
			AccessKeyID:               s3Cfg.GetAccessKeyId(),
			SecretAccessKeyConfigured: profile.GetSecretAccessKeyConfigured() || strings.TrimSpace(s3Cfg.GetSecretAccessKey()) != "",
			Prefix:                    s3Cfg.GetPrefix(),
			ForcePathStyle:            s3Cfg.GetForcePathStyle(),
			UseSSL:                    s3Cfg.GetUseSsl(),
		},
		SecretAccessKeyConfigured: profile.GetSecretAccessKeyConfigured() || strings.TrimSpace(s3Cfg.GetSecretAccessKey()) != "",
		CreatedAt:                 strings.TrimSpace(profile.GetCreatedAt()),
		UpdatedAt:                 strings.TrimSpace(profile.GetUpdatedAt()),
	}
}

func mapProtoSourceProfile(profile *backupv1.SourceProfile) DataManagementSourceProfile {
	if profile == nil {
		return DataManagementSourceProfile{}
	}
	sourceCfg := profile.GetConfig()
	if sourceCfg == nil {
		sourceCfg = &backupv1.SourceConfig{}
	}
	return DataManagementSourceProfile{
		SourceType: profile.GetSourceType(),
		ProfileID:  profile.GetProfileId(),
		Name:       profile.GetName(),
		IsActive:   profile.GetIsActive(),
		Config: DataManagementSourceConfig{
			Host:          sourceCfg.GetHost(),
			Port:          sourceCfg.GetPort(),
			User:          sourceCfg.GetUser(),
			Database:      sourceCfg.GetDatabase(),
			SSLMode:       sourceCfg.GetSslMode(),
			Addr:          sourceCfg.GetAddr(),
			Username:      sourceCfg.GetUsername(),
			DB:            sourceCfg.GetDb(),
			ContainerName: sourceCfg.GetContainerName(),
		},
		PasswordConfigured: profile.GetPasswordConfigured(),
		CreatedAt:          strings.TrimSpace(profile.GetCreatedAt()),
		UpdatedAt:          strings.TrimSpace(profile.GetUpdatedAt()),
	}
}

func mapProtoJob(job *backupv1.BackupJob) DataManagementBackupJob {
	if job == nil {
		return DataManagementBackupJob{}
	}
	artifact := job.GetArtifact()
	s3Object := job.GetS3Object()
	artifactOut := DataManagementArtifactInfo{}
	if artifact != nil {
		artifactOut = DataManagementArtifactInfo{
			LocalPath: artifact.GetLocalPath(),
			SizeBytes: artifact.GetSizeBytes(),
			SHA256:    artifact.GetSha256(),
		}
	}
	s3Out := DataManagementS3ObjectInfo{}
	if s3Object != nil {
		s3Out = DataManagementS3ObjectInfo{
			Bucket: s3Object.GetBucket(),
			Key:    s3Object.GetKey(),
			ETag:   s3Object.GetEtag(),
		}
	}

	return DataManagementBackupJob{
		JobID:          job.GetJobId(),
		BackupType:     job.GetBackupType(),
		Status:         job.GetStatus(),
		TriggeredBy:    job.GetTriggeredBy(),
		IdempotencyKey: job.GetIdempotencyKey(),
		UploadToS3:     job.GetUploadToS3(),
		S3ProfileID:    job.GetS3ProfileId(),
		PostgresID:     job.GetPostgresProfileId(),
		RedisID:        job.GetRedisProfileId(),
		StartedAt:      job.GetStartedAt(),
		FinishedAt:     job.GetFinishedAt(),
		ErrorMessage:   job.GetErrorMessage(),
		Artifact:       artifactOut,
		S3Object:       s3Out,
	}
}

func validateDataManagementConfig(cfg DataManagementConfig) error {
	sourceMode := strings.TrimSpace(cfg.SourceMode)
	if sourceMode != "direct" && sourceMode != "docker_exec" {
		return infraerrors.BadRequest(backupInvalidArgumentReason, "source_mode must be direct or docker_exec")
	}
	if strings.TrimSpace(cfg.BackupRoot) == "" {
		return infraerrors.BadRequest(backupInvalidArgumentReason, "backup_root is required")
	}
	if cfg.RetentionDays <= 0 {
		return infraerrors.BadRequest(backupInvalidArgumentReason, "retention_days must be > 0")
	}
	if cfg.KeepLast <= 0 {
		return infraerrors.BadRequest(backupInvalidArgumentReason, "keep_last must be > 0")
	}

	if strings.TrimSpace(cfg.Postgres.Database) == "" {
		return infraerrors.BadRequest(backupInvalidArgumentReason, "postgres.database is required")
	}
	if cfg.Postgres.Port <= 0 {
		return infraerrors.BadRequest(backupInvalidArgumentReason, "postgres.port must be > 0")
	}
	if sourceMode == "docker_exec" {
		if strings.TrimSpace(cfg.Postgres.ContainerName) == "" {
			return infraerrors.BadRequest(backupInvalidArgumentReason, "postgres.container_name is required in docker_exec mode")
		}
		if strings.TrimSpace(cfg.Redis.ContainerName) == "" {
			return infraerrors.BadRequest(backupInvalidArgumentReason, "redis.container_name is required in docker_exec mode")
		}
	} else {
		if strings.TrimSpace(cfg.Postgres.Host) == "" {
			return infraerrors.BadRequest(backupInvalidArgumentReason, "postgres.host is required in direct mode")
		}
		if strings.TrimSpace(cfg.Redis.Addr) == "" {
			return infraerrors.BadRequest(backupInvalidArgumentReason, "redis.addr is required in direct mode")
		}
	}

	if cfg.Redis.DB < 0 {
		return infraerrors.BadRequest(backupInvalidArgumentReason, "redis.db must be >= 0")
	}

	if cfg.S3.Enabled {
		if err := validateS3Config(cfg.S3); err != nil {
			return err
		}
	}
	return nil
}

func validateS3Config(cfg DataManagementS3Config) error {
	if strings.TrimSpace(cfg.Region) == "" {
		return infraerrors.BadRequest(backupInvalidArgumentReason, "s3.region is required")
	}
	if strings.TrimSpace(cfg.Bucket) == "" {
		return infraerrors.BadRequest(backupInvalidArgumentReason, "s3.bucket is required")
	}
	return nil
}

func validateS3ProfileInput(profileID, name string, s3Cfg DataManagementS3Config) error {
	if strings.TrimSpace(profileID) == "" {
		return infraerrors.BadRequest(backupInvalidArgumentReason, "profile_id is required")
	}
	if strings.TrimSpace(name) == "" {
		return infraerrors.BadRequest(backupInvalidArgumentReason, "name is required")
	}
	if s3Cfg.Enabled {
		return validateS3Config(s3Cfg)
	}
	return nil
}

func validateSourceProfileInput(sourceType, profileID, name string) error {
	sourceType = strings.TrimSpace(sourceType)
	if sourceType != "postgres" && sourceType != "redis" {
		return infraerrors.BadRequest(backupInvalidArgumentReason, "source_type must be postgres or redis")
	}
	if strings.TrimSpace(profileID) == "" {
		return infraerrors.BadRequest(backupInvalidArgumentReason, "profile_id is required")
	}
	if strings.TrimSpace(name) == "" {
		return infraerrors.BadRequest(backupInvalidArgumentReason, "name is required")
	}
	return nil
}

func (s *DataManagementService) probeBackupHealth(ctx context.Context) (*DataManagementAgentInfo, error) {
	socketPath := s.SocketPath()
	dialCtx, dialCancel := context.WithTimeout(ctx, s.dialTimeout)
	defer dialCancel()

	conn, err := grpc.DialContext(
		dialCtx,
		"unix://"+socketPath,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithBlock(),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			dialer := net.Dialer{Timeout: s.dialTimeout}
			return dialer.DialContext(ctx, "unix", socketPath)
		}),
	)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = conn.Close()
	}()

	callCtx, callCancel := context.WithTimeout(ctx, s.dialTimeout)
	defer callCancel()
	if requestID := requestIDFromContext(ctx); requestID != "" {
		callCtx = metadata.AppendToOutgoingContext(callCtx, "x-request-id", requestID)
	}
	resp, err := backupv1.NewBackupServiceClient(conn).Health(callCtx, &backupv1.HealthRequest{})
	if err != nil {
		return nil, err
	}
	statusText := strings.TrimSpace(resp.GetStatus())
	if statusText == "" {
		return nil, fmt.Errorf("empty backup health status")
	}
	return &DataManagementAgentInfo{
		Status:        statusText,
		Version:       strings.TrimSpace(resp.GetVersion()),
		UptimeSeconds: resp.GetUptimeSeconds(),
	}, nil
}

func requestIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	value, _ := ctx.Value(ctxkey.RequestID).(string)
	return strings.TrimSpace(value)
}
