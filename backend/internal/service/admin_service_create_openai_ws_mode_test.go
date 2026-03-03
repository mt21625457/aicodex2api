package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type accountRepoStubForCreateModeValidation struct {
	AccountRepository
	createCalled bool
	createErr    error
}

func (s *accountRepoStubForCreateModeValidation) Create(_ context.Context, account *Account) error {
	s.createCalled = true
	if s.createErr != nil {
		return s.createErr
	}
	if account != nil && account.ID == 0 {
		account.ID = 1
	}
	return nil
}

func TestAdminService_CreateAccount_RejectsInvalidOpenAIWSMode(t *testing.T) {
	repo := &accountRepoStubForCreateModeValidation{}
	svc := &adminServiceImpl{accountRepo: repo}

	account, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name:                 "ws-mode-invalid",
		Platform:             PlatformOpenAI,
		Type:                 AccountTypeAPIKey,
		Credentials:          map[string]any{"api_key": "sk-test"},
		Extra:                map[string]any{"openai_apikey_responses_websockets_v2_mode": "shared"},
		SkipDefaultGroupBind: true,
	})
	require.Nil(t, account)
	require.Error(t, err)
	require.Contains(t, err.Error(), "INVALID_OPENAI_WS_MODE")
	require.False(t, repo.createCalled)
}

func TestAdminService_CreateAccount_AcceptsValidOpenAIWSMode(t *testing.T) {
	repo := &accountRepoStubForCreateModeValidation{}
	svc := &adminServiceImpl{accountRepo: repo}

	account, err := svc.CreateAccount(context.Background(), &CreateAccountInput{
		Name:                 "ws-mode-valid",
		Platform:             PlatformOpenAI,
		Type:                 AccountTypeAPIKey,
		Credentials:          map[string]any{"api_key": "sk-test"},
		Extra:                map[string]any{"openai_apikey_responses_websockets_v2_mode": "passthrough"},
		SkipDefaultGroupBind: true,
	})
	require.NoError(t, err)
	require.NotNil(t, account)
	require.True(t, repo.createCalled)
	require.Equal(t, int64(1), account.ID)
}
