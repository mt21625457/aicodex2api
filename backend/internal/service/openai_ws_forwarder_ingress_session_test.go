package service

import (
	"context"
	"errors"
	"io"
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
	"github.com/tidwall/gjson"
)

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_KeepLeaseAcrossTurns(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_ingress_turn_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_ingress_turn_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(captureDialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          114,
		Name:        "openai-ingress-session-lease",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

	serverErrCh := make(chan error, 1)
	turnWSModeCh := make(chan bool, 2)
	hooks := &OpenAIWSIngressHooks{
		AfterTurn: func(_ int, result *OpenAIForwardResult, turnErr error) {
			if turnErr == nil && result != nil {
				turnWSModeCh <- result.OpenAIWSMode
			}
		},
	}
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
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, hooks)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false}`)
	firstTurnEvent := readMessage()
	require.Equal(t, "response.completed", gjson.GetBytes(firstTurnEvent, "type").String())
	require.Equal(t, "resp_ingress_turn_1", gjson.GetBytes(firstTurnEvent, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"previous_response_id":"resp_ingress_turn_1"}`)
	secondTurnEvent := readMessage()
	require.Equal(t, "response.completed", gjson.GetBytes(secondTurnEvent, "type").String())
	require.Equal(t, "resp_ingress_turn_2", gjson.GetBytes(secondTurnEvent, "response.id").String())
	require.True(t, <-turnWSModeCh, "首轮 turn 应标记为 WS 模式")
	require.True(t, <-turnWSModeCh, "第二轮 turn 应标记为 WS 模式")

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))

	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	metrics := svc.SnapshotOpenAIWSPoolMetrics()
	require.Equal(t, int64(1), metrics.AcquireTotal, "同一 ingress 会话多 turn 应只获取一次上游 lease")
	require.Equal(t, 1, captureDialer.DialCount(), "同一 ingress 会话应保持同一上游连接")
	require.Len(t, captureConn.writes, 2, "应向同一上游连接发送两轮 response.create")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_DedicatedModeDoesNotReuseConnAcrossSessions(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModeShared
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	upstreamConn1 := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_dedicated_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	upstreamConn2 := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_dedicated_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{upstreamConn1, upstreamConn2},
	}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          441,
		Name:        "openai-ingress-dedicated",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModeDedicated,
		},
	}

	serverErrCh := make(chan error, 2)
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
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	runSingleTurnSession := func(expectedResponseID string) {
		dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
		clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
		cancelDial()
		require.NoError(t, err)
		defer func() {
			_ = clientConn.CloseNow()
		}()

		writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
		err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false}`))
		cancelWrite()
		require.NoError(t, err)

		readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
		msgType, event, readErr := clientConn.Read(readCtx)
		cancelRead()
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		require.Equal(t, expectedResponseID, gjson.GetBytes(event, "response.id").String())

		require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))

		select {
		case serverErr := <-serverErrCh:
			require.NoError(t, serverErr)
		case <-time.After(5 * time.Second):
			t.Fatal("等待 ingress websocket 结束超时")
		}
	}

	runSingleTurnSession("resp_dedicated_1")
	runSingleTurnSession("resp_dedicated_2")

	require.Equal(t, 2, dialer.DialCount(), "dedicated 模式下跨客户端会话必须新建上游连接，不允许复用")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_CtxPoolModeBindsPerCodexSession(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 60

	upstreamConn1 := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_ctx_pool_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	upstreamConn2 := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_ctx_pool_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{upstreamConn1, upstreamConn2},
	}
	ctxPool := newOpenAIWSIngressContextPool(cfg)
	ctxPool.setClientDialerForTest(dialer)
	defer ctxPool.Close()

	svc := &OpenAIGatewayService{
		cfg:                    cfg,
		httpUpstream:           &httpUpstreamRecorder{},
		cache:                  &stubGatewayCache{},
		openaiWSResolver:       NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:          NewCodexToolCorrector(),
		openaiWSIngressCtxPool: ctxPool,
	}

	account := &Account{
		ID:          551,
		Name:        "openai-ingress-ctx-pool",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 20,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModeCtxPool,
		},
	}

	serverErrCh := make(chan error, 2)
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
		req.Header.Set("User-Agent", codexCLIUserAgent)
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	runSingleTurnSession := func(expectedResponseID string) {
		dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
		clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
		cancelDial()
		require.NoError(t, err)
		defer func() {
			_ = clientConn.CloseNow()
		}()

		writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
		err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false,"prompt_cache_key":"ctx_pool_session_1"}`))
		cancelWrite()
		require.NoError(t, err)

		readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
		msgType, event, readErr := clientConn.Read(readCtx)
		cancelRead()
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		require.Equal(t, expectedResponseID, gjson.GetBytes(event, "response.id").String())

		require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))

		select {
		case serverErr := <-serverErrCh:
			require.NoError(t, serverErr)
		case <-time.After(5 * time.Second):
			t.Fatal("等待 ingress websocket 结束超时")
		}
	}

	runSingleTurnSession("resp_ctx_pool_1")
	runSingleTurnSession("resp_ctx_pool_2")

	require.Equal(t, 2, dialer.DialCount(), "ctx_pool 模式下每个客户端 ws 会话都应新建上游连接")
	upstreamConn1.mu.Lock()
	writes1 := len(upstreamConn1.writes)
	closed1 := upstreamConn1.closed
	upstreamConn1.mu.Unlock()
	upstreamConn2.mu.Lock()
	writes2 := len(upstreamConn2.writes)
	closed2 := upstreamConn2.closed
	upstreamConn2.mu.Unlock()
	require.Equal(t, 1, writes1, "首个客户端会话应仅使用首条上游连接发送一次请求")
	require.Equal(t, 1, writes2, "第二个客户端会话应仅使用第二条上游连接发送一次请求")
	require.True(t, closed1, "首个客户端会话结束后应关闭对应上游连接")
	require.True(t, closed2, "第二个客户端会话结束后应关闭对应上游连接")
	require.Equal(t, int64(0), svc.SnapshotOpenAIWSPoolMetrics().AcquireTotal, "ctx_pool 模式不应走普通 ws 连接池")
	stateStore := svc.getOpenAIWSStateStore()
	_, hasConn := stateStore.GetResponseConn("resp_ctx_pool_1")
	require.False(t, hasConn, "ctx_pool 响应不应污染普通池 response->conn 粘连")
	_, hasConn = stateStore.GetResponseConn("resp_ctx_pool_2")
	require.False(t, hasConn, "ctx_pool 响应不应污染普通池 response->conn 粘连")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_CtxPoolModeDoesNotRequireCodexUA(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	upstreamConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_ctx_pool_non_codex_ua","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{upstreamConn},
	}
	ctxPool := newOpenAIWSIngressContextPool(cfg)
	ctxPool.setClientDialerForTest(dialer)
	defer ctxPool.Close()

	svc := &OpenAIGatewayService{
		cfg:                    cfg,
		httpUpstream:           &httpUpstreamRecorder{},
		cache:                  &stubGatewayCache{},
		openaiWSResolver:       NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:          NewCodexToolCorrector(),
		openaiWSIngressCtxPool: ctxPool,
	}

	account := &Account{
		ID:          552,
		Name:        "openai-ingress-ctx-pool-no-codex-ua",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 20,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModeCtxPool,
		},
	}

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
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
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
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	msgType, event, readErr := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, readErr)
	require.Equal(t, coderws.MessageText, msgType)
	require.Equal(t, "resp_ctx_pool_non_codex_ua", gjson.GetBytes(event, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Equal(t, 1, dialer.DialCount(), "ctx_pool 模式应走上下文池拨号")
	require.Equal(t, int64(0), svc.SnapshotOpenAIWSPoolMetrics().AcquireTotal, "ctx_pool 模式不应走普通 ws 连接池")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_CtxPoolSessionStateIsScopedByAPIKey(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 60

	firstConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_ctx_scope_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	secondConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_ctx_scope_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{firstConn, secondConn},
	}
	ctxPool := newOpenAIWSIngressContextPool(cfg)
	ctxPool.setClientDialerForTest(dialer)
	defer ctxPool.Close()

	svc := &OpenAIGatewayService{
		cfg:                    cfg,
		httpUpstream:           &httpUpstreamRecorder{},
		cache:                  &stubGatewayCache{},
		openaiWSResolver:       NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:          NewCodexToolCorrector(),
		openaiWSIngressCtxPool: ctxPool,
	}

	account := &Account{
		ID:          554,
		Name:        "openai-ingress-ctx-pool-scope",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 20,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModeCtxPool,
		},
	}

	groupID := int64(2)
	connIdx := 0
	var connIdxMu sync.Mutex
	serverErrCh := make(chan error, 2)
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

		connIdxMu.Lock()
		connIdx++
		currentIdx := connIdx
		connIdxMu.Unlock()

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		req := r.Clone(r.Context())
		req.Header = req.Header.Clone()
		req.Header.Set("User-Agent", "unit-test-agent/1.0")
		ginCtx.Request = req
		if currentIdx == 1 {
			ginCtx.Set("api_key", &APIKey{ID: 6001, UserID: 101, GroupID: &groupID})
		} else {
			ginCtx.Set("api_key", &APIKey{ID: 6002, UserID: 202, GroupID: &groupID})
		}

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	runSingleTurnSession := func(payload string, expectedResponseID string) {
		dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
		clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
		cancelDial()
		require.NoError(t, err)
		defer func() {
			_ = clientConn.CloseNow()
		}()

		writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
		err = clientConn.Write(writeCtx, coderws.MessageText, []byte(payload))
		cancelWrite()
		require.NoError(t, err)

		readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
		msgType, event, readErr := clientConn.Read(readCtx)
		cancelRead()
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		require.Equal(t, expectedResponseID, gjson.GetBytes(event, "response.id").String())

		require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
		select {
		case serverErr := <-serverErrCh:
			require.NoError(t, serverErr)
		case <-time.After(5 * time.Second):
			t.Fatal("等待 ctx_pool scope websocket 会话结束超时")
		}
	}

	runSingleTurnSession(
		`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"prompt_cache_key":"ctx_pool_scope_shared","input":[{"type":"input_text","text":"hello"}]}`,
		"resp_ctx_scope_1",
	)
	runSingleTurnSession(
		`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"prompt_cache_key":"ctx_pool_scope_shared","input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`,
		"resp_ctx_scope_2",
	)

	require.Equal(t, 2, dialer.DialCount())
	secondConn.mu.Lock()
	secondWrites := append([]map[string]any(nil), secondConn.writes...)
	secondConn.mu.Unlock()
	require.Len(t, secondWrites, 1)
	secondWrite := requestToJSONString(secondWrites[0])
	require.False(t, gjson.Get(secondWrite, "previous_response_id").Exists(), "不同 api_key 的 ctx_pool 会话不应继承他人 last_response_id")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_CtxPoolStrictFunctionCallOutputAcquireBusyRetriesWithoutStrictAffinity(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModeShared
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 60

	firstConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_ctx_busy_seed","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	secondConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_ctx_busy_recover","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{firstConn, secondConn},
	}
	ctxPool := newOpenAIWSIngressContextPool(cfg)
	ctxPool.setClientDialerForTest(dialer)
	defer ctxPool.Close()

	svc := &OpenAIGatewayService{
		cfg:                    cfg,
		httpUpstream:           &httpUpstreamRecorder{},
		cache:                  &stubGatewayCache{},
		openaiWSResolver:       NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:          NewCodexToolCorrector(),
		openaiWSIngressCtxPool: ctxPool,
	}

	account := &Account{
		ID:          555,
		Name:        "openai-ingress-ctx-pool-busy-recover",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModeCtxPool,
		},
	}

	groupID := int64(2)
	serverErrCh := make(chan error, 2)
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
		ginCtx.Set("api_key", &APIKey{ID: 6003, UserID: 303, GroupID: &groupID})

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	runSingleTurnSession := func(payload string, expectedResponseID string) {
		dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
		clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
		cancelDial()
		require.NoError(t, err)
		defer func() {
			_ = clientConn.CloseNow()
		}()

		writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
		err = clientConn.Write(writeCtx, coderws.MessageText, []byte(payload))
		cancelWrite()
		require.NoError(t, err)

		readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
		msgType, event, readErr := clientConn.Read(readCtx)
		cancelRead()
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		require.Equal(t, expectedResponseID, gjson.GetBytes(event, "response.id").String())

		require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
		select {
		case serverErr := <-serverErrCh:
			require.NoError(t, serverErr)
		case <-time.After(5 * time.Second):
			t.Fatal("等待 ctx_pool busy recover websocket 会话结束超时")
		}
	}

	runSingleTurnSession(
		`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"input_text","text":"hello"}]}`,
		"resp_ctx_busy_seed",
	)
	runSingleTurnSession(
		`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"prompt_cache_key":"ctx_busy_real_hash","previous_response_id":"resp_ctx_busy_seed","input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`,
		"resp_ctx_busy_recover",
	)

	require.Equal(t, 2, dialer.DialCount(), "ctx_pool strict 首次 acquire busy 后应降级重试并成功建连")
	secondConn.mu.Lock()
	secondWrites := append([]map[string]any(nil), secondConn.writes...)
	secondConn.mu.Unlock()
	require.Len(t, secondWrites, 1)
	require.Equal(t, "resp_ctx_busy_seed", gjson.Get(requestToJSONString(secondWrites[0]), "previous_response_id").String(), "busy 降级重试不应丢失 function_call_output 的 previous_response_id")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_ModeOffReturnsPolicyViolation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModeShared

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     newOpenAIWSConnPool(cfg),
	}

	account := &Account{
		ID:          442,
		Name:        "openai-ingress-off",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModeOff,
		},
	}

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
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
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
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false}`))
	cancelWrite()
	require.NoError(t, err)

	select {
	case serverErr := <-serverErrCh:
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, serverErr, &closeErr)
		require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
		require.Equal(t, "websocket mode is disabled for this account", closeErr.Reason())
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreDisabledPrevResponseStrictDropToFullCreate(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_preflight_rewrite_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_preflight_rewrite_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(captureDialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          140,
		Name:        "openai-ingress-prev-preflight-rewrite",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

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
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"input_text","text":"hello"}]}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_preflight_rewrite_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"previous_response_id":"resp_stale_external","input":[{"type":"input_text","text":"world"}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_preflight_rewrite_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Equal(t, 1, captureDialer.DialCount(), "严格增量不成立时应在同一连接内降级为 full create")
	require.Len(t, captureConn.writes, 2)
	secondWrite := requestToJSONString(captureConn.writes[1])
	require.False(t, gjson.Get(secondWrite, "previous_response_id").Exists(), "严格增量不成立时应移除 previous_response_id，改为 full create")
	require.Equal(t, 2, len(gjson.Get(secondWrite, "input").Array()), "严格降级为 full create 时应重放完整 input 上下文")
	require.Equal(t, "hello", gjson.Get(secondWrite, "input.0.text").String())
	require.Equal(t, "world", gjson.Get(secondWrite, "input.1.text").String())
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_FirstTurnPreferredConnUnavailableRecoversByDropPreviousResponseID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_recovered_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(captureDialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          240,
		Name:        "openai-ingress-first-turn-preferred-recovery",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

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
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
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
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"previous_response_id":"resp_missing_conn","input":[{"type":"input_text","text":"hello"}]}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	msgType, event, readErr := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, readErr)
	require.Equal(t, coderws.MessageText, msgType)
	require.Equal(t, "resp_recovered_1", gjson.GetBytes(event, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待首轮 preferred recovery websocket 结束超时")
	}

	require.Equal(t, 1, captureDialer.DialCount())
	require.Len(t, captureConn.writes, 1)
	firstWrite := requestToJSONString(captureConn.writes[0])
	require.False(t, gjson.Get(firstWrite, "previous_response_id").Exists(), "preferred_conn 缺失时应自动降级移除 previous_response_id")
	require.Equal(t, "hello", gjson.Get(firstWrite, "input.0.text").String())
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreDisabledPrevResponseStrictDropBeforePreflightPingFailReconnects(t *testing.T) {
	gin.SetMode(gin.TestMode)
	prevPreflightPingIdle := openAIWSIngressPreflightPingIdle
	openAIWSIngressPreflightPingIdle = 0
	defer func() {
		openAIWSIngressPreflightPingIdle = prevPreflightPingIdle
	}()

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 2
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 2
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	firstConn := &openAIWSPreflightFailConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_ping_drop_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	secondConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_ping_drop_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{firstConn, secondConn},
	}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          142,
		Name:        "openai-ingress-prev-strict-drop-before-ping",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

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
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"input_text","text":"hello"}]}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_turn_ping_drop_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"previous_response_id":"resp_stale_external","input":[{"type":"input_text","text":"world"}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_turn_ping_drop_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 严格降级后预检换连超时")
	}

	require.Equal(t, 2, dialer.DialCount(), "严格降级为 full create 后，预检 ping 失败应允许换连")
	require.Equal(t, 1, firstConn.WriteCount(), "首连接在预检失败后不应继续发送第二轮")
	require.GreaterOrEqual(t, firstConn.PingCount(), 1, "第二轮前应执行 preflight ping")
	secondConn.mu.Lock()
	secondWrites := append([]map[string]any(nil), secondConn.writes...)
	secondConn.mu.Unlock()
	require.Len(t, secondWrites, 1)
	secondWrite := requestToJSONString(secondWrites[0])
	require.False(t, gjson.Get(secondWrite, "previous_response_id").Exists(), "严格降级后重试应移除 previous_response_id")
	require.Equal(t, 2, len(gjson.Get(secondWrite, "input").Array()))
	require.Equal(t, "hello", gjson.Get(secondWrite, "input.0.text").String())
	require.Equal(t, "world", gjson.Get(secondWrite, "input.1.text").String())
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreEnabledSkipsStrictPrevResponseEval(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_store_enabled_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_store_enabled_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(captureDialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          143,
		Name:        "openai-ingress-store-enabled-skip-strict",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

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
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":true}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_store_enabled_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":true,"previous_response_id":"resp_stale_external"}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_store_enabled_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 store=true 场景 websocket 结束超时")
	}

	require.Equal(t, 1, captureDialer.DialCount())
	require.Len(t, captureConn.writes, 2)
	require.Equal(t, "resp_stale_external", gjson.Get(requestToJSONString(captureConn.writes[1]), "previous_response_id").String(), "store=true 场景不应触发 store-disabled strict 规则")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreDisabledFunctionCallOutputExplicitPrevResponseAlignsToLastTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_preflight_skip_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_preflight_skip_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(captureDialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          141,
		Name:        "openai-ingress-prev-preflight-skip-fco",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

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
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_preflight_skip_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"previous_response_id":"resp_stale_external","input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_preflight_skip_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Equal(t, 1, captureDialer.DialCount())
	require.Len(t, captureConn.writes, 2)
	require.Equal(t, "resp_preflight_skip_1", gjson.Get(requestToJSONString(captureConn.writes[1]), "previous_response_id").String(), "function_call_output 场景显式 stale previous_response_id 应对齐到上一轮 response.id")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreDisabledFunctionCallOutputAutoAttachPreviousResponseID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_auto_prev_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_auto_prev_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(captureDialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          143,
		Name:        "openai-ingress-fco-auto-prev",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

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
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"input_text","text":"hello"}]}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_auto_prev_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"function_call_output","call_id":"call_auto_1","output":"ok"}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_auto_prev_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Equal(t, 1, captureDialer.DialCount())
	require.Len(t, captureConn.writes, 2)
	require.Equal(t, "resp_auto_prev_1", gjson.Get(requestToJSONString(captureConn.writes[1]), "previous_response_id").String(), "function_call_output 缺失 previous_response_id 时应回填上一轮响应 ID")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreDisabledFunctionCallOutputSkipsAutoAttachWhenLastResponseIDMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_auto_prev_skip_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(captureDialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          144,
		Name:        "openai-ingress-fco-auto-prev-skip",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

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
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"input_text","text":"hello"}]}`)
	firstTurn := readMessage()
	require.Equal(t, "response.completed", gjson.GetBytes(firstTurn, "type").String())
	require.Empty(t, gjson.GetBytes(firstTurn, "response.id").String(), "首轮响应不返回 response.id，模拟无法推导续链锚点")

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"function_call_output","call_id":"call_auto_skip_1","output":"ok"}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_auto_prev_skip_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Equal(t, 1, captureDialer.DialCount())
	require.Len(t, captureConn.writes, 2)
	require.False(t, gjson.Get(requestToJSONString(captureConn.writes[1]), "previous_response_id").Exists(), "上一轮缺失 response.id 时不应自动补齐 previous_response_id")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_PreflightPingFailReconnectsBeforeTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	prevPreflightPingIdle := openAIWSIngressPreflightPingIdle
	openAIWSIngressPreflightPingIdle = 0
	defer func() {
		openAIWSIngressPreflightPingIdle = prevPreflightPingIdle
	}()

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	firstConn := &openAIWSPreflightFailConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_ping_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	secondConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_ping_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{firstConn, secondConn},
	}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          116,
		Name:        "openai-ingress-preflight-ping",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

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
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_turn_ping_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"previous_response_id":"resp_turn_ping_1"}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_turn_ping_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}
	require.Equal(t, 2, dialer.DialCount(), "第二轮 turn 前 ping 失败应触发换连")
	require.Equal(t, 1, firstConn.WriteCount(), "preflight ping 失败后不应继续向旧连接发送第二轮 turn")
	require.GreaterOrEqual(t, firstConn.PingCount(), 1, "第二轮前应对旧连接执行 preflight ping")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreDisabledStrictAffinityPreflightPingFailAutoRecoveryReconnects(t *testing.T) {
	gin.SetMode(gin.TestMode)
	prevPreflightPingIdle := openAIWSIngressPreflightPingIdle
	openAIWSIngressPreflightPingIdle = 0
	defer func() {
		openAIWSIngressPreflightPingIdle = prevPreflightPingIdle
	}()

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 2
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 2
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	firstConn := &openAIWSPreflightFailConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_ping_strict_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	secondConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_ping_strict_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{firstConn, secondConn},
	}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          121,
		Name:        "openai-ingress-preflight-ping-strict-affinity",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

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
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"input_text","text":"hello"}]}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_turn_ping_strict_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"previous_response_id":"resp_turn_ping_strict_1","input":[{"type":"input_text","text":"world"}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_turn_ping_strict_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 严格亲和自动恢复后结束超时")
	}

	require.Equal(t, 2, dialer.DialCount(), "严格亲和 preflight ping 失败后应自动降级并换连重放")
	require.Equal(t, 1, firstConn.WriteCount(), "preflight ping 失败后不应继续在旧连接写第二轮")
	require.GreaterOrEqual(t, firstConn.PingCount(), 1, "第二轮前应执行 preflight ping")
	secondConn.mu.Lock()
	secondWrites := append([]map[string]any(nil), secondConn.writes...)
	secondConn.mu.Unlock()
	require.Len(t, secondWrites, 1)
	secondWrite := requestToJSONString(secondWrites[0])
	require.False(t, gjson.Get(secondWrite, "previous_response_id").Exists(), "自动恢复重放应移除 previous_response_id")
	require.Equal(t, 2, len(gjson.Get(secondWrite, "input").Array()), "自动恢复重放应使用完整 input 上下文")
	require.Equal(t, "hello", gjson.Get(secondWrite, "input.0.text").String())
	require.Equal(t, "world", gjson.Get(secondWrite, "input.1.text").String())
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_CtxPoolStoreDisabledFunctionCallOutputStrictAffinityPreflightPingFailReconnects(t *testing.T) {
	gin.SetMode(gin.TestMode)
	prevPreflightPingIdle := openAIWSIngressPreflightPingIdle
	openAIWSIngressPreflightPingIdle = 0
	defer func() {
		openAIWSIngressPreflightPingIdle = prevPreflightPingIdle
	}()

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModeShared
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 2
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 2
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.StickySessionTTLSeconds = 60

	firstConn := &openAIWSPreflightFailConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_ctx_ping_fco_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	secondConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_ctx_ping_fco_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{firstConn, secondConn},
	}
	ctxPool := newOpenAIWSIngressContextPool(cfg)
	ctxPool.setClientDialerForTest(dialer)
	defer ctxPool.Close()

	svc := &OpenAIGatewayService{
		cfg:                    cfg,
		httpUpstream:           &httpUpstreamRecorder{},
		cache:                  &stubGatewayCache{},
		openaiWSResolver:       NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:          NewCodexToolCorrector(),
		openaiWSIngressCtxPool: ctxPool,
	}

	account := &Account{
		ID:          553,
		Name:        "openai-ingress-ctx-pool-preflight-ping-fco",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 20,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModeCtxPool,
		},
	}

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
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"prompt_cache_key":"ctx_pool_preflight_fco","input":[{"type":"input_text","text":"hello"}]}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_ctx_ping_fco_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"prompt_cache_key":"ctx_pool_preflight_fco","previous_response_id":"resp_ctx_ping_fco_1","input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_ctx_ping_fco_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ctx_pool preflight ping function_call_output 恢复结束超时")
	}

	require.Equal(t, 2, dialer.DialCount(), "ctx_pool 严格续链 preflight ping 失败后应换连重试")
	require.Equal(t, 1, firstConn.WriteCount(), "preflight ping 失败后旧连接不应继续发送第二轮")
	require.GreaterOrEqual(t, firstConn.PingCount(), 1, "第二轮前应执行 preflight ping")
	secondConn.mu.Lock()
	secondWrites := append([]map[string]any(nil), secondConn.writes...)
	secondConn.mu.Unlock()
	require.Len(t, secondWrites, 1)
	require.Equal(t, "resp_ctx_ping_fco_1", gjson.Get(requestToJSONString(secondWrites[0]), "previous_response_id").String(), "function_call_output 场景 preflight 恢复应保留 previous_response_id")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_WriteFailBeforeDownstreamRetriesOnce(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	firstConn := &openAIWSWriteFailAfterFirstTurnConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_write_retry_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	secondConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_write_retry_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{firstConn, secondConn},
	}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          117,
		Name:        "openai-ingress-write-retry",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}
	var hooksMu sync.Mutex
	beforeTurnCalls := make(map[int]int)
	afterTurnCalls := make(map[int]int)
	hooks := &OpenAIWSIngressHooks{
		BeforeTurn: func(turn int) error {
			hooksMu.Lock()
			beforeTurnCalls[turn]++
			hooksMu.Unlock()
			return nil
		},
		AfterTurn: func(turn int, _ *OpenAIForwardResult, _ error) {
			hooksMu.Lock()
			afterTurnCalls[turn]++
			hooksMu.Unlock()
		},
	}

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
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, hooks)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_turn_write_retry_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"previous_response_id":"resp_turn_write_retry_1"}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_turn_write_retry_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}
	require.Equal(t, 2, dialer.DialCount(), "第二轮 turn 上游写失败且未写下游时应自动重试并换连")
	hooksMu.Lock()
	beforeTurn1 := beforeTurnCalls[1]
	beforeTurn2 := beforeTurnCalls[2]
	afterTurn1 := afterTurnCalls[1]
	afterTurn2 := afterTurnCalls[2]
	hooksMu.Unlock()
	require.Equal(t, 1, beforeTurn1, "首轮 turn BeforeTurn 应执行一次")
	require.Equal(t, 1, beforeTurn2, "同一 turn 重试不应重复触发 BeforeTurn")
	require.Equal(t, 1, afterTurn1, "首轮 turn AfterTurn 应执行一次")
	require.Equal(t, 1, afterTurn2, "第二轮 turn AfterTurn 应执行一次")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_PreviousResponseNotFoundRecoversByDroppingPrevID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.IngressPreviousResponseRecoveryEnabled = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	firstConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_prev_recover_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"error","error":{"type":"invalid_request_error","code":"previous_response_not_found","message":""}}`),
		},
	}
	secondConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_prev_recover_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{firstConn, secondConn},
	}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          118,
		Name:        "openai-ingress-prev-recovery",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

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
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"previous_response_id":"resp_seed_anchor"}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_turn_prev_recover_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"previous_response_id":"resp_turn_prev_recover_1"}`)
	secondTurn := readMessage()
	require.Equal(t, "response.completed", gjson.GetBytes(secondTurn, "type").String())
	require.Equal(t, "resp_turn_prev_recover_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Equal(t, 2, dialer.DialCount(), "previous_response_not_found 恢复应触发换连重试")

	firstConn.mu.Lock()
	firstWrites := append([]map[string]any(nil), firstConn.writes...)
	firstConn.mu.Unlock()
	require.Len(t, firstWrites, 2, "首个连接应处理首轮与失败的第二轮请求")
	require.True(t, gjson.Get(requestToJSONString(firstWrites[1]), "previous_response_id").Exists(), "失败轮次首发请求应包含 previous_response_id")

	secondConn.mu.Lock()
	secondWrites := append([]map[string]any(nil), secondConn.writes...)
	secondConn.mu.Unlock()
	require.Len(t, secondWrites, 1, "恢复重试应在第二个连接发送一次请求")
	require.False(t, gjson.Get(requestToJSONString(secondWrites[0]), "previous_response_id").Exists(), "恢复重试应移除 previous_response_id")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_FunctionCallOutputToolOutputNotFoundRecoversByDroppingPrevID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.IngressPreviousResponseRecoveryEnabled = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	firstConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_tool_output_not_found_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"No tool output found for function call call_turn_tool_output_not_found_1.","param":"input"}}`),
		},
	}
	secondConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_tool_output_not_found_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{firstConn, secondConn},
	}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          159,
		Name:        "openai-ingress-fco-tool-output-not-found-recovery",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

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
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"input_text","text":"hello"}]}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_turn_tool_output_not_found_1", gjson.GetBytes(firstTurn, "response.id").String())

	// 第二轮不显式带 previous_response_id，触发 function_call_output 自动补齐。
	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"function_call_output","call_id":"call_turn_tool_output_not_found_1","output":"ok"}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_turn_tool_output_not_found_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket function_call_output tool_output_not_found 恢复结束超时")
	}

	require.Equal(t, 2, dialer.DialCount(), "function_call_output 命中 tool_output_not_found 应触发换连重试")

	firstConn.mu.Lock()
	firstWrites := append([]map[string]any(nil), firstConn.writes...)
	firstConn.mu.Unlock()
	require.Len(t, firstWrites, 2, "首连接应接收首轮与失败的 function_call_output 请求")
	require.Equal(t, "resp_turn_tool_output_not_found_1", gjson.Get(requestToJSONString(firstWrites[1]), "previous_response_id").String(), "失败轮次首发请求应自动补齐 previous_response_id")

	secondConn.mu.Lock()
	secondWrites := append([]map[string]any(nil), secondConn.writes...)
	secondConn.mu.Unlock()
	require.Len(t, secondWrites, 1, "恢复重试应在第二个连接发送一次请求")
	require.False(t, gjson.Get(requestToJSONString(secondWrites[0]), "previous_response_id").Exists(), "tool_output_not_found 恢复重试应移除 previous_response_id")
	require.Equal(t, "function_call_output", gjson.Get(requestToJSONString(secondWrites[0]), "input.1.type").String(), "恢复重试应保留 function_call_output 输入用于重放")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_FunctionCallOutputPreviousResponseNotFoundRecoversByRetryingWithPrevID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.IngressPreviousResponseRecoveryEnabled = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	firstConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_fco_prev_recover_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"error","error":{"type":"invalid_request_error","code":"previous_response_not_found","message":"missing fco anchor"}}`),
		},
	}
	secondConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_fco_prev_recover_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{firstConn, secondConn},
	}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          152,
		Name:        "openai-ingress-fco-prev-recovery",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

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
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"input_text","text":"hello"}]}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_turn_fco_prev_recover_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"previous_response_id":"resp_turn_fco_prev_recover_1","input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_turn_fco_prev_recover_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket function_call_output previous_response_not_found 恢复结束超时")
	}

	require.Equal(t, 2, dialer.DialCount(), "function_call_output 命中 previous_response_not_found 应触发换连重试")

	firstConn.mu.Lock()
	firstWrites := append([]map[string]any(nil), firstConn.writes...)
	firstConn.mu.Unlock()
	require.Len(t, firstWrites, 2, "首连接应接收首轮与失败的 function_call_output 请求")

	secondConn.mu.Lock()
	secondWrites := append([]map[string]any(nil), secondConn.writes...)
	secondConn.mu.Unlock()
	require.Len(t, secondWrites, 1, "恢复重试应在第二个连接发送一次请求")
	require.Equal(t, "resp_turn_fco_prev_recover_1", gjson.Get(requestToJSONString(secondWrites[0]), "previous_response_id").String(), "function_call_output 恢复重试应保留 previous_response_id")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_FunctionCallOutputPreviousResponseNotFoundRecoversBySessionLastResponseFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.IngressPreviousResponseRecoveryEnabled = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	firstConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"error","error":{"type":"invalid_request_error","code":"previous_response_not_found","message":"stale anchor"}}`),
		},
	}
	secondConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_fco_prev_fallback_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{firstConn, secondConn},
	}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          153,
		Name:        "openai-ingress-fco-prev-fallback",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

	sessionID := "session_prev_fallback_1"
	sessionHash, _ := deriveOpenAISessionHashes(sessionID)
	store := svc.getOpenAIWSStateStore()
	store.BindSessionLastResponseID(0, sessionHash, "resp_persisted_anchor_1", time.Minute)

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
		req.Header.Set("session_id", sessionID)
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`)
	turn := readMessage()
	require.Equal(t, "resp_turn_fco_prev_fallback_2", gjson.GetBytes(turn, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket session last_response fallback 恢复结束超时")
	}

	require.Equal(t, 2, dialer.DialCount(), "session last_response fallback 命中 previous_response_not_found 时应触发换连重试")

	firstConn.mu.Lock()
	firstWrites := append([]map[string]any(nil), firstConn.writes...)
	firstConn.mu.Unlock()
	require.Len(t, firstWrites, 1, "首连接应收到失败前的首发请求")
	require.False(t, gjson.Get(requestToJSONString(firstWrites[0]), "previous_response_id").Exists(), "首发请求不带 previous_response_id，恢复流程应由 session fallback 回填")

	secondConn.mu.Lock()
	secondWrites := append([]map[string]any(nil), secondConn.writes...)
	secondConn.mu.Unlock()
	require.Len(t, secondWrites, 1, "恢复重试应在第二个连接发送一次请求")
	require.Equal(t, "resp_persisted_anchor_1", gjson.Get(requestToJSONString(secondWrites[0]), "previous_response_id").String(), "恢复重试应使用 session_hash 持久化的 last_response_id")

	persistedLastResponseID, ok := store.GetSessionLastResponseID(0, sessionHash)
	require.True(t, ok)
	require.Equal(t, "resp_turn_fco_prev_fallback_2", persistedLastResponseID, "成功 turn 后应刷新 session_hash 的 last_response_id")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_LateSessionHashBackfillsLastResponseFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_late_hash_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(captureDialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          154,
		Name:        "openai-ingress-late-session-hash",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

	promptCacheKey := "pk_late_hash_1"
	sessionHash, _ := deriveOpenAISessionHashes(promptCacheKey)
	store := svc.getOpenAIWSStateStore()
	store.BindSessionLastResponseID(0, sessionHash, "resp_seed_late_1", time.Minute)

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
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"input_text","text":"hello"}]}`)
	firstTurn := readMessage()
	require.Empty(t, gjson.GetBytes(firstTurn, "response.id").String(), "首轮故意无 response.id，确保只能依赖 session fallback")

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"prompt_cache_key":"pk_late_hash_1","input":[{"type":"function_call_output","call_id":"call_late","output":"ok"}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_late_hash_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket late session hash backfill 结束超时")
	}

	require.Equal(t, 1, captureDialer.DialCount())
	require.Len(t, captureConn.writes, 2)
	require.Equal(t, "resp_seed_late_1", gjson.Get(requestToJSONString(captureConn.writes[1]), "previous_response_id").String(), "第二轮补上 prompt_cache_key 后应回填 session fallback 的 last_response_id")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_ClearsSessionLastResponseAnchorOnFullCreateTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	captureConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_anchor_reset_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(captureDialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          155,
		Name:        "openai-ingress-anchor-reset",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

	sessionID := "session_anchor_reset_1"
	sessionHash, _ := deriveOpenAISessionHashes(sessionID)
	store := svc.getOpenAIWSStateStore()
	store.BindSessionLastResponseID(0, sessionHash, "resp_stale_anchor_1", time.Minute)

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
		req.Header.Set("session_id", sessionID)
		ginCtx.Request = req

		readCtx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"input_text","text":"fresh start"}]}`)
	firstTurn := readMessage()
	require.Empty(t, gjson.GetBytes(firstTurn, "response.id").String(), "首轮故意无 response.id，验证 stale anchor 是否会泄露到下一轮")

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"function_call_output","call_id":"call_reset","output":"ok"}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_anchor_reset_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket anchor reset 结束超时")
	}

	require.Equal(t, 1, captureDialer.DialCount())
	require.Len(t, captureConn.writes, 2)
	require.False(t, gjson.Get(requestToJSONString(captureConn.writes[1]), "previous_response_id").Exists(), "full create 首轮后应清空 stale session anchor，下一轮不应自动回填")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_StoreDisabledStrictAffinityPreviousResponseNotFoundLayer2Recovery(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.IngressPreviousResponseRecoveryEnabled = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	firstConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_prev_strict_recover_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"error","error":{"type":"invalid_request_error","code":"previous_response_not_found","message":"missing strict anchor"}}`),
		},
	}
	secondConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_prev_strict_recover_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{firstConn, secondConn},
	}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          122,
		Name:        "openai-ingress-prev-strict-layer2",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

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
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"prompt_cache_key":"pk_strict_layer2","input":[{"type":"input_text","text":"hello"}]}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_turn_prev_strict_recover_1", gjson.GetBytes(firstTurn, "response.id").String())

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"prompt_cache_key":"pk_strict_layer2","previous_response_id":"resp_turn_prev_strict_recover_1","input":[{"type":"input_text","text":"world"}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_turn_prev_strict_recover_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 严格亲和 Layer2 恢复结束超时")
	}

	require.Equal(t, 2, dialer.DialCount(), "严格亲和链路命中 previous_response_not_found 应触发 Layer2 恢复重试")

	firstConn.mu.Lock()
	firstWrites := append([]map[string]any(nil), firstConn.writes...)
	firstConn.mu.Unlock()
	require.Len(t, firstWrites, 2, "首连接应收到首轮请求和失败的续链请求")
	require.True(t, gjson.Get(requestToJSONString(firstWrites[1]), "previous_response_id").Exists())

	secondConn.mu.Lock()
	secondWrites := append([]map[string]any(nil), secondConn.writes...)
	secondConn.mu.Unlock()
	require.Len(t, secondWrites, 1, "Layer2 恢复应仅重放一次")
	secondWrite := requestToJSONString(secondWrites[0])
	require.False(t, gjson.Get(secondWrite, "previous_response_id").Exists(), "Layer2 恢复重放应移除 previous_response_id")
	require.True(t, gjson.Get(secondWrite, "store").Exists(), "Layer2 恢复不应改变 store 标志")
	require.False(t, gjson.Get(secondWrite, "store").Bool())
	require.Equal(t, 2, len(gjson.Get(secondWrite, "input").Array()), "Layer2 恢复应重放完整 input 上下文")
	require.Equal(t, "hello", gjson.Get(secondWrite, "input.0.text").String())
	require.Equal(t, "world", gjson.Get(secondWrite, "input.1.text").String())
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_DedicatedModePreflightPingFailRetriesWithoutStrictAffinity(t *testing.T) {
	gin.SetMode(gin.TestMode)
	prevPreflightPingIdle := openAIWSIngressPreflightPingIdle
	openAIWSIngressPreflightPingIdle = 0
	defer func() {
		openAIWSIngressPreflightPingIdle = prevPreflightPingIdle
	}()

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModeShared
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	firstConn := &openAIWSPingFailConn{}
	secondConn := &openAIWSPingFailConn{}
	thirdConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_dedicated_ping_recover_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{firstConn, secondConn, thirdConn},
	}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          123,
		Name:        "openai-ingress-dedicated-ping-recover",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModeDedicated,
		},
	}

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
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"input_text","text":"hello"}]}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_ping_fail", gjson.GetBytes(firstTurn, "response.id").String())

	// 不显式带 previous_response_id，触发 function_call_output 自动推断续链锚点。
	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"store":false,"input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_dedicated_ping_recover_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 dedicated preflight ping 恢复会话结束超时")
	}

	require.Equal(t, 3, dialer.DialCount(), "dedicated 严格亲和 preflight ping 连续失败后应降级重试并继续会话")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_PreviousResponseNotFoundRecoveryRemovesDuplicatePrevID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.IngressPreviousResponseRecoveryEnabled = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	firstConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_prev_once_1","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
			[]byte(`{"type":"error","error":{"type":"invalid_request_error","code":"previous_response_not_found","message":"first missing"}}`),
		},
	}
	secondConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_turn_prev_once_2","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{firstConn, secondConn},
	}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          120,
		Name:        "openai-ingress-prev-recovery-once",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

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
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeMessage := func(payload string) {
		writeCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		require.NoError(t, clientConn.Write(writeCtx, coderws.MessageText, []byte(payload)))
	}
	readMessage := func() []byte {
		readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		msgType, message, readErr := clientConn.Read(readCtx)
		require.NoError(t, readErr)
		require.Equal(t, coderws.MessageText, msgType)
		return message
	}

	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false}`)
	firstTurn := readMessage()
	require.Equal(t, "resp_turn_prev_once_1", gjson.GetBytes(firstTurn, "response.id").String())

	// duplicate previous_response_id: 恢复重试时应删除所有重复键，避免再次 previous_response_not_found。
	writeMessage(`{"type":"response.create","model":"gpt-5.1","stream":false,"previous_response_id":"resp_turn_prev_once_1","input":[],"previous_response_id":"resp_turn_prev_duplicate"}`)
	secondTurn := readMessage()
	require.Equal(t, "resp_turn_prev_once_2", gjson.GetBytes(secondTurn, "response.id").String())

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	require.Equal(t, 2, dialer.DialCount(), "previous_response_not_found 恢复应只重试一次")

	firstConn.mu.Lock()
	firstWrites := append([]map[string]any(nil), firstConn.writes...)
	firstConn.mu.Unlock()
	require.Len(t, firstWrites, 2)
	require.True(t, gjson.Get(requestToJSONString(firstWrites[1]), "previous_response_id").Exists())

	secondConn.mu.Lock()
	secondWrites := append([]map[string]any(nil), secondConn.writes...)
	secondConn.mu.Unlock()
	require.Len(t, secondWrites, 1)
	require.False(t, gjson.Get(requestToJSONString(secondWrites[0]), "previous_response_id").Exists(), "重复键场景恢复重试后不应保留 previous_response_id")
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_RejectsMessageIDAsPreviousResponseID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
	}

	account := &Account{
		ID:          119,
		Name:        "openai-ingress-prev-validation",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

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
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
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
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":false,"previous_response_id":"msg_123456"}`))
	cancelWrite()
	require.NoError(t, err)

	select {
	case serverErr := <-serverErrCh:
		require.Error(t, serverErr)
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, serverErr, &closeErr)
		require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
		require.Contains(t, closeErr.Reason(), "previous_response_id must be a response.id")
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_RejectsResponseAppendInWSv2(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
	}

	account := &Account{
		ID:          120,
		Name:        "openai-ingress-append-validation",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

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
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
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
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.append","input":[{"type":"input_text","text":"hello"}]}`))
	cancelWrite()
	require.NoError(t, err)

	select {
	case serverErr := <-serverErrCh:
		require.Error(t, serverErr)
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, serverErr, &closeErr)
		require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
		require.Contains(t, closeErr.Reason(), "response.append is not supported in ws v2")
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_ErrorEventTerminatesTurnWithoutReadTimeoutWait(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	errorConn := &openAIWSBlockAfterErrorConn{
		event: []byte(`{"type":"error","error":{"type":"invalid_request_error","message":"upstream websocket error"}}`),
	}
	recoverConn := &openAIWSCaptureConn{
		events: [][]byte{
			[]byte(`{"type":"response.completed","response":{"id":"resp_after_error","model":"gpt-5.1","usage":{"input_tokens":1,"output_tokens":1}}}`),
		},
	}
	dialer := &openAIWSQueueDialer{
		conns: []openAIWSClientConn{errorConn, recoverConn},
	}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(dialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          121,
		Name:        "openai-ingress-error-event-terminal",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

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
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, nil)
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
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":true,"input":[{"type":"input_text","text":"hello"}]}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	msgType, event, readErr := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, readErr)
	require.Equal(t, coderws.MessageText, msgType)
	require.Equal(t, "error", gjson.GetBytes(event, "type").String())

	start := time.Now()
	writeCtx2, cancelWrite2 := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx2, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.1","stream":true,"input":[{"type":"input_text","text":"retry"}]}`))
	cancelWrite2()
	require.NoError(t, err)

	readCtx2, cancelRead2 := context.WithTimeout(context.Background(), 3*time.Second)
	msgType2, event2, readErr2 := clientConn.Read(readCtx2)
	cancelRead2()
	require.NoError(t, readErr2)
	require.Equal(t, coderws.MessageText, msgType2)
	require.Equal(t, "resp_after_error", gjson.GetBytes(event2, "response.id").String())
	require.Less(t, time.Since(start), 1200*time.Millisecond, "error 事件后应快速结束当前 turn 并允许下一轮，不应等待 read timeout")
	require.Equal(t, 2, dialer.DialCount(), "error turn 后应重建上游连接继续后续 turn")

	require.NoError(t, clientConn.Close(coderws.StatusNormalClosure, "done"))
	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr)
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}
}

