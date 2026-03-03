package service

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

// OpenAIWSIngressHooks 定义入站 WS 每个 turn 的生命周期回调。
type OpenAIWSIngressHooks struct {
	BeforeTurn func(turn int) error
	AfterTurn  func(turn int, result *OpenAIForwardResult, turnErr error)
}

func normalizeOpenAIWSLogValue(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "-"
	}
	return openAIWSLogValueReplacer.Replace(trimmed)
}

func truncateOpenAIWSLogValue(value string, maxLen int) string {
	normalized := normalizeOpenAIWSLogValue(value)
	if normalized == "-" || maxLen <= 0 {
		return normalized
	}
	if len(normalized) <= maxLen {
		return normalized
	}
	return normalized[:maxLen] + "..."
}

func openAIWSHeaderValueForLog(headers http.Header, key string) string {
	if headers == nil {
		return "-"
	}
	return truncateOpenAIWSLogValue(headers.Get(key), openAIWSHeaderValueMaxLen)
}

func hasOpenAIWSHeader(headers http.Header, key string) bool {
	if headers == nil {
		return false
	}
	return strings.TrimSpace(headers.Get(key)) != ""
}

type openAIWSSessionHeaderResolution struct {
	SessionID          string
	ConversationID     string
	SessionSource      string
	ConversationSource string
}

func resolveOpenAIWSSessionHeaders(c *gin.Context, promptCacheKey string) openAIWSSessionHeaderResolution {
	resolution := openAIWSSessionHeaderResolution{
		SessionSource:      "none",
		ConversationSource: "none",
	}
	if c != nil && c.Request != nil {
		if sessionID := strings.TrimSpace(c.Request.Header.Get("session_id")); sessionID != "" {
			resolution.SessionID = sessionID
			resolution.SessionSource = "header_session_id"
		}
		if conversationID := strings.TrimSpace(c.Request.Header.Get("conversation_id")); conversationID != "" {
			resolution.ConversationID = conversationID
			resolution.ConversationSource = "header_conversation_id"
			if resolution.SessionID == "" {
				resolution.SessionID = conversationID
				resolution.SessionSource = "header_conversation_id"
			}
		}
	}

	cacheKey := strings.TrimSpace(promptCacheKey)
	if cacheKey != "" {
		if resolution.SessionID == "" {
			resolution.SessionID = cacheKey
			resolution.SessionSource = "prompt_cache_key"
		}
	}
	return resolution
}

func openAIWSIngressSessionScopeFromContext(c *gin.Context) string {
	if c == nil {
		return ""
	}
	value, exists := c.Get("api_key")
	if !exists || value == nil {
		return ""
	}
	apiKey, ok := value.(*APIKey)
	if !ok || apiKey == nil {
		return ""
	}
	userID := apiKey.UserID
	if userID <= 0 && apiKey.User != nil {
		userID = apiKey.User.ID
	}
	apiKeyID := apiKey.ID
	if userID <= 0 && apiKeyID <= 0 {
		return ""
	}
	return fmt.Sprintf("u%d:k%d", userID, apiKeyID)
}

func openAIWSApplySessionScope(sessionHash, scope string) string {
	hash := strings.TrimSpace(sessionHash)
	if hash == "" {
		return ""
	}
	scope = strings.TrimSpace(scope)
	if scope == "" {
		return hash
	}
	return scope + "|" + hash
}

func shouldLogOpenAIWSEvent(idx int, eventType string) bool {
	if idx <= openAIWSEventLogHeadLimit {
		return true
	}
	if openAIWSEventLogEveryN > 0 && idx%openAIWSEventLogEveryN == 0 {
		return true
	}
	if eventType == "error" || isOpenAIWSTerminalEvent(eventType) {
		return true
	}
	return false
}

func shouldLogOpenAIWSBufferedEvent(idx int) bool {
	if idx <= openAIWSBufferLogHeadLimit {
		return true
	}
	if openAIWSBufferLogEveryN > 0 && idx%openAIWSBufferLogEveryN == 0 {
		return true
	}
	return false
}

func openAIWSEventMayContainModel(eventType string) bool {
	switch eventType {
	case "response.created",
		"response.in_progress",
		"response.completed",
		"response.done",
		"response.failed",
		"response.incomplete",
		"response.cancelled",
		"response.canceled":
		return true
	default:
		trimmed := strings.TrimSpace(eventType)
		if trimmed == eventType {
			return false
		}
		switch trimmed {
		case "response.created",
			"response.in_progress",
			"response.completed",
			"response.done",
			"response.failed",
			"response.incomplete",
			"response.cancelled",
			"response.canceled":
			return true
		default:
			return false
		}
	}
}

func openAIWSEventMayContainToolCalls(eventType string) bool {
	if eventType == "" {
		return false
	}
	if strings.Contains(eventType, "function_call") || strings.Contains(eventType, "tool_call") {
		return true
	}
	switch eventType {
	case "response.output_item.added", "response.output_item.done", "response.completed", "response.done":
		return true
	default:
		return false
	}
}

// openAIWSEventShouldParseUsage 判断是否应解析 usage。
// 调用方需确保 eventType 已经过 TrimSpace（如 parseOpenAIWSEventType 的返回值）。
func openAIWSEventShouldParseUsage(eventType string) bool {
	switch eventType {
	case "response.completed", "response.done", "response.failed":
		return true
	default:
		return false
	}
}

// parseOpenAIWSEventType extracts only the event type and response ID from a WS message.
// Use this lightweight version on hot paths where the full response body is not needed.
func parseOpenAIWSEventType(message []byte) (eventType string, responseID string) {
	if len(message) == 0 {
		return "", ""
	}
	values := gjson.GetManyBytes(message, "type", "response.id", "id")
	eventType = strings.TrimSpace(values[0].String())
	if id := strings.TrimSpace(values[1].String()); id != "" {
		responseID = id
	} else {
		responseID = strings.TrimSpace(values[2].String())
	}
	return eventType, responseID
}

