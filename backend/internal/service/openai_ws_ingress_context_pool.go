package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

var (
	errOpenAIWSIngressContextBusy = errors.New("openai ws ingress context is busy")
)

const (
	openAIWSIngressScheduleLayerExact     = "l0_exact"
	openAIWSIngressScheduleLayerNew       = "l1_new_context"
	openAIWSIngressScheduleLayerMigration = "l2_migration"
	openAIWSIngressAcquireMaxWaitRetries  = 4096
	openAIWSIngressAcquireMaxQueueWait    = 30 * time.Minute

	// openAIWSUpstreamConnMaxAge 是上游 WebSocket 连接的默认最大存活时间。
	// OpenAI 在 60 分钟后强制关闭连接，此处默认 55 分钟主动轮换以避免中途断连。
	openAIWSUpstreamConnMaxAge = 55 * time.Minute

	// openAIWSIngressDelayedPingAfterYield 是 yield 后延迟 Ping 探测的等待时间。
	// 在会话暂时空闲后提前发现死连接，避免下次 Acquire 时才发现。
	openAIWSIngressDelayedPingAfterYield = 5 * time.Second

	// openAIWSIngressPingTimeout 是后台 Ping 探测的超时时间。
	openAIWSIngressPingTimeout = 5 * time.Second
)

const (
	openAIWSIngressStickinessWeak     = "weak"
	openAIWSIngressStickinessBalanced = "balanced"
	openAIWSIngressStickinessStrong   = "strong"
)

type openAIWSIngressContextAcquireRequest struct {
	Account     *Account
	GroupID     int64
	SessionHash string
	OwnerID     string
	WSURL       string
	Headers     http.Header
	ProxyURL    string
	Turn        int

	HasPreviousResponseID bool
	StrictAffinity        bool
	StoreDisabled         bool
}

type openAIWSIngressContextPool struct {
	cfg    *config.Config
	dialer openAIWSClientDialer

	idleTTL        time.Duration
	sweepInterval  time.Duration
	upstreamMaxAge time.Duration

	seq atomic.Uint64

	// schedulerStats provides load-aware signals (error rate, circuit breaker
	// state) for migration scoring. When nil, scoring falls back to the
	// existing idle-time + failure-streak heuristic.
	schedulerStats *openAIAccountRuntimeStats

	mu       sync.Mutex
	accounts map[int64]*openAIWSIngressAccountPool

	stopCh    chan struct{}
	stopOnce  sync.Once
	workerWg  sync.WaitGroup
	closeOnce sync.Once
}

type openAIWSIngressAccountPool struct {
	mu sync.Mutex

	refs atomic.Int64

	// dynamicCap 动态容量：初始 1，按需增长（L1 新建时 +1），空闲超时后缩减。
	// 实际容量为 min(dynamicCap, effectiveContextCapacity)。
	dynamicCap atomic.Int32

	contexts  map[string]*openAIWSIngressContext
	bySession map[string]string
}

type openAIWSIngressContext struct {
	id          string
	groupID     int64
	accountID   int64
	sessionHash string
	sessionKey  string

	mu                    sync.Mutex
	dialing               bool
	dialDone              chan struct{}
	releaseDone           chan struct{} // ownerID 释放时发送单信号，唤醒一个等待者
	ownerID               string
	lastUsedAtUnix        atomic.Int64
	expiresAtUnix         atomic.Int64
	lastTouchUnixNano     atomic.Int64 // throttle: skip touchLease if < 1s since last
	broken                bool
	failureStreak         int
	lastFailureAt         time.Time
	migrationCount        int
	lastMigrationAt       time.Time
	upstream              openAIWSClientConn
	upstreamConnID        string
	upstreamConnCreatedAt atomic.Int64 // UnixNano; 0 表示未设置
	handshakeHeaders      http.Header
	prewarmed             atomic.Bool
	pendingPingTimer      *time.Timer // 延迟 Ping 去重：同一 context 仅保留一个 pending ping
}

type openAIWSIngressContextLease struct {
	pool          *openAIWSIngressContextPool
	context       *openAIWSIngressContext
	ownerID       string
	queueWait     time.Duration
	connPick      time.Duration
	reused        bool
	scheduleLayer string
	stickiness    string
	migrationUsed bool
	released      atomic.Bool
	cachedConnMu  sync.RWMutex
	cachedConn    openAIWSClientConn // fast path: avoid mutex on every activeConn() call
}

func openAIWSTimeToUnixNano(ts time.Time) int64 {
	if ts.IsZero() {
		return 0
	}
	return ts.UnixNano()
}

func openAIWSUnixNanoToTime(ns int64) time.Time {
	if ns <= 0 {
		return time.Time{}
	}
	return time.Unix(0, ns)
}

func (c *openAIWSIngressContext) setLastUsedAt(ts time.Time) {
	if c == nil {
		return
	}
	c.lastUsedAtUnix.Store(openAIWSTimeToUnixNano(ts))
}

func (c *openAIWSIngressContext) lastUsedAt() time.Time {
	if c == nil {
		return time.Time{}
	}
	return openAIWSUnixNanoToTime(c.lastUsedAtUnix.Load())
}

func (c *openAIWSIngressContext) setExpiresAt(ts time.Time) {
	if c == nil {
		return
	}
	c.expiresAtUnix.Store(openAIWSTimeToUnixNano(ts))
}

func (c *openAIWSIngressContext) expiresAt() time.Time {
	if c == nil {
		return time.Time{}
	}
	return openAIWSUnixNanoToTime(c.expiresAtUnix.Load())
}

// upstreamConnAge 返回上游连接已存活的时长。
// 若 createdAt 未设置（零值）或 now 早于 createdAt（时钟回拨），返回 0。
func (c *openAIWSIngressContext) upstreamConnAge(now time.Time) time.Duration {
	if c == nil {
		return 0
	}
	ns := c.upstreamConnCreatedAt.Load()
	if ns <= 0 {
		return 0
	}
	age := now.Sub(time.Unix(0, ns))
	if age < 0 {
		return 0
	}
	return age
}

func (c *openAIWSIngressContext) touchLease(now time.Time, ttl time.Duration) {
	if c == nil {
		return
	}
	nowUnix := openAIWSTimeToUnixNano(now)
	c.lastUsedAtUnix.Store(nowUnix)
	c.expiresAtUnix.Store(openAIWSTimeToUnixNano(now.Add(ttl)))
	c.lastTouchUnixNano.Store(nowUnix)
}

