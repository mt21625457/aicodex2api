package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestIsOpenAIWSClientDisconnectError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "io_eof", err: io.EOF, want: true},
		{name: "net_closed", err: net.ErrClosed, want: true},
		{name: "context_canceled", err: context.Canceled, want: true},
		{name: "ws_normal_closure", err: coderws.CloseError{Code: coderws.StatusNormalClosure}, want: true},
		{name: "ws_going_away", err: coderws.CloseError{Code: coderws.StatusGoingAway}, want: true},
		{name: "ws_no_status", err: coderws.CloseError{Code: coderws.StatusNoStatusRcvd}, want: true},
		{name: "ws_abnormal_1006", err: coderws.CloseError{Code: coderws.StatusAbnormalClosure}, want: true},
		{name: "ws_policy_violation", err: coderws.CloseError{Code: coderws.StatusPolicyViolation}, want: false},
		{name: "wrapped_eof_message", err: errors.New("failed to get reader: failed to read frame header: EOF"), want: true},
		{name: "connection_reset_by_peer", err: errors.New("failed to read frame header: read tcp 127.0.0.1:1234->127.0.0.1:5678: read: connection reset by peer"), want: true},
		{name: "broken_pipe", err: errors.New("write tcp 127.0.0.1:1234->127.0.0.1:5678: write: broken pipe"), want: true},
		{name: "blank_message", err: errors.New("   "), want: false},
		{name: "unmatched_message", err: errors.New("tls handshake timeout"), want: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, isOpenAIWSClientDisconnectError(tt.err))
		})
	}
}

func TestOpenAIWSIngressFallbackSessionSeedFromContext(t *testing.T) {
	t.Parallel()

	require.Empty(t, openAIWSIngressFallbackSessionSeedFromContext(nil))

	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	require.Empty(t, openAIWSIngressFallbackSessionSeedFromContext(c))

	c.Set("api_key", "not_api_key")
	require.Empty(t, openAIWSIngressFallbackSessionSeedFromContext(c))

	groupID := int64(99)
	c.Set("api_key", &APIKey{
		ID:      101,
		GroupID: &groupID,
		User:    &User{ID: 202},
	})
	require.Equal(t, "openai_ws_ingress:99:202:101", openAIWSIngressFallbackSessionSeedFromContext(c))

	c.Set("api_key", &APIKey{
		ID:   303,
		User: nil,
	})
	require.Equal(t, "openai_ws_ingress:0:0:303", openAIWSIngressFallbackSessionSeedFromContext(c))
}

func TestClassifyOpenAIWSIngressTurnAbortReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		err          error
		wantReason   openAIWSIngressTurnAbortReason
		wantExpected bool
	}{
		{
			name:         "nil",
			err:          nil,
			wantReason:   openAIWSIngressTurnAbortReasonUnknown,
			wantExpected: false,
		},
		{
			name:         "context canceled",
			err:          context.Canceled,
			wantReason:   openAIWSIngressTurnAbortReasonContextCanceled,
			wantExpected: true,
		},
		{
			name:         "context deadline",
			err:          context.DeadlineExceeded,
			wantReason:   openAIWSIngressTurnAbortReasonContextDeadline,
			wantExpected: false,
		},
		{
			name:         "client close",
			err:          coderws.CloseError{Code: coderws.StatusNormalClosure},
			wantReason:   openAIWSIngressTurnAbortReasonClientClosed,
			wantExpected: true,
		},
		{
			name:         "client close by eof",
			err:          io.EOF,
			wantReason:   openAIWSIngressTurnAbortReasonClientClosed,
			wantExpected: true,
		},
		{
			name: "previous response not found",
			err: wrapOpenAIWSIngressTurnError(
				openAIWSIngressStagePreviousResponseNotFound,
				errors.New("previous response not found"),
				false,
			),
			wantReason:   openAIWSIngressTurnAbortReasonPreviousResponse,
			wantExpected: true,
		},
		{
			name: "tool output not found",
			err: wrapOpenAIWSIngressTurnError(
				openAIWSIngressStageToolOutputNotFound,
				errors.New("no tool output found"),
				false,
			),
			wantReason:   openAIWSIngressTurnAbortReasonToolOutput,
			wantExpected: true,
		},
		{
			name: "upstream error event",
			err: wrapOpenAIWSIngressTurnError(
				"upstream_error_event",
				errors.New("upstream error event"),
				false,
			),
			wantReason:   openAIWSIngressTurnAbortReasonUpstreamError,
			wantExpected: true,
		},
		{
			name: "write upstream",
			err: wrapOpenAIWSIngressTurnError(
				"write_upstream",
				errors.New("write upstream fail"),
				false,
			),
			wantReason:   openAIWSIngressTurnAbortReasonWriteUpstream,
			wantExpected: false,
		},
		{
			name: "read upstream",
			err: wrapOpenAIWSIngressTurnError(
				"read_upstream",
				errors.New("read upstream fail"),
				false,
			),
			wantReason:   openAIWSIngressTurnAbortReasonReadUpstream,
			wantExpected: false,
		},
		{
			name: "write client",
			err: wrapOpenAIWSIngressTurnError(
				"write_client",
				errors.New("write client fail"),
				true,
			),
			wantReason:   openAIWSIngressTurnAbortReasonWriteClient,
			wantExpected: false,
		},
		{
			name: "unknown turn stage",
			err: wrapOpenAIWSIngressTurnError(
				"some_unknown_stage",
				errors.New("unknown stage fail"),
				false,
			),
			wantReason:   openAIWSIngressTurnAbortReasonUnknown,
			wantExpected: false,
		},
		{
			name: "continuation unavailable close",
			err: NewOpenAIWSClientCloseError(
				coderws.StatusPolicyViolation,
				openAIWSContinuationUnavailableReason,
				nil,
			),
			wantReason:   openAIWSIngressTurnAbortReasonContinuationUnavailable,
			wantExpected: true,
		},
		{
			name: "upstream restart 1012",
			err: wrapOpenAIWSIngressTurnError(
				"read_upstream",
				coderws.CloseError{Code: coderws.StatusServiceRestart, Reason: "service restart"},
				false,
			),
			wantReason:   openAIWSIngressTurnAbortReasonUpstreamRestart,
			wantExpected: true,
		},
		{
			name: "upstream try again later 1013",
			err: wrapOpenAIWSIngressTurnError(
				"read_upstream",
				coderws.CloseError{Code: coderws.StatusTryAgainLater, Reason: "try again later"},
				false,
			),
			wantReason:   openAIWSIngressTurnAbortReasonUpstreamRestart,
			wantExpected: true,
		},
		{
			name: "upstream restart 1012 with wroteDownstream",
			err: wrapOpenAIWSIngressTurnError(
				"read_upstream",
				coderws.CloseError{Code: coderws.StatusServiceRestart, Reason: "service restart"},
				true,
			),
			wantReason:   openAIWSIngressTurnAbortReasonUpstreamRestart,
			wantExpected: true,
		},
		{
			name: "1012 on non-read_upstream stage should not match",
			err: wrapOpenAIWSIngressTurnError(
				"write_upstream",
				coderws.CloseError{Code: coderws.StatusServiceRestart, Reason: "service restart"},
				false,
			),
			wantReason:   openAIWSIngressTurnAbortReasonWriteUpstream,
			wantExpected: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reason, expected := classifyOpenAIWSIngressTurnAbortReason(tt.err)
			require.Equal(t, tt.wantReason, reason)
			require.Equal(t, tt.wantExpected, expected)
		})
	}
}

