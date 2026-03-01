//go:build unit

package service

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/stretchr/testify/require"
)

type openAIOAuthClientStubForRefresher struct {
	tokenResp *openai.TokenResponse
	err       error
}

func (s *openAIOAuthClientStubForRefresher) ExchangeCode(ctx context.Context, code, codeVerifier, redirectURI, proxyURL, clientID string) (*openai.TokenResponse, error) {
	return nil, s.err
}

func (s *openAIOAuthClientStubForRefresher) RefreshToken(ctx context.Context, refreshToken, proxyURL string) (*openai.TokenResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.tokenResp, nil
}

func (s *openAIOAuthClientStubForRefresher) RefreshTokenWithClientID(ctx context.Context, refreshToken, proxyURL string, clientID string) (*openai.TokenResponse, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.tokenResp, nil
}

func TestClaudeTokenRefresher_NeedsRefresh(t *testing.T) {
	refresher := &ClaudeTokenRefresher{}
	refreshWindow := 30 * time.Minute

	tests := []struct {
		name        string
		credentials map[string]any
		wantRefresh bool
	}{
		{
			name: "expires_at as string - expired",
			credentials: map[string]any{
				"expires_at": "1000", // 1970-01-01 00:16:40 UTC, 已过期
			},
			wantRefresh: true,
		},
		{
			name: "expires_at as float64 - expired",
			credentials: map[string]any{
				"expires_at": float64(1000), // 数字类型，已过期
			},
			wantRefresh: true,
		},
		{
			name: "expires_at as RFC3339 - expired",
			credentials: map[string]any{
				"expires_at": "1970-01-01T00:00:00Z", // RFC3339 格式，已过期
			},
			wantRefresh: true,
		},
		{
			name: "expires_at as string - far future",
			credentials: map[string]any{
				"expires_at": "9999999999", // 远未来
			},
			wantRefresh: false,
		},
		{
			name: "expires_at as float64 - far future",
			credentials: map[string]any{
				"expires_at": float64(9999999999), // 远未来，数字类型
			},
			wantRefresh: false,
		},
		{
			name: "expires_at as RFC3339 - far future",
			credentials: map[string]any{
				"expires_at": "2099-12-31T23:59:59Z", // RFC3339 格式，远未来
			},
			wantRefresh: false,
		},
		{
			name:        "expires_at missing",
			credentials: map[string]any{},
			wantRefresh: true,
		},
		{
			name: "expires_at is nil",
			credentials: map[string]any{
				"expires_at": nil,
			},
			wantRefresh: true,
		},
		{
			name: "expires_at is invalid string",
			credentials: map[string]any{
				"expires_at": "invalid",
			},
			wantRefresh: true,
		},
		{
			name:        "credentials is nil",
			credentials: nil,
			wantRefresh: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := &Account{
				Platform:    PlatformAnthropic,
				Type:        AccountTypeOAuth,
				Credentials: tt.credentials,
			}

			got := refresher.NeedsRefresh(account, refreshWindow)
			require.Equal(t, tt.wantRefresh, got)
		})
	}
}

func TestClaudeTokenRefresher_NeedsRefresh_WithinWindow(t *testing.T) {
	refresher := &ClaudeTokenRefresher{}
	refreshWindow := 30 * time.Minute

	// 设置一个在刷新窗口内的时间（当前时间 + 15分钟）
	expiresAt := time.Now().Add(15 * time.Minute).Unix()

	tests := []struct {
		name        string
		credentials map[string]any
	}{
		{
			name: "string type - within refresh window",
			credentials: map[string]any{
				"expires_at": strconv.FormatInt(expiresAt, 10),
			},
		},
		{
			name: "float64 type - within refresh window",
			credentials: map[string]any{
				"expires_at": float64(expiresAt),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := &Account{
				Platform:    PlatformAnthropic,
				Type:        AccountTypeOAuth,
				Credentials: tt.credentials,
			}

			got := refresher.NeedsRefresh(account, refreshWindow)
			require.True(t, got, "should need refresh when within window")
		})
	}
}

