package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	pkghttputil "github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestOpenAIHandleStreamingAwareError_JSONEscaping(t *testing.T) {
	tests := []struct {
		name    string
		errType string
		message string
	}{
		{
			name:    "包含双引号的消息",
			errType: "server_error",
			message: `upstream returned "invalid" response`,
		},
		{
			name:    "包含反斜杠的消息",
			errType: "server_error",
			message: `path C:\Users\test\file.txt not found`,
		},
		{
			name:    "包含双引号和反斜杠的消息",
			errType: "upstream_error",
			message: `error parsing "key\value": unexpected token`,
		},
		{
			name:    "包含换行符的消息",
			errType: "server_error",
			message: "line1\nline2\ttab",
		},
		{
			name:    "普通消息",
			errType: "upstream_error",
			message: "Upstream service temporarily unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

			h := &OpenAIGatewayHandler{}
			h.handleStreamingAwareError(c, http.StatusBadGateway, tt.errType, tt.message, true)

			body := w.Body.String()

			// 验证 SSE 格式：event: error\ndata: {JSON}\n\n
			assert.True(t, strings.HasPrefix(body, "event: error\n"), "应以 'event: error\\n' 开头")
			assert.True(t, strings.HasSuffix(body, "\n\n"), "应以 '\\n\\n' 结尾")

			// 提取 data 部分
			lines := strings.Split(strings.TrimSuffix(body, "\n\n"), "\n")
			require.Len(t, lines, 2, "应有 event 行和 data 行")
			dataLine := lines[1]
			require.True(t, strings.HasPrefix(dataLine, "data: "), "第二行应以 'data: ' 开头")
			jsonStr := strings.TrimPrefix(dataLine, "data: ")

			// 验证 JSON 合法性
			var parsed map[string]any
			err := json.Unmarshal([]byte(jsonStr), &parsed)
			require.NoError(t, err, "JSON 应能被成功解析，原始 JSON: %s", jsonStr)

			// 验证结构
			errorObj, ok := parsed["error"].(map[string]any)
			require.True(t, ok, "应包含 error 对象")
			assert.Equal(t, tt.errType, errorObj["type"])
			assert.Equal(t, tt.message, errorObj["message"])
		})
	}
}

func TestOpenAIHandleStreamingAwareError_NonStreaming(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	h := &OpenAIGatewayHandler{}
	h.handleStreamingAwareError(c, http.StatusBadGateway, "upstream_error", "test error", false)

	// 非流式应返回 JSON 响应
	assert.Equal(t, http.StatusBadGateway, w.Code)

	var parsed map[string]any
	err := json.Unmarshal(w.Body.Bytes(), &parsed)
	require.NoError(t, err)
	errorObj, ok := parsed["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "upstream_error", errorObj["type"])
	assert.Equal(t, "test error", errorObj["message"])
}

func TestReadRequestBodyWithPrealloc(t *testing.T) {
	payload := `{"model":"gpt-5","input":"hello"}`
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(payload))
	req.ContentLength = int64(len(payload))

	body, err := pkghttputil.ReadRequestBodyWithPrealloc(req)
	require.NoError(t, err)
	require.Equal(t, payload, string(body))
}

func TestReadRequestBodyWithPrealloc_MaxBytesError(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(strings.Repeat("x", 8)))
	req.Body = http.MaxBytesReader(rec, req.Body, 4)

	_, err := pkghttputil.ReadRequestBodyWithPrealloc(req)
	require.Error(t, err)
	var maxErr *http.MaxBytesError
	require.ErrorAs(t, err, &maxErr)
}

func TestOpenAIEnsureForwardErrorResponse_WritesFallbackWhenNotWritten(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	h := &OpenAIGatewayHandler{}
	wrote := h.ensureForwardErrorResponse(c, false)

	require.True(t, wrote)
	require.Equal(t, http.StatusBadGateway, w.Code)

	var parsed map[string]any
	err := json.Unmarshal(w.Body.Bytes(), &parsed)
	require.NoError(t, err)
	errorObj, ok := parsed["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "upstream_error", errorObj["type"])
	assert.Equal(t, "Upstream request failed", errorObj["message"])
}

func TestOpenAIEnsureForwardErrorResponse_DoesNotOverrideWrittenResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.String(http.StatusTeapot, "already written")

	h := &OpenAIGatewayHandler{}
	wrote := h.ensureForwardErrorResponse(c, false)

	require.False(t, wrote)
	require.Equal(t, http.StatusTeapot, w.Code)
	assert.Equal(t, "already written", w.Body.String())
}

func TestShouldLogOpenAIForwardFailureAsWarn(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("fallback_written_should_not_downgrade", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		require.False(t, shouldLogOpenAIForwardFailureAsWarn(c, true))
	})

	t.Run("context_nil_should_not_downgrade", func(t *testing.T) {
		require.False(t, shouldLogOpenAIForwardFailureAsWarn(nil, false))
	})

	t.Run("response_not_written_should_not_downgrade", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		require.False(t, shouldLogOpenAIForwardFailureAsWarn(c, false))
	})

	t.Run("response_already_written_should_downgrade", func(t *testing.T) {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		c.String(http.StatusForbidden, "already written")
		require.True(t, shouldLogOpenAIForwardFailureAsWarn(c, false))
	})
}

func TestOpenAIRecoverResponsesPanic_WritesFallbackResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	h := &OpenAIGatewayHandler{}
	streamStarted := false
	require.NotPanics(t, func() {
		func() {
			defer h.recoverResponsesPanic(c, &streamStarted)
			panic("test panic")
		}()
	})

	require.Equal(t, http.StatusBadGateway, w.Code)

	var parsed map[string]any
	err := json.Unmarshal(w.Body.Bytes(), &parsed)
	require.NoError(t, err)

	errorObj, ok := parsed["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "upstream_error", errorObj["type"])
	assert.Equal(t, "Upstream request failed", errorObj["message"])
}