func parseOpenAIWSEventEnvelope(message []byte) (eventType string, responseID string, response gjson.Result) {
	if len(message) == 0 {
		return "", "", gjson.Result{}
	}
	values := gjson.GetManyBytes(message, "type", "response.id", "id", "response")
	eventType = strings.TrimSpace(values[0].String())
	if id := strings.TrimSpace(values[1].String()); id != "" {
		responseID = id
	} else {
		responseID = strings.TrimSpace(values[2].String())
	}
	return eventType, responseID, values[3]
}

func openAIWSMessageLikelyContainsToolCalls(message []byte) bool {
	if len(message) == 0 {
		return false
	}
	return bytes.Contains(message, []byte(`"tool_calls"`)) ||
		bytes.Contains(message, []byte(`"tool_call"`)) ||
		bytes.Contains(message, []byte(`"function_call"`))
}

func openAIWSCollectPendingFunctionCallIDsFromJSONResult(result gjson.Result, callIDSet map[string]struct{}, depth int) {
	if !result.Exists() || callIDSet == nil || depth > 8 || result.Type != gjson.JSON {
		return
	}
	itemType := strings.TrimSpace(result.Get("type").String())
	if itemType == "function_call" || itemType == "tool_call" {
		callID := strings.TrimSpace(result.Get("call_id").String())
		if callID == "" {
			fallbackID := strings.TrimSpace(result.Get("id").String())
			if strings.HasPrefix(fallbackID, "call_") {
				callID = fallbackID
			}
		}
		if callID != "" {
			callIDSet[callID] = struct{}{}
		}
	}
	result.ForEach(func(_, child gjson.Result) bool {
		openAIWSCollectPendingFunctionCallIDsFromJSONResult(child, callIDSet, depth+1)
		return true
	})
}

func openAIWSExtractPendingFunctionCallIDsFromEvent(message []byte) []string {
	if len(message) == 0 {
		return nil
	}
	callIDSet := make(map[string]struct{}, 4)
	openAIWSCollectPendingFunctionCallIDsFromJSONResult(gjson.ParseBytes(message), callIDSet, 0)
	if len(callIDSet) == 0 {
		return nil
	}
	callIDs := make([]string, 0, len(callIDSet))
	for callID := range callIDSet {
		callIDs = append(callIDs, callID)
	}
	sort.Strings(callIDs)
	return callIDs
}

func parseOpenAIWSResponseUsageFromCompletedEvent(message []byte, usage *OpenAIUsage) {
	if usage == nil || len(message) == 0 {
		return
	}
	values := gjson.GetManyBytes(
		message,
		"response.usage.input_tokens",
		"response.usage.output_tokens",
		"response.usage.input_tokens_details.cached_tokens",
	)
	usage.InputTokens = int(values[0].Int())
	usage.OutputTokens = int(values[1].Int())
	usage.CacheReadInputTokens = int(values[2].Int())
}

func parseOpenAIWSErrorEventFields(message []byte) (code string, errType string, errMessage string) {
	if len(message) == 0 {
		return "", "", ""
	}
	values := gjson.GetManyBytes(message, "error.code", "error.type", "error.message")
	return strings.TrimSpace(values[0].String()), strings.TrimSpace(values[1].String()), strings.TrimSpace(values[2].String())
}

func summarizeOpenAIWSErrorEventFieldsFromRaw(codeRaw, errTypeRaw, errMessageRaw string) (code string, errType string, errMessage string) {
	code = truncateOpenAIWSLogValue(codeRaw, openAIWSLogValueMaxLen)
	errType = truncateOpenAIWSLogValue(errTypeRaw, openAIWSLogValueMaxLen)
	errMessage = truncateOpenAIWSLogValue(errMessageRaw, openAIWSLogValueMaxLen)
	return code, errType, errMessage
}

func summarizeOpenAIWSErrorEventFields(message []byte) (code string, errType string, errMessage string) {
	if len(message) == 0 {
		return "-", "-", "-"
	}
	return summarizeOpenAIWSErrorEventFieldsFromRaw(parseOpenAIWSErrorEventFields(message))
}

func summarizeOpenAIWSPayloadKeySizes(payload map[string]any, topN int) string {
	if len(payload) == 0 {
		return "-"
	}
	type keySize struct {
		Key  string
		Size int
	}
	sizes := make([]keySize, 0, len(payload))
	for key, value := range payload {
		size := estimateOpenAIWSPayloadValueSize(value, openAIWSPayloadSizeEstimateDepth)
		sizes = append(sizes, keySize{Key: key, Size: size})
	}
	sort.Slice(sizes, func(i, j int) bool {
		if sizes[i].Size == sizes[j].Size {
			return sizes[i].Key < sizes[j].Key
		}
		return sizes[i].Size > sizes[j].Size
	})

	if topN <= 0 || topN > len(sizes) {
		topN = len(sizes)
	}
	parts := make([]string, 0, topN)
	for idx := 0; idx < topN; idx++ {
		item := sizes[idx]
		parts = append(parts, fmt.Sprintf("%s:%d", item.Key, item.Size))
	}
	return strings.Join(parts, ",")
}

