package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestBuildOpenAIWSIngressPartialResult_ZeroUsageStillReturnsResult(t *testing.T) {
	t.Parallel()

	startAt := time.Now().Add(-20 * time.Millisecond)
	result := buildOpenAIWSIngressPartialResult(
		"",
		OpenAIUsage{},
		"gpt-5.1",
		[]byte(`{"model":"gpt-5.1","stream":true}`),
		true,
		startAt,
		nil,
		"client_disconnected",
	)
	require.NotNil(t, result)
	require.Equal(t, 0, result.Usage.InputTokens)
	require.Equal(t, 0, result.Usage.OutputTokens)
	require.Equal(t, OpenAIWSIngressModeCtxPool, result.WSIngressMode)
	require.Equal(t, "client_disconnected", result.TerminalEventType)
	require.GreaterOrEqual(t, result.Duration, time.Duration(0))
}

func TestBuildOpenAIWSIngressPartialResult_ClonesCoreFields(t *testing.T) {
	t.Parallel()

	firstToken := 8
	startAt := time.Now().Add(-50 * time.Millisecond)
	usage := OpenAIUsage{
		InputTokens:              12,
		OutputTokens:             7,
		CacheCreationInputTokens: 3,
		CacheReadInputTokens:     2,
	}
	result := buildOpenAIWSIngressPartialResult(
		"resp_partial",
		usage,
		"gpt-5.3-codex",
		[]byte(`{"model":"gpt-5.3-codex","reasoning":{"effort":"medium"}}`),
		false,
		startAt,
		&firstToken,
		" response.failed ",
	)
	require.NotNil(t, result)
	require.Equal(t, "resp_partial", result.RequestID)
	require.Equal(t, usage, result.Usage)
	require.Equal(t, "gpt-5.3-codex", result.Model)
	require.NotNil(t, result.FirstTokenMs)
	require.Equal(t, 8, *result.FirstTokenMs)
	require.Equal(t, "response.failed", result.TerminalEventType)
	require.Greater(t, result.Duration, 0*time.Millisecond)
}

func TestBuildOpenAIWSIngressPartialResult_FutureStartTimeClampsDuration(t *testing.T) {
	t.Parallel()

	startAt := time.Now().Add(2 * time.Second)
	result := buildOpenAIWSIngressPartialResult(
		"resp_future",
		OpenAIUsage{},
		"gpt-5.1",
		[]byte(`{"model":"gpt-5.1","stream":true}`),
		true,
		startAt,
		nil,
		"response.failed",
	)
	require.NotNil(t, result)
	require.Equal(t, time.Duration(0), result.Duration)
}

func TestNewOpenAIWSIngressPartialResultBuilder_TracksLatestState(t *testing.T) {
	t.Parallel()

	responseID := "resp_a"
	usage := OpenAIUsage{InputTokens: 5}
	firstToken := 3
	firstTokenPtr := &firstToken
	startAt := time.Now().Add(-10 * time.Millisecond)

	builder := newOpenAIWSIngressPartialResultBuilder(
		&responseID,
		&usage,
		"gpt-5.1",
		[]byte(`{"model":"gpt-5.1","stream":true}`),
		true,
		startAt,
		&firstTokenPtr,
	)
	require.NotNil(t, builder)

	resultA := builder("response.failed")
	require.Equal(t, "resp_a", resultA.RequestID)
	require.Equal(t, 5, resultA.Usage.InputTokens)
	require.NotNil(t, resultA.FirstTokenMs)
	require.Equal(t, 3, *resultA.FirstTokenMs)

	responseID = "resp_b"
	usage = OpenAIUsage{InputTokens: 8, OutputTokens: 2}
	nextFirstToken := 9
	firstTokenPtr = &nextFirstToken

	resultB := builder("response.completed")
	require.Equal(t, "resp_b", resultB.RequestID)
	require.Equal(t, 8, resultB.Usage.InputTokens)
	require.Equal(t, 2, resultB.Usage.OutputTokens)
	require.NotNil(t, resultB.FirstTokenMs)
	require.Equal(t, 9, *resultB.FirstTokenMs)
}

func TestNewOpenAIWSIngressPartialResultBuilder_NilPointersFallbackToZeroValues(t *testing.T) {
	t.Parallel()

	builder := newOpenAIWSIngressPartialResultBuilder(
		nil,
		nil,
		"gpt-5.1",
		[]byte(`{"model":"gpt-5.1","stream":false}`),
		false,
		time.Now().Add(-5*time.Millisecond),
		nil,
	)
	require.NotNil(t, builder)

	result := builder("client_disconnected")
	require.Equal(t, "", result.RequestID)
	require.Equal(t, 0, result.Usage.InputTokens)
	require.Nil(t, result.FirstTokenMs)
	require.Equal(t, "client_disconnected", result.TerminalEventType)
}
