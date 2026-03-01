package service

import (
	"context"
	"errors"
	"time"
)

var ErrUsageBillingEntryNotFound = errors.New("usage billing entry not found")

type UsageBillingEntryStatus int16

const (
	UsageBillingEntryStatusPending    UsageBillingEntryStatus = 0
	UsageBillingEntryStatusProcessing UsageBillingEntryStatus = 1
	UsageBillingEntryStatusApplied    UsageBillingEntryStatus = 2
)

type UsageBillingEntry struct {
	ID             int64
	UsageLogID     int64
	UserID         int64
	APIKeyID       int64
	SubscriptionID *int64
	BillingType    int8
	Applied        bool
	DeltaUSD       float64
	Status         UsageBillingEntryStatus
	AttemptCount   int
	NextRetryAt    time.Time
	UpdatedAt      time.Time
	CreatedAt      time.Time
	LastError      *string
}

type UsageBillingEntryStore interface {
	GetUsageBillingEntryByUsageLogID(ctx context.Context, usageLogID int64) (*UsageBillingEntry, error)
	UpsertUsageBillingEntry(ctx context.Context, entry *UsageBillingEntry) (*UsageBillingEntry, bool, error)
	MarkUsageBillingEntryApplied(ctx context.Context, entryID int64) error
	MarkUsageBillingEntryRetry(ctx context.Context, entryID int64, nextRetryAt time.Time, lastError string) error
	ClaimUsageBillingEntries(ctx context.Context, limit int, processingStaleAfter time.Duration) ([]UsageBillingEntry, error)
}

type UsageBillingTxRunner interface {
	WithUsageBillingTx(ctx context.Context, fn func(txCtx context.Context) error) error
}

func usageBillingRetryBackoff(attempt int) time.Duration {
	if attempt <= 1 {
		return 30 * time.Second
	}
	backoff := 30 * time.Second
	for i := 1; i < attempt && backoff < 30*time.Minute; i++ {
		backoff *= 2
	}
	if backoff > 30*time.Minute {
		return 30 * time.Minute
	}
	return backoff
}
