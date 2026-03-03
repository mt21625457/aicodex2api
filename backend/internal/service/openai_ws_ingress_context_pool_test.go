package service

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAIWSIngressContextPool_Acquire_HardCapacityEqualsConcurrency(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 60

	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{
			&openAIWSCaptureConn{},
			&openAIWSCaptureConn{},
		},
	}
	pool.setClientDialerForTest(dialer)

	account := &Account{ID: 801, Concurrency: 1}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	lease1, err := pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
		Account:     account,
		GroupID:     2,
		SessionHash: "session_a",
		OwnerID:     "owner_a",
		WSURL:       "ws://test-upstream",
	})
	require.NoError(t, err)
	require.NotNil(t, lease1)

	_, err = pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
		Account:     account,
		GroupID:     2,
		SessionHash: "session_b",
		OwnerID:     "owner_b",
		WSURL:       "ws://test-upstream",
	})
	require.ErrorIs(t, err, errOpenAIWSConnQueueFull, "并发=1 时第二个并发 owner 不应获取到 context")

	lease1.Release()

	lease2, err := pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
		Account:     account,
		GroupID:     2,
		SessionHash: "session_b",
		OwnerID:     "owner_b",
		WSURL:       "ws://test-upstream",
	})
	require.NoError(t, err)
	require.NotNil(t, lease2)
	require.Equal(t, openAIWSIngressScheduleLayerMigration, lease2.ScheduleLayer())
	require.Equal(t, openAIWSIngressStickinessWeak, lease2.StickinessLevel())
	require.True(t, lease2.MigrationUsed())
	lease2.Release()

	require.Equal(t, 2, dialer.DialCount(), "会话回收复用 context 后应重建上游连接，避免跨会话污染")
}

func TestOpenAIWSIngressContextPool_Acquire_RespectsGlobalHardCap(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 60
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 2

	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{
			&openAIWSCaptureConn{},
			&openAIWSCaptureConn{},
			&openAIWSCaptureConn{},
		},
	}
	pool.setClientDialerForTest(dialer)

	account := &Account{ID: 802, Concurrency: 10}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	lease1, err := pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
		Account:               account,
		GroupID:               3,
		SessionHash:           "session_a",
		OwnerID:               "owner_a",
		WSURL:                 "ws://test-upstream",
		HasPreviousResponseID: true,
	})
	require.NoError(t, err)
	require.NotNil(t, lease1)

	lease2, err := pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
		Account:               account,
		GroupID:               3,
		SessionHash:           "session_b",
		OwnerID:               "owner_b",
		WSURL:                 "ws://test-upstream",
		HasPreviousResponseID: true,
	})
	require.NoError(t, err)
	require.NotNil(t, lease2)

	_, err = pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
		Account:               account,
		GroupID:               3,
		SessionHash:           "session_c",
		OwnerID:               "owner_c",
		WSURL:                 "ws://test-upstream",
		HasPreviousResponseID: true,
	})
	require.ErrorIs(t, err, errOpenAIWSConnQueueFull, "账号并发高于全局硬上限时，context pool 仍应被硬上限约束")

	lease1.Release()
	lease2.Release()
	require.Equal(t, 2, dialer.DialCount())
}

func TestOpenAIWSIngressContextPool_Acquire_DoesNotCrossAccount(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 60

	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{
			&openAIWSCaptureConn{},
			&openAIWSCaptureConn{},
		},
	}
	pool.setClientDialerForTest(dialer)

	accountA := &Account{ID: 901, Concurrency: 1}
	accountB := &Account{ID: 902, Concurrency: 1}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	leaseA, err := pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
		Account:     accountA,
		GroupID:     5,
		SessionHash: "same_session_hash",
		OwnerID:     "owner_a",
		WSURL:       "ws://test-upstream",
	})
	require.NoError(t, err)
	require.NotNil(t, leaseA)
	leaseA.Release()

	leaseB, err := pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
		Account:     accountB,
		GroupID:     5,
		SessionHash: "same_session_hash",
		OwnerID:     "owner_b",
		WSURL:       "ws://test-upstream",
	})
	require.NoError(t, err)
	require.NotNil(t, leaseB)
	leaseB.Release()

	require.Equal(t, 2, dialer.DialCount(), "相同 session_hash 在不同账号下必须使用不同 context，不允许跨账号复用")
}

func TestOpenAIWSIngressContextPool_Acquire_StrongStickinessDisablesMigration(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 60

	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{
			&openAIWSCaptureConn{},
		},
	}
	pool.setClientDialerForTest(dialer)

	account := &Account{ID: 1001, Concurrency: 1}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	lease1, err := pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
		Account:     account,
		GroupID:     9,
		SessionHash: "session_keep_strong_a",
		OwnerID:     "owner_a",
		WSURL:       "ws://test-upstream",
	})
	require.NoError(t, err)
	require.NotNil(t, lease1)
	lease1.Release()

	_, err = pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
		Account:               account,
		GroupID:               9,
		SessionHash:           "session_keep_strong_b",
		OwnerID:               "owner_b",
		WSURL:                 "ws://test-upstream",
		HasPreviousResponseID: true,
	})
	require.ErrorIs(t, err, errOpenAIWSConnQueueFull, "strong 粘连不应迁移其它 session 的 context")
}

func TestOpenAIWSIngressContextPool_Acquire_AdaptiveStickinessDowngradesAfterFailure(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 60

	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{
			&openAIWSCaptureConn{},
			&openAIWSCaptureConn{},
		},
	}
	pool.setClientDialerForTest(dialer)

	account := &Account{ID: 1002, Concurrency: 1}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	lease1, err := pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
		Account:     account,
		GroupID:     11,
		SessionHash: "session_adaptive",
		OwnerID:     "owner_a",
		WSURL:       "ws://test-upstream",
	})
	require.NoError(t, err)
	require.NotNil(t, lease1)
	lease1.MarkBroken()
	lease1.Release()

	lease2, err := pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
		Account:               account,
		GroupID:               11,
		SessionHash:           "session_adaptive",
		OwnerID:               "owner_b",
		WSURL:                 "ws://test-upstream",
		HasPreviousResponseID: true,
	})
	require.NoError(t, err)
	require.NotNil(t, lease2)
	require.Equal(t, openAIWSIngressScheduleLayerExact, lease2.ScheduleLayer())
	require.Equal(t, openAIWSIngressStickinessBalanced, lease2.StickinessLevel(), "失败后应从 strong 自适应降级到 balanced")
	lease2.Release()
	require.Equal(t, 2, dialer.DialCount(), "故障后重连同一 context 应重新建立上游连接")
}

func TestOpenAIWSIngressContextPool_Acquire_EnsureFailureReleasesOwner(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 60

	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	initialDialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{
			&openAIWSCaptureConn{},
		},
	}
	pool.setClientDialerForTest(initialDialer)

	account := &Account{ID: 1101, Concurrency: 1}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	lease1, err := pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
		Account:     account,
		GroupID:     12,
		SessionHash: "session_owner_release",
		OwnerID:     "owner_a",
		WSURL:       "ws://test-upstream",
	})
	require.NoError(t, err)
	require.NotNil(t, lease1)
	lease1.Release()

	failDialer := &openAIWSAlwaysFailDialer{}
	pool.setClientDialerForTest(failDialer)
	_, err = pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
		Account:     account,
		GroupID:     12,
		SessionHash: "session_owner_release",
		OwnerID:     "owner_b",
		WSURL:       "ws://test-upstream",
	})
	require.Error(t, err)
	require.NotErrorIs(t, err, errOpenAIWSIngressContextBusy, "ensure 上游失败后不应遗留 owner 导致 context 长时间 busy")

	recoverDialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{
			&openAIWSCaptureConn{},
		},
	}
	pool.setClientDialerForTest(recoverDialer)

	lease3, err := pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
		Account:     account,
		GroupID:     12,
		SessionHash: "session_owner_release",
		OwnerID:     "owner_c",
		WSURL:       "ws://test-upstream",
	})
	require.NoError(t, err, "owner 回滚后应允许后续会话重新获取同一 context")
	require.NotNil(t, lease3)
	lease3.Release()
	require.Equal(t, 1, failDialer.DialCount())
	require.Equal(t, 1, recoverDialer.DialCount())
}

func TestOpenAIWSIngressContextPool_Release_ClosesUpstreamAndForcesRedial(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 60

	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	upstreamConn1 := &openAIWSCaptureConn{}
	upstreamConn2 := &openAIWSCaptureConn{}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{
			upstreamConn1,
			upstreamConn2,
		},
	}
	pool.setClientDialerForTest(dialer)

	account := &Account{ID: 1102, Concurrency: 1}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	lease1, err := pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
		Account:     account,
		GroupID:     13,
		SessionHash: "session_same",
		OwnerID:     "owner_a",
		WSURL:       "ws://test-upstream",
	})
	require.NoError(t, err)
	require.NotNil(t, lease1)
	connID1 := lease1.ConnID()
	require.NotEmpty(t, connID1)
	lease1.Release()

	upstreamConn1.mu.Lock()
	closed1 := upstreamConn1.closed
	upstreamConn1.mu.Unlock()
	require.True(t, closed1, "客户端会话结束后应关闭对应上游连接，防止复用污染")

	lease2, err := pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
		Account:     account,
		GroupID:     13,
		SessionHash: "session_same",
		OwnerID:     "owner_b",
		WSURL:       "ws://test-upstream",
	})
	require.NoError(t, err)
	require.NotNil(t, lease2)
	connID2 := lease2.ConnID()
	require.NotEmpty(t, connID2)
	require.NotEqual(t, connID1, connID2, "下一次会话必须重新建立上游连接")
	lease2.Release()

	upstreamConn2.mu.Lock()
	closed2 := upstreamConn2.closed
	upstreamConn2.mu.Unlock()
	require.True(t, closed2)
	require.Equal(t, 2, dialer.DialCount())
}

func TestOpenAIWSIngressContextPool_Yield_ReleasesOwnerKeepsUpstream(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 60

	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	upstreamConn := &openAIWSCaptureConn{}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{upstreamConn},
	}
	pool.setClientDialerForTest(dialer)

	account := &Account{ID: 1103, Concurrency: 1}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	lease1, err := pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
		Account:     account,
		GroupID:     14,
		SessionHash: "session_yield",
		OwnerID:     "owner_a",
		WSURL:       "ws://test-upstream",
	})
	require.NoError(t, err)
	require.NotNil(t, lease1)
	connID1 := lease1.ConnID()
	require.NotEmpty(t, connID1)

	lease1.Yield()
	upstreamConn.mu.Lock()
	closedAfterYield := upstreamConn.closed
	upstreamConn.mu.Unlock()
	require.False(t, closedAfterYield, "yield 只应释放 owner，不应关闭上游连接")

	lease2, err := pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
		Account:     account,
		GroupID:     14,
		SessionHash: "session_yield",
		OwnerID:     "owner_b",
		WSURL:       "ws://test-upstream",
	})
	require.NoError(t, err)
	require.NotNil(t, lease2)
	require.Equal(t, connID1, lease2.ConnID(), "yield 后应复用同一上游连接")
	require.Equal(t, 1, dialer.DialCount(), "yield 后重新获取不应触发重拨号")

	lease2.Release()
	upstreamConn.mu.Lock()
	closedAfterRelease := upstreamConn.closed
	upstreamConn.mu.Unlock()
	require.True(t, closedAfterRelease, "release 仍需关闭上游连接")
}

