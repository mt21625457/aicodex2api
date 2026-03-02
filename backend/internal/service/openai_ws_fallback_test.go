package service

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestClassifyOpenAIWSAcquireError(t *testing.T) {
	t.Run("dial_426_upgrade_required", func(t *testing.T) {
		err := &openAIWSDialError{StatusCode: 426, Err: errors.New("upgrade required")}
		require.Equal(t, "upgrade_required", classifyOpenAIWSAcquireError(err))
	})

	t.Run("queue_full", func(t *testing.T) {
		require.Equal(t, "conn_queue_full", classifyOpenAIWSAcquireError(errOpenAIWSConnQueueFull))
	})

	t.Run("preferred_conn_unavailable", func(t *testing.T) {
		require.Equal(t, "preferred_conn_unavailable", classifyOpenAIWSAcquireError(errOpenAIWSPreferredConnUnavailable))
	})

	t.Run("acquire_timeout", func(t *testing.T) {
		require.Equal(t, "acquire_timeout", classifyOpenAIWSAcquireError(context.DeadlineExceeded))
	})

	t.Run("auth_failed_401", func(t *testing.T) {
		err := &openAIWSDialError{StatusCode: 401, Err: errors.New("unauthorized")}
		require.Equal(t, "auth_failed", classifyOpenAIWSAcquireError(err))
	})

	t.Run("upstream_rate_limited", func(t *testing.T) {
		err := &openAIWSDialError{StatusCode: 429, Err: errors.New("rate limited")}
		require.Equal(t, "upstream_rate_limited", classifyOpenAIWSAcquireError(err))
	})

	t.Run("upstream_5xx", func(t *testing.T) {
		err := &openAIWSDialError{StatusCode: 502, Err: errors.New("bad gateway")}
		require.Equal(t, "upstream_5xx", classifyOpenAIWSAcquireError(err))
	})

	t.Run("dial_failed_other_status", func(t *testing.T) {
		err := &openAIWSDialError{StatusCode: 418, Err: errors.New("teapot")}
		require.Equal(t, "dial_failed", classifyOpenAIWSAcquireError(err))
	})

	t.Run("other", func(t *testing.T) {
		require.Equal(t, "acquire_conn", classifyOpenAIWSAcquireError(errors.New("x")))
	})

	t.Run("nil", func(t *testing.T) {
		require.Equal(t, "acquire_conn", classifyOpenAIWSAcquireError(nil))
	})
}

func TestClassifyOpenAIWSDialError(t *testing.T) {
	t.Run("handshake_not_finished", func(t *testing.T) {
		err := &openAIWSDialError{
			StatusCode: http.StatusBadGateway,
			Err:        errors.New("WebSocket protocol error: Handshake not finished"),
		}
		require.Equal(t, "handshake_not_finished", classifyOpenAIWSDialError(err))
	})

	t.Run("context_deadline", func(t *testing.T) {
		err := &openAIWSDialError{
			StatusCode: 0,
			Err:        context.DeadlineExceeded,
		}
		require.Equal(t, "ctx_deadline_exceeded", classifyOpenAIWSDialError(err))
	})
}

func TestSummarizeOpenAIWSDialError(t *testing.T) {
	err := &openAIWSDialError{
		StatusCode: http.StatusBadGateway,
		ResponseHeaders: http.Header{
			"Server":       []string{"cloudflare"},
			"Via":          []string{"1.1 example"},
			"Cf-Ray":       []string{"abcd1234"},
			"X-Request-Id": []string{"req_123"},
		},
		Err: errors.New("WebSocket protocol error: Handshake not finished"),
	}

	status, class, closeStatus, closeReason, server, via, cfRay, reqID := summarizeOpenAIWSDialError(err)
	require.Equal(t, http.StatusBadGateway, status)
	require.Equal(t, "handshake_not_finished", class)
	require.Equal(t, "-", closeStatus)
	require.Equal(t, "-", closeReason)
	require.Equal(t, "cloudflare", server)
	require.Equal(t, "1.1 example", via)
	require.Equal(t, "abcd1234", cfRay)
	require.Equal(t, "req_123", reqID)
}

