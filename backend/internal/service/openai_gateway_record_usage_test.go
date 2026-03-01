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
	lastLog  *UsageLog
	nextID   int64

	billingEntry        *UsageBillingEntry
	billingEntryErr     error
	upsertCalls         int
	getCalls            int
	markAppliedCalls    int
	markRetryCalls      int
	lastRetryAt         time.Time
	lastRetryErrMessage string
	txCalls             int
}

func (s *openAIRecordUsageLogRepoStub) Create(ctx context.Context, log *UsageLog) (bool, error) {
	s.calls++
	if log != nil {
		if log.ID == 0 {
			if s.nextID == 0 {
				s.nextID = 1000
			}
			log.ID = s.nextID
			s.nextID++
		}
	}
	s.lastLog = log
	return s.inserted, s.err
}

func (s *openAIRecordUsageLogRepoStub) GetUsageBillingEntryByUsageLogID(ctx context.Context, usageLogID int64) (*UsageBillingEntry, error) {
	s.getCalls++
	if s.billingEntryErr != nil {
		return nil, s.billingEntryErr
	}
	if s.billingEntry == nil {
		return nil, ErrUsageBillingEntryNotFound
	}
	if s.billingEntry.UsageLogID != usageLogID {
		return nil, ErrUsageBillingEntryNotFound
	}
	return s.billingEntry, nil
}

func (s *openAIRecordUsageLogRepoStub) UpsertUsageBillingEntry(ctx context.Context, entry *UsageBillingEntry) (*UsageBillingEntry, bool, error) {
	s.upsertCalls++
	if s.billingEntryErr != nil {
		return nil, false, s.billingEntryErr
	}
	if s.billingEntry != nil {
		return s.billingEntry, false, nil
	}
	if entry == nil {
		return nil, false, nil
	}
	copyEntry := *entry
	copyEntry.ID = 9100 + int64(s.upsertCalls)
	copyEntry.Status = UsageBillingEntryStatusPending
	s.billingEntry = &copyEntry
	return s.billingEntry, true, nil
}

func (s *openAIRecordUsageLogRepoStub) MarkUsageBillingEntryApplied(ctx context.Context, entryID int64) error {
	s.markAppliedCalls++
	if s.billingEntry != nil && s.billingEntry.ID == entryID {
		s.billingEntry.Applied = true
		s.billingEntry.Status = UsageBillingEntryStatusApplied
	}
	return nil
}

func (s *openAIRecordUsageLogRepoStub) MarkUsageBillingEntryRetry(ctx context.Context, entryID int64, nextRetryAt time.Time, lastError string) error {
	s.markRetryCalls++
	s.lastRetryAt = nextRetryAt
	s.lastRetryErrMessage = lastError
	if s.billingEntry != nil && s.billingEntry.ID == entryID {
		s.billingEntry.Applied = false
		s.billingEntry.Status = UsageBillingEntryStatusPending
		msg := lastError
		s.billingEntry.LastError = &msg
		s.billingEntry.NextRetryAt = nextRetryAt
	}
	return nil
}

func (s *openAIRecordUsageLogRepoStub) ClaimUsageBillingEntries(ctx context.Context, limit int, processingStaleAfter time.Duration) ([]UsageBillingEntry, error) {
	return nil, nil
}

func (s *openAIRecordUsageLogRepoStub) WithUsageBillingTx(ctx context.Context, fn func(txCtx context.Context) error) error {
	s.txCalls++
	if fn == nil {
		return nil
	}
	return fn(ctx)
}

type openAIRecordUsageUserRepoStub struct {
	UserRepository

	deductCalls int
	deductErr   error
}

func (s *openAIRecordUsageUserRepoStub) DeductBalance(ctx context.Context, id int64, amount float64) error {
	s.deductCalls++
	return s.deductErr
}

type openAIRecordUsageSubRepoStub struct {
	UserSubscriptionRepository

	incrementCalls int
	incrementErr   error
}

func (s *openAIRecordUsageSubRepoStub) IncrementUsage(ctx context.Context, id int64, costUSD float64) error {
	s.incrementCalls++
	return s.incrementErr
}

type openAIRecordUsageBillingCacheStub struct {
	BillingCache

	deductCalls int
	deductErr   error
}

func (s *openAIRecordUsageBillingCacheStub) DeductUserBalance(ctx context.Context, userID int64, amount float64) error {
	s.deductCalls++
	return s.deductErr
}

func (s *openAIRecordUsageBillingCacheStub) GetUserBalance(context.Context, int64) (float64, error) {
	return 0, errors.New("not implemented")
}

func (s *openAIRecordUsageBillingCacheStub) SetUserBalance(context.Context, int64, float64) error {
	return errors.New("not implemented")
}