func TestOpenAIWSIngressContextPool_EvictExpiredIdleLocked_ClosesUpstream(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 60

	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	upstreamConn := &openAIWSCaptureConn{}
	ap := &openAIWSIngressAccountPool{
		contexts:  make(map[string]*openAIWSIngressContext),
		bySession: make(map[string]string),
	}
	expiredCtx := &openAIWSIngressContext{
		id:               "ctx_expired_1",
		groupID:          21,
		accountID:        1201,
		sessionHash:      "session_expired",
		sessionKey:       openAIWSIngressContextSessionKey(21, "session_expired"),
		upstream:         upstreamConn,
		upstreamConnID:   "ctxws_1201_1",
		handshakeHeaders: map[string][]string{"x-test": {"ok"}},
	}
	expiredCtx.setExpiresAt(time.Now().Add(-2 * time.Second))
	ap.contexts[expiredCtx.id] = expiredCtx
	ap.bySession[expiredCtx.sessionKey] = expiredCtx.id

	ap.mu.Lock()
	toClose := pool.evictExpiredIdleLocked(ap, time.Now())
	ap.mu.Unlock()
	closeOpenAIWSClientConns(toClose)

	require.Empty(t, ap.contexts, "过期 idle context 应被清理")
	require.Empty(t, ap.bySession, "过期 context 的 session 索引应同步清理")
	upstreamConn.mu.Lock()
	closed := upstreamConn.closed
	upstreamConn.mu.Unlock()
	require.True(t, closed, "清理过期 context 时应关闭残留上游连接，避免泄漏")
}

func TestOpenAIWSIngressContextPool_ScoreAndStickinessHelpers(t *testing.T) {
	now := time.Now()

	require.Equal(t, 1, minInt(1, 2))
	require.Equal(t, 2, minInt(3, 2))

	require.Equal(t, openAIWSIngressStickinessBalanced, openAIWSIngressStickinessDowngrade(openAIWSIngressStickinessStrong))
	require.Equal(t, openAIWSIngressStickinessWeak, openAIWSIngressStickinessDowngrade(openAIWSIngressStickinessBalanced))
	require.Equal(t, openAIWSIngressStickinessWeak, openAIWSIngressStickinessDowngrade("unknown"))

	require.Equal(t, openAIWSIngressStickinessBalanced, openAIWSIngressStickinessUpgrade(openAIWSIngressStickinessWeak))
	require.Equal(t, openAIWSIngressStickinessStrong, openAIWSIngressStickinessUpgrade(openAIWSIngressStickinessBalanced))
	require.Equal(t, openAIWSIngressStickinessStrong, openAIWSIngressStickinessUpgrade("unknown"))

	allowStrong, scoreStrong := openAIWSIngressMigrationPolicyByStickiness(openAIWSIngressStickinessStrong)
	require.False(t, allowStrong)
	require.Equal(t, 80.0, scoreStrong)
	allowBalanced, scoreBalanced := openAIWSIngressMigrationPolicyByStickiness(openAIWSIngressStickinessBalanced)
	require.True(t, allowBalanced)
	require.Equal(t, 65.0, scoreBalanced)
	allowWeak, scoreWeak := openAIWSIngressMigrationPolicyByStickiness("weak_or_unknown")
	require.True(t, allowWeak)
	require.Equal(t, 40.0, scoreWeak)

	busyCtx := &openAIWSIngressContext{ownerID: "owner_busy"}
	_, _, ok := scoreOpenAIWSIngressMigrationCandidate(busyCtx, now, nil)
	require.False(t, ok, "owner 占用中的 context 不应作为迁移候选")

	oldIdle := &openAIWSIngressContext{}
	oldIdle.setLastUsedAt(now.Add(-5 * time.Minute))
	recentIdle := &openAIWSIngressContext{}
	recentIdle.setLastUsedAt(now.Add(-10 * time.Second))
	scoreOld, _, okOld := scoreOpenAIWSIngressMigrationCandidate(oldIdle, now, nil)
	scoreRecent, _, okRecent := scoreOpenAIWSIngressMigrationCandidate(recentIdle, now, nil)
	require.True(t, okOld)
	require.True(t, okRecent)
	require.Greater(t, scoreOld, scoreRecent, "更久未使用的空闲 context 应该更易被迁移")

	penalized := &openAIWSIngressContext{
		broken:          true,
		failureStreak:   2,
		lastFailureAt:   now.Add(-30 * time.Second),
		migrationCount:  2,
		lastMigrationAt: now.Add(-10 * time.Second),
	}
	penalized.setLastUsedAt(now.Add(-5 * time.Minute))
	scorePenalized, _, okPenalized := scoreOpenAIWSIngressMigrationCandidate(penalized, now, nil)
	require.True(t, okPenalized)
	require.Less(t, scorePenalized, scoreOld, "近期失败和频繁迁移应降低迁移分数")
}

func TestOpenAIWSIngressContextPool_EvictPickAndSweep(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 1

	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	now := time.Now()
	expiredConn := &openAIWSCaptureConn{}
	expiredCtx := &openAIWSIngressContext{
		id:             "ctx_expired",
		sessionKey:     "1:expired",
		upstream:       expiredConn,
		upstreamConnID: "ctxws_expired",
	}
	expiredCtx.setLastUsedAt(now.Add(-20 * time.Minute))
	expiredCtx.setExpiresAt(now.Add(-time.Minute))

	idleNewCtx := &openAIWSIngressContext{
		id:         "ctx_idle_new",
		sessionKey: "1:idle_new",
	}
	idleNewCtx.setLastUsedAt(now.Add(-30 * time.Second))
	idleNewCtx.setExpiresAt(now.Add(time.Minute))

	busyCtx := &openAIWSIngressContext{
		id:         "ctx_busy",
		sessionKey: "1:busy",
		ownerID:    "active_owner",
	}
	busyCtx.setLastUsedAt(now.Add(-40 * time.Minute))
	busyCtx.setExpiresAt(now.Add(-time.Minute))

	ap := &openAIWSIngressAccountPool{
		contexts: map[string]*openAIWSIngressContext{
			"ctx_expired":  expiredCtx,
			"ctx_idle_new": idleNewCtx,
			"ctx_busy":     busyCtx,
		},
		bySession: map[string]string{
			"1:expired":  "ctx_expired",
			"1:idle_new": "ctx_idle_new",
			"1:busy":     "ctx_busy",
		},
	}

	ap.mu.Lock()
	oldestIdle := pool.pickOldestIdleContextLocked(ap)
	ap.mu.Unlock()
	require.NotNil(t, oldestIdle)
	require.Equal(t, "ctx_expired", oldestIdle.id, "应选择最旧的空闲 context")

	ap.mu.Lock()
	toClose := pool.evictExpiredIdleLocked(ap, now)
	ap.mu.Unlock()
	closeOpenAIWSClientConns(toClose)
	require.NotContains(t, ap.contexts, "ctx_expired")
	require.NotContains(t, ap.bySession, "1:expired")
	require.Contains(t, ap.contexts, "ctx_idle_new", "未过期空闲 context 应保留")
	require.Contains(t, ap.contexts, "ctx_busy", "有 owner 的 context 不应被 idle 过期清理")
	expiredConn.mu.Lock()
	expiredClosed := expiredConn.closed
	expiredConn.mu.Unlock()
	require.True(t, expiredClosed, "清理过期 idle context 时应关闭上游连接")

	expiredInPoolConn := &openAIWSCaptureConn{}
	pool.mu.Lock()
	pool.accounts[5001] = ap
	poolExpiredCtx := &openAIWSIngressContext{
		id:         "ctx_pool_expired",
		sessionKey: "2:expired",
		upstream:   expiredInPoolConn,
	}
	poolExpiredCtx.setExpiresAt(now.Add(-time.Minute))
	pool.accounts[5002] = &openAIWSIngressAccountPool{
		contexts: map[string]*openAIWSIngressContext{
			"ctx_pool_expired": poolExpiredCtx,
		},
		bySession: map[string]string{
			"2:expired": "ctx_pool_expired",
		},
	}
	pool.mu.Unlock()

	pool.sweepExpiredIdleContexts()

	pool.mu.Lock()
	_, account2Exists := pool.accounts[5002]
	account1 := pool.accounts[5001]
	pool.mu.Unlock()
	require.False(t, account2Exists, "sweep 后空账号应被移除")
	require.NotNil(t, account1, "非空账号应保留")
	expiredInPoolConn.mu.Lock()
	sweptClosed := expiredInPoolConn.closed
	expiredInPoolConn.mu.Unlock()
	require.True(t, sweptClosed)
}

