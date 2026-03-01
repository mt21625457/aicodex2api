package service

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Mock conn for hotpath performance tests
// ---------------------------------------------------------------------------

type openAIWSNoopConn struct{}

func (c *openAIWSNoopConn) WriteJSON(context.Context, any) error        { return nil }
func (c *openAIWSNoopConn) ReadMessage(context.Context) ([]byte, error) { return nil, nil }
func (c *openAIWSNoopConn) Ping(context.Context) error                  { return nil }
func (c *openAIWSNoopConn) Close() error                                { return nil }

// openAIWSIdentityConn is a distinct conn instance used to verify pointer identity.
type openAIWSIdentityConn struct{ tag string }

func (c *openAIWSIdentityConn) WriteJSON(context.Context, any) error        { return nil }
func (c *openAIWSIdentityConn) ReadMessage(context.Context) ([]byte, error) { return nil, nil }
func (c *openAIWSIdentityConn) Ping(context.Context) error                  { return nil }
func (c *openAIWSIdentityConn) Close() error                                { return nil }

// ===================================================================
// 1. maybeTouchLease throttle
// ===================================================================

func TestMaybeTouchLease_NilReceiverDoesNotPanic(t *testing.T) {
	var c *openAIWSIngressContext
	require.NotPanics(t, func() {
		c.maybeTouchLease(time.Minute)
	})
}

func TestMaybeTouchLease_FirstCallAlwaysTouches(t *testing.T) {
	c := &openAIWSIngressContext{}
	require.Zero(t, c.lastTouchUnixNano.Load(), "precondition: lastTouchUnixNano should be zero")

	c.maybeTouchLease(5 * time.Minute)

	require.NotZero(t, c.lastTouchUnixNano.Load(), "first maybeTouchLease must update lastTouchUnixNano")
	require.False(t, c.expiresAt().IsZero(), "first maybeTouchLease must set expiresAt")
}

func TestMaybeTouchLease_SecondCallWithin1sIsSkipped(t *testing.T) {
	c := &openAIWSIngressContext{}

	// First touch
	c.maybeTouchLease(5 * time.Minute)
	firstExpiry := c.expiresAt()
	firstTouch := c.lastTouchUnixNano.Load()
	require.NotZero(t, firstTouch)

	// Second touch immediately -- within 1s, should be skipped
	c.maybeTouchLease(10 * time.Minute)
	secondExpiry := c.expiresAt()
	secondTouch := c.lastTouchUnixNano.Load()

	require.Equal(t, firstTouch, secondTouch, "lastTouchUnixNano should NOT change within 1s")
	require.Equal(t, firstExpiry, secondExpiry, "expiresAt should NOT change within 1s")
}

func TestMaybeTouchLease_CallAfter1sActuallyTouches(t *testing.T) {
	c := &openAIWSIngressContext{}

	c.maybeTouchLease(5 * time.Minute)
	firstExpiry := c.expiresAt()

	// Simulate 1s+ passing by backdating the lastTouchUnixNano
	backdated := time.Now().Add(-2 * time.Second).UnixNano()
	c.lastTouchUnixNano.Store(backdated)
	// Also backdate expiresAt so we can observe the change
	c.setExpiresAt(time.Now().Add(-time.Minute))
	expiryAfterBackdate := c.expiresAt()
	require.True(t, expiryAfterBackdate.Before(firstExpiry), "precondition: expiresAt should be backdated")

	c.maybeTouchLease(5 * time.Minute)
	touchAfter := c.lastTouchUnixNano.Load()
	secondExpiry := c.expiresAt()

	require.Greater(t, touchAfter, backdated, "lastTouchUnixNano should advance past the backdated value")
	require.True(t, secondExpiry.After(expiryAfterBackdate), "expiresAt should advance after 1s+ gap")
}

func TestTouchLease_NilReceiverDoesNotPanic(t *testing.T) {
	var c *openAIWSIngressContext
	require.NotPanics(t, func() {
		c.touchLease(time.Now(), 5*time.Minute)
	})
}

func TestTouchLease_AlwaysUpdatesLastTouchUnixNano(t *testing.T) {
	c := &openAIWSIngressContext{}

	now := time.Now()
	c.touchLease(now, 5*time.Minute)
	first := c.lastTouchUnixNano.Load()
	require.NotZero(t, first)

	// touchLease (non-throttled) always updates, even if called again immediately.
	time.Sleep(time.Millisecond) // ensure clock moves forward
	now2 := time.Now()
	c.touchLease(now2, 5*time.Minute)
	second := c.lastTouchUnixNano.Load()
	require.Greater(t, second, first, "touchLease must always update lastTouchUnixNano")
}