func (s *openAIRecordUsageBillingCacheStub) InvalidateUserBalance(context.Context, int64) error {
	return nil
}

func (s *openAIRecordUsageBillingCacheStub) GetSubscriptionCache(context.Context, int64, int64) (*SubscriptionCacheData, error) {
	return nil, errors.New("not implemented")
}

func (s *openAIRecordUsageBillingCacheStub) SetSubscriptionCache(context.Context, int64, int64, *SubscriptionCacheData) error {
	return errors.New("not implemented")
}

func (s *openAIRecordUsageBillingCacheStub) UpdateSubscriptionUsage(context.Context, int64, int64, float64) error {
	return errors.New("not implemented")
}

func (s *openAIRecordUsageBillingCacheStub) InvalidateSubscriptionCache(context.Context, int64, int64) error {
	return nil
}

type openAIRecordUsageAPIKeyQuotaStub struct {
	calls int
	err   error
}

func (s *openAIRecordUsageAPIKeyQuotaStub) UpdateQuotaUsed(ctx context.Context, apiKeyID int64, cost float64) error {
	s.calls++
	return s.err
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

func TestOpenAIGatewayServiceRecordUsage_PricingFailureReturnsError(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{
		inserted: true,
	}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo)
	svc.billingService = &BillingService{
		cfg:            &config.Config{},
		fallbackPrices: map[string]*ModelPricing{},
	}

	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID: "resp_pricing_fail",
			Usage: OpenAIUsage{
				InputTokens:  1,
				OutputTokens: 1,
			},
			Model:    "model_pricing_not_found_for_test",
			Duration: time.Second,
		},
		APIKey: &APIKey{
			ID: 1102,
		},
		User: &User{
			ID: 2102,
		},
		Account: &Account{
			ID: 3102,
		},
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "calculate cost")
	require.Equal(t, 0, usageRepo.calls)
	require.Equal(t, 0, userRepo.deductCalls)
	require.Equal(t, 0, subRepo.incrementCalls)
}

func TestOpenAIGatewayServiceRecordUsage_DeductBalanceFailureReturnsError(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{
		inserted: true,
	}
	userRepo := &openAIRecordUsageUserRepoStub{
		deductErr: errors.New("db deduct failed"),
	}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo)

	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID: "resp_deduct_fail",
			Usage: OpenAIUsage{
				InputTokens:  10,
				OutputTokens: 5,
			},
			Model:    "gpt-5.1",
			Duration: time.Second,
		},
		APIKey: &APIKey{
			ID: 1003,
		},
		User: &User{
			ID: 2003,
		},
		Account: &Account{
			ID: 3003,
		},
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "deduct balance")
	require.Equal(t, 1, usageRepo.calls)
	require.Equal(t, 1, userRepo.deductCalls)
	require.Equal(t, 0, subRepo.incrementCalls)
}

func TestOpenAIGatewayServiceRecordUsage_DeductBalanceCacheFailureReturnsError(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{
		inserted: true,
	}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo)
	cache := &openAIRecordUsageBillingCacheStub{
		deductErr: ErrInsufficientBalance,
	}
	svc.billingCacheService = &BillingCacheService{cache: cache}

	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID: "resp_cache_deduct_fail",
			Usage: OpenAIUsage{
				InputTokens:  10,
				OutputTokens: 5,
			},
			Model:    "gpt-5.1",
			Duration: time.Second,
		},
		APIKey: &APIKey{
			ID: 1004,
		},
		User: &User{
			ID: 2004,
		},
		Account: &Account{
			ID: 3004,
		},
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "deduct balance cache")
	require.Equal(t, 1, usageRepo.calls)
	require.Equal(t, 1, cache.deductCalls)
	require.Equal(t, 0, userRepo.deductCalls)
	require.Equal(t, 0, subRepo.incrementCalls)
}

func TestOpenAIGatewayServiceRecordUsage_SubscriptionIncrementFailureReturnsError(t *testing.T) {
	groupID := int64(12)
	usageRepo := &openAIRecordUsageLogRepoStub{
		inserted: true,
	}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{
		incrementErr: errors.New("subscription update failed"),
	}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo)
	svc.billingCacheService = &BillingCacheService{cache: &openAIRecordUsageBillingCacheStub{}}

	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID: "resp_sub_increment_fail",
			Usage: OpenAIUsage{
				InputTokens:  10,
				OutputTokens: 5,
			},
			Model:    "gpt-5.1",
			Duration: time.Second,
		},
		APIKey: &APIKey{
			ID:      1005,
			GroupID: &groupID,
			Group: &Group{
				ID:               groupID,
				SubscriptionType: SubscriptionTypeSubscription,
			},
		},
		User: &User{
			ID: 2005,
		},
		Account: &Account{
			ID: 3005,
		},
		Subscription: &UserSubscription{
			ID: 4005,
		},
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "increment subscription usage")
	require.Equal(t, 1, usageRepo.calls)
	require.Equal(t, 0, userRepo.deductCalls)
	require.Equal(t, 1, subRepo.incrementCalls)
}