type openAIWSQueueDialer struct {
	mu        sync.Mutex
	conns     []openAIWSClientConn
	dialCount int
}

func (d *openAIWSQueueDialer) Dial(
	ctx context.Context,
	wsURL string,
	headers http.Header,
	proxyURL string,
) (openAIWSClientConn, int, http.Header, error) {
	_ = ctx
	_ = wsURL
	_ = headers
	_ = proxyURL
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dialCount++
	if len(d.conns) == 0 {
		return nil, 503, nil, errors.New("no test conn")
	}
	conn := d.conns[0]
	if len(d.conns) > 1 {
		d.conns = d.conns[1:]
	}
	return conn, 0, nil, nil
}

func (d *openAIWSQueueDialer) DialCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dialCount
}

type openAIWSBlockAfterErrorConn struct {
	mu      sync.Mutex
	closed  bool
	emitted bool
	event   []byte
}

func (c *openAIWSBlockAfterErrorConn) WriteJSON(context.Context, any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errOpenAIWSConnClosed
	}
	return nil
}

func (c *openAIWSBlockAfterErrorConn) ReadMessage(ctx context.Context) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errOpenAIWSConnClosed
	}
	if !c.emitted {
		c.emitted = true
		event := make([]byte, len(c.event))
		copy(event, c.event)
		c.mu.Unlock()
		return event, nil
	}
	c.mu.Unlock()
	<-ctx.Done()
	return nil, ctx.Err()
}

