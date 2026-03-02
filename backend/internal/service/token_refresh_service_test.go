//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type tokenRefreshAccountRepo struct {
	mockAccountRepoForGemini
	updateCalls    int
	setErrorCalls  int
	lastAccount    *Account
	updateErr      error
	activeAccounts []Account
}

func (r *tokenRefreshAccountRepo) Update(ctx context.Context, account *Account) error {
	r.updateCalls++
	r.lastAccount = account
	return r.updateErr
}

func (r *tokenRefreshAccountRepo) SetError(ctx context.Context, id int64, errorMsg string) error {
	r.setErrorCalls++
	return nil
}

func (r *tokenRefreshAccountRepo) ListActive(ctx context.Context) ([]Account, error) {
	if len(r.activeAccounts) == 0 {
		return nil, nil
	}
	out := make([]Account, 0, len(r.activeAccounts))
	out = append(out, r.activeAccounts...)
	return out, nil
}

type tokenCacheInvalidatorStub struct {
	calls int
	err   error
}

func (s *tokenCacheInvalidatorStub) InvalidateToken(ctx context.Context, account *Account) error {
	s.calls++
	return s.err
}

type tokenRefresherStub struct {
	credentials  map[string]any
	err          error
	refreshCalls int
}

func (r *tokenRefresherStub) CanRefresh(account *Account) bool {
	return true
}

func (r *tokenRefresherStub) NeedsRefresh(account *Account, refreshWindowDuration time.Duration) bool {
	return true
}

func (r *tokenRefresherStub) Refresh(ctx context.Context, account *Account) (map[string]any, error) {
	r.refreshCalls++
	if r.err != nil {
		return nil, r.err
	}
	return r.credentials, nil
}

type tokenRefreshSchedulerLockStub struct {
	SchedulerCache
	lockByAccount map[int64]bool
	err           error
	calls         []SchedulerBucket
}

func (s *tokenRefreshSchedulerLockStub) TryLockBucket(ctx context.Context, bucket SchedulerBucket, ttl time.Duration) (bool, error) {
	_ = ctx
	_ = ttl
	s.calls = append(s.calls, bucket)
	if s.err != nil {
		return false, s.err
	}
	if s.lockByAccount != nil {
		if ok, exists := s.lockByAccount[bucket.GroupID]; exists {
			return ok, nil
		}
	}
	return true, nil
}

func (s *tokenRefreshSchedulerLockStub) SetAccount(ctx context.Context, account *Account) error {
	_ = ctx
	_ = account
	return nil
}

func TestTokenRefreshService_RefreshWithRetry_InvalidatesCache(t *testing.T) {
	repo := &tokenRefreshAccountRepo{}
	invalidator := &tokenCacheInvalidatorStub{}
	cfg := &config.Config{
		TokenRefresh: config.TokenRefreshConfig{
			MaxRetries:          1,
			RetryBackoffSeconds: 0,
		},
	}
	service := NewTokenRefreshService(repo, nil, nil, nil, nil, invalidator, nil, cfg)
	account := &Account{
		ID:       5,
		Platform: PlatformGemini,
		Type:     AccountTypeOAuth,
	}
	refresher := &tokenRefresherStub{
		credentials: map[string]any{
			"access_token": "new-token",
		},
	}

	err := service.refreshWithRetry(context.Background(), account, refresher)
	require.NoError(t, err)
	require.Equal(t, 1, repo.updateCalls)
	require.Equal(t, 1, invalidator.calls)
	require.Equal(t, "new-token", account.GetCredential("access_token"))
}

