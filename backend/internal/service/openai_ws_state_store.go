package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cespare/xxhash/v2"
)

const (
	openAIWSResponseAccountCachePrefix = "openai:response:"
	openAIWSStateStoreCleanupInterval  = time.Minute
	openAIWSStateStoreCleanupMaxPerMap = 512
	openAIWSStateStoreMaxEntriesPerMap = 65536
	openAIWSStateStoreRedisTimeout     = 3 * time.Second
	openAIWSStateStoreHotCacheTTL      = time.Minute
)

type openAIWSAccountBinding struct {
	accountID int64
	expiresAt time.Time
}

type openAIWSConnBinding struct {
	connID    string
	expiresAt time.Time
}

type openAIWSResponsePendingToolCallsBinding struct {
	callIDs   []string
	expiresAt time.Time
}

type openAIWSTurnStateBinding struct {
	turnState string
	expiresAt time.Time
}

type openAIWSSessionConnBinding struct {
	connID    string
	expiresAt time.Time
}

type openAIWSSessionLastResponseBinding struct {
	responseID string
	expiresAt  time.Time
}

type openAIWSStateStoreSessionLastResponseCache interface {
	SetOpenAIWSSessionLastResponseID(ctx context.Context, groupID int64, sessionHash, responseID string, ttl time.Duration) error
	GetOpenAIWSSessionLastResponseID(ctx context.Context, groupID int64, sessionHash string) (string, error)
	DeleteOpenAIWSSessionLastResponseID(ctx context.Context, groupID int64, sessionHash string) error
}

type openAIWSStateStoreResponsePendingToolCallsCache interface {
	SetOpenAIWSResponsePendingToolCalls(ctx context.Context, groupID int64, responseID string, callIDs []string, ttl time.Duration) error
	GetOpenAIWSResponsePendingToolCalls(ctx context.Context, groupID int64, responseID string) ([]string, error)
	DeleteOpenAIWSResponsePendingToolCalls(ctx context.Context, groupID int64, responseID string) error
}

// OpenAIWSStateStore 管理 WSv2 的粘连状态。
// - response_id -> account_id 用于续链路由
// - response_id -> conn_id 用于连接内上下文复用
//
// response_id -> account_id 优先走 GatewayCache（Redis），同时维护本地热缓存。
// response_id -> conn_id 仅在本进程内有效。
type OpenAIWSStateStore interface {
	BindResponseAccount(ctx context.Context, groupID int64, responseID string, accountID int64, ttl time.Duration) error
	GetResponseAccount(ctx context.Context, groupID int64, responseID string) (int64, error)
	DeleteResponseAccount(ctx context.Context, groupID int64, responseID string) error

	BindResponseConn(responseID, connID string, ttl time.Duration)
	GetResponseConn(responseID string) (string, bool)
	DeleteResponseConn(responseID string)
	BindResponsePendingToolCalls(groupID int64, responseID string, callIDs []string, ttl time.Duration)
	GetResponsePendingToolCalls(groupID int64, responseID string) ([]string, bool)
	DeleteResponsePendingToolCalls(groupID int64, responseID string)

	BindSessionTurnState(groupID int64, sessionHash, turnState string, ttl time.Duration)
	GetSessionTurnState(groupID int64, sessionHash string) (string, bool)
	DeleteSessionTurnState(groupID int64, sessionHash string)

	BindSessionLastResponseID(groupID int64, sessionHash, responseID string, ttl time.Duration)
	GetSessionLastResponseID(groupID int64, sessionHash string) (string, bool)
	DeleteSessionLastResponseID(groupID int64, sessionHash string)

	BindSessionConn(groupID int64, sessionHash, connID string, ttl time.Duration)
	GetSessionConn(groupID int64, sessionHash string) (string, bool)
	DeleteSessionConn(groupID int64, sessionHash string)
}

const openAIWSStateStoreConnShards = 16

type openAIWSConnBindingShard struct {
	mu sync.RWMutex
	m  map[string]openAIWSConnBinding
}

