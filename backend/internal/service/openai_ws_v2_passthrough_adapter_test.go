package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	openaiwsv2 "github.com/Wei-Shaw/sub2api/internal/service/openai_ws_v2"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

var openAIWSModeDebugLogTestMu sync.Mutex

func TestOpenAIWSClientFrameConn_NilGuards(t *testing.T) {
	t.Parallel()

	var nilReceiver *openAIWSClientFrameConn
	msgType, payload, err := nilReceiver.ReadFrame(context.Background())
	require.ErrorIs(t, err, errOpenAIWSConnClosed)
	require.Equal(t, coderws.MessageText, msgType)
	require.Nil(t, payload)

	err = nilReceiver.WriteFrame(context.Background(), coderws.MessageText, []byte("x"))
	require.ErrorIs(t, err, errOpenAIWSConnClosed)
	require.NoError(t, nilReceiver.Close())

	empty := &openAIWSClientFrameConn{}
	var nilCtx context.Context
	_, _, err = empty.ReadFrame(nilCtx)
	require.ErrorIs(t, err, errOpenAIWSConnClosed)
	err = empty.WriteFrame(nilCtx, coderws.MessageText, []byte("x"))
	require.ErrorIs(t, err, errOpenAIWSConnClosed)
	require.NoError(t, empty.Close())
}

func TestOpenAIWSClientFrameConn_NilContextWithLiveConn(t *testing.T) {
	gin.SetMode(gin.TestMode)

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		require.NoError(t, err)
		defer func() {
			_ = conn.CloseNow()
		}()

		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, payload, err := conn.Read(readCtx)
		cancelRead()
		if err != nil {
			serverErrCh <- err
			return
		}
		writeCtx, cancelWrite := context.WithTimeout(r.Context(), 3*time.Second)
		err = conn.Write(writeCtx, msgType, payload)
		cancelWrite()
		serverErrCh <- err
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(
		dialCtx,
		"ws"+strings.TrimPrefix(wsServer.URL, "http"),
		nil,
	)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	frameConn := &openAIWSClientFrameConn{conn: clientConn}
	payload := []byte(`{"type":"response.create","model":"gpt-5.3-codex"}`)
	var nilCtx context.Context

	require.NoError(t, frameConn.WriteFrame(nilCtx, coderws.MessageText, payload))
	msgType, gotPayload, err := frameConn.ReadFrame(nilCtx)
	require.NoError(t, err)
	require.Equal(t, coderws.MessageText, msgType)
	require.Equal(t, payload, gotPayload)
	require.NoError(t, <-serverErrCh)
}

func TestOpenAIWSV2PassthroughHelpers(t *testing.T) {
	t.Parallel()

	require.Equal(t, "text", openaiwsv2RelayMessageTypeName(coderws.MessageText))
	require.Equal(t, "binary", openaiwsv2RelayMessageTypeName(coderws.MessageBinary))
	require.Contains(t, openaiwsv2RelayMessageTypeName(coderws.MessageType(99)), "unknown(")

	require.Equal(t, "", relayErrorText(nil))
	require.Equal(t, "boom", relayErrorText(errors.New("boom")))
	require.Equal(t, -1, openAIWSFirstTokenMsForLog(nil))
	ms := 12
	require.Equal(t, 12, openAIWSFirstTokenMsForLog(&ms))

	require.NotPanics(t, func() {
		logOpenAIWSV2Passthrough("helper_test account_id=%d", 1)
	})
}

func TestLogOpenAIWSV2PassthroughDebug_Disabled(t *testing.T) {
	openAIWSModeDebugLogTestMu.Lock()
	defer openAIWSModeDebugLogTestMu.Unlock()

	logger.InitBootstrap()
	require.NoError(t, logger.SetLevel("info"))
	require.False(t, isOpenAIWSModeDebugEnabled())

	require.NotPanics(t, func() {
		logOpenAIWSV2PassthroughDebug("helper_test_debug account_id=%d", 1)
	})
}

