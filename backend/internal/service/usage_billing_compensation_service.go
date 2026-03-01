package service

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

const (
	defaultUsageBillingCompensationInterval   = 20 * time.Second
	defaultUsageBillingCompensationBatchSize  = 64
	defaultUsageBillingCompensationTaskTimout = 8 * time.Second
	defaultUsageBillingCompensationStaleAfter = 3 * time.Minute
)

// UsageBillingCompensationService retries pending usage charges in billing_usage_entries.
// It only runs when usageLogRepo supports UsageBillingEntryStore.
type UsageBillingCompensationService struct {
	usageLogRepo UsageLogRepository
	userRepo     UserRepository
	userSubRepo  UserSubscriptionRepository
	billingCache *BillingCacheService
	cfg          *config.Config

	startOnce sync.Once
	stopOnce  sync.Once
	stopCh    chan struct{}
}

func NewUsageBillingCompensationService(
	usageLogRepo UsageLogRepository,
	userRepo UserRepository,
	userSubRepo UserSubscriptionRepository,
	billingCache *BillingCacheService,
	cfg *config.Config,
) *UsageBillingCompensationService {
	return &UsageBillingCompensationService{
		usageLogRepo: usageLogRepo,
		userRepo:     userRepo,
		userSubRepo:  userSubRepo,
		billingCache: billingCache,
		cfg:          cfg,
		stopCh:       make(chan struct{}),
	}
}

func (s *UsageBillingCompensationService) Start() {
	if s == nil || s.store() == nil {
		return
	}
	if s.cfg != nil && s.cfg.RunMode == config.RunModeSimple {
		return
	}
	s.startOnce.Do(func() {
		slog.Info("usage_billing_compensation.started",
			"interval", defaultUsageBillingCompensationInterval.String(),
			"batch_size", defaultUsageBillingCompensationBatchSize,
		)
		go s.runLoop()
	})
}

func (s *UsageBillingCompensationService) Stop() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		close(s.stopCh)
		slog.Info("usage_billing_compensation.stopped")
	})
}

func (s *UsageBillingCompensationService) runLoop() {
	ticker := time.NewTicker(defaultUsageBillingCompensationInterval)
	defer ticker.Stop()

	s.processOnce()

	for {
		select {
		case <-ticker.C:
			s.processOnce()
		case <-s.stopCh:
			return
		}
	}
}

