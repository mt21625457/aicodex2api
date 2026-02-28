package service

import (
	"context"
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