func TestOpenAIWSIngressContextLease_AccessorsAndPingGuards(t *testing.T) {
	var nilLease *openAIWSIngressContextLease
	require.Equal(t, "", nilLease.ConnID())
	require.Zero(t, nilLease.QueueWaitDuration())
	require.Zero(t, nilLease.ConnPickDuration())
	require.False(t, nilLease.Reused())
	require.Equal(t, "", nilLease.ScheduleLayer())
	require.Equal(t, "", nilLease.StickinessLevel())
	require.False(t, nilLease.MigrationUsed())
	require.Equal(t, "", nilLease.HandshakeHeader("x-test"))
	require.ErrorIs(t, nilLease.PingWithTimeout(time.Millisecond), errOpenAIWSConnClosed)

	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 60
	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	ctxItem := &openAIWSIngressContext{
		id:               "ctx_lease",
		ownerID:          "owner_ok",
		upstream:         &openAIWSFakeConn{},
		handshakeHeaders: http.Header{"X-Test": []string{"ok"}},
	}
	lease := &openAIWSIngressContextLease{
		pool:          pool,
		context:       ctxItem,
		ownerID:       "owner_ok",
		queueWait:     5 * time.Millisecond,
		connPick:      8 * time.Millisecond,
		reused:        true,
		scheduleLayer: openAIWSIngressScheduleLayerExact,
		stickiness:    openAIWSIngressStickinessBalanced,
		migrationUsed: true,
	}

	require.Equal(t, "ok", lease.HandshakeHeader("x-test"))
	require.Equal(t, 5*time.Millisecond, lease.QueueWaitDuration())
	require.Equal(t, 8*time.Millisecond, lease.ConnPickDuration())
	require.True(t, lease.Reused())
	require.Equal(t, openAIWSIngressScheduleLayerExact, lease.ScheduleLayer())
	require.Equal(t, openAIWSIngressStickinessBalanced, lease.StickinessLevel())
	require.True(t, lease.MigrationUsed())
	require.NoError(t, lease.PingWithTimeout(0), "timeout=0 应回退默认 ping 超时")

	lease.released.Store(true)
	require.ErrorIs(t, lease.PingWithTimeout(time.Millisecond), errOpenAIWSConnClosed)
	lease.released.Store(false)

	ctxItem.mu.Lock()
	ctxItem.ownerID = "other_owner"
	ctxItem.mu.Unlock()
	lease.cachedConn = nil // clear cache to force re-validation (simulates migration)
	require.ErrorIs(t, lease.PingWithTimeout(time.Millisecond), errOpenAIWSConnClosed, "owner 不匹配时应拒绝访问")

	ctxItem.mu.Lock()
	ctxItem.ownerID = "owner_ok"
	ctxItem.upstream = &openAIWSPingFailConn{}
	ctxItem.mu.Unlock()
	lease.cachedConn = nil // clear cache to pick up new upstream
	require.Error(t, lease.PingWithTimeout(time.Millisecond), "上游 ping 失败应透传错误")

	lease.Release()
	lease.Release()
	require.Equal(t, "", lease.context.ownerID, "重复 Release 应幂等且不会 panic")
}

func TestOpenAIWSIngressContextPool_EnsureContextUpstreamBranches(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 60

	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	ctxItem := &openAIWSIngressContext{
		id:        "ctx_ensure",
		accountID: 1,
		ownerID:   "owner",
		upstream:  &openAIWSFakeConn{},
	}

	reused, err := pool.ensureContextUpstream(context.Background(), ctxItem, openAIWSIngressContextAcquireRequest{
		WSURL: "ws://test",
	})
	require.NoError(t, err)
	require.True(t, reused, "已有可用 upstream 时应直接复用")

	pool.dialer = nil
	ctxItem.mu.Lock()
	ctxItem.broken = true
	ctxItem.mu.Unlock()
	_, err = pool.ensureContextUpstream(context.Background(), ctxItem, openAIWSIngressContextAcquireRequest{
		WSURL: "ws://test",
	})
	require.ErrorContains(t, err, "dialer is nil")

	failDialer := &openAIWSAlwaysFailDialer{}
	pool.setClientDialerForTest(failDialer)
	_, err = pool.ensureContextUpstream(context.Background(), ctxItem, openAIWSIngressContextAcquireRequest{
		WSURL: "ws://test",
	})
	require.Error(t, err)
	var dialErr *openAIWSDialError
	require.ErrorAs(t, err, &dialErr, "dial 失败应包装为 openAIWSDialError")
	require.Equal(t, 503, dialErr.StatusCode)
	ctxItem.mu.Lock()
	broken := ctxItem.broken
	failureStreak := ctxItem.failureStreak
	ctxItem.mu.Unlock()
	require.True(t, broken)
	require.GreaterOrEqual(t, failureStreak, 1, "dial 失败后应累计 failure_streak")
}

func TestOpenAIWSIngressContextPool_MarkBrokenDoesNotSignalWaiterBeforeRelease(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 60

	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	upstream := &openAIWSCaptureConn{}
	ctxItem := &openAIWSIngressContext{
		id:          "ctx_mark_broken",
		ownerID:     "owner_broken",
		releaseDone: make(chan struct{}, 1),
		upstream:    upstream,
	}

	pool.markContextBroken(ctxItem)

	select {
	case <-ctxItem.releaseDone:
		t.Fatal("markContextBroken should not wake waiters before owner is released")
	default:
	}

	ctxItem.mu.Lock()
	require.True(t, ctxItem.broken)
	require.Equal(t, "owner_broken", ctxItem.ownerID)
	require.Nil(t, ctxItem.upstream)
	ctxItem.mu.Unlock()

	upstream.mu.Lock()
	require.True(t, upstream.closed, "mark broken should close current upstream connection")
	upstream.mu.Unlock()

	pool.releaseContext(ctxItem, "owner_broken")

	select {
	case <-ctxItem.releaseDone:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("releaseContext should signal one waiting acquire after owner is released")
	}

	ctxItem.mu.Lock()
	require.Equal(t, "", ctxItem.ownerID)
	require.False(t, ctxItem.broken)
	ctxItem.mu.Unlock()
}

type openAIWSWriteDisconnectConn struct{}

func (c *openAIWSWriteDisconnectConn) WriteJSON(context.Context, any) error {
	return net.ErrClosed
}

func (c *openAIWSWriteDisconnectConn) ReadMessage(context.Context) ([]byte, error) {
	return nil, net.ErrClosed
}

func (c *openAIWSWriteDisconnectConn) Ping(context.Context) error {
	return net.ErrClosed
}

func (c *openAIWSWriteDisconnectConn) Close() error {
	return nil
}

type openAIWSWriteGenericFailConn struct{}

func (c *openAIWSWriteGenericFailConn) WriteJSON(context.Context, any) error {
	return errors.New("writer failed")
}

func (c *openAIWSWriteGenericFailConn) ReadMessage(context.Context) ([]byte, error) {
	return nil, errors.New("reader failed")
}

func (c *openAIWSWriteGenericFailConn) Ping(context.Context) error {
	return errors.New("ping failed")
}

func (c *openAIWSWriteGenericFailConn) Close() error {
	return nil
}

func TestOpenAIWSIngressContextLease_IOErrorInvalidatesCachedConn(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 60

	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	upstream := &openAIWSWriteDisconnectConn{}
	ctxItem := &openAIWSIngressContext{
		id:        "ctx_io_err",
		accountID: 7,
		ownerID:   "owner_io",
		upstream:  upstream,
	}
	lease := &openAIWSIngressContextLease{
		pool:    pool,
		context: ctxItem,
		ownerID: "owner_io",
	}
	lease.cachedConn = upstream

	err := lease.WriteJSONWithContextTimeout(context.Background(), map[string]any{"type": "response.create"}, time.Second)
	require.Error(t, err)
	require.ErrorIs(t, err, net.ErrClosed)

	lease.cachedConnMu.RLock()
	cached := lease.cachedConn
	lease.cachedConnMu.RUnlock()
	require.Nil(t, cached, "write failure should invalidate cachedConn")

	ctxItem.mu.Lock()
	require.True(t, ctxItem.broken, "disconnect-style IO failure should mark context broken")
	require.Nil(t, ctxItem.upstream, "broken context should drop upstream reference")
	ctxItem.mu.Unlock()
}

func TestOpenAIWSIngressContextLease_GenericIOErrorKeepsContextButInvalidatesCache(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 60

	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	upstream := &openAIWSWriteGenericFailConn{}
	ctxItem := &openAIWSIngressContext{
		id:        "ctx_generic_err",
		accountID: 8,
		ownerID:   "owner_generic",
		upstream:  upstream,
	}
	lease := &openAIWSIngressContextLease{
		pool:    pool,
		context: ctxItem,
		ownerID: "owner_generic",
	}
	lease.cachedConn = upstream

	err := lease.PingWithTimeout(time.Second)
	require.Error(t, err)

	lease.cachedConnMu.RLock()
	cached := lease.cachedConn
	lease.cachedConnMu.RUnlock()
	require.Nil(t, cached, "generic IO failure should still invalidate cachedConn")

	ctxItem.mu.Lock()
	require.False(t, ctxItem.broken, "non-disconnect IO failure should not force-broken context")
	require.Equal(t, upstream, ctxItem.upstream, "upstream should remain for non-disconnect errors")
	ctxItem.mu.Unlock()
}

func TestOpenAIWSIngressContextPool_EnsureContextUpstream_SerializesConcurrentDial(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 60

	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	releaseDial := make(chan struct{})
	blockingDialer := &openAIWSBlockingDialer{
		release:     releaseDial,
		dialStarted: make(chan struct{}, 4),
	}
	pool.setClientDialerForTest(blockingDialer)

	account := &Account{ID: 1301, Concurrency: 1}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	type acquireResult struct {
		lease *openAIWSIngressContextLease
		err   error
	}
	resultCh := make(chan acquireResult, 2)
	acquireOnce := func() {
		lease, err := pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
			Account:     account,
			GroupID:     23,
			SessionHash: "session_same_owner",
			OwnerID:     "owner_same",
			WSURL:       "ws://test-upstream",
		})
		resultCh <- acquireResult{lease: lease, err: err}
	}

	go acquireOnce()
	select {
	case <-blockingDialer.dialStarted:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("首个 dial 未按预期启动")
	}
	go acquireOnce()

	select {
	case <-blockingDialer.dialStarted:
		t.Fatal("同一 context 并发 acquire 不应触发第二次 dial")
	case <-time.After(120 * time.Millisecond):
	}

	close(releaseDial)

	results := make([]acquireResult, 0, 2)
	for i := 0; i < 2; i++ {
		select {
		case result := <-resultCh:
			require.NoError(t, result.err)
			require.NotNil(t, result.lease)
			results = append(results, result)
		case <-time.After(2 * time.Second):
			t.Fatal("等待并发 acquire 结果超时")
		}
	}

	for _, result := range results {
		result.lease.Release()
	}
	require.Equal(t, 1, blockingDialer.DialCount(), "同一 context 并发获取应只发生一次上游拨号")
}

