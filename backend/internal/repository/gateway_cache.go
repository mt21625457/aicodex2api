package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/cespare/xxhash/v2"
	"github.com/redis/go-redis/v9"
)

const stickySessionPrefix = "sticky_session:"
const openAIWSSessionLastResponsePrefix = "openai_ws_session_last_response:"
const openAIWSResponsePendingToolCallsPrefix = "openai_ws_response_pending_tool_calls:"

type gatewayCache struct {
	rdb *redis.Client
}

func NewGatewayCache(rdb *redis.Client) service.GatewayCache {
	return &gatewayCache{rdb: rdb}
}

// buildSessionKey 构建 session key，包含 groupID 实现分组隔离
// 格式: sticky_session:{groupID}:{sessionHash}
func buildSessionKey(groupID int64, sessionHash string) string {
	return fmt.Sprintf("%s%d:%s", stickySessionPrefix, groupID, sessionHash)
}

func buildOpenAIWSSessionLastResponseKey(groupID int64, sessionHash string) string {
	return fmt.Sprintf("%s%d:%s", openAIWSSessionLastResponsePrefix, groupID, sessionHash)
}

func buildOpenAIWSResponsePendingToolCallsKey(responseID string) string {
	id := strings.TrimSpace(responseID)
	if id == "" {
		return ""
	}
	return openAIWSResponsePendingToolCallsPrefix + strconv.FormatUint(xxhash.Sum64String(id), 16)
}

func (c *gatewayCache) GetSessionAccountID(ctx context.Context, groupID int64, sessionHash string) (int64, error) {
	key := buildSessionKey(groupID, sessionHash)
	return c.rdb.Get(ctx, key).Int64()
}

func (c *gatewayCache) SetSessionAccountID(ctx context.Context, groupID int64, sessionHash string, accountID int64, ttl time.Duration) error {
	key := buildSessionKey(groupID, sessionHash)
	return c.rdb.Set(ctx, key, accountID, ttl).Err()
}

func (c *gatewayCache) RefreshSessionTTL(ctx context.Context, groupID int64, sessionHash string, ttl time.Duration) error {
	key := buildSessionKey(groupID, sessionHash)
	return c.rdb.Expire(ctx, key, ttl).Err()
}

// DeleteSessionAccountID 删除粘性会话与账号的绑定关系。
// 当检测到绑定的账号不可用（如状态错误、禁用、不可调度等）时调用，
// 以便下次请求能够重新选择可用账号。
//
// DeleteSessionAccountID removes the sticky session binding for the given session.
// Called when the bound account becomes unavailable (e.g., error status, disabled,
// or unschedulable), allowing subsequent requests to select a new available account.
func (c *gatewayCache) DeleteSessionAccountID(ctx context.Context, groupID int64, sessionHash string) error {
	key := buildSessionKey(groupID, sessionHash)
	return c.rdb.Del(ctx, key).Err()
}

func (c *gatewayCache) SetOpenAIWSSessionLastResponseID(ctx context.Context, groupID int64, sessionHash, responseID string, ttl time.Duration) error {
	key := buildOpenAIWSSessionLastResponseKey(groupID, sessionHash)
	return c.rdb.Set(ctx, key, responseID, ttl).Err()
}

func (c *gatewayCache) GetOpenAIWSSessionLastResponseID(ctx context.Context, groupID int64, sessionHash string) (string, error) {
	key := buildOpenAIWSSessionLastResponseKey(groupID, sessionHash)
	return c.rdb.Get(ctx, key).Result()
}

func (c *gatewayCache) DeleteOpenAIWSSessionLastResponseID(ctx context.Context, groupID int64, sessionHash string) error {
	key := buildOpenAIWSSessionLastResponseKey(groupID, sessionHash)
	return c.rdb.Del(ctx, key).Err()
}

func (c *gatewayCache) SetOpenAIWSResponsePendingToolCalls(ctx context.Context, responseID string, callIDs []string, ttl time.Duration) error {
	key := buildOpenAIWSResponsePendingToolCallsKey(responseID)
	if key == "" {
		return nil
	}
	normalizedCallIDs := normalizeOpenAIWSResponsePendingToolCallIDs(callIDs)
	if len(normalizedCallIDs) == 0 {
		return c.rdb.Del(ctx, key).Err()
	}
	raw, err := json.Marshal(normalizedCallIDs)
	if err != nil {
		return err
	}
	return c.rdb.Set(ctx, key, raw, ttl).Err()
}

func (c *gatewayCache) GetOpenAIWSResponsePendingToolCalls(ctx context.Context, responseID string) ([]string, error) {
	key := buildOpenAIWSResponsePendingToolCallsKey(responseID)
	if key == "" {
		return nil, nil
	}
	raw, err := c.rdb.Get(ctx, key).Bytes()
	if err != nil {
		return nil, err
	}
	var callIDs []string
	if err := json.Unmarshal(raw, &callIDs); err != nil {
		return nil, err
	}
	return normalizeOpenAIWSResponsePendingToolCallIDs(callIDs), nil
}

func (c *gatewayCache) DeleteOpenAIWSResponsePendingToolCalls(ctx context.Context, responseID string) error {
	key := buildOpenAIWSResponsePendingToolCallsKey(responseID)
	if key == "" {
		return nil
	}
	return c.rdb.Del(ctx, key).Err()
}

func normalizeOpenAIWSResponsePendingToolCallIDs(callIDs []string) []string {
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