func estimateOpenAIWSPayloadValueSize(value any, depth int) int {
	if depth <= 0 {
		return -1
	}
	switch v := value.(type) {
	case nil:
		return 0
	case string:
		return len(v)
	case []byte:
		return len(v)
	case bool:
		return 1
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return 8
	case float32, float64:
		return 8
	case map[string]any:
		if len(v) == 0 {
			return 2
		}
		total := 2
		count := 0
		for key, item := range v {
			count++
			if count > openAIWSPayloadSizeEstimateMaxItems {
				return -1
			}
			itemSize := estimateOpenAIWSPayloadValueSize(item, depth-1)
			if itemSize < 0 {
				return -1
			}
			total += len(key) + itemSize + 3
			if total > openAIWSPayloadSizeEstimateMaxBytes {
				return -1
			}
		}
		return total
	case []any:
		if len(v) == 0 {
			return 2
		}
		total := 2
		limit := len(v)
		if limit > openAIWSPayloadSizeEstimateMaxItems {
			return -1
		}
		for i := 0; i < limit; i++ {
			itemSize := estimateOpenAIWSPayloadValueSize(v[i], depth-1)
			if itemSize < 0 {
				return -1
			}
			total += itemSize + 1
			if total > openAIWSPayloadSizeEstimateMaxBytes {
				return -1
			}
		}
		return total
	default:
		raw, err := json.Marshal(v)
		if err != nil {
			return -1
		}
		if len(raw) > openAIWSPayloadSizeEstimateMaxBytes {
			return -1
		}
		return len(raw)
	}
}

func openAIWSPayloadString(payload map[string]any, key string) string {
	if len(payload) == 0 {
		return ""
	}
	raw, ok := payload[key]
	if !ok {
		return ""
	}
	switch v := raw.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(v)
	case []byte:
		return strings.TrimSpace(string(v))
	default:
		return ""
	}
}

func openAIWSPayloadStringFromRaw(payload []byte, key string) string {
	if len(payload) == 0 || strings.TrimSpace(key) == "" {
		return ""
	}
	return strings.TrimSpace(gjson.GetBytes(payload, key).String())
}

func openAIWSPayloadBoolFromRaw(payload []byte, key string, defaultValue bool) bool {
	if len(payload) == 0 || strings.TrimSpace(key) == "" {
		return defaultValue
	}
	value := gjson.GetBytes(payload, key)
	if !value.Exists() {
		return defaultValue
	}
	if value.Type != gjson.True && value.Type != gjson.False {
		return defaultValue
	}
	return value.Bool()
}

func openAIWSSessionHashesFromID(sessionID string) (string, string) {
	return deriveOpenAISessionHashes(sessionID)
}

func extractOpenAIWSImageURL(value any) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case map[string]any:
		if raw, ok := v["url"].(string); ok {
			return strings.TrimSpace(raw)
		}
	}
	return ""
}

func summarizeOpenAIWSInput(input any) string {
	items, ok := input.([]any)
	if !ok || len(items) == 0 {
		return "-"
	}

	itemCount := len(items)
	textChars := 0
	imageDataURLs := 0
	imageDataURLChars := 0
	imageRemoteURLs := 0

	handleContentItem := func(contentItem map[string]any) {
		contentType, _ := contentItem["type"].(string)
		switch strings.TrimSpace(contentType) {
		case "input_text", "output_text", "text":
			if text, ok := contentItem["text"].(string); ok {
				textChars += len(text)
			}
		case "input_image":
			imageURL := extractOpenAIWSImageURL(contentItem["image_url"])
			if imageURL == "" {
				return
			}
			if strings.HasPrefix(strings.ToLower(imageURL), "data:image/") {
				imageDataURLs++
				imageDataURLChars += len(imageURL)
				return
			}
			imageRemoteURLs++
		}
	}

	handleInputItem := func(inputItem map[string]any) {
		if content, ok := inputItem["content"].([]any); ok {
			for _, rawContent := range content {
				contentItem, ok := rawContent.(map[string]any)
				if !ok {
					continue
				}
				handleContentItem(contentItem)
			}
			return
		}

		itemType, _ := inputItem["type"].(string)
		switch strings.TrimSpace(itemType) {
		case "input_text", "output_text", "text":
			if text, ok := inputItem["text"].(string); ok {
				textChars += len(text)
			}
		case "input_image":
			imageURL := extractOpenAIWSImageURL(inputItem["image_url"])
			if imageURL == "" {
				return
			}
			if strings.HasPrefix(strings.ToLower(imageURL), "data:image/") {
				imageDataURLs++
				imageDataURLChars += len(imageURL)
				return
			}
			imageRemoteURLs++
		}
	}

	for _, rawItem := range items {
		inputItem, ok := rawItem.(map[string]any)
		if !ok {
			continue
		}
		handleInputItem(inputItem)
	}

	return fmt.Sprintf(
		"items=%d,text_chars=%d,image_data_urls=%d,image_data_url_chars=%d,image_remote_urls=%d",
		itemCount,
		textChars,
		imageDataURLs,
		imageDataURLChars,
		imageRemoteURLs,
	)
}

func dropOpenAIWSPayloadKey(payload map[string]any, key string, removed *[]string) {
	if len(payload) == 0 || strings.TrimSpace(key) == "" {
		return
	}
	if _, exists := payload[key]; !exists {
		return
	}
	delete(payload, key)
	*removed = append(*removed, key)
}

// applyOpenAIWSRetryPayloadStrategy 在 WS 连续失败时仅移除无语义字段，
// 避免重试成功却改变原始请求语义。
// 注意：prompt_cache_key 不应在重试中移除；它常用于会话稳定标识（session_id 兜底）。
func applyOpenAIWSRetryPayloadStrategy(payload map[string]any, attempt int) (strategy string, removedKeys []string) {
	if len(payload) == 0 {
		return "empty", nil
	}
	if attempt <= 1 {
		return "full", nil
	}

	removed := make([]string, 0, 2)
	if attempt >= 2 {
		dropOpenAIWSPayloadKey(payload, "include", &removed)
	}

	if len(removed) == 0 {
		return "full", nil
	}
	sort.Strings(removed)
	return "trim_optional_fields", removed
}

func logOpenAIWSModeInfo(format string, args ...any) {
	logger.LegacyPrintf("service.openai_gateway", "[OpenAI WS Mode][openai_ws_mode=true] "+format, args...)
}

