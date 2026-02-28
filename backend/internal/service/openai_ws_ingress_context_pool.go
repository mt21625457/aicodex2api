package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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

	idleTTL       time.Duration
	sweepInterval time.Duration

	seq atomic.Uint64

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

	contexts  map[string]*openAIWSIngressContext
	bySession map[string]string
}

type openAIWSIngressContext struct {
	id          string
	groupID     int64
	accountID   int64
	sessionHash string
	sessionKey  string

	mu               sync.Mutex
	dialing          bool
	dialDone         chan struct{}
	ownerID          string
	ownerLeaseAt     time.Time
	lastUsedAt       time.Time
	expiresAt        time.Time
	broken           bool
	failureStreak    int
	lastFailureAt    time.Time
	migrationCount   int
	lastMigrationAt  time.Time
	upstream         openAIWSClientConn
	upstreamConnID   string
	handshakeHeaders http.Header
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
}

func newOpenAIWSIngressContextPool(cfg *config.Config) *openAIWSIngressContextPool {
	pool := &openAIWSIngressContextPool{
		cfg:           cfg,
		dialer:        newDefaultOpenAIWSClientDialer(),
		idleTTL:       10 * time.Minute,
		sweepInterval: 30 * time.Second,
		accounts:      make(map[int64]*openAIWSIngressAccountPool),
		stopCh:        make(chan struct{}),
	}
	if cfg != nil && cfg.Gateway.OpenAIWS.StickySessionTTLSeconds > 0 {
		pool.idleTTL = time.Duration(cfg.Gateway.OpenAIWS.StickySessionTTLSeconds) * time.Second
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
	if req.Account.Concurrency <= 0 {
		return nil, errOpenAIWSConnQueueFull
	}

	sessionHash := strings.TrimSpace(req.SessionHash)
	if sessionHash == "" {
		// 无会话信号时退化为连接级上下文，避免跨连接串会话。
		sessionHash = "conn:" + ownerID
	}
	sessionKey := openAIWSIngressContextSessionKey(req.GroupID, sessionHash)
	accountID := req.Account.ID
	capacity := req.Account.Concurrency // 硬约束：池容量 == 账号并发

	start := time.Now()
	now := time.Now()
	var (
		selected      *openAIWSIngressContext
		reusedContext bool
		newlyCreated  bool
		ownerAssigned bool
		migrationUsed bool
		scheduleLayer string
		oldUpstream   openAIWSClientConn
	)

	p.mu.Lock()
	ap := p.getOrCreateAccountPoolLocked(accountID)
	ap.refs.Add(1)
	p.mu.Unlock()
	defer ap.refs.Add(-1)

	ap.mu.Lock()
	p.cleanupAccountExpiredLocked(ap, now)

	stickiness := p.resolveStickinessLevelLocked(ap, sessionKey, req, now)
	allowMigration, minMigrationScore := openAIWSIngressMigrationPolicyByStickiness(stickiness)

	if existingID, ok := ap.bySession[sessionKey]; ok {
		if existing := ap.contexts[existingID]; existing != nil {
			existing.mu.Lock()
			switch existing.ownerID {
			case "":
				existing.ownerID = ownerID
				ownerAssigned = true
				existing.ownerLeaseAt = now
				existing.lastUsedAt = now
				existing.expiresAt = now.Add(p.idleTTL)
				selected = existing
				reusedContext = true
				scheduleLayer = openAIWSIngressScheduleLayerExact
			case ownerID:
				existing.ownerLeaseAt = now
				existing.lastUsedAt = now
				existing.expiresAt = now.Add(p.idleTTL)
				selected = existing
				reusedContext = true
				scheduleLayer = openAIWSIngressScheduleLayerExact
			default:
				existing.mu.Unlock()
				ap.mu.Unlock()
				return nil, errOpenAIWSIngressContextBusy
			}
			existing.mu.Unlock()
		}
	}

	if selected == nil {
		if len(ap.contexts) >= capacity {
			p.evictExpiredIdleLocked(ap, now)
		}
		if len(ap.contexts) >= capacity {
			if !allowMigration {
				ap.mu.Unlock()
				return nil, errOpenAIWSConnQueueFull
			}

			recycle := p.pickMigrationCandidateLocked(ap, minMigrationScore, now)
			if recycle == nil {
				ap.mu.Unlock()
				return nil, errOpenAIWSConnQueueFull
			}
			recycle.mu.Lock()
			oldSessionKey := recycle.sessionKey
			oldUpstream = recycle.upstream
			recycle.sessionHash = sessionHash
			recycle.sessionKey = sessionKey
			recycle.groupID = req.GroupID
			recycle.ownerID = ownerID
			recycle.ownerLeaseAt = now
			recycle.lastUsedAt = now
			recycle.expiresAt = now.Add(p.idleTTL)
			// 会话被回收复用时关闭旧上游，避免跨会话污染。
			recycle.upstream = nil
			recycle.upstreamConnID = ""
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
			if oldUpstream != nil {
				_ = oldUpstream.Close()
			}
			connPick := time.Since(start)
			reusedConn, ensureErr := p.ensureContextUpstream(ctx, selected, req)
			if ensureErr != nil {
				p.releaseContext(selected, ownerID)
				return nil, ensureErr
			}
			return &openAIWSIngressContextLease{
				pool:          p,
				context:       selected,
				ownerID:       ownerID,
				connPick:      connPick,
				reused:        reusedContext && reusedConn,
				scheduleLayer: scheduleLayer,
				stickiness:    stickiness,
				migrationUsed: migrationUsed,
			}, nil
		}

		ctxID := fmt.Sprintf("ctx_%d_%d", accountID, p.seq.Add(1))
		created := &openAIWSIngressContext{
			id:           ctxID,
			groupID:      req.GroupID,
			accountID:    accountID,
			sessionHash:  sessionHash,
			sessionKey:   sessionKey,
			ownerID:      ownerID,
			ownerLeaseAt: now,
			lastUsedAt:   now,
			expiresAt:    now.Add(p.idleTTL),
		}
		ap.contexts[ctxID] = created
		ap.bySession[sessionKey] = ctxID
		selected = created
		newlyCreated = true
		ownerAssigned = true
		scheduleLayer = openAIWSIngressScheduleLayerNew
	}
	ap.mu.Unlock()

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

	connPick := time.Since(start)
	return &openAIWSIngressContextLease{
		pool:          p,
		context:       selected,
		ownerID:       ownerID,
		connPick:      connPick,
		reused:        reusedContext && reusedConn,
		scheduleLayer: scheduleLayer,
		stickiness:    stickiness,
		migrationUsed: migrationUsed,
	}, nil
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
	lastUsedAt := existing.lastUsedAt
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
	switch strings.TrimSpace(stickiness) {
	case openAIWSIngressStickinessStrong:
		return false, 85
	case openAIWSIngressStickinessBalanced:
		return true, 68
	default:
		return true, 45
	}
}

func openAIWSIngressStickinessDowngrade(level string) string {
	switch strings.TrimSpace(level) {
	case openAIWSIngressStickinessStrong:
		return openAIWSIngressStickinessBalanced
	case openAIWSIngressStickinessBalanced:
		return openAIWSIngressStickinessWeak
	default:
		return openAIWSIngressStickinessWeak
	}
}

func openAIWSIngressStickinessUpgrade(level string) string {
	switch strings.TrimSpace(level) {
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
		score, lastUsed, ok := scoreOpenAIWSIngressMigrationCandidate(ctx, now)
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

func scoreOpenAIWSIngressMigrationCandidate(c *openAIWSIngressContext, now time.Time) (float64, time.Time, bool) {
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

	idleDuration := now.Sub(c.lastUsedAt)
	switch {
	case idleDuration <= 15*time.Second:
		score -= 15
	case idleDuration >= 3*time.Minute:
		score += 16
	default:
		score += idleDuration.Seconds() / 12.0
	}

	return score, c.lastUsedAt, true
}

func minInt(a, b int) int {
	if a <= b {
		return a
	}
	return b
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
			c.lastUsedAt = now
			c.ownerLeaseAt = now
			c.expiresAt = now.Add(p.idleTTL)
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
		c.handshakeHeaders = nil
		c.upstreamConnID = ""
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
		conn, _, handshakeHeaders, err := dialer.Dial(ctx, req.WSURL, req.Headers, req.ProxyURL)
		if err != nil {
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
			return false, err
		}

		c.mu.Lock()
		now := time.Now()
		c.upstream = conn
		c.upstreamConnID = fmt.Sprintf("ctxws_%d_%d", c.accountID, p.seq.Add(1))
		c.handshakeHeaders = cloneHeader(handshakeHeaders)
		c.lastUsedAt = now
		c.ownerLeaseAt = now
		c.expiresAt = now.Add(p.idleTTL)
		c.broken = false
		c.failureStreak = 0
		c.lastFailureAt = time.Time{}
		c.dialing = false
		if c.dialDone == dialDone {
			c.dialDone = nil
		}
		close(dialDone)
		c.mu.Unlock()
		return false, nil
	}
}

func (p *openAIWSIngressContextPool) releaseContext(c *openAIWSIngressContext, ownerID string) {
	if p == nil || c == nil {
		return
	}
	var upstream openAIWSClientConn
	c.mu.Lock()
	if c.ownerID == ownerID {
		// ctx_pool 模式下每次客户端会话结束都关闭上游连接，
		// 下一次同会话重新获取时必须新建上游 ws。
		upstream = c.upstream
		c.upstream = nil
		c.handshakeHeaders = nil
		c.upstreamConnID = ""
		c.ownerID = ""
		c.ownerLeaseAt = time.Time{}
		c.lastUsedAt = time.Now()
		c.expiresAt = time.Now().Add(p.idleTTL)
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
	c.handshakeHeaders = nil
	c.upstreamConnID = ""
	c.broken = true
	c.failureStreak++
	c.lastFailureAt = time.Now()
	c.mu.Unlock()
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
	p.accounts[accountID] = ap
	return ap
}

func (p *openAIWSIngressContextPool) cleanupAccountExpiredLocked(ap *openAIWSIngressAccountPool, now time.Time) {
	if ap == nil {
		return
	}
	for id, ctx := range ap.contexts {
		if ctx == nil {
			delete(ap.contexts, id)
			continue
		}
		ctx.mu.Lock()
		expired := ctx.ownerID == "" && !ctx.expiresAt.IsZero() && now.After(ctx.expiresAt)
		ctx.mu.Unlock()
		if !expired {
			continue
		}
		ctx.mu.Lock()
		upstream := ctx.upstream
		if expired {
			ctx.upstream = nil
			ctx.handshakeHeaders = nil
			ctx.upstreamConnID = ""
		}
		ctx.mu.Unlock()
		delete(ap.contexts, id)
		if mappedID, ok := ap.bySession[ctx.sessionKey]; ok && mappedID == id {
			delete(ap.bySession, ctx.sessionKey)
		}
		if upstream != nil {
			_ = upstream.Close()
		}
	}
}

func (p *openAIWSIngressContextPool) evictExpiredIdleLocked(ap *openAIWSIngressAccountPool, now time.Time) {
	if ap == nil {
		return
	}
	for id, ctx := range ap.contexts {
		if ctx == nil {
			delete(ap.contexts, id)
			continue
		}
		ctx.mu.Lock()
		expired := ctx.ownerID == "" && !ctx.expiresAt.IsZero() && now.After(ctx.expiresAt)
		upstream := ctx.upstream
		if expired {
			ctx.upstream = nil
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
			_ = upstream.Close()
		}
	}
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
		lastUsed := ctx.lastUsedAt
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
		p.evictExpiredIdleLocked(ap, now)
		empty := len(ap.contexts) == 0
		ap.mu.Unlock()
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
	return fmt.Sprintf("%d:%s", groupID, strings.TrimSpace(sessionHash))
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

func (l *openAIWSIngressContextLease) activeConn() (openAIWSClientConn, error) {
	if l == nil || l.context == nil || l.released.Load() {
		return nil, errOpenAIWSConnClosed
	}
	l.context.mu.Lock()
	defer l.context.mu.Unlock()
	if l.context.ownerID != l.ownerID {
		return nil, errOpenAIWSConnClosed
	}
	if l.context.upstream == nil {
		return nil, errOpenAIWSConnClosed
	}
	return l.context.upstream, nil
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
		return err
	}
	l.context.mu.Lock()
	now := time.Now()
	l.context.lastUsedAt = now
	l.context.ownerLeaseAt = now
	l.context.expiresAt = now.Add(l.pool.idleTTL)
	l.context.mu.Unlock()
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
		return nil, err
	}
	l.context.mu.Lock()
	now := time.Now()
	l.context.lastUsedAt = now
	l.context.ownerLeaseAt = now
	l.context.expiresAt = now.Add(l.pool.idleTTL)
	l.context.mu.Unlock()
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
		return err
	}
	l.context.mu.Lock()
	now := time.Now()
	l.context.lastUsedAt = now
	l.context.ownerLeaseAt = now
	l.context.expiresAt = now.Add(l.pool.idleTTL)
	l.context.mu.Unlock()
	return nil
}

func (l *openAIWSIngressContextLease) MarkBroken() {
	if l == nil || l.pool == nil || l.context == nil || l.released.Load() {
		return
	}
	l.pool.markContextBroken(l.context)
}

func (l *openAIWSIngressContextLease) Release() {
	if l == nil || l.context == nil || l.pool == nil {
		return
	}
	if !l.released.CompareAndSwap(false, true) {
		return
	}
	l.pool.releaseContext(l.context, l.ownerID)
}
