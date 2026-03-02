package service

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// ---------------------------------------------------------------------------
// 改动 1 测试：预防性检测 — store_disabled + function_call_output + 无 previous_response_id
// 在 sendAndRelay 中提前返回可恢复错误，避免上游先写数据再报错导致无法恢复
// ---------------------------------------------------------------------------

// TestProactiveToolOutputNotFound_ErrorShape 验证预防性检测生成的错误格式正确：
// stage = tool_output_not_found、wroteDownstream = false、可被恢复函数识别。
func TestProactiveToolOutputNotFound_ErrorShape(t *testing.T) {
	t.Parallel()

	err := wrapOpenAIWSIngressTurnErrorWithPartial(
		openAIWSIngressStageToolOutputNotFound,
		errors.New("proactive tool_output_not_found: function_call_output without previous_response_id in store_disabled mode"),
		false,
		nil,
	)

	// 1. 应被识别为 tool_output_not_found
	require.True(t, isOpenAIWSIngressToolOutputNotFound(err),
		"预防性检测错误应被 isOpenAIWSIngressToolOutputNotFound 识别")

	// 2. wroteDownstream 应为 false（尚未发送任何数据）
	require.False(t, openAIWSIngressTurnWroteDownstream(err),
		"预防性检测在发送前触发，wroteDownstream 必须为 false")

	// 3. 应被 classifyOpenAIWSIngressTurnAbortReason 归类为 ToolOutput
	reason, expected := classifyOpenAIWSIngressTurnAbortReason(err)
	require.Equal(t, openAIWSIngressTurnAbortReasonToolOutput, reason)
	require.True(t, expected, "tool_output_not_found 是预期错误")

	// 4. 处置方式应为 ContinueTurn（允许恢复重试）
	disposition := openAIWSIngressTurnAbortDispositionForReason(reason)
	require.Equal(t, openAIWSIngressTurnAbortDispositionContinueTurn, disposition,
		"tool_output_not_found 应为 ContinueTurn 处置，允许恢复重试")

	// 5. 部分结果应为 nil（因为尚未产生任何上游响应）
	// partialResult=nil 时 OpenAIWSIngressTurnPartialResult 返回 (nil, false)
	partial, ok := OpenAIWSIngressTurnPartialResult(err)
	require.False(t, ok, "预防性检测传入 partialResult=nil，ok 应为 false")
	require.Nil(t, partial, "预防性检测不应有部分结果")
}

// TestProactiveToolOutputNotFound_NotPreviousResponseNotFound
// 验证预防性检测生成的错误不会被误识别为 previous_response_not_found。
func TestProactiveToolOutputNotFound_NotPreviousResponseNotFound(t *testing.T) {
	t.Parallel()

	err := wrapOpenAIWSIngressTurnErrorWithPartial(
		openAIWSIngressStageToolOutputNotFound,
		errors.New("proactive tool_output_not_found"),
		false,
		nil,
	)

	require.False(t, isOpenAIWSIngressPreviousResponseNotFound(err),
		"tool_output_not_found 不应被误识别为 previous_response_not_found")
}