func TestClaudeTokenRefresher_NeedsRefresh_OutsideWindow(t *testing.T) {
	refresher := &ClaudeTokenRefresher{}
	refreshWindow := 30 * time.Minute

	// 设置一个在刷新窗口外的时间（当前时间 + 1小时）
	expiresAt := time.Now().Add(1 * time.Hour).Unix()

	tests := []struct {
		name        string
		credentials map[string]any
	}{
		{
			name: "string type - outside refresh window",
			credentials: map[string]any{
				"expires_at": strconv.FormatInt(expiresAt, 10),
			},
		},
		{
			name: "float64 type - outside refresh window",
			credentials: map[string]any{
				"expires_at": float64(expiresAt),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := &Account{
				Platform:    PlatformAnthropic,
				Type:        AccountTypeOAuth,
				Credentials: tt.credentials,
			}

			got := refresher.NeedsRefresh(account, refreshWindow)
			require.False(t, got, "should not need refresh when outside window")
		})
	}
}

func TestNeedsRefreshWithoutExpiry_RecentlyUpdated(t *testing.T) {
	refreshWindow := 30 * time.Minute

	t.Run("recently_updated_skips_refresh", func(t *testing.T) {
		// 账号近期更新过（5 分钟前），不需要刷新
		account := &Account{
			Platform:    PlatformAnthropic,
			Type:        AccountTypeOAuth,
			Credentials: map[string]any{},
			UpdatedAt:   time.Now().Add(-5 * time.Minute),
		}
		refresher := &ClaudeTokenRefresher{}
		require.False(t, refresher.NeedsRefresh(account, refreshWindow),
			"近期更新过的账号无 expires_at 时不应刷新")
	})

	t.Run("old_updated_needs_refresh", func(t *testing.T) {
		// 账号很久没更新（2 小时前），需要刷新
		account := &Account{
			Platform:    PlatformOpenAI,
			Type:        AccountTypeOAuth,
			Credentials: map[string]any{},
			UpdatedAt:   time.Now().Add(-2 * time.Hour),
		}
		refresher := &OpenAITokenRefresher{}
		require.True(t, refresher.NeedsRefresh(account, refreshWindow),
			"长期未更新的账号无 expires_at 时应刷新")
	})
}

func TestClaudeTokenRefresher_CanRefresh(t *testing.T) {
	refresher := &ClaudeTokenRefresher{}

	tests := []struct {
		name     string
		platform string
		accType  string
		want     bool
	}{
		{
			name:     "anthropic oauth - can refresh",
			platform: PlatformAnthropic,
			accType:  AccountTypeOAuth,
			want:     true,
		},
		{
			name:     "anthropic api-key - cannot refresh",
			platform: PlatformAnthropic,
			accType:  AccountTypeAPIKey,
			want:     false,
		},
		{
			name:     "openai oauth - cannot refresh",
			platform: PlatformOpenAI,
			accType:  AccountTypeOAuth,
			want:     false,
		},
		{
			name:     "gemini oauth - cannot refresh",
			platform: PlatformGemini,
			accType:  AccountTypeOAuth,
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := &Account{
				Platform: tt.platform,
				Type:     tt.accType,
			}

			got := refresher.CanRefresh(account)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestOpenAITokenRefresher_CanRefresh(t *testing.T) {
	refresher := &OpenAITokenRefresher{}

	tests := []struct {
		name     string
		platform string
		accType  string
		want     bool
	}{
		{
			name:     "openai oauth - can refresh",
			platform: PlatformOpenAI,
			accType:  AccountTypeOAuth,
			want:     true,
		},
		{
			name:     "sora oauth - cannot refresh directly",
			platform: PlatformSora,
			accType:  AccountTypeOAuth,
			want:     false,
		},
		{
			name:     "openai apikey - cannot refresh",
			platform: PlatformOpenAI,
			accType:  AccountTypeAPIKey,
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := &Account{
				Platform: tt.platform,
				Type:     tt.accType,
			}
			require.Equal(t, tt.want, refresher.CanRefresh(account))
		})
	}
}

func TestOpenAITokenRefresher_NeedsRefresh(t *testing.T) {
	refresher := &OpenAITokenRefresher{}
	refreshWindow := 30 * time.Minute

	tests := []struct {
		name        string
		credentials map[string]any
		wantRefresh bool
	}{
		{
			name: "expires_at missing",
			credentials: map[string]any{
				"access_token": "token",
			},
			wantRefresh: true,
		},
		{
			name: "expires_at invalid",
			credentials: map[string]any{
				"expires_at": "invalid",
			},
			wantRefresh: true,
		},
		{
			name: "expires_at expired",
			credentials: map[string]any{
				"expires_at": strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10),
			},
			wantRefresh: true,
		},
		{
			name: "expires_at far future",
			credentials: map[string]any{
				"expires_at": strconv.FormatInt(time.Now().Add(2*time.Hour).Unix(), 10),
			},
			wantRefresh: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := &Account{
				Platform:    PlatformOpenAI,
				Type:        AccountTypeOAuth,
				Credentials: tt.credentials,
			}
			require.Equal(t, tt.wantRefresh, refresher.NeedsRefresh(account, refreshWindow))
		})
	}
}

