package service

import (
	"context"
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

func TestOpenAIWSIngressContextPool_CleanupAccountExpiredLocked_ClosesUpstream(t *testing.T) {
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
		handshakeHeaders: map[string][]string{"x-test": []string{"ok"}},
	}
	expiredCtx.setExpiresAt(time.Now().Add(-2 * time.Second))
	ap.contexts[expiredCtx.id] = expiredCtx
	ap.bySession[expiredCtx.sessionKey] = expiredCtx.id

	ap.mu.Lock()
	toClose := pool.cleanupAccountExpiredLocked(ap, time.Now())
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
	require.Equal(t, 85.0, scoreStrong)
	allowBalanced, scoreBalanced := openAIWSIngressMigrationPolicyByStickiness(openAIWSIngressStickinessBalanced)
	require.True(t, allowBalanced)
	require.Equal(t, 68.0, scoreBalanced)
	allowWeak, scoreWeak := openAIWSIngressMigrationPolicyByStickiness("weak_or_unknown")
	require.True(t, allowWeak)
	require.Equal(t, 45.0, scoreWeak)

	busyCtx := &openAIWSIngressContext{ownerID: "owner_busy"}
	_, _, ok := scoreOpenAIWSIngressMigrationCandidate(busyCtx, now)
	require.False(t, ok, "owner 占用中的 context 不应作为迁移候选")

	oldIdle := &openAIWSIngressContext{}
	oldIdle.setLastUsedAt(now.Add(-5 * time.Minute))
	recentIdle := &openAIWSIngressContext{}
	recentIdle.setLastUsedAt(now.Add(-10 * time.Second))
	scoreOld, _, okOld := scoreOpenAIWSIngressMigrationCandidate(oldIdle, now)
	scoreRecent, _, okRecent := scoreOpenAIWSIngressMigrationCandidate(recentIdle, now)
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
	scorePenalized, _, okPenalized := scoreOpenAIWSIngressMigrationCandidate(penalized, now)
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
	require.ErrorIs(t, lease.PingWithTimeout(time.Millisecond), errOpenAIWSConnClosed, "owner 不匹配时应拒绝访问")

	ctxItem.mu.Lock()
	ctxItem.ownerID = "owner_ok"
	ctxItem.upstream = &openAIWSPingFailConn{}
	ctxItem.mu.Unlock()
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
	ctxItem.mu.Lock()
	broken := ctxItem.broken
	failureStreak := ctxItem.failureStreak
	ctxItem.mu.Unlock()
	require.True(t, broken)
	require.GreaterOrEqual(t, failureStreak, 1, "dial 失败后应累计 failure_streak")
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