// ===================================================================
// 2. activeConn cached connection
// ===================================================================

func TestActiveConn_NilLeaseReturnsError(t *testing.T) {
	var lease *openAIWSIngressContextLease
	conn, err := lease.activeConn()
	require.Nil(t, conn)
	require.ErrorIs(t, err, errOpenAIWSConnClosed)
}

func TestActiveConn_NilContextReturnsError(t *testing.T) {
	lease := &openAIWSIngressContextLease{context: nil}
	conn, err := lease.activeConn()
	require.Nil(t, conn)
	require.ErrorIs(t, err, errOpenAIWSConnClosed)
}

func TestActiveConn_ReleasedLeaseReturnsError(t *testing.T) {
	ctx := &openAIWSIngressContext{
		ownerID:  "owner",
		upstream: &openAIWSNoopConn{},
	}
	lease := &openAIWSIngressContextLease{
		context: ctx,
		ownerID: "owner",
	}
	lease.released.Store(true)

	conn, err := lease.activeConn()
	require.Nil(t, conn)
	require.ErrorIs(t, err, errOpenAIWSConnClosed)
}

func TestActiveConn_FirstCallPopulatesCachedConn(t *testing.T) {
	upstream := &openAIWSIdentityConn{tag: "primary"}
	ctx := &openAIWSIngressContext{
		ownerID:  "owner_1",
		upstream: upstream,
	}
	lease := &openAIWSIngressContextLease{
		context: ctx,
		ownerID: "owner_1",
	}

	require.Nil(t, lease.cachedConn, "precondition: cachedConn should be nil")

	conn, err := lease.activeConn()
	require.NoError(t, err)
	require.Equal(t, upstream, conn, "should return the upstream conn")
	require.Equal(t, upstream, lease.cachedConn, "should populate cachedConn")
}

func TestActiveConn_SecondCallReturnsCachedDirectly(t *testing.T) {
	upstream1 := &openAIWSIdentityConn{tag: "first"}
	ctx := &openAIWSIngressContext{
		ownerID:  "owner_cache",
		upstream: upstream1,
	}
	lease := &openAIWSIngressContextLease{
		context: ctx,
		ownerID: "owner_cache",
	}

	// First call populates cache
	conn1, err := lease.activeConn()
	require.NoError(t, err)
	require.Equal(t, upstream1, conn1)

	// Swap the upstream -- cached path should NOT see the swap
	upstream2 := &openAIWSIdentityConn{tag: "second"}
	ctx.mu.Lock()
	ctx.upstream = upstream2
	ctx.mu.Unlock()

	conn2, err := lease.activeConn()
	require.NoError(t, err)
	require.Equal(t, upstream1, conn2, "second call should return cachedConn, not the swapped upstream")
}

func TestActiveConn_OwnerMismatchReturnsError(t *testing.T) {
	ctx := &openAIWSIngressContext{
		ownerID:  "other_owner",
		upstream: &openAIWSNoopConn{},
	}
	lease := &openAIWSIngressContextLease{
		context: ctx,
		ownerID: "my_owner",
	}

	conn, err := lease.activeConn()
	require.Nil(t, conn)
	require.ErrorIs(t, err, errOpenAIWSConnClosed)
}

func TestActiveConn_NilUpstreamReturnsError(t *testing.T) {
	ctx := &openAIWSIngressContext{
		ownerID:  "owner",
		upstream: nil,
	}
	lease := &openAIWSIngressContextLease{
		context: ctx,
		ownerID: "owner",
	}

	conn, err := lease.activeConn()
	require.Nil(t, conn)
	require.ErrorIs(t, err, errOpenAIWSConnClosed)
}

func TestActiveConn_MarkBrokenClearsCachedConn(t *testing.T) {
	upstream := &openAIWSNoopConn{}
	ctx := &openAIWSIngressContext{
		ownerID:  "owner_mb",
		upstream: upstream,
	}
	pool := &openAIWSIngressContextPool{
		idleTTL: 10 * time.Minute,
	}
	lease := &openAIWSIngressContextLease{
		pool:    pool,
		context: ctx,
		ownerID: "owner_mb",
	}

	// Populate cache
	conn, err := lease.activeConn()
	require.NoError(t, err)
	require.NotNil(t, conn)
	require.NotNil(t, lease.cachedConn)

	lease.MarkBroken()
	require.Nil(t, lease.cachedConn, "MarkBroken must clear cachedConn")
}

