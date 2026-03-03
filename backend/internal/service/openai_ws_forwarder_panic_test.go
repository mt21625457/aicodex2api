package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type openAIWSPanicResolver struct{}

func (openAIWSPanicResolver) Resolve(account *Account) OpenAIWSProtocolDecision {
	panic("resolver panic")
}

type openAIWSPanicStateStore struct {
	OpenAIWSStateStore
}

func (openAIWSPanicStateStore) GetSessionTurnState(groupID int64, sessionHash string) (string, bool) {
	panic("state_store panic")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_PanicRecovered(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := buildIngressPolicyTestConfig()
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	svc := buildIngressPolicyTestService(cfg)
	svc.openaiWSResolver = openAIWSPanicResolver{}
	account := buildIngressPolicyTestAccount(map[string]any{
		"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModeCtxPool,
	})

	serverErr := runIngressProxyWithFirstPayload(t, svc, account, `{"type":"response.create","model":"gpt-5.1","stream":false}`)
	var closeErr *OpenAIWSClientCloseError
	require.ErrorAs(t, serverErr, &closeErr)
	require.Equal(t, coderws.StatusInternalError, closeErr.StatusCode())
	require.Equal(t, "internal websocket proxy panic", closeErr.Reason())
}

func TestOpenAIGatewayService_ForwardOpenAIWSV2_PanicRecovered(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true

	svc := &OpenAIGatewayService{
		cfg:                cfg,
		openaiWSStateStore: openAIWSPanicStateStore{},
		openaiWSResolver:   NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:      NewCodexToolCorrector(),
		cache:              &stubGatewayCache{},
	}

	account := &Account{
		ID:          445,
		Name:        "openai-forwarder-panic",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
	}

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil)
	req.Header.Set("session_id", "sess-panic-check")
	req.Header.Set("User-Agent", "unit-test-agent/1.0")
	ginCtx.Request = req

	_, err := svc.forwardOpenAIWSV2(
		context.Background(),
		ginCtx,
		account,
		map[string]any{
			"model": "gpt-5.1",
			"input": []any{},
		},
		"sk-test",
		OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
		true,
		true,
		"gpt-5.1",
		"gpt-5.1",
		time.Now(),
		1,
		"",
	)
	require.Error(t, err)
	require.ErrorContains(t, err, "panic recovered")
}