func (c *openAIWSBlockAfterErrorConn) Ping(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errOpenAIWSConnClosed
	}
	return nil
}

func (c *openAIWSBlockAfterErrorConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

type openAIWSPreflightFailConn struct {
	mu         sync.Mutex
	events     [][]byte
	pingFails  bool
	writeCount int
	pingCount  int
}

func (c *openAIWSPreflightFailConn) WriteJSON(context.Context, any) error {
	c.mu.Lock()
	c.writeCount++
	c.mu.Unlock()
	return nil
}

func (c *openAIWSPreflightFailConn) ReadMessage(context.Context) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.events) == 0 {
		return nil, io.EOF
	}
	event := c.events[0]
	c.events = c.events[1:]
	if len(c.events) == 0 {
		c.pingFails = true
	}
	return event, nil
}

func (c *openAIWSPreflightFailConn) Ping(context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pingCount++
	if c.pingFails {
		return errors.New("preflight ping failed")
	}
	return nil
}

func (c *openAIWSPreflightFailConn) Close() error {
	return nil
}

func (c *openAIWSPreflightFailConn) WriteCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeCount
}

func (c *openAIWSPreflightFailConn) PingCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pingCount
}

type openAIWSWriteFailAfterFirstTurnConn struct {
	mu          sync.Mutex
	events      [][]byte
	failOnWrite bool
}

