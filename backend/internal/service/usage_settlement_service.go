package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

type UsageSettlementService struct {
	usageLogRepo        UsageLogRepository
	userRepo            UserRepository
	userSubRepo         UserSubscriptionRepository
	billingCacheService *BillingCacheService
	cfg                 *config.Config
}

func NewUsageSettlementService(
	usageLogRepo UsageLogRepository,
	userRepo UserRepository,
	userSubRepo UserSubscriptionRepository,
	billingCacheService *BillingCacheService,
	cfg *config.Config,
) *UsageSettlementService {
	return &UsageSettlementService{
		usageLogRepo:        usageLogRepo,
		userRepo:            userRepo,
		userSubRepo:         userSubRepo,
		billingCacheService: billingCacheService,
		cfg:                 cfg,
	}
}

type UsageSettlementInput struct {
	UsageLog             *UsageLog
	BillingType          int8
	BalanceDeltaUSD      float64
	SubscriptionDeltaUSD float64
	APIKeyID             int64
	APIKeyQuota          float64
	APIKeyQuotaDeltaUSD  float64
	APIKeyService        APIKeyQuotaUpdater
	LogComponent         string
}

type UsageSettlementResult struct {
	Inserted     bool
	ShouldBill   bool
	BilledAmount float64
}

func normalizeUsageSettlementLogComponent(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return "service.usage_settlement"
	}
	return strings.TrimSpace(raw)
}

func isSimpleRunMode(cfg *config.Config) bool {
	return cfg != nil && cfg.RunMode == config.RunModeSimple
}

func resolveUsagePricingFailure(cfg *config.Config, logComponent, model, requestID string, err error) (*CostBreakdown, error) {
	if err == nil {
		return nil, nil
	}
	component := normalizeUsageSettlementLogComponent(logComponent)
	if isSimpleRunMode(cfg) {
		logger.LegacyPrintf(
			component,
			"[PricingWarn] calculate cost failed in simple mode, fallback to zero cost: model=%s request_id=%s err=%v",
			model,
			requestID,
			err,
		)
		return &CostBreakdown{}, nil
	}
	logger.LegacyPrintf(
		component,
		"[PricingAlert] calculate cost failed, reject usage record: model=%s request_id=%s err=%v",
		model,
		requestID,
		err,
	)
	return nil, fmt.Errorf("calculate cost: %w", err)
}

func (s *UsageSettlementService) usageBillingEntryStore() UsageBillingEntryStore {
	if s == nil {
		return nil
	}
	store, ok := s.usageLogRepo.(UsageBillingEntryStore)
	if !ok {
		return nil
	}
	return store
}

func (s *UsageSettlementService) usageBillingTxRunner() UsageBillingTxRunner {
	if s == nil {
		return nil
	}
	runner, ok := s.usageLogRepo.(UsageBillingTxRunner)
	if !ok {
		return nil
	}
	return runner
}

func (s *UsageSettlementService) runUsageBillingTx(ctx context.Context, fn func(txCtx context.Context) error) error {
	runner := s.usageBillingTxRunner()
	if runner == nil {
		return fn(ctx)
	}
	return runner.WithUsageBillingTx(ctx, fn)
}

func (s *UsageSettlementService) prepareUsageBillingEntry(
	ctx context.Context,
	usageLog *UsageLog,
	inserted bool,
	billingType int8,
	deltaUSD float64,
	logComponent string,
) (*UsageBillingEntry, bool, error) {
	if deltaUSD <= 0 {
		return nil, false, nil
	}

	component := normalizeUsageSettlementLogComponent(logComponent)
	store := s.usageBillingEntryStore()
	if store == nil {
		if inserted {
			return nil, true, nil
		}
		return nil, false, nil
	}

	if !inserted {
		entry, err := store.GetUsageBillingEntryByUsageLogID(ctx, usageLog.ID)
		if err != nil {
			if errors.Is(err, ErrUsageBillingEntryNotFound) {
				logger.LegacyPrintf(
					component,
					"[BillingReconcile] missing billing entry for duplicate usage log, skip immediate billing: usage_log=%d request_id=%s",
					usageLog.ID,
					usageLog.RequestID,
				)
				return nil, false, nil
			}
			logger.LegacyPrintf(
				component,
				"[BillingReconcile] load billing entry failed for duplicate usage log, skip immediate billing: usage_log=%d request_id=%s err=%v",
				usageLog.ID,
				usageLog.RequestID,
				err,
			)
			return nil, false, nil
		}
		return entry, !entry.Applied, nil
	}

	entry, _, err := store.UpsertUsageBillingEntry(ctx, &UsageBillingEntry{
		UsageLogID:     usageLog.ID,
		UserID:         usageLog.UserID,
		APIKeyID:       usageLog.APIKeyID,
		SubscriptionID: usageLog.SubscriptionID,
		BillingType:    billingType,
		DeltaUSD:       deltaUSD,
		Status:         UsageBillingEntryStatusPending,
	})
	if err != nil {
		logger.LegacyPrintf(
			component,
			"[BillingReconcile] upsert billing entry failed, fallback to inline billing: usage_log=%d request_id=%s err=%v",
			usageLog.ID,
			usageLog.RequestID,
			err,
		)
		return nil, true, nil
	}

	return entry, !entry.Applied, nil
}

