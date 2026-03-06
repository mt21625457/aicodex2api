package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type usageSettlementBillingCacheStub struct {
	BillingCache

	deductCalls                  int
	deductErr                    error
	invalidateUserBalanceCalls   int
	updateSubscriptionUsageCalls int
	lastSubscriptionUsageAmount  float64
	invalidateSubscriptionCalls  int
}

func (s *usageSettlementBillingCacheStub) GetUserBalance(context.Context, int64) (float64, error) {
	return 0, errors.New("not implemented")
}

func (s *usageSettlementBillingCacheStub) SetUserBalance(context.Context, int64, float64) error {
	return errors.New("not implemented")
}

func (s *usageSettlementBillingCacheStub) DeductUserBalance(context.Context, int64, float64) error {
	s.deductCalls++
	return s.deductErr
}

func (s *usageSettlementBillingCacheStub) InvalidateUserBalance(context.Context, int64) error {
	s.invalidateUserBalanceCalls++
	return nil
}

func (s *usageSettlementBillingCacheStub) GetSubscriptionCache(context.Context, int64, int64) (*SubscriptionCacheData, error) {
	return nil, errors.New("not implemented")
}

func (s *usageSettlementBillingCacheStub) SetSubscriptionCache(context.Context, int64, int64, *SubscriptionCacheData) error {
	return errors.New("not implemented")
}

func (s *usageSettlementBillingCacheStub) UpdateSubscriptionUsage(context.Context, int64, int64, float64) error {
	s.updateSubscriptionUsageCalls++
	return nil
}

func (s *usageSettlementBillingCacheStub) InvalidateSubscriptionCache(context.Context, int64, int64) error {
	s.invalidateSubscriptionCalls++
	return nil
}

func newUsageSettlementServiceForTest(usageRepo UsageLogRepository, userRepo UserRepository, subRepo UserSubscriptionRepository, cache BillingCache) *UsageSettlementService {
	cfg := &config.Config{
		Default: config.DefaultConfig{
			RateMultiplier: 1,
		},
	}
	var billingCacheService *BillingCacheService
	if cache != nil {
		billingCacheService = &BillingCacheService{cache: cache}
	}
	return NewUsageSettlementService(usageRepo, userRepo, subRepo, billingCacheService, cfg)
}