func TestLogOpenAIWSV2PassthroughDebug_Enabled(t *testing.T) {
	openAIWSModeDebugLogTestMu.Lock()
	defer openAIWSModeDebugLogTestMu.Unlock()

	logger.InitBootstrap()
	require.NoError(t, logger.SetLevel("debug"))
	require.True(t, isOpenAIWSModeDebugEnabled())
	t.Cleanup(func() {
		_ = logger.SetLevel("info")
	})

	require.NotPanics(t, func() {
		logOpenAIWSV2PassthroughDebug("helper_test_debug_enabled account_id=%d", 1)
	})
}

func TestProxyResponsesWebSocketV2Passthrough_InvalidInputs(t *testing.T) {
	account := buildIngressPolicyTestAccount(map[string]any{
		"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough,
	})
	firstMessage := []byte(`{"type":"response.create","model":"gpt-5.3-codex","input":[]}`)
	var nilSvc *OpenAIGatewayService
	err := nilSvc.proxyResponsesWebSocketV2Passthrough(
		context.Background(),
		nil,
		nil,
		account,
		"sk-test",
		coderws.MessageText,
		firstMessage,
		nil,
		OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "service is nil")

	cfg := buildIngressPolicyTestConfig()
	svc := buildIngressPolicyTestService(cfg)
	dummyClient := &coderws.Conn{}

	err = svc.proxyResponsesWebSocketV2Passthrough(
		context.Background(),
		nil,
		nil,
		account,
		"sk-test",
		coderws.MessageText,
		firstMessage,
		nil,
		OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "client websocket is nil")

	err = svc.proxyResponsesWebSocketV2Passthrough(
		context.Background(),
		nil,
		dummyClient,
		nil,
		"sk-test",
		coderws.MessageText,
		firstMessage,
		nil,
		OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "account is nil")

	err = svc.proxyResponsesWebSocketV2Passthrough(
		context.Background(),
		nil,
		dummyClient,
		account,
		" ",
		coderws.MessageText,
		firstMessage,
		nil,
		OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "token is empty")
}

func TestProxyResponsesWebSocketV2Passthrough_DialFailure(t *testing.T) {
	cfg := buildIngressPolicyTestConfig()
	svc := buildIngressPolicyTestService(cfg)
	account := buildIngressPolicyTestAccount(map[string]any{
		"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough,
	})
	dummyClient := &coderws.Conn{}
	firstMessage := []byte(`{"type":"response.create","model":"gpt-5.3-codex","input":[]}`)

	svc.openaiWSPassthroughDialer = &passthroughDialerStub{
		err:        errors.New("dial failed"),
		statusCode: 503,
	}
	err := svc.proxyResponsesWebSocketV2Passthrough(
		context.Background(),
		nil,
		dummyClient,
		account,
		"sk-test",
		coderws.MessageText,
		firstMessage,
		nil,
		OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "openai ws passthrough dial")
}

type passthroughDialerStub struct {
	conn       openAIWSClientConn
	statusCode int
	headers    http.Header
	err        error
	dialCount  atomic.Int32
}

func (d *passthroughDialerStub) Dial(
	_ context.Context,
	_ string,
	_ http.Header,
	_ string,
) (openAIWSClientConn, int, http.Header, error) {
	d.dialCount.Add(1)
	return d.conn, d.statusCode, d.headers, d.err
}

type passthroughUpstreamConn struct {
	mu    sync.Mutex
	reads []struct {
		msgType coderws.MessageType
		payload []byte
	}
	readDelay time.Duration
	readErr   error
	writes    [][]byte
	closed    bool
}

