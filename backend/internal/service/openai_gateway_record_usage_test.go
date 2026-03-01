package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type openAIRecordUsageLogRepoStub struct {
	UsageLogRepository

	inserted bool
	err      error
	calls    int
}

func (s *openAIRecordUsageLogRepoStub) Create(ctx context.Context, log *UsageLog) (bool, error) {
	s.calls++
	return s.inserted, s.err
}

type openAIRecordUsageUserRepoStub struct {
	UserRepository

	deductCalls int
}

func (s *openAIRecordUsageUserRepoStub) DeductBalance(ctx context.Context, id int64, amount float64) error {
	s.deductCalls++
	return nil
}

type openAIRecordUsageSubRepoStub struct {
	UserSubscriptionRepository

	incrementCalls int
}

func (s *openAIRecordUsageSubRepoStub) IncrementUsage(ctx context.Context, id int64, costUSD float64) error {
	s.incrementCalls++
	return nil
}

func newOpenAIRecordUsageServiceForTest(usageRepo UsageLogRepository, userRepo UserRepository, subRepo UserSubscriptionRepository) *OpenAIGatewayService {
	cfg := &config.Config{
		Default: config.DefaultConfig{
			RateMultiplier: 1,
		},
	}
	return &OpenAIGatewayService{
		usageLogRepo:        usageRepo,
		userRepo:            userRepo,
		userSubRepo:         subRepo,
		cfg:                 cfg,
		billingService:      NewBillingService(cfg, nil),
		billingCacheService: &BillingCacheService{},
		deferredService:     &DeferredService{},
	}
}

func TestOpenAIGatewayServiceRecordUsage_NoBillingWhenCreateUsageLogFails(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{
		err: errors.New("write usage log failed"),
	}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo)

	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID: "resp_test_create_fail",
			Usage: OpenAIUsage{
				InputTokens:  12,
				OutputTokens: 8,
			},
			Model:    "gpt-5.1",
			Duration: time.Second,
		},
		APIKey: &APIKey{
			ID: 1001,
		},
		User: &User{
			ID: 2001,
		},
		Account: &Account{
			ID: 3001,
		},
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "create usage log")
	require.Equal(t, 1, usageRepo.calls)
	require.Equal(t, 0, userRepo.deductCalls)
	require.Equal(t, 0, subRepo.incrementCalls)
}

func TestOpenAIGatewayServiceRecordUsage_BillingWhenUsageLogInserted(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{
		inserted: true,
	}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo)

	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID: "resp_test_inserted",
			Usage: OpenAIUsage{
				InputTokens:  20,
				OutputTokens: 10,
			},
			Model:    "gpt-5.1",
			Duration: time.Second,
		},
		APIKey: &APIKey{
			ID: 1002,
		},
		User: &User{
			ID: 2002,
		},
		Account: &Account{
			ID: 3002,
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.calls)
	require.Equal(t, 1, userRepo.deductCalls)
	require.Equal(t, 0, subRepo.incrementCalls)
}