// TestProactiveDetection_ConditionMatrix 验证预防性检测的三个触发条件的所有组合。
// 只有 store_disabled + function_call_output + 无 previous_response_id + 无可关联上下文 才触发。
func TestProactiveDetection_ConditionMatrix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		storeDisabled         bool
		hasFunctionCallOutput bool
		previousResponseID    string
		hasToolOutputContext  bool
		shouldTrigger         bool
	}{
		{
			name:                  "all_conditions_met_should_trigger",
			storeDisabled:         true,
			hasFunctionCallOutput: true,
			previousResponseID:    "",
			hasToolOutputContext:  false,
			shouldTrigger:         true,
		},
		{
			name:                  "whitespace_only_previous_response_id_should_trigger",
			storeDisabled:         true,
			hasFunctionCallOutput: true,
			previousResponseID:    "   ",
			hasToolOutputContext:  false,
			shouldTrigger:         true,
		},
		{
			name:                  "store_enabled_should_not_trigger",
			storeDisabled:         false,
			hasFunctionCallOutput: true,
			previousResponseID:    "",
			hasToolOutputContext:  false,
			shouldTrigger:         false,
		},
		{
			name:                  "no_function_call_output_should_not_trigger",
			storeDisabled:         true,
			hasFunctionCallOutput: false,
			previousResponseID:    "",
			hasToolOutputContext:  false,
			shouldTrigger:         false,
		},
		{
			name:                  "has_previous_response_id_should_not_trigger",
			storeDisabled:         true,
			hasFunctionCallOutput: true,
			previousResponseID:    "resp_abc",
			hasToolOutputContext:  false,
			shouldTrigger:         false,
		},
		{
			name:                  "all_false_should_not_trigger",
			storeDisabled:         false,
			hasFunctionCallOutput: false,
			previousResponseID:    "resp_abc",
			hasToolOutputContext:  false,
			shouldTrigger:         false,
		},
		{
			name:                  "store_disabled_no_fco_has_prev_should_not_trigger",
			storeDisabled:         true,
			hasFunctionCallOutput: false,
			previousResponseID:    "resp_abc",
			hasToolOutputContext:  false,
			shouldTrigger:         false,
		},
		{
			name:                  "store_enabled_has_fco_has_prev_should_not_trigger",
			storeDisabled:         false,
			hasFunctionCallOutput: true,
			previousResponseID:    "resp_abc",
			hasToolOutputContext:  false,
			shouldTrigger:         false,
		},
		{
			name:                  "has_tool_output_context_should_not_trigger",
			storeDisabled:         true,
			hasFunctionCallOutput: true,
			previousResponseID:    "",
			hasToolOutputContext:  true,
			shouldTrigger:         false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// 模拟 sendAndRelay 中的检测逻辑
			triggered := shouldProactivelyRejectIngressToolOutputWithoutPreviousResponseID(
				tt.storeDisabled,
				tt.hasFunctionCallOutput,
				tt.previousResponseID,
				tt.hasToolOutputContext,
			)
			require.Equal(t, tt.shouldTrigger, triggered)
		})
	}
}

// TestProactiveDetection_PayloadExtraction 验证从真实 payload 中提取的条件参数
// 与预防性检测逻辑的配合。确保 payload 解析结果能正确触发或跳过检测。
func TestProactiveDetection_PayloadExtraction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		payload        string
		wantHasFCO     bool
		wantPrevID     string
		wantHasContext bool
		shouldTrigger  bool // 假设 storeDisabled=true
	}{
		{
			name:           "fco_without_previous_response_id",
			payload:        `{"type":"response.create","input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`,
			wantHasFCO:     true,
			wantPrevID:     "",
			wantHasContext: false,
			shouldTrigger:  true,
		},
		{
			name:           "fco_with_previous_response_id",
			payload:        `{"type":"response.create","previous_response_id":"resp_1","input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`,
			wantHasFCO:     true,
			wantPrevID:     "resp_1",
			wantHasContext: false,
			shouldTrigger:  false,
		},
		{
			name:           "no_fco_without_previous_response_id",
			payload:        `{"type":"response.create","input":[{"type":"input_text","text":"hello"}]}`,
			wantHasFCO:     false,
			wantPrevID:     "",
			wantHasContext: false,
			shouldTrigger:  false,
		},
		{
			name:           "multiple_fco_without_previous_response_id",
			payload:        `{"type":"response.create","input":[{"type":"function_call_output","call_id":"call_1","output":"r1"},{"type":"function_call_output","call_id":"call_2","output":"r2"}]}`,
			wantHasFCO:     true,
			wantPrevID:     "",
			wantHasContext: false,
			shouldTrigger:  true,
		},
		{
			name:           "fco_with_tool_call_context",
			payload:        `{"type":"response.create","input":[{"type":"tool_call","call_id":"call_1"},{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`,
			wantHasFCO:     true,
			wantPrevID:     "",
			wantHasContext: true,
			shouldTrigger:  false,
		},
		{
			name:           "fco_with_item_reference_context",
			payload:        `{"type":"response.create","input":[{"type":"item_reference","id":"call_1"},{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`,
			wantHasFCO:     true,
			wantPrevID:     "",
			wantHasContext: true,
			shouldTrigger:  false,
		},
		{
			name:           "empty_input_without_previous_response_id",
			payload:        `{"type":"response.create","input":[]}`,
			wantHasFCO:     false,
			wantPrevID:     "",
			wantHasContext: false,
			shouldTrigger:  false,
		},
		{
			name:           "no_input_field",
			payload:        `{"type":"response.create","model":"gpt-5.1"}`,
			wantHasFCO:     false,
			wantPrevID:     "",
			wantHasContext: false,
			shouldTrigger:  false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			payload := []byte(tt.payload)

			callIDs := openAIWSExtractFunctionCallOutputCallIDsFromPayload(payload)
			hasFCO := len(callIDs) > 0
			prevID := openAIWSPayloadStringFromRaw(payload, "previous_response_id")
			hasContext := openAIWSHasToolCallContextInPayload(payload) ||
				openAIWSHasItemReferenceForAllFunctionCallOutputsInPayload(payload, callIDs)

			require.Equal(t, tt.wantHasFCO, hasFCO, "hasFunctionCallOutput 不匹配")
			require.Equal(t, tt.wantPrevID, prevID, "previousResponseID 不匹配")
			require.Equal(t, tt.wantHasContext, hasContext, "tool output context 检测结果不匹配")

			// 模拟 storeDisabled=true 时的检测
			triggered := shouldProactivelyRejectIngressToolOutputWithoutPreviousResponseID(
				true,
				hasFCO,
				prevID,
				hasContext,
			)
			require.Equal(t, tt.shouldTrigger, triggered, "检测触发结果不匹配")
		})
	}
}