func TestClassifyOpenAIWSIngressTurnAbortReason_ClientDisconnectedDrainTimeout(t *testing.T) {
	t.Parallel()

	err := wrapOpenAIWSIngressTurnError(
		"client_disconnected_drain_timeout",
		openAIWSIngressClientDisconnectedDrainTimeoutError(2*time.Second),
		true,
	)
	reason, expected := classifyOpenAIWSIngressTurnAbortReason(err)
	require.Equal(t, openAIWSIngressTurnAbortReasonContextCanceled, reason)
	require.True(t, expected)
	require.Equal(t, openAIWSIngressTurnAbortDispositionCloseGracefully, openAIWSIngressTurnAbortDispositionForReason(reason))
}

func TestOpenAIWSIngressPumpClosedTurnError_ClientDisconnected(t *testing.T) {
	t.Parallel()

	partial := &OpenAIForwardResult{
		RequestID: "resp_partial",
		Usage: OpenAIUsage{
			InputTokens: 12,
		},
	}
	err := openAIWSIngressPumpClosedTurnError(true, true, partial)
	require.Error(t, err)
	require.ErrorIs(t, err, context.Canceled)

	var turnErr *openAIWSIngressTurnError
	require.ErrorAs(t, err, &turnErr)
	require.Equal(t, "client_disconnected_drain_timeout", turnErr.stage)
	require.True(t, turnErr.wroteDownstream)
	require.NotNil(t, turnErr.partialResult)
	require.Equal(t, partial.RequestID, turnErr.partialResult.RequestID)
}

func TestOpenAIWSIngressPumpClosedTurnError_ReadUpstream(t *testing.T) {
	t.Parallel()

	err := openAIWSIngressPumpClosedTurnError(false, false, nil)
	require.Error(t, err)

	var turnErr *openAIWSIngressTurnError
	require.ErrorAs(t, err, &turnErr)
	require.Equal(t, "read_upstream", turnErr.stage)
	require.False(t, turnErr.wroteDownstream)
	require.Nil(t, turnErr.partialResult)
	reason, expected := classifyOpenAIWSIngressTurnAbortReason(err)
	require.Equal(t, openAIWSIngressTurnAbortReasonReadUpstream, reason)
	require.False(t, expected)
}

func TestOpenAIWSIngressPumpClosedTurnError_ClonesPartialResult(t *testing.T) {
	t.Parallel()

	partial := &OpenAIForwardResult{
		RequestID:              "resp_original",
		PendingFunctionCallIDs: []string{"call_a"},
	}
	err := openAIWSIngressPumpClosedTurnError(true, true, partial)
	require.Error(t, err)

	partial.RequestID = "resp_mutated"
	partial.PendingFunctionCallIDs[0] = "call_b"

	var turnErr *openAIWSIngressTurnError
	require.ErrorAs(t, err, &turnErr)
	require.NotNil(t, turnErr.partialResult)
	require.Equal(t, "resp_original", turnErr.partialResult.RequestID)
	require.Equal(t, []string{"call_a"}, turnErr.partialResult.PendingFunctionCallIDs)
}

func TestOpenAIWSIngressClientDisconnectedDrainTimeoutError_DefaultTimeout(t *testing.T) {
	t.Parallel()

	err := openAIWSIngressClientDisconnectedDrainTimeoutError(0)
	require.Error(t, err)
	require.Contains(t, err.Error(), openAIWSIngressClientDisconnectDrainTimeout.String())
	require.ErrorIs(t, err, context.Canceled)
}

func TestOpenAIWSIngressResolveDrainReadTimeout(t *testing.T) {
	t.Parallel()

	now := time.Now()
	tests := []struct {
		name       string
		base       time.Duration
		deadline   time.Time
		want       time.Duration
		wantExpire bool
	}{
		{
			name:       "no_deadline_uses_base",
			base:       15 * time.Second,
			deadline:   time.Time{},
			want:       15 * time.Second,
			wantExpire: false,
		},
		{
			name:       "remaining_shorter_than_base",
			base:       10 * time.Second,
			deadline:   now.Add(3 * time.Second),
			want:       3 * time.Second,
			wantExpire: false,
		},
		{
			name:       "base_shorter_than_remaining",
			base:       2 * time.Second,
			deadline:   now.Add(8 * time.Second),
			want:       2 * time.Second,
			wantExpire: false,
		},
		{
			name:       "base_zero_uses_remaining",
			base:       0,
			deadline:   now.Add(5 * time.Second),
			want:       5 * time.Second,
			wantExpire: false,
		},
		{
			name:       "expired_deadline",
			base:       10 * time.Second,
			deadline:   now.Add(-time.Millisecond),
			want:       0,
			wantExpire: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, expired := openAIWSIngressResolveDrainReadTimeout(tt.base, tt.deadline, now)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.wantExpire, expired)
		})
	}
}

func TestOpenAIWSIngressTurnAbortDispositionForReason(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   openAIWSIngressTurnAbortReason
		want openAIWSIngressTurnAbortDisposition
	}{
		{
			name: "continue turn on previous response mismatch",
			in:   openAIWSIngressTurnAbortReasonPreviousResponse,
			want: openAIWSIngressTurnAbortDispositionContinueTurn,
		},
		{
			name: "continue turn on tool output mismatch",
			in:   openAIWSIngressTurnAbortReasonToolOutput,
			want: openAIWSIngressTurnAbortDispositionContinueTurn,
		},
		{
			name: "continue turn on upstream error event",
			in:   openAIWSIngressTurnAbortReasonUpstreamError,
			want: openAIWSIngressTurnAbortDispositionContinueTurn,
		},
		{
			name: "close gracefully on context canceled",
			in:   openAIWSIngressTurnAbortReasonContextCanceled,
			want: openAIWSIngressTurnAbortDispositionCloseGracefully,
		},
		{
			name: "close gracefully on client closed",
			in:   openAIWSIngressTurnAbortReasonClientClosed,
			want: openAIWSIngressTurnAbortDispositionCloseGracefully,
		},
		{
			name: "default fail request on unknown reason",
			in:   openAIWSIngressTurnAbortReasonUnknown,
			want: openAIWSIngressTurnAbortDispositionFailRequest,
		},
		{
			name: "default fail request on write upstream reason",
			in:   openAIWSIngressTurnAbortReasonWriteUpstream,
			want: openAIWSIngressTurnAbortDispositionFailRequest,
		},
		{
			name: "default fail request on read upstream reason",
			in:   openAIWSIngressTurnAbortReasonReadUpstream,
			want: openAIWSIngressTurnAbortDispositionFailRequest,
		},
		{
			name: "default fail request on write client reason",
			in:   openAIWSIngressTurnAbortReasonWriteClient,
			want: openAIWSIngressTurnAbortDispositionFailRequest,
		},
		{
			name: "continue turn on upstream restart",
			in:   openAIWSIngressTurnAbortReasonUpstreamRestart,
			want: openAIWSIngressTurnAbortDispositionContinueTurn,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, openAIWSIngressTurnAbortDispositionForReason(tt.in))
		})
	}
}

