package service

import (
	"context"
	"errors"
	"io"
	"net"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	coderws "github.com/coder/websocket"
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
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, isOpenAIWSClientDisconnectError(tt.err))
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
