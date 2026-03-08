package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

type accountRepoUpdateExtraRecorder struct {
	AccountRepository
	updatedID int64
	updates   map[string]any
}

func (r *accountRepoUpdateExtraRecorder) UpdateExtra(_ context.Context, id int64, updates map[string]any) error {
	r.updatedID = id
	r.updates = make(map[string]any, len(updates))
	for key, value := range updates {
		r.updates[key] = value
	}
	return nil
}

func TestAccountTestService_testOpenAIAccountConnection_PersistsCodexUsageSnapshot(t *testing.T) {
	upstream := &queuedHTTPUpstream{
		responses: []*http.Response{func() *http.Response {
			resp := newJSONResponse(http.StatusOK, "data: {\"type\":\"response.completed\"}\n\n")
			resp.Header.Set("x-codex-primary-used-percent", "17")
			resp.Header.Set("x-codex-primary-reset-after-seconds", "3600")
			resp.Header.Set("x-codex-primary-window-minutes", "10080")
			resp.Header.Set("x-codex-secondary-used-percent", "42.5")
			resp.Header.Set("x-codex-secondary-reset-after-seconds", "300")
			resp.Header.Set("x-codex-secondary-window-minutes", "300")
			return resp
		}()},
	}
	repo := &accountRepoUpdateExtraRecorder{}
	svc := &AccountTestService{
		accountRepo:  repo,
		httpUpstream: upstream,
	}
	account := &Account{
		ID:          9,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeOAuth,
		Concurrency: 1,
		Credentials: map[string]any{
			"access_token": "test-token",
		},
		Extra: map[string]any{},
	}

	c, rec := newSoraTestContext()
	err := svc.testOpenAIAccountConnection(c, account, "gpt-5")

	require.NoError(t, err)
	require.Equal(t, int64(9), repo.updatedID)
	require.Equal(t, 42.5, repo.updates["codex_5h_used_percent"])
	require.Equal(t, 17.0, repo.updates["codex_7d_used_percent"])
	require.NotEmpty(t, repo.updates["codex_usage_updated_at"])
	require.Contains(t, repo.updates, "codex_5h_reset_at")
	require.Contains(t, repo.updates, "codex_7d_reset_at")
	require.Equal(t, 42.5, account.Extra["codex_5h_used_percent"])
	require.Equal(t, 17.0, account.Extra["codex_7d_used_percent"])
	require.Contains(t, rec.Body.String(), `"type":"test_complete","success":true`)
}
