package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

type openAITokenCacheNilRepoStub struct {
	token       string
	lockCalls   atomic.Int32
	setCalls    atomic.Int32
	getCalls    atomic.Int32
	lockEnabled bool
}

func (s *openAITokenCacheNilRepoStub) GetAccessToken(context.Context, string) (string, error) {
	s.getCalls.Add(1)
	return s.token, nil
}

func (s *openAITokenCacheNilRepoStub) SetAccessToken(context.Context, string, string, time.Duration) error {
	s.setCalls.Add(1)
	return nil
}

func (s *openAITokenCacheNilRepoStub) DeleteAccessToken(context.Context, string) error {
	return nil
}

func (s *openAITokenCacheNilRepoStub) AcquireRefreshLock(context.Context, string, time.Duration) (bool, error) {
	s.lockCalls.Add(1)
	return s.lockEnabled, nil
}

func (s *openAITokenCacheNilRepoStub) ReleaseRefreshLock(context.Context, string) error {
	return nil
}

type openAIOAuthClientNilRepoStub struct{}

func (s *openAIOAuthClientNilRepoStub) ExchangeCode(
	context.Context,
	string,
	string,
	string,
	string,
	string,
) (*openai.TokenResponse, error) {
	return nil, errors.New("not implemented")
}

func (s *openAIOAuthClientNilRepoStub) RefreshToken(
	context.Context,
	string,
	string,
) (*openai.TokenResponse, error) {
	return nil, errors.New("not implemented")
}

func (s *openAIOAuthClientNilRepoStub) RefreshTokenWithClientID(
	context.Context,
	string,
	string,
	string,
) (*openai.TokenResponse, error) {
	return &openai.TokenResponse{
		AccessToken:  "fresh-token",
		RefreshToken: "fresh-refresh-token",
		ExpiresIn:    3600,
	}, nil
}

func TestOpenAITokenProviderRefreshWithNilAccountRepo(t *testing.T) {
	cache := &openAITokenCacheNilRepoStub{lockEnabled: true}
	oauthSvc := NewOpenAIOAuthService(nil, &openAIOAuthClientNilRepoStub{})
	provider := NewOpenAITokenProvider(nil, cache, oauthSvc)

	expiresAt := time.Now().Add(30 * time.Second).Format(time.RFC3339)
	account := &Account{
		ID:       3001,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token":  "stale-token",
			"refresh_token": "refresh-token",
			"expires_at":    expiresAt,
		},
	}

	token, err := provider.GetAccessToken(context.Background(), account)
	require.NoError(t, err)
	require.Equal(t, "fresh-token", token)
	require.Equal(t, int32(1), cache.lockCalls.Load())
	require.Equal(t, int32(1), cache.setCalls.Load())
}
