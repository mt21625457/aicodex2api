package service

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAIWSConnPool_CleanupStaleAndTrimIdle(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	pool := newOpenAIWSConnPool(cfg)

	accountID := int64(10)
	ap := pool.ensureAccountPoolLocked(accountID)

	stale := newOpenAIWSConn("stale", accountID, nil, nil)
	stale.createdAtNano.Store(time.Now().Add(-2 * time.Hour).UnixNano())
	stale.lastUsedNano.Store(time.Now().Add(-2 * time.Hour).UnixNano())

	idleOld := newOpenAIWSConn("idle_old", accountID, nil, nil)
	idleOld.lastUsedNano.Store(time.Now().Add(-10 * time.Minute).UnixNano())

	idleNew := newOpenAIWSConn("idle_new", accountID, nil, nil)
	idleNew.lastUsedNano.Store(time.Now().Add(-1 * time.Minute).UnixNano())

	ap.conns[stale.id] = stale
	ap.conns[idleOld.id] = idleOld
	ap.conns[idleNew.id] = idleNew

	evicted := pool.cleanupAccountLocked(ap, time.Now(), pool.maxConnsHardCap())
	closeOpenAIWSConns(evicted)

	require.Nil(t, ap.conns["stale"], "stale connection should be rotated")
	require.Nil(t, ap.conns["idle_old"], "old idle should be trimmed by max_idle")
	require.NotNil(t, ap.conns["idle_new"], "newer idle should be kept")
}

func TestOpenAIWSConnPool_TargetConnCountAdaptive(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 6
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.PoolTargetUtilization = 0.5

	pool := newOpenAIWSConnPool(cfg)
	ap := pool.ensureAccountPoolLocked(88)

	conn1 := newOpenAIWSConn("c1", 88, nil, nil)
	conn2 := newOpenAIWSConn("c2", 88, nil, nil)
	require.True(t, conn1.tryAcquire())
	require.True(t, conn2.tryAcquire())
	conn1.waiters.Store(1)
	conn2.waiters.Store(1)

	ap.conns[conn1.id] = conn1
	ap.conns[conn2.id] = conn2

	target := pool.targetConnCountLocked(ap, pool.maxConnsHardCap())
	require.Equal(t, 6, target, "应按 inflight+waiters 与 target_utilization 自适应扩容到上限")

	conn1.release()
	conn2.release()
	conn1.waiters.Store(0)
	conn2.waiters.Store(0)
	target = pool.targetConnCountLocked(ap, pool.maxConnsHardCap())
	require.Equal(t, 1, target, "低负载时应缩回到最小空闲连接")
}

func TestOpenAIWSConnPool_TargetConnCountMinIdleZero(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 4
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.PoolTargetUtilization = 0.8

	pool := newOpenAIWSConnPool(cfg)
	ap := pool.ensureAccountPoolLocked(66)

	target := pool.targetConnCountLocked(ap, pool.maxConnsHardCap())
	require.Equal(t, 0, target, "min_idle=0 且无负载时应允许缩容到 0")
}

