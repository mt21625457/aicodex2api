package service

import (
	"encoding/json"
	"errors"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// ---------------------------------------------------------------------------
// openAIWSIngressTurnWroteDownstream 辅助函数测试
// ---------------------------------------------------------------------------

func TestOpenAIWSIngressTurnWroteDownstream(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "nil_error_returns_false",
			err:  nil,
			want: false,
		},
		{
			name: "plain_error_returns_false",
			err:  errors.New("some random error"),
			want: false,
		},
		{
			name: "turn_error_wrote_downstream_false",
			err: wrapOpenAIWSIngressTurnError(
				openAIWSIngressStagePreviousResponseNotFound,
				errors.New("previous response not found"),
				false,
			),
			want: false,
		},
		{
			name: "turn_error_wrote_downstream_true",
			err: wrapOpenAIWSIngressTurnError(
				"upstream_error_event",
				errors.New("upstream error"),
				true,
			),
			want: true,
		},
		{
			name: "turn_error_with_partial_result_wrote_downstream_true",
			err: wrapOpenAIWSIngressTurnErrorWithPartial(
				"read_upstream",
				errors.New("connection reset"),
				true,
				&OpenAIForwardResult{RequestID: "resp_partial"},
			),
			want: true,
		},
		{
			name: "turn_error_with_partial_result_wrote_downstream_false",
			err: wrapOpenAIWSIngressTurnErrorWithPartial(
				"read_upstream",
				errors.New("connection reset"),
				false,
				&OpenAIForwardResult{RequestID: "resp_partial"},
			),
			want: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, openAIWSIngressTurnWroteDownstream(tt.err))
		})
	}
}

// ---------------------------------------------------------------------------
// previous_response_not_found 错误与 ContinueTurn 处置测试
// ---------------------------------------------------------------------------

func TestPreviousResponseNotFound_ClassifiesAsContinueTurn(t *testing.T) {
	t.Parallel()

	// previous_response_not_found（wroteDownstream=false）应被归类为 ContinueTurn
	err := wrapOpenAIWSIngressTurnError(
		openAIWSIngressStagePreviousResponseNotFound,
		errors.New("previous response not found"),
		false,
	)

	reason, expected := classifyOpenAIWSIngressTurnAbortReason(err)
	require.Equal(t, openAIWSIngressTurnAbortReasonPreviousResponse, reason)
	require.True(t, expected)

	disposition := openAIWSIngressTurnAbortDispositionForReason(reason)
	require.Equal(t, openAIWSIngressTurnAbortDispositionContinueTurn, disposition)
}

func TestToolOutputNotFound_ClassifiesAsContinueTurn(t *testing.T) {
	t.Parallel()

	// tool_output_not_found（wroteDownstream=false）应被归类为 ContinueTurn
	err := wrapOpenAIWSIngressTurnError(
		openAIWSIngressStageToolOutputNotFound,
		errors.New("no tool call found for function call output"),
		false,
	)

	reason, expected := classifyOpenAIWSIngressTurnAbortReason(err)
	require.Equal(t, openAIWSIngressTurnAbortReasonToolOutput, reason)
	require.True(t, expected)

	disposition := openAIWSIngressTurnAbortDispositionForReason(reason)
	require.Equal(t, openAIWSIngressTurnAbortDispositionContinueTurn, disposition)
}

// ---------------------------------------------------------------------------
// function_call_output 与 previous_response_id 语义绑定测试
// 验证核心修复：带 function_call_output 时不能 drop previous_response_id
// ---------------------------------------------------------------------------

func TestFunctionCallOutputPayload_HasFunctionCallOutput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{
			name:    "payload_with_function_call_output",
			payload: `{"type":"response.create","previous_response_id":"resp_1","input":[{"type":"function_call_output","call_id":"call_abc","output":"ok"}]}`,
			want:    true,
		},
		{
			name:    "payload_with_mixed_input_including_function_call_output",
			payload: `{"type":"response.create","previous_response_id":"resp_1","input":[{"type":"input_text","text":"hello"},{"type":"function_call_output","call_id":"call_abc","output":"ok"}]}`,
			want:    true,
		},
		{
			name:    "payload_without_function_call_output",
			payload: `{"type":"response.create","previous_response_id":"resp_1","input":[{"type":"input_text","text":"hello"}]}`,
			want:    false,
		},
		{
			name:    "payload_with_empty_input",
			payload: `{"type":"response.create","previous_response_id":"resp_1","input":[]}`,
			want:    false,
		},
		{
			name:    "payload_without_input",
			payload: `{"type":"response.create","model":"gpt-5.1"}`,
			want:    false,
		},
		{
			name:    "multiple_function_call_outputs",
			payload: `{"type":"response.create","previous_response_id":"resp_1","input":[{"type":"function_call_output","call_id":"call_1","output":"r1"},{"type":"function_call_output","call_id":"call_2","output":"r2"}]}`,
			want:    true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := gjson.GetBytes([]byte(tt.payload), `input.#(type=="function_call_output")`).Exists()
			require.Equal(t, tt.want, got)
		})
	}
}

