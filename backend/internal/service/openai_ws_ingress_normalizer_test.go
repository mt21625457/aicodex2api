package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// === 纯透传 normalizer 行为测试 ===

func TestNormalizeOpenAIWSIngressPayloadBeforeSend_BasicPassthrough(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"type":"response.create",
		"model":"gpt-5.1",
		"previous_response_id":"resp_prev",
		"input":[{"type":"input_text","text":"hello"}]
	}`)

	out := normalizeOpenAIWSIngressPayloadBeforeSend(openAIWSIngressPreSendNormalizeInput{
		accountID:                 1,
		turn:                      2,
		connID:                    "conn_1",
		currentPayload:            payload,
		currentPayloadBytes:       len(payload),
		currentPreviousResponseID: "resp_prev",
		expectedPreviousResponse:  "resp_expected",
		pendingExpectedCallIDs:    []string{"call_1"},
	})

	require.JSONEq(t, string(payload), string(out.currentPayload), "payload 应原样透传")
	require.Equal(t, len(payload), out.currentPayloadBytes)
	require.Equal(t, "resp_prev", out.currentPreviousResponseID, "currentPreviousResponseID 应原样透传")
	require.Equal(t, "resp_expected", out.expectedPreviousResponseID, "expectedPreviousResponseID 应原样透传")
	require.Equal(t, []string{"call_1"}, out.pendingExpectedCallIDs, "pendingExpectedCallIDs 应原样透传")
	require.False(t, out.hasFunctionCallOutputCallID, "无 FCO 时应为 false")
	require.Empty(t, out.functionCallOutputCallIDs)
}

func TestNormalizeOpenAIWSIngressPayloadBeforeSend_ExtractsCallID(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"type":"response.create",
		"model":"gpt-5.1",
		"previous_response_id":"resp_prev",
		"input":[{"type":"function_call_output","call_id":"call_abc","output":"{}"}]
	}`)

	out := normalizeOpenAIWSIngressPayloadBeforeSend(openAIWSIngressPreSendNormalizeInput{
		accountID:                 2,
		turn:                      3,
		connID:                    "conn_2",
		currentPayload:            payload,
		currentPayloadBytes:       len(payload),
		currentPreviousResponseID: "resp_prev",
		expectedPreviousResponse:  "resp_prev",
	})

	require.True(t, out.hasFunctionCallOutputCallID, "有 FCO 时应为 true")
	require.Equal(t, []string{"call_abc"}, out.functionCallOutputCallIDs, "应正确提取 call_id")
}

func TestNormalizeOpenAIWSIngressPayloadBeforeSend_NoFCO(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"type":"response.create",
		"model":"gpt-5.1",
		"input":[{"type":"input_text","text":"hello"}]
	}`)

	out := normalizeOpenAIWSIngressPayloadBeforeSend(openAIWSIngressPreSendNormalizeInput{
		accountID:                 3,
		turn:                      1,
		connID:                    "conn_3",
		currentPayload:            payload,
		currentPayloadBytes:       len(payload),
		currentPreviousResponseID: "",
		expectedPreviousResponse:  "",
	})

	require.False(t, out.hasFunctionCallOutputCallID)
	require.Empty(t, out.functionCallOutputCallIDs)
}

func TestNormalizeOpenAIWSIngressPayloadBeforeSend_MultipleFCO(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"type":"response.create",
		"model":"gpt-5.1",
		"previous_response_id":"resp_ok",
		"input":[
			{"type":"function_call_output","call_id":"call_a","output":"{}"},
			{"type":"function_call_output","call_id":"call_b","output":"{}"},
			{"type":"function_call_output","call_id":"call_c","output":"{}"}
		]
	}`)

	out := normalizeOpenAIWSIngressPayloadBeforeSend(openAIWSIngressPreSendNormalizeInput{
		accountID:                 4,
		turn:                      2,
		connID:                    "conn_4",
		currentPayload:            payload,
		currentPayloadBytes:       len(payload),
		currentPreviousResponseID: "resp_ok",
		expectedPreviousResponse:  "resp_ok",
	})

	require.True(t, out.hasFunctionCallOutputCallID)
	require.ElementsMatch(t, []string{"call_a", "call_b", "call_c"}, out.functionCallOutputCallIDs, "应提取所有 call_id")
}