func TestActiveConn_ReleaseClearsCachedConn(t *testing.T) {
	upstream := &openAIWSNoopConn{}
	ctx := &openAIWSIngressContext{
		ownerID:  "owner_rel",
		upstream: upstream,
	}
	pool := &openAIWSIngressContextPool{
		idleTTL: 10 * time.Minute,
	}
	lease := &openAIWSIngressContextLease{
		pool:    pool,
		context: ctx,
		ownerID: "owner_rel",
	}

	// Populate cache
	conn, err := lease.activeConn()
	require.NoError(t, err)
	require.NotNil(t, conn)
	require.NotNil(t, lease.cachedConn)

	lease.Release()
	require.Nil(t, lease.cachedConn, "Release must clear cachedConn")
}

func TestActiveConn_AfterClearCachedConn_ReacquiresViaMutex(t *testing.T) {
	upstream1 := &openAIWSIdentityConn{tag: "v1"}
	ctx := &openAIWSIngressContext{
		ownerID:  "owner_reacq",
		upstream: upstream1,
	}
	lease := &openAIWSIngressContextLease{
		context: ctx,
		ownerID: "owner_reacq",
	}

	// Populate cache with upstream1
	conn, err := lease.activeConn()
	require.NoError(t, err)
	require.Equal(t, upstream1, conn)

	// Simulate a cleared cache (e.g., after recovery)
	lease.cachedConn = nil

	// Swap upstream
	upstream2 := &openAIWSIdentityConn{tag: "v2"}
	ctx.mu.Lock()
	ctx.upstream = upstream2
	ctx.mu.Unlock()

	// Should now re-acquire via mutex and return upstream2
	conn2, err := lease.activeConn()
	require.NoError(t, err)
	require.Equal(t, upstream2, conn2, "after clearing cachedConn, next call must re-acquire via mutex")
	require.Equal(t, upstream2, lease.cachedConn, "should re-populate cachedConn with new upstream")
}

// ===================================================================
// 3. Event type TrimSpace-free functions
// ===================================================================

func TestIsOpenAIWSTerminalEvent(t *testing.T) {
	tests := []struct {
		eventType string
		want      bool
	}{
		{"response.completed", true},
		{"response.done", true},
		{"response.failed", true},
		{"response.incomplete", true},
		{"response.cancelled", true},
		{"response.canceled", true},
		{"response.created", false},
		{"response.in_progress", false},
		{"response.output_text.delta", false},
		{"", false},
		{"unknown_event", false},
	}
	for _, tt := range tests {
		t.Run(tt.eventType, func(t *testing.T) {
			require.Equal(t, tt.want, isOpenAIWSTerminalEvent(tt.eventType))
		})
	}
}

func TestShouldPersistOpenAIWSLastResponseID_HotpathPerf(t *testing.T) {
	tests := []struct {
		eventType string
		want      bool
	}{
		{"response.completed", true},
		{"response.done", true},
		{"response.failed", false},
		{"response.incomplete", false},
		{"response.cancelled", false},
		{"", false},
		{"unknown_event", false},
	}
	for _, tt := range tests {
		t.Run(tt.eventType, func(t *testing.T) {
			require.Equal(t, tt.want, shouldPersistOpenAIWSLastResponseID(tt.eventType))
		})
	}
}

func TestIsOpenAIWSTokenEvent(t *testing.T) {
	tests := []struct {
		eventType string
		want      bool
	}{
		// Known false: structural events
		{"response.created", false},
		{"response.in_progress", false},
		{"response.output_item.added", false},
		{"response.output_item.done", false},
		// Delta events
		{"response.output_text.delta", true},
		{"response.content_part.delta", true},
		{"response.audio.delta", true},
		{"response.function_call_arguments.delta", true},
		// output_text prefix
		{"response.output_text.done", true},
		{"response.output_text.annotation.added", true},
		// output prefix (but not output_item)
		{"response.output.done", true},
		// Terminal events that are also token events
		{"response.completed", true},
		{"response.done", true},
		// Empty and unknown
		{"", false},
		{"unknown_event", false},
		{"session.created", false},
		{"session.updated", false},
	}
	for _, tt := range tests {
		t.Run(tt.eventType, func(t *testing.T) {
			require.Equal(t, tt.want, isOpenAIWSTokenEvent(tt.eventType))
		})
	}
}

func TestOpenAIWSEventShouldParseUsage(t *testing.T) {
	tests := []struct {
		eventType string
		want      bool
	}{
		{"response.completed", true},
		{"response.done", true},
		{"response.failed", true},
		{"", false},
		{"unknown", false},
	}
	for _, tt := range tests {
		t.Run(tt.eventType, func(t *testing.T) {
			require.Equal(t, tt.want, openAIWSEventShouldParseUsage(tt.eventType))
		})
	}
}