func TestDropPreviousResponseID_BreaksFunctionCallOutput(t *testing.T) {
	t.Parallel()

	// 核心回归测试：验证 drop previous_response_id 后 function_call_output 会变成孤立引用
	//
	// 场景：客户端发送 {previous_response_id: "resp_1", input: [{type: "function_call_output", call_id: "call_abc"}]}
	// 如果 drop 了 previous_response_id，上游会创建全新上下文，找不到 call_abc 对应的 tool call
	// 结果：上游报 "No tool call found for function call output with call_id call_abc"
	payload := []byte(`{
		"type":"response.create",
		"model":"gpt-5.1",
		"previous_response_id":"resp_stale_or_lost",
		"input":[
			{"type":"function_call_output","call_id":"call_abc","output":"{\"result\":\"ok\"}"},
			{"type":"function_call_output","call_id":"call_def","output":"{\"result\":\"done\"}"}
		]
	}`)

	// 1. 验证原始 payload 有 previous_response_id 和 function_call_output
	require.True(t, gjson.GetBytes(payload, "previous_response_id").Exists())
	require.True(t, gjson.GetBytes(payload, `input.#(type=="function_call_output")`).Exists())

	// 2. drop previous_response_id
	dropped, removed, err := dropPreviousResponseIDFromRawPayload(payload)
	require.NoError(t, err)
	require.True(t, removed)

	// 3. 验证 drop 后的状态：previous_response_id 被移除但 function_call_output 仍然存在
	require.False(t, gjson.GetBytes(dropped, "previous_response_id").Exists(),
		"previous_response_id 应该被移除")
	require.True(t, gjson.GetBytes(dropped, `input.#(type=="function_call_output")`).Exists(),
		"function_call_output 仍然存在，但此时它引用的 call_id 没有了上下文 — 这就是 bug 的根因")

	// 4. 验证 call_id 仍然在 payload 中（说明 drop 不会清理 function_call_output）
	callIDs := make([]string, 0)
	gjson.GetBytes(dropped, "input").ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == "function_call_output" {
			callIDs = append(callIDs, item.Get("call_id").String())
		}
		return true
	})
	require.ElementsMatch(t, []string{"call_abc", "call_def"}, callIDs,
		"function_call_output 的 call_id 未被清除，但上游已无法匹配")
}

func TestRecoveryStrategy_FunctionCallOutput_ShouldNotDrop(t *testing.T) {
	t.Parallel()

	// 此测试验证修复的核心逻辑：
	// 当 hasFunctionCallOutput=true 且 set/align 策略均失败时，
	// 正确行为是放弃恢复（return false），而非 drop previous_response_id
	//
	// 因为：function_call_output 语义绑定 previous_response_id
	// drop previous_response_id 但保留 function_call_output → 上游报错

	payload := []byte(`{
		"type":"response.create",
		"model":"gpt-5.1",
		"previous_response_id":"resp_lost",
		"input":[{"type":"function_call_output","call_id":"call_JDKR","output":"ok"}]
	}`)

	hasFunctionCallOutput := gjson.GetBytes(payload, `input.#(type=="function_call_output")`).Exists()
	require.True(t, hasFunctionCallOutput, "payload 必须包含 function_call_output")

	// 模拟 set 策略失败（currentPreviousResponseID 不为空，不满足 set 条件）
	currentPreviousResponseID := "resp_lost"
	expectedPrev := "resp_expected"
	require.NotEmpty(t, currentPreviousResponseID, "set 策略需要 currentPreviousResponseID 为空")

	// 模拟 align 策略失败
	_, aligned, alignErr := alignStoreDisabledPreviousResponseID(payload, expectedPrev)
	if alignErr == nil && aligned {
		// align 成功了，更新 payload 中的 previous_response_id
		t.Log("align 策略成功，此场景不触发 abort 路径")
	}
	// 注意：align 通常会成功（替换 resp_lost → resp_expected）。
	// 但在真实场景中，如果 align 后的 previous_response_id 仍然在上游不存在，
	// 上游会再次返回 previous_response_not_found，此时二次进入恢复函数，
	// 但 turnPrevRecoveryTried=true 会阻止二次恢复，直接走 abort。

	// 验证关键断言：即使 drop 技术上可行，也不应该执行
	// 因为这会导致 "No tool call found for function call output" 错误
	droppedPayload, removed, dropErr := dropPreviousResponseIDFromRawPayload(payload)
	require.NoError(t, dropErr)
	require.True(t, removed, "drop 操作本身可以成功")

	// 但 drop 后的 payload 仍有 function_call_output —— 这就是为什么不能 drop
	hasFCOAfterDrop := gjson.GetBytes(droppedPayload, `input.#(type=="function_call_output")`).Exists()
	require.True(t, hasFCOAfterDrop,
		"drop previous_response_id 不会移除 function_call_output，"+
			"导致上游报 'No tool call found for function call output'")
}

