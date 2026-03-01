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

func TestOpenAIWSClientReadIdleTimeout_DefaultAndConfig(t *testing.T) {
	svc := &OpenAIGatewayService{}
	require.Equal(t, 30*time.Minute, svc.openAIWSClientReadIdleTimeout())

	svc.cfg = &config.Config{}
	svc.cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 1800
	require.Equal(t, 30*time.Minute, svc.openAIWSClientReadIdleTimeout())

	svc.cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 120
	require.Equal(t, 120*time.Second, svc.openAIWSClientReadIdleTimeout())
}
