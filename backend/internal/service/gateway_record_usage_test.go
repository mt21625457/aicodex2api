package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func newGatewayRecordUsageServiceForTest(usageRepo UsageLogRepository, userRepo UserRepository, subRepo UserSubscriptionRepository) *GatewayService {
	cfg := &config.Config{
		Default: config.DefaultConfig{
			RateMultiplier: 1,
		},
	}
	return &GatewayService{
		usageLogRepo:        usageRepo,
		userRepo:            userRepo,
		userSubRepo:         subRepo,
		cfg:                 cfg,
		billingService:      NewBillingService(cfg, nil),
		billingCacheService: &BillingCacheService{},
		deferredService:     &DeferredService{},
	}
}

func TestGatewayServiceRecordUsage_PricingFailureReturnsError(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, subRepo)

	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "resp_gateway_pricing_fail",
			Usage: ClaudeUsage{
				InputTokens:  12,
				OutputTokens: 8,
			},
			Model:    "unknown-model",
			Duration: time.Second,
		},
		APIKey:  &APIKey{ID: 1101},
		User:    &User{ID: 2101},
		Account: &Account{ID: 3101},
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "calculate cost")
	require.Equal(t, 0, usageRepo.calls)
	require.Equal(t, 0, userRepo.deductCalls)
	require.Equal(t, 0, subRepo.incrementCalls)
}

func TestGatewayServiceRecordUsage_SimpleModePricingFailureFallsBackToZeroCost(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, subRepo)
	svc.cfg.RunMode = config.RunModeSimple

	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "resp_gateway_pricing_simple",
			Usage: ClaudeUsage{
				InputTokens:  12,
				OutputTokens: 8,
			},
			Model:    "unknown-model",
			Duration: time.Second,
		},
		APIKey:  &APIKey{ID: 1102},
		User:    &User{ID: 2102},
		Account: &Account{ID: 3102},
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.calls)
	require.NotNil(t, usageRepo.lastLog)
	require.Equal(t, 0.0, usageRepo.lastLog.ActualCost)
	require.Equal(t, 0, userRepo.deductCalls)
}

func TestGatewayServiceRecordUsage_UsesBillingEntryAndSynchronousBalanceCache(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, subRepo)
	cache := &openAIRecordUsageBillingCacheStub{}
	svc.billingCacheService = &BillingCacheService{cache: cache}

	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "resp_gateway_billing_entry",
			Usage: ClaudeUsage{
				InputTokens:  20,
				OutputTokens: 10,
			},
			Model:    "claude-3-5-sonnet",
			Duration: time.Second,
		},
		APIKey:  &APIKey{ID: 1103},
		User:    &User{ID: 2103},
		Account: &Account{ID: 3103},
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.calls)
	require.Equal(t, 1, usageRepo.upsertCalls)
	require.Equal(t, 1, usageRepo.markAppliedCalls)
	require.Equal(t, 1, usageRepo.txCalls)
	require.Equal(t, 1, cache.deductCalls)
	require.Equal(t, 1, userRepo.deductCalls)
	require.NotNil(t, usageRepo.billingEntry)
	require.True(t, usageRepo.billingEntry.Applied)
}

func TestGatewayServiceRecordUsage_DeductBalanceCacheFailureReturnsErrorAndMarksRetry(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, subRepo)
	cache := &openAIRecordUsageBillingCacheStub{deductErr: errors.New("cache unavailable")}
	svc.billingCacheService = &BillingCacheService{cache: cache}

	err := svc.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "resp_gateway_cache_fail",
			Usage: ClaudeUsage{
				InputTokens:  20,
				OutputTokens: 10,
			},
			Model:    "claude-3-5-sonnet",
			Duration: time.Second,
		},
		APIKey:  &APIKey{ID: 1104},
		User:    &User{ID: 2104},
		Account: &Account{ID: 3104},
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "deduct balance cache")
	require.Equal(t, 1, usageRepo.calls)
	require.Equal(t, 1, usageRepo.upsertCalls)
	require.Equal(t, 1, usageRepo.markRetryCalls)
	require.Equal(t, 1, cache.deductCalls)
	require.Equal(t, 0, userRepo.deductCalls)
}

func TestGatewayServiceRecordUsageWithLongContext_PricingFailureReturnsError(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newGatewayRecordUsageServiceForTest(usageRepo, userRepo, subRepo)

	err := svc.RecordUsageWithLongContext(context.Background(), &RecordUsageLongContextInput{
		Result: &ForwardResult{
			RequestID: "resp_gateway_longctx_pricing_fail",
			Usage: ClaudeUsage{
				InputTokens:  20,
				OutputTokens: 10,
			},
			Model:    "unknown-model",
			Duration: time.Second,
		},
		APIKey:                &APIKey{ID: 1105},
		User:                  &User{ID: 2105},
		Account:               &Account{ID: 3105},
		LongContextThreshold:  100,
		LongContextMultiplier: 2,
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "calculate cost")
	require.Equal(t, 0, usageRepo.calls)
	require.Equal(t, 0, userRepo.deductCalls)
}
