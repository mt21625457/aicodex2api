package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOpenAIWSStateStore_BindGetDeleteResponseAccount(t *testing.T) {
	cache := &stubGatewayCache{}
	store := NewOpenAIWSStateStore(cache)
	ctx := context.Background()
	groupID := int64(7)

	require.NoError(t, store.BindResponseAccount(ctx, groupID, "resp_abc", 101, time.Minute))

	accountID, err := store.GetResponseAccount(ctx, groupID, "resp_abc")
	require.NoError(t, err)
	require.Equal(t, int64(101), accountID)

	require.NoError(t, store.DeleteResponseAccount(ctx, groupID, "resp_abc"))
	accountID, err = store.GetResponseAccount(ctx, groupID, "resp_abc")
	require.NoError(t, err)
	require.Zero(t, accountID)
}

func TestOpenAIWSStateStore_ResponseConnTTL(t *testing.T) {
	store := NewOpenAIWSStateStore(nil)
	store.BindResponseConn("resp_conn", "conn_1", 30*time.Millisecond)

	connID, ok := store.GetResponseConn("resp_conn")
	require.True(t, ok)
	require.Equal(t, "conn_1", connID)

	time.Sleep(60 * time.Millisecond)
	_, ok = store.GetResponseConn("resp_conn")
	require.False(t, ok)
}

func TestOpenAIWSStateStore_ResponsePendingToolCallsTTL(t *testing.T) {
	store := NewOpenAIWSStateStore(nil)
	store.BindResponsePendingToolCalls("resp_pending_tool_1", []string{"call_1", "call_2", "call_1", " "}, 30*time.Millisecond)

	callIDs, ok := store.GetResponsePendingToolCalls("resp_pending_tool_1")
	require.True(t, ok)
	require.ElementsMatch(t, []string{"call_1", "call_2"}, callIDs)

	store.DeleteResponsePendingToolCalls("resp_pending_tool_1")
	_, ok = store.GetResponsePendingToolCalls("resp_pending_tool_1")
	require.False(t, ok)

	store.BindResponsePendingToolCalls("resp_pending_tool_2", []string{"call_3"}, 30*time.Millisecond)
	time.Sleep(60 * time.Millisecond)
	_, ok = store.GetResponsePendingToolCalls("resp_pending_tool_2")
	require.False(t, ok)
}

func TestOpenAIWSStateStore_SessionTurnStateTTL(t *testing.T) {
	store := NewOpenAIWSStateStore(nil)
	store.BindSessionTurnState(9, "session_hash_1", "turn_state_1", 30*time.Millisecond)

	state, ok := store.GetSessionTurnState(9, "session_hash_1")
	require.True(t, ok)
	require.Equal(t, "turn_state_1", state)

	// group 隔离
	_, ok = store.GetSessionTurnState(10, "session_hash_1")
	require.False(t, ok)

	time.Sleep(60 * time.Millisecond)
	_, ok = store.GetSessionTurnState(9, "session_hash_1")
	require.False(t, ok)
}

func TestOpenAIWSStateStore_SessionConnTTL(t *testing.T) {
	store := NewOpenAIWSStateStore(nil)
	store.BindSessionConn(9, "session_hash_conn_1", "conn_1", 30*time.Millisecond)

	connID, ok := store.GetSessionConn(9, "session_hash_conn_1")
	require.True(t, ok)
	require.Equal(t, "conn_1", connID)

	// group 隔离
	_, ok = store.GetSessionConn(10, "session_hash_conn_1")
	require.False(t, ok)

	time.Sleep(60 * time.Millisecond)
	_, ok = store.GetSessionConn(9, "session_hash_conn_1")
	require.False(t, ok)
}

func TestOpenAIWSStateStore_SessionLastResponseIDTTL(t *testing.T) {
	store := NewOpenAIWSStateStore(nil)
	store.BindSessionLastResponseID(9, "session_hash_resp_1", "resp_1", 30*time.Millisecond)

	responseID, ok := store.GetSessionLastResponseID(9, "session_hash_resp_1")
	require.True(t, ok)
	require.Equal(t, "resp_1", responseID)

	// group 隔离
	_, ok = store.GetSessionLastResponseID(10, "session_hash_resp_1")
	require.False(t, ok)

	time.Sleep(60 * time.Millisecond)
	_, ok = store.GetSessionLastResponseID(9, "session_hash_resp_1")
	require.False(t, ok)
}

