package service

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

// --- 包级常量验证 ---

func TestSSEPackageLevelConstants(t *testing.T) {
	require.Equal(t, []byte("[DONE]"), sseDataDone)
	require.Equal(t, []byte(`"response.completed"`), sseResponseCompletedMark)
	require.Equal(t, []byte(`"response.done"`), sseResponseDoneMark)
	require.Equal(t, []byte(`"response.failed"`), sseResponseFailedMark)
	require.Equal(t, []byte(`"type":"response.completed"`), sseTypeResponseCompleted)
	require.Equal(t, []byte(`"type":"response.done"`), sseTypeResponseDone)
	require.Equal(t, []byte(`"type":"response.failed"`), sseTypeResponseFailed)
}

func TestSSEDataDone_UsedInBytesEqual(t *testing.T) {
	require.True(t, bytes.Equal([]byte("[DONE]"), sseDataDone))
	require.False(t, bytes.Equal([]byte("[done]"), sseDataDone))
	require.False(t, bytes.Equal([]byte(""), sseDataDone))
}

func TestSSEResponseTypeMarks_UsedInBytesContains(t *testing.T) {
	completed := []byte(`{"type":"response.completed","response":{"usage":{}}}`)
	require.True(t, bytes.Contains(completed, sseResponseCompletedMark))
	require.False(t, bytes.Contains(completed, sseResponseDoneMark))
	require.False(t, bytes.Contains(completed, sseResponseFailedMark))

	done := []byte(`{"type":"response.done","response":{"usage":{}}}`)
	require.True(t, bytes.Contains(done, sseResponseDoneMark))

	failed := []byte(`{"type":"response.failed","response":{"usage":{}}}`)
	require.True(t, bytes.Contains(failed, sseResponseFailedMark))

	unrelated := []byte(`{"type":"response.in_progress"}`)
	require.False(t, bytes.Contains(unrelated, sseResponseCompletedMark))
	require.False(t, bytes.Contains(unrelated, sseResponseDoneMark))
	require.False(t, bytes.Contains(unrelated, sseResponseFailedMark))
}

func TestOpenAIResponsesSSEUsageEventHelpers(t *testing.T) {
	t.Run("event type selector", func(t *testing.T) {
		require.True(t, openAIResponsesSSEEventShouldParseUsage("response.completed"))
		require.True(t, openAIResponsesSSEEventShouldParseUsage(" response.done "))
		require.True(t, openAIResponsesSSEEventShouldParseUsage("response.failed"))
		require.False(t, openAIResponsesSSEEventShouldParseUsage("response.in_progress"))
		require.False(t, openAIResponsesSSEEventShouldParseUsage(""))
	})

	t.Run("string marker selector", func(t *testing.T) {
		require.True(t, openAIResponsesSSEStringMayContainUsageEvent(`{"type":"response.completed"}`))
		require.True(t, openAIResponsesSSEStringMayContainUsageEvent(`{"type":"response.done"}`))
		require.True(t, openAIResponsesSSEStringMayContainUsageEvent(`{"type":"response.failed"}`))
		require.False(t, openAIResponsesSSEStringMayContainUsageEvent(`{"type":"response.in_progress"}`))
		require.False(t, openAIResponsesSSEStringMayContainUsageEvent(""))
	})

	t.Run("bytes marker selector", func(t *testing.T) {
		require.True(t, openAIResponsesSSEBytesMayContainUsageEvent([]byte(`{"type":"response.completed"}`)))
		require.True(t, openAIResponsesSSEBytesMayContainUsageEvent([]byte(`{"type":"response.done"}`)))
		require.True(t, openAIResponsesSSEBytesMayContainUsageEvent([]byte(`{"type":"response.failed"}`)))
		require.False(t, openAIResponsesSSEBytesMayContainUsageEvent([]byte(`{"type":"response.in_progress"}`)))
		require.False(t, openAIResponsesSSEBytesMayContainUsageEvent(nil))
	})

	t.Run("canonical terminal type selector", func(t *testing.T) {
		require.True(t, openAIResponsesSSEStringHasCanonicalTerminalType(`{"type":"response.completed"}`))
		require.True(t, openAIResponsesSSEStringHasCanonicalTerminalType(`{"type":"response.done"}`))
		require.True(t, openAIResponsesSSEStringHasCanonicalTerminalType(`{"type":"response.failed"}`))
		require.False(t, openAIResponsesSSEStringHasCanonicalTerminalType(`{"type":"response.in_progress"}`))
		require.False(t, openAIResponsesSSEStringHasCanonicalTerminalType(""))

		require.True(t, openAIResponsesSSEBytesHasCanonicalTerminalType([]byte(`{"type":"response.completed"}`)))
		require.True(t, openAIResponsesSSEBytesHasCanonicalTerminalType([]byte(`{"type":"response.done"}`)))
		require.True(t, openAIResponsesSSEBytesHasCanonicalTerminalType([]byte(`{"type":"response.failed"}`)))
		require.False(t, openAIResponsesSSEBytesHasCanonicalTerminalType([]byte(`{"type":"response.in_progress"}`)))
		require.False(t, openAIResponsesSSEBytesHasCanonicalTerminalType(nil))
	})
}