func TestOpenAIRecoverResponsesPanic_NoPanicNoWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	h := &OpenAIGatewayHandler{}
	streamStarted := false
	require.NotPanics(t, func() {
		func() {
			defer h.recoverResponsesPanic(c, &streamStarted)
		}()
	})

	require.False(t, c.Writer.Written())
	assert.Equal(t, "", w.Body.String())
}

func TestOpenAIRecoverResponsesPanic_DoesNotOverrideWrittenResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.String(http.StatusTeapot, "already written")

	h := &OpenAIGatewayHandler{}
	streamStarted := false
	require.NotPanics(t, func() {
		func() {
			defer h.recoverResponsesPanic(c, &streamStarted)
			panic("test panic")
		}()
	})

	require.Equal(t, http.StatusTeapot, w.Code)
	assert.Equal(t, "already written", w.Body.String())
}

func TestOpenAIMissingResponsesDependencies(t *testing.T) {
	t.Run("nil_handler", func(t *testing.T) {
		var h *OpenAIGatewayHandler
		require.Equal(t, []string{"handler"}, h.missingResponsesDependencies())
	})

	t.Run("all_dependencies_missing", func(t *testing.T) {
		h := &OpenAIGatewayHandler{}
		require.Equal(t,
			[]string{"gatewayService", "billingCacheService", "apiKeyService", "concurrencyHelper"},
			h.missingResponsesDependencies(),
		)
	})

	t.Run("all_dependencies_present", func(t *testing.T) {
		h := &OpenAIGatewayHandler{
			gatewayService:      &service.OpenAIGatewayService{},
			billingCacheService: &service.BillingCacheService{},
			apiKeyService:       &service.APIKeyService{},
			concurrencyHelper: &ConcurrencyHelper{
				concurrencyService: &service.ConcurrencyService{},
			},
		}
		require.Empty(t, h.missingResponsesDependencies())
	})
}

func TestOpenAIEnsureResponsesDependencies(t *testing.T) {
	t.Run("missing_dependencies_returns_503", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

		h := &OpenAIGatewayHandler{}
		ok := h.ensureResponsesDependencies(c, nil)

		require.False(t, ok)
		require.Equal(t, http.StatusServiceUnavailable, w.Code)
		var parsed map[string]any
		err := json.Unmarshal(w.Body.Bytes(), &parsed)
		require.NoError(t, err)
		errorObj, exists := parsed["error"].(map[string]any)
		require.True(t, exists)
		assert.Equal(t, "api_error", errorObj["type"])
		assert.Equal(t, "Service temporarily unavailable", errorObj["message"])
	})

	t.Run("already_written_response_not_overridden", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		c.String(http.StatusTeapot, "already written")

		h := &OpenAIGatewayHandler{}
		ok := h.ensureResponsesDependencies(c, nil)

		require.False(t, ok)
		require.Equal(t, http.StatusTeapot, w.Code)
		assert.Equal(t, "already written", w.Body.String())
	})

	t.Run("dependencies_ready_returns_true_and_no_write", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

		h := &OpenAIGatewayHandler{
			gatewayService:      &service.OpenAIGatewayService{},
			billingCacheService: &service.BillingCacheService{},
			apiKeyService:       &service.APIKeyService{},
			concurrencyHelper: &ConcurrencyHelper{
				concurrencyService: &service.ConcurrencyService{},
			},
		}
		ok := h.ensureResponsesDependencies(c, nil)

		require.True(t, ok)
		require.False(t, c.Writer.Written())
		assert.Equal(t, "", w.Body.String())
	})
}

func TestOpenAIResponses_MissingDependencies_ReturnsServiceUnavailable(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-5","stream":false}`))
	c.Request.Header.Set("Content-Type", "application/json")

	groupID := int64(2)
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		ID:      10,
		GroupID: &groupID,
	})
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{
		UserID:      1,
		Concurrency: 1,
	})

	// 故意使用未初始化依赖，验证快速失败而不是崩溃。
	h := &OpenAIGatewayHandler{}
	require.NotPanics(t, func() {
		h.Responses(c)
	})

	require.Equal(t, http.StatusServiceUnavailable, w.Code)

	var parsed map[string]any
	err := json.Unmarshal(w.Body.Bytes(), &parsed)
	require.NoError(t, err)

	errorObj, ok := parsed["error"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, "api_error", errorObj["type"])
	assert.Equal(t, "Service temporarily unavailable", errorObj["message"])
}

