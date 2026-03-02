//go:build unit

package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// --- 并行刷新专用 stub ---

// concurrentTokenRefresherStub 记录并发度和调用次数
type concurrentTokenRefresherStub struct {
	canRefreshFn   func(*Account) bool
	needsRefreshFn func(*Account, time.Duration) bool
	refreshDelay   time.Duration
	refreshErr     error
	credentials    map[string]any
	refreshCalls   atomic.Int64
	maxConcurrent  atomic.Int64
	currentActive  atomic.Int64
}

func (r *concurrentTokenRefresherStub) CanRefresh(account *Account) bool {
	if r.canRefreshFn != nil {
		return r.canRefreshFn(account)
	}
	return true
}

func (r *concurrentTokenRefresherStub) NeedsRefresh(account *Account, window time.Duration) bool {
	if r.needsRefreshFn != nil {
		return r.needsRefreshFn(account, window)
	}
	return true
}

func (r *concurrentTokenRefresherStub) Refresh(ctx context.Context, account *Account) (map[string]any, error) {
	r.refreshCalls.Add(1)
	active := r.currentActive.Add(1)
	// 记录峰值并发
	for {
		old := r.maxConcurrent.Load()
		if active <= old || r.maxConcurrent.CompareAndSwap(old, active) {
			break
		}
	}
	if r.refreshDelay > 0 {
		time.Sleep(r.refreshDelay)
	}
	r.currentActive.Add(-1)
	if r.refreshErr != nil {
		return nil, r.refreshErr
	}
	// 每次返回新 map，避免多 goroutine 共享同一 map 实例引发竞态
	creds := make(map[string]any, len(r.credentials))
	for k, v := range r.credentials {
		creds[k] = v
	}
	return creds, nil
}

// concurrentTokenRefreshAccountRepo 线程安全的 account repo stub
type concurrentTokenRefreshAccountRepo struct {
	mockAccountRepoForGemini
	mu             sync.Mutex
	updateCalls    int
	setErrorCalls  int
	activeAccounts []Account
	updateErr      error
}

func (r *concurrentTokenRefreshAccountRepo) Update(ctx context.Context, account *Account) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updateCalls++
	return r.updateErr
}

func (r *concurrentTokenRefreshAccountRepo) SetError(ctx context.Context, id int64, errorMsg string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.setErrorCalls++
	return nil
}

func (r *concurrentTokenRefreshAccountRepo) ListActive(ctx context.Context) ([]Account, error) {
	out := make([]Account, len(r.activeAccounts))
	copy(out, r.activeAccounts)
	return out, nil
}

// --- 测试用例 ---

func TestProcessRefresh_ParallelExecution(t *testing.T) {
	accounts := make([]Account, 20)
	for i := range accounts {
		accounts[i] = Account{
			ID:       int64(100 + i),
			Platform: PlatformOpenAI,
			Type:     AccountTypeOAuth,
			Status:   StatusActive,
		}
	}

	repo := &concurrentTokenRefreshAccountRepo{activeAccounts: accounts}
	lockStub := &tokenRefreshSchedulerLockStub{}
	refresher := &concurrentTokenRefresherStub{
		refreshDelay: 20 * time.Millisecond,
		credentials:  map[string]any{"access_token": "tok"},
	}

	cfg := &config.Config{
		TokenRefresh: config.TokenRefreshConfig{
			MaxRetries:               1,
			RetryBackoffSeconds:      0,
			CheckIntervalMinutes:     5,
			RefreshBeforeExpiryHours: 1,
		},
	}
	svc := NewTokenRefreshService(repo, nil, nil, nil, nil, nil, lockStub, cfg)
	svc.refreshers = []TokenRefresher{refresher}

	start := time.Now()
	svc.processRefresh()
	elapsed := time.Since(start)

	// 20 个账号每个 20ms，串行至少 400ms；并行（maxConcurrency=10）约 40-60ms
	require.Equal(t, int64(20), refresher.refreshCalls.Load())
	require.Less(t, elapsed, 300*time.Millisecond, "并行刷新应显著快于串行")
	require.Greater(t, refresher.maxConcurrent.Load(), int64(1), "应有多个账号并发刷新")
	require.LessOrEqual(t, refresher.maxConcurrent.Load(), int64(10), "并发不应超过信号量限制")

	repo.mu.Lock()
	require.Equal(t, 20, repo.updateCalls)
	repo.mu.Unlock()
}

func TestProcessRefresh_SemaphoreLimitsConcurrency(t *testing.T) {
	accounts := make([]Account, 15)
	for i := range accounts {
		accounts[i] = Account{
			ID:       int64(200 + i),
			Platform: PlatformOpenAI,
			Type:     AccountTypeOAuth,
			Status:   StatusActive,
		}
	}

	repo := &concurrentTokenRefreshAccountRepo{activeAccounts: accounts}
	lockStub := &tokenRefreshSchedulerLockStub{}
	refresher := &concurrentTokenRefresherStub{
		refreshDelay: 50 * time.Millisecond,
		credentials:  map[string]any{"access_token": "tok"},
	}

	cfg := &config.Config{
		TokenRefresh: config.TokenRefreshConfig{
			MaxRetries:               1,
			RetryBackoffSeconds:      0,
			CheckIntervalMinutes:     5,
			RefreshBeforeExpiryHours: 1,
		},
	}
	svc := NewTokenRefreshService(repo, nil, nil, nil, nil, nil, lockStub, cfg)
	svc.refreshers = []TokenRefresher{refresher}

	svc.processRefresh()

	require.Equal(t, int64(15), refresher.refreshCalls.Load())
	require.LessOrEqual(t, refresher.maxConcurrent.Load(), int64(10), "并发不应超过 maxConcurrency=10")
}