type defaultOpenAIWSStateStore struct {
	cache GatewayCache

	responseToAccountMu   sync.RWMutex
	responseToAccount     map[string]openAIWSAccountBinding
	responseToConnShards  [openAIWSStateStoreConnShards]openAIWSConnBindingShard
	responsePendingToolMu sync.RWMutex
	responsePendingTool   map[string]openAIWSResponsePendingToolCallsBinding
	sessionToTurnStateMu  sync.RWMutex
	sessionToTurnState    map[string]openAIWSTurnStateBinding
	sessionToLastRespMu   sync.RWMutex
	sessionToLastResp     map[string]openAIWSSessionLastResponseBinding
	sessionToConnMu       sync.RWMutex
	sessionToConn         map[string]openAIWSSessionConnBinding

	lastCleanupUnixNano atomic.Int64
	stopCh              chan struct{}
	stopOnce            sync.Once
	workerWg            sync.WaitGroup
}

func (s *defaultOpenAIWSStateStore) connShard(key string) *openAIWSConnBindingShard {
	h := xxhash.Sum64String(key)
	return &s.responseToConnShards[h%openAIWSStateStoreConnShards]
}

// NewOpenAIWSStateStore 创建默认 WS 状态存储。
func NewOpenAIWSStateStore(cache GatewayCache) OpenAIWSStateStore {
	return newOpenAIWSStateStore(cache, openAIWSStateStoreCleanupInterval)
}

func newOpenAIWSStateStore(cache GatewayCache, cleanupInterval time.Duration) *defaultOpenAIWSStateStore {
	store := &defaultOpenAIWSStateStore{
		cache:               cache,
		responseToAccount:   make(map[string]openAIWSAccountBinding, 256),
		responsePendingTool: make(map[string]openAIWSResponsePendingToolCallsBinding, 256),
		sessionToTurnState:  make(map[string]openAIWSTurnStateBinding, 256),
		sessionToLastResp:   make(map[string]openAIWSSessionLastResponseBinding, 256),
		sessionToConn:       make(map[string]openAIWSSessionConnBinding, 256),
		stopCh:              make(chan struct{}),
	}
	for i := range store.responseToConnShards {
		store.responseToConnShards[i].m = make(map[string]openAIWSConnBinding, 16)
	}
	store.lastCleanupUnixNano.Store(time.Now().UnixNano())
	store.startCleanupWorker(cleanupInterval)
	return store
}

func (s *defaultOpenAIWSStateStore) startCleanupWorker(interval time.Duration) {
	if s == nil || interval <= 0 {
		return
	}
	s.workerWg.Add(1)
	go func() {
		defer s.workerWg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-s.stopCh:
				return
			case <-ticker.C:
				s.maybeCleanup()
			}
		}
	}()
}

func (s *defaultOpenAIWSStateStore) Close() {
	if s == nil {
		return
	}
	s.stopOnce.Do(func() {
		if s.stopCh != nil {
			close(s.stopCh)
		}
	})
	s.workerWg.Wait()
}

func (s *defaultOpenAIWSStateStore) BindResponseAccount(ctx context.Context, groupID int64, responseID string, accountID int64, ttl time.Duration) error {
	id := normalizeOpenAIWSResponseID(responseID)
	if id == "" || accountID <= 0 {
		return nil
	}
	ttl = normalizeOpenAIWSTTL(ttl)
	s.maybeCleanup()

	if s.cache != nil {
		cacheKey := openAIWSResponseAccountCacheKey(id)
		cacheCtx, cancel := withOpenAIWSStateStoreRedisTimeout(ctx)
		err := s.cache.SetSessionAccountID(cacheCtx, groupID, cacheKey, accountID, ttl)
		cancel()
		if err != nil {
			return err
		}
	}

	// 本地仅保留短时热缓存，优先保证跨实例读取一致性。
	localTTL := openAIWSStateStoreLocalHotTTL(ttl)
	s.responseToAccountMu.Lock()
	ensureBindingCapacity(s.responseToAccount, id, openAIWSStateStoreMaxEntriesPerMap)
	s.responseToAccount[id] = openAIWSAccountBinding{
		accountID: accountID,
		expiresAt: time.Now().Add(localTTL),
	}
	s.responseToAccountMu.Unlock()

	return nil
}

