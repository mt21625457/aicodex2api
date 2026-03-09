package admin

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

type accountHandlerConcurrencyCacheStub struct{}

func (s *accountHandlerConcurrencyCacheStub) AcquireAccountSlot(ctx context.Context, accountID int64, maxConcurrency int, requestID string) (bool, error) {
	return false, nil
}

func (s *accountHandlerConcurrencyCacheStub) ReleaseAccountSlot(ctx context.Context, accountID int64, requestID string) error {
	return nil
}

func (s *accountHandlerConcurrencyCacheStub) GetAccountConcurrency(ctx context.Context, accountID int64) (int, error) {
	return 0, nil
}

func (s *accountHandlerConcurrencyCacheStub) GetAccountConcurrencyBatch(ctx context.Context, accountIDs []int64) (map[int64]int, error) {
	out := make(map[int64]int, len(accountIDs))
	for _, id := range accountIDs {
		out[id] = int(id) + 1
	}
	return out, nil
}

func (s *accountHandlerConcurrencyCacheStub) IncrementAccountWaitCount(ctx context.Context, accountID int64, maxWait int) (bool, error) {
	return false, nil
}

func (s *accountHandlerConcurrencyCacheStub) DecrementAccountWaitCount(ctx context.Context, accountID int64) error {
	return nil
}

func (s *accountHandlerConcurrencyCacheStub) GetAccountWaitingCount(ctx context.Context, accountID int64) (int, error) {
	return 0, nil
}

func (s *accountHandlerConcurrencyCacheStub) AcquireUserSlot(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error) {
	return false, nil
}

func (s *accountHandlerConcurrencyCacheStub) ReleaseUserSlot(ctx context.Context, userID int64, requestID string) error {
	return nil
}

func (s *accountHandlerConcurrencyCacheStub) GetUserConcurrency(ctx context.Context, userID int64) (int, error) {
	return 0, nil
}

func (s *accountHandlerConcurrencyCacheStub) IncrementWaitCount(ctx context.Context, userID int64, maxWait int) (bool, error) {
	return false, nil
}

func (s *accountHandlerConcurrencyCacheStub) DecrementWaitCount(ctx context.Context, userID int64) error {
	return nil
}

func (s *accountHandlerConcurrencyCacheStub) GetAccountsLoadBatch(ctx context.Context, accounts []service.AccountWithConcurrency) (map[int64]*service.AccountLoadInfo, error) {
	return map[int64]*service.AccountLoadInfo{}, nil
}

func (s *accountHandlerConcurrencyCacheStub) GetUsersLoadBatch(ctx context.Context, users []service.UserWithConcurrency) (map[int64]*service.UserLoadInfo, error) {
	return map[int64]*service.UserLoadInfo{}, nil
}

func (s *accountHandlerConcurrencyCacheStub) CleanupExpiredAccountSlots(ctx context.Context, accountID int64) error {
	return nil
}

type accountHandlerUsageLogRepoStub struct {
	service.UsageLogRepository
	stats *usagestats.AccountStats
	err   error
}

func (s *accountHandlerUsageLogRepoStub) GetAccountWindowStats(ctx context.Context, accountID int64, startTime time.Time) (*usagestats.AccountStats, error) {
	return s.stats, s.err
}

type accountHandlerSessionLimitCacheStub struct {
	service.SessionLimitCache
	activeSessions map[int64]int
	err            error
}

func (s *accountHandlerSessionLimitCacheStub) GetActiveSessionCountBatch(ctx context.Context, accountIDs []int64, idleTimeouts map[int64]time.Duration) (map[int64]int, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.activeSessions, nil
}

func TestBuildAccountResponseWithRuntime_UsesTierAwareWindowCost(t *testing.T) {
	usageSvc := service.NewAccountUsageService(
		nil,
		&accountHandlerUsageLogRepoStub{stats: &usagestats.AccountStats{Cost: 8.5, StandardCost: 3.2}},
		nil,
		nil,
		nil,
		nil,
		nil,
	)

	handler := &AccountHandler{
		accountUsageService: usageSvc,
		concurrencyService:  service.NewConcurrencyService(&accountHandlerConcurrencyCacheStub{}),
		sessionLimitCache: &accountHandlerSessionLimitCacheStub{
			activeSessions: map[int64]int{7: 4},
		},
	}

	account := &service.Account{
		ID:       7,
		Platform: service.PlatformAnthropic,
		Type:     service.AccountTypeOAuth,
		Extra: map[string]any{
			"window_cost_limit":            100.0,
			"max_sessions":                 10,
			"session_idle_timeout_minutes": 5,
		},
	}

	item := handler.buildAccountResponseWithRuntime(context.Background(), account)
	require.Equal(t, 8, item.CurrentConcurrency)
	require.NotNil(t, item.CurrentWindowCost)
	require.InDelta(t, 8.5, *item.CurrentWindowCost, 1e-10)
	require.NotNil(t, item.ActiveSessions)
	require.Equal(t, 4, *item.ActiveSessions)
}

func TestBuildAccountResponseWithRuntime_NilAccountReturnsZeroValue(t *testing.T) {
	handler := &AccountHandler{}
	item := handler.buildAccountResponseWithRuntime(context.Background(), nil)
	require.Nil(t, item.Account)
	require.Equal(t, 0, item.CurrentConcurrency)
	require.Nil(t, item.CurrentWindowCost)
	require.Nil(t, item.ActiveSessions)
}

func TestBuildAccountResponseWithRuntime_NonAnthropicSkipsRuntimeUsageFields(t *testing.T) {
	handler := &AccountHandler{
		accountUsageService: service.NewAccountUsageService(nil, &accountHandlerUsageLogRepoStub{}, nil, nil, nil, nil, nil),
		sessionLimitCache:   &accountHandlerSessionLimitCacheStub{activeSessions: map[int64]int{9: 3}},
	}

	account := &service.Account{
		ID:       9,
		Platform: service.PlatformOpenAI,
		Type:     service.AccountTypeAPIKey,
	}

	item := handler.buildAccountResponseWithRuntime(context.Background(), account)
	require.Nil(t, item.CurrentWindowCost)
	require.Nil(t, item.ActiveSessions)
}