func TestOpenAIWSIngressContextPool_EnsureContextUpstream_WaiterTimeoutDoesNotReleaseOwner(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 60

	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	releaseDial := make(chan struct{})
	blockingDialer := &openAIWSBlockingDialer{
		release:     releaseDial,
		dialStarted: make(chan struct{}, 4),
	}
	pool.setClientDialerForTest(blockingDialer)

	account := &Account{ID: 1302, Concurrency: 1}
	baseReq := openAIWSIngressContextAcquireRequest{
		Account:     account,
		GroupID:     24,
		SessionHash: "session_waiter_timeout",
		OwnerID:     "owner_same",
		WSURL:       "ws://test-upstream",
	}

	longCtx, longCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer longCancel()
	type acquireResult struct {
		lease *openAIWSIngressContextLease
		err   error
	}
	firstResultCh := make(chan acquireResult, 1)
	go func() {
		lease, err := pool.Acquire(longCtx, baseReq)
		firstResultCh <- acquireResult{lease: lease, err: err}
	}()

	select {
	case <-blockingDialer.dialStarted:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("首个 dial 未按预期启动")
	}

	shortCtx, shortCancel := context.WithTimeout(context.Background(), 60*time.Millisecond)
	defer shortCancel()
	_, waiterErr := pool.Acquire(shortCtx, baseReq)
	require.ErrorIs(t, waiterErr, context.DeadlineExceeded, "等待中的 acquire 超时应返回 context deadline exceeded")

	close(releaseDial)

	select {
	case first := <-firstResultCh:
		require.NoError(t, first.err)
		require.NotNil(t, first.lease)
		require.NoError(t, first.lease.WriteJSONWithContextTimeout(context.Background(), map[string]any{"type": "ping"}, time.Second), "等待方超时不应释放已建连 owner")
		first.lease.Release()
	case <-time.After(2 * time.Second):
		t.Fatal("等待首个 acquire 结果超时")
	}

	require.Equal(t, 1, blockingDialer.DialCount())
}

func TestOpenAIWSIngressContextPool_Acquire_QueueWaitDurationRecorded(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 60

	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{
			&openAIWSCaptureConn{},
			&openAIWSCaptureConn{},
		},
	}
	pool.setClientDialerForTest(dialer)

	account := &Account{ID: 1303, Concurrency: 1}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	lease1, err := pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
		Account:     account,
		GroupID:     25,
		SessionHash: "session_queue_wait",
		OwnerID:     "owner_a",
		WSURL:       "ws://test-upstream",
	})
	require.NoError(t, err)
	require.NotNil(t, lease1)

	type acquireResult struct {
		lease *openAIWSIngressContextLease
		err   error
	}
	waiterCh := make(chan acquireResult, 1)
	go func() {
		lease, acquireErr := pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
			Account:     account,
			GroupID:     25,
			SessionHash: "session_queue_wait",
			OwnerID:     "owner_b",
			WSURL:       "ws://test-upstream",
		})
		waiterCh <- acquireResult{lease: lease, err: acquireErr}
	}()

	time.Sleep(120 * time.Millisecond)
	lease1.Release()

	select {
	case waiter := <-waiterCh:
		require.NoError(t, waiter.err)
		require.NotNil(t, waiter.lease)
		require.GreaterOrEqual(t, waiter.lease.QueueWaitDuration(), 100*time.Millisecond)
		waiter.lease.Release()
	case <-time.After(2 * time.Second):
		t.Fatal("等待第二个 acquire 结果超时")
	}
}

type openAIWSBlockingDialer struct {
	mu          sync.Mutex
	release     <-chan struct{}
	dialStarted chan struct{}
	dialCount   int
}

func (d *openAIWSBlockingDialer) Dial(
	ctx context.Context,
	wsURL string,
	headers http.Header,
	proxyURL string,
) (openAIWSClientConn, int, http.Header, error) {
	_ = wsURL
	_ = headers
	_ = proxyURL
	if ctx == nil {
		ctx = context.Background()
	}
	d.mu.Lock()
	d.dialCount++
	d.mu.Unlock()
	select {
	case d.dialStarted <- struct{}{}:
	default:
	}
	if d.release != nil {
		select {
		case <-d.release:
		case <-ctx.Done():
			return nil, 0, nil, ctx.Err()
		}
	}
	return &openAIWSCaptureConn{}, 0, nil, nil
}

func (d *openAIWSBlockingDialer) DialCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dialCount
}

// ---------------------------------------------------------------------------
// Load-aware migration scoring tests
// ---------------------------------------------------------------------------

func TestScoreOpenAIWSIngressMigrationCandidate_HighErrorRatePenalty(t *testing.T) {
	now := time.Now()
	stats := newOpenAIAccountRuntimeStats()
	accountID := int64(9001)

	// Report a pattern of failures interspersed with occasional successes.
	// This pushes the error rate high without tripping the circuit breaker
	// (consecutive failures stay below the default threshold of 5).
	for i := 0; i < 6; i++ {
		stats.report(accountID, false, nil, "", 0)
		stats.report(accountID, false, nil, "", 0)
		stats.report(accountID, false, nil, "", 0)
		stats.report(accountID, true, nil, "", 0) // reset consecutive fail counter
	}
	require.False(t, stats.isCircuitOpen(accountID), "circuit breaker should remain closed for this test")

	ctx := &openAIWSIngressContext{accountID: accountID}
	ctx.setLastUsedAt(now.Add(-5 * time.Minute))

	scoreWithStats, _, okStats := scoreOpenAIWSIngressMigrationCandidate(ctx, now, stats)
	require.True(t, okStats)

	// Score the same context without stats (nil) for comparison.
	scoreWithout, _, okWithout := scoreOpenAIWSIngressMigrationCandidate(ctx, now, nil)
	require.True(t, okWithout)

	require.Less(t, scoreWithStats, scoreWithout,
		"a context on a high-error-rate account should receive a lower migration score")

	// The error rate penalty should be approximately errorRate * 30.
	// Since the circuit breaker is not open, the only load-aware penalty is
	// errorRate * 30.
	errorRate, _, _ := stats.snapshot(accountID)
	expectedPenalty := errorRate * 30
	require.InDelta(t, expectedPenalty, scoreWithout-scoreWithStats, 1.0,
		"penalty should be approximately errorRate * 30")
}

func TestScoreOpenAIWSIngressMigrationCandidate_CircuitOpenPenalty(t *testing.T) {
	now := time.Now()
	stats := newOpenAIAccountRuntimeStats()
	accountID := int64(9002)

	// Trip the circuit breaker by reporting consecutive failures beyond the
	// default threshold (5).
	for i := 0; i < defaultCircuitBreakerFailThreshold+1; i++ {
		stats.report(accountID, false, nil, "", 0)
	}
	require.True(t, stats.isCircuitOpen(accountID), "circuit breaker should be open after many failures")

	ctx := &openAIWSIngressContext{accountID: accountID}
	ctx.setLastUsedAt(now.Add(-5 * time.Minute))

	scoreCircuitOpen, _, ok := scoreOpenAIWSIngressMigrationCandidate(ctx, now, stats)
	require.True(t, ok)

	// Score without stats for comparison.
	scoreBaseline, _, okBase := scoreOpenAIWSIngressMigrationCandidate(ctx, now, nil)
	require.True(t, okBase)

	// The circuit-open penalty is -50, plus errorRate*30, so the score should
	// be substantially lower.
	require.Less(t, scoreCircuitOpen, scoreBaseline-45,
		"a context on a circuit-open account should have a very low migration score")

	// In practice, the combined penalty should bring the score below any
	// reasonable minimum migration threshold (weakest = 40).
	_, weakMin := openAIWSIngressMigrationPolicyByStickiness(openAIWSIngressStickinessWeak)
	require.Less(t, scoreCircuitOpen, weakMin,
		"circuit-open accounts should score below even the weakest migration threshold")
}

func TestScoreOpenAIWSIngressMigrationCandidate_NilStatsFallback(t *testing.T) {
	now := time.Now()

	ctx := &openAIWSIngressContext{accountID: 9003}
	ctx.setLastUsedAt(now.Add(-5 * time.Minute))

	scoreNil, _, okNil := scoreOpenAIWSIngressMigrationCandidate(ctx, now, nil)
	require.True(t, okNil)

	// Create stats but report nothing for this account -- snapshot returns 0.
	emptyStats := newOpenAIAccountRuntimeStats()
	scoreEmpty, _, okEmpty := scoreOpenAIWSIngressMigrationCandidate(ctx, now, emptyStats)
	require.True(t, okEmpty)

	// With no data for the account, the load-aware path should add zero
	// penalty, yielding the same score as nil stats.
	require.InDelta(t, scoreNil, scoreEmpty, 0.001,
		"when scheduler stats have no data for the account, score should match nil-stats baseline")
}

func TestScoreOpenAIWSIngressMigrationCandidate_NilContext(t *testing.T) {
	now := time.Now()
	score, _, ok := scoreOpenAIWSIngressMigrationCandidate(nil, now, nil)
	require.False(t, ok)
	require.Equal(t, 0.0, score)
}

func TestScoreOpenAIWSIngressMigrationCandidate_IdleDurationBranches(t *testing.T) {
	now := time.Now()

	// Very recently used (≤15s): penalty of -15
	recentCtx := &openAIWSIngressContext{}
	recentCtx.setLastUsedAt(now.Add(-5 * time.Second))
	scoreRecent, _, ok := scoreOpenAIWSIngressMigrationCandidate(recentCtx, now, nil)
	require.True(t, ok)
	require.InDelta(t, 100.0-15.0, scoreRecent, 0.5, "very recently used should get -15 penalty")

	// Medium idle (between 15s and 3min): bonus = idleDuration.Seconds() / 12
	mediumCtx := &openAIWSIngressContext{}
	mediumCtx.setLastUsedAt(now.Add(-90 * time.Second)) // 90s idle
	scoreMedium, _, ok := scoreOpenAIWSIngressMigrationCandidate(mediumCtx, now, nil)
	require.True(t, ok)
	expectedBonus := 90.0 / 12.0 // 7.5
	require.InDelta(t, 100.0+expectedBonus, scoreMedium, 0.5, "medium idle should get proportional bonus")

	// Long idle (≥3min): bonus of +16
	longCtx := &openAIWSIngressContext{}
	longCtx.setLastUsedAt(now.Add(-5 * time.Minute))
	scoreLong, _, ok := scoreOpenAIWSIngressMigrationCandidate(longCtx, now, nil)
	require.True(t, ok)
	require.InDelta(t, 100.0+16.0, scoreLong, 0.5, "long idle should get +16 bonus")

	// Verify ordering: long > medium > recent
	require.Greater(t, scoreLong, scoreMedium, "longer idle should score higher than medium")
	require.Greater(t, scoreMedium, scoreRecent, "medium idle should score higher than very recent")
}