func (c *passthroughUpstreamConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	if c.readDelay > 0 {
		timer := time.NewTimer(c.readDelay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return coderws.MessageText, nil, ctx.Err()
		case <-timer.C:
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return coderws.MessageText, nil, io.EOF
	}
	if len(c.reads) == 0 {
		if c.readErr != nil {
			return coderws.MessageText, nil, c.readErr
		}
		return coderws.MessageText, nil, io.EOF
	}
	item := c.reads[0]
	c.reads = c.reads[1:]
	return item.msgType, append([]byte(nil), item.payload...), nil
}

func (c *passthroughUpstreamConn) WriteFrame(_ context.Context, _ coderws.MessageType, payload []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writes = append(c.writes, append([]byte(nil), payload...))
	return nil
}

func (c *passthroughUpstreamConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func (c *passthroughUpstreamConn) WriteJSON(context.Context, any) error { return nil }
func (c *passthroughUpstreamConn) ReadMessage(context.Context) ([]byte, error) {
	return nil, io.EOF
}
func (c *passthroughUpstreamConn) Ping(context.Context) error { return nil }

type passthroughBlockingUpstreamConn struct{}

func (c *passthroughBlockingUpstreamConn) ReadFrame(ctx context.Context) (coderws.MessageType, []byte, error) {
	<-ctx.Done()
	return coderws.MessageText, nil, ctx.Err()
}
func (c *passthroughBlockingUpstreamConn) WriteFrame(context.Context, coderws.MessageType, []byte) error {
	return nil
}
func (c *passthroughBlockingUpstreamConn) Close() error { return nil }
func (c *passthroughBlockingUpstreamConn) WriteJSON(context.Context, any) error {
	return nil
}
func (c *passthroughBlockingUpstreamConn) ReadMessage(context.Context) ([]byte, error) {
	return nil, io.EOF
}
func (c *passthroughBlockingUpstreamConn) Ping(context.Context) error { return nil }

func TestProxyResponsesWebSocketV2Passthrough_Success(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := &passthroughUpstreamConn{
		reads: []struct {
			msgType coderws.MessageType
			payload []byte
		}{
			{
				msgType: coderws.MessageText,
				payload: []byte(`{"type":"response.completed","response":{"id":"resp_passthrough","usage":{"input_tokens":3,"output_tokens":2}}}`),
			},
		},
	}
	cfg := buildIngressPolicyTestConfig()
	svc := buildIngressPolicyTestService(cfg)
	svc.openaiWSPassthroughDialer = &passthroughDialerStub{
		conn:       upstream,
		statusCode: 101,
		headers: http.Header{
			"X-Request-ID": []string{"req-passthrough"},
		},
	}
	account := buildIngressPolicyTestAccount(map[string]any{
		"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough,
	})

	var (
		afterTurnCalled bool
		afterTurnErr    error
		afterTurnResult *OpenAIForwardResult
	)
	hooks := &OpenAIWSIngressHooks{
		AfterTurn: func(_ int, result *OpenAIForwardResult, turnErr error) {
			afterTurnCalled = true
			afterTurnErr = turnErr
			afterTurnResult = result
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		require.NoError(t, err)
		defer func() {
			_ = conn.CloseNow()
		}()

		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, err := conn.Read(readCtx)
		cancelRead()
		require.NoError(t, err)

		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginCtx.Request = r
		serverErrCh <- svc.proxyResponsesWebSocketV2Passthrough(
			r.Context(),
			ginCtx,
			conn,
			account,
			"sk-test",
			msgType,
			firstMessage,
			hooks,
			OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
		)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(
		dialCtx,
		"ws"+strings.TrimPrefix(wsServer.URL, "http"),
		nil,
	)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.3-codex","service_tier":"priority","input":[]}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, payload, err := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"response.completed","response":{"id":"resp_passthrough","usage":{"input_tokens":3,"output_tokens":2}}}`, string(payload))

	require.NoError(t, <-serverErrCh)
	require.True(t, afterTurnCalled)
	require.NoError(t, afterTurnErr)
	require.NotNil(t, afterTurnResult)
	require.Equal(t, "resp_passthrough", afterTurnResult.RequestID)
}

func TestProxyResponsesWebSocketV2Passthrough_NilContext(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := &passthroughUpstreamConn{
		reads: []struct {
			msgType coderws.MessageType
			payload []byte
		}{
			{
				msgType: coderws.MessageText,
				payload: []byte(`{"type":"response.completed","response":{"id":"resp_nil_ctx","usage":{"input_tokens":1,"output_tokens":1}}}`),
			},
		},
	}
	cfg := buildIngressPolicyTestConfig()
	svc := buildIngressPolicyTestService(cfg)
	svc.openaiWSPassthroughDialer = &passthroughDialerStub{
		conn:       upstream,
		statusCode: 101,
	}
	account := buildIngressPolicyTestAccount(map[string]any{
		"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough,
	})

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		require.NoError(t, err)
		defer func() {
			_ = conn.CloseNow()
		}()

		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, err := conn.Read(readCtx)
		cancelRead()
		require.NoError(t, err)

		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginCtx.Request = r
		serverErrCh <- svc.proxyResponsesWebSocketV2Passthrough(
			context.TODO(),
			ginCtx,
			conn,
			account,
			"sk-test",
			msgType,
			firstMessage,
			nil,
			OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
		)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(
		dialCtx,
		"ws"+strings.TrimPrefix(wsServer.URL, "http"),
		nil,
	)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.3-codex","service_tier":"priority","input":[]}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, payload, err := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"response.completed","response":{"id":"resp_nil_ctx","usage":{"input_tokens":1,"output_tokens":1}}}`, string(payload))

	require.NoError(t, <-serverErrCh)
}

func TestProxyResponsesWebSocketV2Passthrough_ZeroTurnFallbackCallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := &passthroughUpstreamConn{
		reads: []struct {
			msgType coderws.MessageType
			payload []byte
		}{
			{
				msgType: coderws.MessageText,
				payload: []byte(`{"type":"response.output_text.delta","delta":"hello"}`),
			},
		},
	}
	cfg := buildIngressPolicyTestConfig()
	svc := buildIngressPolicyTestService(cfg)
	svc.openaiWSPassthroughDialer = &passthroughDialerStub{
		conn:       upstream,
		statusCode: 101,
	}
	account := buildIngressPolicyTestAccount(map[string]any{
		"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough,
	})

	var (
		afterTurnCalled bool
		afterTurnErr    error
		afterTurnResult *OpenAIForwardResult
	)
	hooks := &OpenAIWSIngressHooks{
		AfterTurn: func(_ int, result *OpenAIForwardResult, turnErr error) {
			afterTurnCalled = true
			afterTurnErr = turnErr
			afterTurnResult = result
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		require.NoError(t, err)
		defer func() {
			_ = conn.CloseNow()
		}()

		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, err := conn.Read(readCtx)
		cancelRead()
		require.NoError(t, err)

		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginCtx.Request = r
		serverErrCh <- svc.proxyResponsesWebSocketV2Passthrough(
			r.Context(),
			ginCtx,
			conn,
			account,
			"sk-test",
			msgType,
			firstMessage,
			hooks,
			OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
		)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(
		dialCtx,
		"ws"+strings.TrimPrefix(wsServer.URL, "http"),
		nil,
	)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.3-codex","service_tier":"priority","input":[]}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	msgType, payload, err := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, err)
	require.Equal(t, coderws.MessageText, msgType)
	require.JSONEq(t, `{"type":"response.output_text.delta","delta":"hello"}`, string(payload))

	require.NoError(t, <-serverErrCh)
	require.True(t, afterTurnCalled)
	require.NoError(t, afterTurnErr)
	require.NotNil(t, afterTurnResult)
	require.Equal(t, "", afterTurnResult.RequestID)
	require.Equal(t, 0, afterTurnResult.Usage.InputTokens)
	require.Equal(t, "", afterTurnResult.TerminalEventType)
}

func TestProxyResponsesWebSocketV2Passthrough_TurnMetricsPropagated(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := &passthroughUpstreamConn{
		readDelay: 15 * time.Millisecond,
		reads: []struct {
			msgType coderws.MessageType
			payload []byte
		}{
			{
				msgType: coderws.MessageText,
				payload: []byte(`{"type":"response.output_text.delta","response_id":"resp_metrics","delta":"hello"}`),
			},
			{
				msgType: coderws.MessageText,
				payload: []byte(`{"type":"response.completed","response":{"id":"resp_metrics","usage":{"input_tokens":4,"output_tokens":2}}}`),
			},
		},
	}
	cfg := buildIngressPolicyTestConfig()
	svc := buildIngressPolicyTestService(cfg)
	svc.openaiWSPassthroughDialer = &passthroughDialerStub{
		conn:       upstream,
		statusCode: 101,
		headers: http.Header{
			"X-Request-ID": []string{"req-passthrough"},
		},
	}
	account := buildIngressPolicyTestAccount(map[string]any{
		"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough,
	})

	var (
		afterTurnCalled bool
		afterTurnResult *OpenAIForwardResult
		afterTurnErr    error
	)
	hooks := &OpenAIWSIngressHooks{
		AfterTurn: func(_ int, result *OpenAIForwardResult, turnErr error) {
			afterTurnCalled = true
			afterTurnResult = result
			afterTurnErr = turnErr
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		require.NoError(t, err)
		defer func() {
			_ = conn.CloseNow()
		}()

		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, err := conn.Read(readCtx)
		cancelRead()
		require.NoError(t, err)

		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginCtx.Request = r
		serverErrCh <- svc.proxyResponsesWebSocketV2Passthrough(
			r.Context(),
			ginCtx,
			conn,
			account,
			"sk-test",
			msgType,
			firstMessage,
			hooks,
			OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
		)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(
		dialCtx,
		"ws"+strings.TrimPrefix(wsServer.URL, "http"),
		nil,
	)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.3-codex","service_tier":"priority","input":[]}`))
	cancelWrite()
	require.NoError(t, err)

	// 读取透传下发事件直到 terminal 到达。
	readCtx1, cancelRead1 := context.WithTimeout(context.Background(), 3*time.Second)
	_, _, err = clientConn.Read(readCtx1)
	cancelRead1()
	require.NoError(t, err)
	readCtx2, cancelRead2 := context.WithTimeout(context.Background(), 3*time.Second)
	_, payload2, err := clientConn.Read(readCtx2)
	cancelRead2()
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"response.completed","response":{"id":"resp_metrics","usage":{"input_tokens":4,"output_tokens":2}}}`, string(payload2))

	require.NoError(t, <-serverErrCh)
	require.True(t, afterTurnCalled)
	require.NoError(t, afterTurnErr)
	require.NotNil(t, afterTurnResult)
	require.NotNil(t, afterTurnResult.FirstTokenMs)
	require.GreaterOrEqual(t, *afterTurnResult.FirstTokenMs, 0)
	require.Greater(t, afterTurnResult.Duration.Milliseconds(), int64(0))
}

func TestProxyResponsesWebSocketV2Passthrough_MultiTurnAfterTurnOnTerminal(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := &passthroughUpstreamConn{
		reads: []struct {
			msgType coderws.MessageType
			payload []byte
		}{
			{
				msgType: coderws.MessageText,
				payload: []byte(`{"type":"response.completed","response":{"id":"resp_turn_1","usage":{"input_tokens":3,"output_tokens":2}}}`),
			},
			{
				msgType: coderws.MessageText,
				payload: []byte(`{"type":"response.failed","response":{"id":"resp_turn_2","usage":{"input_tokens":1,"output_tokens":4}}}`),
			},
		},
	}
	cfg := buildIngressPolicyTestConfig()
	svc := buildIngressPolicyTestService(cfg)
	svc.openaiWSPassthroughDialer = &passthroughDialerStub{
		conn:       upstream,
		statusCode: 101,
		headers: http.Header{
			"X-Request-ID": []string{"req-passthrough"},
		},
	}
	account := buildIngressPolicyTestAccount(map[string]any{
		"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough,
	})

	type afterTurnCall struct {
		turn   int
		result *OpenAIForwardResult
		err    error
	}
	var (
		callsMu sync.Mutex
		calls   = make([]afterTurnCall, 0, 3)
	)
	hooks := &OpenAIWSIngressHooks{
		AfterTurn: func(turn int, result *OpenAIForwardResult, turnErr error) {
			callsMu.Lock()
			defer callsMu.Unlock()
			calls = append(calls, afterTurnCall{
				turn:   turn,
				result: result,
				err:    turnErr,
			})
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		require.NoError(t, err)
		defer func() {
			_ = conn.CloseNow()
		}()

		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, err := conn.Read(readCtx)
		cancelRead()
		require.NoError(t, err)

		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginCtx.Request = r
		serverErrCh <- svc.proxyResponsesWebSocketV2Passthrough(
			r.Context(),
			ginCtx,
			conn,
			account,
			"sk-test",
			msgType,
			firstMessage,
			hooks,
			OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
		)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(
		dialCtx,
		"ws"+strings.TrimPrefix(wsServer.URL, "http"),
		nil,
	)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.3-codex","service_tier":"priority","input":[]}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx1, cancelRead1 := context.WithTimeout(context.Background(), 3*time.Second)
	_, payload1, err := clientConn.Read(readCtx1)
	cancelRead1()
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"response.completed","response":{"id":"resp_turn_1","usage":{"input_tokens":3,"output_tokens":2}}}`, string(payload1))

	readCtx2, cancelRead2 := context.WithTimeout(context.Background(), 3*time.Second)
	_, payload2, err := clientConn.Read(readCtx2)
	cancelRead2()
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"response.failed","response":{"id":"resp_turn_2","usage":{"input_tokens":1,"output_tokens":4}}}`, string(payload2))

	require.NoError(t, <-serverErrCh)

	callsMu.Lock()
	gotCalls := append([]afterTurnCall(nil), calls...)
	callsMu.Unlock()
	require.Len(t, gotCalls, 2)
	require.Equal(t, 1, gotCalls[0].turn)
	require.NoError(t, gotCalls[0].err)
	require.NotNil(t, gotCalls[0].result)
	require.Equal(t, "resp_turn_1", gotCalls[0].result.RequestID)
	require.NotNil(t, gotCalls[0].result.ServiceTier)
	require.Equal(t, "priority", *gotCalls[0].result.ServiceTier)
	require.Equal(t, 3, gotCalls[0].result.Usage.InputTokens)
	require.Equal(t, 2, gotCalls[0].result.Usage.OutputTokens)
	require.Equal(t, 2, gotCalls[1].turn)
	require.NoError(t, gotCalls[1].err)
	require.NotNil(t, gotCalls[1].result)
	require.Equal(t, "resp_turn_2", gotCalls[1].result.RequestID)
	require.NotNil(t, gotCalls[1].result.ServiceTier)
	require.Equal(t, "priority", *gotCalls[1].result.ServiceTier)
	require.Equal(t, 1, gotCalls[1].result.Usage.InputTokens)
	require.Equal(t, 4, gotCalls[1].result.Usage.OutputTokens)
}

func TestProxyResponsesWebSocketV2Passthrough_ErrorAfterTurn_NoPartialDuplicate(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := &passthroughUpstreamConn{
		reads: []struct {
			msgType coderws.MessageType
			payload []byte
		}{
			{
				msgType: coderws.MessageText,
				payload: []byte(`{"type":"response.completed","response":{"id":"resp_turn_ok","usage":{"input_tokens":2,"output_tokens":1}}}`),
			},
		},
		readErr: errors.New("upstream stream broken"),
	}
	cfg := buildIngressPolicyTestConfig()
	svc := buildIngressPolicyTestService(cfg)
	svc.openaiWSPassthroughDialer = &passthroughDialerStub{
		conn:       upstream,
		statusCode: 101,
		headers: http.Header{
			"X-Request-ID": []string{"req-passthrough"},
		},
	}
	account := buildIngressPolicyTestAccount(map[string]any{
		"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough,
	})

	type afterTurnCall struct {
		turn   int
		result *OpenAIForwardResult
		err    error
	}
	var (
		callsMu sync.Mutex
		calls   = make([]afterTurnCall, 0, 3)
	)
	hooks := &OpenAIWSIngressHooks{
		AfterTurn: func(turn int, result *OpenAIForwardResult, turnErr error) {
			callsMu.Lock()
			defer callsMu.Unlock()
			calls = append(calls, afterTurnCall{
				turn:   turn,
				result: result,
				err:    turnErr,
			})
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		require.NoError(t, err)
		defer func() {
			_ = conn.CloseNow()
		}()

		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, err := conn.Read(readCtx)
		cancelRead()
		require.NoError(t, err)

		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginCtx.Request = r
		serverErrCh <- svc.proxyResponsesWebSocketV2Passthrough(
			r.Context(),
			ginCtx,
			conn,
			account,
			"sk-test",
			msgType,
			firstMessage,
			hooks,
			OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
		)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(
		dialCtx,
		"ws"+strings.TrimPrefix(wsServer.URL, "http"),
		nil,
	)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.3-codex","input":[]}`))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, payload, err := clientConn.Read(readCtx)
	cancelRead()
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"response.completed","response":{"id":"resp_turn_ok","usage":{"input_tokens":2,"output_tokens":1}}}`, string(payload))

	serverErr := <-serverErrCh
	require.Error(t, serverErr)
	require.Contains(t, serverErr.Error(), "upstream stream broken")

	callsMu.Lock()
	gotCalls := append([]afterTurnCall(nil), calls...)
	callsMu.Unlock()
	require.Len(t, gotCalls, 2)
	require.Equal(t, 1, gotCalls[0].turn)
	require.NoError(t, gotCalls[0].err)
	require.NotNil(t, gotCalls[0].result)
	require.Equal(t, "resp_turn_ok", gotCalls[0].result.RequestID)
	require.Equal(t, 2, gotCalls[1].turn)
	require.Error(t, gotCalls[1].err)
	require.Nil(t, gotCalls[1].result)

	partial, ok := OpenAIWSIngressTurnPartialResult(gotCalls[1].err)
	require.False(t, ok)
	require.Nil(t, partial)
}

func TestProxyResponsesWebSocketV2Passthrough_UpstreamWithoutFrameConn(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := buildIngressPolicyTestConfig()
	svc := buildIngressPolicyTestService(cfg)
	svc.openaiWSPassthroughDialer = &passthroughDialerStub{
		conn: &openAIWSFakeConn{},
	}
	account := buildIngressPolicyTestAccount(map[string]any{
		"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough,
	})

	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		require.NoError(t, err)
		defer func() {
			_ = conn.CloseNow()
		}()
		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, err := conn.Read(readCtx)
		cancelRead()
		require.NoError(t, err)

		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginCtx.Request = r
		err = svc.proxyResponsesWebSocketV2Passthrough(
			r.Context(),
			ginCtx,
			conn,
			account,
			"sk-test",
			msgType,
			firstMessage,
			nil,
			OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
		)
		require.Error(t, err)
		require.Contains(t, err.Error(), "does not support frame relay")
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(
		dialCtx,
		"ws"+strings.TrimPrefix(wsServer.URL, "http"),
		nil,
	)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.3-codex","input":[]}`))
	cancelWrite()
	require.NoError(t, err)
}