func TestTokenRefreshService_ProcessRefresh_SkipsWhenDistributedLockHeld(t *testing.T) {
	repo := &tokenRefreshAccountRepo{
		activeAccounts: []Account{
			{ID: 31, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true},
		},
	}
	lockStub := &tokenRefreshSchedulerLockStub{
		lockByAccount: map[int64]bool{31: false},
	}
	cfg := &config.Config{
		TokenRefresh: config.TokenRefreshConfig{
			MaxRetries:               1,
			RetryBackoffSeconds:      0,
			CheckIntervalMinutes:     2,
			RefreshBeforeExpiryHours: 1,
			SyncLinkedSoraAccounts:   false,
		},
	}
	service := NewTokenRefreshService(repo, nil, nil, nil, nil, nil, lockStub, cfg)
	refresher := &tokenRefresherStub{credentials: map[string]any{"access_token": "new-token"}}
	service.refreshers = []TokenRefresher{refresher}

	service.processRefresh()

	require.Len(t, lockStub.calls, 1)
	require.Equal(t, int64(31), lockStub.calls[0].GroupID)
	require.Equal(t, tokenRefreshDistributedLockPlatform, lockStub.calls[0].Platform)
	require.Equal(t, PlatformOpenAI, lockStub.calls[0].Mode)
	require.Equal(t, 0, refresher.refreshCalls, "lock held by another instance should skip refresh")
	require.Equal(t, 0, repo.updateCalls)
}

func TestTokenRefreshService_ProcessRefresh_RefreshesWhenDistributedLockAcquired(t *testing.T) {
	repo := &tokenRefreshAccountRepo{
		activeAccounts: []Account{
			{ID: 32, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true},
		},
	}
	lockStub := &tokenRefreshSchedulerLockStub{
		lockByAccount: map[int64]bool{32: true},
	}
	cfg := &config.Config{
		TokenRefresh: config.TokenRefreshConfig{
			MaxRetries:               1,
			RetryBackoffSeconds:      0,
			CheckIntervalMinutes:     2,
			RefreshBeforeExpiryHours: 1,
			SyncLinkedSoraAccounts:   false,
		},
	}
	service := NewTokenRefreshService(repo, nil, nil, nil, nil, nil, lockStub, cfg)
	refresher := &tokenRefresherStub{credentials: map[string]any{"access_token": "new-token"}}
	service.refreshers = []TokenRefresher{refresher}

	service.processRefresh()

	require.Len(t, lockStub.calls, 1)
	require.Equal(t, 1, refresher.refreshCalls)
	require.Equal(t, 1, repo.updateCalls, "lock acquired should allow refresh")
}

func TestTokenRefreshService_ProcessRefresh_FailOpenWhenDistributedLockError(t *testing.T) {
	repo := &tokenRefreshAccountRepo{
		activeAccounts: []Account{
			{ID: 33, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true},
		},
	}
	lockStub := &tokenRefreshSchedulerLockStub{
		err: errors.New("redis unavailable"),
	}
	cfg := &config.Config{
		TokenRefresh: config.TokenRefreshConfig{
			MaxRetries:               1,
			RetryBackoffSeconds:      0,
			CheckIntervalMinutes:     2,
			RefreshBeforeExpiryHours: 1,
			SyncLinkedSoraAccounts:   false,
		},
	}
	service := NewTokenRefreshService(repo, nil, nil, nil, nil, nil, lockStub, cfg)
	refresher := &tokenRefresherStub{credentials: map[string]any{"access_token": "new-token"}}
	service.refreshers = []TokenRefresher{refresher}

	service.processRefresh()

	require.Len(t, lockStub.calls, 1)
	require.Equal(t, 1, refresher.refreshCalls, "lock backend error should fail-open and continue refresh")
	require.Equal(t, 1, repo.updateCalls)
}

func TestTokenRefreshService_RefreshWithRetry_InvalidatorErrorIgnored(t *testing.T) {
	repo := &tokenRefreshAccountRepo{}
	invalidator := &tokenCacheInvalidatorStub{err: errors.New("invalidate failed")}
	cfg := &config.Config{
		TokenRefresh: config.TokenRefreshConfig{
			MaxRetries:          1,
			RetryBackoffSeconds: 0,
		},
	}
	service := NewTokenRefreshService(repo, nil, nil, nil, nil, invalidator, nil, cfg)
	account := &Account{
		ID:       6,
		Platform: PlatformGemini,
		Type:     AccountTypeOAuth,
	}
	refresher := &tokenRefresherStub{
		credentials: map[string]any{
			"access_token": "token",
		},
	}

	err := service.refreshWithRetry(context.Background(), account, refresher)
	require.NoError(t, err)
	require.Equal(t, 1, repo.updateCalls)
	require.Equal(t, 1, invalidator.calls)
}