func isOpenAIWSModeDebugEnabled() bool {
	return logger.L().Core().Enabled(zap.DebugLevel)
}

func logOpenAIWSModeDebug(format string, args ...any) {
	if !isOpenAIWSModeDebugEnabled() {
		return
	}
	logger.LegacyPrintf("service.openai_gateway", "[debug] [OpenAI WS Mode][openai_ws_mode=true] "+format, args...)
}

func logOpenAIWSBindResponseAccountWarn(groupID, accountID int64, responseID string, err error) {
	if err == nil {
		return
	}
	logger.L().Warn(
		"openai.ws_bind_response_account_failed",
		zap.Int64("group_id", groupID),
		zap.Int64("account_id", accountID),
		zap.String("response_id", truncateOpenAIWSLogValue(responseID, openAIWSIDValueMaxLen)),
		zap.Error(err),
	)
}

func logOpenAIWSIngressTurnAbort(
	accountID int64,
	turn int,
	connID string,
	reason openAIWSIngressTurnAbortReason,
	expected bool,
	cause error,
) {
	causeValue := "-"
	if cause != nil {
		causeValue = truncateOpenAIWSLogValue(cause.Error(), openAIWSLogValueMaxLen)
	}
	logOpenAIWSModeInfo(
		"ingress_ws_turn_aborted account_id=%d turn=%d conn_id=%s reason=%s expected=%v cause=%s",
		accountID,
		turn,
		truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
		normalizeOpenAIWSLogValue(string(reason)),
		expected,
		causeValue,
	)
}

func sortedKeys(m map[string]any) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func dropPreviousResponseIDFromRawPayload(payload []byte) ([]byte, bool, error) {
	return dropPreviousResponseIDFromRawPayloadWithDeleteFn(payload, sjson.DeleteBytes)
}

func dropPreviousResponseIDFromRawPayloadWithDeleteFn(
	payload []byte,
	deleteFn func([]byte, string) ([]byte, error),
) ([]byte, bool, error) {
	if len(payload) == 0 {
		return payload, false, nil
	}
	if !gjson.GetBytes(payload, "previous_response_id").Exists() {
		return payload, false, nil
	}
	if deleteFn == nil {
		deleteFn = sjson.DeleteBytes
	}

	updated := payload
	for i := 0; i < openAIWSMaxPrevResponseIDDeletePasses &&
		gjson.GetBytes(updated, "previous_response_id").Exists(); i++ {
		next, err := deleteFn(updated, "previous_response_id")
		if err != nil {
			return payload, false, err
		}
		updated = next
	}
	return updated, !gjson.GetBytes(updated, "previous_response_id").Exists(), nil
}

func setPreviousResponseIDToRawPayload(payload []byte, previousResponseID string) ([]byte, error) {
	normalizedPrevID := strings.TrimSpace(previousResponseID)
	if len(payload) == 0 || normalizedPrevID == "" {
		return payload, nil
	}
	if current := openAIWSPayloadStringFromRaw(payload, "previous_response_id"); current == normalizedPrevID {
		return payload, nil
	}
	updated, err := sjson.SetBytes(payload, "previous_response_id", normalizedPrevID)
	if err == nil {
		return updated, nil
	}

	var reqBody map[string]any
	if unmarshalErr := json.Unmarshal(payload, &reqBody); unmarshalErr != nil {
		return nil, err
	}
	reqBody["previous_response_id"] = normalizedPrevID
	rebuilt, marshalErr := json.Marshal(reqBody)
	if marshalErr != nil {
		return nil, marshalErr
	}
	return rebuilt, nil
}

func shouldInferIngressFunctionCallOutputPreviousResponseID(
	storeDisabled bool,
	turn int,
	hasFunctionCallOutput bool,
	currentPreviousResponseID string,
	expectedPreviousResponseID string,
) bool {
	if !storeDisabled || turn <= 0 || !hasFunctionCallOutput {
		return false
	}
	if strings.TrimSpace(currentPreviousResponseID) != "" {
		return false
	}
	return strings.TrimSpace(expectedPreviousResponseID) != ""
}

func alignStoreDisabledPreviousResponseID(
	payload []byte,
	expectedPreviousResponseID string,
) ([]byte, bool, error) {
	if len(payload) == 0 {
		return payload, false, nil
	}
	expected := strings.TrimSpace(expectedPreviousResponseID)
	if expected == "" {
		return payload, false, nil
	}
	current := openAIWSPayloadStringFromRaw(payload, "previous_response_id")
	if current == "" || current == expected {
		return payload, false, nil
	}

	// 常见路径（无重复 key）直接 set，避免先 delete 再 set 的双遍处理。
	// 仅在检测到重复 key 时走 drop+set 慢路径，确保最终语义一致。
	if bytes.Count(payload, []byte(`"previous_response_id"`)) <= 1 {
		updated, setErr := setPreviousResponseIDToRawPayload(payload, expected)
		if setErr != nil {
			return payload, false, setErr
		}
		return updated, !bytes.Equal(updated, payload), nil
	}

	withoutPrev, removed, dropErr := dropPreviousResponseIDFromRawPayload(payload)
	if dropErr != nil {
		return payload, false, dropErr
	}
	if !removed {
		return payload, false, nil
	}
	updated, setErr := setPreviousResponseIDToRawPayload(withoutPrev, expected)
	if setErr != nil {
		return payload, false, setErr
	}
	return updated, true, nil
}

func cloneOpenAIWSPayloadBytes(payload []byte) []byte {
	if len(payload) == 0 {
		return nil
	}
	cloned := make([]byte, len(payload))
	copy(cloned, payload)
	return cloned
}