func (s *UsageBillingCompensationService) processOnce() {
	store := s.store()
	if store == nil {
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), defaultUsageBillingCompensationTaskTimout)
	defer cancel()
	go func() {
		select {
		case <-s.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	entries, err := store.ClaimUsageBillingEntries(ctx, defaultUsageBillingCompensationBatchSize, defaultUsageBillingCompensationStaleAfter)
	if err != nil {
		slog.Warn("usage_billing_compensation.claim_failed", "error", err)
		return
	}
	for i := range entries {
		if ctx.Err() != nil {
			return
		}
		s.processEntry(ctx, entries[i])
	}
}

func (s *UsageBillingCompensationService) processEntry(ctx context.Context, entry UsageBillingEntry) {
	if entry.Applied || entry.DeltaUSD <= 0 {
		s.markApplied(ctx, entry)
		return
	}

	if err := s.applyEntry(ctx, entry); err != nil {
		s.markRetry(ctx, entry, err)
		return
	}
}

func (s *UsageBillingCompensationService) applyEntry(ctx context.Context, entry UsageBillingEntry) error {
	switch entry.BillingType {
	case BillingTypeSubscription:
		return s.applySubscriptionEntry(ctx, entry)
	default:
		return s.applyBalanceEntry(ctx, entry)
	}
}

func (s *UsageBillingCompensationService) applyBalanceEntry(ctx context.Context, entry UsageBillingEntry) error {
	if s.userRepo == nil {
		return errors.New("user repository unavailable")
	}

	cacheDeducted := false
	if s.billingCache != nil {
		if err := s.billingCache.DeductBalanceCache(ctx, entry.UserID, entry.DeltaUSD); err != nil {
			slog.Warn("usage_billing_compensation.balance_cache_deduct_failed",
				"entry_id", entry.ID,
				"user_id", entry.UserID,
				"amount", entry.DeltaUSD,
				"error", err,
			)
			_ = s.billingCache.InvalidateUserBalance(ctx, entry.UserID)
		} else {
			cacheDeducted = true
		}
	}

	if err := s.runWithTx(ctx, func(txCtx context.Context) error {
		if err := s.userRepo.DeductBalance(txCtx, entry.UserID, entry.DeltaUSD); err != nil {
			return err
		}
		return s.store().MarkUsageBillingEntryApplied(txCtx, entry.ID)
	}); err != nil {
		if s.billingCache != nil && cacheDeducted {
			_ = s.billingCache.InvalidateUserBalance(ctx, entry.UserID)
		}
		return err
	}

	return nil
}

func (s *UsageBillingCompensationService) applySubscriptionEntry(ctx context.Context, entry UsageBillingEntry) error {
	if s.userSubRepo == nil {
		return errors.New("subscription repository unavailable")
	}
	if entry.SubscriptionID == nil {
		return errors.New("subscription_id is nil for subscription billing")
	}

	if err := s.runWithTx(ctx, func(txCtx context.Context) error {
		if err := s.userSubRepo.IncrementUsage(txCtx, *entry.SubscriptionID, entry.DeltaUSD); err != nil {
			return err
		}
		return s.store().MarkUsageBillingEntryApplied(txCtx, entry.ID)
	}); err != nil {
		return err
	}

	if s.billingCache != nil {
		sub, err := s.userSubRepo.GetByID(ctx, *entry.SubscriptionID)
		if err == nil && sub != nil {
			_ = s.billingCache.InvalidateSubscription(ctx, entry.UserID, sub.GroupID)
		}
	}

	return nil
}

func (s *UsageBillingCompensationService) markApplied(ctx context.Context, entry UsageBillingEntry) {
	store := s.store()
	if store == nil {
		return
	}
	if err := store.MarkUsageBillingEntryApplied(ctx, entry.ID); err != nil {
		slog.Warn("usage_billing_compensation.mark_applied_failed", "entry_id", entry.ID, "error", err)
	}
}

func (s *UsageBillingCompensationService) markRetry(ctx context.Context, entry UsageBillingEntry, cause error) {
	store := s.store()
	if store == nil {
		return
	}
	errMsg := strings.TrimSpace(cause.Error())
	if len(errMsg) > 500 {
		errMsg = errMsg[:500]
	}
	backoff := usageBillingRetryBackoff(entry.AttemptCount)
	nextRetryAt := time.Now().Add(backoff)
	if err := store.MarkUsageBillingEntryRetry(ctx, entry.ID, nextRetryAt, errMsg); err != nil {
		slog.Warn("usage_billing_compensation.mark_retry_failed",
			"entry_id", entry.ID,
			"next_retry_at", nextRetryAt,
			"error", err,
		)
		return
	}
	slog.Warn("usage_billing_compensation.requeued",
		"entry_id", entry.ID,
		"attempt", entry.AttemptCount,
		"next_retry_at", nextRetryAt,
		"error", errMsg,
	)
}

func (s *UsageBillingCompensationService) runWithTx(ctx context.Context, fn func(txCtx context.Context) error) error {
	if runner, ok := s.usageLogRepo.(UsageBillingTxRunner); ok && runner != nil {
		return runner.WithUsageBillingTx(ctx, fn)
	}
	return fn(ctx)
}

func (s *UsageBillingCompensationService) store() UsageBillingEntryStore {
	store, ok := s.usageLogRepo.(UsageBillingEntryStore)
	if !ok {
		return nil
	}
	return store
}