func TestScoreOpenAIWSIngressMigrationCandidate_BrokenAndFailures(t *testing.T) {
	now := time.Now()

	// Broken context: -30
	brokenCtx := &openAIWSIngressContext{broken: true}
	brokenCtx.setLastUsedAt(now.Add(-5 * time.Minute))
	scoreBroken, _, ok := scoreOpenAIWSIngressMigrationCandidate(brokenCtx, now, nil)
	require.True(t, ok)

	cleanCtx := &openAIWSIngressContext{}
	cleanCtx.setLastUsedAt(now.Add(-5 * time.Minute))
	scoreClean, _, ok := scoreOpenAIWSIngressMigrationCandidate(cleanCtx, now, nil)
	require.True(t, ok)
	require.InDelta(t, 30.0, scoreClean-scoreBroken, 0.5, "broken should subtract 30")

	// High failure streak (capped at 40)
	highFailCtx := &openAIWSIngressContext{failureStreak: 5}
	highFailCtx.setLastUsedAt(now.Add(-5 * time.Minute))
	scoreHighFail, _, ok := scoreOpenAIWSIngressMigrationCandidate(highFailCtx, now, nil)
	require.True(t, ok)
	// 5*12=60 but capped at 40
	require.InDelta(t, 40.0, scoreClean-scoreHighFail, 0.5, "failure streak penalty should cap at 40")

	// Recent failure (within 2 min): -18
	recentFailCtx := &openAIWSIngressContext{lastFailureAt: now.Add(-30 * time.Second)}
	recentFailCtx.setLastUsedAt(now.Add(-5 * time.Minute))
	scoreRecentFail, _, ok := scoreOpenAIWSIngressMigrationCandidate(recentFailCtx, now, nil)
	require.True(t, ok)
	require.InDelta(t, 18.0, scoreClean-scoreRecentFail, 0.5, "recent failure should subtract 18")

	// Old failure (>2 min): no penalty
	oldFailCtx := &openAIWSIngressContext{lastFailureAt: now.Add(-5 * time.Minute)}
	oldFailCtx.setLastUsedAt(now.Add(-5 * time.Minute))
	scoreOldFail, _, ok := scoreOpenAIWSIngressMigrationCandidate(oldFailCtx, now, nil)
	require.True(t, ok)
	require.InDelta(t, scoreClean, scoreOldFail, 0.5, "old failure should have no penalty")
}

func TestScoreOpenAIWSIngressMigrationCandidate_MigrationPenalties(t *testing.T) {
	now := time.Now()

	cleanCtx := &openAIWSIngressContext{}
	cleanCtx.setLastUsedAt(now.Add(-5 * time.Minute))
	scoreClean, _, _ := scoreOpenAIWSIngressMigrationCandidate(cleanCtx, now, nil)

	// Recent migration (within 1 min): -10
	recentMigCtx := &openAIWSIngressContext{lastMigrationAt: now.Add(-30 * time.Second)}
	recentMigCtx.setLastUsedAt(now.Add(-5 * time.Minute))
	scoreRecentMig, _, ok := scoreOpenAIWSIngressMigrationCandidate(recentMigCtx, now, nil)
	require.True(t, ok)
	require.InDelta(t, 10.0, scoreClean-scoreRecentMig, 0.5, "recent migration should subtract 10")

	// High migration count (capped at 20)
	highMigCtx := &openAIWSIngressContext{migrationCount: 6}
	highMigCtx.setLastUsedAt(now.Add(-5 * time.Minute))
	scoreHighMig, _, ok := scoreOpenAIWSIngressMigrationCandidate(highMigCtx, now, nil)
	require.True(t, ok)
	// 6*4=24 but capped at 20
	require.InDelta(t, 20.0, scoreClean-scoreHighMig, 0.5, "migration count penalty should cap at 20")
}

func TestScoreOpenAIWSIngressMigrationCandidate_CombinedPenalties(t *testing.T) {
	now := time.Now()
	// All penalties combined: broken(-30) + failStreak 1*12(-12) + recentFail(-18) + recentMig(-10) + migCount 1*4(-4) + recentIdle(-15)
	worstCtx := &openAIWSIngressContext{
		broken:          true,
		failureStreak:   1,
		lastFailureAt:   now.Add(-30 * time.Second),
		migrationCount:  1,
		lastMigrationAt: now.Add(-30 * time.Second),
	}
	worstCtx.setLastUsedAt(now.Add(-5 * time.Second))
	score, _, ok := scoreOpenAIWSIngressMigrationCandidate(worstCtx, now, nil)
	require.True(t, ok)
	expected := 100.0 - 30.0 - 12.0 - 18.0 - 10.0 - 4.0 - 15.0 // = 11.0
	require.InDelta(t, expected, score, 0.5, "all penalties should stack correctly")
}

func TestOpenAIWSIngressContextPool_MigrationBlockedByCircuitBreaker(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 60

	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	stats := newOpenAIAccountRuntimeStats()
	pool.schedulerStats = stats

	accountID := int64(9004)

	// Trip circuit breaker for this account.
	for i := 0; i < defaultCircuitBreakerFailThreshold+1; i++ {
		stats.report(accountID, false, nil, "", 0)
	}
	require.True(t, stats.isCircuitOpen(accountID))

	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{
			&openAIWSCaptureConn{},
		},
	}
	pool.setClientDialerForTest(dialer)

	account := &Account{ID: accountID, Concurrency: 1}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// Acquire the only slot.
	lease1, err := pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
		Account:     account,
		GroupID:     30,
		SessionHash: "session_cb_a",
		OwnerID:     "owner_a",
		WSURL:       "ws://test-upstream",
	})
	require.NoError(t, err)
	require.NotNil(t, lease1)
	lease1.Release()

	// Now try a different session -- migration should fail because the only
	// candidate context is on a circuit-open account, whose score will be
	// below the minimum threshold.
	_, err = pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
		Account:     account,
		GroupID:     30,
		SessionHash: "session_cb_b",
		OwnerID:     "owner_b",
		WSURL:       "ws://test-upstream",
	})
	require.ErrorIs(t, err, errOpenAIWSConnQueueFull,
		"migration to a circuit-open account should be blocked")
}

// ---------- 连接生命周期管理（超龄轮换）测试 ----------

func TestOpenAIWSIngressContext_UpstreamConnAge_ZeroValue(t *testing.T) {
	ctx := &openAIWSIngressContext{}
	// 未设置 createdAt 时，connAge 应返回 0
	require.Equal(t, time.Duration(0), ctx.upstreamConnAge(time.Now()))
}

func TestOpenAIWSIngressContext_UpstreamConnAge_Normal(t *testing.T) {
	ctx := &openAIWSIngressContext{}
	past := time.Now().Add(-10 * time.Minute)
	ctx.upstreamConnCreatedAt.Store(past.UnixNano())
	age := ctx.upstreamConnAge(time.Now())
	require.True(t, age >= 10*time.Minute-time.Second, "connAge 应约为 10 分钟，实际=%v", age)
	require.True(t, age < 11*time.Minute, "connAge 不应过大，实际=%v", age)
}

func TestOpenAIWSIngressContext_UpstreamConnAge_NilSafe(t *testing.T) {
	var ctx *openAIWSIngressContext
	require.Equal(t, time.Duration(0), ctx.upstreamConnAge(time.Now()))
}

func TestOpenAIWSIngressContext_UpstreamConnAge_ClockSkew(t *testing.T) {
	ctx := &openAIWSIngressContext{}
	future := time.Now().Add(10 * time.Minute)
	ctx.upstreamConnCreatedAt.Store(future.UnixNano())
	// now 早于 createdAt（时钟回拨），应返回 0
	require.Equal(t, time.Duration(0), ctx.upstreamConnAge(time.Now()))
}

func TestNewOpenAIWSIngressContextPool_UpstreamMaxAge_ZeroDisablesRotation(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.UpstreamConnMaxAgeSeconds = 0

	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	require.Equal(t, time.Duration(0), pool.upstreamMaxAge)
}

func TestOpenAIWSIngressContextPool_EnsureUpstream_MaxAgeRotate(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 600

	pool := newOpenAIWSIngressContextPool(cfg)
	pool.upstreamMaxAge = 1 * time.Second // 设置极短的 maxAge 以便测试
	defer pool.Close()

	conn1 := &openAIWSCaptureConn{}
	conn2 := &openAIWSCaptureConn{}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{conn1, conn2},
	}
	pool.setClientDialerForTest(dialer)

	account := &Account{ID: 901, Concurrency: 2}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// 第一次 Acquire：建连
	lease1, err := pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
		Account:     account,
		GroupID:     1,
		SessionHash: "session_age",
		OwnerID:     "owner_age_1",
		WSURL:       "ws://test-upstream",
	})
	require.NoError(t, err)
	require.NotNil(t, lease1)
	require.Equal(t, 1, dialer.DialCount(), "首次 Acquire 应 dial 一次")

	// Yield 保留连接
	lease1.Yield()

	// 等待超过 maxAge
	time.Sleep(1200 * time.Millisecond)

	// 第二次 Acquire：应触发超龄轮换
	lease2, err := pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
		Account:     account,
		GroupID:     1,
		SessionHash: "session_age",
		OwnerID:     "owner_age_2",
		WSURL:       "ws://test-upstream",
	})
	require.NoError(t, err)
	require.NotNil(t, lease2)
	require.Equal(t, 2, dialer.DialCount(), "超龄轮换应触发重新 dial")
	require.True(t, conn1.closed, "旧连接应被关闭")
	lease2.Release()
}

func TestOpenAIWSIngressContextPool_EnsureUpstream_YoungConnNotRotated(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 600

	pool := newOpenAIWSIngressContextPool(cfg)
	pool.upstreamMaxAge = 10 * time.Minute // 远大于测试时间
	defer pool.Close()

	conn1 := &openAIWSCaptureConn{}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{conn1},
	}
	pool.setClientDialerForTest(dialer)

	account := &Account{ID: 902, Concurrency: 2}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	lease1, err := pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
		Account:     account,
		GroupID:     1,
		SessionHash: "session_young",
		OwnerID:     "owner_young_1",
		WSURL:       "ws://test-upstream",
	})
	require.NoError(t, err)
	lease1.Yield()

	// 立即重新 Acquire：连接年轻，不应轮换
	lease2, err := pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
		Account:     account,
		GroupID:     1,
		SessionHash: "session_young",
		OwnerID:     "owner_young_2",
		WSURL:       "ws://test-upstream",
	})
	require.NoError(t, err)
	require.NotNil(t, lease2)
	require.Equal(t, 1, dialer.DialCount(), "年轻连接不应触发重新 dial")
	require.True(t, lease2.Reused(), "年轻连接应复用")
	require.False(t, conn1.closed, "年轻连接不应被关闭")
	lease2.Release()
}