func cloneOpenAIWSRawMessages(items []json.RawMessage) []json.RawMessage {
	if items == nil {
		return nil
	}
	cloned := make([]json.RawMessage, 0, len(items))
	for idx := range items {
		cloned = append(cloned, json.RawMessage(cloneOpenAIWSPayloadBytes(items[idx])))
	}
	return cloned
}

func cloneOpenAIWSJSONRawString(raw string) []byte {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	cloned := make([]byte, len(raw))
	copy(cloned, raw)
	return cloned
}

func normalizeOpenAIWSJSONForCompare(raw []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return nil, errors.New("json is empty")
	}
	var decoded any
	if err := json.Unmarshal(trimmed, &decoded); err != nil {
		return nil, err
	}
	return json.Marshal(decoded)
}

func normalizeOpenAIWSJSONForCompareOrRaw(raw []byte) []byte {
	normalized, err := normalizeOpenAIWSJSONForCompare(raw)
	if err != nil {
		return bytes.TrimSpace(raw)
	}
	return normalized
}

func normalizeOpenAIWSPayloadWithoutInputAndPreviousResponseID(payload []byte) ([]byte, error) {
	if len(payload) == 0 {
		return nil, errors.New("payload is empty")
	}
	var decoded map[string]any
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, err
	}
	delete(decoded, "input")
	delete(decoded, "previous_response_id")
	return json.Marshal(decoded)
}

func openAIWSExtractNormalizedInputSequence(payload []byte) ([]json.RawMessage, bool, error) {
	if len(payload) == 0 {
		return nil, false, nil
	}
	inputValue := gjson.GetBytes(payload, "input")
	if !inputValue.Exists() {
		return nil, false, nil
	}
	if inputValue.Type == gjson.JSON {
		raw := strings.TrimSpace(inputValue.Raw)
		if strings.HasPrefix(raw, "[") {
			var items []json.RawMessage
			if err := json.Unmarshal([]byte(raw), &items); err != nil {
				return nil, true, err
			}
			return items, true, nil
		}
		return []json.RawMessage{json.RawMessage(raw)}, true, nil
	}
	if inputValue.Type == gjson.String {
		encoded, _ := json.Marshal(inputValue.String())
		return []json.RawMessage{encoded}, true, nil
	}
	return []json.RawMessage{json.RawMessage(inputValue.Raw)}, true, nil
}

func openAIWSInputIsPrefixExtended(previousPayload, currentPayload []byte) (bool, error) {
	previousItems, previousExists, prevErr := openAIWSExtractNormalizedInputSequence(previousPayload)
	if prevErr != nil {
		return false, prevErr
	}
	currentItems, currentExists, currentErr := openAIWSExtractNormalizedInputSequence(currentPayload)
	if currentErr != nil {
		return false, currentErr
	}
	if !previousExists && !currentExists {
		return true, nil
	}
	if !previousExists {
		return len(currentItems) == 0, nil
	}
	if !currentExists {
		return len(previousItems) == 0, nil
	}
	if len(currentItems) < len(previousItems) {
		return false, nil
	}

	for idx := range previousItems {
		previousNormalized := normalizeOpenAIWSJSONForCompareOrRaw(previousItems[idx])
		currentNormalized := normalizeOpenAIWSJSONForCompareOrRaw(currentItems[idx])
		if !bytes.Equal(previousNormalized, currentNormalized) {
			return false, nil
		}
	}
	return true, nil
}

func openAIWSRawItemsHasPrefix(items []json.RawMessage, prefix []json.RawMessage) bool {
	if len(prefix) == 0 {
		return true
	}
	if len(items) < len(prefix) {
		return false
	}
	for idx := range prefix {
		previousNormalized := normalizeOpenAIWSJSONForCompareOrRaw(prefix[idx])
		currentNormalized := normalizeOpenAIWSJSONForCompareOrRaw(items[idx])
		if !bytes.Equal(previousNormalized, currentNormalized) {
			return false
		}
	}
	return true
}

func limitOpenAIWSReplayInputSequenceByBytes(items []json.RawMessage, maxBytes int) []json.RawMessage {
	if len(items) == 0 {
		return nil
	}
	if maxBytes <= 0 {
		return cloneOpenAIWSRawMessages(items)
	}

	start := len(items)
	total := 2 // "[]"
	for idx := len(items) - 1; idx >= 0; idx-- {
		itemBytes := len(items[idx])
		if start != len(items) {
			itemBytes++ // comma
		}
		if total+itemBytes > maxBytes {
			// Keep at least the newest item to avoid creating an empty replay input.
			if start == len(items) {
				start = idx
			}
			break
		}
		total += itemBytes
		start = idx
	}
	if start < 0 || start > len(items) {
		start = len(items) - 1
	}
	return cloneOpenAIWSRawMessages(items[start:])
}

func buildOpenAIWSReplayInputSequence(
	previousFullInput []json.RawMessage,
	previousFullInputExists bool,
	currentPayload []byte,
	hasPreviousResponseID bool,
) ([]json.RawMessage, bool, error) {
	currentItems, currentExists, currentErr := openAIWSExtractNormalizedInputSequence(currentPayload)
	if currentErr != nil {
		return nil, false, currentErr
	}
	candidate := []json.RawMessage(nil)
	exists := false
	if !hasPreviousResponseID {
		candidate = cloneOpenAIWSRawMessages(currentItems)
		exists = currentExists
		if !exists {
			return candidate, false, nil
		}
		return limitOpenAIWSReplayInputSequenceByBytes(candidate, openAIWSIngressReplayInputMaxBytes), true, nil
	}
	if !previousFullInputExists {
		candidate = cloneOpenAIWSRawMessages(currentItems)
		exists = currentExists
		if !exists {
			return candidate, false, nil
		}
		return limitOpenAIWSReplayInputSequenceByBytes(candidate, openAIWSIngressReplayInputMaxBytes), true, nil
	}
	if !currentExists || len(currentItems) == 0 {
		candidate = cloneOpenAIWSRawMessages(previousFullInput)
		exists = true
		return limitOpenAIWSReplayInputSequenceByBytes(candidate, openAIWSIngressReplayInputMaxBytes), exists, nil
	}
	if openAIWSRawItemsHasPrefix(currentItems, previousFullInput) {
		candidate = cloneOpenAIWSRawMessages(currentItems)
		exists = true
		return limitOpenAIWSReplayInputSequenceByBytes(candidate, openAIWSIngressReplayInputMaxBytes), exists, nil
	}
	merged := make([]json.RawMessage, 0, len(previousFullInput)+len(currentItems))
	merged = append(merged, cloneOpenAIWSRawMessages(previousFullInput)...)
	merged = append(merged, cloneOpenAIWSRawMessages(currentItems)...)
	candidate = merged
	exists = true
	return limitOpenAIWSReplayInputSequenceByBytes(candidate, openAIWSIngressReplayInputMaxBytes), exists, nil
}