func TestClassifyOpenAIWSErrorEvent(t *testing.T) {
	reason, recoverable := classifyOpenAIWSErrorEvent([]byte(`{"type":"error","error":{"code":"upgrade_required","message":"Upgrade required"}}`))
	require.Equal(t, "upgrade_required", reason)
	require.True(t, recoverable)

	reason, recoverable = classifyOpenAIWSErrorEvent([]byte(`{"type":"error","error":{"code":"previous_response_not_found","message":"not found"}}`))
	require.Equal(t, "previous_response_not_found", reason)
	require.True(t, recoverable)

	// tool_output_not_found: 用户按 ESC 取消 function_call 后重新发送消息
	reason, recoverable = classifyOpenAIWSErrorEvent([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"No tool output found for function call call_zXKPiNecBmIAoKeW9o2pNMvo.","param":"input"}}`))
	require.Equal(t, openAIWSIngressStageToolOutputNotFound, reason)
	require.True(t, recoverable)

	reason, recoverable = classifyOpenAIWSErrorEventFromRaw("", "invalid_request_error", "No tool output found for function call call_abc123.")
	require.Equal(t, openAIWSIngressStageToolOutputNotFound, reason)
	require.True(t, recoverable)

	reason, recoverable = classifyOpenAIWSErrorEventFromRaw(
		"",
		"invalid_request_error",
		"No tool call found for function call output with call_id call_abc123.",
	)
	require.Equal(t, openAIWSIngressStageToolOutputNotFound, reason)
	require.True(t, recoverable)

	// reasoning orphaned items should reuse tool_output_not_found recovery path.
	reason, recoverable = classifyOpenAIWSErrorEventFromRaw(
		"",
		"invalid_request_error",
		"Item 'rs_xxx' of type 'reasoning' was provided without its required following item.",
	)
	require.Equal(t, openAIWSIngressStageToolOutputNotFound, reason)
	require.True(t, recoverable)

	reason, recoverable = classifyOpenAIWSErrorEventFromRaw(
		"",
		"invalid_request_error",
		"Item 'rs_xxx' of type 'reasoning' was provided without its required preceding item.",
	)
	require.Equal(t, openAIWSIngressStageToolOutputNotFound, reason)
	require.True(t, recoverable)
}

func TestClassifyOpenAIWSErrorEventFromRaw_AllBranches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		codeRaw     string
		errTypeRaw  string
		msgRaw      string
		wantReason  string
		wantRecover bool
	}{
		{
			name:        "code_upgrade_required",
			codeRaw:     "upgrade_required",
			wantReason:  "upgrade_required",
			wantRecover: true,
		},
		{
			name:        "code_ws_unsupported",
			codeRaw:     "websocket_not_supported",
			wantReason:  "ws_unsupported",
			wantRecover: true,
		},
		{
			name:        "code_ws_connection_limit",
			codeRaw:     "websocket_connection_limit_reached",
			wantReason:  "ws_connection_limit_reached",
			wantRecover: true,
		},
		{
			name:        "msg_upgrade_required",
			msgRaw:      "status 426 upgrade required",
			wantReason:  "upgrade_required",
			wantRecover: true,
		},
		{
			name:        "err_type_upgrade",
			errTypeRaw:  "gateway_upgrade_error",
			wantReason:  "upgrade_required",
			wantRecover: true,
		},
		{
			name:        "msg_ws_unsupported",
			msgRaw:      "websocket is unsupported in this region",
			wantReason:  "ws_unsupported",
			wantRecover: true,
		},
		{
			name:        "msg_ws_connection_limit",
			msgRaw:      "websocket connection limit exceeded",
			wantReason:  "ws_connection_limit_reached",
			wantRecover: true,
		},
		{
			name:        "msg_previous_response_not_found_variant",
			msgRaw:      "previous response is not found",
			wantReason:  "previous_response_not_found",
			wantRecover: true,
		},
		{
			name:        "msg_no_tool_output",
			msgRaw:      "No tool output found for function call call_abc.",
			wantReason:  openAIWSIngressStageToolOutputNotFound,
			wantRecover: true,
		},
		{
			name:        "msg_no_tool_call_for_function_call_output",
			msgRaw:      "No tool call found for function call output with call_id call_abc.",
			wantReason:  openAIWSIngressStageToolOutputNotFound,
			wantRecover: true,
		},
		{
			name:        "msg_reasoning_missing_following",
			msgRaw:      "Item 'rs_xxx' of type 'reasoning' was provided without its required following item.",
			wantReason:  openAIWSIngressStageToolOutputNotFound,
			wantRecover: true,
		},
		{
			name:        "msg_reasoning_missing_preceding",
			msgRaw:      "Item 'rs_xxx' of type 'reasoning' was provided without its required preceding item.",
			wantReason:  openAIWSIngressStageToolOutputNotFound,
			wantRecover: true,
		},
		{
			name:        "server_error_by_type",
			errTypeRaw:  "server_error",
			wantReason:  "upstream_error_event",
			wantRecover: true,
		},
		{
			name:        "server_error_by_code",
			codeRaw:     "server_error",
			wantReason:  "upstream_error_event",
			wantRecover: true,
		},
		{
			name:        "unknown_event_error",
			codeRaw:     "other",
			errTypeRaw:  "other",
			msgRaw:      "other",
			wantReason:  "event_error",
			wantRecover: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reason, recoverable := classifyOpenAIWSErrorEventFromRaw(tt.codeRaw, tt.errTypeRaw, tt.msgRaw)
			require.Equal(t, tt.wantReason, reason)
			require.Equal(t, tt.wantRecover, recoverable)
		})
	}
}

func TestClassifyOpenAIWSReconnectReason(t *testing.T) {
	reason, retryable := classifyOpenAIWSReconnectReason(wrapOpenAIWSFallback("policy_violation", errors.New("policy")))
	require.Equal(t, "policy_violation", reason)
	require.False(t, retryable)

	reason, retryable = classifyOpenAIWSReconnectReason(wrapOpenAIWSFallback("read_event", errors.New("io")))
	require.Equal(t, "read_event", reason)
	require.True(t, retryable)
}

func TestOpenAIWSErrorHTTPStatus(t *testing.T) {
	require.Equal(t, http.StatusBadRequest, openAIWSErrorHTTPStatus([]byte(`{"type":"error","error":{"type":"invalid_request_error","code":"invalid_request","message":"invalid input"}}`)))
	require.Equal(t, http.StatusUnauthorized, openAIWSErrorHTTPStatus([]byte(`{"type":"error","error":{"type":"authentication_error","code":"invalid_api_key","message":"auth failed"}}`)))
	require.Equal(t, http.StatusForbidden, openAIWSErrorHTTPStatus([]byte(`{"type":"error","error":{"type":"permission_error","code":"forbidden","message":"forbidden"}}`)))
	require.Equal(t, http.StatusTooManyRequests, openAIWSErrorHTTPStatus([]byte(`{"type":"error","error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"rate limited"}}`)))
	require.Equal(t, http.StatusBadGateway, openAIWSErrorHTTPStatus([]byte(`{"type":"error","error":{"type":"server_error","code":"server_error","message":"server"}}`)))
}

func TestResolveOpenAIWSFallbackErrorResponse(t *testing.T) {
	t.Run("previous_response_not_found", func(t *testing.T) {
		statusCode, errType, clientMessage, upstreamMessage, ok := resolveOpenAIWSFallbackErrorResponse(
			wrapOpenAIWSFallback("previous_response_not_found", errors.New("previous response not found")),
		)
		require.True(t, ok)
		require.Equal(t, http.StatusBadRequest, statusCode)
		require.Equal(t, "invalid_request_error", errType)
		require.Equal(t, "previous response not found", clientMessage)
		require.Equal(t, "previous response not found", upstreamMessage)
	})

	t.Run("auth_failed_uses_dial_status", func(t *testing.T) {
		statusCode, errType, clientMessage, upstreamMessage, ok := resolveOpenAIWSFallbackErrorResponse(
			wrapOpenAIWSFallback("auth_failed", &openAIWSDialError{
				StatusCode: http.StatusForbidden,
				Err:        errors.New("forbidden"),
			}),
		)
		require.True(t, ok)
		require.Equal(t, http.StatusForbidden, statusCode)
		require.Equal(t, "upstream_error", errType)
		require.Equal(t, "forbidden", clientMessage)
		require.Equal(t, "forbidden", upstreamMessage)
	})

	t.Run("non_fallback_error_not_resolved", func(t *testing.T) {
		_, _, _, _, ok := resolveOpenAIWSFallbackErrorResponse(errors.New("plain error"))
		require.False(t, ok)
	})
}

func TestOpenAIWSFallbackCooling(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	svc.cfg.Gateway.OpenAIWS.FallbackCooldownSeconds = 1

	require.False(t, svc.isOpenAIWSFallbackCooling(1))
	svc.markOpenAIWSFallbackCooling(1, "upgrade_required")
	require.True(t, svc.isOpenAIWSFallbackCooling(1))

	svc.clearOpenAIWSFallbackCooling(1)
	require.False(t, svc.isOpenAIWSFallbackCooling(1))

	svc.markOpenAIWSFallbackCooling(2, "x")
	time.Sleep(1200 * time.Millisecond)
	require.False(t, svc.isOpenAIWSFallbackCooling(2))
}

func TestOpenAIWSRetryBackoff(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	svc.cfg.Gateway.OpenAIWS.RetryBackoffInitialMS = 100
	svc.cfg.Gateway.OpenAIWS.RetryBackoffMaxMS = 400
	svc.cfg.Gateway.OpenAIWS.RetryJitterRatio = 0

	require.Equal(t, time.Duration(100)*time.Millisecond, svc.openAIWSRetryBackoff(1))
	require.Equal(t, time.Duration(200)*time.Millisecond, svc.openAIWSRetryBackoff(2))
	require.Equal(t, time.Duration(400)*time.Millisecond, svc.openAIWSRetryBackoff(3))
	require.Equal(t, time.Duration(400)*time.Millisecond, svc.openAIWSRetryBackoff(4))
}

func TestOpenAIWSRetryTotalBudget(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	svc.cfg.Gateway.OpenAIWS.RetryTotalBudgetMS = 1200
	require.Equal(t, 1200*time.Millisecond, svc.openAIWSRetryTotalBudget())

	svc.cfg.Gateway.OpenAIWS.RetryTotalBudgetMS = 0
	require.Equal(t, time.Duration(0), svc.openAIWSRetryTotalBudget())
}

func TestOpenAIWSRetryContextError(t *testing.T) {
	require.NoError(t, openAIWSRetryContextError(nil))
	require.NoError(t, openAIWSRetryContextError(context.Background()))

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	err := openAIWSRetryContextError(canceledCtx)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)

	var fallbackErr *openAIWSFallbackError
	require.ErrorAs(t, err, &fallbackErr)
	require.Equal(t, "retry_context_canceled", fallbackErr.Reason)
}