func TestIsOpenAIWSIngressPreviousResponseNotFound(t *testing.T) {
	t.Parallel()

	require.False(t, isOpenAIWSIngressPreviousResponseNotFound(nil))
	require.False(t, isOpenAIWSIngressPreviousResponseNotFound(errors.New("plain error")))
	require.False(t, isOpenAIWSIngressPreviousResponseNotFound(
		wrapOpenAIWSIngressTurnError("read_upstream", errors.New("upstream read failed"), false),
	))
	require.False(t, isOpenAIWSIngressPreviousResponseNotFound(
		wrapOpenAIWSIngressTurnError(openAIWSIngressStagePreviousResponseNotFound, errors.New("previous response not found"), true),
	))
	require.True(t, isOpenAIWSIngressPreviousResponseNotFound(
		wrapOpenAIWSIngressTurnError(openAIWSIngressStagePreviousResponseNotFound, errors.New("previous response not found"), false),
	))
}

func TestOpenAIWSIngressPreviousResponseRecoveryEnabled(t *testing.T) {
	t.Parallel()

	var nilService *OpenAIGatewayService
	require.True(t, nilService.openAIWSIngressPreviousResponseRecoveryEnabled(), "nil service should default to enabled")

	svcWithNilCfg := &OpenAIGatewayService{}
	require.True(t, svcWithNilCfg.openAIWSIngressPreviousResponseRecoveryEnabled(), "nil config should default to enabled")

	svc := &OpenAIGatewayService{
		cfg: &config.Config{},
	}
	require.False(t, svc.openAIWSIngressPreviousResponseRecoveryEnabled(), "explicit config default should be false")

	svc.cfg.Gateway.OpenAIWS.IngressPreviousResponseRecoveryEnabled = true
	require.True(t, svc.openAIWSIngressPreviousResponseRecoveryEnabled())
}

func TestDropPreviousResponseIDFromRawPayload(t *testing.T) {
	t.Parallel()

	t.Run("empty_payload", func(t *testing.T) {
		updated, removed, err := dropPreviousResponseIDFromRawPayload(nil)
		require.NoError(t, err)
		require.False(t, removed)
		require.Empty(t, updated)
	})

	t.Run("payload_without_previous_response_id", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","model":"gpt-5.1"}`)
		updated, removed, err := dropPreviousResponseIDFromRawPayload(payload)
		require.NoError(t, err)
		require.False(t, removed)
		require.Equal(t, string(payload), string(updated))
	})

	t.Run("normal_delete_success", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","model":"gpt-5.1","previous_response_id":"resp_abc"}`)
		updated, removed, err := dropPreviousResponseIDFromRawPayload(payload)
		require.NoError(t, err)
		require.True(t, removed)
		require.False(t, gjson.GetBytes(updated, "previous_response_id").Exists())
	})

	t.Run("duplicate_keys_are_removed", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","previous_response_id":"resp_a","input":[],"previous_response_id":"resp_b"}`)
		updated, removed, err := dropPreviousResponseIDFromRawPayload(payload)
		require.NoError(t, err)
		require.True(t, removed)
		require.False(t, gjson.GetBytes(updated, "previous_response_id").Exists())
	})

	t.Run("nil_delete_fn_uses_default_delete_logic", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","model":"gpt-5.1","previous_response_id":"resp_abc"}`)
		updated, removed, err := dropPreviousResponseIDFromRawPayloadWithDeleteFn(payload, nil)
		require.NoError(t, err)
		require.True(t, removed)
		require.False(t, gjson.GetBytes(updated, "previous_response_id").Exists())
	})

	t.Run("delete_error", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","model":"gpt-5.1","previous_response_id":"resp_abc"}`)
		updated, removed, err := dropPreviousResponseIDFromRawPayloadWithDeleteFn(payload, func(_ []byte, _ string) ([]byte, error) {
			return nil, errors.New("delete failed")
		})
		require.Error(t, err)
		require.False(t, removed)
		require.Equal(t, string(payload), string(updated))
	})

	t.Run("malformed_json_is_still_best_effort_deleted", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","previous_response_id":"resp_abc"`)
		require.True(t, gjson.GetBytes(payload, "previous_response_id").Exists())

		updated, removed, err := dropPreviousResponseIDFromRawPayload(payload)
		require.NoError(t, err)
		require.True(t, removed)
		require.False(t, gjson.GetBytes(updated, "previous_response_id").Exists())
	})
}

func TestAlignStoreDisabledPreviousResponseID(t *testing.T) {
	t.Parallel()

	t.Run("empty_payload", func(t *testing.T) {
		updated, changed, err := alignStoreDisabledPreviousResponseID(nil, "resp_target")
		require.NoError(t, err)
		require.False(t, changed)
		require.Empty(t, updated)
	})

	t.Run("empty_expected", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","previous_response_id":"resp_old"}`)
		updated, changed, err := alignStoreDisabledPreviousResponseID(payload, "")
		require.NoError(t, err)
		require.False(t, changed)
		require.Equal(t, string(payload), string(updated))
	})

	t.Run("missing_previous_response_id", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","model":"gpt-5.1"}`)
		updated, changed, err := alignStoreDisabledPreviousResponseID(payload, "resp_target")
		require.NoError(t, err)
		require.False(t, changed)
		require.Equal(t, string(payload), string(updated))
	})

	t.Run("already_aligned", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","previous_response_id":"resp_target"}`)
		updated, changed, err := alignStoreDisabledPreviousResponseID(payload, "resp_target")
		require.NoError(t, err)
		require.False(t, changed)
		require.Equal(t, "resp_target", gjson.GetBytes(updated, "previous_response_id").String())
	})

	t.Run("mismatch_rewrites_to_expected", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","previous_response_id":"resp_old","input":[]}`)
		updated, changed, err := alignStoreDisabledPreviousResponseID(payload, "resp_target")
		require.NoError(t, err)
		require.True(t, changed)
		require.Equal(t, "resp_target", gjson.GetBytes(updated, "previous_response_id").String())
	})

	t.Run("duplicate_keys_rewrites_to_single_expected", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","previous_response_id":"resp_old_1","input":[],"previous_response_id":"resp_old_2"}`)
		updated, changed, err := alignStoreDisabledPreviousResponseID(payload, "resp_target")
		require.NoError(t, err)
		require.True(t, changed)
		require.Equal(t, "resp_target", gjson.GetBytes(updated, "previous_response_id").String())
	})
}

func TestSetPreviousResponseIDToRawPayload(t *testing.T) {
	t.Parallel()

	t.Run("empty_payload", func(t *testing.T) {
		updated, err := setPreviousResponseIDToRawPayload(nil, "resp_target")
		require.NoError(t, err)
		require.Empty(t, updated)
	})

	t.Run("empty_previous_response_id", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","model":"gpt-5.1"}`)
		updated, err := setPreviousResponseIDToRawPayload(payload, "")
		require.NoError(t, err)
		require.Equal(t, string(payload), string(updated))
	})

	t.Run("set_previous_response_id_when_missing", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","model":"gpt-5.1"}`)
		updated, err := setPreviousResponseIDToRawPayload(payload, "resp_target")
		require.NoError(t, err)
		require.Equal(t, "resp_target", gjson.GetBytes(updated, "previous_response_id").String())
		require.Equal(t, "gpt-5.1", gjson.GetBytes(updated, "model").String())
	})

	t.Run("overwrite_existing_previous_response_id", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","model":"gpt-5.1","previous_response_id":"resp_old"}`)
		updated, err := setPreviousResponseIDToRawPayload(payload, "resp_new")
		require.NoError(t, err)
		require.Equal(t, "resp_new", gjson.GetBytes(updated, "previous_response_id").String())
	})
}