func TestOpenAITokenRefresher_Refresh_AsyncSyncUsesCopiedCredentials(t *testing.T) {
	oauthSvc := NewOpenAIOAuthService(nil, &openAIOAuthClientStubForRefresher{
		tokenResp: &openai.TokenResponse{
			AccessToken:  "new_access_token",
			RefreshToken: "new_refresh_token",
			ExpiresIn:    3600,
		},
	})
	refresher := NewOpenAITokenRefresher(oauthSvc, &mockAccountRepoForGemini{})
	refresher.SetSyncLinkedSoraAccounts(true)
	refresher.syncLinkedSoraSem = make(chan struct{}, 1)

	readNow := make(chan struct{})
	seenValue := make(chan string, 1)
	refresher.syncLinkedSoraAccountsFn = func(ctx context.Context, openaiAccountID int64, newCredentials map[string]any) {
		<-readNow
		if v, ok := newCredentials["custom"].(string); ok {
			seenValue <- v
			return
		}
		seenValue <- ""
	}

	account := &Account{
		ID:       1001,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"refresh_token": "old_refresh_token",
			"client_id":     "test-client",
			"custom":        "original",
		},
	}

	newCredentials, err := refresher.Refresh(context.Background(), account)
	require.NoError(t, err)
	require.NotNil(t, newCredentials)

	newCredentials["custom"] = "mutated_after_return"
	close(readNow)

	select {
	case got := <-seenValue:
		require.Equal(t, "original", got, "异步同步应使用 credentials 副本，避免并发写污染")
	case <-time.After(500 * time.Millisecond):
		t.Fatal("timed out waiting for sync hook")
	}
}

func TestOpenAITokenRefresher_Refresh_FallsBackToSyncWhenLimiterFull(t *testing.T) {
	oauthSvc := NewOpenAIOAuthService(nil, &openAIOAuthClientStubForRefresher{
		tokenResp: &openai.TokenResponse{
			AccessToken:  "new_access_token",
			RefreshToken: "new_refresh_token",
			ExpiresIn:    3600,
		},
	})
	refresher := NewOpenAITokenRefresher(oauthSvc, &mockAccountRepoForGemini{})
	refresher.SetSyncLinkedSoraAccounts(true)
	refresher.syncLinkedSoraSem = make(chan struct{}, 1)
	refresher.syncLinkedSoraSem <- struct{}{} // 填满 limiter，强制走同步降级路径

	entered := make(chan struct{})
	releaseSync := make(chan struct{})
	refresher.syncLinkedSoraAccountsFn = func(ctx context.Context, openaiAccountID int64, newCredentials map[string]any) {
		close(entered)
		<-releaseSync
	}

	account := &Account{
		ID:       1002,
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"refresh_token": "old_refresh_token",
			"client_id":     "test-client",
		},
	}

	done := make(chan struct{})
	go func() {
		_, _ = refresher.Refresh(context.Background(), account)
		close(done)
	}()

	select {
	case <-entered:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("sync hook was not invoked")
	}

	select {
	case <-done:
		t.Fatal("Refresh should block when falling back to synchronous linked-sora sync")
	default:
	}

	close(releaseSync)
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Refresh did not finish after releasing synchronous sync hook")
	}
}