func TestClassifyOpenAIWSReadFallbackReason(t *testing.T) {
	require.Equal(t, "service_restart", classifyOpenAIWSReadFallbackReason(coderws.CloseError{Code: coderws.StatusServiceRestart}))
	require.Equal(t, "try_again_later", classifyOpenAIWSReadFallbackReason(coderws.CloseError{Code: coderws.StatusTryAgainLater}))
	require.Equal(t, "policy_violation", classifyOpenAIWSReadFallbackReason(coderws.CloseError{Code: coderws.StatusPolicyViolation}))
	require.Equal(t, "message_too_big", classifyOpenAIWSReadFallbackReason(coderws.CloseError{Code: coderws.StatusMessageTooBig}))
	require.Equal(t, "read_event", classifyOpenAIWSReadFallbackReason(errors.New("io")))
}

func TestClassifyOpenAIWSIngressReadErrorClass(t *testing.T) {
	require.Equal(t, "unknown", classifyOpenAIWSIngressReadErrorClass(nil))
	require.Equal(t, "context_canceled", classifyOpenAIWSIngressReadErrorClass(context.Canceled))
	require.Equal(t, "deadline_exceeded", classifyOpenAIWSIngressReadErrorClass(context.DeadlineExceeded))
	require.Equal(t, "service_restart", classifyOpenAIWSIngressReadErrorClass(coderws.CloseError{Code: coderws.StatusServiceRestart}))
	require.Equal(t, "try_again_later", classifyOpenAIWSIngressReadErrorClass(coderws.CloseError{Code: coderws.StatusTryAgainLater}))
	require.Equal(t, "upstream_closed", classifyOpenAIWSIngressReadErrorClass(io.EOF))
	require.Equal(t, "unknown", classifyOpenAIWSIngressReadErrorClass(errors.New("tls handshake timeout")))
}

