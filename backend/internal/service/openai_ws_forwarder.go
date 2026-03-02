package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/pkg/openai"
	"github.com/Wei-Shaw/sub2api/internal/util/responseheaders"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
	"go.uber.org/zap"
)

const (
	openAIWSBetaV1Value        = "responses_websockets=2026-02-04"
	openAIWSBetaV2Value        = "responses_websockets=2026-02-06"
	openAIWSConnIDPrefixLegacy = "oa_ws_"
	openAIWSConnIDPrefixCtx    = "ctxws_"

	openAIWSTurnStateHeader    = "x-codex-turn-state"
	openAIWSTurnMetadataHeader = "x-codex-turn-metadata"

	openAIWSLogValueMaxLen      = 160
	openAIWSHeaderValueMaxLen   = 120
	openAIWSIDValueMaxLen       = 64
	openAIWSEventLogHeadLimit   = 20
	openAIWSEventLogEveryN      = 50
	openAIWSBufferLogHeadLimit  = 8
	openAIWSBufferLogEveryN     = 20
	openAIWSPrewarmEventLogHead = 10
	openAIWSPayloadKeySizeTopN  = 6

	openAIWSPayloadSizeEstimateDepth    = 3
	openAIWSPayloadSizeEstimateMaxBytes = 64 * 1024
	openAIWSPayloadSizeEstimateMaxItems = 16

	openAIWSEventFlushBatchSizeDefault = 4
	openAIWSEventFlushIntervalDefault  = 25 * time.Millisecond
	openAIWSPayloadLogSampleDefault    = 0.2

	openAIWSStoreDisabledConnModeStrict   = "strict"
	openAIWSStoreDisabledConnModeAdaptive = "adaptive"
	openAIWSStoreDisabledConnModeOff      = "off"

	openAIWSIngressStagePreviousResponseNotFound = "previous_response_not_found"
	openAIWSIngressStageToolOutputNotFound       = "tool_output_not_found"
	openAIWSMaxPrevResponseIDDeletePasses        = 8
	openAIWSIngressReplayInputMaxBytes           = 512 * 1024
	openAIWSContinuationUnavailableReason        = "upstream continuation connection is unavailable; please restart the conversation"
	openAIWSAutoAbortedToolOutputValue           = `{"error":"tool call aborted by gateway"}`
	openAIWSClientReadIdleTimeoutDefault         = 30 * time.Minute
	openAIWSIngressClientDisconnectDrainTimeout  = 5 * time.Second
)

type openAIWSIngressTurnAbortReason string

const (
	openAIWSIngressTurnAbortReasonUnknown openAIWSIngressTurnAbortReason = "unknown"

	openAIWSIngressTurnAbortReasonClientClosed            openAIWSIngressTurnAbortReason = "client_closed"
	openAIWSIngressTurnAbortReasonContextCanceled         openAIWSIngressTurnAbortReason = "ctx_canceled"
	openAIWSIngressTurnAbortReasonContextDeadline         openAIWSIngressTurnAbortReason = "ctx_deadline_exceeded"
	openAIWSIngressTurnAbortReasonPreviousResponse        openAIWSIngressTurnAbortReason = openAIWSIngressStagePreviousResponseNotFound
	openAIWSIngressTurnAbortReasonToolOutput              openAIWSIngressTurnAbortReason = openAIWSIngressStageToolOutputNotFound
	openAIWSIngressTurnAbortReasonUpstreamError           openAIWSIngressTurnAbortReason = "upstream_error_event"
	openAIWSIngressTurnAbortReasonWriteUpstream           openAIWSIngressTurnAbortReason = "write_upstream"
	openAIWSIngressTurnAbortReasonReadUpstream            openAIWSIngressTurnAbortReason = "read_upstream"
	openAIWSIngressTurnAbortReasonWriteClient             openAIWSIngressTurnAbortReason = "write_client"
	openAIWSIngressTurnAbortReasonContinuationUnavailable openAIWSIngressTurnAbortReason = "continuation_unavailable"
)

type openAIWSIngressTurnAbortDisposition string

const (
	openAIWSIngressTurnAbortDispositionFailRequest     openAIWSIngressTurnAbortDisposition = "fail_request"
	openAIWSIngressTurnAbortDispositionContinueTurn    openAIWSIngressTurnAbortDisposition = "continue_turn"
	openAIWSIngressTurnAbortDispositionCloseGracefully openAIWSIngressTurnAbortDisposition = "close_gracefully"
)

// openAIWSUpstreamPumpEvent 是上游事件读取泵传递给主 goroutine 的消息载体。
type openAIWSUpstreamPumpEvent struct {
	message []byte
	err     error
}

const (
	// openAIWSUpstreamPumpBufferSize 是上游事件读取泵的缓冲 channel 大小。
	// 缓冲允许上游读取和客户端写入并发执行，吸收客户端写入延迟波动。
	openAIWSUpstreamPumpBufferSize = 16
)

var openAIWSLogValueReplacer = strings.NewReplacer(
	"error", "err",
	"fallback", "fb",
	"warning", "warnx",
	"failed", "fail",
)

var openAIWSIngressPreflightPingIdle = 20 * time.Second

func (s *OpenAIGatewayService) getOpenAIWSIngressContextPool() *openAIWSIngressContextPool {
	if s == nil {
		return nil
	}
	s.openaiWSIngressCtxOnce.Do(func() {
		if s.openaiWSIngressCtxPool == nil {
			pool := newOpenAIWSIngressContextPool(s.cfg)
			// Ensure the scheduler (and its runtime stats) are initialized
			// before wiring load-aware signals into the context pool.
			_ = s.getOpenAIAccountScheduler()
			pool.schedulerStats = s.openaiAccountStats
			s.openaiWSIngressCtxPool = pool
		}
	})
	return s.openaiWSIngressCtxPool
}

type OpenAIWSPerformanceMetricsSnapshot struct {
	Retry     OpenAIWSRetryMetricsSnapshot     `json:"retry"`
	Abort     OpenAIWSAbortMetricsSnapshot     `json:"abort"`
	Transport OpenAIWSTransportMetricsSnapshot `json:"transport"`
}

func (s *OpenAIGatewayService) SnapshotOpenAIWSPerformanceMetrics() OpenAIWSPerformanceMetricsSnapshot {
	ingressPool := s.getOpenAIWSIngressContextPool()
	snapshot := OpenAIWSPerformanceMetricsSnapshot{
		Retry: s.SnapshotOpenAIWSRetryMetrics(),
		Abort: s.SnapshotOpenAIWSAbortMetrics(),
	}
	if ingressPool == nil {
		return snapshot
	}
	snapshot.Transport = ingressPool.SnapshotTransportMetrics()
	return snapshot
}

func (s *OpenAIGatewayService) getOpenAIWSStateStore() OpenAIWSStateStore {
	if s == nil {
		return nil
	}
	s.openaiWSStateStoreOnce.Do(func() {
		if s.openaiWSStateStore == nil {
			s.openaiWSStateStore = NewOpenAIWSStateStore(s.cache)
		}
	})
	return s.openaiWSStateStore
}

func (s *OpenAIGatewayService) openAIWSResponseStickyTTL() time.Duration {
	if s != nil && s.cfg != nil {
		seconds := s.cfg.Gateway.OpenAIWS.StickyResponseIDTTLSeconds
		if seconds > 0 {
			return time.Duration(seconds) * time.Second
		}
	}
	return time.Hour
}

func (s *OpenAIGatewayService) openAIWSIngressPreviousResponseRecoveryEnabled() bool {
	if s != nil && s.cfg != nil {
		return s.cfg.Gateway.OpenAIWS.IngressPreviousResponseRecoveryEnabled
	}
	return true
}

func (s *OpenAIGatewayService) openAIWSReadTimeout() time.Duration {
	if s != nil && s.cfg != nil && s.cfg.Gateway.OpenAIWS.ReadTimeoutSeconds > 0 {
		return time.Duration(s.cfg.Gateway.OpenAIWS.ReadTimeoutSeconds) * time.Second
	}
	return 15 * time.Minute
}

func (s *OpenAIGatewayService) openAIWSClientReadIdleTimeout() time.Duration {
	if s != nil && s.cfg != nil && s.cfg.Gateway.OpenAIWS.ClientReadIdleTimeoutSeconds > 0 {
		return time.Duration(s.cfg.Gateway.OpenAIWS.ClientReadIdleTimeoutSeconds) * time.Second
	}
	return openAIWSClientReadIdleTimeoutDefault
}

func (s *OpenAIGatewayService) openAIWSWriteTimeout() time.Duration {
	if s != nil && s.cfg != nil && s.cfg.Gateway.OpenAIWS.WriteTimeoutSeconds > 0 {
		return time.Duration(s.cfg.Gateway.OpenAIWS.WriteTimeoutSeconds) * time.Second
	}
	return 2 * time.Minute
}

func (s *OpenAIGatewayService) openAIWSEventFlushBatchSize() int {
	if s != nil && s.cfg != nil && s.cfg.Gateway.OpenAIWS.EventFlushBatchSize > 0 {
		return s.cfg.Gateway.OpenAIWS.EventFlushBatchSize
	}
	return openAIWSEventFlushBatchSizeDefault
}

func (s *OpenAIGatewayService) openAIWSEventFlushInterval() time.Duration {
	if s != nil && s.cfg != nil && s.cfg.Gateway.OpenAIWS.EventFlushIntervalMS >= 0 {
		if s.cfg.Gateway.OpenAIWS.EventFlushIntervalMS == 0 {
			return 0
		}
		return time.Duration(s.cfg.Gateway.OpenAIWS.EventFlushIntervalMS) * time.Millisecond
	}
	return openAIWSEventFlushIntervalDefault
}

func (s *OpenAIGatewayService) openAIWSPayloadLogSampleRate() float64 {
	if s != nil && s.cfg != nil {
		rate := s.cfg.Gateway.OpenAIWS.PayloadLogSampleRate
		if rate < 0 {
			return 0
		}
		if rate > 1 {
			return 1
		}
		return rate
	}
	return openAIWSPayloadLogSampleDefault
}

func (s *OpenAIGatewayService) shouldLogOpenAIWSPayloadSchema(attempt int) bool {
	// 首次尝试保留一条完整 payload_schema 便于排障。
	if attempt <= 1 {
		return true
	}
	rate := s.openAIWSPayloadLogSampleRate()
	if rate <= 0 {
		return false
	}
	if rate >= 1 {
		return true
	}
	return rand.Float64() < rate
}

func (s *OpenAIGatewayService) shouldEmitOpenAIWSPayloadSchema(attempt int) bool {
	if !s.shouldLogOpenAIWSPayloadSchema(attempt) {
		return false
	}
	return logger.L().Core().Enabled(zap.DebugLevel)
}

func (s *OpenAIGatewayService) openAIWSDialTimeout() time.Duration {
	if s != nil && s.cfg != nil && s.cfg.Gateway.OpenAIWS.DialTimeoutSeconds > 0 {
		return time.Duration(s.cfg.Gateway.OpenAIWS.DialTimeoutSeconds) * time.Second
	}
	return 10 * time.Second
}

func (s *OpenAIGatewayService) openAIWSAcquireTimeout() time.Duration {
	// Acquire 覆盖“连接复用命中/排队/新建连接”三个阶段。
	// 这里不再叠加 write_timeout，避免高并发排队时把 TTFT 长尾拉到分钟级。
	dial := s.openAIWSDialTimeout()
	if dial <= 0 {
		dial = 10 * time.Second
	}
	return dial + 2*time.Second
}

