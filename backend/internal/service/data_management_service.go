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
	DefaultDataManagementAgentSocketPath = "/tmp/sub2api-datamanagement.sock"
	LegacyBackupAgentSocketPath          = "/tmp/sub2api-backup.sock"

	DataManagementAgentSocketMissingReason = "DATA_MANAGEMENT_AGENT_SOCKET_MISSING"
	DataManagementAgentUnavailableReason   = "DATA_MANAGEMENT_AGENT_UNAVAILABLE"

	// Deprecated: keep old names for compatibility.
	DefaultBackupAgentSocketPath   = DefaultDataManagementAgentSocketPath
	BackupAgentSocketMissingReason = DataManagementAgentSocketMissingReason
	BackupAgentUnavailableReason   = DataManagementAgentUnavailableReason
)

var (
	ErrDataManagementAgentSocketMissing = infraerrors.ServiceUnavailable(
		DataManagementAgentSocketMissingReason,
		"data management agent socket is missing",
	)
	ErrDataManagementAgentUnavailable = infraerrors.ServiceUnavailable(
		DataManagementAgentUnavailableReason,
		"data management agent is unavailable",
	)

	// Deprecated: keep old names for compatibility.
	ErrBackupAgentSocketMissing = ErrDataManagementAgentSocketMissing
	ErrBackupAgentUnavailable   = ErrDataManagementAgentUnavailable
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
	return NewDataManagementServiceWithOptions(DefaultDataManagementAgentSocketPath, 500*time.Millisecond)
}

func NewDataManagementServiceWithOptions(socketPath string, dialTimeout time.Duration) *DataManagementService {
	path := strings.TrimSpace(socketPath)
	if path == "" {
		path = DefaultDataManagementAgentSocketPath
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
		return DefaultDataManagementAgentSocketPath
	}
	return s.socketPath
}

func (s *DataManagementService) GetAgentHealth(ctx context.Context) DataManagementAgentHealth {
	primaryPath := s.SocketPath()
	health := s.getAgentHealthBySocket(ctx, primaryPath)
	if health.Enabled || primaryPath != DefaultDataManagementAgentSocketPath {
		return health
	}

	fallbackPath := strings.TrimSpace(LegacyBackupAgentSocketPath)
	if fallbackPath == "" || fallbackPath == primaryPath {
		return health
	}

	fallback := s.getAgentHealthBySocket(ctx, fallbackPath)
	if fallback.Enabled {
		return fallback
	}
	return health
}

func (s *DataManagementService) getAgentHealthBySocket(ctx context.Context, socketPath string) DataManagementAgentHealth {
	health := DataManagementAgentHealth{
		Enabled:    false,
		Reason:     DataManagementAgentUnavailableReason,
		SocketPath: socketPath,
	}

	info, err := os.Stat(socketPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			health.Reason = DataManagementAgentSocketMissingReason
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

	agent, err := s.probeAgentHealth(ctx, socketPath)
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
	if health.Reason == DataManagementAgentSocketMissingReason {
		return ErrDataManagementAgentSocketMissing.WithMetadata(metadata)
	}
	return ErrDataManagementAgentUnavailable.WithMetadata(metadata)
}