func TestOpenAIWSEventMayContainToolCalls(t *testing.T) {
	tests := []struct {
		eventType string
		want      bool
	}{
		// Explicit function_call / tool_call in name
		{"response.function_call_arguments.delta", true},
		{"response.function_call_arguments.done", true},
		{"response.tool_call.delta", true},
		// Structural events that may contain tool output items
		{"response.output_item.added", true},
		{"response.output_item.done", true},
		{"response.completed", true},
		{"response.done", true},
		// Non-tool events
		{"response.output_text.delta", false},
		{"response.created", false},
		{"response.in_progress", false},
		{"", false},
		{"unknown", false},
	}
	for _, tt := range tests {
		t.Run(tt.eventType, func(t *testing.T) {
			require.Equal(t, tt.want, openAIWSEventMayContainToolCalls(tt.eventType))
		})
	}
}

// ===================================================================
// 4. parseOpenAIWSEventType (lightweight version)
// ===================================================================

func TestParseOpenAIWSEventType_EmptyMessage(t *testing.T) {
	eventType, responseID := parseOpenAIWSEventType(nil)
	require.Empty(t, eventType)
	require.Empty(t, responseID)

	eventType, responseID = parseOpenAIWSEventType([]byte{})
	require.Empty(t, eventType)
	require.Empty(t, responseID)
}

func TestParseOpenAIWSEventType_ResponseIDExtracted(t *testing.T) {
	msg := []byte(`{"type":"response.completed","response":{"id":"resp_abc123"}}`)
	eventType, responseID := parseOpenAIWSEventType(msg)
	require.Equal(t, "response.completed", eventType)
	require.Equal(t, "resp_abc123", responseID)
}

func TestParseOpenAIWSEventType_FallbackToID(t *testing.T) {
	msg := []byte(`{"type":"response.output_text.delta","id":"evt_fallback_id"}`)
	eventType, responseID := parseOpenAIWSEventType(msg)
	require.Equal(t, "response.output_text.delta", eventType)
	require.Equal(t, "evt_fallback_id", responseID)
}

func TestParseOpenAIWSEventType_ConsistentWithEnvelope(t *testing.T) {
	testMessages := [][]byte{
		[]byte(`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.1"}}`),
		[]byte(`{"type":"response.output_text.delta","id":"evt_2"}`),
		[]byte(`{"type":"response.created","response":{"id":"resp_3"}}`),
		[]byte(`{"type":"error","error":{"message":"bad request"}}`),
		[]byte(`{"type":"response.done","id":"resp_4","response":{"id":"resp_4_inner"}}`),
		[]byte(`{}`),
		[]byte(`{"type":"session.created"}`),
	}
	for i, msg := range testMessages {
		t.Run(fmt.Sprintf("case_%d", i), func(t *testing.T) {
			typeLight, idLight := parseOpenAIWSEventType(msg)
			typeEnv, idEnv, _ := parseOpenAIWSEventEnvelope(msg)
			require.Equal(t, typeEnv, typeLight, "eventType must match parseOpenAIWSEventEnvelope")
			require.Equal(t, idEnv, idLight, "responseID must match parseOpenAIWSEventEnvelope")
		})
	}
}

// ===================================================================
// 6. openAIWSResponseAccountCacheKey (xxhash, v2 prefix)
// ===================================================================

func TestOpenAIWSResponseAccountCacheKey_Deterministic(t *testing.T) {
	key1 := openAIWSResponseAccountCacheKey("resp_deterministic_test")
	key2 := openAIWSResponseAccountCacheKey("resp_deterministic_test")
	require.Equal(t, key1, key2, "same responseID must produce the same key")
}

func TestOpenAIWSResponseAccountCacheKey_DifferentIDsDifferentKeys(t *testing.T) {
	key1 := openAIWSResponseAccountCacheKey("resp_alpha")
	key2 := openAIWSResponseAccountCacheKey("resp_beta")
	require.NotEqual(t, key1, key2, "different responseIDs must produce different keys")
}

func TestOpenAIWSResponseAccountCacheKey_V2Prefix(t *testing.T) {
	key := openAIWSResponseAccountCacheKey("resp_v2_check")
	require.True(t, strings.Contains(key, "v2:"), "key must contain v2: prefix for version compatibility")
}