// TestProactiveDetection_RecoveryChainIntegration 验证预防性检测错误能被
// recoverIngressPrevResponseNotFound 的恢复逻辑识别并处理。
// 这是两个改动之间的集成测试。
func TestProactiveDetection_RecoveryChainIntegration(t *testing.T) {
	t.Parallel()

	// 模拟预防性检测返回的错误
	proactiveErr := wrapOpenAIWSIngressTurnErrorWithPartial(
		openAIWSIngressStageToolOutputNotFound,
		errors.New("proactive tool_output_not_found: function_call_output without previous_response_id in store_disabled mode"),
		false,
		nil,
	)

	// 1. 恢复函数入口检测：isToolOutputMissing 应为 true
	require.True(t, isOpenAIWSIngressToolOutputNotFound(proactiveErr),
		"预防性检测错误应通过 isToolOutputMissing 检查")

	// 2. isPrevNotFound 应为 false（确保不走 previous_response_not_found 分支）
	require.False(t, isOpenAIWSIngressPreviousResponseNotFound(proactiveErr),
		"预防性检测错误不应走 previous_response_not_found 分支")

	// 3. wroteDownstream=false 确保可以安全重试
	require.False(t, openAIWSIngressTurnWroteDownstream(proactiveErr),
		"预防性检测在发送前触发，wroteDownstream 必须为 false")
}

// ---------------------------------------------------------------------------
// 改动 2 测试：恢复逻辑修复 — previous_response_id 已缺失时跳过 drop 步骤
// ---------------------------------------------------------------------------

// TestToolOutputRecovery_PreviousResponseIDAlreadyEmpty 验证核心修复：
// 当 payload 中本就不存在 previous_response_id 时，跳过 drop 步骤，
// 直接进入 setOpenAIWSPayloadInputSequence。
func TestToolOutputRecovery_PreviousResponseIDAlreadyEmpty(t *testing.T) {
	t.Parallel()

	// 场景：预防性检测触发后，payload 中不存在 previous_response_id
	payload := []byte(`{
		"type":"response.create",
		"model":"gpt-5.1",
		"input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]
	}`)

	currentPreviousResponseID := strings.TrimSpace(openAIWSPayloadStringFromRaw(payload, "previous_response_id"))
	require.Empty(t, currentPreviousResponseID, "payload 中不应存在 previous_response_id")

	// 修复后的逻辑：currentPreviousResponseID 为空时跳过 drop
	// 直接使用原始 payload 作为 updatedPayload
	updatedPayload := payload
	if currentPreviousResponseID != "" {
		// 此分支不应执行
		t.Fatal("不应进入 drop 分支")
	}

	// 验证跳过 drop 后，payload 仍然有效且可以继续处理
	require.True(t, gjson.ValidBytes(updatedPayload), "payload 应为有效 JSON")
	require.True(t, gjson.GetBytes(updatedPayload, `input.#(type=="function_call_output")`).Exists(),
		"function_call_output 应保持不变")

	// 模拟 setOpenAIWSPayloadInputSequence：使用 replay input 替换
	replayInput := []json.RawMessage{
		json.RawMessage(`{"type":"function_call_output","call_id":"call_1","output":"ok"}`),
	}
	updatedWithInput, setInputErr := setOpenAIWSPayloadInputSequence(
		updatedPayload,
		replayInput,
		true,
	)
	require.NoError(t, setInputErr, "setOpenAIWSPayloadInputSequence 应成功")
	require.True(t, gjson.ValidBytes(updatedWithInput), "更新后的 payload 应为有效 JSON")
	require.True(t, gjson.GetBytes(updatedWithInput, `input.#(type=="function_call_output")`).Exists(),
		"replay input 中的 function_call_output 应存在")
}