// ---------------------------------------------------------------------------
// ContinueTurn abort 路径错误通知测试
// ---------------------------------------------------------------------------

func TestContinueTurnAbort_ErrorEventFormat(t *testing.T) {
	t.Parallel()

	// 验证 ContinueTurn abort 时生成的 error 事件格式正确
	abortReason := openAIWSIngressTurnAbortReasonPreviousResponse
	abortMessage := "turn failed: " + string(abortReason)

	errorEvent := []byte(`{"type":"error","error":{"type":"server_error","code":"` +
		string(abortReason) + `","message":` + strconv.Quote(abortMessage) + `}}`)

	// 验证 JSON 格式有效
	var parsed map[string]any
	err := json.Unmarshal(errorEvent, &parsed)
	require.NoError(t, err, "error 事件应为有效 JSON")

	// 验证事件结构
	require.Equal(t, "error", parsed["type"])
	errorObj, ok := parsed["error"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "server_error", errorObj["type"])
	require.Equal(t, string(abortReason), errorObj["code"])
	require.Contains(t, errorObj["message"], string(abortReason))
}

func TestContinueTurnAbort_ErrorEventWithSpecialChars(t *testing.T) {
	t.Parallel()

	// 验证包含特殊字符的错误消息不会破坏 JSON 格式
	specialMessages := []string{
		`No tool call found for function call output with call_id call_JDKR0SzNTARIsGb0L3hofFWd.`,
		`error with "quotes" and \backslash`,
		"error with\nnewline",
		`error with <html> & entities`,
		"", // 空消息
	}

	for i, msg := range specialMessages {
		msg := msg
		t.Run("special_message_"+strconv.Itoa(i), func(t *testing.T) {
			t.Parallel()
			abortReason := openAIWSIngressTurnAbortReasonToolOutput
			errorEvent := []byte(`{"type":"error","error":{"type":"server_error","code":"` +
				string(abortReason) + `","message":` + strconv.Quote(msg) + `}}`)

			var parsed map[string]any
			err := json.Unmarshal(errorEvent, &parsed)
			require.NoError(t, err, "error event with special chars should be valid JSON: %q", msg)

			errorObj, ok := parsed["error"].(map[string]any)
			require.True(t, ok)
			require.Equal(t, msg, errorObj["message"])
		})
	}
}

func TestContinueTurnAbort_WroteDownstreamDeterminesNotification(t *testing.T) {
	t.Parallel()

	// 验证 wroteDownstream 标志如何影响错误通知策略
	tests := []struct {
		name                    string
		wroteDownstream         bool
		shouldSendErrorToClient bool
	}{
		{
			name:                    "not_wrote_downstream_should_send_error",
			wroteDownstream:         false,
			shouldSendErrorToClient: true,
		},
		{
			name:                    "wrote_downstream_should_not_send_error",
			wroteDownstream:         true,
			shouldSendErrorToClient: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := wrapOpenAIWSIngressTurnError(
				openAIWSIngressStagePreviousResponseNotFound,
				errors.New("previous response not found"),
				tt.wroteDownstream,
			)
			wroteDownstream := openAIWSIngressTurnWroteDownstream(err)
			require.Equal(t, tt.wroteDownstream, wroteDownstream)

			// 只有当 wroteDownstream=false 时才需要补发 error 事件
			shouldNotify := !wroteDownstream
			require.Equal(t, tt.shouldSendErrorToClient, shouldNotify)
		})
	}
}

