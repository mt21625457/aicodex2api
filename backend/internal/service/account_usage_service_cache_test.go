package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestAccountUsageService_InvalidateAccountCache(t *testing.T) {
	t.Parallel()

	cache := NewUsageCache()
	cache.apiCache.Store(int64(7), &apiUsageCache{timestamp: time.Now()})
	cache.windowStatsCache.Store(int64(7), &windowStatsCache{timestamp: time.Now()})
	cache.antigravityCache.Store(int64(7), &antigravityUsageCache{timestamp: time.Now()})
	cache.openAIProbeCache.Store(int64(7), time.Now())

	svc := &AccountUsageService{cache: cache}
	svc.InvalidateAccountCache(7)

	_, ok := cache.apiCache.Load(int64(7))
	require.False(t, ok)
	_, ok = cache.windowStatsCache.Load(int64(7))
	require.False(t, ok)
	_, ok = cache.antigravityCache.Load(int64(7))
	require.False(t, ok)
	_, ok = cache.openAIProbeCache.Load(int64(7))
	require.False(t, ok)
}
