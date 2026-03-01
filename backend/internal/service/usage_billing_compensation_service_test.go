package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type usageBillingCompRepoStub struct {
	UsageLogRepository

	claimErr error
	claims   []UsageBillingEntry

	markAppliedCalls int
	markRetryCalls   int
	lastRetryID      int64
	lastRetryAt      time.Time
	lastRetryErr     string
}

func (s *usageBillingCompRepoStub) GetUsageBillingEntryByUsageLogID(ctx context.Context, usageLogID int64) (*UsageBillingEntry, error) {
	return nil, ErrUsageBillingEntryNotFound
}

func (s *usageBillingCompRepoStub) UpsertUsageBillingEntry(ctx context.Context, entry *UsageBillingEntry) (*UsageBillingEntry, bool, error) {
	return entry, true, nil
}

func (s *usageBillingCompRepoStub) MarkUsageBillingEntryApplied(ctx context.Context, entryID int64) error {
	s.markAppliedCalls++
	return nil
}

func (s *usageBillingCompRepoStub) MarkUsageBillingEntryRetry(ctx context.Context, entryID int64, nextRetryAt time.Time, lastError string) error {
	s.markRetryCalls++
	s.lastRetryID = entryID
	s.lastRetryAt = nextRetryAt
	s.lastRetryErr = lastError
	return nil
}

func (s *usageBillingCompRepoStub) ClaimUsageBillingEntries(ctx context.Context, limit int, processingStaleAfter time.Duration) ([]UsageBillingEntry, error) {
	if s.claimErr != nil {
		return nil, s.claimErr
	}
	out := make([]UsageBillingEntry, len(s.claims))
	copy(out, s.claims)
	s.claims = nil
	return out, nil
}

func (s *usageBillingCompRepoStub) WithUsageBillingTx(ctx context.Context, fn func(txCtx context.Context) error) error {
	if fn == nil {
		return nil
	}
	return fn(ctx)
}

type usageBillingCompUserRepoStub struct {
	UserRepository

	deductCalls int
	deductErr   error
}

func (s *usageBillingCompUserRepoStub) DeductBalance(ctx context.Context, id int64, amount float64) error {
	s.deductCalls++
	return s.deductErr
}

type usageBillingCompSubRepoStub struct {
	UserSubscriptionRepository

	incrementCalls int
	incrementErr   error
}

func (s *usageBillingCompSubRepoStub) IncrementUsage(ctx context.Context, id int64, costUSD float64) error {
	s.incrementCalls++
	return s.incrementErr
}

func TestUsageBillingCompensationService_ProcessOnceBalanceSuccess(t *testing.T) {
	repo := &usageBillingCompRepoStub{
		claims: []UsageBillingEntry{
			{
				ID:           1,
				UsageLogID:   1001,
				UserID:       2001,
				BillingType:  BillingTypeBalance,
				DeltaUSD:     1.23,
				AttemptCount: 1,
			},
		},
	}
	userRepo := &usageBillingCompUserRepoStub{}
	subRepo := &usageBillingCompSubRepoStub{}
	svc := NewUsageBillingCompensationService(repo, userRepo, subRepo, nil, &config.Config{})

	svc.processOnce()

	require.Equal(t, 1, userRepo.deductCalls)
	require.Equal(t, 1, repo.markAppliedCalls)
	require.Equal(t, 0, repo.markRetryCalls)
}

func TestUsageBillingCompensationService_ProcessOnceBalanceFailureRequeues(t *testing.T) {
	repo := &usageBillingCompRepoStub{
		claims: []UsageBillingEntry{
			{
				ID:           2,
				UsageLogID:   1002,
				UserID:       2002,
				BillingType:  BillingTypeBalance,
				DeltaUSD:     2.34,
				AttemptCount: 2,
			},
		},
	}
	userRepo := &usageBillingCompUserRepoStub{deductErr: errors.New("db down")}
	subRepo := &usageBillingCompSubRepoStub{}
	svc := NewUsageBillingCompensationService(repo, userRepo, subRepo, nil, &config.Config{})

	svc.processOnce()

	require.Equal(t, 1, userRepo.deductCalls)
	require.Equal(t, 0, repo.markAppliedCalls)
	require.Equal(t, 1, repo.markRetryCalls)
	require.Equal(t, int64(2), repo.lastRetryID)
	require.NotZero(t, repo.lastRetryAt)
	require.Contains(t, repo.lastRetryErr, "db down")
}

func TestUsageBillingCompensationService_ProcessOnceSubscriptionSuccess(t *testing.T) {
	subID := int64(4003)
	repo := &usageBillingCompRepoStub{
		claims: []UsageBillingEntry{
			{
				ID:             3,
				UsageLogID:     1003,
				UserID:         2003,
				SubscriptionID: &subID,
				BillingType:    BillingTypeSubscription,
				DeltaUSD:       3.45,
				AttemptCount:   1,
			},
		},
	}
	userRepo := &usageBillingCompUserRepoStub{}
	subRepo := &usageBillingCompSubRepoStub{}
	svc := NewUsageBillingCompensationService(repo, userRepo, subRepo, nil, &config.Config{})

	svc.processOnce()

	require.Equal(t, 1, subRepo.incrementCalls)
	require.Equal(t, 1, repo.markAppliedCalls)
	require.Equal(t, 0, repo.markRetryCalls)
}