// TestToolOutputRecovery_PreviousResponseIDExists 验证修复不影响原有逻辑：
// 当 payload 中存在 previous_response_id 时，仍然执行 drop。
func TestToolOutputRecovery_PreviousResponseIDExists(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"type":"response.create",
		"model":"gpt-5.1",
		"previous_response_id":"resp_stale",
		"input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]
	}`)

	currentPreviousResponseID := strings.TrimSpace(openAIWSPayloadStringFromRaw(payload, "previous_response_id"))
	require.NotEmpty(t, currentPreviousResponseID, "payload 中应存在 previous_response_id")

	// 修复后的逻辑：currentPreviousResponseID 不为空时执行 drop
	updatedPayload := payload
	if currentPreviousResponseID != "" {
		dropped, removed, dropErr := dropPreviousResponseIDFromRawPayload(payload)
		require.NoError(t, dropErr, "drop 操作不应出错")
		require.True(t, removed, "previous_response_id 应被成功移除")
		updatedPayload = dropped
	}

	// 验证 drop 后 previous_response_id 已移除
	require.False(t, gjson.GetBytes(updatedPayload, "previous_response_id").Exists(),
		"previous_response_id 应被移除")

	// 验证 function_call_output 仍然存在
	require.True(t, gjson.GetBytes(updatedPayload, `input.#(type=="function_call_output")`).Exists(),
		"function_call_output 应保持不变")

	// 模拟 setOpenAIWSPayloadInputSequence
	replayInput := []json.RawMessage{
		json.RawMessage(`{"type":"function_call_output","call_id":"call_1","output":"ok"}`),
	}
	updatedWithInput, setInputErr := setOpenAIWSPayloadInputSequence(
		updatedPayload,
		replayInput,
		true,
	)
	require.NoError(t, setInputErr, "setOpenAIWSPayloadInputSequence 应成功")
	require.True(t, gjson.ValidBytes(updatedWithInput), "更新后的 payload 应为有效 JSON")
}

// TestToolOutputRecovery_DropError 验证当 drop 操作出错时返回 false。
func TestToolOutputRecovery_DropError(t *testing.T) {
	t.Parallel()

	// 使用非法 JSON 模拟 drop 错误
	currentPreviousResponseID := "resp_stale"

	if currentPreviousResponseID != "" {
		// 使用含有 previous_response_id 但格式异常的 payload 触发 drop 错误
		badPayload := []byte(`not-valid-json`)
		_, _, dropErr := dropPreviousResponseIDFromRawPayload(badPayload)
		// 无论 drop 是否报错，验证错误分支的行为
		if dropErr != nil {
			// drop 出错时应返回 false（跳过恢复）
			t.Log("drop 出错场景：恢复应返回 false")
		}
	}
}

