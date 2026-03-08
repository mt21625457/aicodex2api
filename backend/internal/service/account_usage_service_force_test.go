package service

import (
	"context"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/usagestats"
	"github.com/stretchr/testify/require"
)

type accountUsageForceRepoStub struct {
	AccountRepository
	account *Account
}

func (s *accountUsageForceRepoStub) GetByID(ctx context.Context, id int64) (*Account, error) {
	return s.account, nil
}

type claudeUsageForceFetcherStub struct {
	responses []*ClaudeUsageResponse
	calls     int
}

type accountUsageForceUsageLogRepoStub struct {
	UsageLogRepository
}

func (s *accountUsageForceUsageLogRepoStub) GetAccountWindowStats(ctx context.Context, accountID int64, startTime time.Time) (*usagestats.AccountStats, error) {
	return &usagestats.AccountStats{}, nil
}

func (s *claudeUsageForceFetcherStub) FetchUsage(ctx context.Context, accessToken, proxyURL string) (*ClaudeUsageResponse, error) {
	panic("unexpected FetchUsage call")
}

func (s *claudeUsageForceFetcherStub) FetchUsageWithOptions(ctx context.Context, opts *ClaudeUsageFetchOptions) (*ClaudeUsageResponse, error) {
	if len(s.responses) == 0 {
		return nil, nil
	}
	idx := s.calls
	if idx >= len(s.responses) {
		idx = len(s.responses) - 1
	}
	s.calls++
	return s.responses[idx], nil
}

func TestAccountUsageService_GetUsage_ForceRefreshBypassesCachedOAuthUsage(t *testing.T) {
	t.Parallel()

	resetAt := time.Now().Add(2 * time.Hour).Format(time.RFC3339)
	firstResp := &ClaudeUsageResponse{}
	firstResp.FiveHour.Utilization = 12
	firstResp.FiveHour.ResetsAt = resetAt
	firstResp.SevenDay.Utilization = 25
	firstResp.SevenDay.ResetsAt = resetAt

	secondResp := &ClaudeUsageResponse{}
	secondResp.FiveHour.Utilization = 47
	secondResp.FiveHour.ResetsAt = resetAt
	secondResp.SevenDay.Utilization = 63
	secondResp.SevenDay.ResetsAt = resetAt

	repo := &accountUsageForceRepoStub{
		account: &Account{
			ID:       42,
			Platform: PlatformAnthropic,
			Type:     AccountTypeOAuth,
			Credentials: map[string]any{
				"access_token": "test-token",
			},
		},
	}
	fetcher := &claudeUsageForceFetcherStub{responses: []*ClaudeUsageResponse{firstResp, secondResp}}
	svc := NewAccountUsageService(repo, &accountUsageForceUsageLogRepoStub{}, fetcher, nil, nil, NewUsageCache(), nil)

	usage1, err := svc.GetUsage(context.Background(), 42, false)
	require.NoError(t, err)
	require.NotNil(t, usage1)
	require.NotNil(t, usage1.FiveHour)
	require.Equal(t, 1, fetcher.calls)
	require.InDelta(t, 12, usage1.FiveHour.Utilization, 0.001)

	usage2, err := svc.GetUsage(context.Background(), 42, false)
	require.NoError(t, err)
	require.NotNil(t, usage2)
	require.NotNil(t, usage2.FiveHour)
	require.Equal(t, 1, fetcher.calls)
	require.InDelta(t, 12, usage2.FiveHour.Utilization, 0.001)

	usage3, err := svc.GetUsage(context.Background(), 42, true)
	require.NoError(t, err)
	require.NotNil(t, usage3)
	require.NotNil(t, usage3.FiveHour)
	require.Equal(t, 2, fetcher.calls)
	require.InDelta(t, 47, usage3.FiveHour.Utilization, 0.001)
}
