package service

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", msgType, message, nil)
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

type openAIWSPassthroughProbeStateStore struct {
	mu    sync.Mutex
	calls []string
}

func newOpenAIWSPassthroughProbeStateStore() *openAIWSPassthroughProbeStateStore {
	return &openAIWSPassthroughProbeStateStore{
		calls: make([]string, 0, 4),
	}
}

func (s *openAIWSPassthroughProbeStateStore) record(method string) {
	s.mu.Lock()
	s.calls = append(s.calls, method)
	s.mu.Unlock()
}

func (s *openAIWSPassthroughProbeStateStore) unexpectedErr(method string) error {
	s.record(method)
	return errors.New("passthrough must not call OpenAIWSStateStore." + method)
}

func (s *openAIWSPassthroughProbeStateStore) Calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.calls))
	copy(out, s.calls)
	return out
}

func (s *openAIWSPassthroughProbeStateStore) BindResponseAccount(context.Context, int64, string, int64, time.Duration) error {
	return s.unexpectedErr("BindResponseAccount")
}

func (s *openAIWSPassthroughProbeStateStore) GetResponseAccount(context.Context, int64, string) (int64, error) {
	return 0, s.unexpectedErr("GetResponseAccount")
}

func (s *openAIWSPassthroughProbeStateStore) DeleteResponseAccount(context.Context, int64, string) error {
	return s.unexpectedErr("DeleteResponseAccount")
}

func (s *openAIWSPassthroughProbeStateStore) BindResponseConn(string, string, time.Duration) {
	s.record("BindResponseConn")
}

func (s *openAIWSPassthroughProbeStateStore) GetResponseConn(string) (string, bool) {
	s.record("GetResponseConn")
	return "", false
}

func (s *openAIWSPassthroughProbeStateStore) DeleteResponseConn(string) {
	s.record("DeleteResponseConn")
}

func (s *openAIWSPassthroughProbeStateStore) BindResponsePendingToolCalls(int64, string, []string, time.Duration) {
	s.record("BindResponsePendingToolCalls")
}

func (s *openAIWSPassthroughProbeStateStore) GetResponsePendingToolCalls(int64, string) ([]string, bool) {
	s.record("GetResponsePendingToolCalls")
	return nil, false
}

func (s *openAIWSPassthroughProbeStateStore) DeleteResponsePendingToolCalls(int64, string) {
	s.record("DeleteResponsePendingToolCalls")
}

func (s *openAIWSPassthroughProbeStateStore) BindSessionTurnState(int64, string, string, time.Duration) {
	s.record("BindSessionTurnState")
}

func (s *openAIWSPassthroughProbeStateStore) GetSessionTurnState(int64, string) (string, bool) {
	s.record("GetSessionTurnState")
	return "", false
}

func (s *openAIWSPassthroughProbeStateStore) DeleteSessionTurnState(int64, string) {
	s.record("DeleteSessionTurnState")
}

func (s *openAIWSPassthroughProbeStateStore) BindSessionLastResponseID(int64, string, string, time.Duration) {
	s.record("BindSessionLastResponseID")
}

func (s *openAIWSPassthroughProbeStateStore) GetSessionLastResponseID(int64, string) (string, bool) {
	s.record("GetSessionLastResponseID")
	return "", false
}

func (s *openAIWSPassthroughProbeStateStore) DeleteSessionLastResponseID(int64, string) {
	s.record("DeleteSessionLastResponseID")
}

func (s *openAIWSPassthroughProbeStateStore) BindSessionConn(int64, string, string, time.Duration) {
	s.record("BindSessionConn")
}

func (s *openAIWSPassthroughProbeStateStore) GetSessionConn(int64, string) (string, bool) {
	s.record("GetSessionConn")
	return "", false
}

