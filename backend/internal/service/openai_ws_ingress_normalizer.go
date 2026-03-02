package service

type openAIWSIngressPreSendNormalizeInput struct {
	accountID int64
	turn      int
	connID    string

	currentPayload            []byte
	currentPayloadBytes       int
	currentPreviousResponseID string
	expectedPreviousResponse  string
	pendingExpectedCallIDs    []string
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

// normalizeOpenAIWSIngressPayloadBeforeSend 纯透传 + callID 提取。
// proxy 只负责转发、认证替换、计费，所有边缘场景由 recoverIngressPrevResponseNotFound 兜底。
func normalizeOpenAIWSIngressPayloadBeforeSend(input openAIWSIngressPreSendNormalizeInput) openAIWSIngressPreSendNormalizeOutput {
	callIDs := openAIWSExtractFunctionCallOutputCallIDsFromPayload(input.currentPayload)

	return openAIWSIngressPreSendNormalizeOutput{
		currentPayload:              input.currentPayload,
		currentPayloadBytes:         input.currentPayloadBytes,
		currentPreviousResponseID:   input.currentPreviousResponseID,
		expectedPreviousResponseID:  input.expectedPreviousResponse,
		pendingExpectedCallIDs:      input.pendingExpectedCallIDs,
		functionCallOutputCallIDs:   callIDs,
		hasFunctionCallOutputCallID: len(callIDs) > 0,
	}
}