func TestOpenAIResponses_SetsClientTransportHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(`{"model":"gpt-5"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h := &OpenAIGatewayHandler{}
	h.Responses(c)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Equal(t, service.OpenAIClientTransportHTTP, service.GetOpenAIClientTransport(c))
}

func TestOpenAIResponses_RejectsMessageIDAsPreviousResponseID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(
		`{"model":"gpt-5.1","stream":false,"previous_response_id":"msg_123456","input":[{"type":"input_text","text":"hello"}]}`,
	))
	c.Request.Header.Set("Content-Type", "application/json")

	groupID := int64(2)
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		ID:      101,
		GroupID: &groupID,
		User:    &service.User{ID: 1},
	})
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{
		UserID:      1,
		Concurrency: 1,
	})

	h := newOpenAIHandlerForPreviousResponseIDValidation(t, nil)
	h.Responses(c)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "previous_response_id must be a response.id")
}

func TestOpenAIResponses_InvalidJSONBodyReturnsBadRequest(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(
		`{"model":"gpt-5.1","stream":false,invalid}`,
	))
	c.Request.Header.Set("Content-Type", "application/json")

	groupID := int64(2)
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{
		ID:      201,
		GroupID: &groupID,
		User:    &service.User{ID: 1},
	})
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{
		UserID:      1,
		Concurrency: 1,
	})

	h := newOpenAIHandlerForPreviousResponseIDValidation(t, nil)
	h.Responses(c)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "Failed to parse request body")
}

func TestOpenAIResponsesWebSocket_SetsClientTransportWSWhenUpgradeValid(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/openai/v1/responses", nil)
	c.Request.Header.Set("Upgrade", "websocket")
	c.Request.Header.Set("Connection", "Upgrade")

	h := &OpenAIGatewayHandler{}
	h.ResponsesWebSocket(c)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Equal(t, service.OpenAIClientTransportWS, service.GetOpenAIClientTransport(c))
}

func TestOpenAIResponsesWebSocket_InvalidUpgradeDoesNotSetTransport(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/openai/v1/responses", nil)

	h := &OpenAIGatewayHandler{}
	h.ResponsesWebSocket(c)

	require.Equal(t, http.StatusUpgradeRequired, w.Code)
	require.Equal(t, service.OpenAIClientTransportUnknown, service.GetOpenAIClientTransport(c))
}

func TestOpenAIResponsesWebSocket_DoesNotEarlyRejectMessageIDPreviousResponseID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cache := &concurrencyCacheMock{
		acquireUserSlotFn: func(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error) {
			return false, nil
		},
	}
	h := newOpenAIHandlerForPreviousResponseIDValidation(t, cache)
	wsServer := newOpenAIWSHandlerTestServer(t, h, middleware.AuthSubject{UserID: 1, Concurrency: 1})
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http")+"/openai/v1/responses", nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(
		`{"type":"response.create","model":"gpt-5.1","stream":false,"previous_response_id":"msg_abc123"}`,
	))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, _, err = clientConn.Read(readCtx)
	cancelRead()
	require.Error(t, err)
	var closeErr coderws.CloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, coderws.StatusTryAgainLater, closeErr.Code)
	require.Contains(t, strings.ToLower(closeErr.Reason), "too many concurrent requests")
}

func TestOpenAIResponsesWebSocket_CtxPoolRejectsMessageIDBeforeScheduling(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = service.OpenAIWSIngressModeCtxPool

	cache := &concurrencyCacheMock{
		acquireUserSlotFn: func(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error) {
			return true, nil
		},
		acquireAccountSlotFn: func(ctx context.Context, accountID int64, maxConcurrency int, requestID string) (bool, error) {
			return true, nil
		},
	}

	h := &OpenAIGatewayHandler{
		gatewayService:      &service.OpenAIGatewayService{},
		billingCacheService: &service.BillingCacheService{},
		apiKeyService:       &service.APIKeyService{},
		concurrencyHelper:   NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second),
		cfg:                 cfg,
	}

	var scheduleCalls atomic.Int32
	var stickyBindCalls atomic.Int32
	h.wsSelectAccountWithSchedulerFn = func(
		ctx context.Context,
		groupID *int64,
		previousResponseID string,
		sessionHash string,
		requestedModel string,
		excludedIDs map[int64]struct{},
		requiredTransport service.OpenAIUpstreamTransport,
	) (*service.AccountSelectionResult, service.OpenAIAccountScheduleDecision, error) {
		scheduleCalls.Add(1)
		return nil, service.OpenAIAccountScheduleDecision{}, errors.New("should not be called")
	}
	h.wsBindStickySessionFn = func(ctx context.Context, groupID *int64, sessionHash string, accountID int64) error {
		stickyBindCalls.Add(1)
		return nil
	}

	wsServer := newOpenAIWSHandlerTestServer(t, h, middleware.AuthSubject{UserID: 1, Concurrency: 1})
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http")+"/openai/v1/responses", nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(
		`{"type":"response.create","model":"gpt-5.1","stream":false,"previous_response_id":"msg_abc123"}`,
	))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, _, err = clientConn.Read(readCtx)
	cancelRead()
	require.Error(t, err)

	var closeErr coderws.CloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, coderws.StatusPolicyViolation, closeErr.Code)
	require.Contains(t, strings.ToLower(closeErr.Reason), "previous_response_id must be a response.id")
	require.Equal(t, int32(0), scheduleCalls.Load())
	require.Equal(t, int32(0), stickyBindCalls.Load())
}

func TestOpenAIResponsesWebSocket_RejectsEmptyModelInFirstPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := newOpenAIHandlerForPreviousResponseIDValidation(t, nil)
	wsServer := newOpenAIWSHandlerTestServer(t, h, middleware.AuthSubject{UserID: 1, Concurrency: 1})
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http")+"/openai/v1/responses", nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(
		`{"type":"response.create","stream":false,"input":[]}`,
	))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, _, err = clientConn.Read(readCtx)
	cancelRead()
	require.Error(t, err)
	var closeErr coderws.CloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, coderws.StatusPolicyViolation, closeErr.Code)
	require.Contains(t, strings.ToLower(closeErr.Reason), "model is required")
}

func TestOpenAIResponsesWebSocket_RejectsEmptyModelWithoutCallingProxy(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := newOpenAIHandlerForPreviousResponseIDValidation(t, nil)
	var proxyCalls atomic.Int32
	h.wsProxyResponsesWSFn = func(
		ctx context.Context,
		c *gin.Context,
		clientConn *coderws.Conn,
		account *service.Account,
		token string,
		firstClientMessageType coderws.MessageType,
		firstClientMessage []byte,
		hooks *service.OpenAIWSIngressHooks,
	) error {
		proxyCalls.Add(1)
		return nil
	}

	wsServer := newOpenAIWSHandlerTestServer(t, h, middleware.AuthSubject{UserID: 1, Concurrency: 1})
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http")+"/openai/v1/responses", nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(
		`{"type":"response.create","stream":false,"input":[]}`,
	))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, _, err = clientConn.Read(readCtx)
	cancelRead()
	require.Error(t, err)
	var closeErr coderws.CloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, coderws.StatusPolicyViolation, closeErr.Code)
	require.Contains(t, strings.ToLower(closeErr.Reason), "model is required")
	require.Equal(t, int32(0), proxyCalls.Load())
}

func TestOpenAIResponsesWebSocket_PassthroughAndCtxPoolShareSchedulerInputsAndStickyBind(t *testing.T) {
	gin.SetMode(gin.TestMode)

	type wsScheduleCapture struct {
		groupID            int64
		previousResponseID string
		sessionHash        string
		model              string
		transport          service.OpenAIUpstreamTransport
		stickyBindCount    int
		stickyGroupID      int64
		stickySessionHash  string
		stickyAccountID    int64
	}
	runCase := func(t *testing.T, mode string) wsScheduleCapture {
		t.Helper()

		cfg := &config.Config{RunMode: config.RunModeSimple}
		cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
		cfg.Gateway.OpenAIWS.IngressModeDefault = mode
		billingSvc := service.NewBillingCacheService(nil, nil, nil, cfg)
		t.Cleanup(func() {
			billingSvc.Stop()
		})

		cache := &concurrencyCacheMock{
			acquireUserSlotFn: func(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error) {
				return true, nil
			},
			acquireAccountSlotFn: func(ctx context.Context, accountID int64, maxConcurrency int, requestID string) (bool, error) {
				return true, nil
			},
		}
		h := &OpenAIGatewayHandler{
			gatewayService:      &service.OpenAIGatewayService{},
			billingCacheService: billingSvc,
			apiKeyService:       &service.APIKeyService{},
			concurrencyHelper:   NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second),
			cfg:                 cfg,
		}

		account := &service.Account{
			ID:          901,
			Name:        "ws-mode-test",
			Platform:    service.PlatformOpenAI,
			Type:        service.AccountTypeAPIKey,
			Status:      service.StatusActive,
			Schedulable: true,
			Concurrency: 1,
			Credentials: map[string]any{"api_key": "sk-test"},
			Extra: map[string]any{
				"openai_apikey_responses_websockets_v2_mode": mode,
			},
		}

		var capture wsScheduleCapture
		h.wsSelectAccountWithSchedulerFn = func(
			ctx context.Context,
			groupID *int64,
			previousResponseID string,
			sessionHash string,
			requestedModel string,
			excludedIDs map[int64]struct{},
			requiredTransport service.OpenAIUpstreamTransport,
		) (*service.AccountSelectionResult, service.OpenAIAccountScheduleDecision, error) {
			if groupID != nil {
				capture.groupID = *groupID
			}
			capture.previousResponseID = previousResponseID
			capture.sessionHash = sessionHash
			capture.model = requestedModel
			capture.transport = requiredTransport
			return &service.AccountSelectionResult{
				Account:     account,
				Acquired:    true,
				ReleaseFunc: func() {},
			}, service.OpenAIAccountScheduleDecision{Layer: "unit"}, nil
		}
		h.wsBindStickySessionFn = func(ctx context.Context, groupID *int64, sessionHash string, accountID int64) error {
			capture.stickyBindCount++
			if groupID != nil {
				capture.stickyGroupID = *groupID
			}
			capture.stickySessionHash = sessionHash
			capture.stickyAccountID = accountID
			return nil
		}
		h.wsGetAccessTokenFn = func(ctx context.Context, account *service.Account) (string, string, error) {
			return "sk-test", "apikey", nil
		}
		h.wsProxyResponsesWSFn = func(
			ctx context.Context,
			c *gin.Context,
			clientConn *coderws.Conn,
			account *service.Account,
			token string,
			firstClientMessageType coderws.MessageType,
			firstClientMessage []byte,
			hooks *service.OpenAIWSIngressHooks,
		) error {
			if hooks != nil && hooks.AfterTurn != nil {
				hooks.AfterTurn(1, nil, nil)
			}
			return nil
		}

		wsServer := newOpenAIWSHandlerTestServer(t, h, middleware.AuthSubject{UserID: 1, Concurrency: 1})
		t.Cleanup(wsServer.Close)

		dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
		clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http")+"/openai/v1/responses", &coderws.DialOptions{
			HTTPHeader: http.Header{
				"Session_ID": []string{"session-fixed-123"},
			},
		})
		cancelDial()
		require.NoError(t, err)
		defer func() {
			_ = clientConn.CloseNow()
		}()

		writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
		err = clientConn.Write(writeCtx, coderws.MessageText, []byte(
			`{"type":"response.create","model":"gpt-5.1","stream":false,"previous_response_id":"resp_prev_fixed"}`,
		))
		cancelWrite()
		require.NoError(t, err)

		readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
		_, _, _ = clientConn.Read(readCtx)
		cancelRead()
		return capture
	}

	passthroughCapture := runCase(t, service.OpenAIWSIngressModePassthrough)
	ctxPoolCapture := runCase(t, service.OpenAIWSIngressModeCtxPool)

	require.Equal(t, passthroughCapture.groupID, ctxPoolCapture.groupID)
	require.Equal(t, passthroughCapture.previousResponseID, ctxPoolCapture.previousResponseID)
	require.Equal(t, passthroughCapture.sessionHash, ctxPoolCapture.sessionHash)
	require.Equal(t, passthroughCapture.model, ctxPoolCapture.model)
	require.Equal(t, service.OpenAIUpstreamTransportResponsesWebsocketV2, passthroughCapture.transport)
	require.Equal(t, passthroughCapture.transport, ctxPoolCapture.transport)

	require.Equal(t, 1, passthroughCapture.stickyBindCount)
	require.Equal(t, 1, ctxPoolCapture.stickyBindCount)
	require.Equal(t, passthroughCapture.stickyGroupID, ctxPoolCapture.stickyGroupID)
	require.Equal(t, passthroughCapture.stickySessionHash, ctxPoolCapture.stickySessionHash)
	require.Equal(t, passthroughCapture.stickyAccountID, ctxPoolCapture.stickyAccountID)
}

func TestOpenAIResponsesWebSocket_PassthroughBeforeTurnBillingCheckOnSecondTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{RunMode: config.RunModeStandard}
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = service.OpenAIWSIngressModePassthrough

	billingCache := &billingCacheBalanceSequenceMock{
		balances: []float64{10, 0},
	}
	billingSvc := service.NewBillingCacheService(billingCache, nil, nil, cfg)
	t.Cleanup(func() {
		billingSvc.Stop()
	})

	cache := &concurrencyCacheMock{
		acquireUserSlotFn: func(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error) {
			return true, nil
		},
		acquireAccountSlotFn: func(ctx context.Context, accountID int64, maxConcurrency int, requestID string) (bool, error) {
			return true, nil
		},
	}

	h := &OpenAIGatewayHandler{
		gatewayService:      &service.OpenAIGatewayService{},
		billingCacheService: billingSvc,
		apiKeyService:       &service.APIKeyService{},
		concurrencyHelper:   NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second),
		cfg:                 cfg,
	}

	account := &service.Account{
		ID:          502,
		Name:        "passthrough-before-turn",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test"},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_mode": service.OpenAIWSIngressModePassthrough,
		},
	}
	h.wsSelectAccountWithSchedulerFn = func(
		ctx context.Context,
		groupID *int64,
		previousResponseID string,
		sessionHash string,
		requestedModel string,
		excludedIDs map[int64]struct{},
		requiredTransport service.OpenAIUpstreamTransport,
	) (*service.AccountSelectionResult, service.OpenAIAccountScheduleDecision, error) {
		return &service.AccountSelectionResult{
			Account:     account,
			Acquired:    true,
			ReleaseFunc: func() {},
		}, service.OpenAIAccountScheduleDecision{Layer: "unit"}, nil
	}
	h.wsBindStickySessionFn = func(ctx context.Context, groupID *int64, sessionHash string, accountID int64) error {
		return nil
	}
	h.wsGetAccessTokenFn = func(ctx context.Context, account *service.Account) (string, string, error) {
		return "sk-test", "apikey", nil
	}
	h.wsProxyResponsesWSFn = func(
		ctx context.Context,
		c *gin.Context,
		clientConn *coderws.Conn,
		account *service.Account,
		token string,
		firstClientMessageType coderws.MessageType,
		firstClientMessage []byte,
		hooks *service.OpenAIWSIngressHooks,
	) error {
		require.NotNil(t, hooks)
		require.NotNil(t, hooks.BeforeTurn)
		require.NoError(t, hooks.BeforeTurn(1))
		return hooks.BeforeTurn(2)
	}

	wsServer := newOpenAIWSHandlerTestServer(t, h, middleware.AuthSubject{UserID: 1, Concurrency: 1})
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http")+"/openai/v1/responses", nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(
		`{"type":"response.create","model":"gpt-5.1","stream":false,"input":[]}`,
	))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, _, err = clientConn.Read(readCtx)
	cancelRead()
	require.Error(t, err)

	var closeErr coderws.CloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, coderws.StatusPolicyViolation, closeErr.Code)
	require.Contains(t, strings.ToLower(closeErr.Reason), "billing check failed")
	require.Equal(t, int32(2), billingCache.balanceCalls.Load())
}

func TestOpenAIResponsesWebSocket_PassthroughBeforeTurnConcurrencyCheckOnSecondTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{RunMode: config.RunModeStandard}
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = service.OpenAIWSIngressModePassthrough

	type tc struct {
		name             string
		userAcquireFn    func(call int) (bool, error)
		accountAcquireFn func(call int) (bool, error)
		expectedCode     coderws.StatusCode
		expectedReason   string
	}
	cases := []tc{
		{
			name: "user slot exhausted on second turn",
			userAcquireFn: func(call int) (bool, error) {
				if call == 1 {
					return true, nil
				}
				return false, nil
			},
			accountAcquireFn: func(call int) (bool, error) {
				return true, nil
			},
			expectedCode:   coderws.StatusTryAgainLater,
			expectedReason: "too many concurrent requests",
		},
		{
			name: "user slot acquire error on second turn",
			userAcquireFn: func(call int) (bool, error) {
				if call == 1 {
					return true, nil
				}
				return false, errors.New("acquire user failed")
			},
			accountAcquireFn: func(call int) (bool, error) {
				return true, nil
			},
			expectedCode:   coderws.StatusInternalError,
			expectedReason: "failed to acquire user concurrency slot",
		},
		{
			name: "account slot exhausted on second turn",
			userAcquireFn: func(call int) (bool, error) {
				return true, nil
			},
			accountAcquireFn: func(call int) (bool, error) {
				return false, nil
			},
			expectedCode:   coderws.StatusTryAgainLater,
			expectedReason: "account is busy",
		},
		{
			name: "account slot acquire error on second turn",
			userAcquireFn: func(call int) (bool, error) {
				return true, nil
			},
			accountAcquireFn: func(call int) (bool, error) {
				return false, errors.New("acquire account failed")
			},
			expectedCode:   coderws.StatusInternalError,
			expectedReason: "failed to acquire account concurrency slot",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			billingCache := &billingCacheBalanceSequenceMock{
				balances: []float64{10, 10},
			}
			billingSvc := service.NewBillingCacheService(billingCache, nil, nil, cfg)
			t.Cleanup(func() {
				billingSvc.Stop()
			})

			var userAcquireCalls atomic.Int32
			var accountAcquireCalls atomic.Int32
			cache := &concurrencyCacheMock{
				acquireUserSlotFn: func(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error) {
					call := int(userAcquireCalls.Add(1))
					if tt.userAcquireFn == nil {
						return true, nil
					}
					return tt.userAcquireFn(call)
				},
				acquireAccountSlotFn: func(ctx context.Context, accountID int64, maxConcurrency int, requestID string) (bool, error) {
					call := int(accountAcquireCalls.Add(1))
					if tt.accountAcquireFn == nil {
						return true, nil
					}
					return tt.accountAcquireFn(call)
				},
			}

			h := &OpenAIGatewayHandler{
				gatewayService:      &service.OpenAIGatewayService{},
				billingCacheService: billingSvc,
				apiKeyService:       &service.APIKeyService{},
				concurrencyHelper:   NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second),
				cfg:                 cfg,
			}
			var beforeTurnErr error

			account := &service.Account{
				ID:          503,
				Name:        "passthrough-before-turn-concurrency",
				Platform:    service.PlatformOpenAI,
				Type:        service.AccountTypeAPIKey,
				Status:      service.StatusActive,
				Schedulable: true,
				Concurrency: 1,
				Credentials: map[string]any{"api_key": "sk-test"},
				Extra: map[string]any{
					"openai_apikey_responses_websockets_v2_mode": service.OpenAIWSIngressModePassthrough,
				},
			}
			h.wsSelectAccountWithSchedulerFn = func(
				ctx context.Context,
				groupID *int64,
				previousResponseID string,
				sessionHash string,
				requestedModel string,
				excludedIDs map[int64]struct{},
				requiredTransport service.OpenAIUpstreamTransport,
			) (*service.AccountSelectionResult, service.OpenAIAccountScheduleDecision, error) {
				return &service.AccountSelectionResult{
					Account:     account,
					Acquired:    true,
					ReleaseFunc: func() {},
				}, service.OpenAIAccountScheduleDecision{Layer: "unit"}, nil
			}
			h.wsBindStickySessionFn = func(ctx context.Context, groupID *int64, sessionHash string, accountID int64) error {
				return nil
			}
			h.wsGetAccessTokenFn = func(ctx context.Context, account *service.Account) (string, string, error) {
				return "sk-test", "apikey", nil
			}
			h.wsProxyResponsesWSFn = func(
				ctx context.Context,
				c *gin.Context,
				clientConn *coderws.Conn,
				account *service.Account,
				token string,
				firstClientMessageType coderws.MessageType,
				firstClientMessage []byte,
				hooks *service.OpenAIWSIngressHooks,
			) error {
				require.NotNil(t, hooks)
				require.NotNil(t, hooks.BeforeTurn)
				require.NoError(t, hooks.BeforeTurn(1))
				beforeTurnErr = hooks.BeforeTurn(2)
				return beforeTurnErr
			}

			wsServer := newOpenAIWSHandlerTestServer(t, h, middleware.AuthSubject{UserID: 1, Concurrency: 1})
			defer wsServer.Close()

			dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
			clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http")+"/openai/v1/responses", nil)
			cancelDial()
			require.NoError(t, err)
			defer func() {
				_ = clientConn.CloseNow()
			}()

			writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
			err = clientConn.Write(writeCtx, coderws.MessageText, []byte(
				`{"type":"response.create","model":"gpt-5.1","stream":false,"input":[]}`,
			))
			cancelWrite()
			require.NoError(t, err)

			readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
			_, _, err = clientConn.Read(readCtx)
			cancelRead()
			require.Error(t, err)
			require.Error(t, beforeTurnErr)

			var wsCloseErr *service.OpenAIWSClientCloseError
			require.ErrorAs(t, beforeTurnErr, &wsCloseErr)
			require.Equal(t, tt.expectedCode, wsCloseErr.StatusCode())
			require.Contains(t, strings.ToLower(wsCloseErr.Reason()), tt.expectedReason)
			require.Equal(t, int32(2), billingCache.balanceCalls.Load())
		})
	}
}

func TestOpenAIResponsesWebSocket_PassthroughAfterTurnRecordsUsageForPartialAndSuccess(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = service.OpenAIWSIngressModePassthrough

	billingCache := &billingCacheBalanceSequenceMock{
		balances: []float64{10, 10},
	}
	billingSvc := service.NewBillingCacheService(billingCache, nil, nil, cfg)
	t.Cleanup(func() {
		billingSvc.Stop()
	})

	usageRepo := &openAIWSUsageLogCreateOnlyStub{}
	gatewaySvc := service.NewOpenAIGatewayService(
		nil,
		usageRepo,
		nil,
		nil,
		nil,
		nil,
		cfg,
		nil,
		nil,
		service.NewBillingService(cfg, nil),
		nil,
		nil,
		nil,
		&service.DeferredService{},
		nil,
	)
	t.Cleanup(func() {
		gatewaySvc.CloseOpenAIWSCtxPool()
	})

	cache := &concurrencyCacheMock{
		acquireUserSlotFn: func(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error) {
			return true, nil
		},
		acquireAccountSlotFn: func(ctx context.Context, accountID int64, maxConcurrency int, requestID string) (bool, error) {
			return true, nil
		},
	}

	h := &OpenAIGatewayHandler{
		gatewayService:      gatewaySvc,
		billingCacheService: billingSvc,
		apiKeyService:       &service.APIKeyService{},
		concurrencyHelper:   NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second),
		cfg:                 cfg,
	}

	account := &service.Account{
		ID:          504,
		Name:        "passthrough-after-turn",
		Platform:    service.PlatformOpenAI,
		Type:        service.AccountTypeAPIKey,
		Status:      service.StatusActive,
		Schedulable: true,
		Concurrency: 1,
		Credentials: map[string]any{"api_key": "sk-test"},
		Extra: map[string]any{
			"openai_apikey_responses_websockets_v2_mode": service.OpenAIWSIngressModePassthrough,
		},
	}
	h.wsSelectAccountWithSchedulerFn = func(
		ctx context.Context,
		groupID *int64,
		previousResponseID string,
		sessionHash string,
		requestedModel string,
		excludedIDs map[int64]struct{},
		requiredTransport service.OpenAIUpstreamTransport,
	) (*service.AccountSelectionResult, service.OpenAIAccountScheduleDecision, error) {
		return &service.AccountSelectionResult{
			Account:     account,
			Acquired:    true,
			ReleaseFunc: func() {},
		}, service.OpenAIAccountScheduleDecision{Layer: "unit"}, nil
	}
	h.wsBindStickySessionFn = func(ctx context.Context, groupID *int64, sessionHash string, accountID int64) error {
		return nil
	}
	h.wsGetAccessTokenFn = func(ctx context.Context, account *service.Account) (string, string, error) {
		return "sk-test", "apikey", nil
	}
	h.wsProxyResponsesWSFn = func(
		ctx context.Context,
		c *gin.Context,
		clientConn *coderws.Conn,
		account *service.Account,
		token string,
		firstClientMessageType coderws.MessageType,
		firstClientMessage []byte,
		hooks *service.OpenAIWSIngressHooks,
	) error {
		require.NotNil(t, hooks)
		require.NotNil(t, hooks.BeforeTurn)
		require.NotNil(t, hooks.AfterTurn)
		require.NoError(t, hooks.BeforeTurn(1))
		require.NoError(t, hooks.BeforeTurn(2))

		partial := &service.OpenAIForwardResult{
			RequestID:     "",
			Usage:         service.OpenAIUsage{InputTokens: 1, OutputTokens: 1},
			Model:         "gpt-5.1",
			Stream:        true,
			OpenAIWSMode:  true,
			WSIngressMode: service.OpenAIWSIngressModePassthrough,
		}
		turnErr := service.WrapOpenAIWSIngressTurnErrorWithPartial(
			"client_disconnected",
			errors.New("downstream disconnected"),
			false,
			partial,
		)
		hooks.AfterTurn(2, nil, turnErr)

		firstToken := 7
		hooks.AfterTurn(3, &service.OpenAIForwardResult{
			RequestID:         "",
			Usage:             service.OpenAIUsage{InputTokens: 2, OutputTokens: 3},
			Model:             "gpt-5.1",
			Stream:            true,
			OpenAIWSMode:      true,
			WSIngressMode:     service.OpenAIWSIngressModePassthrough,
			FirstTokenMs:      &firstToken,
			TerminalEventType: "response.completed",
		}, nil)
		return nil
	}

	wsServer := newOpenAIWSHandlerTestServer(t, h, middleware.AuthSubject{UserID: 1, Concurrency: 1})
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http")+"/openai/v1/responses", nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(
		`{"type":"response.create","model":"gpt-5.1","stream":true,"input":[]}`,
	))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, _, _ = clientConn.Read(readCtx)
	cancelRead()

	require.Equal(t, int32(2), usageRepo.createCalls.Load())
}

func TestOpenAIResponsesWebSocket_PreviousResponseIDKindLoggedBeforeAcquireFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cache := &concurrencyCacheMock{
		acquireUserSlotFn: func(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error) {
			return false, errors.New("user slot unavailable")
		},
	}
	h := newOpenAIHandlerForPreviousResponseIDValidation(t, cache)
	wsServer := newOpenAIWSHandlerTestServer(t, h, middleware.AuthSubject{UserID: 1, Concurrency: 1})
	defer wsServer.Close()

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	clientConn, _, err := coderws.Dial(dialCtx, "ws"+strings.TrimPrefix(wsServer.URL, "http")+"/openai/v1/responses", nil)
	cancelDial()
	require.NoError(t, err)
	defer func() {
		_ = clientConn.CloseNow()
	}()

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), 3*time.Second)
	err = clientConn.Write(writeCtx, coderws.MessageText, []byte(
		`{"type":"response.create","model":"gpt-5.1","stream":false,"previous_response_id":"resp_prev_123"}`,
	))
	cancelWrite()
	require.NoError(t, err)

	readCtx, cancelRead := context.WithTimeout(context.Background(), 3*time.Second)
	_, _, err = clientConn.Read(readCtx)
	cancelRead()
	require.Error(t, err)
	var closeErr coderws.CloseError
	require.ErrorAs(t, err, &closeErr)
	require.Equal(t, coderws.StatusInternalError, closeErr.Code)
	require.Contains(t, strings.ToLower(closeErr.Reason), "failed to acquire user concurrency slot")
}

func TestOpenAIWSTurnScopedFallbackRequestID(t *testing.T) {
	t.Parallel()

	require.Equal(t, "req_123", openAIWSTurnScopedFallbackRequestID("req_123", 0))
	require.Equal(t, "req_123", openAIWSTurnScopedFallbackRequestID("req_123", -1))
	require.Equal(t, "req_123:turn:2", openAIWSTurnScopedFallbackRequestID("req_123", 2))
	require.Equal(t, ":turn:3", openAIWSTurnScopedFallbackRequestID("", 3))
}

func TestSetOpenAIClientTransportHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	setOpenAIClientTransportHTTP(c)
	require.Equal(t, service.OpenAIClientTransportHTTP, service.GetOpenAIClientTransport(c))
}

func TestSetOpenAIClientTransportWS(t *testing.T) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	setOpenAIClientTransportWS(c)
	require.Equal(t, service.OpenAIClientTransportWS, service.GetOpenAIClientTransport(c))
}

// TestOpenAIHandler_GjsonExtraction 验证 gjson 从请求体中提取 model/stream 的正确性
func TestOpenAIHandler_GjsonExtraction(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantModel  string
		wantStream bool
	}{
		{"正常提取", `{"model":"gpt-4","stream":true,"input":"hello"}`, "gpt-4", true},
		{"stream false", `{"model":"gpt-4","stream":false}`, "gpt-4", false},
		{"无 stream 字段", `{"model":"gpt-4"}`, "gpt-4", false},
		{"model 缺失", `{"stream":true}`, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(tt.body)
			modelResult := gjson.GetBytes(body, "model")
			model := ""
			if modelResult.Type == gjson.String {
				model = modelResult.String()
			}
			stream := gjson.GetBytes(body, "stream").Bool()
			require.Equal(t, tt.wantModel, model)
			require.Equal(t, tt.wantStream, stream)
		})
	}
}

// TestOpenAIHandler_GjsonValidation 验证修复后的 JSON 合法性和类型校验
func TestOpenAIHandler_GjsonValidation(t *testing.T) {
	// 非法 JSON 被 gjson.ValidBytes 拦截
	require.False(t, gjson.ValidBytes([]byte(`{invalid json`)))

	// model 为数字 → 类型不是 gjson.String，应被拒绝
	body := []byte(`{"model":123}`)
	modelResult := gjson.GetBytes(body, "model")
	require.True(t, modelResult.Exists())
	require.NotEqual(t, gjson.String, modelResult.Type)

	// model 为 null → 类型不是 gjson.String，应被拒绝
	body2 := []byte(`{"model":null}`)
	modelResult2 := gjson.GetBytes(body2, "model")
	require.True(t, modelResult2.Exists())
	require.NotEqual(t, gjson.String, modelResult2.Type)

	// stream 为 string → 类型既不是 True 也不是 False，应被拒绝
	body3 := []byte(`{"model":"gpt-4","stream":"true"}`)
	streamResult := gjson.GetBytes(body3, "stream")
	require.True(t, streamResult.Exists())
	require.NotEqual(t, gjson.True, streamResult.Type)
	require.NotEqual(t, gjson.False, streamResult.Type)

	// stream 为 int → 同上
	body4 := []byte(`{"model":"gpt-4","stream":1}`)
	streamResult2 := gjson.GetBytes(body4, "stream")
	require.True(t, streamResult2.Exists())
	require.NotEqual(t, gjson.True, streamResult2.Type)
	require.NotEqual(t, gjson.False, streamResult2.Type)
}

// TestOpenAIHandler_InstructionsInjection 验证 instructions 的 gjson/sjson 注入逻辑
func TestOpenAIHandler_InstructionsInjection(t *testing.T) {
	// 测试 1：无 instructions → 注入
	body := []byte(`{"model":"gpt-4"}`)
	existing := gjson.GetBytes(body, "instructions").String()
	require.Empty(t, existing)
	newBody, err := sjson.SetBytes(body, "instructions", "test instruction")
	require.NoError(t, err)
	require.Equal(t, "test instruction", gjson.GetBytes(newBody, "instructions").String())

	// 测试 2：已有 instructions → 不覆盖
	body2 := []byte(`{"model":"gpt-4","instructions":"existing"}`)
	existing2 := gjson.GetBytes(body2, "instructions").String()
	require.Equal(t, "existing", existing2)

	// 测试 3：空白 instructions → 注入
	body3 := []byte(`{"model":"gpt-4","instructions":"   "}`)
	existing3 := strings.TrimSpace(gjson.GetBytes(body3, "instructions").String())
	require.Empty(t, existing3)

	// 测试 4：sjson.SetBytes 返回错误时不应 panic
	// 正常 JSON 不会产生 sjson 错误，验证返回值被正确处理
	validBody := []byte(`{"model":"gpt-4"}`)
	result, setErr := sjson.SetBytes(validBody, "instructions", "hello")
	require.NoError(t, setErr)
	require.True(t, gjson.ValidBytes(result))
}

type billingCacheBalanceSequenceMock struct {
	balances     []float64
	balanceCalls atomic.Int32
}

type openAIWSUsageLogCreateOnlyStub struct {
	service.UsageLogRepository
	createCalls atomic.Int32
}

func (s *openAIWSUsageLogCreateOnlyStub) Create(ctx context.Context, log *service.UsageLog) (bool, error) {
	s.createCalls.Add(1)
	return false, nil
}

func (m *billingCacheBalanceSequenceMock) nextBalance() float64 {
	if len(m.balances) == 0 {
		return 0
	}
	idx := int(m.balanceCalls.Add(1)) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(m.balances) {
		return m.balances[len(m.balances)-1]
	}
	return m.balances[idx]
}

func (m *billingCacheBalanceSequenceMock) GetUserBalance(ctx context.Context, userID int64) (float64, error) {
	return m.nextBalance(), nil
}

func (m *billingCacheBalanceSequenceMock) SetUserBalance(ctx context.Context, userID int64, balance float64) error {
	return nil
}

func (m *billingCacheBalanceSequenceMock) DeductUserBalance(ctx context.Context, userID int64, amount float64) error {
	return nil
}

func (m *billingCacheBalanceSequenceMock) InvalidateUserBalance(ctx context.Context, userID int64) error {
	return nil
}

func (m *billingCacheBalanceSequenceMock) GetSubscriptionCache(ctx context.Context, userID, groupID int64) (*service.SubscriptionCacheData, error) {
	return nil, nil
}

func (m *billingCacheBalanceSequenceMock) SetSubscriptionCache(ctx context.Context, userID, groupID int64, data *service.SubscriptionCacheData) error {
	return nil
}

func (m *billingCacheBalanceSequenceMock) UpdateSubscriptionUsage(ctx context.Context, userID, groupID int64, cost float64) error {
	return nil
}

func (m *billingCacheBalanceSequenceMock) InvalidateSubscriptionCache(ctx context.Context, userID, groupID int64) error {
	return nil
}

func newOpenAIHandlerForPreviousResponseIDValidation(t *testing.T, cache *concurrencyCacheMock) *OpenAIGatewayHandler {
	t.Helper()
	if cache == nil {
		cache = &concurrencyCacheMock{
			acquireUserSlotFn: func(ctx context.Context, userID int64, maxConcurrency int, requestID string) (bool, error) {
				return true, nil
			},
			acquireAccountSlotFn: func(ctx context.Context, accountID int64, maxConcurrency int, requestID string) (bool, error) {
				return true, nil
			},
		}
	}
	return &OpenAIGatewayHandler{
		gatewayService:      &service.OpenAIGatewayService{},
		billingCacheService: &service.BillingCacheService{},
		apiKeyService:       &service.APIKeyService{},
		concurrencyHelper:   NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatNone, time.Second),
	}
}

func newOpenAIWSHandlerTestServer(t *testing.T, h *OpenAIGatewayHandler, subject middleware.AuthSubject) *httptest.Server {
	t.Helper()
	groupID := int64(2)
	apiKey := &service.APIKey{
		ID:      101,
		GroupID: &groupID,
		User:    &service.User{ID: subject.UserID},
	}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Set(string(middleware.ContextKeyAPIKey), apiKey)
		c.Set(string(middleware.ContextKeyUser), subject)
		c.Next()
	})
	router.GET("/openai/v1/responses", h.ResponsesWebSocket)
	return httptest.NewServer(router)
}