func TestOpenAIResponsesSSETokenEventHelpers(t *testing.T) {
	t.Run("event type selector", func(t *testing.T) {
		require.False(t, openAIResponsesSSEEventIsTokenEvent("response.created"))
		require.False(t, openAIResponsesSSEEventIsTokenEvent("response.in_progress"))
		require.False(t, openAIResponsesSSEEventIsTokenEvent("response.output_item.added"))
		require.False(t, openAIResponsesSSEEventIsTokenEvent("response.output_item.done"))
		require.False(t, openAIResponsesSSEEventIsTokenEvent("response.completed"))
		require.False(t, openAIResponsesSSEEventIsTokenEvent("response.done"))
		require.True(t, openAIResponsesSSEEventIsTokenEvent("response.output_text.delta"))
		require.True(t, openAIResponsesSSEEventIsTokenEvent("response.output_audio.delta"))
		require.True(t, openAIResponsesSSEEventIsTokenEvent("response.output_text.done"))
	})

	t.Run("string and bytes selector", func(t *testing.T) {
		require.True(t, openAIResponsesSSEStringMayContainTokenEvent(`{"type":"response.output_text.delta","delta":"h"}`))
		require.True(t, openAIResponsesSSEBytesMayContainTokenEvent([]byte(`{"type":"response.output_text.delta","delta":"h"}`)))
		require.False(t, openAIResponsesSSEStringMayContainTokenEvent(`{"type":"response.created"}`))
		require.False(t, openAIResponsesSSEBytesMayContainTokenEvent([]byte(`{"type":"response.created"}`)))
		require.False(t, openAIResponsesSSEStringMayContainTokenEvent(""))
		require.False(t, openAIResponsesSSEBytesMayContainTokenEvent(nil))
	})

	t.Run("payload detector", func(t *testing.T) {
		require.True(t, openAIResponsesSSEStringIsTokenEvent(`{"type":"response.output_text.delta","delta":"h"}`))
		require.True(t, openAIResponsesSSEBytesIsTokenEvent([]byte(`{"type":"response.output_text.delta","delta":"h"}`)))
		require.False(t, openAIResponsesSSEStringIsTokenEvent(`{"type":"response.created"}`))
		require.False(t, openAIResponsesSSEBytesIsTokenEvent([]byte(`{"type":"response.created"}`)))
		require.False(t, openAIResponsesSSEStringIsTokenEvent(`{"type":"response.completed"}`))
		require.False(t, openAIResponsesSSEBytesIsTokenEvent([]byte(`{"type":"response.completed"}`)))
		require.False(t, openAIResponsesSSEStringIsTokenEvent("[DONE]"))
		require.False(t, openAIResponsesSSEBytesIsTokenEvent([]byte("[DONE]")))
		require.False(t, openAIResponsesSSEStringIsTokenEvent(""))
		require.False(t, openAIResponsesSSEBytesIsTokenEvent(nil))
	})
}

// --- parseSSEUsageString 测试 ---

func TestParseSSEUsageString_CompletedEvent(t *testing.T) {
	svc := &OpenAIGatewayService{}
	usage := &OpenAIUsage{}

	data := `{"type":"response.completed","response":{"usage":{"input_tokens":100,"output_tokens":50,"input_tokens_details":{"cached_tokens":20}}}}`
	svc.parseSSEUsageString(data, usage)

	require.Equal(t, 100, usage.InputTokens)
	require.Equal(t, 50, usage.OutputTokens)
	require.Equal(t, 20, usage.CacheReadInputTokens)
}