func (s *defaultOpenAIWSStateStore) GetResponseAccount(ctx context.Context, groupID int64, responseID string) (int64, error) {
	id := normalizeOpenAIWSResponseID(responseID)
	if id == "" {
		return 0, nil
	}

	now := time.Now()
	s.responseToAccountMu.RLock()
	if binding, ok := s.responseToAccount[id]; ok {
		if now.Before(binding.expiresAt) {
			accountID := binding.accountID
			s.responseToAccountMu.RUnlock()
			return accountID, nil
		}
	}
	s.responseToAccountMu.RUnlock()

	if s.cache == nil {
		return 0, nil
	}

	cacheKey := openAIWSResponseAccountCacheKey(id)
	cacheCtx, cancel := withOpenAIWSStateStoreRedisTimeout(ctx)
	accountID, err := s.cache.GetSessionAccountID(cacheCtx, groupID, cacheKey)
	cancel()
	if err == nil && accountID > 0 {
		return accountID, nil
	}

	// Compatibility fallback for pre-v2 cache keys.
	legacyCacheKey := openAIWSResponseAccountLegacyCacheKey(id)
	legacyCtx, legacyCancel := withOpenAIWSStateStoreRedisTimeout(ctx)
	legacyAccountID, legacyErr := s.cache.GetSessionAccountID(legacyCtx, groupID, legacyCacheKey)
	legacyCancel()
	if legacyErr != nil || legacyAccountID <= 0 {
		// 缓存读取失败不阻断主流程，按未命中降级。
		return 0, nil
	}

	// Best effort: backfill v2 key so subsequent reads avoid legacy fallback.
	backfillCtx, backfillCancel := withOpenAIWSStateStoreRedisTimeout(ctx)
	_ = s.cache.SetSessionAccountID(backfillCtx, groupID, cacheKey, legacyAccountID, openAIWSStateStoreHotCacheTTL)
	backfillCancel()

	return legacyAccountID, nil
}

func (s *defaultOpenAIWSStateStore) DeleteResponseAccount(ctx context.Context, groupID int64, responseID string) error {
	id := normalizeOpenAIWSResponseID(responseID)
	if id == "" {
		return nil
	}
	s.responseToAccountMu.Lock()
	delete(s.responseToAccount, id)
	s.responseToAccountMu.Unlock()

	if s.cache == nil {
		return nil
	}
	cacheCtx, cancel := withOpenAIWSStateStoreRedisTimeout(ctx)
	defer cancel()
	primaryKey := openAIWSResponseAccountCacheKey(id)
	if err := s.cache.DeleteSessionAccountID(cacheCtx, groupID, primaryKey); err != nil {
		return err
	}
	legacyKey := openAIWSResponseAccountLegacyCacheKey(id)
	if legacyKey == "" || legacyKey == primaryKey {
		return nil
	}
	return s.cache.DeleteSessionAccountID(cacheCtx, groupID, legacyKey)
}

func (s *defaultOpenAIWSStateStore) BindResponseConn(responseID, connID string, ttl time.Duration) {
	id := normalizeOpenAIWSResponseID(responseID)
	conn := strings.TrimSpace(connID)
	if id == "" || conn == "" {
		return
	}
	ttl = normalizeOpenAIWSTTL(ttl)
	s.maybeCleanup()

	shard := s.connShard(id)
	shard.mu.Lock()
	ensureBindingCapacity(shard.m, id, openAIWSStateStoreMaxEntriesPerMap/openAIWSStateStoreConnShards)
	shard.m[id] = openAIWSConnBinding{
		connID:    conn,
		expiresAt: time.Now().Add(ttl),
	}
	shard.mu.Unlock()
}

func (s *defaultOpenAIWSStateStore) GetResponseConn(responseID string) (string, bool) {
	id := normalizeOpenAIWSResponseID(responseID)
	if id == "" {
		return "", false
	}

	now := time.Now()
	shard := s.connShard(id)
	shard.mu.RLock()
	binding, ok := shard.m[id]
	shard.mu.RUnlock()
	if !ok || now.After(binding.expiresAt) || binding.connID == "" {
		return "", false
	}
	return binding.connID, true
}

