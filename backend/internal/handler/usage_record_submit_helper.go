package handler

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"go.uber.org/zap"
)

func submitUsageRecordTaskWithFallback(
	component string,
	pool *service.UsageRecordWorkerPool,
	cfg *config.Config,
	task service.UsageRecordTask,
) {
	if task == nil {
		return
	}
	if pool != nil {
		mode := pool.Submit(task)
		if mode != service.UsageRecordSubmitModeDropped {
			return
		}
		// 队列溢出导致 submit 丢弃时，同步兜底执行，避免 usage 漏记费。
		logger.L().With(
			zap.String("component", component),
			zap.String("submit_mode", mode.String()),
		).Warn("usage_record.task_submit_dropped_sync_fallback")
	}

	ctx, cancel := context.WithTimeout(context.Background(), usageRecordSyncFallbackTimeout(cfg))
	defer cancel()
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.L().With(
				zap.String("component", component),
				zap.Any("panic", recovered),
			).Error("usage_record.task_panic_recovered")
		}
	}()
	task(ctx)
}

func usageRecordSyncFallbackTimeout(cfg *config.Config) time.Duration {
	timeout := 10 * time.Second
	if cfg != nil && cfg.Gateway.UsageRecord.TaskTimeoutSeconds > 0 {
		timeout = time.Duration(cfg.Gateway.UsageRecord.TaskTimeoutSeconds) * time.Second
	}
	// keep a sane bound on synchronous fallback to limit request-path blocking.
	if timeout < time.Second {
		return time.Second
	}
	if timeout > 10*time.Second {
		return 10 * time.Second
	}
	return timeout
}

