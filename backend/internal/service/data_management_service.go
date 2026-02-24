package service

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"time"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

const (
	DefaultBackupAgentSocketPath = "/tmp/sub2api-backup.sock"

	BackupAgentSocketMissingReason = "BACKUP_AGENT_SOCKET_MISSING"
	BackupAgentUnavailableReason   = "BACKUP_AGENT_UNAVAILABLE"
)

var (
	ErrBackupAgentSocketMissing = infraerrors.ServiceUnavailable(
		BackupAgentSocketMissingReason,
		"backup agent socket is missing",
	)
	ErrBackupAgentUnavailable = infraerrors.ServiceUnavailable(
		BackupAgentUnavailableReason,
		"backup agent is unavailable",
	)
)

type DataManagementAgentHealth struct {
	Enabled    bool
	Reason     string
	SocketPath string
	Agent      *DataManagementAgentInfo
}

type DataManagementAgentInfo struct {
	Status        string
	Version       string
	UptimeSeconds int64
}

type DataManagementService struct {
	socketPath  string
	dialTimeout time.Duration
}

func NewDataManagementService() *DataManagementService {
	return NewDataManagementServiceWithOptions(DefaultBackupAgentSocketPath, 500*time.Millisecond)
}

func NewDataManagementServiceWithOptions(socketPath string, dialTimeout time.Duration) *DataManagementService {
	path := strings.TrimSpace(socketPath)
	if path == "" {
		path = DefaultBackupAgentSocketPath
	}
	if dialTimeout <= 0 {
		dialTimeout = 500 * time.Millisecond
	}
	return &DataManagementService{
		socketPath:  path,
		dialTimeout: dialTimeout,
	}
}

func (s *DataManagementService) SocketPath() string {
	if s == nil || strings.TrimSpace(s.socketPath) == "" {
		return DefaultBackupAgentSocketPath
	}
	return s.socketPath
}

func (s *DataManagementService) GetAgentHealth(ctx context.Context) DataManagementAgentHealth {
	socketPath := s.SocketPath()
	health := DataManagementAgentHealth{
		Enabled:    false,
		Reason:     BackupAgentUnavailableReason,
		SocketPath: socketPath,
	}

	info, err := os.Stat(socketPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			health.Reason = BackupAgentSocketMissingReason
		}
		return health
	}
	if info.Mode()&os.ModeSocket == 0 {
		return health
	}

	dialer := net.Dialer{Timeout: s.dialTimeout}
	conn, err := dialer.DialContext(ctx, "unix", socketPath)
	if err != nil {
		return health
	}
	_ = conn.Close()

	agent, err := s.probeBackupHealth(ctx)
	if err != nil {
		return health
	}

	health.Enabled = true
	health.Reason = ""
	health.Agent = agent
	return health
}

func (s *DataManagementService) EnsureAgentEnabled(ctx context.Context) error {
	health := s.GetAgentHealth(ctx)
	if health.Enabled {
		return nil
	}

	metadata := map[string]string{"socket_path": health.SocketPath}
	if health.Reason == BackupAgentSocketMissingReason {
		return ErrBackupAgentSocketMissing.WithMetadata(metadata)
	}
	return ErrBackupAgentUnavailable.WithMetadata(metadata)
}