func (s *OpenAIGatewayService) buildOpenAIResponsesWSURL(account *Account) (string, error) {
	if account == nil {
		return "", errors.New("account is nil")
	}
	var targetURL string
	switch account.Type {
	case AccountTypeOAuth:
		targetURL = chatgptCodexURL
	case AccountTypeAPIKey:
		baseURL := account.GetOpenAIBaseURL()
		if baseURL == "" {
			targetURL = openaiPlatformAPIURL
		} else {
			validatedURL, err := s.validateUpstreamBaseURL(baseURL)
			if err != nil {
				return "", err
			}
			targetURL = buildOpenAIResponsesURL(validatedURL)
		}
	default:
		targetURL = openaiPlatformAPIURL
	}

	parsed, err := url.Parse(strings.TrimSpace(targetURL))
	if err != nil {
		return "", fmt.Errorf("invalid target url: %w", err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		parsed.Scheme = "wss"
	case "http":
		parsed.Scheme = "ws"
	case "wss", "ws":
		// 保持不变
	default:
		return "", fmt.Errorf("unsupported scheme for ws: %s", parsed.Scheme)
	}
	return parsed.String(), nil
}

func (s *OpenAIGatewayService) buildOpenAIWSHeaders(
	c *gin.Context,
	account *Account,
	token string,
	decision OpenAIWSProtocolDecision,
	isCodexCLI bool,
	turnState string,
	turnMetadata string,
	promptCacheKey string,
) (http.Header, openAIWSSessionHeaderResolution) {
	headers := make(http.Header)
	headers.Set("authorization", "Bearer "+token)

	sessionResolution := resolveOpenAIWSSessionHeaders(c, promptCacheKey)
	if c != nil && c.Request != nil {
		if v := strings.TrimSpace(c.Request.Header.Get("accept-language")); v != "" {
			headers.Set("accept-language", v)
		}
	}
	if sessionResolution.SessionID != "" {
		headers.Set("session_id", sessionResolution.SessionID)
	}
	if sessionResolution.ConversationID != "" {
		headers.Set("conversation_id", sessionResolution.ConversationID)
	}
	if state := strings.TrimSpace(turnState); state != "" {
		headers.Set(openAIWSTurnStateHeader, state)
	}
	if metadata := strings.TrimSpace(turnMetadata); metadata != "" {
		headers.Set(openAIWSTurnMetadataHeader, metadata)
	}

	if account != nil && account.Type == AccountTypeOAuth {
		if chatgptAccountID := account.GetChatGPTAccountID(); chatgptAccountID != "" {
			headers.Set("chatgpt-account-id", chatgptAccountID)
		}
		if isCodexCLI {
			headers.Set("originator", "codex_cli_rs")
		} else {
			headers.Set("originator", "opencode")
		}
	}

	betaValue := openAIWSBetaV2Value
	if decision.Transport == OpenAIUpstreamTransportResponsesWebsocket {
		betaValue = openAIWSBetaV1Value
	}
	headers.Set("OpenAI-Beta", betaValue)

	customUA := ""
	if account != nil {
		customUA = account.GetOpenAIUserAgent()
	}
	if strings.TrimSpace(customUA) != "" {
		headers.Set("user-agent", customUA)
	} else if c != nil {
		if ua := strings.TrimSpace(c.GetHeader("User-Agent")); ua != "" {
			headers.Set("user-agent", ua)
		}
	}
	if s != nil && s.cfg != nil && s.cfg.Gateway.ForceCodexCLI {
		headers.Set("user-agent", codexCLIUserAgent)
	}
	if account != nil && account.Type == AccountTypeOAuth && !openai.IsCodexCLIRequest(headers.Get("user-agent")) {
		headers.Set("user-agent", codexCLIUserAgent)
	}
	if account != nil && account.Type == AccountTypeOAuth && openai.IsCodexCLIRequest(headers.Get("user-agent")) {
		// 保持 OAuth 握手头的一致性：Codex 风格 UA 必须搭配 codex_cli_rs originator。
		headers.Set("originator", "codex_cli_rs")
	}

	return headers, sessionResolution
}

func (s *OpenAIGatewayService) buildOpenAIWSCreatePayload(reqBody map[string]any, account *Account) map[string]any {
	// OpenAI WS Mode 协议：response.create 字段与 HTTP /responses 基本一致。
	// 保留 stream 字段（与 Codex CLI 一致），仅移除 background。
	payload := make(map[string]any, len(reqBody)+1)
	for k, v := range reqBody {
		payload[k] = v
	}

	delete(payload, "background")
	if _, exists := payload["stream"]; !exists {
		payload["stream"] = true
	}
	payload["type"] = "response.create"

	// OAuth 默认保持 store=false，避免误依赖服务端历史。
	if account != nil && account.Type == AccountTypeOAuth && !s.isOpenAIWSStoreRecoveryAllowed(account) {
		payload["store"] = false
	}
	return payload
}

func setOpenAIWSTurnMetadata(payload map[string]any, turnMetadata string) {
	if len(payload) == 0 {
		return
	}
	metadata := strings.TrimSpace(turnMetadata)
	if metadata == "" {
		return
	}

	switch existing := payload["client_metadata"].(type) {
	case map[string]any:
		existing[openAIWSTurnMetadataHeader] = metadata
		payload["client_metadata"] = existing
	case map[string]string:
		next := make(map[string]any, len(existing)+1)
		for k, v := range existing {
			next[k] = v
		}
		next[openAIWSTurnMetadataHeader] = metadata
		payload["client_metadata"] = next
	default:
		payload["client_metadata"] = map[string]any{
			openAIWSTurnMetadataHeader: metadata,
		}
	}
}

func (s *OpenAIGatewayService) isOpenAIWSStoreRecoveryAllowed(account *Account) bool {
	if account != nil && account.IsOpenAIWSAllowStoreRecoveryEnabled() {
		return true
	}
	if s != nil && s.cfg != nil && s.cfg.Gateway.OpenAIWS.AllowStoreRecovery {
		return true
	}
	return false
}

func (s *OpenAIGatewayService) isOpenAIWSStoreDisabledInRequest(reqBody map[string]any, account *Account) bool {
	if account != nil && account.Type == AccountTypeOAuth && !s.isOpenAIWSStoreRecoveryAllowed(account) {
		return true
	}
	if len(reqBody) == 0 {
		return false
	}
	rawStore, ok := reqBody["store"]
	if !ok {
		return false
	}
	storeEnabled, ok := rawStore.(bool)
	if !ok {
		return false
	}
	return !storeEnabled
}

func (s *OpenAIGatewayService) isOpenAIWSStoreDisabledInRequestRaw(reqBody []byte, account *Account) bool {
	if account != nil && account.Type == AccountTypeOAuth && !s.isOpenAIWSStoreRecoveryAllowed(account) {
		return true
	}
	if len(reqBody) == 0 {
		return false
	}
	storeValue := gjson.GetBytes(reqBody, "store")
	if !storeValue.Exists() {
		return false
	}
	if storeValue.Type != gjson.True && storeValue.Type != gjson.False {
		return false
	}
	return !storeValue.Bool()
}

func (s *OpenAIGatewayService) openAIWSStoreDisabledConnMode() string {
	if s == nil || s.cfg == nil {
		return openAIWSStoreDisabledConnModeStrict
	}
	mode := strings.ToLower(strings.TrimSpace(s.cfg.Gateway.OpenAIWS.StoreDisabledConnMode))
	switch mode {
	case openAIWSStoreDisabledConnModeStrict, openAIWSStoreDisabledConnModeAdaptive, openAIWSStoreDisabledConnModeOff:
		return mode
	case "":
		// 兼容旧配置：仅配置了布尔开关时按旧语义推导。
		if s.cfg.Gateway.OpenAIWS.StoreDisabledForceNewConn {
			return openAIWSStoreDisabledConnModeStrict
		}
		return openAIWSStoreDisabledConnModeOff
	default:
		return openAIWSStoreDisabledConnModeStrict
	}
}

func shouldForceNewConnOnStoreDisabled(mode, lastFailureReason string) bool {
	switch mode {
	case openAIWSStoreDisabledConnModeOff:
		return false
	case openAIWSStoreDisabledConnModeAdaptive:
		reason := strings.TrimPrefix(strings.TrimSpace(lastFailureReason), "prewarm_")
		switch reason {
		case "policy_violation", "message_too_big", "auth_failed", "write_request", "write":
			return true
		default:
			return false
		}
	default:
		return true
	}
}

func (s *OpenAIGatewayService) forwardOpenAIWSV2(
	ctx context.Context,
	c *gin.Context,
	account *Account,
	reqBody map[string]any,
	token string,
	decision OpenAIWSProtocolDecision,
	isCodexCLI bool,
	reqStream bool,
	originalModel string,
	mappedModel string,
	startTime time.Time,
	attempt int,
	lastFailureReason string,
) (result *OpenAIForwardResult, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			logger.LegacyPrintf(
				"service.openai_ws_forwarder",
				"[OpenAIWS] recovered panic in forwardOpenAIWSV2: panic=%v stack=%s",
				recovered,
				string(debug.Stack()),
			)
			err = fmt.Errorf("openai ws panic recovered: %v", recovered)
			result = nil
		}
	}()

	if s == nil || account == nil {
		return nil, wrapOpenAIWSFallback("invalid_state", errors.New("service or account is nil"))
	}

	wsURL, err := s.buildOpenAIResponsesWSURL(account)
	if err != nil {
		return nil, wrapOpenAIWSFallback("build_ws_url", err)
	}
	wsHost := "-"
	wsPath := "-"
	if parsed, parseErr := url.Parse(wsURL); parseErr == nil && parsed != nil {
		if h := strings.TrimSpace(parsed.Host); h != "" {
			wsHost = normalizeOpenAIWSLogValue(h)
		}
		if p := strings.TrimSpace(parsed.Path); p != "" {
			wsPath = normalizeOpenAIWSLogValue(p)
		}
	}
	logOpenAIWSModeDebug(
		"dial_target account_id=%d account_type=%s ws_host=%s ws_path=%s",
		account.ID,
		account.Type,
		wsHost,
		wsPath,
	)

	payload := s.buildOpenAIWSCreatePayload(reqBody, account)
	payloadStrategy, removedKeys := applyOpenAIWSRetryPayloadStrategy(payload, attempt)
	previousResponseID := openAIWSPayloadString(payload, "previous_response_id")
	previousResponseIDKind := ClassifyOpenAIPreviousResponseIDKind(previousResponseID)
	promptCacheKey := openAIWSPayloadString(payload, "prompt_cache_key")
	_, hasTools := payload["tools"]
	debugEnabled := isOpenAIWSModeDebugEnabled()
	payloadBytes := -1
	resolvePayloadBytes := func() int {
		if payloadBytes >= 0 {
			return payloadBytes
		}
		payloadBytes = len(payloadAsJSONBytes(payload))
		return payloadBytes
	}
	streamValue := "-"
	if raw, ok := payload["stream"]; ok {
		streamValue = normalizeOpenAIWSLogValue(strings.TrimSpace(fmt.Sprintf("%v", raw)))
	}
	turnState := ""
	turnMetadata := ""
	if c != nil && c.Request != nil {
		turnState = strings.TrimSpace(c.GetHeader(openAIWSTurnStateHeader))
		turnMetadata = strings.TrimSpace(c.GetHeader(openAIWSTurnMetadataHeader))
	}
	setOpenAIWSTurnMetadata(payload, turnMetadata)
	payloadEventType := openAIWSPayloadString(payload, "type")
	if payloadEventType == "" {
		payloadEventType = "response.create"
	}
	if s.shouldEmitOpenAIWSPayloadSchema(attempt) {
		logOpenAIWSModeInfo(
			"[debug] payload_schema account_id=%d attempt=%d event=%s payload_keys=%s payload_bytes=%d payload_key_sizes=%s input_summary=%s stream=%s payload_strategy=%s removed_keys=%s has_previous_response_id=%v has_prompt_cache_key=%v has_tools=%v",
			account.ID,
			attempt,
			payloadEventType,
			normalizeOpenAIWSLogValue(strings.Join(sortedKeys(payload), ",")),
			resolvePayloadBytes(),
			normalizeOpenAIWSLogValue(summarizeOpenAIWSPayloadKeySizes(payload, openAIWSPayloadKeySizeTopN)),
			normalizeOpenAIWSLogValue(summarizeOpenAIWSInput(payload["input"])),
			streamValue,
			normalizeOpenAIWSLogValue(payloadStrategy),
			normalizeOpenAIWSLogValue(strings.Join(removedKeys, ",")),
			previousResponseID != "",
			promptCacheKey != "",
			hasTools,
		)
	}

	stateStore := s.getOpenAIWSStateStore()
	groupID := getOpenAIGroupIDFromContext(c)
	sessionHash := s.GenerateSessionHashWithFallback(c, nil, openAIWSIngressFallbackSessionSeedFromContext(c))
	if sessionHash == "" {
		var legacySessionHash string
		sessionHash, legacySessionHash = openAIWSSessionHashesFromID(promptCacheKey)
		attachOpenAILegacySessionHashToGin(c, legacySessionHash)
	}
	if turnState == "" && stateStore != nil && sessionHash != "" {
		if savedTurnState, ok := stateStore.GetSessionTurnState(groupID, sessionHash); ok {
			turnState = savedTurnState
		}
	}
	preferredConnID := ""
	if stateStore != nil && previousResponseID != "" {
		preferredConnID = openAIWSPreferredConnIDFromResponse(stateStore, previousResponseID)
	}
	storeDisabled := s.isOpenAIWSStoreDisabledInRequest(reqBody, account)
	if stateStore != nil && storeDisabled && previousResponseID == "" && sessionHash != "" {
		if connID, ok := stateStore.GetSessionConn(groupID, sessionHash); ok {
			preferredConnID = connID
		}
	}
	storeDisabledConnMode := s.openAIWSStoreDisabledConnMode()
	forceNewConnByPolicy := shouldForceNewConnOnStoreDisabled(storeDisabledConnMode, lastFailureReason)
	forceNewConn := forceNewConnByPolicy && storeDisabled && previousResponseID == "" && sessionHash != "" && preferredConnID == ""
	wsHeaders, sessionResolution := s.buildOpenAIWSHeaders(c, account, token, decision, isCodexCLI, turnState, turnMetadata, promptCacheKey)
	logOpenAIWSModeDebug(
		"acquire_start account_id=%d account_type=%s transport=%s preferred_conn_id=%s has_previous_response_id=%v session_hash=%s has_turn_state=%v turn_state_len=%d has_turn_metadata=%v turn_metadata_len=%d store_disabled=%v store_disabled_conn_mode=%s retry_last_reason=%s force_new_conn=%v header_user_agent=%s header_openai_beta=%s header_originator=%s header_accept_language=%s header_session_id=%s header_conversation_id=%s session_id_source=%s conversation_id_source=%s has_prompt_cache_key=%v has_chatgpt_account_id=%v has_authorization=%v has_session_id=%v has_conversation_id=%v proxy_enabled=%v",
		account.ID,
		account.Type,
		normalizeOpenAIWSLogValue(string(decision.Transport)),
		truncateOpenAIWSLogValue(preferredConnID, openAIWSIDValueMaxLen),
		previousResponseID != "",
		truncateOpenAIWSLogValue(sessionHash, 12),
		turnState != "",
		len(turnState),
		turnMetadata != "",
		len(turnMetadata),
		storeDisabled,
		normalizeOpenAIWSLogValue(storeDisabledConnMode),
		truncateOpenAIWSLogValue(lastFailureReason, openAIWSLogValueMaxLen),
		forceNewConn,
		openAIWSHeaderValueForLog(wsHeaders, "user-agent"),
		openAIWSHeaderValueForLog(wsHeaders, "openai-beta"),
		openAIWSHeaderValueForLog(wsHeaders, "originator"),
		openAIWSHeaderValueForLog(wsHeaders, "accept-language"),
		openAIWSHeaderValueForLog(wsHeaders, "session_id"),
		openAIWSHeaderValueForLog(wsHeaders, "conversation_id"),
		normalizeOpenAIWSLogValue(sessionResolution.SessionSource),
		normalizeOpenAIWSLogValue(sessionResolution.ConversationSource),
		promptCacheKey != "",
		hasOpenAIWSHeader(wsHeaders, "chatgpt-account-id"),
		hasOpenAIWSHeader(wsHeaders, "authorization"),
		hasOpenAIWSHeader(wsHeaders, "session_id"),
		hasOpenAIWSHeader(wsHeaders, "conversation_id"),
		account.ProxyID != nil && account.Proxy != nil,
	)

	acquireCtx, acquireCancel := context.WithTimeout(ctx, s.openAIWSAcquireTimeout())
	defer acquireCancel()

	ingressCtxPool := s.getOpenAIWSIngressContextPool()
	if ingressCtxPool == nil {
		return nil, wrapOpenAIWSFallback("ctx_pool_unavailable", errors.New("openai ws ingress context pool is nil"))
	}
	sessionHashForCtx := strings.TrimSpace(sessionHash)
	if sessionHashForCtx == "" {
		sessionHashForCtx = fmt.Sprintf("httpws:%d:%d", account.ID, startTime.UnixNano())
	}
	if forceNewConn {
		sessionHashForCtx = fmt.Sprintf("%s:retry:%d", sessionHashForCtx, attempt)
	}
	ownerID := fmt.Sprintf("httpws_%d_%d", account.ID, attempt)
	lease, err := ingressCtxPool.Acquire(acquireCtx, openAIWSIngressContextAcquireRequest{
		Account:     account,
		GroupID:     groupID,
		SessionHash: sessionHashForCtx,
		OwnerID:     ownerID,
		WSURL:       wsURL,
		Headers:     cloneHeader(wsHeaders),
		ProxyURL: func() string {
			if account.ProxyID != nil && account.Proxy != nil {
				return account.Proxy.URL()
			}
			return ""
		}(),
		Turn:                  1,
		HasPreviousResponseID: previousResponseID != "",
		StrictAffinity:        previousResponseID != "",
		StoreDisabled:         storeDisabled,
	})
	if err != nil {
		dialStatus, dialClass, dialCloseStatus, dialCloseReason, dialRespServer, dialRespVia, dialRespCFRay, dialRespReqID := summarizeOpenAIWSDialError(err)
		logOpenAIWSModeInfo(
			"acquire_fail account_id=%d account_type=%s transport=%s reason=%s dial_status=%d dial_class=%s dial_close_status=%s dial_close_reason=%s dial_resp_server=%s dial_resp_via=%s dial_resp_cf_ray=%s dial_resp_x_request_id=%s cause=%s preferred_conn_id=%s force_new_conn=%v ws_host=%s ws_path=%s proxy_enabled=%v",
			account.ID,
			account.Type,
			normalizeOpenAIWSLogValue(string(decision.Transport)),
			normalizeOpenAIWSLogValue(classifyOpenAIWSAcquireError(err)),
			dialStatus,
			dialClass,
			dialCloseStatus,
			truncateOpenAIWSLogValue(dialCloseReason, openAIWSHeaderValueMaxLen),
			dialRespServer,
			dialRespVia,
			dialRespCFRay,
			dialRespReqID,
			truncateOpenAIWSLogValue(err.Error(), openAIWSLogValueMaxLen),
			truncateOpenAIWSLogValue(preferredConnID, openAIWSIDValueMaxLen),
			forceNewConn,
			wsHost,
			wsPath,
			account.ProxyID != nil && account.Proxy != nil,
		)
		return nil, wrapOpenAIWSFallback(classifyOpenAIWSAcquireError(err), err)
	}
	defer lease.Release()
	connID := strings.TrimSpace(lease.ConnID())
	logOpenAIWSModeDebug(
		"connected account_id=%d account_type=%s transport=%s conn_id=%s conn_reused=%v conn_pick_ms=%d queue_wait_ms=%d has_previous_response_id=%v",
		account.ID,
		account.Type,
		normalizeOpenAIWSLogValue(string(decision.Transport)),
		connID,
		lease.Reused(),
		lease.ConnPickDuration().Milliseconds(),
		lease.QueueWaitDuration().Milliseconds(),
		previousResponseID != "",
	)
	if previousResponseID != "" {
		logOpenAIWSModeInfo(
			"continuation_probe account_id=%d account_type=%s conn_id=%s previous_response_id=%s previous_response_id_kind=%s preferred_conn_id=%s conn_reused=%v store_disabled=%v session_hash=%s header_session_id=%s header_conversation_id=%s session_id_source=%s conversation_id_source=%s has_turn_state=%v turn_state_len=%d has_prompt_cache_key=%v",
			account.ID,
			account.Type,
			truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
			truncateOpenAIWSLogValue(previousResponseID, openAIWSIDValueMaxLen),
			normalizeOpenAIWSLogValue(previousResponseIDKind),
			truncateOpenAIWSLogValue(preferredConnID, openAIWSIDValueMaxLen),
			lease.Reused(),
			storeDisabled,
			truncateOpenAIWSLogValue(sessionHash, 12),
			openAIWSHeaderValueForLog(wsHeaders, "session_id"),
			openAIWSHeaderValueForLog(wsHeaders, "conversation_id"),
			normalizeOpenAIWSLogValue(sessionResolution.SessionSource),
			normalizeOpenAIWSLogValue(sessionResolution.ConversationSource),
			turnState != "",
			len(turnState),
			promptCacheKey != "",
		)
	}
	if c != nil {
		SetOpsLatencyMs(c, OpsOpenAIWSConnPickMsKey, lease.ConnPickDuration().Milliseconds())
		SetOpsLatencyMs(c, OpsOpenAIWSQueueWaitMsKey, lease.QueueWaitDuration().Milliseconds())
		c.Set(OpsOpenAIWSConnReusedKey, lease.Reused())
		if connID != "" {
			c.Set(OpsOpenAIWSConnIDKey, connID)
		}
	}

	handshakeTurnState := strings.TrimSpace(lease.HandshakeHeader(openAIWSTurnStateHeader))
	logOpenAIWSModeDebug(
		"handshake account_id=%d conn_id=%s has_turn_state=%v turn_state_len=%d",
		account.ID,
		connID,
		handshakeTurnState != "",
		len(handshakeTurnState),
	)
	if handshakeTurnState != "" {
		if stateStore != nil && sessionHash != "" {
			stateStore.BindSessionTurnState(groupID, sessionHash, handshakeTurnState, s.openAIWSSessionStickyTTL())
		}
		if c != nil {
			c.Header(http.CanonicalHeaderKey(openAIWSTurnStateHeader), handshakeTurnState)
		}
	}

	if err := s.performOpenAIWSGeneratePrewarm(
		ctx,
		lease,
		decision,
		payload,
		previousResponseID,
		reqBody,
		account,
		stateStore,
		groupID,
	); err != nil {
		return nil, err
	}

	if err := lease.WriteJSONWithContextTimeout(ctx, payload, s.openAIWSWriteTimeout()); err != nil {
		lease.MarkBroken()
		logOpenAIWSModeInfo(
			"write_request_fail account_id=%d conn_id=%s cause=%s payload_bytes=%d",
			account.ID,
			connID,
			truncateOpenAIWSLogValue(err.Error(), openAIWSLogValueMaxLen),
			resolvePayloadBytes(),
		)
		return nil, wrapOpenAIWSFallback("write_request", err)
	}
	if debugEnabled {
		logOpenAIWSModeDebug(
			"write_request_sent account_id=%d conn_id=%s stream=%v payload_bytes=%d previous_response_id=%s",
			account.ID,
			connID,
			reqStream,
			resolvePayloadBytes(),
			truncateOpenAIWSLogValue(previousResponseID, openAIWSIDValueMaxLen),
		)
	}

	usage := &OpenAIUsage{}
	var firstTokenMs *int
	responseID := ""
	var finalResponse []byte
	wroteDownstream := false
	needModelReplace := originalModel != mappedModel
	var mappedModelBytes []byte
	if needModelReplace && mappedModel != "" {
		mappedModelBytes = []byte(mappedModel)
	}
	bufferedStreamEvents := make([][]byte, 0, 4)
	eventCount := 0
	tokenEventCount := 0
	terminalEventCount := 0
	bufferedEventCount := 0
	flushedBufferedEventCount := 0
	firstEventType := ""
	lastEventType := ""

	var flusher http.Flusher
	if reqStream {
		if s.responseHeaderFilter != nil {
			responseheaders.WriteFilteredHeaders(c.Writer.Header(), http.Header{}, s.responseHeaderFilter)
		}
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")
		c.Header("X-Accel-Buffering", "no")
		f, ok := c.Writer.(http.Flusher)
		if !ok {
			lease.MarkBroken()
			return nil, wrapOpenAIWSFallback("streaming_not_supported", errors.New("streaming not supported"))
		}
		flusher = f
	}

	clientDisconnected := false
	var downstreamWriteErr error
	var requestCtx context.Context
	if c != nil && c.Request != nil {
		requestCtx = c.Request.Context()
	}
	flushBatchSize := s.openAIWSEventFlushBatchSize()
	flushInterval := s.openAIWSEventFlushInterval()
	pendingFlushEvents := 0
	lastFlushAt := time.Now()
	flushStreamWriter := func(force bool) {
		if clientDisconnected || flusher == nil || pendingFlushEvents <= 0 {
			return
		}
		if !force && flushBatchSize > 1 && pendingFlushEvents < flushBatchSize {
			if flushInterval <= 0 || time.Since(lastFlushAt) < flushInterval {
				return
			}
		}
		flusher.Flush()
		pendingFlushEvents = 0
		lastFlushAt = time.Now()
	}
	var sseFrameBuf []byte
	emitStreamMessage := func(message []byte, forceFlush bool) {
		if clientDisconnected || downstreamWriteErr != nil {
			return
		}
		sseFrameBuf = sseFrameBuf[:0]
		sseFrameBuf = append(sseFrameBuf, "data: "...)
		sseFrameBuf = append(sseFrameBuf, message...)
		sseFrameBuf = append(sseFrameBuf, '\n', '\n')
		_, wErr := c.Writer.Write(sseFrameBuf)
		if wErr == nil {
			wroteDownstream = true
			pendingFlushEvents++
			flushStreamWriter(forceFlush)
			return
		}
		if isOpenAIWSStreamWriteDisconnectError(wErr, requestCtx) {
			clientDisconnected = true
			logger.LegacyPrintf(
				"service.openai_gateway",
				"[OpenAI WS Mode] client disconnected, continue draining upstream: account=%d conn_id=%s",
				account.ID,
				connID,
			)
			return
		}
		downstreamWriteErr = wErr
		setOpsUpstreamError(c, 0, sanitizeUpstreamErrorMessage(wErr.Error()), "")
		logOpenAIWSModeInfo(
			"stream_write_fail account_id=%d conn_id=%s wrote_downstream=%v cause=%s",
			account.ID,
			connID,
			wroteDownstream,
			truncateOpenAIWSLogValue(wErr.Error(), openAIWSLogValueMaxLen),
		)
	}
	flushBufferedStreamEvents := func(reason string) {
		if len(bufferedStreamEvents) == 0 {
			return
		}
		flushed := len(bufferedStreamEvents)
		for _, buffered := range bufferedStreamEvents {
			emitStreamMessage(buffered, false)
			if downstreamWriteErr != nil {
				break
			}
		}
		bufferedStreamEvents = bufferedStreamEvents[:0]
		flushStreamWriter(true)
		flushedBufferedEventCount += flushed
		if debugEnabled {
			logOpenAIWSModeDebug(
				"buffer_flush account_id=%d conn_id=%s reason=%s flushed=%d total_flushed=%d client_disconnected=%v",
				account.ID,
				connID,
				truncateOpenAIWSLogValue(reason, openAIWSLogValueMaxLen),
				flushed,
				flushedBufferedEventCount,
				clientDisconnected,
			)
		}
	}

	readTimeout := s.openAIWSReadTimeout()

	for {
		message, readErr := lease.ReadMessageWithContextTimeout(ctx, readTimeout)
		if readErr != nil {
			lease.MarkBroken()
			closeStatus, closeReason := summarizeOpenAIWSReadCloseError(readErr)
			logOpenAIWSModeInfo(
				"read_fail account_id=%d conn_id=%s wrote_downstream=%v close_status=%s close_reason=%s cause=%s events=%d token_events=%d terminal_events=%d buffered_pending=%d buffered_flushed=%d first_event=%s last_event=%s",
				account.ID,
				connID,
				wroteDownstream,
				closeStatus,
				closeReason,
				truncateOpenAIWSLogValue(readErr.Error(), openAIWSLogValueMaxLen),
				eventCount,
				tokenEventCount,
				terminalEventCount,
				len(bufferedStreamEvents),
				flushedBufferedEventCount,
				truncateOpenAIWSLogValue(firstEventType, openAIWSLogValueMaxLen),
				truncateOpenAIWSLogValue(lastEventType, openAIWSLogValueMaxLen),
			)
			if !wroteDownstream {
				return nil, wrapOpenAIWSFallback(classifyOpenAIWSReadFallbackReason(readErr), readErr)
			}
			if clientDisconnected {
				break
			}
			setOpsUpstreamError(c, 0, sanitizeUpstreamErrorMessage(readErr.Error()), "")
			return nil, fmt.Errorf("openai ws read event: %w", readErr)
		}

		eventType, eventResponseID, responseField := parseOpenAIWSEventEnvelope(message)
		if eventType == "" {
			continue
		}
		eventCount++
		if firstEventType == "" {
			firstEventType = eventType
		}
		lastEventType = eventType

		if responseID == "" && eventResponseID != "" {
			responseID = eventResponseID
		}

		isTokenEvent := isOpenAIWSTokenEvent(eventType)
		if isTokenEvent {
			tokenEventCount++
		}
		isTerminalEvent := isOpenAIWSTerminalEvent(eventType)
		if isTerminalEvent {
			terminalEventCount++
		}
		if firstTokenMs == nil && isTokenEvent {
			ms := int(time.Since(startTime).Milliseconds())
			firstTokenMs = &ms
		}
		if debugEnabled && shouldLogOpenAIWSEvent(eventCount, eventType) {
			logOpenAIWSModeDebug(
				"event_received account_id=%d conn_id=%s idx=%d type=%s bytes=%d token=%v terminal=%v buffered_pending=%d",
				account.ID,
				connID,
				eventCount,
				truncateOpenAIWSLogValue(eventType, openAIWSLogValueMaxLen),
				len(message),
				isTokenEvent,
				isTerminalEvent,
				len(bufferedStreamEvents),
			)
		}

		if !clientDisconnected {
			if needModelReplace && len(mappedModelBytes) > 0 && openAIWSEventMayContainModel(eventType) && bytes.Contains(message, mappedModelBytes) {
				message = replaceOpenAIWSMessageModel(message, mappedModel, originalModel)
			}
			if openAIWSEventMayContainToolCalls(eventType) && openAIWSMessageLikelyContainsToolCalls(message) {
				if corrected, changed := s.toolCorrector.CorrectToolCallsInSSEBytes(message); changed {
					message = corrected
				}
			}
		}
		if openAIWSEventShouldParseUsage(eventType) {
			parseOpenAIWSResponseUsageFromCompletedEvent(message, usage)
		}

		if eventType == "error" {
			errCodeRaw, errTypeRaw, errMsgRaw := parseOpenAIWSErrorEventFields(message)
			errMsg := strings.TrimSpace(errMsgRaw)
			if errMsg == "" {
				errMsg = "Upstream websocket error"
			}
			fallbackReason, canFallback := classifyOpenAIWSErrorEventFromRaw(errCodeRaw, errTypeRaw, errMsgRaw)
			errCode, errType, errMessage := summarizeOpenAIWSErrorEventFieldsFromRaw(errCodeRaw, errTypeRaw, errMsgRaw)
			logOpenAIWSModeInfo(
				"error_event account_id=%d conn_id=%s idx=%d fallback_reason=%s can_fallback=%v err_code=%s err_type=%s err_message=%s",
				account.ID,
				connID,
				eventCount,
				truncateOpenAIWSLogValue(fallbackReason, openAIWSLogValueMaxLen),
				canFallback,
				errCode,
				errType,
				errMessage,
			)
			if fallbackReason == "previous_response_not_found" {
				logOpenAIWSModeInfo(
					"previous_response_not_found_diag account_id=%d account_type=%s conn_id=%s previous_response_id=%s previous_response_id_kind=%s response_id=%s event_idx=%d req_stream=%v store_disabled=%v conn_reused=%v session_hash=%s header_session_id=%s header_conversation_id=%s session_id_source=%s conversation_id_source=%s has_turn_state=%v turn_state_len=%d has_prompt_cache_key=%v err_code=%s err_type=%s err_message=%s",
					account.ID,
					account.Type,
					connID,
					truncateOpenAIWSLogValue(previousResponseID, openAIWSIDValueMaxLen),
					normalizeOpenAIWSLogValue(previousResponseIDKind),
					truncateOpenAIWSLogValue(responseID, openAIWSIDValueMaxLen),
					eventCount,
					reqStream,
					storeDisabled,
					lease.Reused(),
					truncateOpenAIWSLogValue(sessionHash, 12),
					openAIWSHeaderValueForLog(wsHeaders, "session_id"),
					openAIWSHeaderValueForLog(wsHeaders, "conversation_id"),
					normalizeOpenAIWSLogValue(sessionResolution.SessionSource),
					normalizeOpenAIWSLogValue(sessionResolution.ConversationSource),
					turnState != "",
					len(turnState),
					promptCacheKey != "",
					errCode,
					errType,
					errMessage,
				)
			}
			// error 事件后连接不再可复用，避免回池后污染下一请求。
			lease.MarkBroken()
			if !wroteDownstream && canFallback {
				return nil, wrapOpenAIWSFallback(fallbackReason, errors.New(errMsg))
			}
			statusCode := openAIWSErrorHTTPStatusFromRaw(errCodeRaw, errTypeRaw)
			setOpsUpstreamError(c, statusCode, errMsg, "")
			if reqStream && !clientDisconnected {
				if shouldFlushOpenAIWSBufferedEventsOnError(reqStream, wroteDownstream, clientDisconnected) {
					flushBufferedStreamEvents("error_event")
				} else {
					bufferedStreamEvents = bufferedStreamEvents[:0]
				}
				emitStreamMessage(message, true)
				if downstreamWriteErr != nil {
					lease.MarkBroken()
					return nil, fmt.Errorf("openai ws stream write: %w", downstreamWriteErr)
				}
			}
			if !reqStream {
				c.JSON(statusCode, gin.H{
					"error": gin.H{
						"type":    "upstream_error",
						"message": errMsg,
					},
				})
			}
			return nil, fmt.Errorf("openai ws error event: %s", errMsg)
		}

		if reqStream {
			// 在首个 token 前先缓冲事件（如 response.created），
			// 以便上游早期断连时仍可安全回退到 HTTP，不给下游发送半截流。
			shouldBuffer := firstTokenMs == nil && !isTokenEvent && !isTerminalEvent
			if shouldBuffer {
				buffered := make([]byte, len(message))
				copy(buffered, message)
				bufferedStreamEvents = append(bufferedStreamEvents, buffered)
				bufferedEventCount++
				if debugEnabled && shouldLogOpenAIWSBufferedEvent(bufferedEventCount) {
					logOpenAIWSModeDebug(
						"buffer_enqueue account_id=%d conn_id=%s idx=%d event_idx=%d event_type=%s buffer_size=%d",
						account.ID,
						connID,
						bufferedEventCount,
						eventCount,
						truncateOpenAIWSLogValue(eventType, openAIWSLogValueMaxLen),
						len(bufferedStreamEvents),
					)
				}
			} else {
				flushBufferedStreamEvents(eventType)
				emitStreamMessage(message, isTerminalEvent)
				if downstreamWriteErr != nil {
					lease.MarkBroken()
					return nil, fmt.Errorf("openai ws stream write: %w", downstreamWriteErr)
				}
			}
		} else {
			if responseField.Exists() && responseField.Type == gjson.JSON {
				finalResponse = cloneOpenAIWSJSONRawString(responseField.Raw)
			}
		}

		if isTerminalEvent {
			break
		}
	}

	if !reqStream {
		if len(finalResponse) == 0 {
			logOpenAIWSModeInfo(
				"missing_final_response account_id=%d conn_id=%s events=%d token_events=%d terminal_events=%d wrote_downstream=%v",
				account.ID,
				connID,
				eventCount,
				tokenEventCount,
				terminalEventCount,
				wroteDownstream,
			)
			if !wroteDownstream {
				return nil, wrapOpenAIWSFallback("missing_final_response", errors.New("no terminal response payload"))
			}
			return nil, errors.New("ws finished without final response")
		}

		if needModelReplace {
			finalResponse = s.replaceModelInResponseBody(finalResponse, mappedModel, originalModel)
		}
		finalResponse = s.correctToolCallsInResponseBody(finalResponse)
		populateOpenAIUsageFromResponseJSON(finalResponse, usage)
		if responseID == "" {
			responseID = strings.TrimSpace(gjson.GetBytes(finalResponse, "id").String())
		}

		c.Data(http.StatusOK, "application/json", finalResponse)
	} else {
		flushStreamWriter(true)
	}

	if responseID != "" && stateStore != nil {
		ttl := s.openAIWSResponseStickyTTL()
		logOpenAIWSBindResponseAccountWarn(groupID, account.ID, responseID, stateStore.BindResponseAccount(ctx, groupID, responseID, account.ID, ttl))
		if connID, ok := normalizeOpenAIWSPreferredConnID(lease.ConnID()); ok {
			stateStore.BindResponseConn(responseID, connID, ttl)
		}
		if sessionHash != "" && shouldPersistOpenAIWSLastResponseID(lastEventType) {
			stateStore.BindSessionLastResponseID(groupID, sessionHash, responseID, s.openAIWSSessionStickyTTL())
		} else if sessionHash != "" {
			stateStore.DeleteSessionLastResponseID(groupID, sessionHash)
		}
	}
	if stateStore != nil && storeDisabled && sessionHash != "" {
		stateStore.BindSessionConn(groupID, sessionHash, lease.ConnID(), s.openAIWSSessionStickyTTL())
	}
	firstTokenMsValue := -1
	if firstTokenMs != nil {
		firstTokenMsValue = *firstTokenMs
	}
	logOpenAIWSModeDebug(
		"completed account_id=%d conn_id=%s response_id=%s stream=%v duration_ms=%d events=%d token_events=%d terminal_events=%d buffered_events=%d buffered_flushed=%d first_event=%s last_event=%s first_token_ms=%d wrote_downstream=%v client_disconnected=%v",
		account.ID,
		connID,
		truncateOpenAIWSLogValue(strings.TrimSpace(responseID), openAIWSIDValueMaxLen),
		reqStream,
		time.Since(startTime).Milliseconds(),
		eventCount,
		tokenEventCount,
		terminalEventCount,
		bufferedEventCount,
		flushedBufferedEventCount,
		truncateOpenAIWSLogValue(firstEventType, openAIWSLogValueMaxLen),
		truncateOpenAIWSLogValue(lastEventType, openAIWSLogValueMaxLen),
		firstTokenMsValue,
		wroteDownstream,
		clientDisconnected,
	)

	return &OpenAIForwardResult{
		RequestID:         responseID,
		Usage:             *usage,
		Model:             originalModel,
		ReasoningEffort:   extractOpenAIReasoningEffort(reqBody, originalModel),
		Stream:            reqStream,
		OpenAIWSMode:      true,
		Duration:          time.Since(startTime),
		FirstTokenMs:      firstTokenMs,
		TerminalEventType: strings.TrimSpace(lastEventType),
	}, nil
}