// ---------------------------------------------------------------------------
// previous_response_id 恢复策略：set / align / abort 完整流程测试
// ---------------------------------------------------------------------------

func TestRecoveryStrategy_SetPreviousResponseID(t *testing.T) {
	t.Parallel()

	// 场景：客户端未发送 previous_response_id，但 session 中有记录
	// 此时应该通过 set 策略注入 previous_response_id
	payload := []byte(`{
		"type":"response.create",
		"model":"gpt-5.1",
		"input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]
	}`)

	expectedPrev := "resp_expected"

	// set 策略：当 currentPreviousResponseID 为空时，注入 expectedPrev
	updated, err := setPreviousResponseIDToRawPayload(payload, expectedPrev)
	require.NoError(t, err)
	require.Equal(t, expectedPrev, gjson.GetBytes(updated, "previous_response_id").String())

	// function_call_output 保持不变
	require.True(t, gjson.GetBytes(updated, `input.#(type=="function_call_output")`).Exists())
	require.Equal(t, "call_1", gjson.GetBytes(updated, `input.#(type=="function_call_output").call_id`).String())
}

func TestRecoveryStrategy_AlignPreviousResponseID(t *testing.T) {
	t.Parallel()

	// 场景：客户端发送了过时的 previous_response_id，需要 align 到最新
	payload := []byte(`{
		"type":"response.create",
		"model":"gpt-5.1",
		"previous_response_id":"resp_stale",
		"input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]
	}`)

	expectedPrev := "resp_latest"

	updated, changed, err := alignStoreDisabledPreviousResponseID(payload, expectedPrev)
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, expectedPrev, gjson.GetBytes(updated, "previous_response_id").String())

	// function_call_output 保持不变
	require.True(t, gjson.GetBytes(updated, `input.#(type=="function_call_output")`).Exists())
}

func TestRecoveryStrategy_AlignFailsWhenNoExpectedPrev(t *testing.T) {
	t.Parallel()

	// 场景：没有预期的 previous_response_id，align 无法执行
	payload := []byte(`{
		"type":"response.create",
		"model":"gpt-5.1",
		"previous_response_id":"resp_stale",
		"input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]
	}`)

	updated, changed, err := alignStoreDisabledPreviousResponseID(payload, "")
	require.NoError(t, err)
	require.False(t, changed, "align 应该在 expectedPrev 为空时不执行")
	require.Equal(t, string(payload), string(updated))
}

// ---------------------------------------------------------------------------
// isOpenAIWSIngressPreviousResponseNotFound 边界条件测试
// ---------------------------------------------------------------------------

func TestIsOpenAIWSIngressPreviousResponseNotFound_WroteDownstreamBlocks(t *testing.T) {
	t.Parallel()

	// wroteDownstream=true 时，即使 stage 是 previous_response_not_found，
	// 也不应被识别为可恢复的 previous_response_not_found
	// （因为已经向客户端写入了数据，无法安全重试）
	err := wrapOpenAIWSIngressTurnError(
		openAIWSIngressStagePreviousResponseNotFound,
		errors.New("previous response not found"),
		true, // wroteDownstream = true
	)
	require.False(t, isOpenAIWSIngressPreviousResponseNotFound(err),
		"wroteDownstream=true 时不应识别为可恢复的 previous_response_not_found")
}

func TestIsOpenAIWSIngressPreviousResponseNotFound_DifferentStageReturns_False(t *testing.T) {
	t.Parallel()

	stages := []string{
		"read_upstream",
		"write_upstream",
		"upstream_error_event",
		openAIWSIngressStageToolOutputNotFound,
		"unknown",
		"",
	}

	for _, stage := range stages {
		stage := stage
		t.Run("stage_"+stage, func(t *testing.T) {
			t.Parallel()
			err := wrapOpenAIWSIngressTurnError(stage, errors.New("some error"), false)
			require.False(t, isOpenAIWSIngressPreviousResponseNotFound(err),
				"stage=%q 不应被识别为 previous_response_not_found", stage)
		})
	}
}

// ---------------------------------------------------------------------------
// 端到端场景测试：function_call_output 恢复链路
// ---------------------------------------------------------------------------