func TestParseSSEUsageString_NonTerminalUsageEvent(t *testing.T) {
	svc := &OpenAIGatewayService{}
	usage := &OpenAIUsage{InputTokens: 99, OutputTokens: 88}

	data := `{"type":"response.in_progress","response":{"usage":{"input_tokens":1,"output_tokens":2,"input_tokens_details":{"cached_tokens":3}}}}`
	svc.parseSSEUsageString(data, usage)

	// 非 terminal usage 事件不应修改 usage
	require.Equal(t, 99, usage.InputTokens)
	require.Equal(t, 88, usage.OutputTokens)
}

func TestParseSSEUsageString_DoneTerminalEvent(t *testing.T) {
	svc := &OpenAIGatewayService{}
	usage := &OpenAIUsage{}

	data := `{"type":"response.done","response":{"usage":{"input_tokens":8,"output_tokens":5,"input_tokens_details":{"cached_tokens":2}}}}`
	svc.parseSSEUsageString(data, usage)

	require.Equal(t, 8, usage.InputTokens)
	require.Equal(t, 5, usage.OutputTokens)
	require.Equal(t, 2, usage.CacheReadInputTokens)
}

func TestParseSSEUsageString_FailedTerminalEvent(t *testing.T) {
	svc := &OpenAIGatewayService{}
	usage := &OpenAIUsage{}

	data := `{"type":"response.failed","response":{"usage":{"input_tokens":6,"output_tokens":4,"input_tokens_details":{"cached_tokens":1}}}}`
	svc.parseSSEUsageString(data, usage)

	require.Equal(t, 6, usage.InputTokens)
	require.Equal(t, 4, usage.OutputTokens)
	require.Equal(t, 1, usage.CacheReadInputTokens)
}

func TestParseSSEUsageString_SpacedTypeFallbackToGJSON(t *testing.T) {
	svc := &OpenAIGatewayService{}
	usage := &OpenAIUsage{}

	data := `{"type" : "response.done","response":{"usage":{"input_tokens":16,"output_tokens":9,"input_tokens_details":{"cached_tokens":4}}}}`
	svc.parseSSEUsageString(data, usage)

	require.Equal(t, 16, usage.InputTokens)
	require.Equal(t, 9, usage.OutputTokens)
	require.Equal(t, 4, usage.CacheReadInputTokens)
}

func TestParseSSEUsageString_DoneEvent(t *testing.T) {
	svc := &OpenAIGatewayService{}
	usage := &OpenAIUsage{InputTokens: 10}

	svc.parseSSEUsageString("[DONE]", usage)
	require.Equal(t, 10, usage.InputTokens) // 不应修改
}

func TestParseSSEUsageString_EmptyString(t *testing.T) {
	svc := &OpenAIGatewayService{}
	usage := &OpenAIUsage{InputTokens: 5}

	svc.parseSSEUsageString("", usage)
	require.Equal(t, 5, usage.InputTokens) // 不应修改
}

func TestParseSSEUsageString_NilUsage(t *testing.T) {
	svc := &OpenAIGatewayService{}

	// 不应 panic
	require.NotPanics(t, func() {
		svc.parseSSEUsageString(`{"type":"response.completed"}`, nil)
	})
}

func TestParseSSEUsageString_ShortData(t *testing.T) {
	svc := &OpenAIGatewayService{}
	usage := &OpenAIUsage{InputTokens: 7}

	// 短于 80 字节的数据直接跳过
	svc.parseSSEUsageString(`{"type":"response.completed"}`, usage)
	require.Equal(t, 7, usage.InputTokens) // 不应修改
}

func TestParseSSEUsageString_ContainsCompletedButWrongType(t *testing.T) {
	svc := &OpenAIGatewayService{}
	usage := &OpenAIUsage{InputTokens: 42}

	// 包含 "response.completed" 子串但 type 字段不匹配
	data := `{"type":"response.in_progress","description":"not response.completed at all","padding":"aaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`
	svc.parseSSEUsageString(data, usage)
	require.Equal(t, 42, usage.InputTokens) // 不应修改
}

func TestParseSSEUsageString_ZeroUsageValues(t *testing.T) {
	svc := &OpenAIGatewayService{}
	usage := &OpenAIUsage{InputTokens: 99}

	data := `{"type":"response.completed","response":{"usage":{"input_tokens":0,"output_tokens":0,"input_tokens_details":{"cached_tokens":0}}}}`
	svc.parseSSEUsageString(data, usage)

	require.Equal(t, 0, usage.InputTokens)
	require.Equal(t, 0, usage.OutputTokens)
	require.Equal(t, 0, usage.CacheReadInputTokens)
}

