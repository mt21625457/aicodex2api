package service

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	backupv1 "github.com/Wei-Shaw/sub2api/internal/backup/proto/backup/v1"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
)

func TestDataManagementService_GetAgentHealth_SocketMissing(t *testing.T) {
	t.Parallel()

	svc := NewDataManagementServiceWithOptions(filepath.Join(t.TempDir(), "missing.sock"), 100*time.Millisecond)
	health := svc.GetAgentHealth(context.Background())

	require.False(t, health.Enabled)
	require.Equal(t, BackupAgentSocketMissingReason, health.Reason)
	require.NotEmpty(t, health.SocketPath)
}

func TestDataManagementService_GetAgentHealth_SocketReachable(t *testing.T) {
	t.Parallel()

	socketPath := filepath.Join(os.TempDir(), fmt.Sprintf("sub2api-dm-%d.sock", time.Now().UnixNano()))
	startTestBackupHealthServer(t, socketPath)

	svc := NewDataManagementServiceWithOptions(socketPath, 100*time.Millisecond)
	health := svc.GetAgentHealth(context.Background())

	require.True(t, health.Enabled)
	require.Equal(t, "", health.Reason)
	require.Equal(t, socketPath, health.SocketPath)
	require.NotNil(t, health.Agent)
	require.Equal(t, "SERVING", health.Agent.Status)
	require.Equal(t, "test-backupd", health.Agent.Version)
	require.EqualValues(t, 42, health.Agent.UptimeSeconds)
}

func TestDataManagementService_EnsureAgentEnabled(t *testing.T) {
	t.Parallel()

	svc := NewDataManagementServiceWithOptions(filepath.Join(t.TempDir(), "missing.sock"), 100*time.Millisecond)
	err := svc.EnsureAgentEnabled(context.Background())
	require.Error(t, err)

	statusCode, status := infraerrors.ToHTTP(err)
	require.Equal(t, 503, statusCode)
	require.Equal(t, BackupAgentSocketMissingReason, status.Reason)
	require.Equal(t, svc.SocketPath(), status.Metadata["socket_path"])
}

func startTestBackupHealthServer(t *testing.T, socketPath string) {
	t.Helper()
	_ = os.Remove(socketPath)

	listener, err := net.Listen("unix", socketPath)
	require.NoError(t, err)

	server := grpc.NewServer()
	backupv1.RegisterBackupServiceServer(server, &testBackupHealthServer{})

	go func() {
		_ = server.Serve(listener)
	}()

	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
		_ = os.Remove(socketPath)
	})
}

type testBackupHealthServer struct {
	backupv1.UnimplementedBackupServiceServer
}

func (s *testBackupHealthServer) Health(context.Context, *backupv1.HealthRequest) (*backupv1.HealthResponse, error) {
	return &backupv1.HealthResponse{
		Status:        "SERVING",
		Version:       "test-backupd",
		UptimeSeconds: 42,
	}, nil
}