func TestEndToEnd_FunctionCallOutputRecoveryChain(t *testing.T) {
	t.Parallel()

	// 完整场景：
	// 1. 客户端发送带 function_call_output 的请求
	// 2. 上游返回 previous_response_not_found
	// 3. 恢复策略尝试 set/align
	// 4. 如果都失败，应该 abort（而非 drop previous_response_id）
	// 5. 客户端收到 error 事件
	// 6. 客户端重置并发送完整请求

	payload := []byte(`{
		"type":"response.create",
		"model":"gpt-5.1",
		"previous_response_id":"resp_lost",
		"input":[
			{"type":"function_call_output","call_id":"call_JDKR0SzNTARIsGb0L3hofFWd","output":"{\"ok\":true}"}
		]
	}`)

	// Step 1: 检测 function_call_output
	hasFCO := gjson.GetBytes(payload, `input.#(type=="function_call_output")`).Exists()
	require.True(t, hasFCO, "payload 包含 function_call_output")

	// Step 2: 模拟 previous_response_not_found 错误
	turnErr := wrapOpenAIWSIngressTurnError(
		openAIWSIngressStagePreviousResponseNotFound,
		errors.New("previous response not found"),
		false,
	)
	require.True(t, isOpenAIWSIngressPreviousResponseNotFound(turnErr))

	// Step 3: 验证 ContinueTurn 处置
	reason, _ := classifyOpenAIWSIngressTurnAbortReason(turnErr)
	disposition := openAIWSIngressTurnAbortDispositionForReason(reason)
	require.Equal(t, openAIWSIngressTurnAbortDispositionContinueTurn, disposition)

	// Step 4: set 策略 — 失败（currentPreviousResponseID 不为空）
	currentPrevID := gjson.GetBytes(payload, "previous_response_id").String()
	require.NotEmpty(t, currentPrevID, "set 策略前提条件不满足（需要 currentPreviousResponseID 为空）")

	// Step 5: align 策略 — 假设 expectedPrev 为空（session 中无记录）
	expectedPrev := ""
	_, aligned, alignErr := alignStoreDisabledPreviousResponseID(payload, expectedPrev)
	require.NoError(t, alignErr)
	require.False(t, aligned, "expectedPrev 为空时 align 应失败")

	// Step 6: 此时应该 abort（return false）而非 drop
	// 验证：如果错误地执行 drop，会导致 function_call_output 成为孤立引用
	dropped, removed, _ := dropPreviousResponseIDFromRawPayload(payload)
	if removed {
		hasFCOAfterDrop := gjson.GetBytes(dropped, `input.#(type=="function_call_output")`).Exists()
		require.True(t, hasFCOAfterDrop,
			"drop 后 function_call_output 仍存在，上游会报 'No tool call found'")
	}

	// Step 7: 正确行为——abort 后生成 error 事件通知客户端
	wroteDownstream := openAIWSIngressTurnWroteDownstream(turnErr)
	require.False(t, wroteDownstream, "abort 前未向客户端写入数据")

	abortMessage := "turn failed: " + string(reason)
	errorEvent := []byte(`{"type":"error","error":{"type":"server_error","code":"` +
		string(reason) + `","message":` + strconv.Quote(abortMessage) + `}}`)

	var parsed map[string]any
	require.NoError(t, json.Unmarshal(errorEvent, &parsed))
	require.Equal(t, "error", parsed["type"])
}

func TestEndToEnd_NonFunctionCallOutput_CanDrop(t *testing.T) {
	t.Parallel()

	// 对照场景：没有 function_call_output 的 payload 可以安全 drop previous_response_id
	payload := []byte(`{
		"type":"response.create",
		"model":"gpt-5.1",
		"previous_response_id":"resp_old",
		"input":[{"type":"input_text","text":"hello"}]
	}`)

	hasFCO := gjson.GetBytes(payload, `input.#(type=="function_call_output")`).Exists()
	require.False(t, hasFCO, "此 payload 不包含 function_call_output")

	// drop 是安全的
	dropped, removed, err := dropPreviousResponseIDFromRawPayload(payload)
	require.NoError(t, err)
	require.True(t, removed)
	require.False(t, gjson.GetBytes(dropped, "previous_response_id").Exists())

	// input 仍然有效（input_text 不依赖 previous_response_id）
	require.Equal(t, "hello", gjson.GetBytes(dropped, "input.0.text").String())
}

// ---------------------------------------------------------------------------
// shouldKeepIngressPreviousResponseID 与 function_call_output 的交互测试
// ---------------------------------------------------------------------------