func openAIWSInputAppearsEditedFromPreviousFullInput(
	previousFullInput []json.RawMessage,
	previousFullInputExists bool,
	currentPayload []byte,
	hasPreviousResponseID bool,
) (bool, error) {
	if !hasPreviousResponseID || !previousFullInputExists {
		return false, nil
	}
	currentItems, currentExists, currentErr := openAIWSExtractNormalizedInputSequence(currentPayload)
	if currentErr != nil {
		return false, currentErr
	}
	if !currentExists || len(currentItems) == 0 {
		return false, nil
	}
	if len(previousFullInput) < 2 {
		// Single-item turns are ambiguous (could be a normal incremental replace), avoid false positives.
		return false, nil
	}
	if len(currentItems) < len(previousFullInput) {
		// Most delta appends only send the latest one/few items.
		return false, nil
	}
	if openAIWSRawItemsHasPrefix(currentItems, previousFullInput) {
		// Full snapshot append or unchanged snapshot.
		return false, nil
	}
	return true, nil
}

func setOpenAIWSPayloadInputSequence(
	payload []byte,
	fullInput []json.RawMessage,
	fullInputExists bool,
) ([]byte, error) {
	if !fullInputExists {
		return payload, nil
	}
	// Preserve [] vs null semantics when input exists but is empty.
	inputForMarshal := fullInput
	if inputForMarshal == nil {
		inputForMarshal = []json.RawMessage{}
	}
	inputRaw, marshalErr := json.Marshal(inputForMarshal)
	if marshalErr != nil {
		return nil, marshalErr
	}
	return sjson.SetRawBytes(payload, "input", inputRaw)
}

func openAIWSNormalizeCallIDs(callIDs []string) []string {
	if len(callIDs) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(callIDs))
	normalized := make([]string, 0, len(callIDs))
	for _, callID := range callIDs {
		id := strings.TrimSpace(callID)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		normalized = append(normalized, id)
	}
	sort.Strings(normalized)
	return normalized
}

func openAIWSExtractFunctionCallOutputCallIDsFromPayload(payload []byte) []string {
	if len(payload) == 0 {
		return nil
	}
	input := gjson.GetBytes(payload, "input")
	if !input.Exists() {
		return nil
	}
	callIDSet := make(map[string]struct{}, 4)
	collect := func(item gjson.Result) {
		if item.Type != gjson.JSON {
			return
		}
		if strings.TrimSpace(item.Get("type").String()) != "function_call_output" {
			return
		}
		callID := strings.TrimSpace(item.Get("call_id").String())
		if callID == "" {
			return
		}
		callIDSet[callID] = struct{}{}
	}
	if input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			collect(item)
			return true
		})
	} else {
		collect(input)
	}
	if len(callIDSet) == 0 {
		return nil
	}
	callIDs := make([]string, 0, len(callIDSet))
	for callID := range callIDSet {
		callIDs = append(callIDs, callID)
	}
	sort.Strings(callIDs)
	return callIDs
}

func openAIWSHasToolCallContextInPayload(payload []byte) bool {
	if len(payload) == 0 {
		return false
	}
	input := gjson.GetBytes(payload, "input")
	if !input.Exists() {
		return false
	}

	hasContext := false
	collect := func(item gjson.Result) {
		if hasContext || item.Type != gjson.JSON {
			return
		}
		itemType := strings.TrimSpace(item.Get("type").String())
		if itemType != "tool_call" && itemType != "function_call" {
			return
		}
		if strings.TrimSpace(item.Get("call_id").String()) == "" {
			return
		}
		hasContext = true
	}
	if input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			collect(item)
			return !hasContext
		})
		return hasContext
	}
	collect(input)
	return hasContext
}

func openAIWSHasItemReferenceForAllFunctionCallOutputsInPayload(payload []byte, functionCallOutputCallIDs []string) bool {
	requiredCallIDs := openAIWSNormalizeCallIDs(functionCallOutputCallIDs)
	if len(payload) == 0 || len(requiredCallIDs) == 0 {
		return false
	}
	input := gjson.GetBytes(payload, "input")
	if !input.Exists() {
		return false
	}

	referenceIDSet := make(map[string]struct{}, len(requiredCallIDs))
	collect := func(item gjson.Result) {
		if item.Type != gjson.JSON {
			return
		}
		if strings.TrimSpace(item.Get("type").String()) != "item_reference" {
			return
		}
		referenceID := strings.TrimSpace(item.Get("id").String())
		if referenceID == "" {
			return
		}
		referenceIDSet[referenceID] = struct{}{}
	}
	if input.IsArray() {
		input.ForEach(func(_, item gjson.Result) bool {
			collect(item)
			return true
		})
	} else {
		collect(input)
	}

	if len(referenceIDSet) == 0 {
		return false
	}
	for _, callID := range requiredCallIDs {
		if _, ok := referenceIDSet[callID]; !ok {
			return false
		}
	}
	return true
}