func TestOpenAIWSResponseAccountCacheKey_StartsWithCachePrefix(t *testing.T) {
	key := openAIWSResponseAccountCacheKey("resp_prefix_check")
	require.True(t, strings.HasPrefix(key, openAIWSResponseAccountCachePrefix),
		"key must start with the standard cache prefix %q, got %q", openAIWSResponseAccountCachePrefix, key)
}

func TestOpenAIWSResponseAccountCacheKey_HexLength(t *testing.T) {
	key := openAIWSResponseAccountCacheKey("resp_hex_length")
	// Expected format: "openai:response:v2:<16 hex chars>"
	prefix := openAIWSResponseAccountCachePrefix + "v2:"
	require.True(t, strings.HasPrefix(key, prefix))
	hexPart := strings.TrimPrefix(key, prefix)
	require.Len(t, hexPart, 16, "xxhash hex digest should be zero-padded to 16 chars, got %q", hexPart)
}

func TestOpenAIWSResponseAccountCacheKey_ManyInputs_AllPaddedTo16(t *testing.T) {
	// Verify that all inputs produce exactly 16-char hex, testing many variations.
	prefix := openAIWSResponseAccountCachePrefix + "v2:"
	for i := 0; i < 1000; i++ {
		responseID := fmt.Sprintf("resp_%d", i)
		key := openAIWSResponseAccountCacheKey(responseID)
		hexPart := strings.TrimPrefix(key, prefix)
		require.Len(t, hexPart, 16, "responseID=%q produced hex %q (len %d)", responseID, hexPart, len(hexPart))
	}
}

// ===================================================================
// 7. openAIWSSessionTurnStateKey uses strconv
// ===================================================================

func TestOpenAIWSSessionTurnStateKey_NormalCase(t *testing.T) {
	key := openAIWSSessionTurnStateKey(123, "abc_hash")
	require.Equal(t, "123:abc_hash", key)
}

func TestOpenAIWSSessionTurnStateKey_EmptySessionHash(t *testing.T) {
	key := openAIWSSessionTurnStateKey(123, "")
	require.Equal(t, "", key)
}

func TestOpenAIWSSessionTurnStateKey_WhitespaceOnlySessionHash(t *testing.T) {
	key := openAIWSSessionTurnStateKey(123, "   ")
	require.Equal(t, "", key)
}

func TestOpenAIWSSessionTurnStateKey_NegativeGroupID(t *testing.T) {
	key := openAIWSSessionTurnStateKey(-1, "hash")
	require.Equal(t, "-1:hash", key)
}

func TestOpenAIWSSessionTurnStateKey_ZeroGroupID(t *testing.T) {
	key := openAIWSSessionTurnStateKey(0, "hash")
	require.Equal(t, "0:hash", key)
}

// ===================================================================
// 8. openAIWSIngressContextSessionKey uses strconv
// ===================================================================

func TestOpenAIWSIngressContextSessionKey_NormalCase(t *testing.T) {
	key := openAIWSIngressContextSessionKey(456, "session_xyz")
	require.Equal(t, "456:session_xyz", key)
}

func TestOpenAIWSIngressContextSessionKey_EmptySessionHash(t *testing.T) {
	key := openAIWSIngressContextSessionKey(456, "")
	require.Equal(t, "", key)
}

func TestOpenAIWSIngressContextSessionKey_WhitespaceOnlySessionHash(t *testing.T) {
	key := openAIWSIngressContextSessionKey(456, "  \t  ")
	require.Equal(t, "", key)
}

func TestOpenAIWSIngressContextSessionKey_LargeGroupID(t *testing.T) {
	key := openAIWSIngressContextSessionKey(9223372036854775807, "h")
	require.Equal(t, "9223372036854775807:h", key)
}

// ===================================================================
// 9. deriveOpenAISessionHash and deriveOpenAILegacySessionHash
// ===================================================================

func TestDeriveOpenAISessionHash_EmptyReturnsEmpty(t *testing.T) {
	require.Equal(t, "", deriveOpenAISessionHash(""))
	require.Equal(t, "", deriveOpenAISessionHash("   "))
}