type openAIWSSessionLastResponseProbeCache struct {
	sessionData map[string]string
	setCalled   bool
	getCalled   bool
	delCalled   bool
}

func (c *openAIWSSessionLastResponseProbeCache) GetSessionAccountID(context.Context, int64, string) (int64, error) {
	return 0, nil
}

func (c *openAIWSSessionLastResponseProbeCache) SetSessionAccountID(context.Context, int64, string, int64, time.Duration) error {
	return nil
}

func (c *openAIWSSessionLastResponseProbeCache) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}

func (c *openAIWSSessionLastResponseProbeCache) DeleteSessionAccountID(context.Context, int64, string) error {
	return nil
}

func (c *openAIWSSessionLastResponseProbeCache) SetOpenAIWSSessionLastResponseID(_ context.Context, groupID int64, sessionHash, responseID string, _ time.Duration) error {
	if c.sessionData == nil {
		c.sessionData = make(map[string]string)
	}
	c.setCalled = true
	c.sessionData[fmt.Sprintf("%d:%s", groupID, sessionHash)] = responseID
	return nil
}

func (c *openAIWSSessionLastResponseProbeCache) GetOpenAIWSSessionLastResponseID(_ context.Context, groupID int64, sessionHash string) (string, error) {
	c.getCalled = true
	return c.sessionData[fmt.Sprintf("%d:%s", groupID, sessionHash)], nil
}

func (c *openAIWSSessionLastResponseProbeCache) DeleteOpenAIWSSessionLastResponseID(_ context.Context, groupID int64, sessionHash string) error {
	c.delCalled = true
	delete(c.sessionData, fmt.Sprintf("%d:%s", groupID, sessionHash))
	return nil
}

func TestOpenAIWSStateStore_SessionLastResponseID_UsesOptionalCacheFallback(t *testing.T) {
	probe := &openAIWSSessionLastResponseProbeCache{sessionData: make(map[string]string)}
	raw := NewOpenAIWSStateStore(probe)
	store, ok := raw.(*defaultOpenAIWSStateStore)
	require.True(t, ok)

	groupID := int64(9)
	sessionHash := "session_hash_resp_cache_1"
	responseID := "resp_cache_1"
	store.BindSessionLastResponseID(groupID, sessionHash, responseID, time.Minute)
	require.True(t, probe.setCalled, "绑定 session last_response_id 时应写入可选缓存")

	key := openAIWSSessionTurnStateKey(groupID, sessionHash)
	store.sessionToLastRespMu.Lock()
	delete(store.sessionToLastResp, key)
	store.sessionToLastRespMu.Unlock()

	gotResponseID, found := store.GetSessionLastResponseID(groupID, sessionHash)
	require.True(t, found, "本地缓存缺失时应降级读取可选缓存")
	require.Equal(t, responseID, gotResponseID)
	require.True(t, probe.getCalled)

	store.DeleteSessionLastResponseID(groupID, sessionHash)
	require.True(t, probe.delCalled, "删除 session last_response_id 时应同步删除可选缓存")
	_, found = store.GetSessionLastResponseID(groupID, sessionHash)
	require.False(t, found)
}

type openAIWSResponsePendingToolCallsProbeCache struct {
	pendingData map[string][]string
	setCalls    int
	getCalls    int
	delCalls    int
}

func (c *openAIWSResponsePendingToolCallsProbeCache) GetSessionAccountID(context.Context, int64, string) (int64, error) {
	return 0, nil
}

func (c *openAIWSResponsePendingToolCallsProbeCache) SetSessionAccountID(context.Context, int64, string, int64, time.Duration) error {
	return nil
}

func (c *openAIWSResponsePendingToolCallsProbeCache) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}

func (c *openAIWSResponsePendingToolCallsProbeCache) DeleteSessionAccountID(context.Context, int64, string) error {
	return nil
}

func (c *openAIWSResponsePendingToolCallsProbeCache) SetOpenAIWSResponsePendingToolCalls(_ context.Context, responseID string, callIDs []string, _ time.Duration) error {
	if c.pendingData == nil {
		c.pendingData = make(map[string][]string)
	}
	normalized := normalizeOpenAIWSPendingToolCallIDs(callIDs)
	if len(normalized) == 0 {
		delete(c.pendingData, responseID)
	} else {
		c.pendingData[responseID] = append([]string(nil), normalized...)
	}
	c.setCalls++
	return nil
}