func TestOpenAIWSStoreDisabledConnMode(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	svc.cfg.Gateway.OpenAIWS.StoreDisabledForceNewConn = true
	require.Equal(t, openAIWSStoreDisabledConnModeStrict, svc.openAIWSStoreDisabledConnMode())

	svc.cfg.Gateway.OpenAIWS.StoreDisabledConnMode = "adaptive"
	require.Equal(t, openAIWSStoreDisabledConnModeAdaptive, svc.openAIWSStoreDisabledConnMode())

	svc.cfg.Gateway.OpenAIWS.StoreDisabledConnMode = ""
	svc.cfg.Gateway.OpenAIWS.StoreDisabledForceNewConn = false
	require.Equal(t, openAIWSStoreDisabledConnModeOff, svc.openAIWSStoreDisabledConnMode())
}

func TestShouldForceNewConnOnStoreDisabled(t *testing.T) {
	require.True(t, shouldForceNewConnOnStoreDisabled(openAIWSStoreDisabledConnModeStrict, ""))
	require.False(t, shouldForceNewConnOnStoreDisabled(openAIWSStoreDisabledConnModeOff, "policy_violation"))

	require.True(t, shouldForceNewConnOnStoreDisabled(openAIWSStoreDisabledConnModeAdaptive, "policy_violation"))
	require.True(t, shouldForceNewConnOnStoreDisabled(openAIWSStoreDisabledConnModeAdaptive, "prewarm_message_too_big"))
	require.False(t, shouldForceNewConnOnStoreDisabled(openAIWSStoreDisabledConnModeAdaptive, "read_event"))
}