func (s *defaultOpenAIWSStateStore) DeleteResponseConn(responseID string) {
	id := normalizeOpenAIWSResponseID(responseID)
	if id == "" {
		return
	}
	shard := s.connShard(id)
	shard.mu.Lock()
	delete(shard.m, id)
	shard.mu.Unlock()
}

func (s *defaultOpenAIWSStateStore) BindResponsePendingToolCalls(groupID int64, responseID string, callIDs []string, ttl time.Duration) {
	id := normalizeOpenAIWSResponseID(responseID)
	normalizedCallIDs := normalizeOpenAIWSPendingToolCallIDs(callIDs)
	if id == "" || len(normalizedCallIDs) == 0 {
		return
	}
	key := openAIWSResponsePendingToolCallsBindingKey(groupID, id)
	if key == "" {
		return
	}
	ttl = normalizeOpenAIWSTTL(ttl)
	s.maybeCleanup()

	s.responsePendingToolMu.Lock()
	ensureBindingCapacity(s.responsePendingTool, key, openAIWSStateStoreMaxEntriesPerMap)
	s.responsePendingTool[key] = openAIWSResponsePendingToolCallsBinding{
		callIDs:   append([]string(nil), normalizedCallIDs...),
		expiresAt: time.Now().Add(ttl),
	}
	s.responsePendingToolMu.Unlock()

	if cache := s.responsePendingToolCallsCache(); cache != nil {
		cacheCtx, cancel := withOpenAIWSStateStoreRedisTimeout(context.Background())
		defer cancel()
		_ = cache.SetOpenAIWSResponsePendingToolCalls(cacheCtx, groupID, id, normalizedCallIDs, ttl)
	}
}

func (s *defaultOpenAIWSStateStore) GetResponsePendingToolCalls(groupID int64, responseID string) ([]string, bool) {
	id := normalizeOpenAIWSResponseID(responseID)
	if id == "" {
		return nil, false
	}
	key := openAIWSResponsePendingToolCallsBindingKey(groupID, id)
	if key == "" {
		return nil, false
	}

	now := time.Now()
	s.responsePendingToolMu.RLock()
	binding, ok := s.responsePendingTool[key]
	s.responsePendingToolMu.RUnlock()
	if !ok || now.After(binding.expiresAt) || len(binding.callIDs) == 0 {
		cache := s.responsePendingToolCallsCache()
		if cache == nil {
			return nil, false
		}
		cacheCtx, cancel := withOpenAIWSStateStoreRedisTimeout(context.Background())
		defer cancel()
		callIDs, err := cache.GetOpenAIWSResponsePendingToolCalls(cacheCtx, groupID, id)
		normalizedCallIDs := normalizeOpenAIWSPendingToolCallIDs(callIDs)
		if err != nil || len(normalizedCallIDs) == 0 {
			return nil, false
		}

		// Redis 命中后回填本地热缓存，降低后续访问开销。
		s.responsePendingToolMu.Lock()
		ensureBindingCapacity(s.responsePendingTool, key, openAIWSStateStoreMaxEntriesPerMap)
		s.responsePendingTool[key] = openAIWSResponsePendingToolCallsBinding{
			callIDs:   append([]string(nil), normalizedCallIDs...),
			expiresAt: time.Now().Add(openAIWSStateStoreHotCacheTTL),
		}
		s.responsePendingToolMu.Unlock()
		return normalizedCallIDs, true
	}
	// binding.callIDs was already copied at bind time; return directly (callers are read-only).
	return binding.callIDs, true
}

func (s *defaultOpenAIWSStateStore) DeleteResponsePendingToolCalls(groupID int64, responseID string) {
	id := normalizeOpenAIWSResponseID(responseID)
	if id == "" {
		return
	}
	key := openAIWSResponsePendingToolCallsBindingKey(groupID, id)
	if key == "" {
		return
	}
	s.responsePendingToolMu.Lock()
	delete(s.responsePendingTool, key)
	s.responsePendingToolMu.Unlock()

	if cache := s.responsePendingToolCallsCache(); cache != nil {
		cacheCtx, cancel := withOpenAIWSStateStoreRedisTimeout(context.Background())
		defer cancel()
		_ = cache.DeleteOpenAIWSResponsePendingToolCalls(cacheCtx, groupID, id)
	}
}