func TestProxyResponsesWebSocketV2Passthrough_BuildWSURLError(t *testing.T) {
	cfg := buildIngressPolicyTestConfig()
	svc := buildIngressPolicyTestService(cfg)
	account := buildIngressPolicyTestAccount(map[string]any{
		"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough,
	})
	account.Credentials["base_url"] = "http://[::1"
	err := svc.proxyResponsesWebSocketV2Passthrough(
		context.Background(),
		nil,
		&coderws.Conn{},
		account,
		"sk-test",
		coderws.MessageText,
		[]byte(`{"type":"response.create","model":"gpt-5.3-codex","input":[]}`),
		nil,
		OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
	)
	require.Error(t, err)
	require.Contains(t, err.Error(), "build ws url")
}

func TestProxyResponsesWebSocketV2Passthrough_IdleTimeoutErrorPath(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := buildIngressPolicyTestConfig()
	cfg.Gateway.OpenAIWS.ClientReadIdleTimeoutSeconds = 1
	svc := buildIngressPolicyTestService(cfg)
	svc.openaiWSPassthroughDialer = &passthroughDialerStub{
		conn: &passthroughBlockingUpstreamConn{},
	}
	account := buildIngressPolicyTestAccount(map[string]any{
		"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModePassthrough,
	})

	var (
		afterTurnCalled bool
		afterTurnErr    error
	)
	hooks := &OpenAIWSIngressHooks{
		AfterTurn: func(_ int, _ *OpenAIForwardResult, turnErr error) {
			afterTurnCalled = true
			afterTurnErr = turnErr
		},
	}

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		require.NoError(t, err)
		defer func() {
			_ = conn.CloseNow()
		}()

		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		msgType, firstMessage, err := conn.Read(readCtx)
		cancelRead()
		require.NoError(t, err)

		ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ginCtx.Request = r
		serverErrCh <- svc.proxyResponsesWebSocketV2Passthrough(
			r.Context(),
			ginCtx,
			conn,
			account,
			"sk-test",
			msgType,
			firstMessage,
			hooks,
			OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
		)
	}))
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(
		dialCtx,
		"ws"+strings.TrimPrefix(wsServer.URL, "http"),
		nil,
	)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.create","model":"gpt-5.3-codex","input":[]}`))
	cancelWrite()
	require.NoError(t, err)

	serverErr := <-serverErrCh
	require.Error(t, serverErr)
	require.Contains(t, serverErr.Error(), "idle timeout")
	require.True(t, afterTurnCalled)
	require.Error(t, afterTurnErr)
	var closeErr *OpenAIWSClientCloseError
	require.ErrorAs(t, afterTurnErr, &closeErr)
	require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
	require.Contains(t, closeErr.Reason(), "idle timeout")
	require.ErrorIs(t, afterTurnErr, openaiwsv2.ErrRelayIdleTimeout)
	require.NotErrorIs(t, afterTurnErr, context.DeadlineExceeded)
}