func TestTokenRefreshService_RefreshWithRetry_NilInvalidator(t *testing.T) {
	repo := &tokenRefreshAccountRepo{}
	cfg := &config.Config{
		TokenRefresh: config.TokenRefreshConfig{
			MaxRetries:          1,
			RetryBackoffSeconds: 0,
		},
	}
	service := NewTokenRefreshService(repo, nil, nil, nil, nil, nil, nil, cfg)
	account := &Account{
		ID:       7,
		Platform: PlatformGemini,
		Type:     AccountTypeOAuth,
	}
	refresher := &tokenRefresherStub{
		credentials: map[string]any{
			"access_token": "token",
		},
	}

	err := service.refreshWithRetry(context.Background(), account, refresher)
	require.NoError(t, err)
	require.Equal(t, 1, repo.updateCalls)
}

// TestTokenRefreshService_RefreshWithRetry_Antigravity 测试 Antigravity 平台的缓存失效
func TestTokenRefreshService_RefreshWithRetry_Antigravity(t *testing.T) {
	repo := &tokenRefreshAccountRepo{}
	invalidator := &tokenCacheInvalidatorStub{}
	cfg := &config.Config{
		TokenRefresh: config.TokenRefreshConfig{
			MaxRetries:          1,
			RetryBackoffSeconds: 0,
		},
	}
	service := NewTokenRefreshService(repo, nil, nil, nil, nil, invalidator, nil, cfg)
	account := &Account{
		ID:       8,
		Platform: PlatformAntigravity,
		Type:     AccountTypeOAuth,
	}
	refresher := &tokenRefresherStub{
		credentials: map[string]any{
			"access_token": "ag-token",
		},
	}

	err := service.refreshWithRetry(context.Background(), account, refresher)
	require.NoError(t, err)
	require.Equal(t, 1, repo.updateCalls)
	require.Equal(t, 1, invalidator.calls) // Antigravity 也应触发缓存失效
}

// TestTokenRefreshService_RefreshWithRetry_NonOAuthAccount 测试非 OAuth 账号不触发缓存失效
func TestTokenRefreshService_RefreshWithRetry_NonOAuthAccount(t *testing.T) {
	repo := &tokenRefreshAccountRepo{}
	invalidator := &tokenCacheInvalidatorStub{}
	cfg := &config.Config{
		TokenRefresh: config.TokenRefreshConfig{
			MaxRetries:          1,
			RetryBackoffSeconds: 0,
		},
	}
	service := NewTokenRefreshService(repo, nil, nil, nil, nil, invalidator, nil, cfg)
	account := &Account{
		ID:       9,
		Platform: PlatformGemini,
		Type:     AccountTypeAPIKey, // 非 OAuth
	}
	refresher := &tokenRefresherStub{
		credentials: map[string]any{
			"access_token": "token",
		},
	}

	err := service.refreshWithRetry(context.Background(), account, refresher)
	require.NoError(t, err)
	require.Equal(t, 1, repo.updateCalls)
	require.Equal(t, 0, invalidator.calls) // 非 OAuth 不触发缓存失效
}

