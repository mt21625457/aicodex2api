package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/Wei-Shaw/sub2api/backup/internal/executor"
	"github.com/Wei-Shaw/sub2api/backup/internal/grpcserver"
	"github.com/Wei-Shaw/sub2api/backup/internal/store/entstore"
	backupv1 "github.com/Wei-Shaw/sub2api/backup/proto/backup/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
)

func main() {
	socketPath := flag.String("socket-path", "/tmp/sub2api-backup.sock", "backupd unix socket path")
	sqlitePath := flag.String("sqlite-path", "/tmp/sub2api-backupd.db", "backupd sqlite database path")
	version := flag.String("version", "dev", "backupd version")
	flag.Parse()

	if err := run(strings.TrimSpace(*socketPath), strings.TrimSpace(*sqlitePath), strings.TrimSpace(*version)); err != nil {
		log.Fatalf("backupd start failed: %v", err)
	}
}

func run(socketPath, sqlitePath, version string) error {
	if socketPath == "" {
		socketPath = "/tmp/sub2api-backup.sock"
	}
	if sqlitePath == "" {
		sqlitePath = "/tmp/sub2api-backupd.db"
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := entstore.Open(ctx, sqlitePath)
	if err != nil {
		return err
	}
	defer func() {
		_ = store.Close()
	}()

	runner := executor.NewRunner(store, executor.Options{Logger: log.Default()})
	if err := runner.Start(); err != nil {
		return err
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = runner.Stop(stopCtx)
	}()

	if err := os.Remove(socketPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return err
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(socketPath)
	}()

	if err := os.Chmod(socketPath, 0o660); err != nil {
		return err
	}

	grpcServer := grpc.NewServer(grpc.UnaryInterceptor(grpcserver.UnaryServerInterceptor(log.Default())))
	healthServer := health.NewServer()
	healthpb.RegisterHealthServer(grpcServer, healthServer)
	healthServer.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	healthServer.SetServingStatus("backup.v1.BackupService", healthpb.HealthCheckResponse_SERVING)
	backupv1.RegisterBackupServiceServer(grpcServer, grpcserver.New(store, version, runner))

	errCh := make(chan error, 1)
	go func() {
		log.Printf("backupd listening on %s", socketPath)
		errCh <- grpcServer.Serve(listener)
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	select {
	case sig := <-sigCh:
		log.Printf("backupd shutting down, signal=%s", sig.String())
		stopped := make(chan struct{})
		go func() {
			grpcServer.GracefulStop()
			close(stopped)
		}()
		select {
		case <-stopped:
			return nil
		case <-time.After(5 * time.Second):
			grpcServer.Stop()
			return nil
		}
	case err := <-errCh:
		return err
	}
}