func TestDeriveOpenAISessionHash_ProducesXXHash16Chars(t *testing.T) {
	hash := deriveOpenAISessionHash("test_session_id")
	require.Len(t, hash, 16, "xxhash hex should be exactly 16 chars, got %q", hash)
	// Verify it's valid hex
	for _, ch := range hash {
		require.True(t, (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f'),
			"hash should be lowercase hex, got char %c in %q", ch, hash)
	}
}

func TestDeriveOpenAISessionHash_Deterministic(t *testing.T) {
	h1 := deriveOpenAISessionHash("session_abc")
	h2 := deriveOpenAISessionHash("session_abc")
	require.Equal(t, h1, h2)
}

func TestDeriveOpenAILegacySessionHash_EmptyReturnsEmpty(t *testing.T) {
	require.Equal(t, "", deriveOpenAILegacySessionHash(""))
	require.Equal(t, "", deriveOpenAILegacySessionHash("   "))
}

func TestDeriveOpenAILegacySessionHash_ProducesSHA256_64Chars(t *testing.T) {
	hash := deriveOpenAILegacySessionHash("test_session_id")
	require.Len(t, hash, 64, "SHA-256 hex should be exactly 64 chars, got %q", hash)
	for _, ch := range hash {
		require.True(t, (ch >= '0' && ch <= '9') || (ch >= 'a' && ch <= 'f'),
			"hash should be lowercase hex, got char %c in %q", ch, hash)
	}
}

func TestDeriveOpenAILegacySessionHash_Deterministic(t *testing.T) {
	h1 := deriveOpenAILegacySessionHash("session_xyz")
	h2 := deriveOpenAILegacySessionHash("session_xyz")
	require.Equal(t, h1, h2)
}

func TestDeriveOpenAISessionHashes_MatchesIndividualFunctions(t *testing.T) {
	sessionID := "test_combined_session"
	currentHash, legacyHash := deriveOpenAISessionHashes(sessionID)

	require.Equal(t, deriveOpenAISessionHash(sessionID), currentHash)
	require.Equal(t, deriveOpenAILegacySessionHash(sessionID), legacyHash)
}

func TestDeriveOpenAISessionHashes_EmptyReturnsEmpty(t *testing.T) {
	currentHash, legacyHash := deriveOpenAISessionHashes("")
	require.Equal(t, "", currentHash)
	require.Equal(t, "", legacyHash)
}

func TestDeriveOpenAISessionHashes_DifferentInputsDifferentOutputs(t *testing.T) {
	h1Current, h1Legacy := deriveOpenAISessionHashes("session_A")
	h2Current, h2Legacy := deriveOpenAISessionHashes("session_B")
	require.NotEqual(t, h1Current, h2Current)
	require.NotEqual(t, h1Legacy, h2Legacy)
}

func TestDeriveOpenAISessionHash_DifferentFromLegacy(t *testing.T) {
	// xxhash and SHA-256 produce completely different outputs for the same input
	currentHash := deriveOpenAISessionHash("same_input")
	legacyHash := deriveOpenAILegacySessionHash("same_input")
	require.NotEqual(t, currentHash, legacyHash, "xxhash and SHA-256 should produce different results")
	require.Len(t, currentHash, 16)
	require.Len(t, legacyHash, 64)
}

// ===================================================================
// 10. State store sharded lock (responseToConn)
// ===================================================================

func TestConnShard_DistributesAcrossShards(t *testing.T) {
	store := NewOpenAIWSStateStore(nil).(*defaultOpenAIWSStateStore)

	shardHits := make(map[int]int)
	for i := 0; i < 256; i++ {
		key := fmt.Sprintf("resp_%d", i)
		shard := store.connShard(key)
		// Find which shard index this is
		for j := 0; j < openAIWSStateStoreConnShards; j++ {
			if shard == &store.responseToConnShards[j] {
				shardHits[j]++
				break
			}
		}
	}

	// With 256 keys and 16 shards, each shard should get some keys.
	// We don't require perfect uniformity, just that keys aren't all in one shard.
	require.Greater(t, len(shardHits), 1, "keys must be distributed across multiple shards, got %d shards used", len(shardHits))
	require.GreaterOrEqual(t, len(shardHits), openAIWSStateStoreConnShards/2,
		"keys should hit at least half the shards for reasonable distribution")
}

func TestStateStore_ShardedBindGetDelete(t *testing.T) {
	store := NewOpenAIWSStateStore(nil)

	store.BindResponseConn("resp_shard_1", "conn_a", time.Minute)
	store.BindResponseConn("resp_shard_2", "conn_b", time.Minute)

	conn1, ok1 := store.GetResponseConn("resp_shard_1")
	require.True(t, ok1)
	require.Equal(t, "conn_a", conn1)

	conn2, ok2 := store.GetResponseConn("resp_shard_2")
	require.True(t, ok2)
	require.Equal(t, "conn_b", conn2)

	store.DeleteResponseConn("resp_shard_1")
	_, ok1After := store.GetResponseConn("resp_shard_1")
	require.False(t, ok1After)

	// resp_shard_2 should still be accessible
	conn2After, ok2After := store.GetResponseConn("resp_shard_2")
	require.True(t, ok2After)
	require.Equal(t, "conn_b", conn2After)
}

func TestStateStore_ShardedConcurrentAccessNoRace(t *testing.T) {
	store := NewOpenAIWSStateStore(nil)
	const goroutines = 32
	const opsPerGoroutine = 200

	var wg sync.WaitGroup
	wg.Add(goroutines)

	for g := 0; g < goroutines; g++ {
		g := g
		go func() {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				key := fmt.Sprintf("resp_conc_%d_%d", g, i)
				connID := fmt.Sprintf("conn_%d_%d", g, i)

				store.BindResponseConn(key, connID, time.Minute)
				got, ok := store.GetResponseConn(key)
				if ok {
					_ = got
				}
				store.DeleteResponseConn(key)
			}
		}()
	}

	wg.Wait()
}

