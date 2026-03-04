package service

import (
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestOpenAIWSIngressTurnPartialResult_NotTurnError(t *testing.T) {
	result, ok := OpenAIWSIngressTurnPartialResult(errors.New("plain error"))
	require.False(t, ok)
	require.Nil(t, result)
}

func TestOpenAIWSIngressTurnPartialResult_DeepCopy(t *testing.T) {
	partial := &OpenAIForwardResult{
		RequestID: "resp_partial",
		Usage: OpenAIUsage{
			InputTokens:  12,
			OutputTokens: 34,
		},
		PendingFunctionCallIDs: []string{"call_1", "call_2"},
	}
	err := wrapOpenAIWSIngressTurnErrorWithPartial("read_upstream", errors.New("boom"), false, partial)

	got, ok := OpenAIWSIngressTurnPartialResult(err)
	require.True(t, ok)
	require.NotNil(t, got)
	require.Equal(t, partial.RequestID, got.RequestID)
	require.Equal(t, partial.Usage, got.Usage)
	require.Equal(t, partial.PendingFunctionCallIDs, got.PendingFunctionCallIDs)

	// mutate returned copy should not affect stored partial result
	got.PendingFunctionCallIDs[0] = "changed"
	again, ok := OpenAIWSIngressTurnPartialResult(err)
	require.True(t, ok)
	require.Equal(t, "call_1", again.PendingFunctionCallIDs[0])
}

func TestWrapOpenAIWSIngressTurnErrorWithPartial_Exported(t *testing.T) {
	partial := &OpenAIForwardResult{
		RequestID: "resp_exported",
		Usage: OpenAIUsage{
			InputTokens: 7,
		},
	}
	err := WrapOpenAIWSIngressTurnErrorWithPartial("client_disconnected", errors.New("boom"), false, partial)
	require.Error(t, err)
	got, ok := OpenAIWSIngressTurnPartialResult(err)
	require.True(t, ok)
	require.NotNil(t, got)
	require.Equal(t, "resp_exported", got.RequestID)
	require.Equal(t, 7, got.Usage.InputTokens)
}

func TestOpenAIWSClientReadIdleTimeout_DefaultAndConfig(t *testing.T) {
	svc := &OpenAIGatewayService{}
	require.Equal(t, 30*time.Minute, svc.openAIWSClientReadIdleTimeout())

	svc.cfg = &config.Config{}
	svc.cfg.Gateway.OpenAIWS.ClientReadIdleTimeoutSeconds = 1800
	require.Equal(t, 30*time.Minute, svc.openAIWSClientReadIdleTimeout())

	svc.cfg.Gateway.OpenAIWS.ClientReadIdleTimeoutSeconds = 120
	require.Equal(t, 120*time.Second, svc.openAIWSClientReadIdleTimeout())
}

func TestOpenAIWSPassthroughIdleTimeout_DefaultAndConfig(t *testing.T) {
	svc := &OpenAIGatewayService{}
	require.Equal(t, time.Hour, svc.openAIWSPassthroughIdleTimeout())

	svc.cfg = &config.Config{}
	svc.cfg.Gateway.OpenAIWS.ClientReadIdleTimeoutSeconds = 120
	require.Equal(t, 120*time.Second, svc.openAIWSPassthroughIdleTimeout())
}