func (s *defaultOpenAIWSStateStore) BindSessionTurnState(groupID int64, sessionHash, turnState string, ttl time.Duration) {
	key := openAIWSSessionTurnStateKey(groupID, sessionHash)
	state := strings.TrimSpace(turnState)
	if key == "" || state == "" {
		return
	}
	ttl = normalizeOpenAIWSTTL(ttl)
	s.maybeCleanup()

	s.sessionToTurnStateMu.Lock()
	ensureBindingCapacity(s.sessionToTurnState, key, openAIWSStateStoreMaxEntriesPerMap)
	s.sessionToTurnState[key] = openAIWSTurnStateBinding{
		turnState: state,
		expiresAt: time.Now().Add(ttl),
	}
	s.sessionToTurnStateMu.Unlock()
}

func (s *defaultOpenAIWSStateStore) GetSessionTurnState(groupID int64, sessionHash string) (string, bool) {
	key := openAIWSSessionTurnStateKey(groupID, sessionHash)
	if key == "" {
		return "", false
	}

	now := time.Now()
	s.sessionToTurnStateMu.RLock()
	binding, ok := s.sessionToTurnState[key]
	s.sessionToTurnStateMu.RUnlock()
	if !ok || now.After(binding.expiresAt) || strings.TrimSpace(binding.turnState) == "" {
		return "", false
	}
	return binding.turnState, true
}

func (s *defaultOpenAIWSStateStore) DeleteSessionTurnState(groupID int64, sessionHash string) {
	key := openAIWSSessionTurnStateKey(groupID, sessionHash)
	if key == "" {
		return
	}
	s.sessionToTurnStateMu.Lock()
	delete(s.sessionToTurnState, key)
	s.sessionToTurnStateMu.Unlock()
}

func (s *defaultOpenAIWSStateStore) BindSessionLastResponseID(groupID int64, sessionHash, responseID string, ttl time.Duration) {
	key := openAIWSSessionTurnStateKey(groupID, sessionHash)
	id := normalizeOpenAIWSResponseID(responseID)
	if key == "" || id == "" {
		return
	}
	ttl = normalizeOpenAIWSTTL(ttl)
	s.maybeCleanup()

	s.sessionToLastRespMu.Lock()
	ensureBindingCapacity(s.sessionToLastResp, key, openAIWSStateStoreMaxEntriesPerMap)
	s.sessionToLastResp[key] = openAIWSSessionLastResponseBinding{
		responseID: id,
		expiresAt:  time.Now().Add(ttl),
	}
	s.sessionToLastRespMu.Unlock()

	if cache := s.sessionLastResponseCache(); cache != nil {
		cacheCtx, cancel := withOpenAIWSStateStoreRedisTimeout(context.Background())
		defer cancel()
		_ = cache.SetOpenAIWSSessionLastResponseID(cacheCtx, groupID, strings.TrimSpace(sessionHash), id, ttl)
	}
}

func (s *defaultOpenAIWSStateStore) GetSessionLastResponseID(groupID int64, sessionHash string) (string, bool) {
	key := openAIWSSessionTurnStateKey(groupID, sessionHash)
	if key == "" {
		return "", false
	}

	now := time.Now()
	s.sessionToLastRespMu.RLock()
	binding, ok := s.sessionToLastResp[key]
	s.sessionToLastRespMu.RUnlock()
	if ok && now.Before(binding.expiresAt) && strings.TrimSpace(binding.responseID) != "" {
		return binding.responseID, true
	}

	cache := s.sessionLastResponseCache()
	if cache == nil {
		return "", false
	}
	cacheCtx, cancel := withOpenAIWSStateStoreRedisTimeout(context.Background())
	defer cancel()
	responseID, err := cache.GetOpenAIWSSessionLastResponseID(cacheCtx, groupID, strings.TrimSpace(sessionHash))
	responseID = normalizeOpenAIWSResponseID(responseID)
	if err != nil || responseID == "" {
		return "", false
	}

	// Redis 命中后回填本地热缓存，降低后续访问开销。
	s.sessionToLastRespMu.Lock()
	ensureBindingCapacity(s.sessionToLastResp, key, openAIWSStateStoreMaxEntriesPerMap)
	s.sessionToLastResp[key] = openAIWSSessionLastResponseBinding{
		responseID: responseID,
		expiresAt:  time.Now().Add(openAIWSStateStoreHotCacheTTL),
	}
	s.sessionToLastRespMu.Unlock()
	return responseID, true
}