func TestOpenAIWSRetryMetricsSnapshot(t *testing.T) {
	svc := &OpenAIGatewayService{}
	svc.recordOpenAIWSRetryAttempt(150 * time.Millisecond)
	svc.recordOpenAIWSRetryAttempt(0)
	svc.recordOpenAIWSRetryExhausted()
	svc.recordOpenAIWSNonRetryableFastFallback()

	snapshot := svc.SnapshotOpenAIWSRetryMetrics()
	require.Equal(t, int64(2), snapshot.RetryAttemptsTotal)
	require.Equal(t, int64(150), snapshot.RetryBackoffMsTotal)
	require.Equal(t, int64(1), snapshot.RetryExhaustedTotal)
	require.Equal(t, int64(1), snapshot.NonRetryableFastFallbackTotal)
}

func TestWriteOpenAIWSV1UnsupportedResponse_TracksOps(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	svc := &OpenAIGatewayService{}
	account := &Account{
		ID:       42,
		Name:     "acc-ws-v1",
		Platform: PlatformOpenAI,
	}

	err := svc.writeOpenAIWSV1UnsupportedResponse(c, account)
	require.Error(t, err)
	require.Contains(t, err.Error(), "openai ws v1 is temporarily unsupported")

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "invalid_request_error")
	require.Contains(t, rec.Body.String(), "temporarily unsupported")

	rawStatus, ok := c.Get(OpsUpstreamStatusCodeKey)
	require.True(t, ok)
	require.Equal(t, http.StatusBadRequest, rawStatus)

	rawMsg, ok := c.Get(OpsUpstreamErrorMessageKey)
	require.True(t, ok)
	require.Equal(t, "openai ws v1 is temporarily unsupported; use ws v2", rawMsg)

	rawEvents, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok)
	events, ok := rawEvents.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, account.ID, events[0].AccountID)
	require.Equal(t, account.Platform, events[0].Platform)
	require.Equal(t, http.StatusBadRequest, events[0].UpstreamStatusCode)
	require.Equal(t, "ws_error", events[0].Kind)
}