func TestProcessRefresh_StopInterruptsPhase2(t *testing.T) {
	accounts := make([]Account, 30)
	for i := range accounts {
		accounts[i] = Account{
			ID:       int64(300 + i),
			Platform: PlatformOpenAI,
			Type:     AccountTypeOAuth,
			Status:   StatusActive,
		}
	}

	repo := &concurrentTokenRefreshAccountRepo{activeAccounts: accounts}
	lockStub := &tokenRefreshSchedulerLockStub{}
	refresher := &concurrentTokenRefresherStub{
		refreshDelay: 100 * time.Millisecond,
		credentials:  map[string]any{"access_token": "tok"},
	}

	cfg := &config.Config{
		TokenRefresh: config.TokenRefreshConfig{
			MaxRetries:               1,
			RetryBackoffSeconds:      0,
			CheckIntervalMinutes:     5,
			RefreshBeforeExpiryHours: 1,
		},
	}
	svc := NewTokenRefreshService(repo, nil, nil, nil, nil, nil, lockStub, cfg)
	svc.refreshers = []TokenRefresher{refresher}

	done := make(chan struct{})
	go func() {
		svc.processRefresh()
		close(done)
	}()

	// 短暂等待让部分 goroutine 启动
	time.Sleep(30 * time.Millisecond)
	svc.Stop()

	select {
	case <-done:
		// ok
	case <-time.After(3 * time.Second):
		t.Fatal("processRefresh 应在收到 stop 信号后及时退出")
	}

	// 因中断，不应刷新全部 30 个账号
	require.Less(t, refresher.refreshCalls.Load(), int64(30), "stop 应中断后续任务提交")
}

func TestProcessRefresh_EmptyAccounts(t *testing.T) {
	repo := &concurrentTokenRefreshAccountRepo{activeAccounts: nil}
	cfg := &config.Config{
		TokenRefresh: config.TokenRefreshConfig{
			MaxRetries:               1,
			CheckIntervalMinutes:     5,
			RefreshBeforeExpiryHours: 1,
		},
	}
	svc := NewTokenRefreshService(repo, nil, nil, nil, nil, nil, nil, cfg)
	refresher := &concurrentTokenRefresherStub{}
	svc.refreshers = []TokenRefresher{refresher}

	// 不应 panic
	require.NotPanics(t, func() {
		svc.processRefresh()
	})
	require.Equal(t, int64(0), refresher.refreshCalls.Load())
}

func TestProcessRefresh_NoAccountsNeedRefresh(t *testing.T) {
	accounts := []Account{
		{ID: 401, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive},
		{ID: 402, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive},
	}
	repo := &concurrentTokenRefreshAccountRepo{activeAccounts: accounts}
	lockStub := &tokenRefreshSchedulerLockStub{}
	refresher := &concurrentTokenRefresherStub{
		needsRefreshFn: func(a *Account, d time.Duration) bool { return false },
		credentials:    map[string]any{"access_token": "tok"},
	}

	cfg := &config.Config{
		TokenRefresh: config.TokenRefreshConfig{
			MaxRetries:               1,
			CheckIntervalMinutes:     5,
			RefreshBeforeExpiryHours: 1,
		},
	}
	svc := NewTokenRefreshService(repo, nil, nil, nil, nil, nil, lockStub, cfg)
	svc.refreshers = []TokenRefresher{refresher}

	svc.processRefresh()

	require.Equal(t, int64(0), refresher.refreshCalls.Load())
}

func TestProcessRefresh_MixedSuccessAndFailure(t *testing.T) {
	accounts := []Account{
		{ID: 501, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive},
		{ID: 502, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive},
		{ID: 503, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive},
		{ID: 504, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive},
	}
	repo := &concurrentTokenRefreshAccountRepo{activeAccounts: accounts}
	lockStub := &tokenRefreshSchedulerLockStub{}

	// 偶数 ID 成功，奇数 ID 失败
	refresher := &concurrentTokenRefresherStub{
		credentials: map[string]any{"access_token": "tok"},
	}

	failRefresher := &concurrentTokenRefresherStub{
		refreshErr: errors.New("refresh failed"),
	}

	// 使用 selectiveRefresher 按 ID 分流
	selectiveRefresher := &selectiveTokenRefresherStub{
		successRefresher: refresher,
		failRefresher:    failRefresher,
		failIDs:          map[int64]bool{501: true, 503: true},
	}

	cfg := &config.Config{
		TokenRefresh: config.TokenRefreshConfig{
			MaxRetries:               1,
			RetryBackoffSeconds:      0,
			CheckIntervalMinutes:     5,
			RefreshBeforeExpiryHours: 1,
		},
	}
	svc := NewTokenRefreshService(repo, nil, nil, nil, nil, nil, lockStub, cfg)
	svc.refreshers = []TokenRefresher{selectiveRefresher}

	svc.processRefresh()

	totalCalls := refresher.refreshCalls.Load() + failRefresher.refreshCalls.Load()
	require.Equal(t, int64(4), totalCalls)
	require.Equal(t, int64(2), refresher.refreshCalls.Load())
	require.Equal(t, int64(2), failRefresher.refreshCalls.Load())
}