func TestOpenAIWSIngressContextPool_CloseAgedIdleUpstreams_ClosesAgedIdle(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 600

	pool := newOpenAIWSIngressContextPool(cfg)
	pool.upstreamMaxAge = 1 * time.Second
	defer pool.Close()

	conn1 := &openAIWSCaptureConn{}
	ap := &openAIWSIngressAccountPool{
		contexts:  make(map[string]*openAIWSIngressContext),
		bySession: make(map[string]string),
	}

	ctx := &openAIWSIngressContext{
		id:        "ctx_aged_1",
		accountID: 903,
		upstream:  conn1,
	}
	// 设 createdAt 为 2 秒前
	ctx.upstreamConnCreatedAt.Store(time.Now().Add(-2 * time.Second).UnixNano())
	ctx.touchLease(time.Now(), pool.idleTTL)
	ap.contexts["ctx_aged_1"] = ctx

	now := time.Now()
	ap.mu.Lock()
	toClose := pool.closeAgedIdleUpstreamsLocked(ap, now)
	ap.mu.Unlock()

	require.Len(t, toClose, 1, "应关闭超龄空闲连接")
	closeOpenAIWSClientConns(toClose)
	require.True(t, conn1.closed)

	// upstream 应已清空
	ctx.mu.Lock()
	require.Nil(t, ctx.upstream)
	require.Equal(t, int64(0), ctx.upstreamConnCreatedAt.Load())
	ctx.mu.Unlock()
}

func TestOpenAIWSIngressContextPool_CloseAgedIdleUpstreams_SkipsOwnedContext(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 600

	pool := newOpenAIWSIngressContextPool(cfg)
	pool.upstreamMaxAge = 1 * time.Second
	defer pool.Close()

	conn1 := &openAIWSCaptureConn{}
	ap := &openAIWSIngressAccountPool{
		contexts:  make(map[string]*openAIWSIngressContext),
		bySession: make(map[string]string),
	}

	ctx := &openAIWSIngressContext{
		id:        "ctx_owned_1",
		accountID: 904,
		ownerID:   "active_owner",
		upstream:  conn1,
	}
	ctx.upstreamConnCreatedAt.Store(time.Now().Add(-2 * time.Second).UnixNano())
	ap.contexts["ctx_owned_1"] = ctx

	now := time.Now()
	ap.mu.Lock()
	toClose := pool.closeAgedIdleUpstreamsLocked(ap, now)
	ap.mu.Unlock()

	require.Len(t, toClose, 0, "有 owner 的超龄连接不应被关闭")
	require.False(t, conn1.closed)
}

func TestOpenAIWSIngressContextPool_CloseAgedIdleUpstreams_SkipsYoungConn(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 600

	pool := newOpenAIWSIngressContextPool(cfg)
	pool.upstreamMaxAge = 10 * time.Minute
	defer pool.Close()

	conn1 := &openAIWSCaptureConn{}
	ap := &openAIWSIngressAccountPool{
		contexts:  make(map[string]*openAIWSIngressContext),
		bySession: make(map[string]string),
	}

	ctx := &openAIWSIngressContext{
		id:        "ctx_young_1",
		accountID: 905,
		upstream:  conn1,
	}
	ctx.upstreamConnCreatedAt.Store(time.Now().UnixNano())
	ctx.touchLease(time.Now(), pool.idleTTL)
	ap.contexts["ctx_young_1"] = ctx

	now := time.Now()
	ap.mu.Lock()
	toClose := pool.closeAgedIdleUpstreamsLocked(ap, now)
	ap.mu.Unlock()

	require.Len(t, toClose, 0, "年轻连接不应被关闭")
	require.False(t, conn1.closed)
}

func TestOpenAIWSIngressContextPool_E2E_AcquireYieldAgedReconnect(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 600

	pool := newOpenAIWSIngressContextPool(cfg)
	pool.upstreamMaxAge = 55 * time.Minute // 使用实际默认值
	defer pool.Close()

	conn1 := &openAIWSCaptureConn{}
	conn2 := &openAIWSCaptureConn{}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{conn1, conn2},
	}
	pool.setClientDialerForTest(dialer)

	account := &Account{ID: 906, Concurrency: 2}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Acquire → Yield
	lease1, err := pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
		Account:     account,
		GroupID:     1,
		SessionHash: "session_e2e",
		OwnerID:     "owner_e2e_1",
		WSURL:       "ws://test-upstream",
	})
	require.NoError(t, err)
	require.NotNil(t, lease1)
	lease1.Yield()

	// 手动设置 createdAt 为 56 分钟前以模拟超龄
	pool.mu.Lock()
	ap := pool.accounts[account.ID]
	pool.mu.Unlock()
	require.NotNil(t, ap)

	ap.mu.Lock()
	for _, c := range ap.contexts {
		c.upstreamConnCreatedAt.Store(time.Now().Add(-56 * time.Minute).UnixNano())
	}
	ap.mu.Unlock()

	// 重新 Acquire：应检测到超龄并重连
	lease2, err := pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
		Account:     account,
		GroupID:     1,
		SessionHash: "session_e2e",
		OwnerID:     "owner_e2e_2",
		WSURL:       "ws://test-upstream",
	})
	require.NoError(t, err)
	require.NotNil(t, lease2)
	require.Equal(t, 2, dialer.DialCount(), "超龄 56 分钟的连接应触发重连")
	require.True(t, conn1.closed, "旧的超龄连接应被关闭")
	lease2.Release()
}

// 回归测试：容量满 + 存在过期 context 时，Acquire 仍能通过 evict 腾出空间后正常分配。
func TestOpenAIWSIngressContextPool_Acquire_EvictsExpiredWhenFull(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 60

	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	conn1 := &openAIWSCaptureConn{}
	conn2 := &openAIWSCaptureConn{}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{conn1, conn2},
	}
	pool.setClientDialerForTest(dialer)

	account := &Account{ID: 901, Concurrency: 1}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// 获取一个 lease 占满容量
	lease1, err := pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
		Account:     account,
		GroupID:     3,
		SessionHash: "session_old",
		OwnerID:     "owner_old",
		WSURL:       "ws://test-upstream",
	})
	require.NoError(t, err)
	require.NotNil(t, lease1)
	lease1.Release()

	// 手动令该 context 过期
	pool.mu.Lock()
	ap := pool.accounts[account.ID]
	pool.mu.Unlock()
	ap.mu.Lock()
	for _, c := range ap.contexts {
		c.setExpiresAt(time.Now().Add(-2 * time.Second))
	}
	ap.mu.Unlock()

	// 容量满（1个过期 context），新 session 的 Acquire 应通过 evict 成功
	lease2, err := pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
		Account:     account,
		GroupID:     3,
		SessionHash: "session_new",
		OwnerID:     "owner_new",
		WSURL:       "ws://test-upstream",
	})
	require.NoError(t, err, "容量满但有过期 context 时 Acquire 应成功")
	require.NotNil(t, lease2)
	lease2.Release()
}

// 回归测试：Acquire 找到已过期但仍在 bySession 映射中的 context 时，能正确取得所有权并刷新租约。
func TestOpenAIWSIngressContextPool_Acquire_ReusesExpiredContextBySession(t *testing.T) {
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 60

	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	conn1 := &openAIWSCaptureConn{}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{conn1},
	}
	pool.setClientDialerForTest(dialer)

	account := &Account{ID: 902, Concurrency: 2}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	// 第一次获取 context
	lease1, err := pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
		Account:     account,
		GroupID:     4,
		SessionHash: "session_reuse",
		OwnerID:     "owner_1",
		WSURL:       "ws://test-upstream",
	})
	require.NoError(t, err)
	require.NotNil(t, lease1)
	lease1.Release()

	// 令 context 过期（但不清理，模拟热路径不再清理的行为）
	pool.mu.Lock()
	ap := pool.accounts[account.ID]
	pool.mu.Unlock()
	ap.mu.Lock()
	for _, c := range ap.contexts {
		c.setExpiresAt(time.Now().Add(-1 * time.Second))
	}
	ap.mu.Unlock()

	// 同 session 再次 Acquire，应能复用过期但未清理的 context
	lease2, err := pool.Acquire(ctx, openAIWSIngressContextAcquireRequest{
		Account:     account,
		GroupID:     4,
		SessionHash: "session_reuse",
		OwnerID:     "owner_2",
		WSURL:       "ws://test-upstream",
	})
	require.NoError(t, err, "过期但未清理的 context 应被同 session 的 Acquire 复用")
	require.NotNil(t, lease2)

	// 验证租约已刷新（expiresAt 应在未来）
	pool.mu.Lock()
	ap2 := pool.accounts[account.ID]
	pool.mu.Unlock()
	ap2.mu.Lock()
	for _, c := range ap2.contexts {
		c.mu.Lock()
		ea := c.expiresAt()
		c.mu.Unlock()
		require.True(t, ea.After(time.Now()), "复用后租约应被刷新到未来")
	}
	ap2.mu.Unlock()

	lease2.Release()
}

// === P3: 后台主动 Ping 检测测试 ===

func TestOpenAIWSIngressContextPool_PingIdleUpstreams_MarksBrokenOnPingFailure(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 600
	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	failConn := &openAIWSPingFailConn{}
	ap := &openAIWSIngressAccountPool{
		contexts:  make(map[string]*openAIWSIngressContext),
		bySession: make(map[string]string),
	}
	ctx := &openAIWSIngressContext{
		id:        "ctx_ping_fail_1",
		accountID: 2001,
		upstream:  failConn,
	}
	ctx.touchLease(time.Now(), pool.idleTTL)
	ap.contexts["ctx_ping_fail_1"] = ctx

	pool.pingIdleUpstreams(ap)

	ctx.mu.Lock()
	broken := ctx.broken
	streak := ctx.failureStreak
	ctx.mu.Unlock()
	require.True(t, broken, "Ping 失败应标记 context 为 broken")
	require.Equal(t, 1, streak, "Ping 失败应增加 failureStreak")
}

func TestOpenAIWSIngressContextPool_PingIdleUpstreams_SkipsOwnedContext(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 600
	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	failConn := &openAIWSPingFailConn{}
	ap := &openAIWSIngressAccountPool{
		contexts:  make(map[string]*openAIWSIngressContext),
		bySession: make(map[string]string),
	}
	ctx := &openAIWSIngressContext{
		id:        "ctx_ping_owned_1",
		accountID: 2002,
		ownerID:   "active_owner",
		upstream:  failConn,
	}
	ctx.touchLease(time.Now(), pool.idleTTL)
	ap.contexts["ctx_ping_owned_1"] = ctx

	pool.pingIdleUpstreams(ap)

	ctx.mu.Lock()
	broken := ctx.broken
	ctx.mu.Unlock()
	require.False(t, broken, "有 owner 的 context 不应被 Ping 探测")
}

func TestOpenAIWSIngressContextPool_PingIdleUpstreams_SkipsBrokenContext(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 600
	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	failConn := &openAIWSPingFailConn{}
	ap := &openAIWSIngressAccountPool{
		contexts:  make(map[string]*openAIWSIngressContext),
		bySession: make(map[string]string),
	}
	ctx := &openAIWSIngressContext{
		id:        "ctx_ping_broken_1",
		accountID: 2003,
		upstream:  failConn,
		broken:    true,
	}
	ctx.touchLease(time.Now(), pool.idleTTL)
	ap.contexts["ctx_ping_broken_1"] = ctx

	pool.pingIdleUpstreams(ap)

	ctx.mu.Lock()
	streak := ctx.failureStreak
	ctx.mu.Unlock()
	require.Equal(t, 0, streak, "已 broken 的 context 不应被再次 Ping")
}