func (s *defaultOpenAIWSStateStore) DeleteSessionLastResponseID(groupID int64, sessionHash string) {
	key := openAIWSSessionTurnStateKey(groupID, sessionHash)
	if key == "" {
		return
	}
	s.sessionToLastRespMu.Lock()
	delete(s.sessionToLastResp, key)
	s.sessionToLastRespMu.Unlock()

	if cache := s.sessionLastResponseCache(); cache != nil {
		cacheCtx, cancel := withOpenAIWSStateStoreRedisTimeout(context.Background())
		defer cancel()
		_ = cache.DeleteOpenAIWSSessionLastResponseID(cacheCtx, groupID, strings.TrimSpace(sessionHash))
	}
}

func (s *defaultOpenAIWSStateStore) BindSessionConn(groupID int64, sessionHash, connID string, ttl time.Duration) {
	key := openAIWSSessionTurnStateKey(groupID, sessionHash)
	conn := strings.TrimSpace(connID)
	if key == "" || conn == "" {
		return
	}
	ttl = normalizeOpenAIWSTTL(ttl)
	s.maybeCleanup()

	s.sessionToConnMu.Lock()
	ensureBindingCapacity(s.sessionToConn, key, openAIWSStateStoreMaxEntriesPerMap)
	s.sessionToConn[key] = openAIWSSessionConnBinding{
		connID:    conn,
		expiresAt: time.Now().Add(ttl),
	}
	s.sessionToConnMu.Unlock()
}

func (s *defaultOpenAIWSStateStore) GetSessionConn(groupID int64, sessionHash string) (string, bool) {
	key := openAIWSSessionTurnStateKey(groupID, sessionHash)
	if key == "" {
		return "", false
	}

	now := time.Now()
	s.sessionToConnMu.RLock()
	binding, ok := s.sessionToConn[key]
	s.sessionToConnMu.RUnlock()
	if !ok || now.After(binding.expiresAt) || strings.TrimSpace(binding.connID) == "" {
		return "", false
	}
	return binding.connID, true
}

func (s *defaultOpenAIWSStateStore) DeleteSessionConn(groupID int64, sessionHash string) {
	key := openAIWSSessionTurnStateKey(groupID, sessionHash)
	if key == "" {
		return
	}
	s.sessionToConnMu.Lock()
	delete(s.sessionToConn, key)
	s.sessionToConnMu.Unlock()
}

func (s *defaultOpenAIWSStateStore) maybeCleanup() {
	if s == nil {
		return
	}
	now := time.Now()
	last := time.Unix(0, s.lastCleanupUnixNano.Load())
	if now.Sub(last) < openAIWSStateStoreCleanupInterval {
		return
	}
	if !s.lastCleanupUnixNano.CompareAndSwap(last.UnixNano(), now.UnixNano()) {
		return
	}

	// 增量限额清理，避免高规模下一次性全量扫描导致长时间阻塞。
	s.responseToAccountMu.Lock()
	cleanupExpiredAccountBindings(s.responseToAccount, now, openAIWSStateStoreCleanupMaxPerMap)
	s.responseToAccountMu.Unlock()

	perShardLimit := openAIWSStateStoreCleanupMaxPerMap / openAIWSStateStoreConnShards
	if perShardLimit < 32 {
		perShardLimit = 32
	}
	for i := range s.responseToConnShards {
		shard := &s.responseToConnShards[i]
		shard.mu.Lock()
		cleanupExpiredConnBindings(shard.m, now, perShardLimit)
		shard.mu.Unlock()
	}

	s.responsePendingToolMu.Lock()
	cleanupExpiredResponsePendingToolCallsBindings(s.responsePendingTool, now, openAIWSStateStoreCleanupMaxPerMap)
	s.responsePendingToolMu.Unlock()

	s.sessionToTurnStateMu.Lock()
	cleanupExpiredTurnStateBindings(s.sessionToTurnState, now, openAIWSStateStoreCleanupMaxPerMap)
	s.sessionToTurnStateMu.Unlock()

	s.sessionToLastRespMu.Lock()
	cleanupExpiredSessionLastResponseBindings(s.sessionToLastResp, now, openAIWSStateStoreCleanupMaxPerMap)
	s.sessionToLastRespMu.Unlock()

	s.sessionToConnMu.Lock()
	cleanupExpiredSessionConnBindings(s.sessionToConn, now, openAIWSStateStoreCleanupMaxPerMap)
	s.sessionToConnMu.Unlock()
}