func (c *openAIWSResponsePendingToolCallsProbeCache) GetOpenAIWSResponsePendingToolCalls(_ context.Context, responseID string) ([]string, error) {
	c.getCalls++
	callIDs := c.pendingData[responseID]
	return append([]string(nil), callIDs...), nil
}

func (c *openAIWSResponsePendingToolCallsProbeCache) DeleteOpenAIWSResponsePendingToolCalls(_ context.Context, responseID string) error {
	c.delCalls++
	delete(c.pendingData, responseID)
	return nil
}

func TestOpenAIWSStateStore_ResponsePendingToolCalls_UsesOptionalCacheFallback(t *testing.T) {
	probe := &openAIWSResponsePendingToolCallsProbeCache{pendingData: make(map[string][]string)}
	raw := NewOpenAIWSStateStore(probe)
	store, ok := raw.(*defaultOpenAIWSStateStore)
	require.True(t, ok)

	responseID := "resp_pending_tool_cache_1"
	store.BindResponsePendingToolCalls(responseID, []string{"call_1", "call_2", "call_1"}, time.Minute)
	require.Equal(t, 1, probe.setCalls, "绑定 pending_tool_calls 时应写入可选缓存")

	store.responsePendingToolMu.Lock()
	delete(store.responsePendingTool, normalizeOpenAIWSResponseID(responseID))
	store.responsePendingToolMu.Unlock()

	callIDs, found := store.GetResponsePendingToolCalls(responseID)
	require.True(t, found, "本地缓存缺失时应降级读取可选缓存")
	require.ElementsMatch(t, []string{"call_1", "call_2"}, callIDs)
	require.Equal(t, 1, probe.getCalls)

	// 回填后再次读取应命中本地缓存，不再触发 Redis 回源。
	callIDs, found = store.GetResponsePendingToolCalls(responseID)
	require.True(t, found)
	require.ElementsMatch(t, []string{"call_1", "call_2"}, callIDs)
	require.Equal(t, 1, probe.getCalls)

	store.DeleteResponsePendingToolCalls(responseID)
	require.Equal(t, 1, probe.delCalls, "删除 pending_tool_calls 时应同步删除可选缓存")
	_, found = store.GetResponsePendingToolCalls(responseID)
	require.False(t, found)
}

func TestOpenAIWSStateStore_GetResponseAccount_NoStaleAfterCacheMiss(t *testing.T) {
	cache := &stubGatewayCache{sessionBindings: map[string]int64{}}
	store := NewOpenAIWSStateStore(cache)
	ctx := context.Background()
	groupID := int64(17)
	responseID := "resp_cache_stale"
	cacheKey := openAIWSResponseAccountCacheKey(responseID)

	cache.sessionBindings[cacheKey] = 501
	accountID, err := store.GetResponseAccount(ctx, groupID, responseID)
	require.NoError(t, err)
	require.Equal(t, int64(501), accountID)

	delete(cache.sessionBindings, cacheKey)
	accountID, err = store.GetResponseAccount(ctx, groupID, responseID)
	require.NoError(t, err)
	require.Zero(t, accountID, "上游缓存失效后不应继续命中本地陈旧映射")
}

func TestOpenAIWSStateStore_GetResponseAccount_LegacyKeyFallback(t *testing.T) {
	cache := &stubGatewayCache{sessionBindings: map[string]int64{}}
	store := NewOpenAIWSStateStore(cache)
	ctx := context.Background()
	groupID := int64(18)
	responseID := "resp_cache_legacy_fallback"

	legacyKey := openAIWSResponseAccountLegacyCacheKey(responseID)
	v2Key := openAIWSResponseAccountCacheKey(responseID)
	cache.sessionBindings[legacyKey] = 601

	accountID, err := store.GetResponseAccount(ctx, groupID, responseID)
	require.NoError(t, err)
	require.Equal(t, int64(601), accountID, "应支持 legacy cache key 回读")
	require.Equal(t, int64(601), cache.sessionBindings[v2Key], "legacy 回读后应回填 v2 cache key")
}