func TestMapOpenAIWSPassthroughDialError_StatusMapping(t *testing.T) {
	t.Parallel()

	svc := &OpenAIGatewayService{}

	t.Run("nil error returns nil", func(t *testing.T) {
		err := svc.mapOpenAIWSPassthroughDialError(nil, 0, nil)
		require.NoError(t, err)
	})

	t.Run("401 maps to policy violation", func(t *testing.T) {
		err := svc.mapOpenAIWSPassthroughDialError(errors.New("unauthorized"), 401, nil)
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, err, &closeErr)
		require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
	})

	t.Run("403 maps to policy violation", func(t *testing.T) {
		err := svc.mapOpenAIWSPassthroughDialError(errors.New("forbidden"), 403, nil)
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, err, &closeErr)
		require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
	})

	t.Run("429 maps to try again later", func(t *testing.T) {
		err := svc.mapOpenAIWSPassthroughDialError(errors.New("rate limited"), 429, nil)
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, err, &closeErr)
		require.Equal(t, coderws.StatusTryAgainLater, closeErr.StatusCode())
	})

	t.Run("4xx generic maps to policy violation", func(t *testing.T) {
		err := svc.mapOpenAIWSPassthroughDialError(errors.New("bad request"), 400, nil)
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, err, &closeErr)
		require.Equal(t, coderws.StatusPolicyViolation, closeErr.StatusCode())
	})

	t.Run("5xx wraps as generic error without CloseError", func(t *testing.T) {
		err := svc.mapOpenAIWSPassthroughDialError(errors.New("server error"), 500, nil)
		require.Error(t, err)
		var closeErr *OpenAIWSClientCloseError
		require.False(t, errors.As(err, &closeErr), "5xx 不应封装为 CloseError")
		require.Contains(t, err.Error(), "openai ws passthrough dial")
	})

	t.Run("deadline exceeded maps to try again later", func(t *testing.T) {
		err := svc.mapOpenAIWSPassthroughDialError(context.DeadlineExceeded, 0, nil)
		var closeErr *OpenAIWSClientCloseError
		require.ErrorAs(t, err, &closeErr)
		require.Equal(t, coderws.StatusTryAgainLater, closeErr.StatusCode())
	})

	t.Run("context canceled returns original error", func(t *testing.T) {
		err := svc.mapOpenAIWSPassthroughDialError(context.Canceled, 0, nil)
		require.ErrorIs(t, err, context.Canceled)
		var closeErr *OpenAIWSClientCloseError
		require.False(t, errors.As(err, &closeErr), "context.Canceled 不应封装为 CloseError")
	})
}

func TestOpenAIWSPassthroughBeforeClientFrameHook(t *testing.T) {
	t.Parallel()

	require.Nil(t, openAIWSPassthroughBeforeClientFrameHook(nil))
	require.Nil(t, openAIWSPassthroughBeforeClientFrameHook(&OpenAIWSIngressHooks{}))

	var turns []int
	hook := openAIWSPassthroughBeforeClientFrameHook(&OpenAIWSIngressHooks{
		BeforeTurn: func(turn int) error {
			turns = append(turns, turn)
			if turn == 3 {
				return errors.New("billing check failed")
			}
			return nil
		},
	})
	require.NotNil(t, hook)
	require.NoError(t, hook(2, coderws.MessageText, []byte(`{"type":"response.create"}`)))
	require.ErrorContains(t, hook(3, coderws.MessageText, []byte(`{"type":"response.create"}`)), "billing check failed")
	require.Equal(t, []int{2, 3}, turns)
}