// TestTokenRefreshService_RefreshWithRetry_OtherPlatformOAuth 测试所有 OAuth 平台都触发缓存失效
func TestTokenRefreshService_RefreshWithRetry_OtherPlatformOAuth(t *testing.T) {
	repo := &tokenRefreshAccountRepo{}
	invalidator := &tokenCacheInvalidatorStub{}
	cfg := &config.Config{
		TokenRefresh: config.TokenRefreshConfig{
			MaxRetries:          1,
			RetryBackoffSeconds: 0,
		},
	}
	service := NewTokenRefreshService(repo, nil, nil, nil, nil, invalidator, nil, cfg)
	account := &Account{
		ID:       10,
		Platform: PlatformOpenAI, // OpenAI OAuth 账户
		Type:     AccountTypeOAuth,
	}
	refresher := &tokenRefresherStub{
		credentials: map[string]any{
			"access_token": "token",
		},
	}

	err := service.refreshWithRetry(context.Background(), account, refresher)
	require.NoError(t, err)
	require.Equal(t, 1, repo.updateCalls)
	require.Equal(t, 1, invalidator.calls) // 所有 OAuth 账户刷新后触发缓存失效
}

// TestTokenRefreshService_RefreshWithRetry_UpdateFailed 测试更新失败的情况
func TestTokenRefreshService_RefreshWithRetry_UpdateFailed(t *testing.T) {
	repo := &tokenRefreshAccountRepo{updateErr: errors.New("update failed")}
	invalidator := &tokenCacheInvalidatorStub{}
	cfg := &config.Config{
		TokenRefresh: config.TokenRefreshConfig{
			MaxRetries:          1,
			RetryBackoffSeconds: 0,
		},
	}
	service := NewTokenRefreshService(repo, nil, nil, nil, nil, invalidator, nil, cfg)
	account := &Account{
		ID:       11,
		Platform: PlatformGemini,
		Type:     AccountTypeOAuth,
	}
	refresher := &tokenRefresherStub{
		credentials: map[string]any{
			"access_token": "token",
		},
	}

	err := service.refreshWithRetry(context.Background(), account, refresher)
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to save credentials")
	require.Equal(t, 1, repo.updateCalls)
	require.Equal(t, 0, invalidator.calls) // 更新失败时不应触发缓存失效
}

// TestTokenRefreshService_RefreshWithRetry_RefreshFailed 测试刷新失败的情况
func TestTokenRefreshService_RefreshWithRetry_RefreshFailed(t *testing.T) {
	repo := &tokenRefreshAccountRepo{}
	invalidator := &tokenCacheInvalidatorStub{}
	cfg := &config.Config{
		TokenRefresh: config.TokenRefreshConfig{
			MaxRetries:          2,
			RetryBackoffSeconds: 0,
		},
	}
	service := NewTokenRefreshService(repo, nil, nil, nil, nil, invalidator, nil, cfg)
	account := &Account{
		ID:       12,
		Platform: PlatformGemini,
		Type:     AccountTypeOAuth,
	}
	refresher := &tokenRefresherStub{
		err: errors.New("refresh failed"),
	}

	err := service.refreshWithRetry(context.Background(), account, refresher)
	require.Error(t, err)
	require.Equal(t, 0, repo.updateCalls)   // 刷新失败不应更新
	require.Equal(t, 0, invalidator.calls)  // 刷新失败不应触发缓存失效
	require.Equal(t, 1, repo.setErrorCalls) // 应设置错误状态
}

// TestTokenRefreshService_RefreshWithRetry_AntigravityRefreshFailed 测试 Antigravity 刷新失败不设置错误状态
func TestTokenRefreshService_RefreshWithRetry_AntigravityRefreshFailed(t *testing.T) {
	repo := &tokenRefreshAccountRepo{}
	invalidator := &tokenCacheInvalidatorStub{}
	cfg := &config.Config{
		TokenRefresh: config.TokenRefreshConfig{
			MaxRetries:          1,
			RetryBackoffSeconds: 0,
		},
	}
	service := NewTokenRefreshService(repo, nil, nil, nil, nil, invalidator, nil, cfg)
	account := &Account{
		ID:       13,
		Platform: PlatformAntigravity,
		Type:     AccountTypeOAuth,
	}
	refresher := &tokenRefresherStub{
		err: errors.New("network error"), // 可重试错误
	}

	err := service.refreshWithRetry(context.Background(), account, refresher)
	require.Error(t, err)
	require.Equal(t, 0, repo.updateCalls)
	require.Equal(t, 0, invalidator.calls)
	require.Equal(t, 0, repo.setErrorCalls) // Antigravity 可重试错误不设置错误状态
}