// maybeTouchLease is a throttled version of touchLease.
// It skips the update if less than 1 second has passed since the last touch,
// avoiding redundant time.Now() + atomic stores on every hot-path message.
func (c *openAIWSIngressContext) maybeTouchLease(ttl time.Duration) {
	if c == nil {
		return
	}
	now := time.Now()
	nowNano := now.UnixNano()
	lastNano := c.lastTouchUnixNano.Load()
	if lastNano > 0 && nowNano-lastNano < int64(time.Second) {
		return
	}
	c.touchLease(now, ttl)
}

func newOpenAIWSIngressContextPool(cfg *config.Config) *openAIWSIngressContextPool {
	pool := &openAIWSIngressContextPool{
		cfg:            cfg,
		dialer:         newDefaultOpenAIWSClientDialer(),
		idleTTL:        10 * time.Minute,
		sweepInterval:  30 * time.Second,
		upstreamMaxAge: openAIWSUpstreamConnMaxAge,
		accounts:       make(map[int64]*openAIWSIngressAccountPool),
		stopCh:         make(chan struct{}),
	}
	if cfg != nil && cfg.Gateway.OpenAIWS.StickySessionTTLSeconds > 0 {
		pool.idleTTL = time.Duration(cfg.Gateway.OpenAIWS.StickySessionTTLSeconds) * time.Second
	}
	if cfg != nil && cfg.Gateway.OpenAIWS.UpstreamConnMaxAgeSeconds >= 0 {
		// 配置语义：0 表示禁用超龄轮换。
		pool.upstreamMaxAge = time.Duration(cfg.Gateway.OpenAIWS.UpstreamConnMaxAgeSeconds) * time.Second
	}
	pool.startWorker()
	return pool
}

func (p *openAIWSIngressContextPool) setClientDialerForTest(dialer openAIWSClientDialer) {
	if p == nil || dialer == nil {
		return
	}
	p.dialer = dialer
}

func (p *openAIWSIngressContextPool) SnapshotTransportMetrics() OpenAIWSTransportMetricsSnapshot {
	if p == nil {
		return OpenAIWSTransportMetricsSnapshot{}
	}
	if dialer, ok := p.dialer.(openAIWSTransportMetricsDialer); ok {
		return dialer.SnapshotTransportMetrics()
	}
	return OpenAIWSTransportMetricsSnapshot{}
}

func (p *openAIWSIngressContextPool) maxConnsHardCap() int {
	if p != nil && p.cfg != nil && p.cfg.Gateway.OpenAIWS.MaxConnsPerAccount > 0 {
		return p.cfg.Gateway.OpenAIWS.MaxConnsPerAccount
	}
	return 8
}

func (p *openAIWSIngressContextPool) effectiveContextCapacity(account *Account) int {
	if account == nil || account.Concurrency <= 0 {
		return 0
	}
	capacity := account.Concurrency
	hardCap := p.maxConnsHardCap()
	if hardCap > 0 && capacity > hardCap {
		return hardCap
	}
	return capacity
}

func (p *openAIWSIngressContextPool) Close() {
	if p == nil {
		return
	}
	p.closeOnce.Do(func() {
		p.stopOnce.Do(func() {
			close(p.stopCh)
		})
		p.workerWg.Wait()

		var toClose []openAIWSClientConn
		p.mu.Lock()
		accountPools := make([]*openAIWSIngressAccountPool, 0, len(p.accounts))
		for _, ap := range p.accounts {
			if ap != nil {
				accountPools = append(accountPools, ap)
			}
		}
		p.accounts = make(map[int64]*openAIWSIngressAccountPool)
		p.mu.Unlock()

		for _, ap := range accountPools {
			ap.mu.Lock()
			for _, ctx := range ap.contexts {
				if ctx == nil {
					continue
				}
				ctx.mu.Lock()
				if ctx.upstream != nil {
					toClose = append(toClose, ctx.upstream)
				}
				ctx.upstream = nil
				ctx.upstreamConnCreatedAt.Store(0)
				ctx.broken = true
				ctx.ownerID = ""
				ctx.handshakeHeaders = nil
				ctx.mu.Unlock()
			}
			ap.contexts = make(map[string]*openAIWSIngressContext)
			ap.bySession = make(map[string]string)
			ap.mu.Unlock()
		}

		for _, conn := range toClose {
			if conn != nil {
				_ = conn.Close()
			}
		}
	})
}

func (p *openAIWSIngressContextPool) startWorker() {
	if p == nil {
		return
	}
	interval := p.sweepInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	p.workerWg.Add(1)
	go func() {
		defer p.workerWg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-p.stopCh:
				return
			case <-ticker.C:
				p.sweepExpiredIdleContexts()
			}
		}
	}()
}