func TestOpenAIWSIngressContextPool_PingIdleUpstreams_HealthyConnStaysHealthy(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 600
	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	healthyConn := &openAIWSCaptureConn{}
	ap := &openAIWSIngressAccountPool{
		contexts:  make(map[string]*openAIWSIngressContext),
		bySession: make(map[string]string),
	}
	ctx := &openAIWSIngressContext{
		id:        "ctx_ping_healthy_1",
		accountID: 2004,
		upstream:  healthyConn,
	}
	ctx.touchLease(time.Now(), pool.idleTTL)
	ap.contexts["ctx_ping_healthy_1"] = ctx

	pool.pingIdleUpstreams(ap)

	ctx.mu.Lock()
	broken := ctx.broken
	hasUpstream := ctx.upstream != nil
	ctx.mu.Unlock()
	require.False(t, broken, "Ping 成功的 context 不应被标记 broken")
	require.True(t, hasUpstream, "Ping 成功的 upstream 应保持")
}

func TestOpenAIWSIngressContextPool_SweepTriggersPingOnIdleContexts(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 600
	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	failConn := &openAIWSPingFailConn{}
	healthyConn := &openAIWSCaptureConn{}

	pool.mu.Lock()
	ap := pool.getOrCreateAccountPoolLocked(3001)
	pool.mu.Unlock()

	ap.mu.Lock()
	ctxFail := &openAIWSIngressContext{
		id:        "ctx_sweep_ping_fail",
		accountID: 3001,
		upstream:  failConn,
	}
	ctxFail.touchLease(time.Now(), pool.idleTTL)
	ap.contexts["ctx_sweep_ping_fail"] = ctxFail

	ctxOk := &openAIWSIngressContext{
		id:        "ctx_sweep_ping_ok",
		accountID: 3001,
		upstream:  healthyConn,
	}
	ctxOk.touchLease(time.Now(), pool.idleTTL)
	ap.contexts["ctx_sweep_ping_ok"] = ctxOk
	ap.dynamicCap.Store(2)
	ap.mu.Unlock()

	// 手动触发 sweep
	pool.sweepExpiredIdleContexts()

	ctxFail.mu.Lock()
	failBroken := ctxFail.broken
	ctxFail.mu.Unlock()
	require.True(t, failBroken, "sweep 后 Ping 失败的空闲 context 应被标记 broken")

	ctxOk.mu.Lock()
	okBroken := ctxOk.broken
	ctxOk.mu.Unlock()
	require.False(t, okBroken, "sweep 后 Ping 成功的空闲 context 应保持健康")
}

func TestOpenAIWSIngressContextPool_YieldSchedulesDelayedPing(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 600
	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	failConn := &openAIWSPingFailConn{}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{failConn},
	}
	pool.setClientDialerForTest(dialer)

	account := &Account{ID: 4001, Concurrency: 2}
	bCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	lease, err := pool.Acquire(bCtx, openAIWSIngressContextAcquireRequest{
		Account:     account,
		GroupID:     1,
		SessionHash: "session_yield_ping",
		OwnerID:     "owner_yield_ping",
		WSURL:       "ws://test-upstream",
	})
	require.NoError(t, err)

	ingressCtx := lease.context
	// Yield 触发延迟 Ping
	lease.Yield()

	// 等待延迟 Ping 执行完毕（默认 5s + 余量）
	time.Sleep(6 * time.Second)

	ingressCtx.mu.Lock()
	broken := ingressCtx.broken
	ingressCtx.mu.Unlock()
	require.True(t, broken, "Yield 后延迟 Ping 应检测到 PingFailConn 并标记 broken")
}

func TestOpenAIWSIngressContextPool_ScheduleDelayedPing_CancelledOnPoolClose(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 600
	pool := newOpenAIWSIngressContextPool(cfg)

	failConn := &openAIWSPingFailConn{}
	ctx := &openAIWSIngressContext{
		id:        "ctx_delayed_cancel_1",
		accountID: 5001,
		upstream:  failConn,
	}
	ctx.touchLease(time.Now(), pool.idleTTL)

	// 安排 10s 延迟（远大于测试等待时间）
	pool.scheduleDelayedPing(ctx, 10*time.Second)

	// 立刻关闭 pool，应取消延迟 Ping
	pool.Close()
	time.Sleep(200 * time.Millisecond)

	ctx.mu.Lock()
	broken := ctx.broken
	ctx.mu.Unlock()
	require.False(t, broken, "pool 关闭后延迟 Ping 不应执行")
}

// === effectiveDynamicCapacity 边界测试 ===

func TestOpenAIWSIngressContextPool_EffectiveDynamicCapacity_NilAccountPool(t *testing.T) {
	t.Parallel()
	pool := &openAIWSIngressContextPool{}
	require.Equal(t, 4, pool.effectiveDynamicCapacity(nil, 4), "ap==nil 时应返回 hardCap")
	require.Equal(t, 0, pool.effectiveDynamicCapacity(nil, 0), "ap==nil && hardCap==0")
}

func TestOpenAIWSIngressContextPool_EffectiveDynamicCapacity_ZeroHardCap(t *testing.T) {
	t.Parallel()
	pool := &openAIWSIngressContextPool{}
	ap := &openAIWSIngressAccountPool{}
	ap.dynamicCap.Store(3)
	require.Equal(t, 0, pool.effectiveDynamicCapacity(ap, 0), "hardCap<=0 应返回 hardCap")
	require.Equal(t, -1, pool.effectiveDynamicCapacity(ap, -1), "hardCap<0 应返回 hardCap")
}

func TestOpenAIWSIngressContextPool_EffectiveDynamicCapacity_DynCapBelowOne(t *testing.T) {
	t.Parallel()
	pool := &openAIWSIngressContextPool{}
	ap := &openAIWSIngressAccountPool{}
	ap.dynamicCap.Store(0) // 异常值
	result := pool.effectiveDynamicCapacity(ap, 4)
	require.Equal(t, 1, result, "dynCap<1 应自动修复为 1")
	require.Equal(t, int32(1), ap.dynamicCap.Load(), "dynCap 应被修复为 1")
}

func TestOpenAIWSIngressContextPool_EffectiveDynamicCapacity_DynCapExceedsHardCap(t *testing.T) {
	t.Parallel()
	pool := &openAIWSIngressContextPool{}
	ap := &openAIWSIngressAccountPool{}
	ap.dynamicCap.Store(10)
	require.Equal(t, 4, pool.effectiveDynamicCapacity(ap, 4), "dynCap>hardCap 应 clamp 到 hardCap")
}

func TestOpenAIWSIngressContextPool_EffectiveDynamicCapacity_DynCapEqualsHardCap(t *testing.T) {
	t.Parallel()
	pool := &openAIWSIngressContextPool{}
	ap := &openAIWSIngressAccountPool{}
	ap.dynamicCap.Store(4)
	require.Equal(t, 4, pool.effectiveDynamicCapacity(ap, 4), "dynCap==hardCap 应返回 hardCap")
}

func TestOpenAIWSIngressContextPool_EffectiveDynamicCapacity_NormalPath(t *testing.T) {
	t.Parallel()
	pool := &openAIWSIngressContextPool{}
	ap := &openAIWSIngressAccountPool{}
	ap.dynamicCap.Store(2)
	require.Equal(t, 2, pool.effectiveDynamicCapacity(ap, 8), "正常 dynCap<hardCap 应返回 dynCap")
}

// === 动态容量增长/缩容测试 ===

func TestOpenAIWSIngressContextPool_Acquire_DynamicCapGrows(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 600
	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{
			&openAIWSCaptureConn{}, &openAIWSCaptureConn{}, &openAIWSCaptureConn{},
		},
	}
	pool.setClientDialerForTest(dialer)

	account := &Account{ID: 6001, Concurrency: 4}
	bCtx := context.Background()

	// 第一次 Acquire：dynamicCap 初始为 1，创建第一个 context
	lease1, err := pool.Acquire(bCtx, openAIWSIngressContextAcquireRequest{
		Account: account, GroupID: 1, SessionHash: "s1", OwnerID: "o1", WSURL: "ws://t",
	})
	require.NoError(t, err)

	pool.mu.Lock()
	ap := pool.accounts[account.ID]
	pool.mu.Unlock()
	require.Equal(t, int32(1), ap.dynamicCap.Load(), "初始 dynamicCap 应为 1")

	// 第二次 Acquire (不同 session)：dynCap=1 < capacity=4，应增长
	lease2, err := pool.Acquire(bCtx, openAIWSIngressContextAcquireRequest{
		Account: account, GroupID: 1, SessionHash: "s2", OwnerID: "o2", WSURL: "ws://t",
	})
	require.NoError(t, err)
	require.True(t, ap.dynamicCap.Load() >= 2, "第二次 Acquire 应触发 dynamicCap 增长")

	lease1.Release()
	lease2.Release()
}

func TestOpenAIWSIngressContextPool_Sweeper_ShrinksDynamicCap(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 1 // 1 秒 TTL 让 context 快速过期
	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{
			&openAIWSCaptureConn{}, &openAIWSCaptureConn{}, &openAIWSCaptureConn{},
		},
	}
	pool.setClientDialerForTest(dialer)

	account := &Account{ID: 6002, Concurrency: 4}
	bCtx := context.Background()

	// 创建两个 context
	lease1, _ := pool.Acquire(bCtx, openAIWSIngressContextAcquireRequest{
		Account: account, GroupID: 1, SessionHash: "s1", OwnerID: "o1", WSURL: "ws://t",
	})
	lease2, _ := pool.Acquire(bCtx, openAIWSIngressContextAcquireRequest{
		Account: account, GroupID: 1, SessionHash: "s2", OwnerID: "o2", WSURL: "ws://t",
	})
	lease1.Release()
	lease2.Release()

	pool.mu.Lock()
	ap := pool.accounts[account.ID]
	pool.mu.Unlock()
	require.True(t, ap.dynamicCap.Load() >= 2)

	// 等待 context 过期
	time.Sleep(2 * time.Second)

	// 手动 sweep
	pool.sweepExpiredIdleContexts()

	// sweep 后 dynamicCap 应缩减
	ap.mu.Lock()
	ctxCount := len(ap.contexts)
	ap.mu.Unlock()
	dynCap := ap.dynamicCap.Load()
	// 如果所有 context 都被 evict，dynamicCap 应缩到 1（min）
	if ctxCount == 0 {
		require.Equal(t, int32(1), dynCap, "context 全部 evict 后 dynamicCap 应缩至 1")
	} else {
		require.LessOrEqual(t, dynCap, int32(ctxCount), "dynamicCap 应缩至当前 context 数")
	}
}