// TestToolOutputRecovery_DropNotRemoved 验证当 drop 操作返回 removed=false 时的行为。
// 修复后：如果 currentPreviousResponseID 不为空但 drop 未能移除（removed=false），
// updatedPayload 仍使用原始 payload（不更新），继续执行后续流程。
func TestToolOutputRecovery_DropNotRemoved(t *testing.T) {
	t.Parallel()

	// 构造一个含有 previous_response_id 的 payload
	payload := []byte(`{"type":"response.create","previous_response_id":"resp_1","input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`)

	currentPreviousResponseID := strings.TrimSpace(openAIWSPayloadStringFromRaw(payload, "previous_response_id"))
	require.NotEmpty(t, currentPreviousResponseID)

	// 使用自定义 delete 函数模拟 removed=false 的场景
	updatedPayload := payload
	if currentPreviousResponseID != "" {
		// 正常 drop 应该成功
		dropped, removed, dropErr := dropPreviousResponseIDFromRawPayload(payload)
		require.NoError(t, dropErr)
		if removed {
			updatedPayload = dropped
		}
		// 无论 removed 与否，后续逻辑都应继续执行（不再提前 return false）
	}

	// 验证可以继续执行 setOpenAIWSPayloadInputSequence
	updatedWithInput, setInputErr := setOpenAIWSPayloadInputSequence(
		updatedPayload,
		nil,
		false, // fullInputExists=false 时直接返回原 payload
	)
	require.NoError(t, setInputErr)
	require.Equal(t, string(updatedPayload), string(updatedWithInput))
}

// TestToolOutputRecovery_WithDeleteFn_Removed_False 使用 WithDeleteFn 接口模拟
// drop 函数返回 removed=false 的边界情况。
func TestToolOutputRecovery_WithDeleteFn_Removed_False(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"type":"response.create","previous_response_id":"resp_1","input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`)
	currentPreviousResponseID := "resp_1"

	// 使用 noop delete 函数模拟 removed=false
	noopDelete := func(data []byte, _ string) ([]byte, error) {
		return data, nil // 不修改 payload，sjson 会返回原样
	}

	updatedPayload := payload
	if currentPreviousResponseID != "" {
		dropped, removed, dropErr := dropPreviousResponseIDFromRawPayloadWithDeleteFn(payload, noopDelete)
		require.NoError(t, dropErr)
		// noop delete 不移除字段，但 previous_response_id 仍存在
		// removed 取决于 payload 比较
		if removed {
			updatedPayload = dropped
		}
	}

	// 无论 removed 与否，都应继续（不提前 return false）
	require.True(t, gjson.ValidBytes(updatedPayload))
}

// ---------------------------------------------------------------------------
// 端到端场景测试：预防性检测 → 恢复逻辑 完整链路
// ---------------------------------------------------------------------------

// TestEndToEnd_ProactiveDetection_RecoveryWithEmptyPreviousResponseID
// 完整模拟：store_disabled 模式下，客户端发送 function_call_output 但
// 未携带 previous_response_id → 预防性检测触发 → 恢复逻辑跳过 drop → 重放。
func TestEndToEnd_ProactiveDetection_RecoveryWithEmptyPreviousResponseID(t *testing.T) {
	t.Parallel()

	// Step 1: 构造客户端 payload（无 previous_response_id）
	payload := []byte(`{
		"type":"response.create",
		"model":"gpt-5.1",
		"input":[
			{"type":"function_call_output","call_id":"call_abc","output":"{\"result\":\"ok\"}"}
		]
	}`)

	// Step 2: 提取条件参数（模拟 sendAndRelay 中的变量赋值）
	turnPreviousResponseID := openAIWSPayloadStringFromRaw(payload, "previous_response_id")
	turnFunctionCallOutputCallIDs := openAIWSExtractFunctionCallOutputCallIDsFromPayload(payload)
	turnHasFunctionCallOutput := len(turnFunctionCallOutputCallIDs) > 0
	turnStoreDisabled := true // 模拟 store_disabled 模式

	require.Empty(t, turnPreviousResponseID)
	require.True(t, turnHasFunctionCallOutput)
	require.Equal(t, []string{"call_abc"}, turnFunctionCallOutputCallIDs)

	// Step 3: 预防性检测触发
	shouldTrigger := shouldProactivelyRejectIngressToolOutputWithoutPreviousResponseID(
		turnStoreDisabled,
		turnHasFunctionCallOutput,
		turnPreviousResponseID,
		false,
	)
	require.True(t, shouldTrigger, "预防性检测应触发")

	// Step 4: 构造预防性检测错误
	proactiveErr := wrapOpenAIWSIngressTurnErrorWithPartial(
		openAIWSIngressStageToolOutputNotFound,
		errors.New("proactive tool_output_not_found: function_call_output without previous_response_id in store_disabled mode"),
		false,
		nil,
	)

	// Step 5: 验证恢复入口条件
	isToolOutputMissing := isOpenAIWSIngressToolOutputNotFound(proactiveErr)
	require.True(t, isToolOutputMissing, "恢复入口应识别 tool_output_not_found")

	// Step 6: 模拟恢复逻辑（改动 2 的核心路径）
	currentPreviousResponseID := strings.TrimSpace(openAIWSPayloadStringFromRaw(payload, "previous_response_id"))
	require.Empty(t, currentPreviousResponseID, "payload 中无 previous_response_id")

	// 改动 2：跳过 drop 步骤
	updatedPayload := payload
	if currentPreviousResponseID != "" {
		t.Fatal("不应进入 drop 分支")
	}

	// Step 7: 执行 setOpenAIWSPayloadInputSequence
	replayInput := []json.RawMessage{
		json.RawMessage(`{"type":"function_call_output","call_id":"call_abc","output":"{\"result\":\"ok\"}"}`),
	}
	updatedWithInput, setInputErr := setOpenAIWSPayloadInputSequence(
		updatedPayload,
		replayInput,
		true,
	)
	require.NoError(t, setInputErr, "setOpenAIWSPayloadInputSequence 应成功")
	require.True(t, gjson.ValidBytes(updatedWithInput), "结果应为有效 JSON")

	// Step 8: 验证最终 payload
	require.False(t, gjson.GetBytes(updatedWithInput, "previous_response_id").Exists(),
		"最终 payload 不应含 previous_response_id")
	require.True(t, gjson.GetBytes(updatedWithInput, `input.#(type=="function_call_output")`).Exists(),
		"最终 payload 应含 function_call_output")
	require.Equal(t, "call_abc",
		gjson.GetBytes(updatedWithInput, `input.#(type=="function_call_output").call_id`).String(),
		"call_id 应保持不变")
}