func (s *UsageSettlementService) markUsageBillingRetry(ctx context.Context, entry *UsageBillingEntry, cause error, logComponent string) {
	if entry == nil || cause == nil {
		return
	}
	store := s.usageBillingEntryStore()
	if store == nil {
		return
	}
	errMsg := strings.TrimSpace(cause.Error())
	if len(errMsg) > 500 {
		errMsg = errMsg[:500]
	}
	nextRetryAt := time.Now().Add(usageBillingRetryBackoff(entry.AttemptCount + 1))
	if err := store.MarkUsageBillingEntryRetry(ctx, entry.ID, nextRetryAt, errMsg); err != nil {
		logger.LegacyPrintf(normalizeUsageSettlementLogComponent(logComponent), "[BillingReconcile] mark retry failed: entry=%d err=%v", entry.ID, err)
	}
}

func (s *UsageSettlementService) Record(ctx context.Context, input *UsageSettlementInput) (*UsageSettlementResult, error) {
	if input == nil || input.UsageLog == nil {
		return nil, errors.New("usage settlement input is nil")
	}
	if s == nil || s.usageLogRepo == nil {
		return nil, errors.New("usage settlement service is unavailable")
	}

	result := &UsageSettlementResult{}
	component := normalizeUsageSettlementLogComponent(input.LogComponent)
	usageLog := input.UsageLog

	inserted, err := s.usageLogRepo.Create(ctx, usageLog)
	if err != nil {
		return nil, fmt.Errorf("create usage log: %w", err)
	}
	result.Inserted = inserted

	if isSimpleRunMode(s.cfg) {
		logger.LegacyPrintf(component, "[SIMPLE MODE] Usage recorded (not billed): user=%d, tokens=%d", usageLog.UserID, usageLog.TotalTokens())
		return result, nil
	}

	billAmount := input.BalanceDeltaUSD
	if input.BillingType == BillingTypeSubscription {
		billAmount = input.SubscriptionDeltaUSD
	}

	billingEntry, shouldBill, err := s.prepareUsageBillingEntry(ctx, usageLog, inserted, input.BillingType, billAmount, component)
	if err != nil {
		return nil, fmt.Errorf("prepare usage billing entry: %w", err)
	}
	result.ShouldBill = shouldBill
	if shouldBill {
		result.BilledAmount = billAmount
	}

	if shouldBill {
		cacheDeducted := false
		if input.BillingType != BillingTypeSubscription && billAmount > 0 && s.billingCacheService != nil {
			if err := s.billingCacheService.DeductBalanceCache(ctx, usageLog.UserID, billAmount); err != nil {
				s.markUsageBillingRetry(ctx, billingEntry, err, component)
				return nil, fmt.Errorf("deduct balance cache: %w", err)
			}
			cacheDeducted = true
		}

		applyErr := s.runUsageBillingTx(ctx, func(txCtx context.Context) error {
			switch input.BillingType {
			case BillingTypeSubscription:
				if s.userSubRepo == nil {
					return errors.New("subscription repository unavailable")
				}
				if usageLog.SubscriptionID == nil {
					return errors.New("subscription_id is nil for subscription billing")
				}
				if err := s.userSubRepo.IncrementUsage(txCtx, *usageLog.SubscriptionID, input.SubscriptionDeltaUSD); err != nil {
					return fmt.Errorf("increment subscription usage: %w", err)
				}
			default:
				if billAmount > 0 {
					if s.userRepo == nil {
						return errors.New("user repository unavailable")
					}
					if err := s.userRepo.DeductBalance(txCtx, usageLog.UserID, billAmount); err != nil {
						return fmt.Errorf("deduct balance: %w", err)
					}
				}
			}
			if billingEntry == nil {
				return nil
			}
			store := s.usageBillingEntryStore()
			if store == nil {
				return nil
			}
			if err := store.MarkUsageBillingEntryApplied(txCtx, billingEntry.ID); err != nil {
				return fmt.Errorf("mark usage billing entry applied: %w", err)
			}
			return nil
		})
		if applyErr != nil {
			if input.BillingType != BillingTypeSubscription && cacheDeducted && s.billingCacheService != nil {
				_ = s.billingCacheService.InvalidateUserBalance(context.Background(), usageLog.UserID)
			}
			s.markUsageBillingRetry(ctx, billingEntry, applyErr, component)
			return nil, applyErr
		}

		if input.BillingType == BillingTypeSubscription && s.billingCacheService != nil && usageLog.GroupID != nil && input.SubscriptionDeltaUSD > 0 {
			s.billingCacheService.QueueUpdateSubscriptionUsage(usageLog.UserID, *usageLog.GroupID, input.SubscriptionDeltaUSD)
		}
	}

	if shouldBill && input.APIKeyQuota > 0 && input.APIKeyQuotaDeltaUSD > 0 && input.APIKeyService != nil {
		if err := input.APIKeyService.UpdateQuotaUsed(ctx, input.APIKeyID, input.APIKeyQuotaDeltaUSD); err != nil {
			logger.LegacyPrintf(component, "Update API key quota failed: %v", err)
		}
	}

	return result, nil
}