// ===================================================================
// 11. State store: Get paths don't call maybeCleanup
// ===================================================================

func TestStateStore_GetPaths_DoNotTriggerCleanup(t *testing.T) {
	raw := NewOpenAIWSStateStore(nil)
	store := raw.(*defaultOpenAIWSStateStore)

	// Seed some data so Get paths have something to read
	store.BindResponseConn("resp_get_noclean", "conn_1", time.Minute)
	store.BindResponsePendingToolCalls(0, "resp_get_noclean", []string{"call_1"}, time.Minute)
	store.BindSessionTurnState(1, "session_get_noclean", "state_1", time.Minute)
	store.BindSessionConn(1, "session_get_noclean", "conn_1", time.Minute)

	// Record the lastCleanupUnixNano after the Bind calls
	cleanupBefore := store.lastCleanupUnixNano.Load()

	// Set lastCleanup to the future to ensure no cleanup triggers from Binds
	store.lastCleanupUnixNano.Store(time.Now().Add(time.Hour).UnixNano())
	cleanupFrozen := store.lastCleanupUnixNano.Load()

	// Perform many Get calls
	for i := 0; i < 100; i++ {
		store.GetResponseConn("resp_get_noclean")
		store.GetResponsePendingToolCalls(0, "resp_get_noclean")
		store.GetSessionTurnState(1, "session_get_noclean")
		store.GetSessionConn(1, "session_get_noclean")
	}

	cleanupAfterGets := store.lastCleanupUnixNano.Load()
	require.Equal(t, cleanupFrozen, cleanupAfterGets,
		"Get paths must NOT change lastCleanupUnixNano (was %d before, %d after)", cleanupBefore, cleanupAfterGets)
}

func TestStateStore_MaybeCleanup_NilReceiverDoesNotPanic(t *testing.T) {
	var store *defaultOpenAIWSStateStore
	require.NotPanics(t, func() {
		store.maybeCleanup()
	})
}

func TestStateStore_BindPaths_MayTriggerCleanup(t *testing.T) {
	raw := NewOpenAIWSStateStore(nil)
	store := raw.(*defaultOpenAIWSStateStore)

	// Set lastCleanup to long ago to ensure cleanup triggers on next Bind
	pastNano := time.Now().Add(-2 * openAIWSStateStoreCleanupInterval).UnixNano()
	store.lastCleanupUnixNano.Store(pastNano)

	store.BindResponseConn("resp_bind_trigger", "conn_trigger", time.Minute)

	cleanupAfterBind := store.lastCleanupUnixNano.Load()
	require.NotEqual(t, pastNano, cleanupAfterBind,
		"Bind paths should trigger maybeCleanup when interval has elapsed")
}

// ===================================================================
// 12. GetResponsePendingToolCalls returns internal slice directly
// ===================================================================

func TestGetResponsePendingToolCalls_ReturnsInternalSlice(t *testing.T) {
	raw := NewOpenAIWSStateStore(nil)
	store := raw.(*defaultOpenAIWSStateStore)

	store.BindResponsePendingToolCalls(0, "resp_slice_identity", []string{"call_x", "call_y"}, time.Minute)

	callIDs, ok := store.GetResponsePendingToolCalls(0, "resp_slice_identity")
	require.True(t, ok)
	require.Equal(t, []string{"call_x", "call_y"}, callIDs)

	// Verify it's the same underlying slice as stored in the binding (pointer equality).
	// The binding stores callIDs as a copied slice at bind time, but Get returns it directly.
	id := openAIWSResponsePendingToolCallsBindingKey(0, "resp_slice_identity")
	store.responsePendingToolMu.RLock()
	binding, exists := store.responsePendingTool[id]
	store.responsePendingToolMu.RUnlock()
	require.True(t, exists)

	// Check pointer equality of the underlying array via unsafe
	gotHeader := (*[3]uintptr)(unsafe.Pointer(&callIDs))
	internalHeader := (*[3]uintptr)(unsafe.Pointer(&binding.callIDs))
	require.Equal(t, gotHeader[0], internalHeader[0],
		"returned slice should share the same underlying array pointer as the internal binding (zero-copy)")
}