// ProxyResponsesWebSocketFromClient 处理客户端入站 WebSocket（OpenAI Responses WS Mode）并转发到上游。
// 当前实现按“单请求 -> 终止事件 -> 下一请求”的顺序代理，适配 Codex CLI 的 turn 模式。
func (s *OpenAIGatewayService) ProxyResponsesWebSocketFromClient(
	ctx context.Context,
	c *gin.Context,
	clientConn *coderws.Conn,
	account *Account,
	token string,
	firstClientMessage []byte,
	hooks *OpenAIWSIngressHooks,
) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			const panicCloseReason = "internal websocket proxy panic"
			logger.LegacyPrintf(
				"service.openai_ws_forwarder",
				"[OpenAIWS] recovered panic in ProxyResponsesWebSocketFromClient: panic=%v stack=%s",
				recovered,
				string(debug.Stack()),
			)
			err = NewOpenAIWSClientCloseError(
				coderws.StatusInternalError,
				panicCloseReason,
				fmt.Errorf("panic recovered: %v", recovered),
			)
		}
	}()

	if s == nil {
		return errors.New("service is nil")
	}
	if c == nil {
		return errors.New("gin context is nil")
	}
	if clientConn == nil {
		return errors.New("client websocket is nil")
	}
	if account == nil {
		return errors.New("account is nil")
	}
	if strings.TrimSpace(token) == "" {
		return errors.New("token is empty")
	}

	wsDecision := s.getOpenAIWSProtocolResolver().Resolve(account)
	modeRouterV2Enabled := s != nil && s.cfg != nil && s.cfg.Gateway.OpenAIWS.ModeRouterV2Enabled
	if !modeRouterV2Enabled {
		return NewOpenAIWSClientCloseError(
			coderws.StatusPolicyViolation,
			"websocket mode requires mode_router_v2 with ctx_pool",
			nil,
		)
	}
	ingressMode := account.ResolveOpenAIResponsesWebSocketV2Mode(s.cfg.Gateway.OpenAIWS.IngressModeDefault)
	logOpenAIWSModeInfo(
		"ingress_ws_validate account_id=%d ingress_mode=%s transport=%s",
		account.ID,
		normalizeOpenAIWSLogValue(string(ingressMode)),
		normalizeOpenAIWSLogValue(string(wsDecision.Transport)),
	)
	if ingressMode == OpenAIWSIngressModeOff {
		return NewOpenAIWSClientCloseError(
			coderws.StatusPolicyViolation,
			"websocket mode is disabled for this account",
			nil,
		)
	}
	if ingressMode != OpenAIWSIngressModeCtxPool {
		return NewOpenAIWSClientCloseError(
			coderws.StatusPolicyViolation,
			"websocket mode only supports ctx_pool",
			nil,
		)
	}
	// Ingress ws_v2 请求天然是 Codex 会话语义，ctx_pool 是否启用仅由账号 mode 决定。
	ctxPoolMode := ingressMode == OpenAIWSIngressModeCtxPool
	ctxPoolSessionScope := ""
	if ctxPoolMode {
		ctxPoolSessionScope = openAIWSIngressSessionScopeFromContext(c)
	}
	if wsDecision.Transport != OpenAIUpstreamTransportResponsesWebsocketV2 {
		return fmt.Errorf("websocket ingress requires ws_v2 transport, got=%s", wsDecision.Transport)
	}
	wsURL, err := s.buildOpenAIResponsesWSURL(account)
	if err != nil {
		return fmt.Errorf("build ws url: %w", err)
	}
	wsHost := "-"
	wsPath := "-"
	if parsedURL, parseErr := url.Parse(wsURL); parseErr == nil && parsedURL != nil {
		wsHost = normalizeOpenAIWSLogValue(parsedURL.Host)
		wsPath = normalizeOpenAIWSLogValue(parsedURL.Path)
	}
	debugEnabled := isOpenAIWSModeDebugEnabled()
	logOpenAIWSModeInfo(
		"ingress_ws_session_init account_id=%d ws_host=%s ws_path=%s ctx_pool=%v session_scope=%s debug=%v",
		account.ID,
		wsHost,
		wsPath,
		ctxPoolMode,
		truncateOpenAIWSLogValue(ctxPoolSessionScope, openAIWSIDValueMaxLen),
		debugEnabled,
	)

	type openAIWSClientPayload struct {
		payloadRaw         []byte
		rawForHash         []byte
		promptCacheKey     string
		previousResponseID string
		originalModel      string
		payloadBytes       int
	}

	applyPayloadMutation := func(current []byte, path string, value any) ([]byte, error) {
		next, err := sjson.SetBytes(current, path, value)
		if err == nil {
			return next, nil
		}

		// 仅在确实需要修改 payload 且 sjson 失败时，退回 map 路径确保兼容性。
		payload := make(map[string]any)
		if unmarshalErr := json.Unmarshal(current, &payload); unmarshalErr != nil {
			return nil, err
		}
		switch path {
		case "type", "model":
			payload[path] = value
		case "client_metadata." + openAIWSTurnMetadataHeader:
			setOpenAIWSTurnMetadata(payload, fmt.Sprintf("%v", value))
		default:
			return nil, err
		}
		rebuilt, marshalErr := json.Marshal(payload)
		if marshalErr != nil {
			return nil, marshalErr
		}
		return rebuilt, nil
	}

	parseClientPayload := func(raw []byte) (openAIWSClientPayload, error) {
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 {
			return openAIWSClientPayload{}, NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "empty websocket request payload", nil)
		}
		if !gjson.ValidBytes(trimmed) {
			return openAIWSClientPayload{}, NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "invalid websocket request payload", errors.New("invalid json"))
		}

		values := gjson.GetManyBytes(trimmed, "type", "model", "prompt_cache_key", "previous_response_id")
		eventType := strings.TrimSpace(values[0].String())
		normalized := trimmed
		switch eventType {
		case "":
			eventType = "response.create"
			next, setErr := applyPayloadMutation(normalized, "type", eventType)
			if setErr != nil {
				return openAIWSClientPayload{}, NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "invalid websocket request payload", setErr)
			}
			normalized = next
		case "response.create":
		case "response.append":
			return openAIWSClientPayload{}, NewOpenAIWSClientCloseError(
				coderws.StatusPolicyViolation,
				"response.append is not supported in ws v2; use response.create with previous_response_id",
				nil,
			)
		default:
			return openAIWSClientPayload{}, NewOpenAIWSClientCloseError(
				coderws.StatusPolicyViolation,
				fmt.Sprintf("unsupported websocket request type: %s", eventType),
				nil,
			)
		}

		originalModel := strings.TrimSpace(values[1].String())
		if originalModel == "" {
			return openAIWSClientPayload{}, NewOpenAIWSClientCloseError(
				coderws.StatusPolicyViolation,
				"model is required in response.create payload",
				nil,
			)
		}
		promptCacheKey := strings.TrimSpace(values[2].String())
		previousResponseID := strings.TrimSpace(values[3].String())
		previousResponseIDKind := ClassifyOpenAIPreviousResponseIDKind(previousResponseID)
		if previousResponseID != "" && previousResponseIDKind == OpenAIPreviousResponseIDKindMessageID {
			return openAIWSClientPayload{}, NewOpenAIWSClientCloseError(
				coderws.StatusPolicyViolation,
				"previous_response_id must be a response.id (resp_*), not a message id",
				nil,
			)
		}
		if turnMetadata := strings.TrimSpace(c.GetHeader(openAIWSTurnMetadataHeader)); turnMetadata != "" {
			next, setErr := applyPayloadMutation(normalized, "client_metadata."+openAIWSTurnMetadataHeader, turnMetadata)
			if setErr != nil {
				return openAIWSClientPayload{}, NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "invalid websocket request payload", setErr)
			}
			normalized = next
		}
		mappedModel := account.GetMappedModel(originalModel)
		if normalizedModel := normalizeCodexModel(mappedModel); normalizedModel != "" {
			mappedModel = normalizedModel
		}
		if mappedModel != originalModel {
			next, setErr := applyPayloadMutation(normalized, "model", mappedModel)
			if setErr != nil {
				return openAIWSClientPayload{}, NewOpenAIWSClientCloseError(coderws.StatusPolicyViolation, "invalid websocket request payload", setErr)
			}
			normalized = next
		}

		return openAIWSClientPayload{
			payloadRaw:         normalized,
			rawForHash:         trimmed,
			promptCacheKey:     promptCacheKey,
			previousResponseID: previousResponseID,
			originalModel:      originalModel,
			payloadBytes:       len(normalized),
		}, nil
	}

	firstPayload, err := parseClientPayload(firstClientMessage)
	if err != nil {
		return err
	}

	turnState := strings.TrimSpace(c.GetHeader(openAIWSTurnStateHeader))
	stateStore := s.getOpenAIWSStateStore()
	groupID := getOpenAIGroupIDFromContext(c)
	fallbackSessionSeed := openAIWSIngressFallbackSessionSeedFromContext(c)
	legacySessionHash := strings.TrimSpace(s.GenerateSessionHashWithFallback(c, firstPayload.rawForHash, fallbackSessionSeed))
	sessionHash := legacySessionHash
	if ctxPoolMode {
		sessionHash = openAIWSApplySessionScope(legacySessionHash, ctxPoolSessionScope)
	}
	resolveSessionTurnState := func() (string, bool) {
		if stateStore == nil || sessionHash == "" {
			return "", false
		}
		if savedTurnState, ok := stateStore.GetSessionTurnState(groupID, sessionHash); ok {
			return savedTurnState, true
		}
		if !ctxPoolMode || legacySessionHash == "" || legacySessionHash == sessionHash {
			return "", false
		}
		return stateStore.GetSessionTurnState(groupID, legacySessionHash)
	}
	resolveSessionLastResponseID := func() (string, bool) {
		if stateStore == nil || sessionHash == "" {
			return "", false
		}
		if savedResponseID, ok := stateStore.GetSessionLastResponseID(groupID, sessionHash); ok {
			return strings.TrimSpace(savedResponseID), true
		}
		if !ctxPoolMode || legacySessionHash == "" || legacySessionHash == sessionHash {
			return "", false
		}
		savedResponseID, ok := stateStore.GetSessionLastResponseID(groupID, legacySessionHash)
		return strings.TrimSpace(savedResponseID), ok
	}
	if turnState == "" && stateStore != nil && sessionHash != "" {
		if savedTurnState, ok := resolveSessionTurnState(); ok {
			turnState = savedTurnState
		}
	}
	sessionLastResponseID := ""
	if stateStore != nil && sessionHash != "" {
		if savedResponseID, ok := resolveSessionLastResponseID(); ok {
			sessionLastResponseID = savedResponseID
		}
	}

	preferredConnID := ""
	if stateStore != nil && firstPayload.previousResponseID != "" {
		preferredConnID = openAIWSPreferredConnIDFromResponse(stateStore, firstPayload.previousResponseID)
	}

	storeDisabled := s.isOpenAIWSStoreDisabledInRequestRaw(firstPayload.payloadRaw, account)
	storeDisabledConnMode := s.openAIWSStoreDisabledConnMode()

	isCodexCLI := openai.IsCodexCLIRequest(c.GetHeader("User-Agent")) || (s.cfg != nil && s.cfg.Gateway.ForceCodexCLI)
	wsHeaders, _ := s.buildOpenAIWSHeaders(c, account, token, wsDecision, isCodexCLI, turnState, strings.TrimSpace(c.GetHeader(openAIWSTurnMetadataHeader)), firstPayload.promptCacheKey)
	baseAcquireReq := struct {
		WSURL    string
		Headers  http.Header
		ProxyURL string
	}{
		WSURL:   wsURL,
		Headers: wsHeaders,
		ProxyURL: func() string {
			if account.ProxyID != nil && account.Proxy != nil {
				return account.Proxy.URL()
			}
			return ""
		}(),
	}

	ingressCtxPool := s.getOpenAIWSIngressContextPool()
	if ingressCtxPool == nil {
		return errors.New("openai ws ingress context pool is nil")
	}

	logOpenAIWSModeInfo(
		"ingress_ws_protocol_confirm account_id=%d account_type=%s transport=%s ws_host=%s ws_path=%s ws_mode=%s ctx_pool_mode=%v store_disabled=%v has_session_hash=%v has_previous_response_id=%v",
		account.ID,
		account.Type,
		normalizeOpenAIWSLogValue(string(wsDecision.Transport)),
		wsHost,
		wsPath,
		normalizeOpenAIWSLogValue(ingressMode),
		ctxPoolMode,
		storeDisabled,
		sessionHash != "",
		firstPayload.previousResponseID != "",
	)

	if debugEnabled {
		logOpenAIWSModeDebug(
			"ingress_ws_start account_id=%d account_type=%s transport=%s ws_host=%s preferred_conn_id=%s has_session_hash=%v has_previous_response_id=%v store_disabled=%v ctx_pool_mode=%v",
			account.ID,
			account.Type,
			normalizeOpenAIWSLogValue(string(wsDecision.Transport)),
			wsHost,
			truncateOpenAIWSLogValue(preferredConnID, openAIWSIDValueMaxLen),
			sessionHash != "",
			firstPayload.previousResponseID != "",
			storeDisabled,
			ctxPoolMode,
		)
	}
	if firstPayload.previousResponseID != "" {
		firstPreviousResponseIDKind := ClassifyOpenAIPreviousResponseIDKind(firstPayload.previousResponseID)
		logOpenAIWSModeInfo(
			"ingress_ws_continuation_probe account_id=%d turn=%d previous_response_id=%s previous_response_id_kind=%s preferred_conn_id=%s session_hash=%s header_session_id=%s header_conversation_id=%s has_turn_state=%v turn_state_len=%d has_prompt_cache_key=%v store_disabled=%v",
			account.ID,
			1,
			truncateOpenAIWSLogValue(firstPayload.previousResponseID, openAIWSIDValueMaxLen),
			normalizeOpenAIWSLogValue(firstPreviousResponseIDKind),
			truncateOpenAIWSLogValue(preferredConnID, openAIWSIDValueMaxLen),
			truncateOpenAIWSLogValue(sessionHash, 12),
			openAIWSHeaderValueForLog(baseAcquireReq.Headers, "session_id"),
			openAIWSHeaderValueForLog(baseAcquireReq.Headers, "conversation_id"),
			turnState != "",
			len(turnState),
			firstPayload.promptCacheKey != "",
			storeDisabled,
		)
	}

	acquireTimeout := s.openAIWSAcquireTimeout()
	if acquireTimeout <= 0 {
		acquireTimeout = 30 * time.Second
	}

	ownerID := fmt.Sprintf("cliws_%p", clientConn)
	acquireTurnLease := func(
		turn int,
		preferred string,
		forcePreferredConn bool,
		hasPreviousResponseID bool,
	) (openAIWSIngressUpstreamLease, error) {
		acquireCtx, acquireCancel := context.WithTimeout(ctx, acquireTimeout)
		defer acquireCancel()

		var (
			lease      openAIWSIngressUpstreamLease
			acquireErr error
		)
		sessionHashForCtx := strings.TrimSpace(sessionHash)
		if sessionHashForCtx == "" {
			sessionHashForCtx = fmt.Sprintf("conn:%s", ownerID)
		}
		lease, acquireErr = ingressCtxPool.Acquire(acquireCtx, openAIWSIngressContextAcquireRequest{
			Account:               account,
			GroupID:               groupID,
			SessionHash:           sessionHashForCtx,
			OwnerID:               ownerID,
			WSURL:                 baseAcquireReq.WSURL,
			Headers:               cloneHeader(baseAcquireReq.Headers),
			ProxyURL:              baseAcquireReq.ProxyURL,
			Turn:                  turn,
			HasPreviousResponseID: hasPreviousResponseID,
			StrictAffinity:        forcePreferredConn,
			StoreDisabled:         storeDisabled,
		})
		if acquireErr != nil {
			dialStatus, dialClass, dialCloseStatus, dialCloseReason, dialRespServer, dialRespVia, dialRespCFRay, dialRespReqID := summarizeOpenAIWSDialError(acquireErr)
			logOpenAIWSModeInfo(
				"ingress_ws_upstream_acquire_fail account_id=%d turn=%d reason=%s dial_status=%d dial_class=%s dial_close_status=%s dial_close_reason=%s dial_resp_server=%s dial_resp_via=%s dial_resp_cf_ray=%s dial_resp_x_request_id=%s cause=%s preferred_conn_id=%s force_preferred_conn=%v ws_host=%s ws_path=%s proxy_enabled=%v",
				account.ID,
				turn,
				normalizeOpenAIWSLogValue(classifyOpenAIWSAcquireError(acquireErr)),
				dialStatus,
				dialClass,
				dialCloseStatus,
				truncateOpenAIWSLogValue(dialCloseReason, openAIWSHeaderValueMaxLen),
				dialRespServer,
				dialRespVia,
				dialRespCFRay,
				dialRespReqID,
				truncateOpenAIWSLogValue(acquireErr.Error(), openAIWSLogValueMaxLen),
				truncateOpenAIWSLogValue(preferred, openAIWSIDValueMaxLen),
				forcePreferredConn,
				wsHost,
				wsPath,
				account.ProxyID != nil && account.Proxy != nil,
			)
			if errors.Is(acquireErr, context.DeadlineExceeded) ||
				errors.Is(acquireErr, errOpenAIWSConnQueueFull) ||
				errors.Is(acquireErr, errOpenAIWSIngressContextBusy) {
				return nil, NewOpenAIWSClientCloseError(
					coderws.StatusTryAgainLater,
					"upstream websocket is busy, please retry later",
					acquireErr,
				)
			}
			return nil, acquireErr
		}
		connID := strings.TrimSpace(lease.ConnID())
		if handshakeTurnState := strings.TrimSpace(lease.HandshakeHeader(openAIWSTurnStateHeader)); handshakeTurnState != "" {
			turnState = handshakeTurnState
			if stateStore != nil && sessionHash != "" {
				stateStore.BindSessionTurnState(groupID, sessionHash, handshakeTurnState, s.openAIWSSessionStickyTTL())
			}
			updatedHeaders := cloneHeader(baseAcquireReq.Headers)
			if updatedHeaders == nil {
				updatedHeaders = make(http.Header)
			}
			updatedHeaders.Set(openAIWSTurnStateHeader, handshakeTurnState)
			baseAcquireReq.Headers = updatedHeaders
		}
		logOpenAIWSModeInfo(
			"ingress_ws_upstream_connected account_id=%d turn=%d conn_id=%s conn_reused=%v conn_pick_ms=%d queue_wait_ms=%d preferred_conn_id=%s ctx_pool_mode=%v schedule_layer=%s stickiness_level=%s migration_used=%v",
			account.ID,
			turn,
			truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
			lease.Reused(),
			lease.ConnPickDuration().Milliseconds(),
			lease.QueueWaitDuration().Milliseconds(),
			truncateOpenAIWSLogValue(preferred, openAIWSIDValueMaxLen),
			ctxPoolMode,
			normalizeOpenAIWSLogValue(lease.ScheduleLayer()),
			normalizeOpenAIWSLogValue(lease.StickinessLevel()),
			lease.MigrationUsed(),
		)
		return lease, nil
	}

	writeClientMessage := func(message []byte) error {
		writeCtx, cancel := context.WithTimeout(ctx, s.openAIWSWriteTimeout())
		defer cancel()
		return clientConn.Write(writeCtx, coderws.MessageText, message)
	}

	readClientMessage := func() ([]byte, error) {
		readCtx := ctx
		if idleTimeout := s.openAIWSClientReadIdleTimeout(); idleTimeout > 0 {
			var cancel context.CancelFunc
			readCtx, cancel = context.WithTimeout(ctx, idleTimeout)
			defer cancel()
		}
		msgType, payload, readErr := clientConn.Read(readCtx)
		if readErr != nil {
			if readCtx != nil && readCtx.Err() == context.DeadlineExceeded && (ctx == nil || ctx.Err() == nil) {
				return nil, NewOpenAIWSClientCloseError(
					coderws.StatusPolicyViolation,
					"client websocket idle timeout",
					readErr,
				)
			}
			return nil, readErr
		}
		if msgType != coderws.MessageText && msgType != coderws.MessageBinary {
			return nil, NewOpenAIWSClientCloseError(
				coderws.StatusPolicyViolation,
				fmt.Sprintf("unsupported websocket client message type: %s", msgType.String()),
				nil,
			)
		}
		return payload, nil
	}

	sendAndRelay := func(turn int, lease openAIWSIngressUpstreamLease, payload []byte, payloadBytes int, originalModel string, expectedPreviousResponseID string) (*OpenAIForwardResult, error) {
		if lease == nil {
			return nil, errors.New("upstream websocket lease is nil")
		}
		turnStart := time.Now()
		wroteDownstream := false
		if err := lease.WriteJSONWithContextTimeout(ctx, json.RawMessage(payload), s.openAIWSWriteTimeout()); err != nil {
			return nil, wrapOpenAIWSIngressTurnError(
				"write_upstream",
				fmt.Errorf("write upstream websocket request: %w", err),
				false,
			)
		}
		if debugEnabled {
			logOpenAIWSModeDebug(
				"ingress_ws_turn_request_sent account_id=%d turn=%d conn_id=%s payload_bytes=%d",
				account.ID,
				turn,
				truncateOpenAIWSLogValue(lease.ConnID(), openAIWSIDValueMaxLen),
				payloadBytes,
			)
		}

		responseID := ""
		usage := OpenAIUsage{}
		var firstTokenMs *int
		reqStream := openAIWSPayloadBoolFromRaw(payload, "stream", true)
		turnPreviousResponseID := openAIWSPayloadStringFromRaw(payload, "previous_response_id")
		turnPreviousResponseIDKind := ClassifyOpenAIPreviousResponseIDKind(turnPreviousResponseID)
		turnExpectedPreviousResponseID := strings.TrimSpace(expectedPreviousResponseID)
		turnPromptCacheKey := openAIWSPayloadStringFromRaw(payload, "prompt_cache_key")
		turnStoreDisabled := s.isOpenAIWSStoreDisabledInRequestRaw(payload, account)
		turnFunctionCallOutputCallIDs := openAIWSExtractFunctionCallOutputCallIDsFromPayload(payload)
		turnHasFunctionCallOutput := len(turnFunctionCallOutputCallIDs) > 0
		turnPendingFunctionCallIDSet := make(map[string]struct{}, 4)
		eventCount := 0
		tokenEventCount := 0
		terminalEventCount := 0
		firstEventType := ""
		lastEventType := ""
		needModelReplace := false
		clientDisconnected := false
		clientDisconnectDrainDeadline := time.Time{}
		terminateOnErrorEvent := false
		terminateOnErrorMessage := ""
		mappedModel := ""
		var mappedModelBytes []byte
		buildPartialResult := func(terminalEventType string) *OpenAIForwardResult {
			if usage.InputTokens <= 0 &&
				usage.OutputTokens <= 0 &&
				usage.CacheCreationInputTokens <= 0 &&
				usage.CacheReadInputTokens <= 0 {
				return nil
			}
			return &OpenAIForwardResult{
				RequestID:         responseID,
				Usage:             usage,
				Model:             originalModel,
				ReasoningEffort:   extractOpenAIReasoningEffortFromBody(payload, originalModel),
				Stream:            reqStream,
				OpenAIWSMode:      true,
				Duration:          time.Since(turnStart),
				FirstTokenMs:      firstTokenMs,
				TerminalEventType: strings.TrimSpace(terminalEventType),
			}
		}
		if originalModel != "" {
			mappedModel = account.GetMappedModel(originalModel)
			if normalizedModel := normalizeCodexModel(mappedModel); normalizedModel != "" {
				mappedModel = normalizedModel
			}
			needModelReplace = mappedModel != "" && mappedModel != originalModel
			if needModelReplace {
				mappedModelBytes = []byte(mappedModel)
			}
		}
		// 启动上游事件读取泵：解耦上游读取和客户端写入，允许二者并发执行。
		// 读取 goroutine 将上游事件推送到缓冲 channel，主 goroutine 从 channel 消费并处理/转发。
		// 缓冲 channel 允许上游在客户端写入阻塞时继续读取后续事件，降低端到端延迟。
		pumpEventCh := make(chan openAIWSUpstreamPumpEvent, openAIWSUpstreamPumpBufferSize)
		pumpCtx, pumpCancel := context.WithCancel(ctx)
		defer pumpCancel()
		go func() {
			defer close(pumpEventCh)
			for {
				msg, readErr := lease.ReadMessageWithContextTimeout(pumpCtx, s.openAIWSReadTimeout())
				select {
				case pumpEventCh <- openAIWSUpstreamPumpEvent{message: msg, err: readErr}:
				case <-pumpCtx.Done():
					return
				}
				if readErr != nil {
					return
				}
				// 检测终端/错误事件以终止读取泵。
				evtType, _ := parseOpenAIWSEventType(msg)
				if isOpenAIWSTerminalEvent(evtType) || evtType == "error" {
					return
				}
			}
		}()
		var drainTimer *time.Timer
		defer func() {
			if drainTimer != nil {
				drainTimer.Stop()
			}
		}()
		for evt := range pumpEventCh {
			// 排水超时检查：客户端已断连且排水截止时间已过，终止读取。
			if clientDisconnected && !clientDisconnectDrainDeadline.IsZero() && time.Now().After(clientDisconnectDrainDeadline) {
				pumpCancel()
				logOpenAIWSModeInfo(
					"ingress_ws_client_disconnected_drain_timeout account_id=%d turn=%d conn_id=%s timeout_ms=%d",
					account.ID,
					turn,
					truncateOpenAIWSLogValue(lease.ConnID(), openAIWSIDValueMaxLen),
					openAIWSIngressClientDisconnectDrainTimeout.Milliseconds(),
				)
				lease.MarkBroken()
				return nil, wrapOpenAIWSIngressTurnErrorWithPartial(
					"client_disconnected_drain_timeout",
					openAIWSIngressClientDisconnectedDrainTimeoutError(openAIWSIngressClientDisconnectDrainTimeout),
					wroteDownstream,
					buildPartialResult("client_disconnected_drain_timeout"),
				)
			}
			upstreamMessage := evt.message
			if evt.err != nil {
				readErr := evt.err
				if clientDisconnected {
					// 排水期间读取失败（上游关闭或读取泵被取消），按排水超时处理。
					logOpenAIWSModeInfo(
						"ingress_ws_client_disconnected_drain_timeout account_id=%d turn=%d conn_id=%s timeout_ms=%d",
						account.ID,
						turn,
						truncateOpenAIWSLogValue(lease.ConnID(), openAIWSIDValueMaxLen),
						openAIWSIngressClientDisconnectDrainTimeout.Milliseconds(),
					)
					lease.MarkBroken()
					return nil, wrapOpenAIWSIngressTurnErrorWithPartial(
						"client_disconnected_drain_timeout",
						openAIWSIngressClientDisconnectedDrainTimeoutError(openAIWSIngressClientDisconnectDrainTimeout),
						wroteDownstream,
						buildPartialResult("client_disconnected_drain_timeout"),
					)
				}
				readErrClass := "unknown"
				if errors.Is(readErr, context.Canceled) {
					readErrClass = "context_canceled"
				} else if errors.Is(readErr, context.DeadlineExceeded) {
					readErrClass = "deadline_exceeded"
				} else if isOpenAIWSClientDisconnectError(readErr) {
					readErrClass = "upstream_closed"
				} else if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
					readErrClass = "eof"
				}
				logOpenAIWSModeInfo(
					"ingress_ws_upstream_read_error account_id=%d turn=%d conn_id=%s class=%s events_received=%d wrote_downstream=%v response_id=%s cause=%s",
					account.ID,
					turn,
					truncateOpenAIWSLogValue(lease.ConnID(), openAIWSIDValueMaxLen),
					normalizeOpenAIWSLogValue(readErrClass),
					eventCount,
					wroteDownstream,
					truncateOpenAIWSLogValue(responseID, openAIWSIDValueMaxLen),
					truncateOpenAIWSLogValue(readErr.Error(), openAIWSLogValueMaxLen),
				)
				lease.MarkBroken()
				return nil, wrapOpenAIWSIngressTurnErrorWithPartial(
					"read_upstream",
					fmt.Errorf("read upstream websocket event: %w", readErr),
					wroteDownstream,
					buildPartialResult("read_upstream"),
				)
			}

			eventType, eventResponseID := parseOpenAIWSEventType(upstreamMessage)
			if responseID == "" && eventResponseID != "" {
				responseID = eventResponseID
			}
			if eventType != "" {
				eventCount++
				if firstEventType == "" {
					firstEventType = eventType
				}
				lastEventType = eventType
			}
			if eventType == "error" {
				errCodeRaw, errTypeRaw, errMsgRaw := parseOpenAIWSErrorEventFields(upstreamMessage)
				fallbackReason, _ := classifyOpenAIWSErrorEventFromRaw(errCodeRaw, errTypeRaw, errMsgRaw)
				errCode, errType, errMessage := summarizeOpenAIWSErrorEventFieldsFromRaw(errCodeRaw, errTypeRaw, errMsgRaw)
				recoveryEnabled := s.openAIWSIngressPreviousResponseRecoveryEnabled()
				recoverablePrevNotFound := fallbackReason == openAIWSIngressStagePreviousResponseNotFound &&
					recoveryEnabled &&
					(turnPreviousResponseID != "" || (turnHasFunctionCallOutput && turnExpectedPreviousResponseID != "")) &&
					!wroteDownstream
				// tool_output_not_found: previous_response_id 指向的 response 包含未完成的 function_call
				// （用户在 Codex CLI 按 ESC 取消后重新发送消息），需要移除 previous_response_id 后重放。
				recoverableToolOutputNotFound := fallbackReason == openAIWSIngressStageToolOutputNotFound &&
					recoveryEnabled &&
					turnPreviousResponseID != "" &&
					!wroteDownstream
				recoverableContextMismatch := recoverablePrevNotFound || recoverableToolOutputNotFound
				if recoverableContextMismatch {
					// 可恢复场景使用非 error 关键字日志，避免被 LegacyPrintf 误判为 ERROR 级别。
					logOpenAIWSModeInfo(
						"ingress_ws_prev_response_recoverable account_id=%d turn=%d conn_id=%s idx=%d reason=%s code=%s type=%s message=%s previous_response_id=%s previous_response_id_kind=%s response_id=%s ws_mode=%s ctx_pool_mode=%v store_disabled=%v has_prompt_cache_key=%v has_function_call_output=%v recovery_enabled=%v wrote_downstream=%v",
						account.ID,
						turn,
						truncateOpenAIWSLogValue(lease.ConnID(), openAIWSIDValueMaxLen),
						eventCount,
						truncateOpenAIWSLogValue(fallbackReason, openAIWSLogValueMaxLen),
						errCode,
						errType,
						errMessage,
						truncateOpenAIWSLogValue(turnPreviousResponseID, openAIWSIDValueMaxLen),
						normalizeOpenAIWSLogValue(turnPreviousResponseIDKind),
						truncateOpenAIWSLogValue(responseID, openAIWSIDValueMaxLen),
						normalizeOpenAIWSLogValue(ingressMode),
						ctxPoolMode,
						turnStoreDisabled,
						turnPromptCacheKey != "",
						turnHasFunctionCallOutput,
						recoveryEnabled,
						wroteDownstream,
					)
				} else {
					logOpenAIWSModeInfo(
						"ingress_ws_error_event account_id=%d turn=%d conn_id=%s idx=%d fallback_reason=%s err_code=%s err_type=%s err_message=%s previous_response_id=%s previous_response_id_kind=%s response_id=%s ws_mode=%s ctx_pool_mode=%v store_disabled=%v has_prompt_cache_key=%v has_function_call_output=%v recovery_enabled=%v wrote_downstream=%v",
						account.ID,
						turn,
						truncateOpenAIWSLogValue(lease.ConnID(), openAIWSIDValueMaxLen),
						eventCount,
						truncateOpenAIWSLogValue(fallbackReason, openAIWSLogValueMaxLen),
						errCode,
						errType,
						errMessage,
						truncateOpenAIWSLogValue(turnPreviousResponseID, openAIWSIDValueMaxLen),
						normalizeOpenAIWSLogValue(turnPreviousResponseIDKind),
						truncateOpenAIWSLogValue(responseID, openAIWSIDValueMaxLen),
						normalizeOpenAIWSLogValue(ingressMode),
						ctxPoolMode,
						turnStoreDisabled,
						turnPromptCacheKey != "",
						turnHasFunctionCallOutput,
						recoveryEnabled,
						wroteDownstream,
					)
				}
				// previous_response_not_found / tool_output_not_found 在 ingress 模式支持单次恢复重试：
				// 不把该 error 直接下发客户端，而是由上层去掉 previous_response_id 后重放当前 turn。
				if recoverableContextMismatch {
					lease.MarkBroken()
					errMsg := strings.TrimSpace(errMsgRaw)
					if errMsg == "" {
						if fallbackReason == openAIWSIngressStageToolOutputNotFound {
							errMsg = "no tool output found for function call"
						} else {
							errMsg = "previous response not found"
						}
					}
					return nil, wrapOpenAIWSIngressTurnErrorWithPartial(
						fallbackReason,
						errors.New(errMsg),
						false,
						buildPartialResult(fallbackReason),
					)
				}
				terminateOnErrorEvent = true
				terminateOnErrorMessage = strings.TrimSpace(errMsgRaw)
				if terminateOnErrorMessage == "" {
					terminateOnErrorMessage = "upstream websocket error"
				}
			}
			isTokenEvent := isOpenAIWSTokenEvent(eventType)
			if isTokenEvent {
				tokenEventCount++
			}
			isTerminalEvent := isOpenAIWSTerminalEvent(eventType)
			if isTerminalEvent {
				terminalEventCount++
			}
			if firstTokenMs == nil && isTokenEvent {
				ms := int(time.Since(turnStart).Milliseconds())
				firstTokenMs = &ms
			}
			if openAIWSEventShouldParseUsage(eventType) {
				parseOpenAIWSResponseUsageFromCompletedEvent(upstreamMessage, &usage)
			}
			if openAIWSEventMayContainToolCalls(eventType) && openAIWSMessageLikelyContainsToolCalls(upstreamMessage) {
				for _, callID := range openAIWSExtractPendingFunctionCallIDsFromEvent(upstreamMessage) {
					turnPendingFunctionCallIDSet[callID] = struct{}{}
				}
			}

			if !clientDisconnected {
				if needModelReplace && len(mappedModelBytes) > 0 && openAIWSEventMayContainModel(eventType) && bytes.Contains(upstreamMessage, mappedModelBytes) {
					upstreamMessage = replaceOpenAIWSMessageModel(upstreamMessage, mappedModel, originalModel)
				}
				if openAIWSEventMayContainToolCalls(eventType) && openAIWSMessageLikelyContainsToolCalls(upstreamMessage) {
					if corrected, changed := s.toolCorrector.CorrectToolCallsInSSEBytes(upstreamMessage); changed {
						upstreamMessage = corrected
					}
				}
				if err := writeClientMessage(upstreamMessage); err != nil {
					if isOpenAIWSClientDisconnectError(err) {
						clientDisconnected = true
						if clientDisconnectDrainDeadline.IsZero() {
							clientDisconnectDrainDeadline = time.Now().Add(openAIWSIngressClientDisconnectDrainTimeout)
							// 排水定时器到期后取消读取泵，确保不会无限等待上游。
							drainTimer = time.AfterFunc(openAIWSIngressClientDisconnectDrainTimeout, func() {
								lease.MarkBroken()
								pumpCancel()
							})
						}
						closeStatus, closeReason := summarizeOpenAIWSReadCloseError(err)
						logOpenAIWSModeInfo(
							"ingress_ws_client_disconnected_drain account_id=%d turn=%d conn_id=%s close_status=%s close_reason=%s drain_timeout_ms=%d",
							account.ID,
							turn,
							truncateOpenAIWSLogValue(lease.ConnID(), openAIWSIDValueMaxLen),
							closeStatus,
							truncateOpenAIWSLogValue(closeReason, openAIWSHeaderValueMaxLen),
							openAIWSIngressClientDisconnectDrainTimeout.Milliseconds(),
						)
					} else {
						return nil, wrapOpenAIWSIngressTurnErrorWithPartial(
							"write_client",
							fmt.Errorf("write client websocket event: %w", err),
							wroteDownstream,
							buildPartialResult("write_client"),
						)
					}
				} else {
					wroteDownstream = true
				}
			}
			if terminateOnErrorEvent {
				// WS ingress 中的 error 事件应立即终止当前 turn，避免继续阻塞在下一次上游 read。
				lease.MarkBroken()
				return nil, wrapOpenAIWSIngressTurnErrorWithPartial(
					"upstream_error_event",
					errors.New(terminateOnErrorMessage),
					wroteDownstream,
					buildPartialResult("upstream_error_event"),
				)
			}
			if isTerminalEvent {
				// 客户端已断连时，上游连接的 session 状态不可信，标记 broken 避免回池复用。
				if clientDisconnected {
					lease.MarkBroken()
				}
				firstTokenMsValue := -1
				if firstTokenMs != nil {
					firstTokenMsValue = *firstTokenMs
				}
				if debugEnabled {
					logOpenAIWSModeDebug(
						"ingress_ws_turn_completed account_id=%d turn=%d conn_id=%s response_id=%s duration_ms=%d events=%d token_events=%d terminal_events=%d first_event=%s last_event=%s first_token_ms=%d client_disconnected=%v has_function_call_output=%v pending_function_call_ids=%d",
						account.ID,
						turn,
						truncateOpenAIWSLogValue(lease.ConnID(), openAIWSIDValueMaxLen),
						truncateOpenAIWSLogValue(responseID, openAIWSIDValueMaxLen),
						time.Since(turnStart).Milliseconds(),
						eventCount,
						tokenEventCount,
						terminalEventCount,
						truncateOpenAIWSLogValue(firstEventType, openAIWSLogValueMaxLen),
						truncateOpenAIWSLogValue(lastEventType, openAIWSLogValueMaxLen),
						firstTokenMsValue,
						clientDisconnected,
						turnHasFunctionCallOutput,
						len(turnPendingFunctionCallIDSet),
					)
				}
				pendingFunctionCallIDs := make([]string, 0, len(turnPendingFunctionCallIDSet))
				for callID := range turnPendingFunctionCallIDSet {
					pendingFunctionCallIDs = append(pendingFunctionCallIDs, callID)
				}
				sort.Strings(pendingFunctionCallIDs)
				return &OpenAIForwardResult{
					RequestID:              responseID,
					Usage:                  usage,
					Model:                  originalModel,
					ReasoningEffort:        extractOpenAIReasoningEffortFromBody(payload, originalModel),
					Stream:                 reqStream,
					OpenAIWSMode:           true,
					Duration:               time.Since(turnStart),
					FirstTokenMs:           firstTokenMs,
					TerminalEventType:      strings.TrimSpace(eventType),
					PendingFunctionCallIDs: pendingFunctionCallIDs,
				}, nil
			}
		}
		// 读取泵 channel 关闭但未收到终端事件：
		// - 客户端已断连：按排水超时收尾，避免误判为 read_upstream。
		// - 其他场景：按上游读取异常处理。
		lease.MarkBroken()
		if clientDisconnected {
			return nil, openAIWSIngressPumpClosedTurnError(
				true,
				wroteDownstream,
				buildPartialResult("client_disconnected_drain_timeout"),
			)
		}
		return nil, openAIWSIngressPumpClosedTurnError(
			false,
			wroteDownstream,
			buildPartialResult("read_upstream"),
		)
	}

	currentPayload := firstPayload.payloadRaw
	currentOriginalModel := firstPayload.originalModel
	currentPayloadBytes := firstPayload.payloadBytes
	isStrictAffinityTurn := func(payload []byte) bool {
		if !storeDisabled {
			return false
		}
		return strings.TrimSpace(openAIWSPayloadStringFromRaw(payload, "previous_response_id")) != ""
	}
	var sessionLease openAIWSIngressUpstreamLease
	sessionConnID := ""
	unpinSessionConn := func(_ string) {}
	pinSessionConn := func(_ string) {}
	releaseSessionLease := func() {
		if sessionLease == nil {
			return
		}
		unpinSessionConn(sessionConnID)
		sessionLease.Release()
		if debugEnabled {
			logOpenAIWSModeDebug(
				"ingress_ws_upstream_released account_id=%d conn_id=%s",
				account.ID,
				truncateOpenAIWSLogValue(sessionConnID, openAIWSIDValueMaxLen),
			)
		}
	}
	yieldSessionLease := func() {
		if sessionLease == nil {
			return
		}
		unpinSessionConn(sessionConnID)
		sessionLease.Yield()
		if debugEnabled {
			logOpenAIWSModeDebug(
				"ingress_ws_upstream_yielded account_id=%d conn_id=%s",
				account.ID,
				truncateOpenAIWSLogValue(sessionConnID, openAIWSIDValueMaxLen),
			)
		}
		sessionLease = nil
		sessionConnID = ""
	}
	defer releaseSessionLease()

	turn := 1
	turnRetry := 0
	turnPrevRecoveryTried := false
	turnPreferredRehydrateTried := false
	strictAffinityBypassOnce := false
	lastTurnFinishedAt := time.Time{}
	lastTurnResponseID := sessionLastResponseID
	clearSessionLastResponseID := func() {
		lastTurnResponseID = ""
		if stateStore == nil || sessionHash == "" {
			return
		}
		stateStore.DeleteSessionLastResponseID(groupID, sessionHash)
		if ctxPoolMode && legacySessionHash != "" && legacySessionHash != sessionHash {
			stateStore.DeleteSessionLastResponseID(groupID, legacySessionHash)
		}
	}
	lastTurnPayload := []byte(nil)
	var lastTurnStrictState *openAIWSIngressPreviousTurnStrictState
	lastTurnReplayInput := []json.RawMessage(nil)
	lastTurnReplayInputExists := false
	currentTurnReplayInput := []json.RawMessage(nil)
	currentTurnReplayInputExists := false
	skipBeforeTurn := false
	resetSessionLease := func(markBroken bool) {
		if sessionLease == nil {
			return
		}
		if markBroken {
			sessionLease.MarkBroken()
		}
		releaseSessionLease()
		sessionLease = nil
		sessionConnID = ""
		preferredConnID = ""
	}
	recoverIngressPrevResponseNotFound := func(relayErr error, turn int, connID string) bool {
		isPrevNotFound := isOpenAIWSIngressPreviousResponseNotFound(relayErr)
		isToolOutputMissing := isOpenAIWSIngressToolOutputNotFound(relayErr)
		if !isPrevNotFound && !isToolOutputMissing {
			return false
		}
		if turnPrevRecoveryTried || !s.openAIWSIngressPreviousResponseRecoveryEnabled() {
			skipReason := "already_tried"
			if !s.openAIWSIngressPreviousResponseRecoveryEnabled() {
				skipReason = "recovery_disabled"
			}
			logOpenAIWSModeInfo(
				"ingress_ws_prev_response_recovery_skipped account_id=%d turn=%d conn_id=%s reason=%s is_prev_not_found=%v is_tool_output_missing=%v",
				account.ID,
				turn,
				truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
				normalizeOpenAIWSLogValue(skipReason),
				isPrevNotFound,
				isToolOutputMissing,
			)
			return false
		}
		currentPreviousResponseID := strings.TrimSpace(openAIWSPayloadStringFromRaw(currentPayload, "previous_response_id"))
		// tool_output_not_found: previous_response_id 指向的 response 包含未完成的 function_call
		// （用户在 Codex CLI 按 ESC 取消了 function_call 后重新发送消息）。
		// 对齐/保持 previous_response_id 无法解决问题，直接跳到 drop 分支移除后重放。
		if isToolOutputMissing {
			logOpenAIWSModeInfo(
				"ingress_ws_tool_output_not_found_recovery account_id=%d turn=%d conn_id=%s action=drop_previous_response_id_retry previous_response_id=%s",
				account.ID,
				turn,
				truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
				truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
			)
			turnPrevRecoveryTried = true
			updatedPayload, removed, dropErr := dropPreviousResponseIDFromRawPayload(currentPayload)
			if dropErr != nil || !removed {
				reason := "not_removed"
				if dropErr != nil {
					reason = "drop_error"
				}
				logOpenAIWSModeInfo(
					"ingress_ws_tool_output_not_found_recovery_skip account_id=%d turn=%d conn_id=%s reason=%s",
					account.ID,
					turn,
					truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
					normalizeOpenAIWSLogValue(reason),
				)
				return false
			}
			updatedWithInput, setInputErr := setOpenAIWSPayloadInputSequence(
				updatedPayload,
				currentTurnReplayInput,
				currentTurnReplayInputExists,
			)
			if setInputErr != nil {
				logOpenAIWSModeInfo(
					"ingress_ws_tool_output_not_found_recovery_skip account_id=%d turn=%d conn_id=%s reason=set_full_input_error cause=%s",
					account.ID,
					turn,
					truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
					truncateOpenAIWSLogValue(setInputErr.Error(), openAIWSLogValueMaxLen),
				)
				return false
			}
			currentPayload = updatedWithInput
			currentPayloadBytes = len(updatedWithInput)
			clearSessionLastResponseID()
			resetSessionLease(true)
			skipBeforeTurn = true
			return true
		}
		hasFunctionCallOutput := gjson.GetBytes(currentPayload, `input.#(type=="function_call_output")`).Exists()
		if hasFunctionCallOutput {
			turnPrevRecoveryTried = true
			expectedPrev := strings.TrimSpace(lastTurnResponseID)
			if currentPreviousResponseID == "" && expectedPrev != "" {
				updatedPayload, setPrevErr := setPreviousResponseIDToRawPayload(currentPayload, expectedPrev)
				if setPrevErr != nil {
					logOpenAIWSModeInfo(
						"ingress_ws_prev_response_recovery_skip account_id=%d turn=%d conn_id=%s reason=set_previous_response_id_error cause=%s expected_previous_response_id=%s",
						account.ID,
						turn,
						truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
						truncateOpenAIWSLogValue(setPrevErr.Error(), openAIWSLogValueMaxLen),
						truncateOpenAIWSLogValue(expectedPrev, openAIWSIDValueMaxLen),
					)
				} else {
					updatedWithInput, setInputErr := setOpenAIWSPayloadInputSequence(
						updatedPayload,
						currentTurnReplayInput,
						currentTurnReplayInputExists,
					)
					if setInputErr != nil {
						logOpenAIWSModeInfo(
							"ingress_ws_prev_response_recovery_skip account_id=%d turn=%d conn_id=%s reason=set_full_input_error cause=%s previous_response_id=%s expected_previous_response_id=%s",
							account.ID,
							turn,
							truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
							truncateOpenAIWSLogValue(setInputErr.Error(), openAIWSLogValueMaxLen),
							truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
							truncateOpenAIWSLogValue(expectedPrev, openAIWSIDValueMaxLen),
						)
					} else {
						logOpenAIWSModeInfo(
							"ingress_ws_prev_response_recovery account_id=%d turn=%d conn_id=%s action=set_previous_response_id_retry previous_response_id=%s expected_previous_response_id=%s",
							account.ID,
							turn,
							truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
							truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
							truncateOpenAIWSLogValue(expectedPrev, openAIWSIDValueMaxLen),
						)
						currentPayload = updatedWithInput
						currentPayloadBytes = len(updatedWithInput)
						resetSessionLease(true)
						strictAffinityBypassOnce = true
						skipBeforeTurn = true
						return true
					}
				}
			}
			alignedPayload, aligned, alignErr := alignStoreDisabledPreviousResponseID(currentPayload, expectedPrev)
			if alignErr != nil {
				logOpenAIWSModeInfo(
					"ingress_ws_prev_response_recovery_skip account_id=%d turn=%d conn_id=%s reason=align_previous_response_id_error cause=%s previous_response_id=%s expected_previous_response_id=%s",
					account.ID,
					turn,
					truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
					truncateOpenAIWSLogValue(alignErr.Error(), openAIWSLogValueMaxLen),
					truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
					truncateOpenAIWSLogValue(expectedPrev, openAIWSIDValueMaxLen),
				)
			} else if aligned {
				updatedWithInput, setInputErr := setOpenAIWSPayloadInputSequence(
					alignedPayload,
					currentTurnReplayInput,
					currentTurnReplayInputExists,
				)
				if setInputErr != nil {
					logOpenAIWSModeInfo(
						"ingress_ws_prev_response_recovery_skip account_id=%d turn=%d conn_id=%s reason=set_full_input_error cause=%s previous_response_id=%s expected_previous_response_id=%s",
						account.ID,
						turn,
						truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
						truncateOpenAIWSLogValue(setInputErr.Error(), openAIWSLogValueMaxLen),
						truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
						truncateOpenAIWSLogValue(expectedPrev, openAIWSIDValueMaxLen),
					)
				} else {
					logOpenAIWSModeInfo(
						"ingress_ws_prev_response_recovery account_id=%d turn=%d conn_id=%s action=align_previous_response_id_retry previous_response_id=%s expected_previous_response_id=%s",
						account.ID,
						turn,
						truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
						truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
						truncateOpenAIWSLogValue(expectedPrev, openAIWSIDValueMaxLen),
					)
					currentPayload = updatedWithInput
					currentPayloadBytes = len(updatedWithInput)
					resetSessionLease(true)
					strictAffinityBypassOnce = true
					skipBeforeTurn = true
					return true
				}
			}
			// function_call_output 与 previous_response_id 语义绑定：
			// function_call_output 引用了前一个 response 中的 call_id，
			// 移除 previous_response_id 但保留 function_call_output 会导致上游报错
			// "No tool call found for function call output with call_id ..."。
			// 此场景在网关层不可恢复，返回 false 走 abort 路径通知客户端，
			// 客户端收到错误后会重置并发送完整请求（不带 previous_response_id）。
			logOpenAIWSModeInfo(
				"ingress_ws_prev_response_recovery account_id=%d turn=%d conn_id=%s action=abort_function_call_unrecoverable previous_response_id=%s",
				account.ID,
				turn,
				truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
				truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
			)
			return false
		}
		if isStrictAffinityTurn(currentPayload) {
			// Layer 2：严格亲和链路命中 previous_response_not_found 时，降级为“去掉 previous_response_id 后重放一次”。
			// 该错误说明续链锚点已失效，继续 strict fail-close 只会直接中断本轮请求。
			logOpenAIWSModeInfo(
				"ingress_ws_prev_response_recovery_layer2 account_id=%d turn=%d conn_id=%s store_disabled_conn_mode=%s action=drop_previous_response_id_retry",
				account.ID,
				turn,
				truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
				normalizeOpenAIWSLogValue(storeDisabledConnMode),
			)
		}
		turnPrevRecoveryTried = true
		updatedPayload, removed, dropErr := dropPreviousResponseIDFromRawPayload(currentPayload)
		if dropErr != nil || !removed {
			reason := "not_removed"
			if dropErr != nil {
				reason = "drop_error"
			}
			logOpenAIWSModeInfo(
				"ingress_ws_prev_response_recovery_skip account_id=%d turn=%d conn_id=%s reason=%s",
				account.ID,
				turn,
				truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
				normalizeOpenAIWSLogValue(reason),
			)
			return false
		}
		updatedWithInput, setInputErr := setOpenAIWSPayloadInputSequence(
			updatedPayload,
			currentTurnReplayInput,
			currentTurnReplayInputExists,
		)
		if setInputErr != nil {
			logOpenAIWSModeInfo(
				"ingress_ws_prev_response_recovery_skip account_id=%d turn=%d conn_id=%s reason=set_full_input_error cause=%s",
				account.ID,
				turn,
				truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
				truncateOpenAIWSLogValue(setInputErr.Error(), openAIWSLogValueMaxLen),
			)
			return false
		}
		logOpenAIWSModeInfo(
			"ingress_ws_prev_response_recovery account_id=%d turn=%d conn_id=%s action=drop_previous_response_id retry=1",
			account.ID,
			turn,
			truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
		)
		currentPayload = updatedWithInput
		currentPayloadBytes = len(updatedWithInput)
		clearSessionLastResponseID()
		resetSessionLease(true)
		skipBeforeTurn = true
		return true
	}
	retryIngressTurn := func(relayErr error, turn int, connID string) bool {
		if !isOpenAIWSIngressTurnRetryable(relayErr) || turnRetry >= 1 {
			retrySkipReason := "not_retryable"
			if turnRetry >= 1 {
				retrySkipReason = "retry_exhausted"
			}
			logOpenAIWSModeInfo(
				"ingress_ws_turn_retry_skipped account_id=%d turn=%d conn_id=%s reason=%s retry_count=%d err_stage=%s",
				account.ID,
				turn,
				truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
				normalizeOpenAIWSLogValue(retrySkipReason),
				turnRetry,
				truncateOpenAIWSLogValue(openAIWSIngressTurnRetryReason(relayErr), openAIWSLogValueMaxLen),
			)
			return false
		}
		if isStrictAffinityTurn(currentPayload) {
			logOpenAIWSModeInfo(
				"ingress_ws_turn_retry_skip account_id=%d turn=%d conn_id=%s reason=strict_affinity",
				account.ID,
				turn,
				truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
			)
			return false
		}
		turnRetry++
		logOpenAIWSModeInfo(
			"ingress_ws_turn_retry account_id=%d turn=%d retry=%d reason=%s conn_id=%s",
			account.ID,
			turn,
			turnRetry,
			truncateOpenAIWSLogValue(openAIWSIngressTurnRetryReason(relayErr), openAIWSLogValueMaxLen),
			truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
		)
		resetSessionLease(true)
		skipBeforeTurn = true
		return true
	}
	advanceToNextClientTurn := func(turn int, connID string) (bool, error) {
		logOpenAIWSModeInfo(
			"ingress_ws_advance_wait_client account_id=%d turn=%d conn_id=%s",
			account.ID,
			turn,
			truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
		)
		nextClientMessage, readErr := readClientMessage()
		if readErr != nil {
			if isOpenAIWSClientDisconnectError(readErr) {
				closeStatus, closeReason := summarizeOpenAIWSReadCloseError(readErr)
				logOpenAIWSModeInfo(
					"ingress_ws_client_closed account_id=%d conn_id=%s close_status=%s close_reason=%s",
					account.ID,
					truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
					closeStatus,
					truncateOpenAIWSLogValue(closeReason, openAIWSHeaderValueMaxLen),
				)
				return true, nil
			}
			logOpenAIWSModeInfo(
				"ingress_ws_advance_read_fail account_id=%d turn=%d conn_id=%s cause=%s",
				account.ID,
				turn,
				truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
				truncateOpenAIWSLogValue(readErr.Error(), openAIWSLogValueMaxLen),
			)
			return false, fmt.Errorf("read client websocket request: %w", readErr)
		}

		nextPayload, parseErr := parseClientPayload(nextClientMessage)
		if parseErr != nil {
			return false, parseErr
		}
		nextStoreDisabled := s.isOpenAIWSStoreDisabledInRequestRaw(nextPayload.payloadRaw, account)
		nextLegacySessionHash := strings.TrimSpace(s.GenerateSessionHashWithFallback(c, nextPayload.rawForHash, fallbackSessionSeed))
		nextSessionHash := nextLegacySessionHash
		if ctxPoolMode {
			nextSessionHash = openAIWSApplySessionScope(nextLegacySessionHash, ctxPoolSessionScope)
		}
		if sessionHash == "" && nextSessionHash != "" {
			sessionHash = nextSessionHash
			legacySessionHash = nextLegacySessionHash
			if stateStore != nil {
				if turnState == "" {
					if savedTurnState, ok := resolveSessionTurnState(); ok {
						turnState = savedTurnState
					}
				}
				if lastTurnResponseID == "" {
					if savedResponseID, ok := resolveSessionLastResponseID(); ok {
						lastTurnResponseID = savedResponseID
					}
				}
			}
			logOpenAIWSModeInfo(
				"ingress_ws_session_hash_backfill account_id=%d turn=%d next_turn=%d conn_id=%s session_hash=%s has_turn_state=%v has_last_response_id=%v store_disabled=%v",
				account.ID,
				turn,
				turn+1,
				truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
				truncateOpenAIWSLogValue(sessionHash, 12),
				turnState != "",
				strings.TrimSpace(lastTurnResponseID) != "",
				nextStoreDisabled,
			)
		}
		if nextPayload.promptCacheKey != "" {
			// ingress 会话在整个客户端 WS 生命周期内复用同一上游连接；
			// prompt_cache_key 对握手头的更新仅在未来需要重新建连时生效。
			updatedHeaders, _ := s.buildOpenAIWSHeaders(c, account, token, wsDecision, isCodexCLI, turnState, strings.TrimSpace(c.GetHeader(openAIWSTurnMetadataHeader)), nextPayload.promptCacheKey)
			baseAcquireReq.Headers = updatedHeaders
		}
		if nextPayload.previousResponseID != "" {
			expectedPrev := strings.TrimSpace(lastTurnResponseID)
			chainedFromLast := expectedPrev != "" && nextPayload.previousResponseID == expectedPrev
			nextPreviousResponseIDKind := ClassifyOpenAIPreviousResponseIDKind(nextPayload.previousResponseID)
			logOpenAIWSModeInfo(
				"ingress_ws_next_turn_chain account_id=%d turn=%d next_turn=%d conn_id=%s previous_response_id=%s previous_response_id_kind=%s last_turn_response_id=%s chained_from_last=%v has_prompt_cache_key=%v store_disabled=%v",
				account.ID,
				turn,
				turn+1,
				truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
				truncateOpenAIWSLogValue(nextPayload.previousResponseID, openAIWSIDValueMaxLen),
				normalizeOpenAIWSLogValue(nextPreviousResponseIDKind),
				truncateOpenAIWSLogValue(expectedPrev, openAIWSIDValueMaxLen),
				chainedFromLast,
				nextPayload.promptCacheKey != "",
				storeDisabled,
			)
		}
		if stateStore != nil && nextPayload.previousResponseID != "" {
			if stickyConnID := openAIWSPreferredConnIDFromResponse(stateStore, nextPayload.previousResponseID); stickyConnID != "" {
				if sessionConnID != "" && stickyConnID != "" && stickyConnID != sessionConnID {
					logOpenAIWSModeInfo(
						"ingress_ws_keep_session_conn account_id=%d turn=%d conn_id=%s sticky_conn_id=%s previous_response_id=%s",
						account.ID,
						turn,
						truncateOpenAIWSLogValue(sessionConnID, openAIWSIDValueMaxLen),
						truncateOpenAIWSLogValue(stickyConnID, openAIWSIDValueMaxLen),
						truncateOpenAIWSLogValue(nextPayload.previousResponseID, openAIWSIDValueMaxLen),
					)
				} else {
					preferredConnID = stickyConnID
				}
			}
		}
		currentPayload = nextPayload.payloadRaw
		currentOriginalModel = nextPayload.originalModel
		currentPayloadBytes = nextPayload.payloadBytes
		storeDisabled = nextStoreDisabled
		if !storeDisabled {
			unpinSessionConn(sessionConnID)
		}
		return false, nil
	}
	for {
		if !skipBeforeTurn && hooks != nil && hooks.BeforeTurn != nil {
			if err := hooks.BeforeTurn(turn); err != nil {
				return err
			}
		}
		skipBeforeTurn = false
		currentPreviousResponseID := openAIWSPayloadStringFromRaw(currentPayload, "previous_response_id")
		expectedPrev := strings.TrimSpace(lastTurnResponseID)
		if expectedPrev == "" && stateStore != nil && sessionHash != "" {
			if savedResponseID, ok := resolveSessionLastResponseID(); ok {
				expectedPrev = savedResponseID
				if expectedPrev != "" {
					lastTurnResponseID = expectedPrev
				}
			}
		}
		logOpenAIWSModeInfo(
			"ingress_ws_turn_begin account_id=%d turn=%d conn_id=%s previous_response_id=%s expected_previous_response_id=%s store_disabled=%v has_session_lease=%v",
			account.ID,
			turn,
			truncateOpenAIWSLogValue(sessionConnID, openAIWSIDValueMaxLen),
			truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
			truncateOpenAIWSLogValue(expectedPrev, openAIWSIDValueMaxLen),
			storeDisabled,
			sessionLease != nil,
		)
		pendingExpectedCallIDs := []string(nil)
		if storeDisabled && expectedPrev != "" && stateStore != nil {
			if pendingCallIDs, ok := stateStore.GetResponsePendingToolCalls(groupID, expectedPrev); ok {
				pendingExpectedCallIDs = openAIWSNormalizeCallIDs(pendingCallIDs)
			}
		}
		normalized := normalizeOpenAIWSIngressPayloadBeforeSend(openAIWSIngressPreSendNormalizeInput{
			accountID:                 account.ID,
			turn:                      turn,
			connID:                    sessionConnID,
			currentPayload:            currentPayload,
			currentPayloadBytes:       currentPayloadBytes,
			currentPreviousResponseID: currentPreviousResponseID,
			expectedPreviousResponse:  expectedPrev,
			pendingExpectedCallIDs:    pendingExpectedCallIDs,
		})
		currentPayload = normalized.currentPayload
		currentPayloadBytes = normalized.currentPayloadBytes
		currentPreviousResponseID = normalized.currentPreviousResponseID
		expectedPrev = normalized.expectedPreviousResponseID
		pendingExpectedCallIDs = normalized.pendingExpectedCallIDs
		currentFunctionCallOutputCallIDs := normalized.functionCallOutputCallIDs
		hasFunctionCallOutput := normalized.hasFunctionCallOutputCallID
		nextReplayInput, nextReplayInputExists, replayInputErr := buildOpenAIWSReplayInputSequence(
			lastTurnReplayInput,
			lastTurnReplayInputExists,
			currentPayload,
			currentPreviousResponseID != "",
		)
		if replayInputErr != nil {
			logOpenAIWSModeInfo(
				"ingress_ws_replay_input_skip account_id=%d turn=%d conn_id=%s reason=build_error cause=%s",
				account.ID,
				turn,
				truncateOpenAIWSLogValue(sessionConnID, openAIWSIDValueMaxLen),
				truncateOpenAIWSLogValue(replayInputErr.Error(), openAIWSLogValueMaxLen),
			)
			currentTurnReplayInput = nil
			currentTurnReplayInputExists = false
		} else {
			currentTurnReplayInput = nextReplayInput
			currentTurnReplayInputExists = nextReplayInputExists
		}
		if storeDisabled && turn > 1 && currentPreviousResponseID != "" {
			shouldKeepPreviousResponseID := false
			strictReason := ""
			var strictErr error
			if lastTurnStrictState != nil {
				shouldKeepPreviousResponseID, strictReason, strictErr = shouldKeepIngressPreviousResponseIDWithStrictState(
					lastTurnStrictState,
					currentPayload,
					lastTurnResponseID,
					hasFunctionCallOutput,
					pendingExpectedCallIDs,
					currentFunctionCallOutputCallIDs,
				)
			} else {
				shouldKeepPreviousResponseID, strictReason, strictErr = shouldKeepIngressPreviousResponseID(
					lastTurnPayload,
					currentPayload,
					lastTurnResponseID,
					hasFunctionCallOutput,
					pendingExpectedCallIDs,
					currentFunctionCallOutputCallIDs,
				)
			}
			if strictErr != nil {
				logOpenAIWSModeInfo(
					"ingress_ws_prev_response_strict_eval account_id=%d turn=%d conn_id=%s action=keep_previous_response_id reason=%s cause=%s previous_response_id=%s expected_previous_response_id=%s has_function_call_output=%v",
					account.ID,
					turn,
					truncateOpenAIWSLogValue(sessionConnID, openAIWSIDValueMaxLen),
					normalizeOpenAIWSLogValue(strictReason),
					truncateOpenAIWSLogValue(strictErr.Error(), openAIWSLogValueMaxLen),
					truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
					truncateOpenAIWSLogValue(expectedPrev, openAIWSIDValueMaxLen),
					hasFunctionCallOutput,
				)
			} else if !shouldKeepPreviousResponseID {
				updatedPayload, removed, dropErr := dropPreviousResponseIDFromRawPayload(currentPayload)
				if dropErr != nil || !removed {
					dropReason := "not_removed"
					if dropErr != nil {
						dropReason = "drop_error"
					}
					logOpenAIWSModeInfo(
						"ingress_ws_prev_response_strict_eval account_id=%d turn=%d conn_id=%s action=keep_previous_response_id reason=%s drop_reason=%s previous_response_id=%s expected_previous_response_id=%s has_function_call_output=%v",
						account.ID,
						turn,
						truncateOpenAIWSLogValue(sessionConnID, openAIWSIDValueMaxLen),
						normalizeOpenAIWSLogValue(strictReason),
						normalizeOpenAIWSLogValue(dropReason),
						truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
						truncateOpenAIWSLogValue(expectedPrev, openAIWSIDValueMaxLen),
						hasFunctionCallOutput,
					)
				} else {
					updatedWithInput, setInputErr := setOpenAIWSPayloadInputSequence(
						updatedPayload,
						currentTurnReplayInput,
						currentTurnReplayInputExists,
					)
					if setInputErr != nil {
						logOpenAIWSModeInfo(
							"ingress_ws_prev_response_strict_eval account_id=%d turn=%d conn_id=%s action=keep_previous_response_id reason=%s drop_reason=set_full_input_error previous_response_id=%s expected_previous_response_id=%s cause=%s has_function_call_output=%v",
							account.ID,
							turn,
							truncateOpenAIWSLogValue(sessionConnID, openAIWSIDValueMaxLen),
							normalizeOpenAIWSLogValue(strictReason),
							truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
							truncateOpenAIWSLogValue(expectedPrev, openAIWSIDValueMaxLen),
							truncateOpenAIWSLogValue(setInputErr.Error(), openAIWSLogValueMaxLen),
							hasFunctionCallOutput,
						)
					} else {
						currentPayload = updatedWithInput
						currentPayloadBytes = len(updatedWithInput)
						logOpenAIWSModeInfo(
							"ingress_ws_prev_response_strict_eval account_id=%d turn=%d conn_id=%s action=drop_previous_response_id_full_create reason=%s previous_response_id=%s expected_previous_response_id=%s has_function_call_output=%v",
							account.ID,
							turn,
							truncateOpenAIWSLogValue(sessionConnID, openAIWSIDValueMaxLen),
							normalizeOpenAIWSLogValue(strictReason),
							truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
							truncateOpenAIWSLogValue(expectedPrev, openAIWSIDValueMaxLen),
							hasFunctionCallOutput,
						)
						currentPreviousResponseID = ""
					}
				}
			}
		}
		forcePreferredConn := isStrictAffinityTurn(currentPayload)
		hasPreviousResponseIDForAcquire := currentPreviousResponseID != ""
		if strictAffinityBypassOnce {
			forcePreferredConn = false
			// strict bypass 仅影响连接调度层：允许迁移/复用候选；
			// payload 中 previous_response_id 仍按原语义保留。
			hasPreviousResponseIDForAcquire = false
			strictAffinityBypassOnce = false
		}
		if sessionLease == nil {
			acquiredLease, acquireErr := acquireTurnLease(
				turn,
				preferredConnID,
				forcePreferredConn,
				hasPreviousResponseIDForAcquire,
			)
			if acquireErr != nil {
				if forcePreferredConn &&
					ctxPoolMode &&
					currentPreviousResponseID != "" &&
					hasFunctionCallOutput &&
					(errors.Is(acquireErr, errOpenAIWSConnQueueFull) ||
						errors.Is(acquireErr, errOpenAIWSIngressContextBusy) ||
						errors.Is(acquireErr, context.DeadlineExceeded)) {
					logOpenAIWSModeInfo(
						"ingress_ws_preferred_conn_recovery account_id=%d turn=%d action=retry_without_strict_affinity_keep_previous_response_id reason=ctx_pool_busy previous_response_id=%s",
						account.ID,
						turn,
						truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
					)
					strictAffinityBypassOnce = true
					skipBeforeTurn = true
					continue
				}
				if forcePreferredConn && currentPreviousResponseID != "" && isOpenAIWSContinuationUnavailableCloseError(acquireErr) {
					if !turnPreferredRehydrateTried && stateStore != nil && strings.TrimSpace(preferredConnID) == "" {
						if stickyConnID := openAIWSPreferredConnIDFromResponse(stateStore, currentPreviousResponseID); stickyConnID != "" {
							preferredConnID = stickyConnID
							turnPreferredRehydrateTried = true
							if preferredConnID != "" {
								logOpenAIWSModeInfo(
									"ingress_ws_preferred_conn_rehydrate account_id=%d turn=%d action=retry_with_sticky_conn previous_response_id=%s preferred_conn_id=%s",
									account.ID,
									turn,
									truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
									truncateOpenAIWSLogValue(preferredConnID, openAIWSIDValueMaxLen),
								)
								skipBeforeTurn = true
								continue
							}
						}
					}
					if !turnPrevRecoveryTried && hasFunctionCallOutput {
						// function_call_output 续链请求优先保留 previous_response_id，
						// 仅单次放宽 strict affinity，避免提前降级为 full create。
						logOpenAIWSModeInfo(
							"ingress_ws_preferred_conn_recovery account_id=%d turn=%d action=retry_without_strict_affinity_keep_previous_response_id previous_response_id=%s",
							account.ID,
							turn,
							truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
						)
						strictAffinityBypassOnce = true
						skipBeforeTurn = true
						continue
					}
					if !turnPrevRecoveryTried {
						updatedPayload, removed, dropErr := dropPreviousResponseIDFromRawPayload(currentPayload)
						if dropErr != nil || !removed {
							reason := "not_removed"
							if dropErr != nil {
								reason = "drop_error"
							}
							logOpenAIWSModeInfo(
								"ingress_ws_preferred_conn_recovery_skip account_id=%d turn=%d reason=%s previous_response_id=%s",
								account.ID,
								turn,
								normalizeOpenAIWSLogValue(reason),
								truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
							)
						} else {
							updatedWithInput, setInputErr := setOpenAIWSPayloadInputSequence(
								updatedPayload,
								currentTurnReplayInput,
								currentTurnReplayInputExists,
							)
							if setInputErr != nil {
								logOpenAIWSModeInfo(
									"ingress_ws_preferred_conn_recovery_skip account_id=%d turn=%d reason=set_full_input_error previous_response_id=%s cause=%s",
									account.ID,
									turn,
									truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
									truncateOpenAIWSLogValue(setInputErr.Error(), openAIWSLogValueMaxLen),
								)
							} else {
								logOpenAIWSModeInfo(
									"ingress_ws_preferred_conn_recovery account_id=%d turn=%d action=drop_previous_response_id_retry previous_response_id=%s",
									account.ID,
									turn,
									truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
								)
								turnPrevRecoveryTried = true
								currentPayload = updatedWithInput
								currentPayloadBytes = len(updatedWithInput)
								skipBeforeTurn = true
								continue
							}
						}
					}
				}
				logOpenAIWSModeInfo(
					"ingress_ws_acquire_lease_fail account_id=%d turn=%d conn_id=%s cause=%s",
					account.ID,
					turn,
					truncateOpenAIWSLogValue(sessionConnID, openAIWSIDValueMaxLen),
					truncateOpenAIWSLogValue(acquireErr.Error(), openAIWSLogValueMaxLen),
				)
				return fmt.Errorf("acquire upstream websocket: %w", acquireErr)
			}
			sessionLease = acquiredLease
			sessionConnID = strings.TrimSpace(sessionLease.ConnID())
			if storeDisabled {
				pinSessionConn(sessionConnID)
			} else {
				unpinSessionConn(sessionConnID)
			}
		}
		shouldPreflightPing := turn > 1 && sessionLease != nil && turnRetry == 0
		if shouldPreflightPing && openAIWSIngressPreflightPingIdle > 0 && !lastTurnFinishedAt.IsZero() {
			if time.Since(lastTurnFinishedAt) < openAIWSIngressPreflightPingIdle {
				shouldPreflightPing = false
			}
		}
		if shouldPreflightPing {
			if pingErr := sessionLease.PingWithTimeout(openAIWSConnHealthCheckTO); pingErr != nil {
				logOpenAIWSModeInfo(
					"ingress_ws_upstream_preflight_ping_fail account_id=%d turn=%d conn_id=%s cause=%s",
					account.ID,
					turn,
					truncateOpenAIWSLogValue(sessionConnID, openAIWSIDValueMaxLen),
					truncateOpenAIWSLogValue(pingErr.Error(), openAIWSLogValueMaxLen),
				)
				if forcePreferredConn {
					if !turnPrevRecoveryTried && hasFunctionCallOutput {
						// 与 acquire 失败分支对齐：function_call_output 续链优先保留 previous_response_id，
						// 仅单次放宽 strict affinity，避免因 preflight 失败提前降级为 full create。
						logOpenAIWSModeInfo(
							"ingress_ws_preflight_ping_recovery account_id=%d turn=%d conn_id=%s action=retry_without_strict_affinity_keep_previous_response_id previous_response_id=%s",
							account.ID,
							turn,
							truncateOpenAIWSLogValue(sessionConnID, openAIWSIDValueMaxLen),
							truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
						)
						resetSessionLease(true)
						strictAffinityBypassOnce = true
						skipBeforeTurn = true
						continue
					}
					if !turnPrevRecoveryTried && currentPreviousResponseID != "" {
						updatedPayload, removed, dropErr := dropPreviousResponseIDFromRawPayload(currentPayload)
						if dropErr != nil || !removed {
							reason := "not_removed"
							if dropErr != nil {
								reason = "drop_error"
							}
							logOpenAIWSModeInfo(
								"ingress_ws_preflight_ping_recovery_skip account_id=%d turn=%d conn_id=%s reason=%s previous_response_id=%s",
								account.ID,
								turn,
								truncateOpenAIWSLogValue(sessionConnID, openAIWSIDValueMaxLen),
								normalizeOpenAIWSLogValue(reason),
								truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
							)
						} else {
							updatedWithInput, setInputErr := setOpenAIWSPayloadInputSequence(
								updatedPayload,
								currentTurnReplayInput,
								currentTurnReplayInputExists,
							)
							if setInputErr != nil {
								logOpenAIWSModeInfo(
									"ingress_ws_preflight_ping_recovery_skip account_id=%d turn=%d conn_id=%s reason=set_full_input_error previous_response_id=%s cause=%s",
									account.ID,
									turn,
									truncateOpenAIWSLogValue(sessionConnID, openAIWSIDValueMaxLen),
									truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
									truncateOpenAIWSLogValue(setInputErr.Error(), openAIWSLogValueMaxLen),
								)
							} else {
								logOpenAIWSModeInfo(
									"ingress_ws_preflight_ping_recovery account_id=%d turn=%d conn_id=%s action=drop_previous_response_id_retry previous_response_id=%s",
									account.ID,
									turn,
									truncateOpenAIWSLogValue(sessionConnID, openAIWSIDValueMaxLen),
									truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
								)
								turnPrevRecoveryTried = true
								currentPayload = updatedWithInput
								currentPayloadBytes = len(updatedWithInput)
								resetSessionLease(true)
								skipBeforeTurn = true
								continue
							}
						}
					}
					resetSessionLease(true)
					return NewOpenAIWSClientCloseError(
						coderws.StatusPolicyViolation,
						openAIWSContinuationUnavailableReason,
						pingErr,
					)
				}
				resetSessionLease(true)

				acquiredLease, acquireErr := acquireTurnLease(
					turn,
					preferredConnID,
					forcePreferredConn,
					currentPreviousResponseID != "",
				)
				if acquireErr != nil {
					return fmt.Errorf("acquire upstream websocket after preflight ping fail: %w", acquireErr)
				}
				sessionLease = acquiredLease
				sessionConnID = strings.TrimSpace(sessionLease.ConnID())
				if storeDisabled {
					pinSessionConn(sessionConnID)
				}
			}
		}
		connID := sessionConnID
		if currentPreviousResponseID != "" {
			chainedFromLast := expectedPrev != "" && currentPreviousResponseID == expectedPrev
			currentPreviousResponseIDKind := ClassifyOpenAIPreviousResponseIDKind(currentPreviousResponseID)
			logOpenAIWSModeInfo(
				"ingress_ws_turn_chain account_id=%d turn=%d conn_id=%s previous_response_id=%s previous_response_id_kind=%s last_turn_response_id=%s chained_from_last=%v preferred_conn_id=%s header_session_id=%s header_conversation_id=%s has_turn_state=%v turn_state_len=%d has_prompt_cache_key=%v store_disabled=%v",
				account.ID,
				turn,
				truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
				truncateOpenAIWSLogValue(currentPreviousResponseID, openAIWSIDValueMaxLen),
				normalizeOpenAIWSLogValue(currentPreviousResponseIDKind),
				truncateOpenAIWSLogValue(expectedPrev, openAIWSIDValueMaxLen),
				chainedFromLast,
				truncateOpenAIWSLogValue(preferredConnID, openAIWSIDValueMaxLen),
				openAIWSHeaderValueForLog(baseAcquireReq.Headers, "session_id"),
				openAIWSHeaderValueForLog(baseAcquireReq.Headers, "conversation_id"),
				turnState != "",
				len(turnState),
				openAIWSPayloadStringFromRaw(currentPayload, "prompt_cache_key") != "",
				storeDisabled,
			)
		}

		result, relayErr := sendAndRelay(turn, sessionLease, currentPayload, currentPayloadBytes, currentOriginalModel, expectedPrev)
		if relayErr != nil {
			if recoverIngressPrevResponseNotFound(relayErr, turn, connID) {
				continue
			}
			if retryIngressTurn(relayErr, turn, connID) {
				continue
			}
			finalErr := relayErr
			if unwrapped := errors.Unwrap(relayErr); unwrapped != nil {
				finalErr = unwrapped
			}
			abortReason, abortExpected := classifyOpenAIWSIngressTurnAbortReason(relayErr)
			s.recordOpenAIWSTurnAbort(abortReason, abortExpected)
			logOpenAIWSIngressTurnAbort(account.ID, turn, connID, abortReason, abortExpected, finalErr)
			if hooks != nil && hooks.AfterTurn != nil {
				hooks.AfterTurn(turn, nil, finalErr)
			}
			switch openAIWSIngressTurnAbortDispositionForReason(abortReason) {
			case openAIWSIngressTurnAbortDispositionContinueTurn:
				// turn 级终止：当前 turn 失败，但客户端 WS 会话继续。
				// 这样可与 Codex 客户端语义对齐：后续 turn 允许在新上游连接上继续进行。
				//
				// 关键修复：若未向客户端写入过任何数据 (wroteDownstream=false)，
				// 必须补发 error 事件通知客户端本轮失败，否则客户端会一直等待响应，
				// 而服务端在 advanceToNextClientTurn 中等待客户端下一条消息 → 双向死锁。
				if !openAIWSIngressTurnWroteDownstream(relayErr) {
					abortMessage := "turn failed: " + string(abortReason)
					if finalErr != nil {
						abortMessage = finalErr.Error()
					}
					errorEvent := []byte(`{"type":"error","error":{"type":"server_error","code":"` + string(abortReason) + `","message":` + strconv.Quote(abortMessage) + `}}`)
					if writeErr := writeClientMessage(errorEvent); writeErr != nil {
						logOpenAIWSModeInfo(
							"ingress_ws_turn_abort_notify_failed account_id=%d turn=%d conn_id=%s reason=%s cause=%s",
							account.ID,
							turn,
							truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
							normalizeOpenAIWSLogValue(string(abortReason)),
							truncateOpenAIWSLogValue(writeErr.Error(), openAIWSLogValueMaxLen),
						)
					} else {
						logOpenAIWSModeInfo(
							"ingress_ws_turn_abort_notified account_id=%d turn=%d conn_id=%s reason=%s",
							account.ID,
							turn,
							truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
							normalizeOpenAIWSLogValue(string(abortReason)),
						)
					}
				}
				resetSessionLease(true)
				clearSessionLastResponseID()
				turnRetry = 0
				turnPrevRecoveryTried = false
				turnPreferredRehydrateTried = false
				exit, advanceErr := advanceToNextClientTurn(turn, connID)
				if advanceErr != nil {
					return advanceErr
				}
				if exit {
					return nil
				}
				s.recordOpenAIWSTurnAbortRecovered()
				turn++
				continue
			case openAIWSIngressTurnAbortDispositionCloseGracefully:
				resetSessionLease(true)
				clearSessionLastResponseID()
				return nil
			case openAIWSIngressTurnAbortDispositionFailRequest:
				sessionLease.MarkBroken()
				return finalErr
			}
		}
		turnRetry = 0
		turnPrevRecoveryTried = false
		turnPreferredRehydrateTried = false
		lastTurnFinishedAt = time.Now()
		if hooks != nil && hooks.AfterTurn != nil {
			hooks.AfterTurn(turn, result, nil)
		}
		if result == nil {
			return errors.New("websocket turn result is nil")
		}
		responseID := strings.TrimSpace(result.RequestID)
		persistLastResponseID := responseID != "" && shouldPersistOpenAIWSLastResponseID(result.TerminalEventType)
		logOpenAIWSModeInfo(
			"ingress_ws_turn_completed account_id=%d turn=%d conn_id=%s response_id=%s duration_ms=%d persist_response_id=%v has_function_call_output=%v pending_function_calls=%d",
			account.ID,
			turn,
			truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
			truncateOpenAIWSLogValue(responseID, openAIWSIDValueMaxLen),
			result.Duration.Milliseconds(),
			persistLastResponseID,
			hasFunctionCallOutput,
			len(result.PendingFunctionCallIDs),
		)
		if persistLastResponseID {
			lastTurnResponseID = responseID
		} else {
			clearSessionLastResponseID()
		}
		lastTurnPayload = cloneOpenAIWSPayloadBytes(currentPayload)
		lastTurnReplayInput = cloneOpenAIWSRawMessages(currentTurnReplayInput)
		lastTurnReplayInputExists = currentTurnReplayInputExists
		nextStrictState, strictStateErr := buildOpenAIWSIngressPreviousTurnStrictState(currentPayload)
		if strictStateErr != nil {
			lastTurnStrictState = nil
			logOpenAIWSModeInfo(
				"ingress_ws_prev_response_strict_state_skip account_id=%d turn=%d conn_id=%s reason=build_error cause=%s",
				account.ID,
				turn,
				truncateOpenAIWSLogValue(connID, openAIWSIDValueMaxLen),
				truncateOpenAIWSLogValue(strictStateErr.Error(), openAIWSLogValueMaxLen),
			)
		} else {
			lastTurnStrictState = nextStrictState
		}

		if stateStore != nil &&
			expectedPrev != "" &&
			currentPreviousResponseID == expectedPrev &&
			(hasFunctionCallOutput || len(pendingExpectedCallIDs) > 0) {
			stateStore.DeleteResponsePendingToolCalls(groupID, expectedPrev)
		}

		if responseID != "" && stateStore != nil {
			ttl := s.openAIWSResponseStickyTTL()
			logOpenAIWSBindResponseAccountWarn(groupID, account.ID, responseID, stateStore.BindResponseAccount(ctx, groupID, responseID, account.ID, ttl))
			if poolConnID, ok := normalizeOpenAIWSPreferredConnID(connID); ok {
				stateStore.BindResponseConn(responseID, poolConnID, ttl)
			}
			if pendingFunctionCallIDs := openAIWSNormalizeCallIDs(result.PendingFunctionCallIDs); len(pendingFunctionCallIDs) > 0 {
				stateStore.BindResponsePendingToolCalls(groupID, responseID, pendingFunctionCallIDs, ttl)
			} else {
				stateStore.DeleteResponsePendingToolCalls(groupID, responseID)
			}
			if sessionHash != "" && persistLastResponseID {
				stateStore.BindSessionLastResponseID(groupID, sessionHash, responseID, s.openAIWSSessionStickyTTL())
			}
		}
		if connID != "" {
			preferredConnID = connID
		}
		yieldSessionLease()

		exit, advanceErr := advanceToNextClientTurn(turn, connID)
		if advanceErr != nil {
			return advanceErr
		}
		if exit {
			return nil
		}
		turn++
	}
}