func TestShouldInferIngressFunctionCallOutputPreviousResponseID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                    string
		storeDisabled           bool
		turn                    int
		hasFunctionCallOutput   bool
		currentPreviousResponse string
		expectedPrevious        string
		want                    bool
	}{
		{
			name:                  "infer_when_all_conditions_match",
			storeDisabled:         true,
			turn:                  2,
			hasFunctionCallOutput: true,
			expectedPrevious:      "resp_1",
			want:                  true,
		},
		{
			name:                  "skip_when_store_enabled",
			storeDisabled:         false,
			turn:                  2,
			hasFunctionCallOutput: true,
			expectedPrevious:      "resp_1",
			want:                  false,
		},
		{
			name:                  "infer_on_first_turn_when_expected_previous_exists",
			storeDisabled:         true,
			turn:                  1,
			hasFunctionCallOutput: true,
			expectedPrevious:      "resp_1",
			want:                  true,
		},
		{
			name:                  "skip_without_function_call_output",
			storeDisabled:         true,
			turn:                  2,
			hasFunctionCallOutput: false,
			expectedPrevious:      "resp_1",
			want:                  false,
		},
		{
			name:                    "skip_when_request_already_has_previous_response_id",
			storeDisabled:           true,
			turn:                    2,
			hasFunctionCallOutput:   true,
			currentPreviousResponse: "resp_client",
			expectedPrevious:        "resp_1",
			want:                    false,
		},
		{
			name:                  "skip_when_last_turn_response_id_missing",
			storeDisabled:         true,
			turn:                  2,
			hasFunctionCallOutput: true,
			expectedPrevious:      "",
			want:                  false,
		},
		{
			name:                  "trim_whitespace_before_judgement",
			storeDisabled:         true,
			turn:                  2,
			hasFunctionCallOutput: true,
			expectedPrevious:      "   resp_2   ",
			want:                  true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := shouldInferIngressFunctionCallOutputPreviousResponseID(
				tt.storeDisabled,
				tt.turn,
				tt.hasFunctionCallOutput,
				tt.currentPreviousResponse,
				tt.expectedPrevious,
			)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestShouldProactivelyRejectIngressToolOutputWithoutPreviousResponseID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		storeDisabled         bool
		hasFunctionCallOutput bool
		previousResponseID    string
		hasToolOutputContext  bool
		want                  bool
	}{
		{
			name:                  "reject_when_store_disabled_and_missing_prev_without_context",
			storeDisabled:         true,
			hasFunctionCallOutput: true,
			previousResponseID:    "",
			hasToolOutputContext:  false,
			want:                  true,
		},
		{
			name:                  "skip_when_store_enabled",
			storeDisabled:         false,
			hasFunctionCallOutput: true,
			previousResponseID:    "",
			hasToolOutputContext:  false,
			want:                  false,
		},
		{
			name:                  "skip_when_previous_response_id_exists",
			storeDisabled:         true,
			hasFunctionCallOutput: true,
			previousResponseID:    "resp_1",
			hasToolOutputContext:  false,
			want:                  false,
		},
		{
			name:                  "skip_when_has_tool_output_context",
			storeDisabled:         true,
			hasFunctionCallOutput: true,
			previousResponseID:    "",
			hasToolOutputContext:  true,
			want:                  false,
		},
		{
			name:                  "skip_when_no_function_call_output",
			storeDisabled:         true,
			hasFunctionCallOutput: false,
			previousResponseID:    "",
			hasToolOutputContext:  false,
			want:                  false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := shouldProactivelyRejectIngressToolOutputWithoutPreviousResponseID(
				tt.storeDisabled,
				tt.hasFunctionCallOutput,
				tt.previousResponseID,
				tt.hasToolOutputContext,
			)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestOpenAIWSHasToolOutputContextInPayload(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                 string
		payload              []byte
		expectedCallIDs      []string
		wantHasToolCall      bool
		wantHasItemReference bool
	}{
		{
			name:                 "empty_payload",
			payload:              nil,
			expectedCallIDs:      []string{"call_1"},
			wantHasToolCall:      false,
			wantHasItemReference: false,
		},
		{
			name:                 "has_tool_call_context",
			payload:              []byte(`{"input":[{"type":"tool_call","call_id":"call_1"},{"type":"function_call_output","call_id":"call_1"}]}`),
			expectedCallIDs:      []string{"call_1"},
			wantHasToolCall:      true,
			wantHasItemReference: false,
		},
		{
			name:                 "has_function_call_context",
			payload:              []byte(`{"input":[{"type":"function_call","call_id":"call_1"},{"type":"function_call_output","call_id":"call_1"}]}`),
			expectedCallIDs:      []string{"call_1"},
			wantHasToolCall:      true,
			wantHasItemReference: false,
		},
		{
			name:                 "tool_call_without_call_id_is_not_context",
			payload:              []byte(`{"input":[{"type":"tool_call"},{"type":"function_call_output","call_id":"call_1"}]}`),
			expectedCallIDs:      []string{"call_1"},
			wantHasToolCall:      false,
			wantHasItemReference: false,
		},
		{
			name:                 "has_item_reference_for_all_function_call_outputs",
			payload:              []byte(`{"input":[{"type":"item_reference","id":"call_1"},{"type":"item_reference","id":"call_2"},{"type":"function_call_output","call_id":"call_1"},{"type":"function_call_output","call_id":"call_2"}]}`),
			expectedCallIDs:      []string{"call_1", "call_2"},
			wantHasToolCall:      false,
			wantHasItemReference: true,
		},
		{
			name:                 "missing_item_reference_for_some_call_ids",
			payload:              []byte(`{"input":[{"type":"item_reference","id":"call_1"},{"type":"function_call_output","call_id":"call_1"},{"type":"function_call_output","call_id":"call_2"}]}`),
			expectedCallIDs:      []string{"call_1", "call_2"},
			wantHasToolCall:      false,
			wantHasItemReference: false,
		},
		{
			name:                 "ignores_non_array_input",
			payload:              []byte(`{"input":"bad"}`),
			expectedCallIDs:      []string{"call_1"},
			wantHasToolCall:      false,
			wantHasItemReference: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.wantHasToolCall, openAIWSHasToolCallContextInPayload(tt.payload))
			require.Equal(t, tt.wantHasItemReference, openAIWSHasItemReferenceForAllFunctionCallOutputsInPayload(tt.payload, tt.expectedCallIDs))
		})
	}
}