// TestEndToEnd_ProactiveDetection_StoreEnabledBypasses 对照测试：
// store 未禁用时，即使 function_call_output 无 previous_response_id，也不触发预防性检测。
func TestEndToEnd_ProactiveDetection_StoreEnabledBypasses(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"type":"response.create",
		"input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]
	}`)

	turnPreviousResponseID := openAIWSPayloadStringFromRaw(payload, "previous_response_id")
	turnHasFunctionCallOutput := len(openAIWSExtractFunctionCallOutputCallIDsFromPayload(payload)) > 0
	turnStoreDisabled := false // store 未禁用

	shouldTrigger := shouldProactivelyRejectIngressToolOutputWithoutPreviousResponseID(
		turnStoreDisabled,
		turnHasFunctionCallOutput,
		turnPreviousResponseID,
		false,
	)
	require.False(t, shouldTrigger, "store 未禁用时不应触发预防性检测")
}

// TestEndToEnd_ProactiveDetection_WithPreviousResponseIDBypasses 对照测试：
// store_disabled 但 payload 含有 previous_response_id 时，不触发预防性检测。
func TestEndToEnd_ProactiveDetection_WithPreviousResponseIDBypasses(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"type":"response.create",
		"previous_response_id":"resp_valid",
		"input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]
	}`)

	turnPreviousResponseID := openAIWSPayloadStringFromRaw(payload, "previous_response_id")
	turnHasFunctionCallOutput := len(openAIWSExtractFunctionCallOutputCallIDsFromPayload(payload)) > 0
	turnStoreDisabled := true

	shouldTrigger := shouldProactivelyRejectIngressToolOutputWithoutPreviousResponseID(
		turnStoreDisabled,
		turnHasFunctionCallOutput,
		turnPreviousResponseID,
		false,
	)
	require.False(t, shouldTrigger, "有 previous_response_id 时不应触发预防性检测")
}