func (p *openAIWSIngressContextPool) Acquire(
	ctx context.Context,
	req openAIWSIngressContextAcquireRequest,
) (*openAIWSIngressContextLease, error) {
	if p == nil {
		return nil, errors.New("openai ws ingress context pool is nil")
	}
	if req.Account == nil || req.Account.ID <= 0 {
		return nil, errors.New("invalid account in ingress context acquire request")
	}
	ownerID := strings.TrimSpace(req.OwnerID)
	if ownerID == "" {
		return nil, errors.New("owner id is empty")
	}
	if strings.TrimSpace(req.WSURL) == "" {
		return nil, errors.New("ws url is empty")
	}
	capacity := p.effectiveContextCapacity(req.Account)
	if capacity <= 0 {
		return nil, errOpenAIWSConnQueueFull
	}

	sessionHash := strings.TrimSpace(req.SessionHash)
	if sessionHash == "" {
		// 无会话信号时退化为连接级上下文，避免跨连接串会话。
		sessionHash = "conn:" + ownerID
	}
	sessionKey := openAIWSIngressContextSessionKey(req.GroupID, sessionHash)
	accountID := req.Account.ID

	start := time.Now()
	queueWait := time.Duration(0)
	waitRetries := 0

	p.mu.Lock()
	ap := p.getOrCreateAccountPoolLocked(accountID)
	ap.refs.Add(1)
	p.mu.Unlock()
	defer ap.refs.Add(-1)

	calcConnPick := func() time.Duration {
		connPick := time.Since(start) - queueWait
		if connPick < 0 {
			return 0
		}
		return connPick
	}

	for {
		now := time.Now()
		var (
			selected      *openAIWSIngressContext
			reusedContext bool
			newlyCreated  bool
			ownerAssigned bool
			migrationUsed bool
			scheduleLayer string
			oldUpstream   openAIWSClientConn
			deferredClose []openAIWSClientConn
		)

		ap.mu.Lock()

		stickiness := p.resolveStickinessLevelLocked(ap, sessionKey, req, now)
		allowMigration, minMigrationScore := openAIWSIngressMigrationPolicyByStickiness(stickiness)

		if existingID, ok := ap.bySession[sessionKey]; ok {
			if existing := ap.contexts[existingID]; existing != nil {
				existing.mu.Lock()
				switch existing.ownerID {
				case "":
					if existing.releaseDone != nil {
						select {
						case <-existing.releaseDone:
						default:
						}
					}
					existing.ownerID = ownerID
					ownerAssigned = true
					existing.touchLease(now, p.idleTTL)
					selected = existing
					reusedContext = true
					scheduleLayer = openAIWSIngressScheduleLayerExact
				case ownerID:
					existing.touchLease(now, p.idleTTL)
					selected = existing
					reusedContext = true
					scheduleLayer = openAIWSIngressScheduleLayerExact
				default:
					// 当前 context 被其他 owner 占用，等待其释放后重试（循环重试替代递归）。
					if existing.releaseDone == nil {
						existing.releaseDone = make(chan struct{}, 1)
					}
					releaseDone := existing.releaseDone
					existing.mu.Unlock()
					ap.mu.Unlock()
					closeOpenAIWSClientConns(deferredClose)

					waitStart := time.Now()
					select {
					case <-releaseDone:
						queueWait += time.Since(waitStart)
						waitRetries++
						if waitRetries >= openAIWSIngressAcquireMaxWaitRetries || queueWait >= openAIWSIngressAcquireMaxQueueWait {
							logOpenAIWSModeInfo(
								"ctx_pool_owner_wait_exhausted account_id=%d ctx_id=%s owner_id=%s wait_retries=%d queue_wait_ms=%d",
								accountID, existing.id, ownerID, waitRetries, queueWait.Milliseconds(),
							)
							return nil, errOpenAIWSIngressContextBusy
						}
						continue
					case <-ctx.Done():
						queueWait += time.Since(waitStart)
						logOpenAIWSModeInfo(
							"ctx_pool_owner_wait_canceled account_id=%d ctx_id=%s owner_id=%s wait_retries=%d queue_wait_ms=%d",
							accountID, existing.id, ownerID, waitRetries, queueWait.Milliseconds(),
						)
						return nil, errOpenAIWSIngressContextBusy
					}
				}
				existing.mu.Unlock()
			}
		}

		if selected == nil {
			dynCap := p.effectiveDynamicCapacity(ap, capacity)
			if len(ap.contexts) >= dynCap {
				deferredClose = append(deferredClose, p.evictExpiredIdleLocked(ap, now)...)
			}
			if len(ap.contexts) >= dynCap {
				if dynCap < capacity {
					// 动态扩容：尚未达到硬上限，增长 1 后创建新 context
					ap.dynamicCap.Add(1)
				} else if !allowMigration {
					ap.mu.Unlock()
					closeOpenAIWSClientConns(deferredClose)
					logOpenAIWSModeInfo(
						"ctx_pool_full_no_migration account_id=%d capacity=%d stickiness=%s",
						accountID, capacity, stickiness,
					)
					return nil, errOpenAIWSConnQueueFull
				} else {
					recycle := p.pickMigrationCandidateLocked(ap, minMigrationScore, now)
					if recycle == nil {
						ap.mu.Unlock()
						closeOpenAIWSClientConns(deferredClose)
						logOpenAIWSModeInfo(
							"ctx_pool_no_migration_candidate account_id=%d capacity=%d min_score=%.1f",
							accountID, capacity, minMigrationScore,
						)
						return nil, errOpenAIWSConnQueueFull
					}
					recycle.mu.Lock()
					oldSessionKey := recycle.sessionKey
					oldUpstream = recycle.upstream
					recycle.sessionHash = sessionHash
					recycle.sessionKey = sessionKey
					recycle.groupID = req.GroupID
					if recycle.releaseDone != nil {
						select {
						case <-recycle.releaseDone:
						default:
						}
					}
					recycle.ownerID = ownerID
					recycle.touchLease(now, p.idleTTL)
					// 会话被回收复用时关闭旧上游，避免跨会话污染。
					recycle.upstream = nil
					recycle.upstreamConnID = ""
					recycle.upstreamConnCreatedAt.Store(0)
					recycle.handshakeHeaders = nil
					recycle.broken = false
					recycle.migrationCount++
					recycle.lastMigrationAt = now
					recycle.mu.Unlock()

					if oldSessionKey != "" {
						if mapped, ok := ap.bySession[oldSessionKey]; ok && mapped == recycle.id {
							delete(ap.bySession, oldSessionKey)
						}
					}
					ap.bySession[sessionKey] = recycle.id
					selected = recycle
					reusedContext = true
					migrationUsed = true
					scheduleLayer = openAIWSIngressScheduleLayerMigration
					ap.mu.Unlock()
					closeOpenAIWSClientConns(deferredClose)
					if oldUpstream != nil {
						_ = oldUpstream.Close()
					}
					reusedConn, ensureErr := p.ensureContextUpstream(ctx, selected, req)
					if ensureErr != nil {
						p.releaseContext(selected, ownerID)
						return nil, ensureErr
					}
					logOpenAIWSModeInfo(
						"ctx_pool_migration account_id=%d ctx_id=%s old_session=%s new_session=%s group_id=%d session_hash=%s migration_count=%d",
						accountID, selected.id, truncateOpenAIWSLogValue(oldSessionKey, openAIWSIDValueMaxLen),
						truncateOpenAIWSLogValue(sessionKey, openAIWSIDValueMaxLen), selected.groupID,
						truncateOpenAIWSLogValue(selected.sessionHash, openAIWSIDValueMaxLen), selected.migrationCount,
					)
					return &openAIWSIngressContextLease{
						pool:          p,
						context:       selected,
						ownerID:       ownerID,
						queueWait:     queueWait,
						connPick:      calcConnPick(),
						reused:        reusedContext && reusedConn,
						scheduleLayer: scheduleLayer,
						stickiness:    stickiness,
						migrationUsed: migrationUsed,
					}, nil
				}
			}

			ctxID := fmt.Sprintf("ctx_%d_%d", accountID, p.seq.Add(1))
			created := &openAIWSIngressContext{
				id:          ctxID,
				groupID:     req.GroupID,
				accountID:   accountID,
				sessionHash: sessionHash,
				sessionKey:  sessionKey,
				ownerID:     ownerID,
			}
			created.touchLease(now, p.idleTTL)
			ap.contexts[ctxID] = created
			ap.bySession[sessionKey] = ctxID
			selected = created
			newlyCreated = true
			ownerAssigned = true
			scheduleLayer = openAIWSIngressScheduleLayerNew
		}
		ap.mu.Unlock()
		closeOpenAIWSClientConns(deferredClose)

		reusedConn, ensureErr := p.ensureContextUpstream(ctx, selected, req)
		if ensureErr != nil {
			if newlyCreated {
				ap.mu.Lock()
				delete(ap.contexts, selected.id)
				if mapped, ok := ap.bySession[sessionKey]; ok && mapped == selected.id {
					delete(ap.bySession, sessionKey)
				}
				ap.mu.Unlock()
			} else if ownerAssigned {
				p.releaseContext(selected, ownerID)
			}
			return nil, ensureErr
		}

		return &openAIWSIngressContextLease{
			pool:          p,
			context:       selected,
			ownerID:       ownerID,
			queueWait:     queueWait,
			connPick:      calcConnPick(),
			reused:        reusedContext && reusedConn,
			scheduleLayer: scheduleLayer,
			stickiness:    stickiness,
			migrationUsed: migrationUsed,
		}, nil
	}
}

