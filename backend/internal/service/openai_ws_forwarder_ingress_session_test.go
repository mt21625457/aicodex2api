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