// === Ping 额外边界测试 ===

func TestOpenAIWSIngressContextPool_PingContextUpstream_NilPoolOrContext(t *testing.T) {
	t.Parallel()
	pool := &openAIWSIngressContextPool{stopCh: make(chan struct{})}
	// nil context 不应 panic
	pool.pingContextUpstream(nil)
	// nil pool 不应 panic
	var nilPool *openAIWSIngressContextPool
	nilPool.pingContextUpstream(&openAIWSIngressContext{})
}

func TestOpenAIWSIngressContextPool_PingContextUpstream_SkipsDialingContext(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	failConn := &openAIWSPingFailConn{}
	ctx := &openAIWSIngressContext{
		id: "ctx_dialing_1", accountID: 7001,
		upstream: failConn, dialing: true,
	}
	pool.pingContextUpstream(ctx)
	ctx.mu.Lock()
	require.False(t, ctx.broken, "dialing 中的 context 不应被 Ping")
	ctx.mu.Unlock()
}

func TestOpenAIWSIngressContextPool_PingContextUpstream_SkipsNoUpstream(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	ctx := &openAIWSIngressContext{
		id: "ctx_no_upstream", accountID: 7002, upstream: nil,
	}
	pool.pingContextUpstream(ctx)
	ctx.mu.Lock()
	require.False(t, ctx.broken, "无 upstream 的 context 不应被 Ping")
	ctx.mu.Unlock()
}

func TestOpenAIWSIngressContextPool_PingIdleUpstreams_NilPoolOrAP(t *testing.T) {
	t.Parallel()
	pool := &openAIWSIngressContextPool{stopCh: make(chan struct{})}
	pool.pingIdleUpstreams(nil)
	var nilPool *openAIWSIngressContextPool
	nilPool.pingIdleUpstreams(&openAIWSIngressAccountPool{})
}

func TestOpenAIWSIngressContextPool_PingIdleUpstreams_SkipsNilContext(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	ap := &openAIWSIngressAccountPool{
		contexts:  map[string]*openAIWSIngressContext{"nil_ctx": nil},
		bySession: make(map[string]string),
	}
	// 不应 panic
	pool.pingIdleUpstreams(ap)
}

func TestOpenAIWSIngressContextPool_ScheduleDelayedPing_ZeroDelay(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	failConn := &openAIWSPingFailConn{}
	ctx := &openAIWSIngressContext{
		id: "ctx_zero_delay", accountID: 8001, upstream: failConn,
	}
	// delay <= 0 应为 no-op
	pool.scheduleDelayedPing(ctx, 0)
	pool.scheduleDelayedPing(ctx, -1*time.Second)
	time.Sleep(100 * time.Millisecond)
	ctx.mu.Lock()
	require.False(t, ctx.broken, "delay<=0 不应触发 Ping")
	ctx.mu.Unlock()
}

func TestOpenAIWSIngressContextPool_ScheduleDelayedPing_NilParams(t *testing.T) {
	t.Parallel()
	pool := &openAIWSIngressContextPool{stopCh: make(chan struct{})}
	// nil context
	pool.scheduleDelayedPing(nil, 5*time.Second)
	// nil pool
	var nilPool *openAIWSIngressContextPool
	nilPool.scheduleDelayedPing(&openAIWSIngressContext{}, 5*time.Second)
}

// === P1 并发回归：旧连接 Ping 失败不应误杀新连接 ===

func TestOpenAIWSIngressContextPool_PingFailDoesNotKillNewConn(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 600
	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	// 旧连接：Ping 带 200ms 延迟后失败
	oldConn := newOpenAIWSDelayedPingFailConn(200 * time.Millisecond)
	// 新连接：正常的 Ping
	newConn := &openAIWSCaptureConn{}

	ctx := &openAIWSIngressContext{
		id:             "ctx_race_test",
		accountID:      9001,
		upstream:       oldConn,
		upstreamConnID: "old_conn_1",
	}
	ctx.touchLease(time.Now(), pool.idleTTL)

	// 在后台对旧连接发起 Ping 探测
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		pool.pingContextUpstream(ctx)
	}()

	// 等待 Ping 开始执行
	<-oldConn.pingDone

	// 模拟连接重建：在 Ping 执行期间将 upstream 替换为新连接
	ctx.mu.Lock()
	ctx.upstream = newConn
	ctx.upstreamConnID = "new_conn_2"
	ctx.broken = false
	ctx.failureStreak = 0
	ctx.mu.Unlock()

	// 等待 Ping goroutine 完成
	wg.Wait()

	// 验证：新连接不应被标记为 broken
	ctx.mu.Lock()
	broken := ctx.broken
	streak := ctx.failureStreak
	upstream := ctx.upstream
	connID := ctx.upstreamConnID
	ctx.mu.Unlock()

	require.False(t, broken, "新连接不应被旧 Ping 失败标记为 broken")
	require.Equal(t, 0, streak, "failureStreak 不应增加")
	require.Equal(t, newConn, upstream, "upstream 应仍是新连接")
	require.Equal(t, "new_conn_2", connID, "upstreamConnID 应仍是新连接的 ID")
	require.False(t, newConn.Closed(), "新连接不应被关闭")
}

func TestOpenAIWSIngressContextPool_PingFailKillsSameConn(t *testing.T) {
	// 对照测试：connID 未变时应正常标记 broken
	t.Parallel()
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 600
	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	failConn := &openAIWSPingFailConn{}
	ctx := &openAIWSIngressContext{
		id:             "ctx_same_conn",
		accountID:      9002,
		upstream:       failConn,
		upstreamConnID: "conn_1",
	}
	ctx.touchLease(time.Now(), pool.idleTTL)

	pool.pingContextUpstream(ctx)

	ctx.mu.Lock()
	broken := ctx.broken
	streak := ctx.failureStreak
	connID := ctx.upstreamConnID
	ctx.mu.Unlock()

	require.True(t, broken, "同一连接 Ping 失败应标记 broken")
	require.Equal(t, 1, streak, "failureStreak 应为 1")
	require.Empty(t, connID, "upstreamConnID 应被清空")
}

func TestOpenAIWSIngressContextPool_PingFailDoesNotKillBusyConn(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 600
	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	failConn := newOpenAIWSDelayedPingFailConn(200 * time.Millisecond)
	ctx := &openAIWSIngressContext{
		id:             "ctx_busy_conn",
		accountID:      9005,
		upstream:       failConn,
		upstreamConnID: "conn_busy_1",
	}
	ctx.touchLease(time.Now(), pool.idleTTL)

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		pool.pingContextUpstream(ctx)
	}()

	<-failConn.pingDone

	ctx.mu.Lock()
	ctx.ownerID = "active_owner"
	ctx.mu.Unlock()

	wg.Wait()

	ctx.mu.Lock()
	broken := ctx.broken
	streak := ctx.failureStreak
	upstream := ctx.upstream
	connID := ctx.upstreamConnID
	ownerID := ctx.ownerID
	ctx.mu.Unlock()

	require.False(t, broken, "busy context 不应被后台 Ping 标记 broken")
	require.Equal(t, 0, streak, "failureStreak 不应增加")
	require.Equal(t, failConn, upstream, "busy context 的 upstream 不应被替换")
	require.Equal(t, "conn_busy_1", connID, "busy context 的 connID 不应变化")
	require.Equal(t, "active_owner", ownerID, "owner 应保持不变")
	require.False(t, failConn.Closed(), "busy context 的连接不应被后台 Ping 关闭")
}

// === P2a 去重：连续多次 Yield 仅触发一次延迟 Ping ===

func TestOpenAIWSIngressContextPool_ConsecutiveYieldsOnlyOnePing(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 600
	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	// 使用 Ping 失败连接以便观察是否被标记 broken
	failConn := &openAIWSPingFailConn{}
	ctx := &openAIWSIngressContext{
		id:             "ctx_yield_dedup",
		accountID:      9003,
		upstream:       failConn,
		upstreamConnID: "conn_dedup_1",
	}
	ctx.touchLease(time.Now(), pool.idleTTL)

	// 连续调用 5 次 scheduleDelayedPing
	for i := 0; i < 5; i++ {
		pool.scheduleDelayedPing(ctx, 100*time.Millisecond)
	}

	// 验证：只有一个 pendingPingTimer（不应堆积多个 goroutine）
	ctx.mu.Lock()
	hasPending := ctx.pendingPingTimer != nil
	ctx.mu.Unlock()
	require.True(t, hasPending, "应有一个 pendingPingTimer")

	// 等待 timer 到期并执行 Ping
	time.Sleep(300 * time.Millisecond)

	// Ping 失败应标记 broken（证明延迟 Ping 确实执行了）
	ctx.mu.Lock()
	broken := ctx.broken
	pendingTimer := ctx.pendingPingTimer
	ctx.mu.Unlock()

	require.True(t, broken, "延迟 Ping 应已执行并标记 broken")
	require.Nil(t, pendingTimer, "Ping 执行后 pendingPingTimer 应被清理")
}

func TestOpenAIWSIngressContextPool_ScheduleDelayedPing_ResetExtendsDelay(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	pool := newOpenAIWSIngressContextPool(cfg)
	defer pool.Close()

	failConn := &openAIWSPingFailConn{}
	ctx := &openAIWSIngressContext{
		id:             "ctx_reset_delay",
		accountID:      9004,
		upstream:       failConn,
		upstreamConnID: "conn_reset_1",
	}
	ctx.touchLease(time.Now(), pool.idleTTL)

	// 第一次调度 200ms 延迟
	pool.scheduleDelayedPing(ctx, 200*time.Millisecond)

	// 100ms 后再次调度 200ms（应 Reset timer，从此刻起再等 200ms）
	time.Sleep(100 * time.Millisecond)
	pool.scheduleDelayedPing(ctx, 200*time.Millisecond)

	// 150ms 后（距第一次 250ms，距 Reset 150ms）应未执行
	time.Sleep(150 * time.Millisecond)
	ctx.mu.Lock()
	broken := ctx.broken
	ctx.mu.Unlock()
	require.False(t, broken, "Reset 后 150ms 不应触发 Ping")

	// 再等 100ms（距 Reset 250ms）应已执行
	time.Sleep(100 * time.Millisecond)
	ctx.mu.Lock()
	broken = ctx.broken
	ctx.mu.Unlock()
	require.True(t, broken, "Reset 后 250ms 应已触发 Ping")
}