func TestParseSSEUsageString_MissingUsageFields(t *testing.T) {
	svc := &OpenAIGatewayService{}
	usage := &OpenAIUsage{}

	// response.usage 存在但缺少某些子字段
	data := `{"type":"response.completed","response":{"usage":{"input_tokens":10},"padding":"aaaaaaaaaaaaaaaaaaa"}}`
	svc.parseSSEUsageString(data, usage)

	require.Equal(t, 10, usage.InputTokens)
	require.Equal(t, 0, usage.OutputTokens)
	require.Equal(t, 0, usage.CacheReadInputTokens)
}

// --- parseSSEUsageBytes 与包级常量集成测试 ---

func TestParseSSEUsageBytes_CompletedEvent(t *testing.T) {
	svc := &OpenAIGatewayService{}
	usage := &OpenAIUsage{}

	data := []byte(`{"type":"response.completed","response":{"usage":{"input_tokens":200,"output_tokens":80,"input_tokens_details":{"cached_tokens":30}}}}`)
	svc.parseSSEUsageBytes(data, usage)

	require.Equal(t, 200, usage.InputTokens)
	require.Equal(t, 80, usage.OutputTokens)
	require.Equal(t, 30, usage.CacheReadInputTokens)
}

func TestParseSSEUsageBytes_DoneEvent(t *testing.T) {
	svc := &OpenAIGatewayService{}
	usage := &OpenAIUsage{InputTokens: 10}

	svc.parseSSEUsageBytes([]byte("[DONE]"), usage)
	require.Equal(t, 10, usage.InputTokens) // 不应修改
}

func TestParseSSEUsageBytes_EmptyData(t *testing.T) {
	svc := &OpenAIGatewayService{}
	usage := &OpenAIUsage{InputTokens: 5}

	svc.parseSSEUsageBytes(nil, usage)
	require.Equal(t, 5, usage.InputTokens)

	svc.parseSSEUsageBytes([]byte{}, usage)
	require.Equal(t, 5, usage.InputTokens)
}

func TestParseSSEUsageBytes_NilUsage(t *testing.T) {
	svc := &OpenAIGatewayService{}

	require.NotPanics(t, func() {
		svc.parseSSEUsageBytes([]byte(`{"type":"response.completed"}`), nil)
	})
}

func TestParseSSEUsageBytes_ShortData(t *testing.T) {
	svc := &OpenAIGatewayService{}
	usage := &OpenAIUsage{InputTokens: 7}

	svc.parseSSEUsageBytes([]byte(`{"type":"response.completed"}`), usage)
	require.Equal(t, 7, usage.InputTokens)
}

func TestParseSSEUsageBytes_NonTerminalUsageEvent(t *testing.T) {
	svc := &OpenAIGatewayService{}
	usage := &OpenAIUsage{InputTokens: 99}

	data := []byte(`{"type":"response.in_progress","response":{"usage":{"input_tokens":1,"output_tokens":2,"input_tokens_details":{"cached_tokens":3}}},"pad":"xxxx"}`)
	svc.parseSSEUsageBytes(data, usage)

	require.Equal(t, 99, usage.InputTokens)
}

func TestParseSSEUsageBytes_DoneTerminalEvent(t *testing.T) {
	svc := &OpenAIGatewayService{}
	usage := &OpenAIUsage{}

	data := []byte(`{"type":"response.done","response":{"usage":{"input_tokens":21,"output_tokens":13,"input_tokens_details":{"cached_tokens":5}}}}`)
	svc.parseSSEUsageBytes(data, usage)

	require.Equal(t, 21, usage.InputTokens)
	require.Equal(t, 13, usage.OutputTokens)
	require.Equal(t, 5, usage.CacheReadInputTokens)
}

func TestParseSSEUsageBytes_FailedTerminalEvent(t *testing.T) {
	svc := &OpenAIGatewayService{}
	usage := &OpenAIUsage{}

	data := []byte(`{"type":"response.failed","response":{"usage":{"input_tokens":9,"output_tokens":3,"input_tokens_details":{"cached_tokens":1}}}}`)
	svc.parseSSEUsageBytes(data, usage)

	require.Equal(t, 9, usage.InputTokens)
	require.Equal(t, 3, usage.OutputTokens)
	require.Equal(t, 1, usage.CacheReadInputTokens)
}

func TestParseSSEUsageBytes_SpacedTypeFallbackToGJSON(t *testing.T) {
	svc := &OpenAIGatewayService{}
	usage := &OpenAIUsage{}

	data := []byte(`{"type" : "response.failed","response":{"usage":{"input_tokens":12,"output_tokens":7,"input_tokens_details":{"cached_tokens":3}}}}`)
	svc.parseSSEUsageBytes(data, usage)

	require.Equal(t, 12, usage.InputTokens)
	require.Equal(t, 7, usage.OutputTokens)
	require.Equal(t, 3, usage.CacheReadInputTokens)
}