func TestNormalizeOpenAIWSIngressPayloadBeforeSend_EmptyInput(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"type":"response.create","model":"gpt-5.1","input":[]}`)

	out := normalizeOpenAIWSIngressPayloadBeforeSend(openAIWSIngressPreSendNormalizeInput{
		accountID:                 5,
		turn:                      1,
		connID:                    "conn_5",
		currentPayload:            payload,
		currentPayloadBytes:       len(payload),
		currentPreviousResponseID: "",
		expectedPreviousResponse:  "",
	})

	require.JSONEq(t, string(payload), string(out.currentPayload), "空 input 不应 panic")
	require.False(t, out.hasFunctionCallOutputCallID)
	require.Empty(t, out.functionCallOutputCallIDs)
}

func TestNormalizeOpenAIWSIngressPayloadBeforeSend_ESCInterruptPassthrough(t *testing.T) {
	t.Parallel()

	// 场景：ESC 中断后客户端有意不传 previous_response_id，有 pendingCallIDs。
	// 透传不补 prev、不注入 aborted output。
	payload := []byte(`{
		"type":"response.create",
		"model":"gpt-5.1",
		"input":[{"type":"input_text","text":"new task after ESC"}]
	}`)

	out := normalizeOpenAIWSIngressPayloadBeforeSend(openAIWSIngressPreSendNormalizeInput{
		accountID:                 6,
		turn:                      5,
		connID:                    "conn_esc",
		currentPayload:            payload,
		currentPayloadBytes:       len(payload),
		currentPreviousResponseID: "",
		expectedPreviousResponse:  "resp_prev_turn4",
		pendingExpectedCallIDs:    []string{"call_pending_1", "call_pending_2"},
	})

	require.Empty(t, out.currentPreviousResponseID, "透传不应补 previous_response_id")
	require.Equal(t, "resp_prev_turn4", out.expectedPreviousResponseID)
	require.False(t, out.hasFunctionCallOutputCallID, "透传不应注入 function_call_output")
	require.Empty(t, out.functionCallOutputCallIDs)
	require.Equal(t, []string{"call_pending_1", "call_pending_2"}, out.pendingExpectedCallIDs, "pendingExpectedCallIDs 应原样传递")
	require.JSONEq(t, string(payload), string(out.currentPayload), "payload 应原样透传")
}

func TestNormalizeOpenAIWSIngressPayloadBeforeSend_StalePrevPassthrough(t *testing.T) {
	t.Parallel()

	// 场景：客户端传了过期 previous_response_id，透传不对齐。
	// 由下游 recoverIngressPrevResponseNotFound 处理。
	payload := []byte(`{
		"type":"response.create",
		"model":"gpt-5.1",
		"previous_response_id":"resp_stale",
		"input":[{"type":"function_call_output","call_id":"call_1","output":"{}"}]
	}`)

	out := normalizeOpenAIWSIngressPayloadBeforeSend(openAIWSIngressPreSendNormalizeInput{
		accountID:                 7,
		turn:                      4,
		connID:                    "conn_stale",
		currentPayload:            payload,
		currentPayloadBytes:       len(payload),
		currentPreviousResponseID: "resp_stale",
		expectedPreviousResponse:  "resp_latest",
	})

	require.Equal(t, "resp_stale", out.currentPreviousResponseID, "透传不应对齐 previous_response_id")
	require.Equal(t, "resp_latest", out.expectedPreviousResponseID)
	require.JSONEq(t, string(payload), string(out.currentPayload), "payload 应原样透传")
	require.True(t, out.hasFunctionCallOutputCallID)
	require.Equal(t, []string{"call_1"}, out.functionCallOutputCallIDs)
}