func TestUsageSettlementServiceRecord_DuplicateWithPendingBillingEntryStillBills(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{
		inserted: false,
		nextID:   1000,
		billingEntry: &UsageBillingEntry{
			ID:         9101,
			UsageLogID: 1000,
			Applied:    false,
		},
	}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	cache := &usageSettlementBillingCacheStub{}
	svc := newUsageSettlementServiceForTest(usageRepo, userRepo, subRepo, cache)

	result, err := svc.Record(context.Background(), &UsageSettlementInput{
		UsageLog: &UsageLog{
			UserID:    2001,
			APIKeyID:  3001,
			AccountID: 4001,
			RequestID: "dup_pending_entry",
			Model:     "claude-3-5-sonnet",
			CreatedAt: time.Now(),
		},
		BillingType:         BillingTypeBalance,
		BalanceDeltaUSD:     1.23,
		APIKeyID:            3001,
		APIKeyQuota:         10,
		APIKeyQuotaDeltaUSD: 1.23,
		LogComponent:        "service.test",
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Inserted)
	require.True(t, result.ShouldBill)
	require.InDelta(t, 1.23, result.BilledAmount, 1e-10)
	require.Equal(t, 1, usageRepo.calls)
	require.Equal(t, 1, usageRepo.getCalls)
	require.Equal(t, 0, usageRepo.upsertCalls)
	require.Equal(t, 1, usageRepo.markAppliedCalls)
	require.Equal(t, 1, userRepo.deductCalls)
	require.Equal(t, 1, cache.deductCalls)
}

func TestUsageSettlementServiceRecord_DuplicateWithoutBillingEntrySkipsImmediateBilling(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: false}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	cache := &usageSettlementBillingCacheStub{}
	svc := newUsageSettlementServiceForTest(usageRepo, userRepo, subRepo, cache)

	result, err := svc.Record(context.Background(), &UsageSettlementInput{
		UsageLog: &UsageLog{
			UserID:    2002,
			APIKeyID:  3002,
			AccountID: 4002,
			RequestID: "dup_missing_entry",
			Model:     "claude-3-5-sonnet",
			CreatedAt: time.Now(),
		},
		BillingType:         BillingTypeBalance,
		BalanceDeltaUSD:     2.34,
		APIKeyID:            3002,
		APIKeyQuota:         10,
		APIKeyQuotaDeltaUSD: 2.34,
		LogComponent:        "service.test",
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.False(t, result.Inserted)
	require.False(t, result.ShouldBill)
	require.Zero(t, result.BilledAmount)
	require.Equal(t, 1, usageRepo.calls)
	require.Equal(t, 1, usageRepo.getCalls)
	require.Equal(t, 0, usageRepo.upsertCalls)
	require.Equal(t, 0, usageRepo.markAppliedCalls)
	require.Equal(t, 0, usageRepo.markRetryCalls)
	require.Equal(t, 0, userRepo.deductCalls)
	require.Equal(t, 0, cache.deductCalls)
}

func TestUsageSettlementServiceRecord_UpsertFailureFallsBackToInlineBilling(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{
		inserted:        true,
		billingEntryErr: errors.New("upsert failed"),
	}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	cache := &usageSettlementBillingCacheStub{}
	svc := newUsageSettlementServiceForTest(usageRepo, userRepo, subRepo, cache)

	result, err := svc.Record(context.Background(), &UsageSettlementInput{
		UsageLog: &UsageLog{
			UserID:    2003,
			APIKeyID:  3003,
			AccountID: 4003,
			RequestID: "upsert_fallback_inline",
			Model:     "claude-3-5-sonnet",
			CreatedAt: time.Now(),
		},
		BillingType:         BillingTypeBalance,
		BalanceDeltaUSD:     3.45,
		APIKeyID:            3003,
		APIKeyQuota:         10,
		APIKeyQuotaDeltaUSD: 3.45,
		LogComponent:        "service.test",
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Inserted)
	require.True(t, result.ShouldBill)
	require.InDelta(t, 3.45, result.BilledAmount, 1e-10)
	require.Equal(t, 1, usageRepo.calls)
	require.Equal(t, 1, usageRepo.upsertCalls)
	require.Equal(t, 0, usageRepo.markAppliedCalls)
	require.Equal(t, 0, usageRepo.markRetryCalls)
	require.Equal(t, 1, userRepo.deductCalls)
	require.Equal(t, 1, cache.deductCalls)
}

func TestUsageSettlementServiceRecord_BalanceDeductFailureInvalidatesCacheAndMarksRetry(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	userRepo := &openAIRecordUsageUserRepoStub{deductErr: errors.New("db failed")}
	subRepo := &openAIRecordUsageSubRepoStub{}
	cache := &usageSettlementBillingCacheStub{}
	svc := newUsageSettlementServiceForTest(usageRepo, userRepo, subRepo, cache)

	result, err := svc.Record(context.Background(), &UsageSettlementInput{
		UsageLog: &UsageLog{
			UserID:    2004,
			APIKeyID:  3004,
			AccountID: 4004,
			RequestID: "balance_db_failed",
			Model:     "claude-3-5-sonnet",
			CreatedAt: time.Now(),
		},
		BillingType:         BillingTypeBalance,
		BalanceDeltaUSD:     4.56,
		APIKeyID:            3004,
		APIKeyQuota:         10,
		APIKeyQuotaDeltaUSD: 4.56,
		LogComponent:        "service.test",
	})

	require.Nil(t, result)
	require.Error(t, err)
	require.ErrorContains(t, err, "deduct balance")
	require.Equal(t, 1, usageRepo.calls)
	require.Equal(t, 1, usageRepo.upsertCalls)
	require.Equal(t, 0, usageRepo.markAppliedCalls)
	require.Equal(t, 1, usageRepo.markRetryCalls)
	require.Equal(t, 1, userRepo.deductCalls)
	require.Equal(t, 1, cache.deductCalls)
	require.Equal(t, 1, cache.invalidateUserBalanceCalls)
}

func TestUsageSettlementServiceRecord_SubscriptionSuccessUpdatesSubscriptionCache(t *testing.T) {
	subID := int64(5005)
	groupID := int64(6005)
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	cache := &usageSettlementBillingCacheStub{}
	svc := newUsageSettlementServiceForTest(usageRepo, userRepo, subRepo, cache)

	result, err := svc.Record(context.Background(), &UsageSettlementInput{
		UsageLog: &UsageLog{
			UserID:         2005,
			APIKeyID:       3005,
			AccountID:      4005,
			RequestID:      "subscription_success",
			Model:          "claude-3-5-sonnet",
			GroupID:        &groupID,
			SubscriptionID: &subID,
			CreatedAt:      time.Now(),
		},
		BillingType:          BillingTypeSubscription,
		BalanceDeltaUSD:      1.11,
		SubscriptionDeltaUSD: 5.67,
		APIKeyID:             3005,
		APIKeyQuota:          10,
		APIKeyQuotaDeltaUSD:  1.11,
		LogComponent:         "service.test",
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Inserted)
	require.True(t, result.ShouldBill)
	require.InDelta(t, 5.67, result.BilledAmount, 1e-10)
	require.Equal(t, 1, usageRepo.upsertCalls)
	require.Equal(t, 1, usageRepo.markAppliedCalls)
	require.Equal(t, 1, subRepo.incrementCalls)
	require.Equal(t, 0, userRepo.deductCalls)
	require.Equal(t, 0, cache.deductCalls)
	require.Equal(t, 1, cache.updateSubscriptionUsageCalls)
}

func TestUsageSettlementServiceRecord_UpdatesAPIKeyQuotaWhenBilled(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	cache := &usageSettlementBillingCacheStub{}
	quota := &openAIRecordUsageAPIKeyQuotaStub{}
	svc := newUsageSettlementServiceForTest(usageRepo, userRepo, subRepo, cache)

	result, err := svc.Record(context.Background(), &UsageSettlementInput{
		UsageLog: &UsageLog{
			UserID:    2006,
			APIKeyID:  3006,
			AccountID: 4006,
			RequestID: "apikey_quota_update",
			Model:     "claude-3-5-sonnet",
			CreatedAt: time.Now(),
		},
		BillingType:         BillingTypeBalance,
		BalanceDeltaUSD:     6.78,
		APIKeyID:            3006,
		APIKeyQuota:         10,
		APIKeyQuotaDeltaUSD: 6.78,
		APIKeyService:       quota,
		LogComponent:        "service.test",
	})

	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.ShouldBill)
	require.Equal(t, 1, quota.calls)
}