func shouldProactivelyRejectIngressToolOutputWithoutPreviousResponseID(
	storeDisabled bool,
	hasFunctionCallOutput bool,
	previousResponseID string,
	hasToolOutputContext bool,
) bool {
	if !storeDisabled || !hasFunctionCallOutput {
		return false
	}
	if strings.TrimSpace(previousResponseID) != "" {
		return false
	}
	return !hasToolOutputContext
}

func openAIWSFindMissingCallIDs(requiredCallIDs []string, actualCallIDs []string) []string {
	required := openAIWSNormalizeCallIDs(requiredCallIDs)
	if len(required) == 0 {
		return nil
	}
	actualSet := make(map[string]struct{}, len(actualCallIDs))
	for _, callID := range actualCallIDs {
		id := strings.TrimSpace(callID)
		if id == "" {
			continue
		}
		actualSet[id] = struct{}{}
	}
	missing := make([]string, 0, len(required))
	for _, callID := range required {
		if _, ok := actualSet[callID]; ok {
			continue
		}
		missing = append(missing, callID)
	}
	return missing
}

func openAIWSInjectFunctionCallOutputItems(payload []byte, callIDs []string, outputValue string) ([]byte, int, error) {
	normalizedCallIDs := openAIWSNormalizeCallIDs(callIDs)
	if len(normalizedCallIDs) == 0 {
		return payload, 0, nil
	}
	inputItems, inputExists, inputErr := openAIWSExtractNormalizedInputSequence(payload)
	if inputErr != nil {
		return nil, 0, inputErr
	}
	if !inputExists {
		inputItems = []json.RawMessage{}
	}
	updatedInput := make([]json.RawMessage, 0, len(inputItems)+len(normalizedCallIDs))
	updatedInput = append(updatedInput, cloneOpenAIWSRawMessages(inputItems)...)
	for _, callID := range normalizedCallIDs {
		rawItem, marshalErr := json.Marshal(map[string]any{
			"type":    "function_call_output",
			"call_id": callID,
			"output":  outputValue,
		})
		if marshalErr != nil {
			return nil, 0, marshalErr
		}
		updatedInput = append(updatedInput, json.RawMessage(rawItem))
	}
	updatedPayload, setErr := setOpenAIWSPayloadInputSequence(payload, updatedInput, true)
	if setErr != nil {
		return nil, 0, setErr
	}
	return updatedPayload, len(normalizedCallIDs), nil
}

func shouldKeepIngressPreviousResponseID(
	previousPayload []byte,
	currentPayload []byte,
	lastTurnResponseID string,
	hasFunctionCallOutput bool,
	expectedPendingCallIDs []string,
	functionCallOutputCallIDs []string,
) (bool, string, error) {
	if hasFunctionCallOutput {
		if len(expectedPendingCallIDs) == 0 {
			return true, "has_function_call_output", nil
		}
		if len(openAIWSFindMissingCallIDs(expectedPendingCallIDs, functionCallOutputCallIDs)) > 0 {
			return false, "function_call_output_call_id_mismatch", nil
		}
		return true, "function_call_output_call_id_match", nil
	}
	currentPreviousResponseID := strings.TrimSpace(openAIWSPayloadStringFromRaw(currentPayload, "previous_response_id"))
	if currentPreviousResponseID == "" {
		return false, "missing_previous_response_id", nil
	}
	expectedPreviousResponseID := strings.TrimSpace(lastTurnResponseID)
	if expectedPreviousResponseID == "" {
		return false, "missing_last_turn_response_id", nil
	}
	if currentPreviousResponseID != expectedPreviousResponseID {
		return false, "previous_response_id_mismatch", nil
	}
	if len(previousPayload) == 0 {
		return false, "missing_previous_turn_payload", nil
	}

	previousComparable, previousComparableErr := normalizeOpenAIWSPayloadWithoutInputAndPreviousResponseID(previousPayload)
	if previousComparableErr != nil {
		return false, "non_input_compare_error", previousComparableErr
	}
	currentComparable, currentComparableErr := normalizeOpenAIWSPayloadWithoutInputAndPreviousResponseID(currentPayload)
	if currentComparableErr != nil {
		return false, "non_input_compare_error", currentComparableErr
	}
	if !bytes.Equal(previousComparable, currentComparable) {
		return false, "non_input_changed", nil
	}
	return true, "strict_incremental_ok", nil
}

type openAIWSIngressPreviousTurnStrictState struct {
	nonInputComparable []byte
}

func buildOpenAIWSIngressPreviousTurnStrictState(payload []byte) (*openAIWSIngressPreviousTurnStrictState, error) {
	if len(payload) == 0 {
		return nil, nil
	}
	nonInputComparable, nonInputErr := normalizeOpenAIWSPayloadWithoutInputAndPreviousResponseID(payload)
	if nonInputErr != nil {
		return nil, nonInputErr
	}
	return &openAIWSIngressPreviousTurnStrictState{
		nonInputComparable: nonInputComparable,
	}, nil
}

