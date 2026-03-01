package service

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestParseOpenAIWSEventEnvelope(t *testing.T) {
	eventType, responseID, response := parseOpenAIWSEventEnvelope([]byte(`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.1"}}`))
	require.Equal(t, "response.completed", eventType)
	require.Equal(t, "resp_1", responseID)
	require.True(t, response.Exists())
	require.Equal(t, `{"id":"resp_1","model":"gpt-5.1"}`, response.Raw)

	eventType, responseID, response = parseOpenAIWSEventEnvelope([]byte(`{"type":"response.delta","id":"evt_1"}`))
	require.Equal(t, "response.delta", eventType)
	require.Equal(t, "evt_1", responseID)
	require.False(t, response.Exists())
}

func TestParseOpenAIWSResponseUsageFromCompletedEvent(t *testing.T) {
	usage := &OpenAIUsage{}
	parseOpenAIWSResponseUsageFromCompletedEvent(
		[]byte(`{"type":"response.completed","response":{"usage":{"input_tokens":11,"output_tokens":7,"input_tokens_details":{"cached_tokens":3}}}}`),
		usage,
	)
	require.Equal(t, 11, usage.InputTokens)
	require.Equal(t, 7, usage.OutputTokens)
	require.Equal(t, 3, usage.CacheReadInputTokens)
}

func TestOpenAIWSEventShouldParseUsage_TerminalEvents(t *testing.T) {
	require.True(t, openAIWSEventShouldParseUsage("response.completed"))
	require.True(t, openAIWSEventShouldParseUsage("response.done"))
	require.True(t, openAIWSEventShouldParseUsage("response.failed"))
	// After removing TrimSpace, callers must provide pre-trimmed input.
	require.False(t, openAIWSEventShouldParseUsage(" response.done "))
	require.False(t, openAIWSEventShouldParseUsage("response.in_progress"))
}

func TestOpenAIWSErrorEventHelpers_ConsistentWithWrapper(t *testing.T) {
	message := []byte(`{"type":"error","error":{"type":"invalid_request_error","code":"invalid_request","message":"invalid input"}}`)
	codeRaw, errTypeRaw, errMsgRaw := parseOpenAIWSErrorEventFields(message)

	wrappedReason, wrappedRecoverable := classifyOpenAIWSErrorEvent(message)
	rawReason, rawRecoverable := classifyOpenAIWSErrorEventFromRaw(codeRaw, errTypeRaw, errMsgRaw)
	require.Equal(t, wrappedReason, rawReason)
	require.Equal(t, wrappedRecoverable, rawRecoverable)

	wrappedStatus := openAIWSErrorHTTPStatus(message)
	rawStatus := openAIWSErrorHTTPStatusFromRaw(codeRaw, errTypeRaw)
	require.Equal(t, wrappedStatus, rawStatus)
	require.Equal(t, http.StatusBadRequest, rawStatus)

	wrappedCode, wrappedType, wrappedMsg := summarizeOpenAIWSErrorEventFields(message)
	rawCode, rawType, rawMsg := summarizeOpenAIWSErrorEventFieldsFromRaw(codeRaw, errTypeRaw, errMsgRaw)
	require.Equal(t, wrappedCode, rawCode)
	require.Equal(t, wrappedType, rawType)
	require.Equal(t, wrappedMsg, rawMsg)
}

func TestOpenAIWSMessageLikelyContainsToolCalls(t *testing.T) {
	require.False(t, openAIWSMessageLikelyContainsToolCalls([]byte(`{"type":"response.output_text.delta","delta":"hello"}`)))
	require.True(t, openAIWSMessageLikelyContainsToolCalls([]byte(`{"type":"response.output_item.added","item":{"tool_calls":[{"id":"tc1"}]}}`)))
	require.True(t, openAIWSMessageLikelyContainsToolCalls([]byte(`{"type":"response.output_item.added","item":{"type":"function_call"}}`)))
}

func TestOpenAIWSExtractPendingFunctionCallIDsFromEvent(t *testing.T) {
	callIDs := openAIWSExtractPendingFunctionCallIDsFromEvent([]byte(`{
		"type":"response.output_item.added",
		"response":{"id":"resp_1"},
		"item":{"type":"function_call","call_id":"call_a"}
	}`))
	require.Equal(t, []string{"call_a"}, callIDs)

	callIDs = openAIWSExtractPendingFunctionCallIDsFromEvent([]byte(`{
		"type":"response.completed",
		"response":{
			"id":"resp_2",
			"output":[
				{"type":"function_call","call_id":"call_b"},
				{"type":"message","content":[{"type":"output_text","text":"ok"}]},
				{"type":"function_call","call_id":"call_c"}
			]
		}
	}`))
	require.Equal(t, []string{"call_b", "call_c"}, callIDs)
}

func TestOpenAIWSExtractFunctionCallOutputCallIDsFromPayload(t *testing.T) {
	callIDs := openAIWSExtractFunctionCallOutputCallIDsFromPayload([]byte(`{
		"input":[
			{"type":"input_text","text":"hi"},
			{"type":"function_call_output","call_id":"call_2","output":"ok"},
			{"type":"function_call_output","call_id":"call_1","output":"ok"},
			{"type":"function_call_output","call_id":"call_2","output":"dup"}
		]
	}`))
	require.Equal(t, []string{"call_1", "call_2"}, callIDs)
}

func TestOpenAIWSInjectFunctionCallOutputItems(t *testing.T) {
	updatedPayload, injected, err := openAIWSInjectFunctionCallOutputItems(
		[]byte(`{"type":"response.create","input":[{"type":"input_text","text":"hello"}]}`),
		[]string{"call_1", "call_2", "call_1"},
		openAIWSAutoAbortedToolOutputValue,
	)
	require.NoError(t, err)
	require.Equal(t, 2, injected)
	require.Equal(t, "input_text", gjson.GetBytes(updatedPayload, "input.0.type").String())
	require.Equal(t, "function_call_output", gjson.GetBytes(updatedPayload, "input.1.type").String())
	require.Equal(t, "call_1", gjson.GetBytes(updatedPayload, "input.1.call_id").String())
	require.Equal(t, openAIWSAutoAbortedToolOutputValue, gjson.GetBytes(updatedPayload, "input.1.output").String())
	require.Equal(t, "call_2", gjson.GetBytes(updatedPayload, "input.2.call_id").String())
}

func TestReplaceOpenAIWSMessageModel_OptimizedStillCorrect(t *testing.T) {
	noModel := []byte(`{"type":"response.output_text.delta","delta":"hello"}`)
	require.Equal(t, string(noModel), string(replaceOpenAIWSMessageModel(noModel, "gpt-5.1", "custom-model")))

	rootOnly := []byte(`{"type":"response.created","model":"gpt-5.1"}`)
	require.Equal(t, `{"type":"response.created","model":"custom-model"}`, string(replaceOpenAIWSMessageModel(rootOnly, "gpt-5.1", "custom-model")))

	responseOnly := []byte(`{"type":"response.completed","response":{"model":"gpt-5.1"}}`)
	require.Equal(t, `{"type":"response.completed","response":{"model":"custom-model"}}`, string(replaceOpenAIWSMessageModel(responseOnly, "gpt-5.1", "custom-model")))

	both := []byte(`{"model":"gpt-5.1","response":{"model":"gpt-5.1"}}`)
	require.Equal(t, `{"model":"custom-model","response":{"model":"custom-model"}}`, string(replaceOpenAIWSMessageModel(both, "gpt-5.1", "custom-model")))
}