func TestIsOpenAIWSStreamWriteDisconnectError(t *testing.T) {
	require.False(t, isOpenAIWSStreamWriteDisconnectError(nil, nil))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.True(t, isOpenAIWSStreamWriteDisconnectError(errors.New("writer failed"), ctx))

	require.True(t, isOpenAIWSStreamWriteDisconnectError(errors.New("broken pipe"), context.Background()))
	require.True(t, isOpenAIWSStreamWriteDisconnectError(io.EOF, context.Background()))

	require.False(t, isOpenAIWSStreamWriteDisconnectError(errors.New("template execute failed"), context.Background()))
}

func TestShouldFlushOpenAIWSBufferedEventsOnError(t *testing.T) {
	require.True(t, shouldFlushOpenAIWSBufferedEventsOnError(true, true, false))
	require.False(t, shouldFlushOpenAIWSBufferedEventsOnError(true, false, false))
	require.False(t, shouldFlushOpenAIWSBufferedEventsOnError(true, true, true))
	require.False(t, shouldFlushOpenAIWSBufferedEventsOnError(false, true, false))
}

func TestCloneOpenAIWSJSONRawString(t *testing.T) {
	require.Nil(t, cloneOpenAIWSJSONRawString(""))
	require.Nil(t, cloneOpenAIWSJSONRawString("   "))

	raw := `{"id":"resp_1","type":"response"}`
	cloned := cloneOpenAIWSJSONRawString(raw)
	require.Equal(t, raw, string(cloned))
	require.Equal(t, len(raw), len(cloned))
}

func TestOpenAIWSAbortMetricsSnapshot(t *testing.T) {
	svc := &OpenAIGatewayService{}
	svc.recordOpenAIWSTurnAbort(openAIWSIngressTurnAbortReasonUpstreamError, true)
	svc.recordOpenAIWSTurnAbort(openAIWSIngressTurnAbortReasonUpstreamError, true)
	svc.recordOpenAIWSTurnAbort(openAIWSIngressTurnAbortReasonWriteUpstream, false)
	svc.recordOpenAIWSTurnAbortRecovered()

	snapshot := svc.SnapshotOpenAIWSAbortMetrics()
	require.Equal(t, int64(1), snapshot.TurnAbortRecoveredTotal)

	getTotal := func(reason string, expected bool) int64 {
		for _, point := range snapshot.TurnAbortTotal {
			if point.Reason == reason && point.Expected == expected {
				return point.Total
			}
		}
		return 0
	}
	require.Equal(t, int64(2), getTotal(string(openAIWSIngressTurnAbortReasonUpstreamError), true))
	require.Equal(t, int64(1), getTotal(string(openAIWSIngressTurnAbortReasonWriteUpstream), false))
}

func TestOpenAIWSPerformanceMetricsSnapshot_ContainsAbortMetrics(t *testing.T) {
	svc := &OpenAIGatewayService{}
	svc.recordOpenAIWSTurnAbort(openAIWSIngressTurnAbortReasonClientClosed, true)
	svc.recordOpenAIWSTurnAbortRecovered()

	snapshot := svc.SnapshotOpenAIWSPerformanceMetrics()
	require.Equal(t, int64(1), snapshot.Abort.TurnAbortRecoveredTotal)

	found := false
	for _, point := range snapshot.Abort.TurnAbortTotal {
		if point.Reason == string(openAIWSIngressTurnAbortReasonClientClosed) && point.Expected {
			require.Equal(t, int64(1), point.Total)
			found = true
			break
		}
	}
	require.True(t, found)
}

func TestShouldLogOpenAIWSPayloadSchema(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}}

	svc.cfg.Gateway.OpenAIWS.PayloadLogSampleRate = 0
	require.True(t, svc.shouldLogOpenAIWSPayloadSchema(1), "首次尝试应始终记录 payload_schema")
	require.False(t, svc.shouldLogOpenAIWSPayloadSchema(2))

	svc.cfg.Gateway.OpenAIWS.PayloadLogSampleRate = 1
	require.True(t, svc.shouldLogOpenAIWSPayloadSchema(2))
}