func (c *openAIWSWriteFailAfterFirstTurnConn) WriteJSON(context.Context, any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.failOnWrite {
		return errors.New("write failed on stale conn")
	}
	return nil
}

func (c *openAIWSWriteFailAfterFirstTurnConn) ReadMessage(context.Context) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.events) == 0 {
		return nil, io.EOF
	}
	event := c.events[0]
	c.events = c.events[1:]
	if len(c.events) == 0 {
		c.failOnWrite = true
	}
	return event, nil
}

func (c *openAIWSWriteFailAfterFirstTurnConn) Ping(context.Context) error {
	return nil
}

func (c *openAIWSWriteFailAfterFirstTurnConn) Close() error {
	return nil
}

func TestOpenAIGatewayService_ProxyResponsesWebSocketFromClient_ClientDisconnectStillDrainsUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled = true
	cfg.Gateway.OpenAIWS.OAuthEnabled = true
	cfg.Gateway.OpenAIWS.APIKeyEnabled = true
	cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
	cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
	cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 3
	cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

	// 多个上游事件：前几个为非 terminal 事件，最后一个为 terminal。
	// 第一个事件延迟 250ms 让客户端 RST 有时间传播，使 writeClientMessage 可靠失败。
	captureConn := &openAIWSCaptureConn{
		readDelays: []time.Duration{250 * time.Millisecond, 0, 0},
		events: [][]byte{
			[]byte(`{"type":"response.created","response":{"id":"resp_ingress_disconnect","model":"gpt-5.1"}}`),
			[]byte(`{"type":"response.output_item.added","response":{"id":"resp_ingress_disconnect"}}`),
			[]byte(`{"type":"response.completed","response":{"id":"resp_ingress_disconnect","model":"gpt-5.1","usage":{"input_tokens":2,"output_tokens":1}}}`),
		},
	}
	captureDialer := &openAIWSCaptureDialer{conn: captureConn}
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(captureDialer)

	svc := &OpenAIGatewayService{
		cfg:              cfg,
		httpUpstream:     &httpUpstreamRecorder{},
		cache:            &stubGatewayCache{},
		openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
		toolCorrector:    NewCodexToolCorrector(),
		openaiWSPool:     pool,
	}

	account := &Account{
		ID:          115,
		Name:        "openai-ingress-client-disconnect",
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key": "sk-test",
			"model_mapping": map[string]any{
				"custom-original-model": "gpt-5.1",
			},
		},
		Extra: map[string]any{
			"responses_websockets_v2_enabled": true,
		},
	}

	serverErrCh := make(chan error, 1)
	resultCh := make(chan *OpenAIForwardResult, 1)
	hooks := &OpenAIWSIngressHooks{
		AfterTurn: func(_ int, result *OpenAIForwardResult, turnErr error) {
			if turnErr == nil && result != nil {
				resultCh <- result
			}
		},
	}
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
		msgType, firstMessage, readErr := conn.Read(readCtx)
		cancel()
		if readErr != nil {
			serverErrCh <- readErr
			return
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			serverErrCh <- errors.New("unsupported websocket client message type")
			return
		}

		serverErrCh <- svc.ProxyResponsesWebSocketFromClient(r.Context(), ginCtx, conn, account, "sk-test", firstMessage, hooks)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http"), nil)
	cancelDial()
	require.NoError(t, err)

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"custom-original-model","stream":false}`))
	cancelWrite()
	require.NoError(t, err)
	// 立即关闭客户端，模拟客户端在 relay 期间断连。
	require.NoError(t, clientConn.CloseNow(), "模拟 ingress 客户端提前断连")

	select {
	case serverErr := <-serverErrCh:
		require.NoError(t, serverErr, "客户端断连后应继续 drain 上游直到 terminal 或正常结束")
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ingress websocket 结束超时")
	}

	select {
	case result := <-resultCh:
		require.Equal(t, "resp_ingress_disconnect", result.RequestID)
		require.Equal(t, 2, result.Usage.InputTokens)
		require.Equal(t, 1, result.Usage.OutputTokens)
	case <-time.After(2 * time.Second):
		t.Fatal("未收到断连后的 turn 结果回调")
	}
}