func cleanupExpiredAccountBindings(bindings map[string]openAIWSAccountBinding, now time.Time, maxScan int) {
	if len(bindings) == 0 || maxScan <= 0 {
		return
	}
	scanned := 0
	for key, binding := range bindings {
		if now.After(binding.expiresAt) {
			delete(bindings, key)
		}
		scanned++
		if scanned >= maxScan {
			break
		}
	}
}

func cleanupExpiredConnBindings(bindings map[string]openAIWSConnBinding, now time.Time, maxScan int) {
	if len(bindings) == 0 || maxScan <= 0 {
		return
	}
	scanned := 0
	for key, binding := range bindings {
		if now.After(binding.expiresAt) {
			delete(bindings, key)
		}
		scanned++
		if scanned >= maxScan {
			break
		}
	}
}

func cleanupExpiredResponsePendingToolCallsBindings(bindings map[string]openAIWSResponsePendingToolCallsBinding, now time.Time, maxScan int) {
	if len(bindings) == 0 || maxScan <= 0 {
		return
	}
	scanned := 0
	for key, binding := range bindings {
		if now.After(binding.expiresAt) {
			delete(bindings, key)
		}
		scanned++
		if scanned >= maxScan {
			break
		}
	}
}

func cleanupExpiredTurnStateBindings(bindings map[string]openAIWSTurnStateBinding, now time.Time, maxScan int) {
	if len(bindings) == 0 || maxScan <= 0 {
		return
	}
	scanned := 0
	for key, binding := range bindings {
		if now.After(binding.expiresAt) {
			delete(bindings, key)
		}
		scanned++
		if scanned >= maxScan {
			break
		}
	}
}

func cleanupExpiredSessionLastResponseBindings(bindings map[string]openAIWSSessionLastResponseBinding, now time.Time, maxScan int) {
	if len(bindings) == 0 || maxScan <= 0 {
		return
	}
	scanned := 0
	for key, binding := range bindings {
		if now.After(binding.expiresAt) {
			delete(bindings, key)
		}
		scanned++
		if scanned >= maxScan {
			break
		}
	}
}

func cleanupExpiredSessionConnBindings(bindings map[string]openAIWSSessionConnBinding, now time.Time, maxScan int) {
	if len(bindings) == 0 || maxScan <= 0 {
		return
	}
	scanned := 0
	for key, binding := range bindings {
		if now.After(binding.expiresAt) {
			delete(bindings, key)
		}
		scanned++
		if scanned >= maxScan {
			break
		}
	}
}

type expiringBinding interface {
	getExpiresAt() time.Time
}

func (b openAIWSAccountBinding) getExpiresAt() time.Time                  { return b.expiresAt }
func (b openAIWSConnBinding) getExpiresAt() time.Time                     { return b.expiresAt }
func (b openAIWSResponsePendingToolCallsBinding) getExpiresAt() time.Time { return b.expiresAt }
func (b openAIWSTurnStateBinding) getExpiresAt() time.Time                { return b.expiresAt }
func (b openAIWSSessionConnBinding) getExpiresAt() time.Time              { return b.expiresAt }
func (b openAIWSSessionLastResponseBinding) getExpiresAt() time.Time      { return b.expiresAt }

