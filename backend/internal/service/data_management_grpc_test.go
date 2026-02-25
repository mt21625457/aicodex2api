package service

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	backupv1 "github.com/Wei-Shaw/sub2api/internal/backup/proto/backup/v1"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	grpcstatus "google.golang.org/grpc/status"
)

func TestMapBackupGRPCError(t *testing.T) {
	t.Parallel()

	socketPath := "/tmp/sub2api-backup.sock"
	testCases := []struct {
		name       string
		err        error
		wantCode   int
		wantReason string
	}{
		{
			name:       "invalid argument",
			err:        grpcstatus.Error(codes.InvalidArgument, "bad request"),
			wantCode:   400,
			wantReason: backupInvalidArgumentReason,
		},
		{
			name:       "not found",
			err:        grpcstatus.Error(codes.NotFound, "not found"),
			wantCode:   404,
			wantReason: backupResourceNotFoundReason,
		},
		{
			name:       "already exists",
			err:        grpcstatus.Error(codes.AlreadyExists, "exists"),
			wantCode:   409,
			wantReason: backupResourceConflictReason,
		},
		{
			name:       "failed precondition",
			err:        grpcstatus.Error(codes.FailedPrecondition, "precondition failed"),
			wantCode:   412,
			wantReason: backupFailedPrecondition,
		},
		{
			name:       "unavailable",
			err:        grpcstatus.Error(codes.Unavailable, "agent unavailable"),
			wantCode:   503,
			wantReason: BackupAgentUnavailableReason,
		},
		{
			name:       "deadline exceeded",
			err:        grpcstatus.Error(codes.DeadlineExceeded, "timeout"),
			wantCode:   504,
			wantReason: backupAgentTimeoutReason,
		},
		{
			name:       "internal fallback",
			err:        grpcstatus.Error(codes.Internal, "internal"),
			wantCode:   500,
			wantReason: backupAgentInternalReason,
		},
		{
			name:       "non grpc error",
			err:        errors.New("plain error"),
			wantCode:   500,
			wantReason: backupAgentInternalReason,
		},
	}

	for _, tc := range testCases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mapped := mapBackupGRPCError(tc.err, socketPath)
			statusCode, body := infraerrors.ToHTTP(mapped)

			require.Equal(t, tc.wantCode, statusCode)
			require.Equal(t, tc.wantReason, body.Reason)

			if tc.wantCode == 503 || tc.wantCode == 500 {
				require.Equal(t, socketPath, body.Metadata["socket_path"])
			}
		})
	}
}

func TestValidateDataManagementConfig(t *testing.T) {
	t.Parallel()

	valid := DataManagementConfig{
		SourceMode:    "direct",
		BackupRoot:    "/var/lib/sub2api/backups",
		RetentionDays: 7,
		KeepLast:      30,
		Postgres: DataManagementPostgresConfig{
			Host:     "127.0.0.1",
			Port:     5432,
			Database: "sub2api",
		},
		Redis: DataManagementRedisConfig{
			Addr: "127.0.0.1:6379",
			DB:   0,
		},
		S3: DataManagementS3Config{
			Enabled: false,
		},
	}

	require.NoError(t, validateDataManagementConfig(valid))

	invalidMode := valid
	invalidMode.SourceMode = "invalid"
	require.Error(t, validateDataManagementConfig(invalidMode))

	dockerMissingContainer := valid
	dockerMissingContainer.SourceMode = "docker_exec"
	require.Error(t, validateDataManagementConfig(dockerMissingContainer))

	s3EnabledMissingBucket := valid
	s3EnabledMissingBucket.S3.Enabled = true
	s3EnabledMissingBucket.S3.Region = "us-east-1"
	s3EnabledMissingBucket.S3.Bucket = ""
	require.Error(t, validateDataManagementConfig(s3EnabledMissingBucket))
}

func TestDataManagementService_DialBackupAgent_TimeoutDisabled(t *testing.T) {
	t.Parallel()

	socketPath := filepath.Join("/tmp", fmt.Sprintf("s2dm0-%d.sock", time.Now().UnixNano()))
	startTestBackupHealthServer(t, socketPath)

	svc := &DataManagementService{
		socketPath:  socketPath,
		dialTimeout: 0,
	}

	conn, err := svc.dialBackupAgent(context.Background(), socketPath)
	require.NoError(t, err)
	require.NotNil(t, conn)
	require.NoError(t, conn.Close())
}

func TestDataManagementService_DialBackupAgent_TimeoutExceeded(t *testing.T) {
	t.Parallel()

	socketPath := filepath.Join(t.TempDir(), "missing.sock")
	svc := &DataManagementService{
		socketPath:  socketPath,
		dialTimeout: 30 * time.Millisecond,
	}

	conn, err := svc.dialBackupAgent(context.Background(), socketPath)
	require.Nil(t, conn)
	require.Error(t, err)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestDataManagementService_WithClient_PassesRequestID(t *testing.T) {
	t.Parallel()

	socketPath := filepath.Join("/tmp", fmt.Sprintf("s2dm1-%d.sock", time.Now().UnixNano()))
	startTestBackupHealthServer(t, socketPath)

	svc := &DataManagementService{
		socketPath:  socketPath,
		dialTimeout: 200 * time.Millisecond,
	}

	ctx := context.WithValue(context.Background(), ctxkey.RequestID, "req-data-management-1")
	called := false
	err := svc.withClient(ctx, func(callCtx context.Context, _ backupv1.BackupServiceClient) error {
		called = true
		md, ok := metadata.FromOutgoingContext(callCtx)
		require.True(t, ok)
		require.Equal(t, []string{"req-data-management-1"}, md.Get("x-request-id"))
		return nil
	})
	require.NoError(t, err)
	require.True(t, called)
}