func (s *OpenAIGatewayService) isOpenAIWSGeneratePrewarmEnabled() bool {
	return s != nil && s.cfg != nil && s.cfg.Gateway.OpenAIWS.PrewarmGenerateEnabled
}

// performOpenAIWSGeneratePrewarm 在 WSv2 下执行可选的 generate=false 预热。
// 预热默认关闭，仅在配置开启后生效；失败时按可恢复错误回退到 HTTP。
func (s *OpenAIGatewayService) performOpenAIWSGeneratePrewarm(
	ctx context.Context,
	lease openAIWSIngressUpstreamLease,
	decision OpenAIWSProtocolDecision,
	payload map[string]any,
	previousResponseID string,
	reqBody map[string]any,
	account *Account,
	stateStore OpenAIWSStateStore,
	groupID int64,
) error {
	if s == nil {
		return nil
	}
	if lease == nil || account == nil {
		logOpenAIWSModeInfo("prewarm_skip reason=invalid_state has_lease=%v has_account=%v", lease != nil, account != nil)
		return nil
	}
	connID := strings.TrimSpace(lease.ConnID())
	if !s.isOpenAIWSGeneratePrewarmEnabled() {
		return nil
	}
	if decision.Transport != OpenAIUpstreamTransportResponsesWebsocketV2 {
		logOpenAIWSModeInfo(
			"prewarm_skip account_id=%d conn_id=%s reason=transport_not_v2 transport=%s",
			account.ID,
			connID,
			normalizeOpenAIWSLogValue(string(decision.Transport)),
		)
		return nil
	}
	if strings.TrimSpace(previousResponseID) != "" {
		logOpenAIWSModeInfo(
			"prewarm_skip account_id=%d conn_id=%s reason=has_previous_response_id previous_response_id=%s",
			account.ID,
			connID,
			truncateOpenAIWSLogValue(previousResponseID, openAIWSIDValueMaxLen),
		)
		return nil
	}
	if lease.IsPrewarmed() {
		logOpenAIWSModeInfo("prewarm_skip account_id=%d conn_id=%s reason=already_prewarmed", account.ID, connID)
		return nil
	}
	if NeedsToolContinuation(reqBody) {
		logOpenAIWSModeInfo("prewarm_skip account_id=%d conn_id=%s reason=tool_continuation", account.ID, connID)
		return nil
	}
	prewarmStart := time.Now()
	logOpenAIWSModeInfo("prewarm_start account_id=%d conn_id=%s", account.ID, connID)

	prewarmPayload := make(map[string]any, len(payload)+1)
	for k, v := range payload {
		prewarmPayload[k] = v
	}
	prewarmPayload["generate"] = false
	prewarmPayloadJSON := payloadAsJSONBytes(prewarmPayload)

	if err := lease.WriteJSONWithContextTimeout(ctx, prewarmPayload, s.openAIWSWriteTimeout()); err != nil {
		lease.MarkBroken()
		logOpenAIWSModeInfo(
			"prewarm_write_fail account_id=%d conn_id=%s cause=%s",
			account.ID,
			connID,
			truncateOpenAIWSLogValue(err.Error(), openAIWSLogValueMaxLen),
		)
		return wrapOpenAIWSFallback("prewarm_write", err)
	}
	logOpenAIWSModeInfo("prewarm_write_sent account_id=%d conn_id=%s payload_bytes=%d", account.ID, connID, len(prewarmPayloadJSON))

	prewarmResponseID := ""
	prewarmEventCount := 0
	prewarmTerminalCount := 0
	for {
		message, readErr := lease.ReadMessageWithContextTimeout(ctx, s.openAIWSReadTimeout())
		if readErr != nil {
			lease.MarkBroken()
			closeStatus, closeReason := summarizeOpenAIWSReadCloseError(readErr)
			logOpenAIWSModeInfo(
				"prewarm_read_fail account_id=%d conn_id=%s close_status=%s close_reason=%s cause=%s events=%d",
				account.ID,
				connID,
				closeStatus,
				closeReason,
				truncateOpenAIWSLogValue(readErr.Error(), openAIWSLogValueMaxLen),
				prewarmEventCount,
			)
			return wrapOpenAIWSFallback("prewarm_"+classifyOpenAIWSReadFallbackReason(readErr), readErr)
		}

		eventType, eventResponseID := parseOpenAIWSEventType(message)
		if eventType == "" {
			continue
		}
		prewarmEventCount++
		if prewarmResponseID == "" && eventResponseID != "" {
			prewarmResponseID = eventResponseID
		}
		if prewarmEventCount <= openAIWSPrewarmEventLogHead || eventType == "error" || isOpenAIWSTerminalEvent(eventType) {
			logOpenAIWSModeInfo(
				"prewarm_event account_id=%d conn_id=%s idx=%d type=%s bytes=%d",
				account.ID,
				connID,
				prewarmEventCount,
				truncateOpenAIWSLogValue(eventType, openAIWSLogValueMaxLen),
				len(message),
			)
		}

		if eventType == "error" {
			errCodeRaw, errTypeRaw, errMsgRaw := parseOpenAIWSErrorEventFields(message)
			errMsg := strings.TrimSpace(errMsgRaw)
			if errMsg == "" {
				errMsg = "OpenAI websocket prewarm error"
			}
			fallbackReason, canFallback := classifyOpenAIWSErrorEventFromRaw(errCodeRaw, errTypeRaw, errMsgRaw)
			errCode, errType, errMessage := summarizeOpenAIWSErrorEventFieldsFromRaw(errCodeRaw, errTypeRaw, errMsgRaw)
			logOpenAIWSModeInfo(
				"prewarm_error_event account_id=%d conn_id=%s idx=%d fallback_reason=%s can_fallback=%v err_code=%s err_type=%s err_message=%s",
				account.ID,
				connID,
				prewarmEventCount,
				truncateOpenAIWSLogValue(fallbackReason, openAIWSLogValueMaxLen),
				canFallback,
				errCode,
				errType,
				errMessage,
			)
			lease.MarkBroken()
			if canFallback {
				return wrapOpenAIWSFallback("prewarm_"+fallbackReason, errors.New(errMsg))
			}
			return wrapOpenAIWSFallback("prewarm_error_event", errors.New(errMsg))
		}

		if isOpenAIWSTerminalEvent(eventType) {
			prewarmTerminalCount++
			break
		}
	}

	lease.MarkPrewarmed()
	if prewarmResponseID != "" && stateStore != nil {
		ttl := s.openAIWSResponseStickyTTL()
		logOpenAIWSBindResponseAccountWarn(groupID, account.ID, prewarmResponseID, stateStore.BindResponseAccount(ctx, groupID, prewarmResponseID, account.ID, ttl))
		if connID, ok := normalizeOpenAIWSPreferredConnID(lease.ConnID()); ok {
			stateStore.BindResponseConn(prewarmResponseID, connID, ttl)
		}
	}
	logOpenAIWSModeInfo(
		"prewarm_done account_id=%d conn_id=%s response_id=%s events=%d terminal_events=%d duration_ms=%d",
		account.ID,
		connID,
		truncateOpenAIWSLogValue(prewarmResponseID, openAIWSIDValueMaxLen),
		prewarmEventCount,
		prewarmTerminalCount,
		time.Since(prewarmStart).Milliseconds(),
	)
	return nil
}