func ensureBindingCapacity[T expiringBinding](bindings map[string]T, incomingKey string, maxEntries int) {
	if len(bindings) < maxEntries || maxEntries <= 0 {
		return
	}
	if _, exists := bindings[incomingKey]; exists {
		return
	}
	// 优先驱逐已过期条目；若不存在过期项，则按 expiresAt 最早驱逐，避免随机删除活跃绑定。
	now := time.Now()
	for key, val := range bindings {
		if !val.getExpiresAt().IsZero() && now.After(val.getExpiresAt()) {
			delete(bindings, key)
			return
		}
	}
	var (
		evictKey      string
		evictExpireAt time.Time
		hasCandidate  bool
	)
	for key, val := range bindings {
		expiresAt := val.getExpiresAt()
		if !hasCandidate {
			evictKey = key
			evictExpireAt = expiresAt
			hasCandidate = true
			continue
		}
		switch {
		case expiresAt.IsZero() && !evictExpireAt.IsZero():
			evictKey = key
			evictExpireAt = expiresAt
		case !expiresAt.IsZero() && !evictExpireAt.IsZero() && expiresAt.Before(evictExpireAt):
			evictKey = key
			evictExpireAt = expiresAt
		}
	}
	if hasCandidate {
		delete(bindings, evictKey)
	}
}

func normalizeOpenAIWSResponseID(responseID string) string {
	return strings.TrimSpace(responseID)
}

func normalizeOpenAIWSPendingToolCallIDs(callIDs []string) []string {
	if len(callIDs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(callIDs))
	normalized := make([]string, 0, len(callIDs))
	for _, callID := range callIDs {
		id := strings.TrimSpace(callID)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	return normalized
}

func openAIWSResponsePendingToolCallsBindingKey(groupID int64, responseID string) string {
	id := normalizeOpenAIWSResponseID(responseID)
	if id == "" {
		return ""
	}
	return strconv.FormatInt(groupID, 10) + ":" + id
}

func openAIWSResponseAccountCacheKey(responseID string) string {
	h := xxhash.Sum64String(responseID)
	// Pad to 16 hex chars for consistent key length.
	hex := strconv.FormatUint(h, 16)
	const pad = "0000000000000000"
	if len(hex) < 16 {
		hex = pad[:16-len(hex)] + hex
	}
	return openAIWSResponseAccountCachePrefix + "v2:" + hex
}

func openAIWSResponseAccountLegacyCacheKey(responseID string) string {
	sum := sha256.Sum256([]byte(responseID))
	return openAIWSResponseAccountCachePrefix + hex.EncodeToString(sum[:])
}

func normalizeOpenAIWSTTL(ttl time.Duration) time.Duration {
	if ttl <= 0 {
		return time.Hour
	}
	return ttl
}

func openAIWSStateStoreLocalHotTTL(ttl time.Duration) time.Duration {
	ttl = normalizeOpenAIWSTTL(ttl)
	if ttl > openAIWSStateStoreHotCacheTTL {
		return openAIWSStateStoreHotCacheTTL
	}
	return ttl
}

func (s *defaultOpenAIWSStateStore) sessionLastResponseCache() openAIWSStateStoreSessionLastResponseCache {
	if s == nil || s.cache == nil {
		return nil
	}
	cache, ok := s.cache.(openAIWSStateStoreSessionLastResponseCache)
	if !ok {
		return nil
	}
	return cache
}

func (s *defaultOpenAIWSStateStore) responsePendingToolCallsCache() openAIWSStateStoreResponsePendingToolCallsCache {
	if s == nil || s.cache == nil {
		return nil
	}
	cache, ok := s.cache.(openAIWSStateStoreResponsePendingToolCallsCache)
	if !ok {
		return nil
	}
	return cache
}

func openAIWSSessionTurnStateKey(groupID int64, sessionHash string) string {
	hash := strings.TrimSpace(sessionHash)
	if hash == "" {
		return ""
	}
	return strconv.FormatInt(groupID, 10) + ":" + hash
}

func withOpenAIWSStateStoreRedisTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(ctx, openAIWSStateStoreRedisTimeout)
}