func TestOpenAIGatewayServiceRecordUsage_DuplicateUsageLogSkipsBilling(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{
		inserted: false,
	}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo)

	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID: "resp_duplicate",
			Usage: OpenAIUsage{
				InputTokens:  8,
				OutputTokens: 4,
			},
			Model:    "gpt-5.1",
			Duration: time.Second,
		},
		APIKey: &APIKey{
			ID: 1006,
		},
		User: &User{
			ID: 2006,
		},
		Account: &Account{
			ID: 3006,
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.calls)
	require.Equal(t, 0, userRepo.deductCalls)
	require.Equal(t, 0, subRepo.incrementCalls)
}

func TestOpenAIGatewayServiceRecordUsage_SimpleModeSkipsBilling(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{
		inserted: true,
	}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo)
	svc.cfg.RunMode = config.RunModeSimple
	quotaSvc := &openAIRecordUsageAPIKeyQuotaStub{}

	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID: "resp_simple_mode",
			Usage: OpenAIUsage{
				InputTokens:  5,
				OutputTokens: 2,
			},
			Model:    "gpt-5.1",
			Duration: time.Second,
		},
		APIKey: &APIKey{
			ID:    1007,
			Quota: 100,
		},
		User: &User{
			ID: 2007,
		},
		Account: &Account{
			ID: 3007,
		},
		APIKeyService: quotaSvc,
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.calls)
	require.Equal(t, 0, userRepo.deductCalls)
	require.Equal(t, 0, subRepo.incrementCalls)
	require.Equal(t, 0, quotaSvc.calls)
}

func TestOpenAIGatewayServiceRecordUsage_SubscriptionBillingSuccess(t *testing.T) {
	groupID := int64(13)
	usageRepo := &openAIRecordUsageLogRepoStub{
		inserted: true,
	}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo)

	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID: "resp_sub_success",
			Usage: OpenAIUsage{
				InputTokens:  10,
				OutputTokens: 8,
			},
			Model:    "gpt-5.1",
			Duration: time.Second,
		},
		APIKey: &APIKey{
			ID:      1008,
			GroupID: &groupID,
			Group: &Group{
				ID:               groupID,
				SubscriptionType: SubscriptionTypeSubscription,
			},
		},
		User: &User{
			ID: 2008,
		},
		Account: &Account{
			ID: 3008,
		},
		Subscription: &UserSubscription{
			ID: 4008,
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.calls)
	require.Equal(t, 0, userRepo.deductCalls)
	require.Equal(t, 1, subRepo.incrementCalls)
}

func TestOpenAIGatewayServiceRecordUsage_UpdatesAPIKeyQuotaWhenConfigured(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{
		inserted: true,
	}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo)
	quotaSvc := &openAIRecordUsageAPIKeyQuotaStub{}

	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID: "resp_quota_update",
			Usage: OpenAIUsage{
				InputTokens:          1,
				OutputTokens:         1,
				CacheReadInputTokens: 3,
			},
			Model:    "gpt-5.1",
			Duration: time.Second,
		},
		APIKey: &APIKey{
			ID:    1009,
			Quota: 100,
		},
		User: &User{
			ID: 2009,
		},
		Account: &Account{
			ID: 3009,
		},
		APIKeyService: quotaSvc,
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.calls)
	require.NotNil(t, usageRepo.lastLog)
	require.Equal(t, 0, usageRepo.lastLog.InputTokens, "input_tokens 小于 cache_read_tokens 时应被钳制为 0")
	require.Equal(t, 1, userRepo.deductCalls)
	require.Equal(t, 1, quotaSvc.calls)
}

func TestOpenAIGatewayServiceRecordUsage_DuplicateWithPendingBillingEntryStillBills(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{
		inserted: false,
		billingEntry: &UsageBillingEntry{
			ID:          9201,
			UsageLogID:  1000,
			Applied:     false,
			BillingType: BillingTypeBalance,
			DeltaUSD:    1.25,
		},
	}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo)

	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID: "resp_duplicate_pending_entry",
			Usage: OpenAIUsage{
				InputTokens:  8,
				OutputTokens: 2,
			},
			Model:    "gpt-5.1",
			Duration: time.Second,
		},
		APIKey: &APIKey{ID: 1011},
		User:   &User{ID: 2011},
		Account: &Account{
			ID: 3011,
		},
	})

	require.NoError(t, err)
	require.Equal(t, 1, usageRepo.calls)
	require.Equal(t, 1, usageRepo.getCalls)
	require.Equal(t, 1, usageRepo.markAppliedCalls)
	require.Equal(t, 1, userRepo.deductCalls)
}