func TestOpenAIWSConnPool_EnsureTargetIdleAsync(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 4
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 2
	cfg.Gateway.OpenAIWS.PoolTargetUtilization = 0.8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 1

	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(&openAIWSFakeDialer{})

	accountID := int64(77)
	account := &Account{ID: accountID, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	pool.mu.Lock()
	ap := pool.ensureAccountPoolLocked(accountID)
	ap.lastAcquire = &openAIWSAcquireRequest{
		Account: account,
		WSURL:   "wss://example.com/v1/responses",
	}
	pool.mu.Unlock()

	pool.ensureTargetIdleAsync(accountID)

	require.Eventually(t, func() bool {
		pool.mu.Lock()
		defer pool.mu.Unlock()
		return len(pool.accounts[accountID].conns) >= 2
	}, 2*time.Second, 20*time.Millisecond)

	metrics := pool.SnapshotMetrics()
	require.GreaterOrEqual(t, metrics.ScaleUpTotal, int64(2))
}

func TestOpenAIWSConnPool_AcquireQueueWaitMetrics(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 4

	pool := newOpenAIWSConnPool(cfg)
	accountID := int64(99)
	account := &Account{ID: accountID, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	conn := newOpenAIWSConn("busy", accountID, &openAIWSFakeConn{}, nil)
	require.True(t, conn.tryAcquire()) // 占用连接，触发后续排队

	pool.mu.Lock()
	ap := pool.ensureAccountPoolLocked(accountID)
	ap.conns[conn.id] = conn
	ap.lastAcquire = &openAIWSAcquireRequest{
		Account: account,
		WSURL:   "wss://example.com/v1/responses",
	}
	pool.mu.Unlock()

	go func() {
		time.Sleep(60 * time.Millisecond)
		conn.release()
	}()

	lease, err := pool.Acquire(context.Background(), openAIWSAcquireRequest{
		Account: account,
		WSURL:   "wss://example.com/v1/responses",
	})
	require.NoError(t, err)
	require.NotNil(t, lease)
	require.True(t, lease.Reused())
	require.GreaterOrEqual(t, lease.QueueWaitDuration(), 50*time.Millisecond)
	lease.Release()

	metrics := pool.SnapshotMetrics()
	require.GreaterOrEqual(t, metrics.AcquireQueueWaitTotal, int64(1))
	require.Greater(t, metrics.AcquireQueueWaitMsTotal, int64(0))
}

func TestOpenAIWSConnPool_EffectiveMaxConnsByAccount(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 8
	cfg.Gateway.OpenAIWS.DynamicMaxConnsByAccountConcurrencyEnabled = true
	cfg.Gateway.OpenAIWS.OAuthMaxConnsFactor = 1.0
	cfg.Gateway.OpenAIWS.APIKeyMaxConnsFactor = 0.6

	pool := newOpenAIWSConnPool(cfg)

	oauthHigh := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 10}
	require.Equal(t, 8, pool.effectiveMaxConnsByAccount(oauthHigh), "应受全局硬上限约束")

	oauthLow := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 3}
	require.Equal(t, 3, pool.effectiveMaxConnsByAccount(oauthLow))

	apiKeyHigh := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 10}
	require.Equal(t, 6, pool.effectiveMaxConnsByAccount(apiKeyHigh), "API Key 应按系数缩放")

	apiKeyLow := &Account{Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
	require.Equal(t, 1, pool.effectiveMaxConnsByAccount(apiKeyLow), "最小值应保持为 1")

	unlimited := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 0}
	require.Equal(t, 8, pool.effectiveMaxConnsByAccount(unlimited), "无限并发应回退到全局硬上限")

	require.Equal(t, 8, pool.effectiveMaxConnsByAccount(nil), "缺少账号上下文应回退到全局硬上限")
}

func TestOpenAIWSConnPool_EffectiveMaxConnsDisabledFallbackHardCap(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 8
	cfg.Gateway.OpenAIWS.DynamicMaxConnsByAccountConcurrencyEnabled = false
	cfg.Gateway.OpenAIWS.OAuthMaxConnsFactor = 1.0
	cfg.Gateway.OpenAIWS.APIKeyMaxConnsFactor = 1.0

	pool := newOpenAIWSConnPool(cfg)
	account := &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Concurrency: 2}
	require.Equal(t, 8, pool.effectiveMaxConnsByAccount(account), "关闭动态模式后应保持旧行为")
}

type openAIWSFakeDialer struct{}

func (d *openAIWSFakeDialer) Dial(
	ctx context.Context,
	wsURL string,
	headers http.Header,
	proxyURL string,
) (openAIWSClientConn, int, http.Header, error) {
	_ = ctx
	_ = wsURL
	_ = headers
	_ = proxyURL
	return &openAIWSFakeConn{}, 0, nil, nil
}

type openAIWSFakeConn struct {
	mu      sync.Mutex
	closed  bool
	payload [][]byte
}

func (c *openAIWSFakeConn) WriteJSON(ctx context.Context, value any) error {
	_ = ctx
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("closed")
	}
	c.payload = append(c.payload, []byte("ok"))
	_ = value
	return nil
}

func (c *openAIWSFakeConn) ReadMessage(ctx context.Context) ([]byte, error) {
	_ = ctx
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("closed")
	}
	return []byte(`{"type":"response.completed","response":{"id":"resp_fake"}}`), nil
}

func (c *openAIWSFakeConn) Ping(ctx context.Context) error {
	_ = ctx
	return nil
}

func (c *openAIWSFakeConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

type openAIWSNilConnDialer struct{}

func (d *openAIWSNilConnDialer) Dial(
	ctx context.Context,
	wsURL string,
	headers http.Header,
	proxyURL string,
) (openAIWSClientConn, int, http.Header, error) {
	_ = ctx
	_ = wsURL
	_ = headers
	_ = proxyURL
	return nil, 200, nil, nil
}

func TestOpenAIWSConnPool_DialConnNilConnection(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 2
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 1

	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(&openAIWSNilConnDialer{})
	account := &Account{ID: 91, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}

	_, err := pool.Acquire(context.Background(), openAIWSAcquireRequest{
		Account: account,
		WSURL:   "wss://example.com/v1/responses",
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "nil connection")
}