func TestOpenAIWSInputIsPrefixExtended(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		previous  []byte
		current   []byte
		want      bool
		expectErr bool
	}{
		{
			name:     "both_missing_input",
			previous: []byte(`{"type":"response.create","model":"gpt-5.1"}`),
			current:  []byte(`{"type":"response.create","model":"gpt-5.1","previous_response_id":"resp_1"}`),
			want:     true,
		},
		{
			name:     "previous_missing_current_empty_array",
			previous: []byte(`{"type":"response.create","model":"gpt-5.1"}`),
			current:  []byte(`{"type":"response.create","model":"gpt-5.1","input":[]}`),
			want:     true,
		},
		{
			name:     "previous_missing_current_non_empty_array",
			previous: []byte(`{"type":"response.create","model":"gpt-5.1"}`),
			current:  []byte(`{"type":"response.create","model":"gpt-5.1","input":[{"type":"input_text","text":"hello"}]}`),
			want:     false,
		},
		{
			name:     "array_prefix_match",
			previous: []byte(`{"input":[{"type":"input_text","text":"hello"}]}`),
			current:  []byte(`{"input":[{"text":"hello","type":"input_text"},{"type":"input_text","text":"world"}]}`),
			want:     true,
		},
		{
			name:     "array_prefix_mismatch",
			previous: []byte(`{"input":[{"type":"input_text","text":"hello"}]}`),
			current:  []byte(`{"input":[{"type":"input_text","text":"different"}]}`),
			want:     false,
		},
		{
			name:     "current_shorter_than_previous",
			previous: []byte(`{"input":[{"type":"input_text","text":"a"},{"type":"input_text","text":"b"}]}`),
			current:  []byte(`{"input":[{"type":"input_text","text":"a"}]}`),
			want:     false,
		},
		{
			name:     "previous_has_input_current_missing",
			previous: []byte(`{"input":[{"type":"input_text","text":"a"}]}`),
			current:  []byte(`{"model":"gpt-5.1"}`),
			want:     false,
		},
		{
			name:     "input_string_treated_as_single_item",
			previous: []byte(`{"input":"hello"}`),
			current:  []byte(`{"input":"hello"}`),
			want:     true,
		},
		{
			name:      "current_invalid_input_json",
			previous:  []byte(`{"input":[]}`),
			current:   []byte(`{"input":[}`),
			expectErr: true,
		},
		{
			name:      "invalid_input_json",
			previous:  []byte(`{"input":[}`),
			current:   []byte(`{"input":[]}`),
			expectErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := openAIWSInputIsPrefixExtended(tt.previous, tt.current)
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestNormalizeOpenAIWSJSONForCompare(t *testing.T) {
	t.Parallel()

	normalized, err := normalizeOpenAIWSJSONForCompare([]byte(`{"b":2,"a":1}`))
	require.NoError(t, err)
	require.Equal(t, `{"a":1,"b":2}`, string(normalized))

	_, err = normalizeOpenAIWSJSONForCompare([]byte("   "))
	require.Error(t, err)

	_, err = normalizeOpenAIWSJSONForCompare([]byte(`{"a":`))
	require.Error(t, err)
}

func TestNormalizeOpenAIWSJSONForCompareOrRaw(t *testing.T) {
	t.Parallel()

	require.Equal(t, `{"a":1,"b":2}`, string(normalizeOpenAIWSJSONForCompareOrRaw([]byte(`{"b":2,"a":1}`))))
	require.Equal(t, `{"a":`, string(normalizeOpenAIWSJSONForCompareOrRaw([]byte(`{"a":`))))
}

func TestNormalizeOpenAIWSPayloadWithoutInputAndPreviousResponseID(t *testing.T) {
	t.Parallel()

	normalized, err := normalizeOpenAIWSPayloadWithoutInputAndPreviousResponseID(
		[]byte(`{"model":"gpt-5.1","input":[1],"previous_response_id":"resp_x","metadata":{"b":2,"a":1}}`),
	)
	require.NoError(t, err)
	require.False(t, gjson.GetBytes(normalized, "input").Exists())
	require.False(t, gjson.GetBytes(normalized, "previous_response_id").Exists())
	require.Equal(t, float64(1), gjson.GetBytes(normalized, "metadata.a").Float())

	_, err = normalizeOpenAIWSPayloadWithoutInputAndPreviousResponseID(nil)
	require.Error(t, err)

	_, err = normalizeOpenAIWSPayloadWithoutInputAndPreviousResponseID([]byte(`[]`))
	require.Error(t, err)
}

func TestOpenAIWSExtractNormalizedInputSequence(t *testing.T) {
	t.Parallel()

	t.Run("empty_payload", func(t *testing.T) {
		items, exists, err := openAIWSExtractNormalizedInputSequence(nil)
		require.NoError(t, err)
		require.False(t, exists)
		require.Nil(t, items)
	})

	t.Run("input_missing", func(t *testing.T) {
		items, exists, err := openAIWSExtractNormalizedInputSequence([]byte(`{"type":"response.create"}`))
		require.NoError(t, err)
		require.False(t, exists)
		require.Nil(t, items)
	})

	t.Run("input_array", func(t *testing.T) {
		items, exists, err := openAIWSExtractNormalizedInputSequence([]byte(`{"input":[{"type":"input_text","text":"hello"}]}`))
		require.NoError(t, err)
		require.True(t, exists)
		require.Len(t, items, 1)
	})

	t.Run("input_object", func(t *testing.T) {
		items, exists, err := openAIWSExtractNormalizedInputSequence([]byte(`{"input":{"type":"input_text","text":"hello"}}`))
		require.NoError(t, err)
		require.True(t, exists)
		require.Len(t, items, 1)
	})

	t.Run("input_string", func(t *testing.T) {
		items, exists, err := openAIWSExtractNormalizedInputSequence([]byte(`{"input":"hello"}`))
		require.NoError(t, err)
		require.True(t, exists)
		require.Len(t, items, 1)
		require.Equal(t, `"hello"`, string(items[0]))
	})

	t.Run("input_number", func(t *testing.T) {
		items, exists, err := openAIWSExtractNormalizedInputSequence([]byte(`{"input":42}`))
		require.NoError(t, err)
		require.True(t, exists)
		require.Len(t, items, 1)
		require.Equal(t, "42", string(items[0]))
	})

	t.Run("input_bool", func(t *testing.T) {
		items, exists, err := openAIWSExtractNormalizedInputSequence([]byte(`{"input":true}`))
		require.NoError(t, err)
		require.True(t, exists)
		require.Len(t, items, 1)
		require.Equal(t, "true", string(items[0]))
	})

	t.Run("input_null", func(t *testing.T) {
		items, exists, err := openAIWSExtractNormalizedInputSequence([]byte(`{"input":null}`))
		require.NoError(t, err)
		require.True(t, exists)
		require.Len(t, items, 1)
		require.Equal(t, "null", string(items[0]))
	})

	t.Run("input_invalid_array_json", func(t *testing.T) {
		items, exists, err := openAIWSExtractNormalizedInputSequence([]byte(`{"input":[}`))
		require.Error(t, err)
		require.True(t, exists)
		require.Nil(t, items)
	})
}

func TestShouldKeepIngressPreviousResponseID(t *testing.T) {
	t.Parallel()

	previousPayload := []byte(`{
		"type":"response.create",
		"model":"gpt-5.1",
		"store":false,
		"tools":[{"type":"function","name":"tool_a"}],
		"input":[{"type":"input_text","text":"hello"}]
	}`)
	currentStrictPayload := []byte(`{
		"type":"response.create",
		"model":"gpt-5.1",
		"store":false,
		"tools":[{"name":"tool_a","type":"function"}],
		"previous_response_id":"resp_turn_1",
		"input":[{"text":"hello","type":"input_text"},{"type":"input_text","text":"world"}]
	}`)

	t.Run("strict_incremental_keep", func(t *testing.T) {
		keep, reason, err := shouldKeepIngressPreviousResponseID(previousPayload, currentStrictPayload, "resp_turn_1", false, nil, nil)
		require.NoError(t, err)
		require.True(t, keep)
		require.Equal(t, "strict_incremental_ok", reason)
	})

	t.Run("missing_previous_response_id", func(t *testing.T) {
		payload := []byte(`{"type":"response.create","model":"gpt-5.1","input":[]}`)
		keep, reason, err := shouldKeepIngressPreviousResponseID(previousPayload, payload, "resp_turn_1", false, nil, nil)
		require.NoError(t, err)
		require.False(t, keep)
		require.Equal(t, "missing_previous_response_id", reason)
	})

	t.Run("missing_last_turn_response_id", func(t *testing.T) {
		keep, reason, err := shouldKeepIngressPreviousResponseID(previousPayload, currentStrictPayload, "", false, nil, nil)
		require.NoError(t, err)
		require.False(t, keep)
		require.Equal(t, "missing_last_turn_response_id", reason)
	})

	t.Run("previous_response_id_mismatch", func(t *testing.T) {
		keep, reason, err := shouldKeepIngressPreviousResponseID(previousPayload, currentStrictPayload, "resp_turn_other", false, nil, nil)
		require.NoError(t, err)
		require.False(t, keep)
		require.Equal(t, "previous_response_id_mismatch", reason)
	})

	t.Run("missing_previous_turn_payload", func(t *testing.T) {
		keep, reason, err := shouldKeepIngressPreviousResponseID(nil, currentStrictPayload, "resp_turn_1", false, nil, nil)
		require.NoError(t, err)
		require.False(t, keep)
		require.Equal(t, "missing_previous_turn_payload", reason)
	})

	t.Run("non_input_changed", func(t *testing.T) {
		payload := []byte(`{
			"type":"response.create",
			"model":"gpt-5.1-mini",
			"store":false,
			"tools":[{"type":"function","name":"tool_a"}],
			"previous_response_id":"resp_turn_1",
			"input":[{"type":"input_text","text":"hello"},{"type":"input_text","text":"world"}]
		}`)
		keep, reason, err := shouldKeepIngressPreviousResponseID(previousPayload, payload, "resp_turn_1", false, nil, nil)
		require.NoError(t, err)
		require.False(t, keep)
		require.Equal(t, "non_input_changed", reason)
	})

	t.Run("delta_input_keeps_previous_response_id", func(t *testing.T) {
		payload := []byte(`{
			"type":"response.create",
			"model":"gpt-5.1",
			"store":false,
			"tools":[{"type":"function","name":"tool_a"}],
			"previous_response_id":"resp_turn_1",
			"input":[{"type":"input_text","text":"different"}]
		}`)
		keep, reason, err := shouldKeepIngressPreviousResponseID(previousPayload, payload, "resp_turn_1", false, nil, nil)
		require.NoError(t, err)
		require.True(t, keep)
		require.Equal(t, "strict_incremental_ok", reason)
	})

	t.Run("function_call_output_keeps_previous_response_id", func(t *testing.T) {
		payload := []byte(`{
			"type":"response.create",
			"model":"gpt-5.1",
			"store":false,
			"previous_response_id":"resp_external",
			"input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]
		}`)
		keep, reason, err := shouldKeepIngressPreviousResponseID(previousPayload, payload, "resp_turn_1", true, nil, []string{"call_1"})
		require.NoError(t, err)
		require.True(t, keep)
		require.Equal(t, "has_function_call_output", reason)
	})

	t.Run("function_call_output_pending_call_id_match_keeps_previous_response_id", func(t *testing.T) {
		payload := []byte(`{
			"type":"response.create",
			"model":"gpt-5.1",
			"store":false,
			"previous_response_id":"resp_turn_1",
			"input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]
		}`)
		keep, reason, err := shouldKeepIngressPreviousResponseID(
			previousPayload,
			payload,
			"resp_turn_1",
			true,
			[]string{"call_1"},
			[]string{"call_1"},
		)
		require.NoError(t, err)
		require.True(t, keep)
		require.Equal(t, "function_call_output_call_id_match", reason)
	})

	t.Run("function_call_output_pending_call_id_mismatch_drops_previous_response_id", func(t *testing.T) {
		payload := []byte(`{
			"type":"response.create",
			"model":"gpt-5.1",
			"store":false,
			"previous_response_id":"resp_turn_1",
			"input":[{"type":"function_call_output","call_id":"call_other","output":"ok"}]
		}`)
		keep, reason, err := shouldKeepIngressPreviousResponseID(
			previousPayload,
			payload,
			"resp_turn_1",
			true,
			[]string{"call_1"},
			[]string{"call_other"},
		)
		require.NoError(t, err)
		require.False(t, keep)
		require.Equal(t, "function_call_output_call_id_mismatch", reason)
	})

	t.Run("non_input_compare_error", func(t *testing.T) {
		keep, reason, err := shouldKeepIngressPreviousResponseID([]byte(`[]`), currentStrictPayload, "resp_turn_1", false, nil, nil)
		require.Error(t, err)
		require.False(t, keep)
		require.Equal(t, "non_input_compare_error", reason)
	})

	t.Run("current_payload_compare_error", func(t *testing.T) {
		keep, reason, err := shouldKeepIngressPreviousResponseID(previousPayload, []byte(`{"previous_response_id":"resp_turn_1","input":[}`), "resp_turn_1", false, nil, nil)
		require.Error(t, err)
		require.False(t, keep)
		require.Equal(t, "non_input_compare_error", reason)
	})
}

func TestBuildOpenAIWSReplayInputSequence(t *testing.T) {
	t.Parallel()

	lastFull := []json.RawMessage{
		json.RawMessage(`{"type":"input_text","text":"hello"}`),
	}

	t.Run("no_previous_response_id_use_current", func(t *testing.T) {
		items, exists, err := buildOpenAIWSReplayInputSequence(
			lastFull,
			true,
			[]byte(`{"input":[{"type":"input_text","text":"new"}]}`),
			false,
		)
		require.NoError(t, err)
		require.True(t, exists)
		require.Len(t, items, 1)
		require.Equal(t, "new", gjson.GetBytes(items[0], "text").String())
	})

	t.Run("previous_response_id_delta_append", func(t *testing.T) {
		items, exists, err := buildOpenAIWSReplayInputSequence(
			lastFull,
			true,
			[]byte(`{"previous_response_id":"resp_1","input":[{"type":"input_text","text":"world"}]}`),
			true,
		)
		require.NoError(t, err)
		require.True(t, exists)
		require.Len(t, items, 2)
		require.Equal(t, "hello", gjson.GetBytes(items[0], "text").String())
		require.Equal(t, "world", gjson.GetBytes(items[1], "text").String())
	})

	t.Run("previous_response_id_full_input_replace", func(t *testing.T) {
		items, exists, err := buildOpenAIWSReplayInputSequence(
			lastFull,
			true,
			[]byte(`{"previous_response_id":"resp_1","input":[{"type":"input_text","text":"hello"},{"type":"input_text","text":"world"}]}`),
			true,
		)
		require.NoError(t, err)
		require.True(t, exists)
		require.Len(t, items, 2)
		require.Equal(t, "hello", gjson.GetBytes(items[0], "text").String())
		require.Equal(t, "world", gjson.GetBytes(items[1], "text").String())
	})

	t.Run("replay_input_limited_by_bytes_keeps_newest_items", func(t *testing.T) {
		makeItem := func(text string) json.RawMessage {
			raw, err := json.Marshal(map[string]any{
				"type": "input_text",
				"text": text,
			})
			require.NoError(t, err)
			return json.RawMessage(raw)
		}
		largeA := strings.Repeat("a", openAIWSIngressReplayInputMaxBytes/2)
		largeB := strings.Repeat("b", openAIWSIngressReplayInputMaxBytes/2)
		largeC := strings.Repeat("c", openAIWSIngressReplayInputMaxBytes/2)
		previousLarge := []json.RawMessage{
			makeItem(largeA),
			makeItem(largeB),
		}
		currentPayload, err := json.Marshal(map[string]any{
			"previous_response_id": "resp_1",
			"input": []map[string]any{
				{"type": "input_text", "text": largeC},
			},
		})
		require.NoError(t, err)

		items, exists, err := buildOpenAIWSReplayInputSequence(
			previousLarge,
			true,
			currentPayload,
			true,
		)
		require.NoError(t, err)
		require.True(t, exists)
		require.GreaterOrEqual(t, len(items), 1)
		require.Equal(t, largeC, gjson.GetBytes(items[len(items)-1], "text").String(), "latest item should always be preserved")
		require.Less(t, len(items), 3, "oversized replay input should be truncated")
	})

	t.Run("replay_input_limited_by_bytes_still_keeps_single_oversized_latest_item", func(t *testing.T) {
		tooLargeText := strings.Repeat("z", openAIWSIngressReplayInputMaxBytes+1024)
		currentPayload, err := json.Marshal(map[string]any{
			"input": []map[string]any{
				{"type": "input_text", "text": tooLargeText},
			},
		})
		require.NoError(t, err)

		items, exists, err := buildOpenAIWSReplayInputSequence(
			nil,
			false,
			currentPayload,
			false,
		)
		require.NoError(t, err)
		require.True(t, exists)
		require.Len(t, items, 1)
		require.Equal(t, tooLargeText, gjson.GetBytes(items[0], "text").String())
	})
}

func TestOpenAIWSInputAppearsEditedFromPreviousFullInput(t *testing.T) {
	t.Parallel()

	makeItems := func(values ...string) []json.RawMessage {
		items := make([]json.RawMessage, 0, len(values))
		for _, v := range values {
			raw, err := json.Marshal(map[string]any{
				"type": "input_text",
				"text": v,
			})
			require.NoError(t, err)
			items = append(items, json.RawMessage(raw))
		}
		return items
	}

	previous := makeItems("hello", "world")

	t.Run("skip_when_no_previous_response_id", func(t *testing.T) {
		edited, err := openAIWSInputAppearsEditedFromPreviousFullInput(
			previous,
			true,
			[]byte(`{"input":[{"type":"input_text","text":"HELLO_EDITED"},{"type":"input_text","text":"world"}]}`),
			false,
		)
		require.NoError(t, err)
		require.False(t, edited)
	})

	t.Run("skip_when_previous_full_input_missing", func(t *testing.T) {
		edited, err := openAIWSInputAppearsEditedFromPreviousFullInput(
			nil,
			false,
			[]byte(`{"previous_response_id":"resp_1","input":[{"type":"input_text","text":"HELLO_EDITED"},{"type":"input_text","text":"world"}]}`),
			true,
		)
		require.NoError(t, err)
		require.False(t, edited)
	})

	t.Run("error_when_current_payload_invalid", func(t *testing.T) {
		edited, err := openAIWSInputAppearsEditedFromPreviousFullInput(
			previous,
			true,
			[]byte(`{"previous_response_id":"resp_1","input":[}`),
			true,
		)
		require.Error(t, err)
		require.False(t, edited)
	})

	t.Run("skip_when_current_input_missing", func(t *testing.T) {
		edited, err := openAIWSInputAppearsEditedFromPreviousFullInput(
			previous,
			true,
			[]byte(`{"previous_response_id":"resp_1"}`),
			true,
		)
		require.NoError(t, err)
		require.False(t, edited)
	})

	t.Run("skip_when_previous_len_lt_2", func(t *testing.T) {
		edited, err := openAIWSInputAppearsEditedFromPreviousFullInput(
			makeItems("hello"),
			true,
			[]byte(`{"previous_response_id":"resp_1","input":[{"type":"input_text","text":"HELLO_EDITED"}]}`),
			true,
		)
		require.NoError(t, err)
		require.False(t, edited)
	})

	t.Run("skip_when_current_shorter_than_previous", func(t *testing.T) {
		edited, err := openAIWSInputAppearsEditedFromPreviousFullInput(
			previous,
			true,
			[]byte(`{"previous_response_id":"resp_1","input":[{"type":"input_text","text":"world"}]}`),
			true,
		)
		require.NoError(t, err)
		require.False(t, edited)
	})

	t.Run("skip_when_current_has_previous_prefix", func(t *testing.T) {
		edited, err := openAIWSInputAppearsEditedFromPreviousFullInput(
			previous,
			true,
			[]byte(`{"previous_response_id":"resp_1","input":[{"type":"input_text","text":"hello"},{"type":"input_text","text":"world"},{"type":"input_text","text":"new"}]}`),
			true,
		)
		require.NoError(t, err)
		require.False(t, edited)
	})

	t.Run("detect_when_current_is_full_snapshot_edit", func(t *testing.T) {
		edited, err := openAIWSInputAppearsEditedFromPreviousFullInput(
			previous,
			true,
			[]byte(`{"previous_response_id":"resp_1","input":[{"type":"input_text","text":"HELLO_EDITED"},{"type":"input_text","text":"world"}]}`),
			true,
		)
		require.NoError(t, err)
		require.True(t, edited)
	})
}

func TestSetOpenAIWSPayloadInputSequence(t *testing.T) {
	t.Parallel()

	t.Run("set_items", func(t *testing.T) {
		original := []byte(`{"type":"response.create","previous_response_id":"resp_1"}`)
		items := []json.RawMessage{
			json.RawMessage(`{"type":"input_text","text":"hello"}`),
			json.RawMessage(`{"type":"input_text","text":"world"}`),
		}
		updated, err := setOpenAIWSPayloadInputSequence(original, items, true)
		require.NoError(t, err)
		require.Equal(t, "hello", gjson.GetBytes(updated, "input.0.text").String())
		require.Equal(t, "world", gjson.GetBytes(updated, "input.1.text").String())
	})

	t.Run("preserve_empty_array_not_null", func(t *testing.T) {
		original := []byte(`{"type":"response.create","previous_response_id":"resp_1"}`)
		updated, err := setOpenAIWSPayloadInputSequence(original, nil, true)
		require.NoError(t, err)
		require.True(t, gjson.GetBytes(updated, "input").IsArray())
		require.Len(t, gjson.GetBytes(updated, "input").Array(), 0)
		require.False(t, gjson.GetBytes(updated, "input").Type == gjson.Null)
	})
}

func TestCloneOpenAIWSRawMessages(t *testing.T) {
	t.Parallel()

	t.Run("nil_slice", func(t *testing.T) {
		cloned := cloneOpenAIWSRawMessages(nil)
		require.Nil(t, cloned)
	})

	t.Run("empty_slice", func(t *testing.T) {
		items := make([]json.RawMessage, 0)
		cloned := cloneOpenAIWSRawMessages(items)
		require.NotNil(t, cloned)
		require.Len(t, cloned, 0)
	})
}

// ---------------------------------------------------------------------------
// TestInjectPreviousResponseIDForFunctionCallOutput
// 端到端测试：当客户端发送 function_call_output 但未携带 previous_response_id 时，
// Gateway 应主动注入 lastTurnResponseID，避免上游返回 tool_output_not_found 错误。
// ---------------------------------------------------------------------------

func TestInjectPreviousResponseIDForFunctionCallOutput(t *testing.T) {
	t.Parallel()

	// 辅助函数：模拟 forwarder 中的注入逻辑
	// 返回 (注入后的 payload, 注入后的 previousResponseID, 是否执行了注入)
	simulateInject := func(
		storeDisabled bool,
		turn int,
		payload []byte,
		expectedPrev string,
	) ([]byte, string, bool) {
		currentPreviousResponseID := ""
		prev := gjson.GetBytes(payload, "previous_response_id")
		if prev.Exists() {
			currentPreviousResponseID = strings.TrimSpace(prev.String())
		}
		hasFunctionCallOutput := gjson.GetBytes(payload, `input.#(type=="function_call_output")`).Exists()

		if shouldInferIngressFunctionCallOutputPreviousResponseID(
			storeDisabled, turn, hasFunctionCallOutput, currentPreviousResponseID, expectedPrev,
		) {
			injected, err := setPreviousResponseIDToRawPayload(payload, expectedPrev)
			if err != nil {
				return payload, currentPreviousResponseID, false
			}
			return injected, expectedPrev, true
		}
		return payload, currentPreviousResponseID, false
	}

	t.Run("inject_when_function_call_output_without_prev_id", func(t *testing.T) {
		t.Parallel()
		payload := []byte(`{"type":"response.create","model":"gpt-5.1","input":[{"type":"function_call_output","call_id":"call_abc123","output":"result"}]}`)
		updated, prevID, injected := simulateInject(true, 2, payload, "resp_last_turn")

		require.True(t, injected, "应该执行注入")
		require.Equal(t, "resp_last_turn", prevID)
		require.Equal(t, "resp_last_turn", gjson.GetBytes(updated, "previous_response_id").String())
		// 验证原始 input 保持不变
		require.Equal(t, "call_abc123", gjson.GetBytes(updated, `input.0.call_id`).String())
		require.Equal(t, "function_call_output", gjson.GetBytes(updated, `input.0.type`).String())
		require.Equal(t, "gpt-5.1", gjson.GetBytes(updated, "model").String())
	})

	t.Run("skip_when_prev_id_already_present", func(t *testing.T) {
		t.Parallel()
		payload := []byte(`{"type":"response.create","previous_response_id":"resp_client","input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`)
		_, prevID, injected := simulateInject(true, 2, payload, "resp_last_turn")

		require.False(t, injected, "客户端已携带 previous_response_id，不应注入")
		require.Equal(t, "resp_client", prevID)
	})

	t.Run("skip_when_store_enabled", func(t *testing.T) {
		t.Parallel()
		payload := []byte(`{"type":"response.create","input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`)
		_, _, injected := simulateInject(false, 2, payload, "resp_last_turn")

		require.False(t, injected, "store 未禁用时不应注入")
	})

	t.Run("skip_when_no_function_call_output", func(t *testing.T) {
		t.Parallel()
		payload := []byte(`{"type":"response.create","input":[{"type":"input_text","text":"hello"}]}`)
		_, _, injected := simulateInject(true, 2, payload, "resp_last_turn")

		require.False(t, injected, "没有 function_call_output 时不应注入")
	})

	t.Run("skip_when_expected_prev_empty", func(t *testing.T) {
		t.Parallel()
		payload := []byte(`{"type":"response.create","input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`)
		_, _, injected := simulateInject(true, 2, payload, "")

		require.False(t, injected, "没有 expectedPrev 时不应注入")
	})

	t.Run("inject_preserves_multiple_function_call_outputs", func(t *testing.T) {
		t.Parallel()
		payload := []byte(`{"type":"response.create","input":[{"type":"function_call_output","call_id":"call_1","output":"a"},{"type":"function_call_output","call_id":"call_2","output":"b"}]}`)
		updated, prevID, injected := simulateInject(true, 5, payload, "resp_multi")

		require.True(t, injected)
		require.Equal(t, "resp_multi", prevID)
		require.Equal(t, "resp_multi", gjson.GetBytes(updated, "previous_response_id").String())
		outputs := gjson.GetBytes(updated, `input.#(type=="function_call_output")#.call_id`).Array()
		require.Len(t, outputs, 2)
		require.Equal(t, "call_1", outputs[0].String())
		require.Equal(t, "call_2", outputs[1].String())
	})

	t.Run("inject_on_first_turn_with_expected_prev", func(t *testing.T) {
		t.Parallel()
		// turn=1 但有 expectedPrev（可能来自 session state store 恢复），应注入
		payload := []byte(`{"type":"response.create","input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`)
		_, _, injected := simulateInject(true, 1, payload, "resp_restored")

		require.True(t, injected, "turn=1 且有 expectedPrev 时应注入")
	})

	t.Run("inject_updates_payload_bytes_correctly", func(t *testing.T) {
		t.Parallel()
		payload := []byte(`{"type":"response.create","input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`)
		updated, _, injected := simulateInject(true, 3, payload, "resp_check_size")

		require.True(t, injected)
		// 注入后 payload 长度应增加（包含了新的 previous_response_id 字段）
		require.Greater(t, len(updated), len(payload))
		// 验证 JSON 合法性
		require.True(t, json.Valid(updated), "注入后的 payload 应为合法 JSON")
	})

	t.Run("skip_when_turn_zero", func(t *testing.T) {
		t.Parallel()
		payload := []byte(`{"type":"response.create","input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`)
		_, _, injected := simulateInject(true, 0, payload, "resp_1")

		require.False(t, injected, "turn=0 时不应注入")
	})

	t.Run("inject_with_whitespace_expected_prev", func(t *testing.T) {
		t.Parallel()
		payload := []byte(`{"type":"response.create","input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`)
		// shouldInfer 内部会 trim，所以带空格的 expectedPrev 仍然有效
		_, _, injected := simulateInject(true, 2, payload, "  resp_trimmed  ")

		require.True(t, injected, "trim 后非空的 expectedPrev 应触发注入")
	})

	t.Run("skip_when_prev_id_is_whitespace_only", func(t *testing.T) {
		t.Parallel()
		payload := []byte(`{"type":"response.create","input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`)
		_, _, injected := simulateInject(true, 2, payload, "   ")

		require.False(t, injected, "纯空白的 expectedPrev 不应触发注入")
	})
}