// selectiveTokenRefresherStub 按账号 ID 分流到不同的 refresher
type selectiveTokenRefresherStub struct {
	successRefresher *concurrentTokenRefresherStub
	failRefresher    *concurrentTokenRefresherStub
	failIDs          map[int64]bool
}

func (r *selectiveTokenRefresherStub) CanRefresh(account *Account) bool {
	return true
}

func (r *selectiveTokenRefresherStub) NeedsRefresh(account *Account, window time.Duration) bool {
	return true
}

func (r *selectiveTokenRefresherStub) Refresh(ctx context.Context, account *Account) (map[string]any, error) {
	if r.failIDs[account.ID] {
		return r.failRefresher.Refresh(ctx, account)
	}
	return r.successRefresher.Refresh(ctx, account)
}

func TestProcessRefresh_SingleAccount(t *testing.T) {
	accounts := []Account{
		{ID: 601, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive},
	}
	repo := &concurrentTokenRefreshAccountRepo{activeAccounts: accounts}
	lockStub := &tokenRefreshSchedulerLockStub{}
	refresher := &concurrentTokenRefresherStub{
		credentials: map[string]any{"access_token": "tok"},
	}

	cfg := &config.Config{
		TokenRefresh: config.TokenRefreshConfig{
			MaxRetries:               1,
			RetryBackoffSeconds:      0,
			CheckIntervalMinutes:     5,
			RefreshBeforeExpiryHours: 1,
		},
	}
	svc := NewTokenRefreshService(repo, nil, nil, nil, nil, nil, lockStub, cfg)
	svc.refreshers = []TokenRefresher{refresher}

	svc.processRefresh()

	require.Equal(t, int64(1), refresher.refreshCalls.Load())
	require.Equal(t, int64(1), refresher.maxConcurrent.Load())
}

func TestProcessRefresh_AllFailed(t *testing.T) {
	accounts := make([]Account, 5)
	for i := range accounts {
		accounts[i] = Account{
			ID:       int64(700 + i),
			Platform: PlatformOpenAI,
			Type:     AccountTypeOAuth,
			Status:   StatusActive,
		}
	}
	repo := &concurrentTokenRefreshAccountRepo{activeAccounts: accounts}
	lockStub := &tokenRefreshSchedulerLockStub{}
	refresher := &concurrentTokenRefresherStub{
		refreshErr: errors.New("all fail"),
	}

	cfg := &config.Config{
		TokenRefresh: config.TokenRefreshConfig{
			MaxRetries:               1,
			RetryBackoffSeconds:      0,
			CheckIntervalMinutes:     5,
			RefreshBeforeExpiryHours: 1,
		},
	}
	svc := NewTokenRefreshService(repo, nil, nil, nil, nil, nil, lockStub, cfg)
	svc.refreshers = []TokenRefresher{refresher}

	// 不应 panic
	require.NotPanics(t, func() {
		svc.processRefresh()
	})
	require.Equal(t, int64(5), refresher.refreshCalls.Load())

	repo.mu.Lock()
	require.Equal(t, 5, repo.setErrorCalls)
	require.Equal(t, 0, repo.updateCalls)
	repo.mu.Unlock()
}

func TestProcessRefresh_CanRefreshFilters(t *testing.T) {
	accounts := []Account{
		{ID: 801, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive},
		{ID: 802, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive},
		{ID: 803, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive},
	}
	repo := &concurrentTokenRefreshAccountRepo{activeAccounts: accounts}
	lockStub := &tokenRefreshSchedulerLockStub{}
	refresher := &concurrentTokenRefresherStub{
		canRefreshFn: func(a *Account) bool { return a.Type == AccountTypeOAuth },
		credentials:  map[string]any{"access_token": "tok"},
	}

	cfg := &config.Config{
		TokenRefresh: config.TokenRefreshConfig{
			MaxRetries:               1,
			RetryBackoffSeconds:      0,
			CheckIntervalMinutes:     5,
			RefreshBeforeExpiryHours: 1,
		},
	}
	svc := NewTokenRefreshService(repo, nil, nil, nil, nil, nil, lockStub, cfg)
	svc.refreshers = []TokenRefresher{refresher}

	svc.processRefresh()

	// 只有 OAuth 账号（ID 801, 803）应被刷新
	require.Equal(t, int64(2), refresher.refreshCalls.Load())
}