func (p *openAIWSIngressContextPool) resolveStickinessLevelLocked(
	ap *openAIWSIngressAccountPool,
	sessionKey string,
	req openAIWSIngressContextAcquireRequest,
	now time.Time,
) string {
	if req.StrictAffinity {
		return openAIWSIngressStickinessStrong
	}

	level := openAIWSIngressStickinessWeak
	switch {
	case req.HasPreviousResponseID:
		level = openAIWSIngressStickinessStrong
	case req.StoreDisabled || req.Turn > 1:
		level = openAIWSIngressStickinessBalanced
	}

	if ap == nil {
		return level
	}
	ctxID, ok := ap.bySession[sessionKey]
	if !ok {
		return level
	}
	existing := ap.contexts[ctxID]
	if existing == nil {
		return level
	}

	existing.mu.Lock()
	broken := existing.broken
	failureStreak := existing.failureStreak
	lastFailureAt := existing.lastFailureAt
	lastUsedAt := existing.lastUsedAt()
	existing.mu.Unlock()

	recentFailure := failureStreak > 0 && !lastFailureAt.IsZero() && now.Sub(lastFailureAt) <= 2*time.Minute
	if broken || recentFailure {
		return openAIWSIngressStickinessDowngrade(level)
	}
	if failureStreak == 0 && !lastUsedAt.IsZero() && now.Sub(lastUsedAt) <= 20*time.Second {
		return openAIWSIngressStickinessUpgrade(level)
	}
	return level
}

func openAIWSIngressMigrationPolicyByStickiness(stickiness string) (bool, float64) {
	switch stickiness {
	case openAIWSIngressStickinessStrong:
		return false, 80 // was 85; lowered to allow migration away from degraded accounts
	case openAIWSIngressStickinessBalanced:
		return true, 65 // was 68; lowered to allow more aggressive migration to healthier accounts
	default:
		return true, 40 // was 45; lowered for weak stickiness
	}
}

func openAIWSIngressStickinessDowngrade(level string) string {
	switch level {
	case openAIWSIngressStickinessStrong:
		return openAIWSIngressStickinessBalanced
	case openAIWSIngressStickinessBalanced:
		return openAIWSIngressStickinessWeak
	default:
		return openAIWSIngressStickinessWeak
	}
}

func openAIWSIngressStickinessUpgrade(level string) string {
	switch level {
	case openAIWSIngressStickinessWeak:
		return openAIWSIngressStickinessBalanced
	case openAIWSIngressStickinessBalanced:
		return openAIWSIngressStickinessStrong
	default:
		return openAIWSIngressStickinessStrong
	}
}

func (p *openAIWSIngressContextPool) pickMigrationCandidateLocked(
	ap *openAIWSIngressAccountPool,
	minScore float64,
	now time.Time,
) *openAIWSIngressContext {
	if ap == nil {
		return nil
	}
	var (
		selected      *openAIWSIngressContext
		selectedScore float64
		selectedAt    time.Time
		hasSelected   bool
	)

	for _, ctx := range ap.contexts {
		if ctx == nil {
			continue
		}
		score, lastUsed, ok := scoreOpenAIWSIngressMigrationCandidate(ctx, now, p.schedulerStats)
		if !ok || score < minScore {
			continue
		}
		if !hasSelected || score > selectedScore || (score == selectedScore && lastUsed.Before(selectedAt)) {
			selected = ctx
			selectedScore = score
			selectedAt = lastUsed
			hasSelected = true
		}
	}
	return selected
}

