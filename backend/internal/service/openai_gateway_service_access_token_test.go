package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIGatewayServiceGetAccessToken(t *testing.T) {
	t.Parallel()

	svc := &OpenAIGatewayService{}

	t.Run("nil account", func(t *testing.T) {
		token, tokenType, err := svc.GetAccessToken(context.Background(), nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "account is nil")
		require.Empty(t, token)
		require.Empty(t, tokenType)
	})

	t.Run("oauth account", func(t *testing.T) {
		account := &Account{
			Platform: PlatformOpenAI,
			Type:     AccountTypeOAuth,
			Credentials: map[string]any{
				"access_token": "oauth-token",
			},
		}
		token, tokenType, err := svc.GetAccessToken(context.Background(), account)
		require.NoError(t, err)
		require.Equal(t, "oauth-token", token)
		require.Equal(t, "oauth", tokenType)
	})

	t.Run("oauth account trims token", func(t *testing.T) {
		account := &Account{
			Platform: PlatformOpenAI,
			Type:     AccountTypeOAuth,
			Credentials: map[string]any{
				"access_token": "  oauth-token-trim  ",
			},
		}
		token, tokenType, err := svc.GetAccessToken(context.Background(), account)
		require.NoError(t, err)
		require.Equal(t, "oauth-token-trim", token)
		require.Equal(t, "oauth", tokenType)
	})

	t.Run("api key account", func(t *testing.T) {
		account := &Account{
			Platform: PlatformOpenAI,
			Type:     AccountTypeAPIKey,
			Credentials: map[string]any{
				"api_key": "sk-live-token",
			},
		}
		token, tokenType, err := svc.GetAccessToken(context.Background(), account)
		require.NoError(t, err)
		require.Equal(t, "sk-live-token", token)
		require.Equal(t, "apikey", tokenType)
	})

	t.Run("api key account trims token", func(t *testing.T) {
		account := &Account{
			Platform: PlatformOpenAI,
			Type:     AccountTypeAPIKey,
			Credentials: map[string]any{
				"api_key": "  sk-live-trim  ",
			},
		}
		token, tokenType, err := svc.GetAccessToken(context.Background(), account)
		require.NoError(t, err)
		require.Equal(t, "sk-live-trim", token)
		require.Equal(t, "apikey", tokenType)
	})

	t.Run("unsupported account type", func(t *testing.T) {
		account := &Account{
			Platform: PlatformOpenAI,
			Type:     "unknown",
		}
		token, tokenType, err := svc.GetAccessToken(context.Background(), account)
		require.Error(t, err)
		require.Contains(t, err.Error(), "unsupported account type")
		require.Empty(t, token)
		require.Empty(t, tokenType)
	})
}
