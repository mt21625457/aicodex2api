package grpcserver

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestIncomingRequestID(t *testing.T) {
	t.Parallel()

	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-request-id", "req-123"))
	require.Equal(t, "req-123", incomingRequestID(ctx))
}

func TestNormalizeGRPCError(t *testing.T) {
	t.Parallel()

	grpcErr := status.Error(codes.InvalidArgument, "bad")
	require.Equal(t, grpcErr, normalizeGRPCError(grpcErr))

	plain := normalizeGRPCError(errors.New("plain error"))
	require.Equal(t, codes.Internal, status.Code(plain))
	require.Contains(t, status.Convert(plain).Message(), "plain error")
}

func TestApplyMethodTimeout(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	callCtx, cancel := applyMethodTimeout(ctx, "/backup.v1.BackupService/Health")
	defer cancel()
	deadline, ok := callCtx.Deadline()
	require.True(t, ok)
	require.WithinDuration(t, time.Now().Add(1*time.Second), deadline, 200*time.Millisecond)

	shortCtx, shortCancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer shortCancel()
	callCtx2, cancel2 := applyMethodTimeout(shortCtx, "/backup.v1.BackupService/UpdateConfig")
	defer cancel2()
	deadline2, ok2 := callCtx2.Deadline()
	require.True(t, ok2)
	require.WithinDuration(t, time.Now().Add(200*time.Millisecond), deadline2, 200*time.Millisecond)
}
