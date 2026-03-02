package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func runIngressProxyWithFirstPayload(
	t *testing.T,
	svc *OpenAIGatewayService,
	account *Account,
	firstPayload string,
) error {
	t.Helper()

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, &coderws.AcceptOptions{
			CompressionMode: coderws.CompressionContextTakeover,
		})
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, message, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", message, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(firstPayload))
	cancelWrite()
	require.NoError(t, err)

	select {
	case serverErr := <-serverErrCh:
		return serverErr
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
		return nil
	}
}

func buildIngressPolicyTestConfig() *config.Config {
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModeCtxPool
	return cfg
}

func buildIngressPolicyTestService(cfg *config.Config) *OpenAIGatewayService {
	return &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
	}
}

func buildIngressPolicyTestAccount(extra map[string]any) *Account {
	return &Account{
		ID:          442,
		Name:        "openai-ingress-policy",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: extra,
	}
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_ModeOffReturnsPolicyViolation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := buildIngressPolicyTestConfig()
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	svc := buildIngressPolicyTestService(cfg)
	account := buildIngressPolicyTestAccount(map[string]any{
		"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModeOff,
	})

	serverErr := runIngressProxyWithFirstPayload(t, svc, account, `{"type":"response.create","model":"gpt-5.1","stream":false}`)
	var closeErr *OpenAIWSClientCloseError
	require.ErrorAs(t, serverErr, &closeErr)
	require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
	require.Equal(t, "websocket mode is disabled for this account", closeErr.Reason())
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_ModeRouterDisabledReturnsPolicyViolation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := buildIngressPolicyTestConfig()
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = false
	svc := buildIngressPolicyTestService(cfg)
	account := buildIngressPolicyTestAccount(map[string]any{
		"responses_websockets_v2_enabled": true,
	})

	serverErr := runIngressProxyWithFirstPayload(t, svc, account, `{"type":"response.create","model":"gpt-5.1","stream":false}`)
	var closeErr *OpenAIWSClientCloseError
	require.ErrorAs(t, serverErr, &closeErr)
	require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
	require.Equal(t, "websocket mode requires mode_router_v2 with ctx_pool", closeErr.Reason())
}