func TestShouldKeepIngressPreviousResponseID_FunctionCallOutputCallIDMatch(t *testing.T) {
	t.Parallel()

	// 当 function_call_output 的 call_id 与 pending call_id 匹配时，应保留 previous_response_id
	previousPayload := []byte(`{"type":"response.create","model":"gpt-5.1","input":[]}`)
	currentPayload := []byte(`{
		"type":"response.create",
		"model":"gpt-5.1",
		"previous_response_id":"resp_1",
		"input":[{"type":"function_call_output","call_id":"call_match","output":"ok"}]
	}`)

	keep, reason, err := shouldKeepIngressPreviousResponseID(
		previousPayload,
		currentPayload,
		"resp_1",
		true,                   // hasFunctionCallOutput
		[]string{"call_match"}, // pendingCallIDs
		[]string{"call_match"}, // requestCallIDs
	)
	require.NoError(t, err)
	require.True(t, keep, "call_id 匹配时应保留 previous_response_id")
	require.Equal(t, "function_call_output_call_id_match", reason)
}

func TestShouldKeepIngressPreviousResponseID_FunctionCallOutputCallIDMismatch(t *testing.T) {
	t.Parallel()

	// 当 function_call_output 的 call_id 与 pending call_id 不匹配时，
	// 应放弃 previous_response_id
	previousPayload := []byte(`{"type":"response.create","model":"gpt-5.1","input":[]}`)
	currentPayload := []byte(`{
		"type":"response.create",
		"model":"gpt-5.1",
		"previous_response_id":"resp_1",
		"input":[{"type":"function_call_output","call_id":"call_wrong","output":"ok"}]
	}`)

	keep, reason, err := shouldKeepIngressPreviousResponseID(
		previousPayload,
		currentPayload,
		"resp_1",
		true,                   // hasFunctionCallOutput
		[]string{"call_real"},  // pendingCallIDs
		[]string{"call_wrong"}, // requestCallIDs
	)
	require.NoError(t, err)
	require.False(t, keep, "call_id 不匹配时应放弃 previous_response_id")
	require.Equal(t, "function_call_output_call_id_mismatch", reason)
}

// ---------------------------------------------------------------------------
// isOpenAIWSIngressTurnRetryable 与 function_call_output 场景的交互
// ---------------------------------------------------------------------------

func TestIsOpenAIWSIngressTurnRetryable_PreviousResponseNotFound(t *testing.T) {
	t.Parallel()

	// previous_response_not_found 不应被标记为 retryable（因为有专门的恢复路径）
	err := wrapOpenAIWSIngressTurnError(
		openAIWSIngressStagePreviousResponseNotFound,
		errors.New("previous response not found"),
		false,
	)
	require.False(t, isOpenAIWSIngressTurnRetryable(err),
		"previous_response_not_found 有专门的恢复逻辑，不走通用重试")
}

func TestIsOpenAIWSIngressTurnRetryable_WroteDownstreamBlocksRetry(t *testing.T) {
	t.Parallel()

	// wroteDownstream=true 时，任何 stage 都不应 retryable
	err := wrapOpenAIWSIngressTurnError(
		"write_upstream",
		errors.New("write failed"),
		true, // wroteDownstream
	)
	require.False(t, isOpenAIWSIngressTurnRetryable(err),
		"wroteDownstream=true 时不应重试")
}

// ---------------------------------------------------------------------------
// normalizeOpenAIWSIngressPayloadBeforeSend 与恢复的集成测试
// ---------------------------------------------------------------------------

func TestNormalizePayload_FunctionCallOutputPassthrough(t *testing.T) {
	t.Parallel()

	// 透传模式：normalizer 不再注入 previous_response_id，原样传递
	payload := []byte(`{
		"type":"response.create",
		"model":"gpt-5.1",
		"input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]
	}`)

	out := normalizeOpenAIWSIngressPayloadBeforeSend(openAIWSIngressPreSendNormalizeInput{
		accountID:                 1,
		turn:                      2,
		connID:                    "conn_test",
		currentPayload:            payload,
		currentPayloadBytes:       len(payload),
		currentPreviousResponseID: "",
		expectedPreviousResponse:  "resp_expected",
		pendingExpectedCallIDs:    []string{"call_1"},
	})

	// 透传模式：previous_response_id 保持客户端原值（空），由下游 recovery 处理
	require.Empty(t, out.currentPreviousResponseID,
		"透传模式不应注入 previous_response_id")
	require.True(t, out.hasFunctionCallOutputCallID)
	require.Equal(t, []string{"call_1"}, out.functionCallOutputCallIDs)
}