// TestTokenRefreshService_RefreshWithRetry_AntigravityNonRetryableError 测试 Antigravity 不可重试错误
func TestTokenRefreshService_RefreshWithRetry_AntigravityNonRetryableError(t *testing.T) {
	repo := &tokenRefreshAccountRepo{}
	invalidator := &tokenCacheInvalidatorStub{}
	cfg := &config.Config{
		TokenRefresh: config.TokenRefreshConfig{
			MaxRetries:          3,
			RetryBackoffSeconds: 0,
		},
	}
	service := NewTokenRefreshService(repo, nil, nil, nil, nil, invalidator, nil, cfg)
	account := &Account{
		ID:       14,
		Platform: PlatformAntigravity,
		Type:     AccountTypeOAuth,
	}
	refresher := &tokenRefresherStub{
		err: errors.New("invalid_grant: token revoked"), // 不可重试错误
	}

	err := service.refreshWithRetry(context.Background(), account, refresher)
	require.Error(t, err)
	require.Equal(t, 0, repo.updateCalls)
	require.Equal(t, 0, invalidator.calls)
	require.Equal(t, 1, repo.setErrorCalls) // 不可重试错误应设置错误状态
}

// TestIsNonRetryableRefreshError 测试不可重试错误判断
func TestIsNonRetryableRefreshError(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected bool
	}{
		{name: "nil_error", err: nil, expected: false},
		{name: "network_error", err: errors.New("network timeout"), expected: false},
		{name: "invalid_grant", err: errors.New("invalid_grant"), expected: true},
		{name: "invalid_client", err: errors.New("invalid_client"), expected: true},
		{name: "unauthorized_client", err: errors.New("unauthorized_client"), expected: true},
		{name: "access_denied", err: errors.New("access_denied"), expected: true},
		{name: "invalid_grant_with_desc", err: errors.New("Error: invalid_grant - token revoked"), expected: true},
		{name: "case_insensitive", err: errors.New("INVALID_GRANT"), expected: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isNonRetryableRefreshError(tt.err)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestTokenRefreshService_Stop_Idempotent(t *testing.T) {
	repo := &tokenRefreshAccountRepo{}
	cfg := &config.Config{
		TokenRefresh: config.TokenRefreshConfig{
			MaxRetries:          1,
			RetryBackoffSeconds: 0,
		},
	}
	service := NewTokenRefreshService(repo, nil, nil, nil, nil, nil, nil, cfg)

	require.NotPanics(t, func() {
		service.Stop()
		service.Stop()
	})
}

func TestTokenRefreshService_RefreshWithRetry_StopInterruptsBackoff(t *testing.T) {
	repo := &tokenRefreshAccountRepo{}
	cfg := &config.Config{
		TokenRefresh: config.TokenRefreshConfig{
			MaxRetries:          3,
			RetryBackoffSeconds: 5,
		},
	}
	service := NewTokenRefreshService(repo, nil, nil, nil, nil, nil, nil, cfg)
	account := &Account{
		ID:       21,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
	}
	refresher := &tokenRefresherStub{
		err: errors.New("refresh failed"),
	}

	start := time.Now()
	done := make(chan error, 1)
	go func() {
		done <- service.refreshWithRetry(context.Background(), account, refresher)
	}()

	time.Sleep(80 * time.Millisecond)
	service.Stop()

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(600 * time.Millisecond):
		t.Fatal("refreshWithRetry should exit quickly after service stop")
	}
	require.Less(t, time.Since(start), time.Second)
	require.Equal(t, 0, repo.setErrorCalls, "stop 中断时不应落错误状态")
}
