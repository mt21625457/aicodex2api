package service

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNormalizeOpenAIWSIngressPayloadBeforeSend_DropsPreviousResponseIDWhenInputEdited(t *testing.T) {
	t.Parallel()

	lastReplay := []json.RawMessage{
		json.RawMessage(`{"type":"input_text","text":"old_1"}`),
		json.RawMessage(`{"type":"input_text","text":"old_2"}`),
	}
	payload := []byte(`{
		"type":"response.create",
		"model":"gpt-5.1",
		"previous_response_id":"resp_old",
		"input":[
			{"type":"input_text","text":"new_1"},
			{"type":"input_text","text":"old_2"}
		]
	}`)

	cleared := false
	out := normalizeOpenAIWSIngressPayloadBeforeSend(openAIWSIngressPreSendNormalizeInput{
		accountID:                 1,
		turn:                      3,
		connID:                    "conn_1",
		storeDisabled:             true,
		currentPayload:            payload,
		currentPayloadBytes:       len(payload),
		currentPreviousResponseID: "resp_old",
		expectedPreviousResponse:  "resp_old",
		pendingExpectedCallIDs:    []string{"call_1"},
		lastTurnReplayInput:       lastReplay,
		lastTurnReplayInputExists: true,
		clearSessionLastResponseID: func() {
			cleared = true
		},
	})

	require.True(t, cleared, "edited input should clear session last response id")
	require.Empty(t, out.currentPreviousResponseID)
	require.Empty(t, out.expectedPreviousResponseID)
	require.Nil(t, out.pendingExpectedCallIDs)
	require.False(t, gjson.GetBytes(out.currentPayload, "previous_response_id").Exists())
}

func TestNormalizeOpenAIWSIngressPayloadBeforeSend_InferPreviousAndInjectAbortedOutputs(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"type":"response.create",
		"model":"gpt-5.1",
		"input":[{"type":"function_call_output","call_id":"call_1","output":"{\"ok\":true}"}]
	}`)

	out := normalizeOpenAIWSIngressPayloadBeforeSend(openAIWSIngressPreSendNormalizeInput{
		accountID:                 2,
		turn:                      2,
		connID:                    "conn_2",
		storeDisabled:             true,
		currentPayload:            payload,
		currentPayloadBytes:       len(payload),
		currentPreviousResponseID: "",
		expectedPreviousResponse:  "resp_expected",
		pendingExpectedCallIDs:    []string{"call_2", "call_1"},
		lastTurnReplayInputExists: false,
	})

	require.Equal(t, "resp_expected", out.currentPreviousResponseID)
	require.Equal(t, "resp_expected", gjson.GetBytes(out.currentPayload, "previous_response_id").String())
	require.True(t, out.hasFunctionCallOutputCallID)
	require.ElementsMatch(t, []string{"call_1", "call_2"}, out.functionCallOutputCallIDs)

	injected := false
	gjson.GetBytes(out.currentPayload, "input").ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() != "function_call_output" || item.Get("call_id").String() != "call_2" {
			return true
		}
		require.Equal(t, openAIWSAutoAbortedToolOutputValue, item.Get("output").String())
		injected = true
		return false
	})
	require.True(t, injected, "expected injected function_call_output for call_2")
}

func TestNormalizeOpenAIWSIngressPayloadBeforeSend_AlignsStalePreviousResponseIDForFunctionCallOutput(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"type":"response.create",
		"model":"gpt-5.1",
		"previous_response_id":"resp_stale",
		"input":[{"type":"function_call_output","call_id":"call_1","output":"{\"ok\":true}"}]
	}`)

	out := normalizeOpenAIWSIngressPayloadBeforeSend(openAIWSIngressPreSendNormalizeInput{
		accountID:                 3,
		turn:                      4,
		connID:                    "conn_3",
		storeDisabled:             true,
		currentPayload:            payload,
		currentPayloadBytes:       len(payload),
		currentPreviousResponseID: "resp_stale",
		expectedPreviousResponse:  "resp_latest",
	})

	require.Equal(t, "resp_latest", out.currentPreviousResponseID)
	require.Equal(t, "resp_latest", gjson.GetBytes(out.currentPayload, "previous_response_id").String())
}