func TestOpenAIWSStateStore_DeleteResponseAccount_DeletesLegacyAndV2Keys(t *testing.T) {
	cache := &stubGatewayCache{sessionBindings: map[string]int64{}}
	store := NewOpenAIWSStateStore(cache)
	ctx := context.Background()
	groupID := int64(19)
	responseID := "resp_cache_delete_both_keys"

	legacyKey := openAIWSResponseAccountLegacyCacheKey(responseID)
	v2Key := openAIWSResponseAccountCacheKey(responseID)
	cache.sessionBindings[legacyKey] = 701
	cache.sessionBindings[v2Key] = 701

	require.NoError(t, store.DeleteResponseAccount(ctx, groupID, responseID))
	_, legacyExists := cache.sessionBindings[legacyKey]
	_, v2Exists := cache.sessionBindings[v2Key]
	require.False(t, legacyExists, "删除 response account 绑定时应清理 legacy key")
	require.False(t, v2Exists, "删除 response account 绑定时应清理 v2 key")
}

func TestOpenAIWSStateStore_MaybeCleanupRemovesExpiredIncrementally(t *testing.T) {
	raw := NewOpenAIWSStateStore(nil)
	store, ok := raw.(*defaultOpenAIWSStateStore)
	require.True(t, ok)

	expiredAt := time.Now().Add(-time.Minute)
	total := 2048
	for i := 0; i < total; i++ {
		key := fmt.Sprintf("resp_%d", i)
		shard := store.connShard(key)
		shard.mu.Lock()
		shard.m[key] = openAIWSConnBinding{
			connID:    "conn_incremental",
			expiresAt: expiredAt,
		}
		shard.mu.Unlock()
	}

	store.lastCleanupUnixNano.Store(time.Now().Add(-2 * openAIWSStateStoreCleanupInterval).UnixNano())
	store.maybeCleanup()

	remainingAfterFirst := 0
	for i := range store.responseToConnShards {
		shard := &store.responseToConnShards[i]
		shard.mu.RLock()
		remainingAfterFirst += len(shard.m)
		shard.mu.RUnlock()
	}
	require.Less(t, remainingAfterFirst, total, "单轮 cleanup 应至少有进展")
	require.Greater(t, remainingAfterFirst, 0, "增量清理不要求单轮清空全部键")

	for i := 0; i < 8; i++ {
		store.lastCleanupUnixNano.Store(time.Now().Add(-2 * openAIWSStateStoreCleanupInterval).UnixNano())
		store.maybeCleanup()
	}

	remaining := 0
	for i := range store.responseToConnShards {
		shard := &store.responseToConnShards[i]
		shard.mu.RLock()
		remaining += len(shard.m)
		shard.mu.RUnlock()
	}
	require.Zero(t, remaining, "多轮 cleanup 后应逐步清空全部过期键")
}

func TestEnsureBindingCapacity_EvictsOneWhenMapIsFull(t *testing.T) {
	bindings := map[string]openAIWSAccountBinding{
		"a": {accountID: 1, expiresAt: time.Now().Add(time.Hour)},
		"b": {accountID: 2, expiresAt: time.Now().Add(time.Hour)},
	}

	ensureBindingCapacity(bindings, "c", 2)
	bindings["c"] = openAIWSAccountBinding{accountID: 3, expiresAt: time.Now().Add(time.Hour)}

	require.Len(t, bindings, 2)
	require.Equal(t, int64(3), bindings["c"].accountID)
}

func TestEnsureBindingCapacity_DoesNotEvictWhenUpdatingExistingKey(t *testing.T) {
	bindings := map[string]openAIWSAccountBinding{
		"a": {accountID: 1, expiresAt: time.Now().Add(time.Hour)},
		"b": {accountID: 2, expiresAt: time.Now().Add(time.Hour)},
	}

	ensureBindingCapacity(bindings, "a", 2)
	bindings["a"] = openAIWSAccountBinding{accountID: 9, expiresAt: time.Now().Add(time.Hour)}

	require.Len(t, bindings, 2)
	require.Equal(t, int64(9), bindings["a"].accountID)
}