func TestParseSSEUsageBytes_GetManyBytesExtraction(t *testing.T) {
	svc := &OpenAIGatewayService{}
	usage := &OpenAIUsage{}

	// 验证 GetManyBytes 一次提取 3 个字段的正确性
	data := []byte(`{"type":"response.completed","response":{"usage":{"input_tokens":111,"output_tokens":222,"input_tokens_details":{"cached_tokens":333}}}}`)
	svc.parseSSEUsageBytes(data, usage)

	require.Equal(t, 111, usage.InputTokens)
	require.Equal(t, 222, usage.OutputTokens)
	require.Equal(t, 333, usage.CacheReadInputTokens)
}

// --- parseSSEUsage wrapper 测试 ---

func TestParseSSEUsage_DelegatesToString(t *testing.T) {
	svc := &OpenAIGatewayService{}
	usage := &OpenAIUsage{}

	// 验证 parseSSEUsage 最终正确提取 usage
	data := `{"type":"response.completed","response":{"usage":{"input_tokens":55,"output_tokens":66,"input_tokens_details":{"cached_tokens":77}}}}`
	svc.parseSSEUsage(data, usage)

	require.Equal(t, 55, usage.InputTokens)
	require.Equal(t, 66, usage.OutputTokens)
	require.Equal(t, 77, usage.CacheReadInputTokens)
}

func TestParseSSEUsage_DoneNotParsed(t *testing.T) {
	svc := &OpenAIGatewayService{}
	usage := &OpenAIUsage{InputTokens: 123}

	svc.parseSSEUsage("[DONE]", usage)
	require.Equal(t, 123, usage.InputTokens)
}

func TestParseSSEUsage_EmptyNotParsed(t *testing.T) {
	svc := &OpenAIGatewayService{}
	usage := &OpenAIUsage{InputTokens: 456}

	svc.parseSSEUsage("", usage)
	require.Equal(t, 456, usage.InputTokens)
}

// --- string 和 bytes 一致性测试 ---

func TestParseSSEUsage_StringAndBytesConsistency(t *testing.T) {
	svc := &OpenAIGatewayService{}

	completedData := `{"type":"response.completed","response":{"usage":{"input_tokens":300,"output_tokens":150,"input_tokens_details":{"cached_tokens":50}}}}`

	usageStr := &OpenAIUsage{}
	svc.parseSSEUsageString(completedData, usageStr)

	usageBytes := &OpenAIUsage{}
	svc.parseSSEUsageBytes([]byte(completedData), usageBytes)

	require.Equal(t, usageStr.InputTokens, usageBytes.InputTokens)
	require.Equal(t, usageStr.OutputTokens, usageBytes.OutputTokens)
	require.Equal(t, usageStr.CacheReadInputTokens, usageBytes.CacheReadInputTokens)
}

func TestParseSSEUsage_StringAndBytesConsistency_NonTerminalUsageEvent(t *testing.T) {
	svc := &OpenAIGatewayService{}

	inProgressData := `{"type":"response.in_progress","response":{"usage":{"input_tokens":1,"output_tokens":2,"input_tokens_details":{"cached_tokens":3}}},"pad":"xxx"}`

	usageStr := &OpenAIUsage{InputTokens: 10}
	svc.parseSSEUsageString(inProgressData, usageStr)

	usageBytes := &OpenAIUsage{InputTokens: 10}
	svc.parseSSEUsageBytes([]byte(inProgressData), usageBytes)

	// 两者都不应修改
	require.Equal(t, 10, usageStr.InputTokens)
	require.Equal(t, 10, usageBytes.InputTokens)
}

func TestParseSSEUsage_StringAndBytesConsistency_LargeTokenCounts(t *testing.T) {
	svc := &OpenAIGatewayService{}

	data := `{"type":"response.completed","response":{"usage":{"input_tokens":1000000,"output_tokens":500000,"input_tokens_details":{"cached_tokens":200000}}}}`

	usageStr := &OpenAIUsage{}
	svc.parseSSEUsageString(data, usageStr)

	usageBytes := &OpenAIUsage{}
	svc.parseSSEUsageBytes([]byte(data), usageBytes)

	require.Equal(t, 1000000, usageStr.InputTokens)
	require.Equal(t, usageStr, usageBytes)
}
