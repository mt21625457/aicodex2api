package grpcserver

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

var defaultMethodTimeouts = map[string]time.Duration{
	"/backup.v1.BackupService/Health":          1 * time.Second,
	"/backup.v1.BackupService/GetConfig":       2 * time.Second,
	"/backup.v1.BackupService/ListBackupJobs":  2 * time.Second,
	"/backup.v1.BackupService/GetBackupJob":    2 * time.Second,
	"/backup.v1.BackupService/CreateBackupJob": 3 * time.Second,
	"/backup.v1.BackupService/UpdateConfig":    5 * time.Second,
	"/backup.v1.BackupService/ValidateS3":      5 * time.Second,
}

func UnaryServerInterceptor(logger *log.Logger) grpc.UnaryServerInterceptor {
	if logger == nil {
		logger = log.Default()
	}

	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		method := ""
		if info != nil {
			method = info.FullMethod
		}
		requestID := incomingRequestID(ctx)

		if requestID != "" {
			_ = grpc.SetHeader(ctx, metadata.Pairs("x-request-id", requestID))
		}

		callCtx, cancel := applyMethodTimeout(ctx, method)
		defer cancel()

		start := time.Now()
		defer func() {
			if recovered := recover(); recovered != nil {
				err = status.Error(codes.Internal, "panic recovered")
				logger.Printf(
					"[backupd-grpc] request_id=%s method=%s code=%s duration_ms=%d panic=%q",
					requestID,
					method,
					codes.Internal.String(),
					time.Since(start).Milliseconds(),
					sanitizeLogValue(fmt.Sprint(recovered)),
				)
				return
			}

			err = normalizeGRPCError(err)
			logger.Printf(
				"[backupd-grpc] request_id=%s method=%s code=%s duration_ms=%d err=%q",
				requestID,
				method,
				status.Code(err).String(),
				time.Since(start).Milliseconds(),
				sanitizeLogValue(status.Convert(err).Message()),
			)
		}()

		resp, err = handler(callCtx, req)
		return resp, err
	}
}

func applyMethodTimeout(ctx context.Context, method string) (context.Context, context.CancelFunc) {
	timeout, ok := defaultMethodTimeouts[method]
	if !ok || timeout <= 0 {
		return context.WithCancel(ctx)
	}

	if deadline, hasDeadline := ctx.Deadline(); hasDeadline {
		if remaining := time.Until(deadline); remaining > 0 && remaining <= timeout {
			return context.WithCancel(ctx)
		}
	}
	return context.WithTimeout(ctx, timeout)
}

func incomingRequestID(ctx context.Context) string {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}

	for _, key := range []string{"x-request-id", "request-id", "x_request_id"} {
		values := md.Get(key)
		if len(values) == 0 {
			continue
		}
		value := strings.TrimSpace(values[0])
		if value != "" {
			return value
		}
	}
	return ""
}

func normalizeGRPCError(err error) error {
	if err == nil {
		return nil
	}

	if _, ok := status.FromError(err); ok {
		return err
	}
	return status.Error(codes.Internal, sanitizeLogValue(err.Error()))
}

func sanitizeLogValue(value string) string {
	normalized := strings.TrimSpace(value)
	normalized = strings.ReplaceAll(normalized, "\n", " ")
	normalized = strings.ReplaceAll(normalized, "\r", " ")
	if normalized == "" {
		return "-"
	}
	if len(normalized) > 512 {
		return normalized[:512]
	}
	return normalized
}