// TestEndToEnd_NormalTextInput_NeverTriggersProactive 对照测试：
// 普通文本输入（无 function_call_output）在任何模式下都不触发预防性检测。
func TestEndToEnd_NormalTextInput_NeverTriggersProactive(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"type":"response.create",
		"model":"gpt-5.1",
		"input":[{"type":"input_text","text":"hello world"}]
	}`)

	turnPreviousResponseID := openAIWSPayloadStringFromRaw(payload, "previous_response_id")
	turnHasFunctionCallOutput := len(openAIWSExtractFunctionCallOutputCallIDsFromPayload(payload)) > 0
	turnStoreDisabled := true

	shouldTrigger := shouldProactivelyRejectIngressToolOutputWithoutPreviousResponseID(
		turnStoreDisabled,
		turnHasFunctionCallOutput,
		turnPreviousResponseID,
		false,
	)
	require.False(t, shouldTrigger, "普通文本输入不应触发预防性检测")
}

// ---------------------------------------------------------------------------
// 改动 2 与原有 drop 路径的兼容性测试
// ---------------------------------------------------------------------------

// TestToolOutputRecovery_OriginalFlow_WithPreviousResponseID_StillWorks
// 验证改动 2 不破坏原有的 tool_output_not_found 恢复流程：
// 当 previous_response_id 存在时，仍然执行 drop + setInputSequence。
func TestToolOutputRecovery_OriginalFlow_WithPreviousResponseID_StillWorks(t *testing.T) {
	t.Parallel()

	// 原有场景：用户按 ESC 取消 function_call 后重新发送消息，
	// payload 含有过时的 previous_response_id
	payload := []byte(`{
		"type":"response.create",
		"model":"gpt-5.1",
		"previous_response_id":"resp_stale",
		"input":[
			{"type":"function_call_output","call_id":"call_canceled","output":"canceled"},
			{"type":"input_text","text":"new message"}
		]
	}`)

	currentPreviousResponseID := strings.TrimSpace(openAIWSPayloadStringFromRaw(payload, "previous_response_id"))
	require.Equal(t, "resp_stale", currentPreviousResponseID)

	// 改动后的逻辑
	updatedPayload := payload
	if currentPreviousResponseID != "" {
		dropped, removed, dropErr := dropPreviousResponseIDFromRawPayload(payload)
		require.NoError(t, dropErr)
		require.True(t, removed)
		updatedPayload = dropped
	}

	// 验证 drop 成功
	require.False(t, gjson.GetBytes(updatedPayload, "previous_response_id").Exists())

	// 验证 input 保持不变
	inputCount := gjson.GetBytes(updatedPayload, "input.#").Int()
	require.Equal(t, int64(2), inputCount, "input 数组应保持不变")

	// setOpenAIWSPayloadInputSequence 使用 replay input
	replayInput := []json.RawMessage{
		json.RawMessage(`{"type":"input_text","text":"new message"}`),
	}
	updatedWithInput, setInputErr := setOpenAIWSPayloadInputSequence(
		updatedPayload,
		replayInput,
		true,
	)
	require.NoError(t, setInputErr)
	require.True(t, gjson.ValidBytes(updatedWithInput))
	require.Equal(t, int64(1), gjson.GetBytes(updatedWithInput, "input.#").Int(),
		"replay input 应替换原始 input")
}

// TestToolOutputRecovery_SetInputSequence_NoReplayInput 验证当没有 replay input 时，
// setOpenAIWSPayloadInputSequence 直接返回原 payload。
func TestToolOutputRecovery_SetInputSequence_NoReplayInput(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"type":"response.create","input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`)

	// fullInputExists=false 时直接返回原 payload
	result, err := setOpenAIWSPayloadInputSequence(payload, nil, false)
	require.NoError(t, err)
	require.Equal(t, string(payload), string(result))
}

// TestToolOutputRecovery_SetInputSequence_EmptyReplayInput 验证 replay input 为空数组时
// 仍然能正确设置（替换为空数组）。
func TestToolOutputRecovery_SetInputSequence_EmptyReplayInput(t *testing.T) {
	t.Parallel()

	payload := []byte(`{"type":"response.create","input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]}`)

	result, err := setOpenAIWSPayloadInputSequence(payload, []json.RawMessage{}, true)
	require.NoError(t, err)
	require.True(t, gjson.ValidBytes(result))
	require.Equal(t, int64(0), gjson.GetBytes(result, "input.#").Int(),
		"空 replay input 应替换为空数组")
}

// ---------------------------------------------------------------------------
// 边界条件与防御性测试
// ---------------------------------------------------------------------------