func (s *openAIWSPassthroughProbeStateStore) DeleteSessionConn(int64, string) {
	s.record("DeleteSessionConn")
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
	require.Equal(t, "websocket mode requires mode_router_v2 with ctx_pool/passthrough", closeErr.Reason())
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_CtxPoolRejectsMessageIDPreviousResponseID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := buildIngressPolicyTestConfig()
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	svc := buildIngressPolicyTestService(cfg)
	account := buildIngressPolicyTestAccount(map[string]any{
		"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModeCtxPool,
	})

	serverErr := runIngressProxyWithFirstPayload(
		t,
		svc,
		account,
		`{"type":"response.create","model":"gpt-5.1","stream":false,"previous_response_id":"msg_abc123"}`,
	)
	var closeErr *OpenAIWSClientCloseError
	require.ErrorAs(t, serverErr, &closeErr)
	require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
	require.Contains(t, closeErr.Reason(), "previous_response_id must be a response.id")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_PassthroughDoesNotRejectMessageIDPreviousResponseID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := buildIngressPolicyTestConfig()
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	svc := buildIngressPolicyTestService(cfg)
	dialer := &openAIWSAlwaysFailDialer{}
	svc.openaiWSPassthroughDialer = dialer
	account := buildIngressPolicyTestAccount(map[string]any{
		"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough,
	})

	serverErr := runIngressProxyWithFirstPayload(
		t,
		svc,
		account,
		`{"type":"response.create","model":"gpt-5.1","stream":false,"previous_response_id":"msg_abc123"}`,
	)
	require.Error(t, serverErr)
	require.Contains(t, serverErr.Error(), "openai ws passthrough dial")
	require.NotContains(t, serverErr.Error(), "previous_response_id must be a response.id")
	require.Equal(t, 1, dialer.DialCount())
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_PassthroughEmptyModelFailsBeforeDial(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := buildIngressPolicyTestConfig()
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	svc := buildIngressPolicyTestService(cfg)
	dialer := &openAIWSAlwaysFailDialer{}
	svc.openaiWSPassthroughDialer = dialer
	account := buildIngressPolicyTestAccount(map[string]any{
		"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough,
	})

	serverErr := runIngressProxyWithFirstPayload(
		t,
		svc,
		account,
		`{"type":"response.create","stream":false,"input":[]}`,
	)
	var closeErr *OpenAIWSClientCloseError
	require.ErrorAs(t, serverErr, &closeErr)
	require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
	require.Contains(t, closeErr.Reason(), "model is required")
	require.Equal(t, 0, dialer.DialCount())
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_PassthroughFunctionCallOutputNoRecoveryReject(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := buildIngressPolicyTestConfig()
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	svc := buildIngressPolicyTestService(cfg)
	dialer := &openAIWSAlwaysFailDialer{}
	svc.openaiWSPassthroughDialer = dialer
	account := buildIngressPolicyTestAccount(map[string]any{
		"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough,
	})

	serverErr := runIngressProxyWithFirstPayload(
		t,
		svc,
		account,
		`{"type":"response.create","model":"gpt-5.1","stream":false,"input":[{"type":"function_call_output","call_id":"call_abc","output":"ok"}]}`,
	)
	require.Error(t, serverErr)
	require.Contains(t, serverErr.Error(), "openai ws passthrough dial")
	require.NotContains(t, serverErr.Error(), "tool_output_not_found")
	require.NotContains(t, serverErr.Error(), "previous_response_not_found")
	require.Equal(t, 1, dialer.DialCount())
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_PassthroughDoesNotTouchStateStore(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := buildIngressPolicyTestConfig()
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	svc := buildIngressPolicyTestService(cfg)
	dialer := &openAIWSAlwaysFailDialer{}
	svc.openaiWSPassthroughDialer = dialer
	storeProbe := newOpenAIWSPassthroughProbeStateStore()
	svc.openaiWSStateStore = storeProbe
	account := buildIngressPolicyTestAccount(map[string]any{
		"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough,
	})

	serverErr := runIngressProxyWithFirstPayload(
		t,
		svc,
		account,
		`{"type":"response.create","model":"gpt-5.1","stream":false}`,
	)
	require.Error(t, serverErr)
	require.Contains(t, serverErr.Error(), "openai ws passthrough dial")
	require.Equal(t, 1, dialer.DialCount())
	require.Empty(t, storeProbe.Calls(), "passthrough 路径不应访问 StateStore")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_PassthroughDial429TriggersRateLimitSideEffect(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := buildIngressPolicyTestConfig()
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	svc := buildIngressPolicyTestService(cfg)
	repo := &wsFallbackSideEffectRepo{}
	svc.rateLimitService = NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc.openaiWSPassthroughDialer = &passthroughDialerStub{
		err:        errors.New("rate limited"),
		statusCode: http.StatusTooManyRequests,
		headers: http.Header{
			"X-Codex-Primary-Reset-After-Seconds": []string{"45"},
		},
	}
	account := buildIngressPolicyTestAccount(map[string]any{
		"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough,
	})

	err := runIngressProxyWithFirstPayload(t, svc, account, `{"type":"response.create","model":"gpt-5.3-codex","input":[]}`)
	require.Error(t, err)
	var closeErr *OpenAIWSClientCloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, coderws.StatusTryAgainLater, closeErr.StatusCode())
	require.Equal(t, 1, repo.setRateLimitedCalls)
	require.Equal(t, account.ID, repo.setRateLimitedID)
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_PassthroughDial503TriggersCustomErrorSideEffect(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := buildIngressPolicyTestConfig()
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	svc := buildIngressPolicyTestService(cfg)
	repo := &wsFallbackSideEffectRepo{}
	svc.rateLimitService = NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc.openaiWSPassthroughDialer = &passthroughDialerStub{
		err:        errors.New("service unavailable"),
		statusCode: http.StatusServiceUnavailable,
	}
	account := buildIngressPolicyTestAccount(map[string]any{
		"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough,
	})
	account.Credentials = map[string]any{
		"api_key":                    "sk-test",
		"custom_error_codes_enabled": true,
		"custom_error_codes":         []any{float64(http.StatusServiceUnavailable)},
	}

	err := runIngressProxyWithFirstPayload(t, svc, account, `{"type":"response.create","model":"gpt-5.3-codex","input":[]}`)
	require.Error(t, err)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Equal(t, account.ID, repo.setErrorID)
	require.Contains(t, repo.setErrorMsg, "Custom error code 503")
}
