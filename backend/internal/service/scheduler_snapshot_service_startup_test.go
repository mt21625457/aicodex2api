package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type startupAccountRepoStub struct {
	accounts []Account
}

func (s *startupAccountRepoStub) Create(context.Context, *Account) error           { return nil }
func (s *startupAccountRepoStub) GetByID(context.Context, int64) (*Account, error) { return nil, nil }
func (s *startupAccountRepoStub) GetByIDs(context.Context, []int64) ([]*Account, error) {
	return nil, nil
}
func (s *startupAccountRepoStub) ExistsByID(context.Context, int64) (bool, error) { return false, nil }
func (s *startupAccountRepoStub) GetByCRSAccountID(context.Context, string) (*Account, error) {
	return nil, nil
}
func (s *startupAccountRepoStub) FindByExtraField(context.Context, string, any) ([]Account, error) {
	return nil, nil
}
func (s *startupAccountRepoStub) ListCRSAccountIDs(context.Context) (map[string]int64, error) {
	return nil, nil
}
func (s *startupAccountRepoStub) Update(context.Context, *Account) error { return nil }
func (s *startupAccountRepoStub) Delete(context.Context, int64) error    { return nil }
func (s *startupAccountRepoStub) List(context.Context, pagination.PaginationParams) ([]Account, *pagination.PaginationResult, error) {
	return nil, nil, nil
}
func (s *startupAccountRepoStub) ListWithFilters(context.Context, pagination.PaginationParams, string, string, string, string, int64) ([]Account, *pagination.PaginationResult, error) {
	return nil, nil, nil
}
func (s *startupAccountRepoStub) ListByGroup(context.Context, int64) ([]Account, error) {
	return nil, nil
}
func (s *startupAccountRepoStub) ListActive(context.Context) ([]Account, error) { return nil, nil }
func (s *startupAccountRepoStub) ListByPlatform(context.Context, string) ([]Account, error) {
	return nil, nil
}
func (s *startupAccountRepoStub) UpdateLastUsed(context.Context, int64) error { return nil }
func (s *startupAccountRepoStub) BatchUpdateLastUsed(context.Context, map[int64]time.Time) error {
	return nil
}
func (s *startupAccountRepoStub) SetError(context.Context, int64, string) error     { return nil }
func (s *startupAccountRepoStub) ClearError(context.Context, int64) error           { return nil }
func (s *startupAccountRepoStub) SetSchedulable(context.Context, int64, bool) error { return nil }
func (s *startupAccountRepoStub) AutoPauseExpiredAccounts(context.Context, time.Time) (int64, error) {
	return 0, nil
}
func (s *startupAccountRepoStub) BindGroups(context.Context, int64, []int64) error   { return nil }
func (s *startupAccountRepoStub) ListSchedulable(context.Context) ([]Account, error) { return nil, nil }
func (s *startupAccountRepoStub) ListSchedulableByGroupID(context.Context, int64) ([]Account, error) {
	return nil, nil
}
func (s *startupAccountRepoStub) ListSchedulableByPlatform(_ context.Context, platform string) ([]Account, error) {
	var out []Account
	for _, account := range s.accounts {
		if account.Platform == platform && account.IsSchedulable() {
			out = append(out, account)
		}
	}
	return out, nil
}
func (s *startupAccountRepoStub) ListSchedulableByGroupIDAndPlatform(ctx context.Context, groupID int64, platform string) ([]Account, error) {
	return s.ListSchedulableByPlatform(ctx, platform)
}
func (s *startupAccountRepoStub) ListSchedulableByPlatforms(_ context.Context, platforms []string) ([]Account, error) {
	platformSet := make(map[string]struct{}, len(platforms))
	for _, platform := range platforms {
		platformSet[platform] = struct{}{}
	}
	var out []Account
	for _, account := range s.accounts {
		if _, ok := platformSet[account.Platform]; ok && account.IsSchedulable() {
			out = append(out, account)
		}
	}
	return out, nil
}
func (s *startupAccountRepoStub) ListSchedulableByGroupIDAndPlatforms(ctx context.Context, groupID int64, platforms []string) ([]Account, error) {
	return s.ListSchedulableByPlatforms(ctx, platforms)
}
func (s *startupAccountRepoStub) ListSchedulableUngroupedByPlatform(ctx context.Context, platform string) ([]Account, error) {
	return s.ListSchedulableByPlatform(ctx, platform)
}
func (s *startupAccountRepoStub) ListSchedulableUngroupedByPlatforms(ctx context.Context, platforms []string) ([]Account, error) {
	return s.ListSchedulableByPlatforms(ctx, platforms)
}
func (s *startupAccountRepoStub) SetRateLimited(context.Context, int64, time.Time) error { return nil }
func (s *startupAccountRepoStub) SetModelRateLimit(context.Context, int64, string, time.Time) error {
	return nil
}
func (s *startupAccountRepoStub) SetOverloaded(context.Context, int64, time.Time) error { return nil }
func (s *startupAccountRepoStub) SetTempUnschedulable(context.Context, int64, time.Time, string) error {
	return nil
}
func (s *startupAccountRepoStub) ClearTempUnschedulable(context.Context, int64) error { return nil }
func (s *startupAccountRepoStub) ClearRateLimit(context.Context, int64) error         { return nil }
func (s *startupAccountRepoStub) ClearAntigravityQuotaScopes(context.Context, int64) error {
	return nil
}
func (s *startupAccountRepoStub) ClearModelRateLimits(context.Context, int64) error { return nil }
func (s *startupAccountRepoStub) UpdateSessionWindow(context.Context, int64, *time.Time, *time.Time, string) error {
	return nil
}
func (s *startupAccountRepoStub) UpdateExtra(context.Context, int64, map[string]any) error {
	return nil
}
func (s *startupAccountRepoStub) IncrementQuotaUsed(context.Context, int64, float64) error {
	return nil
}
func (s *startupAccountRepoStub) ResetQuotaUsed(context.Context, int64) error { return nil }
func (s *startupAccountRepoStub) BulkUpdate(context.Context, []int64, AccountBulkUpdate) (int64, error) {
	return 0, nil
}