// SelectAccountByPreviousResponseID 按 previous_response_id 命中账号粘连。
// 未命中或账号不可用时返回 (nil, nil)，由调用方继续走常规调度。
func (s *OpenAIGatewayService) SelectAccountByPreviousResponseID(
	ctx context.Context,
	groupID *int64,
	previousResponseID string,
	requestedModel string,
	excludedIDs map[int64]struct{},
) (*AccountSelectionResult, error) {
	if s == nil {
		return nil, nil
	}
	responseID := strings.TrimSpace(previousResponseID)
	if responseID == "" {
		return nil, nil
	}
	store := s.getOpenAIWSStateStore()
	if store == nil {
		return nil, nil
	}

	accountID, err := store.GetResponseAccount(ctx, derefGroupID(groupID), responseID)
	if err != nil || accountID <= 0 {
		return nil, nil
	}
	if excludedIDs != nil {
		if _, excluded := excludedIDs[accountID]; excluded {
			return nil, nil
		}
	}

	account, err := s.getSchedulableAccount(ctx, accountID)
	if err != nil || account == nil {
		_ = store.DeleteResponseAccount(ctx, derefGroupID(groupID), responseID)
		return nil, nil
	}
	// 非 WSv2 场景（如 force_http/全局关闭）不应使用 previous_response_id 粘连，
	// 以保持“回滚到 HTTP”后的历史行为一致性。
	if s.getOpenAIWSProtocolResolver().Resolve(account).Transport != OpenAIUpstreamTransportResponsesWebsocketV2 {
		return nil, nil
	}
	if shouldClearStickySession(account, requestedModel) || !account.IsOpenAI() {
		_ = store.DeleteResponseAccount(ctx, derefGroupID(groupID), responseID)
		return nil, nil
	}
	if requestedModel != "" && !account.IsModelSupported(requestedModel) {
		return nil, nil
	}

	result, acquireErr := s.tryAcquireAccountSlot(ctx, accountID, account.Concurrency)
	if acquireErr == nil && result.Acquired {
		logOpenAIWSBindResponseAccountWarn(
			derefGroupID(groupID),
			accountID,
			responseID,
			store.BindResponseAccount(ctx, derefGroupID(groupID), responseID, accountID, s.openAIWSResponseStickyTTL()),
		)
		return &AccountSelectionResult{
			Account:     account,
			Acquired:    true,
			ReleaseFunc: result.ReleaseFunc,
		}, nil
	}

	cfg := s.schedulingConfig()
	if s.concurrencyService != nil {
		waitingCount, _ := s.concurrencyService.GetAccountWaitingCount(ctx, accountID)
		if waitingCount < cfg.StickySessionMaxWaiting {
			return &AccountSelectionResult{
				Account: account,
				WaitPlan: &AccountWaitPlan{
					AccountID:      accountID,
					MaxConcurrency: account.Concurrency,
					Timeout:        cfg.StickySessionWaitTimeout,
					MaxWaiting:     cfg.StickySessionMaxWaiting,
				},
			}, nil
		}
	}
	return nil, nil
}