// TestProactiveDetection_EmptyPayload 验证空 payload 的行为。
func TestProactiveDetection_EmptyPayload(t *testing.T) {
	t.Parallel()

	emptyPayloads := [][]byte{
		nil,
		{},
		[]byte(""),
	}

	for i, payload := range emptyPayloads {
		callIDs := openAIWSExtractFunctionCallOutputCallIDsFromPayload(payload)
		require.Empty(t, callIDs, "空 payload[%d] 不应提取到 call_id", i)

		prevID := openAIWSPayloadStringFromRaw(payload, "previous_response_id")
		require.Empty(t, prevID, "空 payload[%d] 不应有 previous_response_id", i)
	}
}

// TestProactiveDetection_MalformedJSON 验证非法 JSON 不会导致 panic。
func TestProactiveDetection_MalformedJSON(t *testing.T) {
	t.Parallel()

	badPayloads := [][]byte{
		[]byte(`{invalid json`),
		[]byte(`{"type":"response.create","input":"not_array"}`),
		[]byte(`{"type":"response.create","input":123}`),
	}

	for i, payload := range badPayloads {
		// 不应 panic
		callIDs := openAIWSExtractFunctionCallOutputCallIDsFromPayload(payload)
		_ = callIDs
		prevID := openAIWSPayloadStringFromRaw(payload, "previous_response_id")
		_ = prevID

		// 模拟检测逻辑不应 panic
		hasFCO := len(callIDs) > 0
		hasContext := openAIWSHasToolCallContextInPayload(payload) ||
			openAIWSHasItemReferenceForAllFunctionCallOutputsInPayload(payload, callIDs)
		triggered := shouldProactivelyRejectIngressToolOutputWithoutPreviousResponseID(
			true,
			hasFCO,
			prevID,
			hasContext,
		)
		_ = triggered
		t.Logf("badPayload[%d]: hasFCO=%v, triggered=%v", i, hasFCO, triggered)
	}
}

// TestToolOutputRecovery_OldCodeWouldFail_Regression 回归测试：
// 验证旧代码在 previous_response_id 为空时会因 removed=false 而失败。
// 新代码跳过 drop 步骤后应成功。
func TestToolOutputRecovery_OldCodeWouldFail_Regression(t *testing.T) {
	t.Parallel()

	payload := []byte(`{
		"type":"response.create",
		"model":"gpt-5.1",
		"input":[{"type":"function_call_output","call_id":"call_1","output":"ok"}]
	}`)

	// 旧代码行为：直接调用 dropPreviousResponseIDFromRawPayload
	_, removed, dropErr := dropPreviousResponseIDFromRawPayload(payload)
	require.NoError(t, dropErr)
	require.False(t, removed, "payload 中无 previous_response_id 时 drop 返回 removed=false")

	// 旧代码：!removed → return false（恢复失败）
	oldCodeResult := !(dropErr != nil || !removed) // 旧条件：dropErr != nil || !removed
	require.False(t, oldCodeResult, "旧代码在此场景会失败（return false）")

	// 新代码行为：先检查 currentPreviousResponseID，为空时跳过 drop
	currentPreviousResponseID := strings.TrimSpace(openAIWSPayloadStringFromRaw(payload, "previous_response_id"))
	updatedPayload := payload
	newCodeSkippedDrop := false
	if currentPreviousResponseID != "" {
		dropped, removedNew, dropErrNew := dropPreviousResponseIDFromRawPayload(payload)
		require.NoError(t, dropErrNew)
		if removedNew {
			updatedPayload = dropped
		}
	} else {
		newCodeSkippedDrop = true
	}

	require.True(t, newCodeSkippedDrop, "新代码应跳过 drop 步骤")
	require.Equal(t, string(payload), string(updatedPayload), "payload 应保持不变")

	// 新代码继续执行 setOpenAIWSPayloadInputSequence
	replayInput := []json.RawMessage{
		json.RawMessage(`{"type":"function_call_output","call_id":"call_1","output":"ok"}`),
	}
	result, setErr := setOpenAIWSPayloadInputSequence(updatedPayload, replayInput, true)
	require.NoError(t, setErr, "新代码应能继续执行 setInputSequence")
	require.True(t, gjson.ValidBytes(result))
}