func TestOpenAIGatewayServiceRecordUsage_BillingFailureMarksRetry(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{
		inserted: true,
	}
	userRepo := &openAIRecordUsageUserRepoStub{
		deductErr: errors.New("deduct failed"),
	}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo)

	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID: "resp_mark_retry",
			Usage: OpenAIUsage{
				InputTokens:  10,
				OutputTokens: 6,
			},
			Model:    "gpt-5.1",
			Duration: time.Second,
		},
		APIKey: &APIKey{
			ID: 1012,
		},
		User: &User{
			ID: 2012,
		},
		Account: &Account{
			ID: 3012,
		},
	})

	require.Error(t, err)
	require.ErrorContains(t, err, "deduct balance")
	require.Equal(t, 1, usageRepo.markRetryCalls)
	require.NotZero(t, usageRepo.lastRetryAt)
	require.NotEmpty(t, usageRepo.lastRetryErrMessage)
	require.Equal(t, 0, usageRepo.markAppliedCalls)
}

func TestResolveOpenAIUsageRequestID_FallbackDeterministic(t *testing.T) {
	reasoning := "medium"
	input := &OpenAIRecordUsageInput{
		FallbackRequestID: "req_fallback_seed",
		APIKey:            &APIKey{ID: 11001},
		Account:           &Account{ID: 21001},
		Result: &OpenAIForwardResult{
			RequestID: "",
			Model:     "gpt-5.1",
			Usage: OpenAIUsage{
				InputTokens:              12,
				OutputTokens:             8,
				CacheCreationInputTokens: 2,
				CacheReadInputTokens:     1,
			},
			Duration:        2300 * time.Millisecond,
			ReasoningEffort: &reasoning,
			Stream:          true,
			OpenAIWSMode:    true,
		},
	}

	got1 := resolveOpenAIUsageRequestID(input)
	got2 := resolveOpenAIUsageRequestID(input)

	require.NotEmpty(t, got1)
	require.Equal(t, got1, got2, "fallback request id should be deterministic")
	require.Contains(t, got1, "wsf_")
}

func TestResolveOpenAIUsageRequestID_FallbackChangesWhenUsageChanges(t *testing.T) {
	base := &OpenAIRecordUsageInput{
		FallbackRequestID: "req_fallback_seed",
		APIKey:            &APIKey{ID: 11002},
		Account:           &Account{ID: 21002},
		Result: &OpenAIForwardResult{
			Model: "gpt-5.1",
			Usage: OpenAIUsage{
				InputTokens:  10,
				OutputTokens: 4,
			},
			Duration: 2 * time.Second,
		},
	}
	changed := &OpenAIRecordUsageInput{
		FallbackRequestID: base.FallbackRequestID,
		APIKey:            base.APIKey,
		Account:           base.Account,
		Result: &OpenAIForwardResult{
			Model: "gpt-5.1",
			Usage: OpenAIUsage{
				InputTokens:  11,
				OutputTokens: 4,
			},
			Duration: 2 * time.Second,
		},
	}

	baseID := resolveOpenAIUsageRequestID(base)
	changedID := resolveOpenAIUsageRequestID(changed)

	require.NotEqual(t, baseID, changedID, "fallback request id should change when usage fingerprint changes")
}

func TestOpenAIGatewayServiceRecordUsage_UsesFallbackRequestIDWhenMissing(t *testing.T) {
	usageRepo := &openAIRecordUsageLogRepoStub{
		inserted: true,
	}
	userRepo := &openAIRecordUsageUserRepoStub{}
	subRepo := &openAIRecordUsageSubRepoStub{}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, userRepo, subRepo)

	input := &OpenAIRecordUsageInput{
		FallbackRequestID: "req_from_handler",
		Result: &OpenAIForwardResult{
			RequestID: "",
			Usage: OpenAIUsage{
				InputTokens:  9,
				OutputTokens: 3,
			},
			Model:    "gpt-5.1",
			Duration: time.Second,
		},
		APIKey: &APIKey{
			ID: 1013,
		},
		User: &User{
			ID: 2013,
		},
		Account: &Account{
			ID: 3013,
		},
	}

	expectedRequestID := resolveOpenAIUsageRequestID(input)
	require.NotEmpty(t, expectedRequestID)

	err := svc.RecordUsage(context.Background(), input)
	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog)
	require.Equal(t, expectedRequestID, usageRepo.lastLog.RequestID)
}