type startupSchedulerCacheStub struct {
	snapshot         []*Account
	hit              bool
	getSnapshotCalls int
	setSnapshotCalls int
	lastSnapshotIDs  []int64
}

func (s *startupSchedulerCacheStub) GetSnapshot(_ context.Context, _ SchedulerBucket) ([]*Account, bool, error) {
	s.getSnapshotCalls++
	return s.snapshot, s.hit, nil
}

func (s *startupSchedulerCacheStub) SetSnapshot(_ context.Context, bucket SchedulerBucket, accounts []Account) error {
	s.setSnapshotCalls++
	s.lastSnapshotIDs = s.lastSnapshotIDs[:0]
	for _, account := range accounts {
		s.lastSnapshotIDs = append(s.lastSnapshotIDs, account.ID)
	}
	return nil
}

func (s *startupSchedulerCacheStub) GetAccount(_ context.Context, _ int64) (*Account, error) {
	return nil, nil
}

func (s *startupSchedulerCacheStub) SetAccount(_ context.Context, _ *Account) error {
	return nil
}

func (s *startupSchedulerCacheStub) DeleteAccount(_ context.Context, _ int64) error {
	return nil
}

func (s *startupSchedulerCacheStub) UpdateLastUsed(_ context.Context, _ map[int64]time.Time) error {
	return nil
}

func (s *startupSchedulerCacheStub) TryLockBucket(_ context.Context, _ SchedulerBucket, _ time.Duration) (bool, error) {
	return true, nil
}

func (s *startupSchedulerCacheStub) ListBuckets(_ context.Context) ([]SchedulerBucket, error) {
	return nil, nil
}

func (s *startupSchedulerCacheStub) GetOutboxWatermark(_ context.Context) (int64, error) {
	return 0, nil
}

func (s *startupSchedulerCacheStub) SetOutboxWatermark(_ context.Context, _ int64) error {
	return nil
}

func TestSchedulerSnapshotServiceListSchedulableAccountsBypassesCacheDuringStartupWarmup(t *testing.T) {
	ctx := context.Background()
	groupID := int64(42)
	fresh := Account{ID: 2, Platform: PlatformAnthropic, Status: StatusActive, Schedulable: true}
	stale := &Account{ID: 1, Platform: PlatformAnthropic, Status: StatusError, Schedulable: true}

	repo := &startupAccountRepoStub{accounts: []Account{fresh}}
	cache := &startupSchedulerCacheStub{snapshot: []*Account{stale}, hit: true}
	svc := NewSchedulerSnapshotService(cache, nil, repo, nil, nil)
	svc.startupWarm.Store(false)

	accounts, useMixed, err := svc.ListSchedulableAccounts(ctx, &groupID, PlatformAnthropic, false)
	require.NoError(t, err)
	require.True(t, useMixed)
	require.Len(t, accounts, 1)
	require.Equal(t, fresh.ID, accounts[0].ID)
	require.Zero(t, cache.getSnapshotCalls)
	require.Equal(t, 1, cache.setSnapshotCalls)
	require.Equal(t, []int64{fresh.ID}, cache.lastSnapshotIDs)
}

func TestSchedulerSnapshotServiceListSchedulableAccountsUsesCacheAfterStartupWarmup(t *testing.T) {
	ctx := context.Background()
	groupID := int64(42)
	fresh := Account{ID: 2, Platform: PlatformAnthropic, Status: StatusActive, Schedulable: true}
	stale := &Account{ID: 1, Platform: PlatformAnthropic, Status: StatusActive, Schedulable: true}

	repo := &startupAccountRepoStub{accounts: []Account{fresh}}
	cache := &startupSchedulerCacheStub{snapshot: []*Account{stale}, hit: true}
	svc := NewSchedulerSnapshotService(cache, nil, repo, nil, nil)
	svc.startupWarm.Store(true)

	accounts, useMixed, err := svc.ListSchedulableAccounts(ctx, &groupID, PlatformAnthropic, false)
	require.NoError(t, err)
	require.True(t, useMixed)
	require.Len(t, accounts, 1)
	require.Equal(t, stale.ID, accounts[0].ID)
	require.Equal(t, 1, cache.getSnapshotCalls)
	require.Zero(t, cache.setSnapshotCalls)
}