func scoreOpenAIWSIngressMigrationCandidate(c *openAIWSIngressContext, now time.Time, stats *openAIAccountRuntimeStats) (float64, time.Time, bool) {
	if c == nil {
		return 0, time.Time{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if strings.TrimSpace(c.ownerID) != "" {
		return 0, time.Time{}, false
	}

	score := 100.0
	if c.broken {
		score -= 30
	}
	if c.failureStreak > 0 {
		score -= float64(minInt(c.failureStreak*12, 40))
	}
	if !c.lastFailureAt.IsZero() && now.Sub(c.lastFailureAt) <= 2*time.Minute {
		score -= 18
	}
	if !c.lastMigrationAt.IsZero() && now.Sub(c.lastMigrationAt) <= time.Minute {
		score -= 10
	}
	if c.migrationCount > 0 {
		score -= float64(minInt(c.migrationCount*4, 20))
	}

	lastUsedAt := c.lastUsedAt()
	idleDuration := now.Sub(lastUsedAt)
	switch {
	case idleDuration <= 15*time.Second:
		score -= 15
	case idleDuration >= 3*time.Minute:
		score += 16
	default:
		score += idleDuration.Seconds() / 12.0
	}

	// Load-aware factors: penalize contexts bound to accounts that the
	// scheduler has flagged as degraded or circuit-open. When stats is nil
	// (e.g. during tests or before scheduler init), these adjustments are
	// silently skipped so existing behaviour is preserved.
	if stats != nil && c.accountID > 0 {
		errorRate, _, _ := stats.snapshot(c.accountID)
		// errorRate is in [0,1]; a fully-erroring account subtracts up to 30
		// points, making it significantly harder for a migration to land on
		// an unhealthy account.
		score -= errorRate * 30

		// Circuit-open accounts receive a harsh penalty (-50) that in
		// practice drops the score below any reasonable minimum threshold,
		// effectively blocking migration to that account.
		if stats.isCircuitOpen(c.accountID) {
			score -= 50
		}
	}

	return score, lastUsedAt, true
}

func minInt(a, b int) int {
	if a <= b {
		return a
	}
	return b
}

func closeOpenAIWSClientConns(conns []openAIWSClientConn) {
	for _, conn := range conns {
		if conn != nil {
			_ = conn.Close()
		}
	}
}

func (p *openAIWSIngressContextPool) ensureContextUpstream(
	ctx context.Context,
	c *openAIWSIngressContext,
	req openAIWSIngressContextAcquireRequest,
) (bool, error) {
	if p == nil || c == nil {
		return false, errOpenAIWSConnClosed
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		c.mu.Lock()
		if c.upstream != nil && !c.broken {
			now := time.Now()
			connAge := c.upstreamConnAge(now)
			if p.upstreamMaxAge > 0 && connAge > 0 && connAge >= p.upstreamMaxAge {
				// 主动轮换：关闭旧连接，不设 broken、不增 failureStreak
				oldUpstream, oldConnID := c.upstream, c.upstreamConnID
				c.upstream = nil
				c.upstreamConnID = ""
				c.upstreamConnCreatedAt.Store(0)
				c.handshakeHeaders = nil
				c.prewarmed.Store(false)
				c.mu.Unlock()
				_ = oldUpstream.Close()
				logOpenAIWSModeInfo(
					"ctx_pool_upstream_max_age_rotate account_id=%d ctx_id=%s conn_id=%s conn_age_min=%.1f max_age_min=%.1f",
					c.accountID, c.id, oldConnID,
					connAge.Minutes(), p.upstreamMaxAge.Minutes(),
				)
				continue // 回到 for 循环走 dialing 路径
			}
			c.touchLease(now, p.idleTTL)
			c.mu.Unlock()
			return true, nil
		}
		if c.dialing {
			dialDone := c.dialDone
			c.mu.Unlock()
			if dialDone == nil {
				if err := ctx.Err(); err != nil {
					return false, err
				}
				continue
			}
			select {
			case <-dialDone:
				continue
			case <-ctx.Done():
				return false, ctx.Err()
			}
		}
		oldUpstream := c.upstream
		c.upstream = nil
		c.upstreamConnCreatedAt.Store(0)
		c.handshakeHeaders = nil
		c.upstreamConnID = ""
		c.prewarmed.Store(false)
		c.broken = false
		c.dialing = true
		dialDone := make(chan struct{})
		c.dialDone = dialDone
		c.mu.Unlock()

		if oldUpstream != nil {
			_ = oldUpstream.Close()
		}

		dialer := p.dialer
		if dialer == nil {
			c.mu.Lock()
			c.broken = true
			c.failureStreak++
			c.lastFailureAt = time.Now()
			c.dialing = false
			if c.dialDone == dialDone {
				c.dialDone = nil
			}
			close(dialDone)
			c.mu.Unlock()
			return false, errors.New("openai ws ingress context dialer is nil")
		}
		conn, statusCode, handshakeHeaders, err := dialer.Dial(ctx, req.WSURL, req.Headers, req.ProxyURL)
		if err != nil {
			wrappedErr := err
			var dialErr *openAIWSDialError
			if !errors.As(err, &dialErr) {
				wrappedErr = &openAIWSDialError{
					StatusCode:      statusCode,
					ResponseHeaders: cloneHeader(handshakeHeaders),
					Err:             err,
				}
			}
			c.mu.Lock()
			c.broken = true
			c.failureStreak++
			c.lastFailureAt = time.Now()
			c.dialing = false
			if c.dialDone == dialDone {
				c.dialDone = nil
			}
			close(dialDone)
			failureStreak := c.failureStreak
			c.mu.Unlock()
			logOpenAIWSModeInfo(
				"ctx_pool_dial_fail account_id=%d ctx_id=%s status_code=%d failure_streak=%d cause=%s",
				c.accountID, c.id, statusCode, failureStreak, truncateOpenAIWSLogValue(err.Error(), openAIWSLogValueMaxLen),
			)
			return false, wrappedErr
		}

		c.mu.Lock()
		now := time.Now()
		c.upstream = conn
		c.upstreamConnID = fmt.Sprintf("ctxws_%d_%d", c.accountID, p.seq.Add(1))
		c.upstreamConnCreatedAt.Store(now.UnixNano())
		c.handshakeHeaders = cloneHeader(handshakeHeaders)
		c.prewarmed.Store(false)
		c.touchLease(now, p.idleTTL)
		c.broken = false
		c.failureStreak = 0
		c.lastFailureAt = time.Time{}
		c.dialing = false
		if c.dialDone == dialDone {
			c.dialDone = nil
		}
		close(dialDone)
		connID := c.upstreamConnID
		c.mu.Unlock()
		logOpenAIWSModeInfo(
			"ctx_pool_dial_ok account_id=%d ctx_id=%s conn_id=%s",
			c.accountID, c.id, connID,
		)
		return false, nil
	}
}

func (p *openAIWSIngressContextPool) yieldContext(c *openAIWSIngressContext, ownerID string) {
	p.releaseContextWithPolicy(c, ownerID, false)
	// yield 后延迟 Ping，提前发现死连接
	p.scheduleDelayedPing(c, openAIWSIngressDelayedPingAfterYield)
}

func (p *openAIWSIngressContextPool) releaseContext(c *openAIWSIngressContext, ownerID string) {
	p.releaseContextWithPolicy(c, ownerID, true)
}

func (p *openAIWSIngressContextPool) releaseContextWithPolicy(
	c *openAIWSIngressContext,
	ownerID string,
	closeUpstream bool,
) {
	if p == nil || c == nil {
		return
	}
	var upstream openAIWSClientConn
	c.mu.Lock()
	if c.ownerID == ownerID {
		if closeUpstream {
			// 会话结束或链路损坏时销毁上游连接，避免污染后续请求。
			upstream = c.upstream
			c.upstream = nil
			c.upstreamConnCreatedAt.Store(0)
			c.handshakeHeaders = nil
			c.upstreamConnID = ""
			c.prewarmed.Store(false)
		}
		c.ownerID = ""
		// 通知一个等待中的 Acquire 请求，避免 close 广播导致惊群。
		if c.releaseDone != nil {
			select {
			case c.releaseDone <- struct{}{}:
			default:
			}
		}
		now := time.Now()
		c.setLastUsedAt(now)
		c.setExpiresAt(now.Add(p.idleTTL))
		c.broken = false
	}
	c.mu.Unlock()
	if upstream != nil {
		_ = upstream.Close()
	}
}

func (p *openAIWSIngressContextPool) markContextBroken(c *openAIWSIngressContext) {
	if c == nil {
		return
	}
	c.mu.Lock()
	upstream := c.upstream
	c.upstream = nil
	c.upstreamConnCreatedAt.Store(0)
	c.handshakeHeaders = nil
	c.upstreamConnID = ""
	c.prewarmed.Store(false)
	c.broken = true
	c.failureStreak++
	c.lastFailureAt = time.Now()
	// 注意：此处不发送 releaseDone 信号。ownerID 仍被占用，等待者被唤醒后
	// 会发现 owner 未释放而重新阻塞，造成信号浪费。实际释放由 Release() 完成。
	failureStreak := c.failureStreak
	c.mu.Unlock()
	logOpenAIWSModeInfo(
		"ctx_pool_mark_broken account_id=%d ctx_id=%s failure_streak=%d",
		c.accountID, c.id, failureStreak,
	)
	if upstream != nil {
		_ = upstream.Close()
	}
}

// markContextBrokenIfConnMatch 仅在连接代次（connID）匹配时标记 broken。
// 后台 Ping 在解锁期间执行，期间连接可能已被重建为新连接；
// 若 connID 已变则说明旧连接已被替换，放弃标记以避免误杀新连接。
func (p *openAIWSIngressContextPool) markContextBrokenIfConnMatch(c *openAIWSIngressContext, expectedConnID string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	actualConnID := c.upstreamConnID
	if actualConnID != expectedConnID {
		// 连接已被重建（connID 变了），放弃标记
		c.mu.Unlock()
		logOpenAIWSModeInfo(
			"ctx_pool_bg_ping_skip_stale account_id=%d ctx_id=%s expected_conn_id=%s actual_conn_id=%s",
			c.accountID, c.id, expectedConnID, actualConnID,
		)
		return
	}
	ownerID := c.ownerID
	dialing := c.dialing
	if ownerID != "" || dialing {
		// Ping 期间 context 可能被重新占用或进入建连流程，不应由后台探测路径误杀活跃连接。
		c.mu.Unlock()
		logOpenAIWSModeInfo(
			"ctx_pool_bg_ping_skip_busy account_id=%d ctx_id=%s conn_id=%s owner_id=%s dialing=%v",
			c.accountID,
			c.id,
			actualConnID,
			truncateOpenAIWSLogValue(ownerID, openAIWSIDValueMaxLen),
			dialing,
		)
		return
	}
	upstream := c.upstream
	c.upstream = nil
	c.upstreamConnCreatedAt.Store(0)
	c.handshakeHeaders = nil
	c.upstreamConnID = ""
	c.prewarmed.Store(false)
	c.broken = true
	c.failureStreak++
	c.lastFailureAt = time.Now()
	failureStreak := c.failureStreak
	c.mu.Unlock()
	logOpenAIWSModeInfo(
		"ctx_pool_mark_broken account_id=%d ctx_id=%s failure_streak=%d",
		c.accountID, c.id, failureStreak,
	)
	if upstream != nil {
		_ = upstream.Close()
	}
}

func (p *openAIWSIngressContextPool) getOrCreateAccountPoolLocked(accountID int64) *openAIWSIngressAccountPool {
	if ap, ok := p.accounts[accountID]; ok && ap != nil {
		return ap
	}
	ap := &openAIWSIngressAccountPool{
		contexts:  make(map[string]*openAIWSIngressContext),
		bySession: make(map[string]string),
	}
	ap.dynamicCap.Store(1)
	p.accounts[accountID] = ap
	return ap
}

// effectiveDynamicCapacity 返回 min(dynamicCap, hardCap)。
// dynamicCap 从 1 开始，按需增长，空闲时缩减；hardCap 由账户并发度和全局上限决定。
func (p *openAIWSIngressContextPool) effectiveDynamicCapacity(ap *openAIWSIngressAccountPool, hardCap int) int {
	if ap == nil || hardCap <= 0 {
		return hardCap
	}
	dynCap := int(ap.dynamicCap.Load())
	if dynCap < 1 {
		dynCap = 1
		ap.dynamicCap.Store(1)
	}
	if dynCap > hardCap {
		return hardCap
	}
	return dynCap
}

func (p *openAIWSIngressContextPool) evictExpiredIdleLocked(
	ap *openAIWSIngressAccountPool,
	now time.Time,
) []openAIWSClientConn {
	if ap == nil {
		return nil
	}
	var toClose []openAIWSClientConn
	for id, ctx := range ap.contexts {
		if ctx == nil {
			delete(ap.contexts, id)
			continue
		}
		ctx.mu.Lock()
		expiresAt := ctx.expiresAt()
		expired := ctx.ownerID == "" && !expiresAt.IsZero() && now.After(expiresAt)
		upstream := ctx.upstream
		if expired {
			ctx.upstream = nil
			ctx.upstreamConnCreatedAt.Store(0)
			ctx.handshakeHeaders = nil
			ctx.upstreamConnID = ""
		}
		ctx.mu.Unlock()
		if !expired {
			continue
		}
		delete(ap.contexts, id)
		if mappedID, ok := ap.bySession[ctx.sessionKey]; ok && mappedID == id {
			delete(ap.bySession, ctx.sessionKey)
		}
		if upstream != nil {
			toClose = append(toClose, upstream)
		}
	}
	return toClose
}

func (p *openAIWSIngressContextPool) pickOldestIdleContextLocked(ap *openAIWSIngressAccountPool) *openAIWSIngressContext {
	if ap == nil {
		return nil
	}
	var (
		selected   *openAIWSIngressContext
		selectedAt time.Time
	)
	for _, ctx := range ap.contexts {
		if ctx == nil {
			continue
		}
		ctx.mu.Lock()
		idle := strings.TrimSpace(ctx.ownerID) == ""
		lastUsed := ctx.lastUsedAt()
		ctx.mu.Unlock()
		if !idle {
			continue
		}
		if selected == nil || lastUsed.Before(selectedAt) {
			selected = ctx
			selectedAt = lastUsed
		}
	}
	return selected
}

// closeAgedIdleUpstreamsLocked 关闭空闲且超龄的上游连接。
// 只清理 upstream，保留 context 槽位（不删 context、不清 bySession）。
// 不设 broken、不增 failureStreak。
// 调用方必须持有 ap.mu。
func (p *openAIWSIngressContextPool) closeAgedIdleUpstreamsLocked(
	ap *openAIWSIngressAccountPool,
	now time.Time,
) []openAIWSClientConn {
	if ap == nil || p.upstreamMaxAge <= 0 {
		return nil
	}
	var toClose []openAIWSClientConn
	for _, ctx := range ap.contexts {
		if ctx == nil {
			continue
		}
		ctx.mu.Lock()
		idle := ctx.ownerID == ""
		hasUpstream := ctx.upstream != nil
		connAge := ctx.upstreamConnAge(now)
		aged := connAge > 0 && connAge >= p.upstreamMaxAge
		if idle && hasUpstream && aged {
			toClose = append(toClose, ctx.upstream)
			ctx.upstream = nil
			ctx.upstreamConnCreatedAt.Store(0)
			ctx.upstreamConnID = ""
			ctx.handshakeHeaders = nil
			ctx.prewarmed.Store(false)
		}
		ctx.mu.Unlock()
	}
	return toClose
}

// pingContextUpstream 对空闲 context 的上游连接发送 Ping 探测。
// 若 Ping 失败则标记 context 为 broken，让后续 Acquire 走重建路径。
// 调用方不需要持有任何锁。
//
// 使用 connID 代次守卫：Ping 期间连接可能被重建，仅当 connID 未变时才标记 broken，
// 避免旧连接 Ping 失败误杀新连接。
func (p *openAIWSIngressContextPool) pingContextUpstream(c *openAIWSIngressContext) {
	if p == nil || c == nil {
		return
	}
	c.mu.Lock()
	idle := c.ownerID == ""
	hasUpstream := c.upstream != nil
	broken := c.broken
	dialing := c.dialing
	upstream := c.upstream
	connID := c.upstreamConnID // 快照连接代次
	c.mu.Unlock()
	if !idle || !hasUpstream || broken || dialing || upstream == nil {
		return
	}

	pingCtx, cancel := context.WithTimeout(context.Background(), openAIWSIngressPingTimeout)
	defer cancel()
	if err := upstream.Ping(pingCtx); err != nil {
		p.markContextBrokenIfConnMatch(c, connID)
		logOpenAIWSModeInfo(
			"ctx_pool_bg_ping_fail account_id=%d ctx_id=%s conn_id=%s cause=%s",
			c.accountID, c.id, connID, truncateOpenAIWSLogValue(err.Error(), openAIWSLogValueMaxLen),
		)
	}
}

// pingIdleUpstreams 对账户池内所有空闲且有上游连接的 context 发起 Ping 探测。
// 先在锁内收集候选列表，再在锁外逐个 Ping，避免阻塞其他操作。
func (p *openAIWSIngressContextPool) pingIdleUpstreams(ap *openAIWSIngressAccountPool) {
	if p == nil || ap == nil {
		return
	}
	ap.mu.Lock()
	targets := make([]*openAIWSIngressContext, 0, len(ap.contexts))
	for _, ctx := range ap.contexts {
		if ctx == nil {
			continue
		}
		ctx.mu.Lock()
		eligible := ctx.ownerID == "" && ctx.upstream != nil && !ctx.broken && !ctx.dialing
		ctx.mu.Unlock()
		if eligible {
			targets = append(targets, ctx)
		}
	}
	ap.mu.Unlock()

	for _, ctx := range targets {
		p.pingContextUpstream(ctx)
	}
}

// scheduleDelayedPing 在 yield 后延迟一段时间对 context 发送 Ping 探测。
// 通过 pendingPingTimer 去重：同一 context 同时只保留一个延迟 Ping，
// 连续 yield 只 Reset timer 而不创建新 goroutine，避免高并发下 goroutine 堆积。
func (p *openAIWSIngressContextPool) scheduleDelayedPing(c *openAIWSIngressContext, delay time.Duration) {
	if p == nil || c == nil || delay <= 0 {
		return
	}
	c.mu.Lock()
	if c.pendingPingTimer != nil {
		// 已有 pending ping，只需 Reset timer 延迟窗口
		c.pendingPingTimer.Reset(delay)
		c.mu.Unlock()
		return
	}
	timer := time.NewTimer(delay)
	c.pendingPingTimer = timer
	c.mu.Unlock()

	go func() {
		select {
		case <-p.stopCh:
			timer.Stop()
		case <-timer.C:
			p.pingContextUpstream(c)
		}
		c.mu.Lock()
		if c.pendingPingTimer == timer {
			c.pendingPingTimer = nil
		}
		c.mu.Unlock()
	}()
}

func (p *openAIWSIngressContextPool) sweepExpiredIdleContexts() {
	if p == nil {
		return
	}
	now := time.Now()

	type accountSnapshot struct {
		accountID int64
		ap        *openAIWSIngressAccountPool
	}

	snapshots := make([]accountSnapshot, 0, len(p.accounts))
	p.mu.Lock()
	for accountID, ap := range p.accounts {
		if ap == nil {
			delete(p.accounts, accountID)
			continue
		}
		snapshots = append(snapshots, accountSnapshot{accountID: accountID, ap: ap})
	}
	p.mu.Unlock()

	removable := make([]accountSnapshot, 0)
	for _, item := range snapshots {
		ap := item.ap
		ap.mu.Lock()
		toClose := p.evictExpiredIdleLocked(ap, now)
		agedClose := p.closeAgedIdleUpstreamsLocked(ap, now)
		empty := len(ap.contexts) == 0
		// 动态缩容：将 dynamicCap 收缩到 max(1, 当前 context 数量)
		shrinkTarget := int32(len(ap.contexts))
		if shrinkTarget < 1 {
			shrinkTarget = 1
		}
		if ap.dynamicCap.Load() > shrinkTarget {
			ap.dynamicCap.Store(shrinkTarget)
		}
		ap.mu.Unlock()
		closeOpenAIWSClientConns(toClose)
		closeOpenAIWSClientConns(agedClose)
		// 后台 Ping 探测：对剩余空闲连接发送 Ping，及时剔除死连接
		if !empty {
			p.pingIdleUpstreams(ap)
		}
		if empty && ap.refs.Load() == 0 {
			removable = append(removable, item)
		}
	}

	if len(removable) == 0 {
		return
	}

	p.mu.Lock()
	for _, item := range removable {
		existing := p.accounts[item.accountID]
		if existing != item.ap || existing == nil {
			continue
		}
		if existing.refs.Load() != 0 {
			continue
		}
		delete(p.accounts, item.accountID)
	}
	p.mu.Unlock()
}

func openAIWSIngressContextSessionKey(groupID int64, sessionHash string) string {
	hash := strings.TrimSpace(sessionHash)
	if hash == "" {
		return ""
	}
	return strconv.FormatInt(groupID, 10) + ":" + hash
}

func (l *openAIWSIngressContextLease) ConnID() string {
	if l == nil || l.context == nil {
		return ""
	}
	l.context.mu.Lock()
	defer l.context.mu.Unlock()
	return strings.TrimSpace(l.context.upstreamConnID)
}

func (l *openAIWSIngressContextLease) QueueWaitDuration() time.Duration {
	if l == nil {
		return 0
	}
	return l.queueWait
}

func (l *openAIWSIngressContextLease) ConnPickDuration() time.Duration {
	if l == nil {
		return 0
	}
	return l.connPick
}

func (l *openAIWSIngressContextLease) Reused() bool {
	if l == nil {
		return false
	}
	return l.reused
}

func (l *openAIWSIngressContextLease) ScheduleLayer() string {
	if l == nil {
		return ""
	}
	return strings.TrimSpace(l.scheduleLayer)
}

func (l *openAIWSIngressContextLease) StickinessLevel() string {
	if l == nil {
		return ""
	}
	return strings.TrimSpace(l.stickiness)
}

func (l *openAIWSIngressContextLease) MigrationUsed() bool {
	if l == nil {
		return false
	}
	return l.migrationUsed
}

func (l *openAIWSIngressContextLease) HandshakeHeader(name string) string {
	if l == nil || l.context == nil {
		return ""
	}
	l.context.mu.Lock()
	defer l.context.mu.Unlock()
	if l.context.handshakeHeaders == nil {
		return ""
	}
	return strings.TrimSpace(l.context.handshakeHeaders.Get(strings.TrimSpace(name)))
}

func (l *openAIWSIngressContextLease) IsPrewarmed() bool {
	if l == nil || l.context == nil {
		return false
	}
	return l.context.prewarmed.Load()
}

func (l *openAIWSIngressContextLease) MarkPrewarmed() {
	if l == nil || l.context == nil {
		return
	}
	l.context.prewarmed.Store(true)
}

func (l *openAIWSIngressContextLease) activeConn() (openAIWSClientConn, error) {
	if l == nil || l.context == nil || l.released.Load() {
		return nil, errOpenAIWSConnClosed
	}
	// Fast path: return cached conn without mutex if lease is still valid.
	l.cachedConnMu.RLock()
	cc := l.cachedConn
	l.cachedConnMu.RUnlock()
	if cc != nil {
		return cc, nil
	}
	// Slow path: acquire mutex, validate ownership, cache result.
	l.context.mu.Lock()
	defer l.context.mu.Unlock()
	if l.context.ownerID != l.ownerID {
		return nil, errOpenAIWSConnClosed
	}
	if l.context.upstream == nil {
		return nil, errOpenAIWSConnClosed
	}
	l.cachedConnMu.Lock()
	l.cachedConn = l.context.upstream
	l.cachedConnMu.Unlock()
	return l.context.upstream, nil
}

func (l *openAIWSIngressContextLease) invalidateCachedConnOnIOError(err error) {
	if l == nil || err == nil {
		return
	}
	l.cachedConnMu.Lock()
	l.cachedConn = nil
	l.cachedConnMu.Unlock()
	if l.pool != nil && l.context != nil && isOpenAIWSClientDisconnectError(err) {
		l.pool.markContextBroken(l.context)
	}
}

func (l *openAIWSIngressContextLease) WriteJSONWithContextTimeout(ctx context.Context, value any, timeout time.Duration) error {
	conn, err := l.activeConn()
	if err != nil {
		return err
	}
	writeCtx := ctx
	if writeCtx == nil {
		writeCtx = context.Background()
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		writeCtx, cancel = context.WithTimeout(writeCtx, timeout)
		defer cancel()
	}
	if err := conn.WriteJSON(writeCtx, value); err != nil {
		l.invalidateCachedConnOnIOError(err)
		return err
	}
	l.context.maybeTouchLease(l.pool.idleTTL)
	return nil
}

func (l *openAIWSIngressContextLease) ReadMessageWithContextTimeout(ctx context.Context, timeout time.Duration) ([]byte, error) {
	conn, err := l.activeConn()
	if err != nil {
		return nil, err
	}
	readCtx := ctx
	if readCtx == nil {
		readCtx = context.Background()
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		readCtx, cancel = context.WithTimeout(readCtx, timeout)
		defer cancel()
	}
	payload, err := conn.ReadMessage(readCtx)
	if err != nil {
		l.invalidateCachedConnOnIOError(err)
		return nil, err
	}
	l.context.maybeTouchLease(l.pool.idleTTL)
	return payload, nil
}

func (l *openAIWSIngressContextLease) PingWithTimeout(timeout time.Duration) error {
	conn, err := l.activeConn()
	if err != nil {
		return err
	}
	pingTimeout := timeout
	if pingTimeout <= 0 {
		pingTimeout = openAIWSConnHealthCheckTO
	}
	pingCtx, cancel := context.WithTimeout(context.Background(), pingTimeout)
	defer cancel()
	if err := conn.Ping(pingCtx); err != nil {
		l.invalidateCachedConnOnIOError(err)
		return err
	}
	l.context.maybeTouchLease(l.pool.idleTTL)
	return nil
}

func (l *openAIWSIngressContextLease) MarkBroken() {
	if l == nil || l.pool == nil || l.context == nil || l.released.Load() {
		return
	}
	l.cachedConnMu.Lock()
	l.cachedConn = nil
	l.cachedConnMu.Unlock()
	l.pool.markContextBroken(l.context)
}

func (l *openAIWSIngressContextLease) Release() {
	if l == nil || l.context == nil || l.pool == nil {
		return
	}
	if !l.released.CompareAndSwap(false, true) {
		return
	}
	l.cachedConnMu.Lock()
	l.cachedConn = nil
	l.cachedConnMu.Unlock()
	l.pool.releaseContext(l.context, l.ownerID)
}

func (l *openAIWSIngressContextLease) Yield() {
	if l == nil || l.context == nil || l.pool == nil {
		return
	}
	if !l.released.CompareAndSwap(false, true) {
		return
	}
	l.cachedConnMu.Lock()
	l.cachedConn = nil
	l.cachedConnMu.Unlock()
	l.pool.yieldContext(l.context, l.ownerID)
}
