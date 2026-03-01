package service

import "encoding/json"

type openAIWSIngressPreSendNormalizeInput struct {
	accountID int64
	turn      int
	connID    string

	storeDisabled bool

	currentPayload            []byte
	currentPayloadBytes       int
	currentPreviousResponseID string
	expectedPreviousResponse  string
	pendingExpectedCallIDs    []string

	lastTurnReplayInput       []json.RawMessage
	lastTurnReplayInputExists bool

	clearSessionLastResponseID func()
}

type openAIWSIngressPreSendNormalizeOutput struct {
	currentPayload              []byte
	currentPayloadBytes         int
	currentPreviousResponseID   string
	expectedPreviousResponseID  string
	pendingExpectedCallIDs      []string
	functionCallOutputCallIDs   []string
	hasFunctionCallOutputCallID bool
}

func normalizeOpenAIWSIngressPayloadBeforeSend(input openAIWSIngressPreSendNormalizeInput) openAIWSIngressPreSendNormalizeOutput {
	currentPayload := input.currentPayload
	currentPayloadBytes := input.currentPayloadBytes
	currentPreviousResponseID := input.currentPreviousResponseID
	expectedPrev := input.expectedPreviousResponse
	pendingExpectedCallIDs := input.pendingExpectedCallIDs

	currentFunctionCallOutputCallIDs := openAIWSExtractFunctionCallOutputCallIDsFromPayload(currentPayload)
	hasFunctionCallOutput := len(currentFunctionCallOutputCallIDs) > 0
	refreshFunctionCallOutputState := func() {
		currentFunctionCallOutputCallIDs = openAIWSExtractFunctionCallOutputCallIDsFromPayload(currentPayload)
		hasFunctionCallOutput = len(currentFunctionCallOutputCallIDs) > 0
	}

	if input.storeDisabled && currentPreviousResponseID != "" && !hasFunctionCallOutput {
		inputEdited, inputEditedErr := openAIWSInputAppearsEditedFromPreviousFullInput(
			input.lastTurnReplayInput,
			input.lastTurnReplayInputExists,
			currentPayload,
			true,
		)
		if inputEditedErr != nil {
			logOpenAIWSModeInfo(
				"ingress_ws_prev_response_input_edit_eval_skip account_id=%d turn=%d conn_id=%s reason=compare_input_error cause=%s previous_response_id=%s expected_previous_response_id=%s",
				input.accountID,
				input.turn,
				truncateOpenAIWSLogValue(input.connID, openAIWSIDValueMaxLen),
				truncateOpenAIWSLogValue(inputEditedErr.Error(), openAIWSLogValueMaxLen),
				truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
				truncateOpenAIWSLogValue(expectedPrev, openAIWSIDValueMaxLen),
			)
		} else if inputEdited {
			updatedPayload, removed, dropErr := dropPreviousResponseIDFromRawPayload(currentPayload)
			if dropErr != nil || !removed {
				dropReason := "not_removed"
				if dropErr != nil {
					dropReason = "drop_error"
				}
				logOpenAIWSModeInfo(
					"ingress_ws_prev_response_input_edit_eval_skip account_id=%d turn=%d conn_id=%s reason=%s previous_response_id=%s expected_previous_response_id=%s",
					input.accountID,
					input.turn,
					truncateOpenAIWSLogValue(input.connID, openAIWSIDValueMaxLen),
					normalizeOpenAIWSLogValue(dropReason),
					truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
					truncateOpenAIWSLogValue(expectedPrev, openAIWSIDValueMaxLen),
				)
			} else {
				droppedPreviousResponseID := currentPreviousResponseID
				currentPayload = updatedPayload
				currentPayloadBytes = len(updatedPayload)
				currentPreviousResponseID = ""
				expectedPrev = ""
				pendingExpectedCallIDs = nil
				if input.clearSessionLastResponseID != nil {
					input.clearSessionLastResponseID()
				}
				logOpenAIWSModeInfo(
					"ingress_ws_prev_response_input_edit_eval account_id=%d turn=%d conn_id=%s action=drop_previous_response_id previous_response_id=%s",
					input.accountID,
					input.turn,
					truncateOpenAIWSLogValue(input.connID, openAIWSIDValueMaxLen),
					truncateOpenAIWSLogValue(droppedPreviousResponseID, openAIWSIDValueMaxLen),
				)
			}
		}
	}
	if currentPreviousResponseID == "" && !hasFunctionCallOutput && expectedPrev != "" && len(pendingExpectedCallIDs) == 0 {
		logOpenAIWSModeInfo(
			"ingress_ws_prev_response_anchor_reset account_id=%d turn=%d conn_id=%s action=clear_session_last_response_id previous_response_id=%s expected_previous_response_id=%s",
			input.accountID,
			input.turn,
			truncateOpenAIWSLogValue(input.connID, openAIWSIDValueMaxLen),
			truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
			truncateOpenAIWSLogValue(expectedPrev, openAIWSIDValueMaxLen),
		)
		if input.clearSessionLastResponseID != nil {
			input.clearSessionLastResponseID()
		}
		expectedPrev = ""
	}
	// Codex 风格前置自愈：若上一轮 response 有未完成 function_call，
	// 自动补齐缺失的 function_call_output(output=aborted) 并保持续链锚点。
	if input.storeDisabled && expectedPrev != "" && len(pendingExpectedCallIDs) > 0 {
		if currentPreviousResponseID == "" {
			updatedPayload, setPrevErr := setPreviousResponseIDToRawPayload(currentPayload, expectedPrev)
			if setPrevErr != nil {
				logOpenAIWSModeInfo(
					"ingress_ws_pending_tool_calls_prev_infer_skip account_id=%d turn=%d conn_id=%s reason=set_previous_response_id_error cause=%s expected_previous_response_id=%s",
					input.accountID,
					input.turn,
					truncateOpenAIWSLogValue(input.connID, openAIWSIDValueMaxLen),
					truncateOpenAIWSLogValue(setPrevErr.Error(), openAIWSLogValueMaxLen),
					truncateOpenAIWSLogValue(expectedPrev, openAIWSIDValueMaxLen),
				)
			} else {
				currentPayload = updatedPayload
				currentPayloadBytes = len(updatedPayload)
				currentPreviousResponseID = expectedPrev
				logOpenAIWSModeInfo(
					"ingress_ws_pending_tool_calls_prev_infer account_id=%d turn=%d conn_id=%s action=set_previous_response_id previous_response_id=%s pending_call_ids=%d",
					input.accountID,
					input.turn,
					truncateOpenAIWSLogValue(input.connID, openAIWSIDValueMaxLen),
					truncateOpenAIWSLogValue(expectedPrev, openAIWSIDValueMaxLen),
					len(pendingExpectedCallIDs),
				)
			}
		} else if currentPreviousResponseID != expectedPrev {
			alignedPayload, aligned, alignErr := alignStoreDisabledPreviousResponseID(currentPayload, expectedPrev)
			switch {
			case alignErr != nil:
				logOpenAIWSModeInfo(
					"ingress_ws_pending_tool_calls_prev_align_skip account_id=%d turn=%d conn_id=%s reason=align_previous_response_id_error cause=%s expected_previous_response_id=%s",
					input.accountID,
					input.turn,
					truncateOpenAIWSLogValue(input.connID, openAIWSIDValueMaxLen),
					truncateOpenAIWSLogValue(alignErr.Error(), openAIWSLogValueMaxLen),
					truncateOpenAIWSLogValue(expectedPrev, openAIWSIDValueMaxLen),
				)
			case aligned:
				currentPayload = alignedPayload
				currentPayloadBytes = len(alignedPayload)
				currentPreviousResponseID = expectedPrev
				logOpenAIWSModeInfo(
					"ingress_ws_pending_tool_calls_prev_align account_id=%d turn=%d conn_id=%s action=align_previous_response_id previous_response_id=%s pending_call_ids=%d",
					input.accountID,
					input.turn,
					truncateOpenAIWSLogValue(input.connID, openAIWSIDValueMaxLen),
					truncateOpenAIWSLogValue(expectedPrev, openAIWSIDValueMaxLen),
					len(pendingExpectedCallIDs),
				)
			}
		}
		refreshFunctionCallOutputState()
		missingCallIDs := openAIWSFindMissingCallIDs(pendingExpectedCallIDs, currentFunctionCallOutputCallIDs)
		if len(missingCallIDs) > 0 {
			updatedPayload, injected, injectErr := openAIWSInjectFunctionCallOutputItems(
				currentPayload,
				missingCallIDs,
				openAIWSAutoAbortedToolOutputValue,
			)
			if injectErr != nil {
				logOpenAIWSModeInfo(
					"ingress_ws_pending_tool_calls_inject_skip account_id=%d turn=%d conn_id=%s reason=inject_aborted_error cause=%s previous_response_id=%s missing_call_ids=%d",
					input.accountID,
					input.turn,
					truncateOpenAIWSLogValue(input.connID, openAIWSIDValueMaxLen),
					truncateOpenAIWSLogValue(injectErr.Error(), openAIWSLogValueMaxLen),
					truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
					len(missingCallIDs),
				)
			} else if injected > 0 {
				currentPayload = updatedPayload
				currentPayloadBytes = len(updatedPayload)
				refreshFunctionCallOutputState()
				logOpenAIWSModeInfo(
					"ingress_ws_pending_tool_calls_inject account_id=%d turn=%d conn_id=%s action=inject_aborted previous_response_id=%s injected_call_ids=%d",
					input.accountID,
					input.turn,
					truncateOpenAIWSLogValue(input.connID, openAIWSIDValueMaxLen),
					truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
					injected,
				)
			}
		}
	}
	// store=false + function_call_output 场景必须有续链锚点。
	// 若客户端未传 previous_response_id，优先回填上一轮响应 ID，避免上游报 call_id 无法关联。
	if shouldInferIngressFunctionCallOutputPreviousResponseID(
		input.storeDisabled,
		input.turn,
		hasFunctionCallOutput,
		currentPreviousResponseID,
		expectedPrev,
	) {
		updatedPayload, setPrevErr := setPreviousResponseIDToRawPayload(currentPayload, expectedPrev)
		if setPrevErr != nil {
			logOpenAIWSModeInfo(
				"ingress_ws_function_call_output_prev_infer_skip account_id=%d turn=%d conn_id=%s reason=set_previous_response_id_error cause=%s expected_previous_response_id=%s",
				input.accountID,
				input.turn,
				truncateOpenAIWSLogValue(input.connID, openAIWSIDValueMaxLen),
				truncateOpenAIWSLogValue(setPrevErr.Error(), openAIWSLogValueMaxLen),
				truncateOpenAIWSLogValue(expectedPrev, openAIWSIDValueMaxLen),
			)
		} else {
			currentPayload = updatedPayload
			currentPayloadBytes = len(updatedPayload)
			currentPreviousResponseID = expectedPrev
			refreshFunctionCallOutputState()
			logOpenAIWSModeInfo(
				"ingress_ws_function_call_output_prev_infer account_id=%d turn=%d conn_id=%s action=set_previous_response_id previous_response_id=%s",
				input.accountID,
				input.turn,
				truncateOpenAIWSLogValue(input.connID, openAIWSIDValueMaxLen),
				truncateOpenAIWSLogValue(expectedPrev, openAIWSIDValueMaxLen),
			)
		}
	}
	// store=false + function_call_output 场景若客户端显式传入了过期 previous_response_id，
	// 优先对齐到上一轮 response.id，避免把 stale 锚点直接送到上游触发 previous_response_not_found。
	if input.storeDisabled && input.turn > 1 && hasFunctionCallOutput {
		alignedPayload, aligned, alignErr := alignStoreDisabledPreviousResponseID(currentPayload, expectedPrev)
		switch {
		case alignErr != nil:
			logOpenAIWSModeInfo(
				"ingress_ws_function_call_output_prev_align_skip account_id=%d turn=%d conn_id=%s reason=align_previous_response_id_error cause=%s expected_previous_response_id=%s",
				input.accountID,
				input.turn,
				truncateOpenAIWSLogValue(input.connID, openAIWSIDValueMaxLen),
				truncateOpenAIWSLogValue(alignErr.Error(), openAIWSLogValueMaxLen),
				truncateOpenAIWSLogValue(expectedPrev, openAIWSIDValueMaxLen),
			)
		case aligned:
			currentPayload = alignedPayload
			currentPayloadBytes = len(alignedPayload)
			currentPreviousResponseID = expectedPrev
			refreshFunctionCallOutputState()
			logOpenAIWSModeInfo(
				"ingress_ws_function_call_output_prev_align account_id=%d turn=%d conn_id=%s action=align_previous_response_id previous_response_id=%s",
				input.accountID,
				input.turn,
				truncateOpenAIWSLogValue(input.connID, openAIWSIDValueMaxLen),
				truncateOpenAIWSLogValue(expectedPrev, openAIWSIDValueMaxLen),
			)
		}
	}

	return openAIWSIngressPreSendNormalizeOutput{
		currentPayload:              currentPayload,
		currentPayloadBytes:         currentPayloadBytes,
		currentPreviousResponseID:   currentPreviousResponseID,
		expectedPreviousResponseID:  expectedPrev,
		pendingExpectedCallIDs:      pendingExpectedCallIDs,
		functionCallOutputCallIDs:   currentFunctionCallOutputCallIDs,
		hasFunctionCallOutputCallID: hasFunctionCallOutput,
	}
}