func shouldKeepIngressPreviousResponseIDWithStrictState(
	previousState *openAIWSIngressPreviousTurnStrictState,
	currentPayload []byte,
	lastTurnResponseID string,
	hasFunctionCallOutput bool,
	expectedPendingCallIDs []string,
	functionCallOutputCallIDs []string,
) (bool, string, error) {
	if hasFunctionCallOutput {
		if len(expectedPendingCallIDs) == 0 {
			return true, "has_function_call_output", nil
		}
		if len(openAIWSFindMissingCallIDs(expectedPendingCallIDs, functionCallOutputCallIDs)) > 0 {
			return false, "function_call_output_call_id_mismatch", nil
		}
		return true, "function_call_output_call_id_match", nil
	}
	currentPreviousResponseID := strings.TrimSpace(openAIWSPayloadStringFromRaw(currentPayload, "previous_response_id"))
	if currentPreviousResponseID == "" {
		return false, "missing_previous_response_id", nil
	}
	expectedPreviousResponseID := strings.TrimSpace(lastTurnResponseID)
	if expectedPreviousResponseID == "" {
		return false, "missing_last_turn_response_id", nil
	}
	if currentPreviousResponseID != expectedPreviousResponseID {
		return false, "previous_response_id_mismatch", nil
	}
	if previousState == nil {
		return false, "missing_previous_turn_payload", nil
	}

	currentComparable, currentComparableErr := normalizeOpenAIWSPayloadWithoutInputAndPreviousResponseID(currentPayload)
	if currentComparableErr != nil {
		return false, "non_input_compare_error", currentComparableErr
	}
	if !bytes.Equal(previousState.nonInputComparable, currentComparable) {
		return false, "non_input_changed", nil
	}
	return true, "strict_incremental_ok", nil
}

func payloadAsJSON(payload map[string]any) string {
	return string(payloadAsJSONBytes(payload))
}

func normalizeOpenAIWSPreferredConnID(connID string) (string, bool) {
	trimmed := strings.TrimSpace(connID)
	if trimmed == "" {
		return "", false
	}
	if strings.HasPrefix(trimmed, openAIWSConnIDPrefixCtx) {
		return trimmed, true
	}
	if strings.HasPrefix(trimmed, openAIWSConnIDPrefixLegacy) {
		return trimmed, true
	}
	return "", false
}

func openAIWSPreferredConnIDFromResponse(stateStore OpenAIWSStateStore, responseID string) string {
	if stateStore == nil {
		return ""
	}
	normalizedResponseID := strings.TrimSpace(responseID)
	if normalizedResponseID == "" {
		return ""
	}
	connID, ok := stateStore.GetResponseConn(normalizedResponseID)
	if !ok {
		return ""
	}
	normalizedConnID, ok := normalizeOpenAIWSPreferredConnID(connID)
	if !ok {
		return ""
	}
	return normalizedConnID
}

func payloadAsJSONBytes(payload map[string]any) []byte {
	if len(payload) == 0 {
		return []byte("{}")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return []byte("{}")
	}
	return body
}

func isOpenAIWSTerminalEvent(eventType string) bool {
	switch eventType {
	case "response.completed", "response.done", "response.failed", "response.incomplete", "response.cancelled", "response.canceled":
		return true
	default:
		return false
	}
}

func shouldPersistOpenAIWSLastResponseID(terminalEventType string) bool {
	switch terminalEventType {
	case "response.completed", "response.done":
		return true
	default:
		return false
	}
}

func isOpenAIWSTokenEvent(eventType string) bool {
	if eventType == "" {
		return false
	}
	switch eventType {
	case "response.created", "response.in_progress", "response.output_item.added", "response.output_item.done":
		return false
	}
	if strings.Contains(eventType, ".delta") {
		return true
	}
	if strings.HasPrefix(eventType, "response.output_text") {
		return true
	}
	if strings.HasPrefix(eventType, "response.output") {
		return true
	}
	return eventType == "response.completed" || eventType == "response.done"
}

func replaceOpenAIWSMessageModel(message []byte, fromModel, toModel string) []byte {
	if len(message) == 0 {
		return message
	}
	if strings.TrimSpace(fromModel) == "" || strings.TrimSpace(toModel) == "" || fromModel == toModel {
		return message
	}
	if !bytes.Contains(message, []byte(`"model"`)) || !bytes.Contains(message, []byte(fromModel)) {
		return message
	}
	modelValues := gjson.GetManyBytes(message, "model", "response.model")
	replaceModel := modelValues[0].Exists() && modelValues[0].Str == fromModel
	replaceResponseModel := modelValues[1].Exists() && modelValues[1].Str == fromModel
	if !replaceModel && !replaceResponseModel {
		return message
	}
	updated := message
	if replaceModel {
		if next, err := sjson.SetBytes(updated, "model", toModel); err == nil {
			updated = next
		}
	}
	if replaceResponseModel {
		if next, err := sjson.SetBytes(updated, "response.model", toModel); err == nil {
			updated = next
		}
	}
	return updated
}

func populateOpenAIUsageFromResponseJSON(body []byte, usage *OpenAIUsage) {
	if usage == nil || len(body) == 0 {
		return
	}
	values := gjson.GetManyBytes(
		body,
		"usage.input_tokens",
		"usage.output_tokens",
		"usage.input_tokens_details.cached_tokens",
	)
	usage.InputTokens = int(values[0].Int())
	usage.OutputTokens = int(values[1].Int())
	usage.CacheReadInputTokens = int(values[2].Int())
}

func getOpenAIGroupIDFromContext(c *gin.Context) int64 {
	if c == nil {
		return 0
	}
	value, exists := c.Get("api_key")
	if !exists {
		return 0
	}
	apiKey, ok := value.(*APIKey)
	if !ok || apiKey == nil || apiKey.GroupID == nil {
		return 0
	}
	return *apiKey.GroupID
}

func openAIWSIngressFallbackSessionSeedFromContext(c *gin.Context) string {
	if c == nil {
		return ""
	}
	value, exists := c.Get("api_key")
	if !exists {
		return ""
	}
	apiKey, ok := value.(*APIKey)
	if !ok || apiKey == nil {
		return ""
	}
	gid := int64(0)
	if apiKey.GroupID != nil {
		gid = *apiKey.GroupID
	}
	userID := int64(0)
	if apiKey.User != nil {
		userID = apiKey.User.ID
	}
	return fmt.Sprintf("openai_ws_ingress:%d:%d:%d", gid, userID, apiKey.ID)
}