func TestEnsureBindingCapacity_PrefersExpiredEntry(t *testing.T) {
	bindings := map[string]openAIWSAccountBinding{
		"expired": {accountID: 1, expiresAt: time.Now().Add(-time.Hour)},
		"active":  {accountID: 2, expiresAt: time.Now().Add(time.Hour)},
	}

	ensureBindingCapacity(bindings, "c", 2)
	bindings["c"] = openAIWSAccountBinding{accountID: 3, expiresAt: time.Now().Add(time.Hour)}

	require.Len(t, bindings, 2)
	_, hasExpired := bindings["expired"]
	require.False(t, hasExpired, "expired entry should have been evicted")
	require.Equal(t, int64(2), bindings["active"].accountID)
	require.Equal(t, int64(3), bindings["c"].accountID)
}

type openAIWSStateStoreTimeoutProbeCache struct {
	setHasDeadline    bool
	getHasDeadline    bool
	deleteHasDeadline bool
	setDeadlineDelta  time.Duration
	getDeadlineDelta  time.Duration
	delDeadlineDelta  time.Duration
}

func (c *openAIWSStateStoreTimeoutProbeCache) GetSessionAccountID(ctx context.Context, _ int64, _ string) (int64, error) {
	if deadline, ok := ctx.Deadline(); ok {
		c.getHasDeadline = true
		c.getDeadlineDelta = time.Until(deadline)
	}
	return 123, nil
}

func (c *openAIWSStateStoreTimeoutProbeCache) SetSessionAccountID(ctx context.Context, _ int64, _ string, _ int64, _ time.Duration) error {
	if deadline, ok := ctx.Deadline(); ok {
		c.setHasDeadline = true
		c.setDeadlineDelta = time.Until(deadline)
	}
	return errors.New("set failed")
}

func (c *openAIWSStateStoreTimeoutProbeCache) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}

func (c *openAIWSStateStoreTimeoutProbeCache) DeleteSessionAccountID(ctx context.Context, _ int64, _ string) error {
	if deadline, ok := ctx.Deadline(); ok {
		c.deleteHasDeadline = true
		c.delDeadlineDelta = time.Until(deadline)
	}
	return nil
}

func TestOpenAIWSStateStore_RedisOpsUseShortTimeout(t *testing.T) {
	probe := &openAIWSStateStoreTimeoutProbeCache{}
	store := NewOpenAIWSStateStore(probe)
	ctx := context.Background()
	groupID := int64(5)

	err := store.BindResponseAccount(ctx, groupID, "resp_timeout_probe", 11, time.Minute)
	require.Error(t, err)

	accountID, getErr := store.GetResponseAccount(ctx, groupID, "resp_timeout_probe")
	require.NoError(t, getErr)
	require.Equal(t, int64(11), accountID, "本地缓存命中应优先返回已绑定账号")

	require.NoError(t, store.DeleteResponseAccount(ctx, groupID, "resp_timeout_probe"))

	require.True(t, probe.setHasDeadline, "SetSessionAccountID 应携带独立超时上下文")
	require.True(t, probe.deleteHasDeadline, "DeleteSessionAccountID 应携带独立超时上下文")
	require.False(t, probe.getHasDeadline, "GetSessionAccountID 本用例应由本地缓存命中，不触发 Redis 读取")
	require.Greater(t, probe.setDeadlineDelta, 2*time.Second)
	require.LessOrEqual(t, probe.setDeadlineDelta, 3*time.Second)
	require.Greater(t, probe.delDeadlineDelta, 2*time.Second)
	require.LessOrEqual(t, probe.delDeadlineDelta, 3*time.Second)

	probe2 := &openAIWSStateStoreTimeoutProbeCache{}
	store2 := NewOpenAIWSStateStore(probe2)
	accountID2, err2 := store2.GetResponseAccount(ctx, groupID, "resp_cache_only")
	require.NoError(t, err2)
	require.Equal(t, int64(123), accountID2)
	require.True(t, probe2.getHasDeadline, "GetSessionAccountID 在缓存未命中时应携带独立超时上下文")
	require.Greater(t, probe2.getDeadlineDelta, 2*time.Second)
	require.LessOrEqual(t, probe2.getDeadlineDelta, 3*time.Second)
}

func TestWithOpenAIWSStateStoreRedisTimeout_WithParentContext(t *testing.T) {
	ctx, cancel := withOpenAIWSStateStoreRedisTimeout(context.Background())
	defer cancel()
	require.NotNil(t, ctx)
	_, ok := ctx.Deadline()
	require.True(t, ok, "应附加短超时")
}