// ===================================================================
// Additional edge-case tests for completeness
// ===================================================================

func TestParseOpenAIWSEventType_MalformedJSON(t *testing.T) {
	// Should not panic on malformed JSON
	eventType, responseID := parseOpenAIWSEventType([]byte(`{not valid json`))
	// gjson returns empty for invalid JSON
	_ = eventType
	_ = responseID
}

func TestOpenAIWSResponseAccountCacheKey_EmptyInput(t *testing.T) {
	// Even empty string should produce a valid key
	key := openAIWSResponseAccountCacheKey("")
	require.True(t, strings.HasPrefix(key, openAIWSResponseAccountCachePrefix+"v2:"))
	hexPart := strings.TrimPrefix(key, openAIWSResponseAccountCachePrefix+"v2:")
	require.Len(t, hexPart, 16)
}

func TestConnShard_SameKeyAlwaysSameShard(t *testing.T) {
	store := NewOpenAIWSStateStore(nil).(*defaultOpenAIWSStateStore)
	shard1 := store.connShard("resp_stable_key")
	shard2 := store.connShard("resp_stable_key")
	require.Equal(t, shard1, shard2, "same key must always map to the same shard")
}

func TestMaybeTouchLease_ConcurrentSafe(t *testing.T) {
	c := &openAIWSIngressContext{}
	var wg sync.WaitGroup
	const goroutines = 16

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				c.maybeTouchLease(5 * time.Minute)
			}
		}()
	}
	wg.Wait()

	require.NotZero(t, c.lastTouchUnixNano.Load())
	require.False(t, c.expiresAt().IsZero())
}

func TestActiveConn_SingleOwnerSequentialAccess(t *testing.T) {
	// activeConn uses a non-synchronized cachedConn field by design.
	// A lease is only used by a single goroutine (the forwarding loop).
	// This test verifies sequential repeated calls from the same goroutine
	// always return the same cached conn without error.
	upstream := &openAIWSNoopConn{}
	ctx := &openAIWSIngressContext{
		ownerID:  "owner_seq",
		upstream: upstream,
	}
	lease := &openAIWSIngressContextLease{
		context: ctx,
		ownerID: "owner_seq",
	}

	for i := 0; i < 1000; i++ {
		conn, err := lease.activeConn()
		require.NoError(t, err)
		require.Equal(t, upstream, conn)
	}
}

func TestOpenAIWSIngressContextSessionKey_ConsistentWithTurnStateKey(t *testing.T) {
	// Both functions use the same pattern: strconv.FormatInt(groupID, 10) + ":" + hash
	groupID := int64(42)
	sessionHash := "test_hash"

	sessionKey := openAIWSIngressContextSessionKey(groupID, sessionHash)
	turnStateKey := openAIWSSessionTurnStateKey(groupID, sessionHash)

	require.Equal(t, sessionKey, turnStateKey,
		"openAIWSIngressContextSessionKey and openAIWSSessionTurnStateKey should produce identical keys for the same inputs")
}

func TestStateStore_ShardedBindOverwrite(t *testing.T) {
	store := NewOpenAIWSStateStore(nil)

	store.BindResponseConn("resp_overwrite", "conn_old", time.Minute)
	store.BindResponseConn("resp_overwrite", "conn_new", time.Minute)

	conn, ok := store.GetResponseConn("resp_overwrite")
	require.True(t, ok)
	require.Equal(t, "conn_new", conn, "later bind should overwrite earlier bind")
}

func TestStateStore_ShardedTTLExpiry(t *testing.T) {
	store := NewOpenAIWSStateStore(nil)

	store.BindResponseConn("resp_ttl_shard", "conn_ttl", 30*time.Millisecond)
	conn, ok := store.GetResponseConn("resp_ttl_shard")
	require.True(t, ok)
	require.Equal(t, "conn_ttl", conn)

	time.Sleep(60 * time.Millisecond)
	_, ok = store.GetResponseConn("resp_ttl_shard")
	require.False(t, ok, "entry should be expired after TTL")
}
