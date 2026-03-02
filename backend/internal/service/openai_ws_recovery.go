package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	coderws "github.com/coder/websocket"
)

// openAIWSFallbackError 表示可安全回退到 HTTP 的 WS 错误（尚未写下游）。
type openAIWSFallbackError struct {
	Reason string
	Err    error
}

func (e *openAIWSFallbackError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return fmt.Sprintf("openai ws fallback: %s", strings.TrimSpace(e.Reason))
	}
	return fmt.Sprintf("openai ws fallback: %s: %v", strings.TrimSpace(e.Reason), e.Err)
}

func (e *openAIWSFallbackError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func wrapOpenAIWSFallback(reason string, err error) error {
	return &openAIWSFallbackError{Reason: strings.TrimSpace(reason), Err: err}
}

// OpenAIWSClientCloseError 表示应以指定 WebSocket close code 主动关闭客户端连接的错误。
type OpenAIWSClientCloseError struct {
	statusCode coderws.StatusCode
	reason     string
	err        error
}

type openAIWSIngressTurnError struct {
	stage           string
	cause           error
	wroteDownstream bool
	partialResult   *OpenAIForwardResult
}

type openAIWSIngressUpstreamLease interface {
	ConnID() string
	QueueWaitDuration() time.Duration
	ConnPickDuration() time.Duration
	Reused() bool
	ScheduleLayer() string
	StickinessLevel() string
	MigrationUsed() bool
	HandshakeHeader(name string) string
	IsPrewarmed() bool
	MarkPrewarmed()
	WriteJSONWithContextTimeout(ctx context.Context, value any, timeout time.Duration) error
	ReadMessageWithContextTimeout(ctx context.Context, timeout time.Duration) ([]byte, error)
	PingWithTimeout(timeout time.Duration) error
	MarkBroken()
	Yield()
	Release()
}

func (e *openAIWSIngressTurnError) Error() string {
	if e == nil {
		return ""
	}
	if e.cause == nil {
		return strings.TrimSpace(e.stage)
	}
	return e.cause.Error()
}

func (e *openAIWSIngressTurnError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

func wrapOpenAIWSIngressTurnError(stage string, cause error, wroteDownstream bool) error {
	return wrapOpenAIWSIngressTurnErrorWithPartial(stage, cause, wroteDownstream, nil)
}

func cloneOpenAIForwardResult(result *OpenAIForwardResult) *OpenAIForwardResult {
	if result == nil {
		return nil
	}
	cloned := *result
	if result.PendingFunctionCallIDs != nil {
		cloned.PendingFunctionCallIDs = make([]string, len(result.PendingFunctionCallIDs))
		copy(cloned.PendingFunctionCallIDs, result.PendingFunctionCallIDs)
	}
	return &cloned
}

func wrapOpenAIWSIngressTurnErrorWithPartial(stage string, cause error, wroteDownstream bool, partialResult *OpenAIForwardResult) error {
	if cause == nil {
		return nil
	}
	return &openAIWSIngressTurnError{
		stage:           strings.TrimSpace(stage),
		cause:           cause,
		wroteDownstream: wroteDownstream,
		partialResult:   cloneOpenAIForwardResult(partialResult),
	}
}

// OpenAIWSIngressTurnPartialResult returns usage-bearing partial turn result
// when WS ingress turn aborts after receiving upstream events.
func OpenAIWSIngressTurnPartialResult(err error) (*OpenAIForwardResult, bool) {
	var turnErr *openAIWSIngressTurnError
	if !errors.As(err, &turnErr) || turnErr == nil || turnErr.partialResult == nil {
		return nil, false
	}
	return cloneOpenAIForwardResult(turnErr.partialResult), true
}

func isOpenAIWSIngressTurnRetryable(err error) bool {
	var turnErr *openAIWSIngressTurnError
	if !errors.As(err, &turnErr) || turnErr == nil {
		return false
	}
	if errors.Is(turnErr.cause, context.Canceled) || errors.Is(turnErr.cause, context.DeadlineExceeded) {
		return false
	}
	if turnErr.wroteDownstream {
		return false
	}
	switch turnErr.stage {
	case "write_upstream", "read_upstream":
		return true
	default:
		return false
	}
}

func openAIWSIngressTurnRetryReason(err error) string {
	var turnErr *openAIWSIngressTurnError
	if !errors.As(err, &turnErr) || turnErr == nil {
		return "unknown"
	}
	if turnErr.stage == "" {
		return "unknown"
	}
	return turnErr.stage
}

func isOpenAIWSIngressPreviousResponseNotFound(err error) bool {
	var turnErr *openAIWSIngressTurnError
	if !errors.As(err, &turnErr) || turnErr == nil {
		return false
	}
	if strings.TrimSpace(turnErr.stage) != openAIWSIngressStagePreviousResponseNotFound {
		return false
	}
	return !turnErr.wroteDownstream
}

func isOpenAIWSIngressToolOutputNotFound(err error) bool {
	var turnErr *openAIWSIngressTurnError
	if !errors.As(err, &turnErr) || turnErr == nil {
		return false
	}
	if strings.TrimSpace(turnErr.stage) != openAIWSIngressStageToolOutputNotFound {
		return false
	}
	return !turnErr.wroteDownstream
}

// openAIWSIngressTurnWroteDownstream 返回本次 turn 是否已向客户端写入过数据。
// 用于 ContinueTurn abort 时判断是否需要补发 error 事件。
func openAIWSIngressTurnWroteDownstream(err error) bool {
	var turnErr *openAIWSIngressTurnError
	if !errors.As(err, &turnErr) || turnErr == nil {
		return false
	}
	return turnErr.wroteDownstream
}

func isOpenAIWSIngressUpstreamErrorEvent(err error) bool {
	var turnErr *openAIWSIngressTurnError
	if !errors.As(err, &turnErr) || turnErr == nil {
		return false
	}
	return strings.TrimSpace(turnErr.stage) == "upstream_error_event"
}

func isOpenAIWSContinuationUnavailableCloseError(err error) bool {
	var closeErr *OpenAIWSClientCloseError
	if !errors.As(err, &closeErr) || closeErr == nil {
		return false
	}
	if closeErr.StatusCode() != coderws.StatusPolicyViolation {
		return false
	}
	return strings.Contains(closeErr.Reason(), openAIWSContinuationUnavailableReason)
}

// NewOpenAIWSClientCloseError 创建一个客户端 WS 关闭错误。
func NewOpenAIWSClientCloseError(statusCode coderws.StatusCode, reason string, err error) error {
	return &OpenAIWSClientCloseError{
		statusCode: statusCode,
		reason:     strings.TrimSpace(reason),
		err:        err,
	}
}

func (e *OpenAIWSClientCloseError) Error() string {
	if e == nil {
		return ""
	}
	if e.err == nil {
		return fmt.Sprintf("openai ws client close: %d %s", int(e.statusCode), strings.TrimSpace(e.reason))
	}
	return fmt.Sprintf("openai ws client close: %d %s: %v", int(e.statusCode), strings.TrimSpace(e.reason), e.err)
}

func (e *OpenAIWSClientCloseError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func (e *OpenAIWSClientCloseError) StatusCode() coderws.StatusCode {
	if e == nil {
		return coderws.StatusInternalError
	}
	return e.statusCode
}

func (e *OpenAIWSClientCloseError) Reason() string {
	if e == nil {
		return ""
	}
	return strings.TrimSpace(e.reason)
}

func summarizeOpenAIWSReadCloseError(err error) (status string, reason string) {
	if err == nil {
		return "-", "-"
	}
	statusCode := coderws.CloseStatus(err)
	if statusCode == -1 {
		return "-", "-"
	}
	closeStatus := fmt.Sprintf("%d(%s)", int(statusCode), statusCode.String())
	closeReason := "-"
	var closeErr coderws.CloseError
	if errors.As(err, &closeErr) {
		reasonText := strings.TrimSpace(closeErr.Reason)
		if reasonText != "" {
			closeReason = normalizeOpenAIWSLogValue(reasonText)
		}
	}
	return normalizeOpenAIWSLogValue(closeStatus), closeReason
}

func unwrapOpenAIWSDialBaseError(err error) error {
	if err == nil {
		return nil
	}
	var dialErr *openAIWSDialError
	if errors.As(err, &dialErr) && dialErr != nil && dialErr.Err != nil {
		return dialErr.Err
	}
	return err
}

func openAIWSDialRespHeaderForLog(err error, key string) string {
	var dialErr *openAIWSDialError
	if !errors.As(err, &dialErr) || dialErr == nil || dialErr.ResponseHeaders == nil {
		return "-"
	}
	return truncateOpenAIWSLogValue(dialErr.ResponseHeaders.Get(key), openAIWSHeaderValueMaxLen)
}

func classifyOpenAIWSDialError(err error) string {
	if err == nil {
		return "-"
	}
	baseErr := unwrapOpenAIWSDialBaseError(err)
	if baseErr == nil {
		return "-"
	}
	if errors.Is(baseErr, context.DeadlineExceeded) {
		return "ctx_deadline_exceeded"
	}
	if errors.Is(baseErr, context.Canceled) {
		return "ctx_canceled"
	}
	var netErr net.Error
	if errors.As(baseErr, &netErr) && netErr.Timeout() {
		return "net_timeout"
	}
	if status := coderws.CloseStatus(baseErr); status != -1 {
		return normalizeOpenAIWSLogValue(fmt.Sprintf("ws_close_%d", int(status)))
	}
	message := strings.ToLower(strings.TrimSpace(baseErr.Error()))
	switch {
	case strings.Contains(message, "handshake not finished"):
		return "handshake_not_finished"
	case strings.Contains(message, "bad handshake"):
		return "bad_handshake"
	case strings.Contains(message, "connection refused"):
		return "connection_refused"
	case strings.Contains(message, "no such host"):
		return "dns_not_found"
	case strings.Contains(message, "tls"):
		return "tls_error"
	case strings.Contains(message, "i/o timeout"):
		return "io_timeout"
	case strings.Contains(message, "context deadline exceeded"):
		return "ctx_deadline_exceeded"
	default:
		return "dial_error"
	}
}

func summarizeOpenAIWSDialError(err error) (
	statusCode int,
	dialClass string,
	closeStatus string,
	closeReason string,
	respServer string,
	respVia string,
	respCFRay string,
	respRequestID string,
) {
	dialClass = "-"
	closeStatus = "-"
	closeReason = "-"
	respServer = "-"
	respVia = "-"
	respCFRay = "-"
	respRequestID = "-"
	if err == nil {
		return
	}
	var dialErr *openAIWSDialError
	if errors.As(err, &dialErr) && dialErr != nil {
		statusCode = dialErr.StatusCode
		respServer = openAIWSDialRespHeaderForLog(err, "server")
		respVia = openAIWSDialRespHeaderForLog(err, "via")
		respCFRay = openAIWSDialRespHeaderForLog(err, "cf-ray")
		respRequestID = openAIWSDialRespHeaderForLog(err, "x-request-id")
	}
	dialClass = normalizeOpenAIWSLogValue(classifyOpenAIWSDialError(err))
	closeStatus, closeReason = summarizeOpenAIWSReadCloseError(unwrapOpenAIWSDialBaseError(err))
	return
}

func isOpenAIWSClientDisconnectError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) || errors.Is(err, context.Canceled) {
		return true
	}
	switch coderws.CloseStatus(err) {
	case coderws.StatusNormalClosure, coderws.StatusGoingAway, coderws.StatusNoStatusRcvd, coderws.StatusAbnormalClosure:
		return true
	}
	message := strings.ToLower(strings.TrimSpace(err.Error()))
	if message == "" {
		return false
	}
	return strings.Contains(message, "failed to read frame header: eof") ||
		strings.Contains(message, "unexpected eof") ||
		strings.Contains(message, "use of closed network connection") ||
		strings.Contains(message, "connection reset by peer") ||
		strings.Contains(message, "broken pipe")
}

func classifyOpenAIWSIngressReadErrorClass(err error) string {
	if err == nil {
		return "unknown"
	}
	if errors.Is(err, context.Canceled) {
		return "context_canceled"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	switch coderws.CloseStatus(err) {
	case coderws.StatusServiceRestart:
		return "service_restart"
	case coderws.StatusTryAgainLater:
		return "try_again_later"
	}
	if isOpenAIWSClientDisconnectError(err) {
		return "upstream_closed"
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "eof"
	}
	return "unknown"
}

func isOpenAIWSStreamWriteDisconnectError(err error, reqCtx context.Context) bool {
	if err == nil {
		return false
	}
	if reqCtx != nil && reqCtx.Err() != nil {
		return true
	}
	return isOpenAIWSClientDisconnectError(err)
}

func openAIWSIngressResolveDrainReadTimeout(
	baseTimeout time.Duration,
	disconnectDeadline time.Time,
	now time.Time,
) (time.Duration, bool) {
	if disconnectDeadline.IsZero() {
		return baseTimeout, false
	}
	remaining := disconnectDeadline.Sub(now)
	if remaining <= 0 {
		return 0, true
	}
	if baseTimeout <= 0 || remaining < baseTimeout {
		return remaining, false
	}
	return baseTimeout, false
}

func openAIWSIngressClientDisconnectedDrainTimeoutError(timeout time.Duration) error {
	if timeout <= 0 {
		timeout = openAIWSIngressClientDisconnectDrainTimeout
	}
	return fmt.Errorf("client disconnected before upstream terminal event (drain timeout=%s): %w", timeout, context.Canceled)
}

func openAIWSIngressPumpClosedTurnError(
	clientDisconnected bool,
	wroteDownstream bool,
	partialResult *OpenAIForwardResult,
) error {
	if clientDisconnected {
		return wrapOpenAIWSIngressTurnErrorWithPartial(
			"client_disconnected_drain_timeout",
			openAIWSIngressClientDisconnectedDrainTimeoutError(openAIWSIngressClientDisconnectDrainTimeout),
			wroteDownstream,
			partialResult,
		)
	}
	return wrapOpenAIWSIngressTurnErrorWithPartial(
		"read_upstream",
		errors.New("upstream event pump closed unexpectedly"),
		wroteDownstream,
		partialResult,
	)
}

func shouldFlushOpenAIWSBufferedEventsOnError(reqStream bool, wroteDownstream bool, clientDisconnected bool) bool {
	return reqStream && wroteDownstream && !clientDisconnected
}

// errOpenAIWSClientPreempted 表示客户端在当前 turn 尚未完成时发送了新的 response.create 请求。
var errOpenAIWSClientPreempted = errors.New("client preempted current turn with new request")

var errOpenAIWSAdvanceClientReadUnavailable = errors.New("client reader channels unavailable")

func openAIWSAdvanceConsumePendingClientReadErr(pendingErr *error) error {
	if pendingErr == nil || *pendingErr == nil {
		return nil
	}
	readErr := *pendingErr
	*pendingErr = nil
	return fmt.Errorf("read client websocket request: %w", readErr)
}

func openAIWSAdvanceClientReadUnavailable(clientMsgCh <-chan []byte, clientReadErrCh <-chan error) bool {
	return clientMsgCh == nil && clientReadErrCh == nil
}

// isOpenAIWSUpstreamRestartCloseError 检测上游是否因服务重启/维护关闭了连接。
// 1012=ServiceRestart, 1013=TryAgainLater，都是临时性上游维护，proxy 应视为可恢复错误。
func isOpenAIWSUpstreamRestartCloseError(err error) bool {
	var turnErr *openAIWSIngressTurnError
	if !errors.As(err, &turnErr) || turnErr == nil {
		return false
	}
	if turnErr.stage != "read_upstream" {
		return false
	}
	status := coderws.CloseStatus(turnErr.cause)
	return status == 1012 || status == 1013 // ServiceRestart, TryAgainLater
}

func classifyOpenAIWSIngressTurnAbortReason(err error) (openAIWSIngressTurnAbortReason, bool) {
	if err == nil {
		return openAIWSIngressTurnAbortReasonUnknown, false
	}
	if isOpenAIWSIngressPreviousResponseNotFound(err) {
		return openAIWSIngressTurnAbortReasonPreviousResponse, true
	}
	if isOpenAIWSIngressToolOutputNotFound(err) {
		return openAIWSIngressTurnAbortReasonToolOutput, true
	}
	if isOpenAIWSIngressUpstreamErrorEvent(err) {
		return openAIWSIngressTurnAbortReasonUpstreamError, true
	}
	if isOpenAIWSContinuationUnavailableCloseError(err) {
		return openAIWSIngressTurnAbortReasonContinuationUnavailable, true
	}
	if errors.Is(err, errOpenAIWSClientPreempted) {
		return openAIWSIngressTurnAbortReasonClientPreempted, true
	}
	if errors.Is(err, context.Canceled) {
		return openAIWSIngressTurnAbortReasonContextCanceled, true
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return openAIWSIngressTurnAbortReasonContextDeadline, false
	}
	if isOpenAIWSClientDisconnectError(err) {
		return openAIWSIngressTurnAbortReasonClientClosed, true
	}
	// 上游 ServiceRestart/TryAgainLater：必须在 stage-based 分类之前检测，
	// 否则会被 "read_upstream" 分支兜底为 FailRequest。
	if isOpenAIWSUpstreamRestartCloseError(err) {
		return openAIWSIngressTurnAbortReasonUpstreamRestart, true
	}

	var turnErr *openAIWSIngressTurnError
	if errors.As(err, &turnErr) && turnErr != nil {
		switch strings.TrimSpace(turnErr.stage) {
		case "write_upstream":
			return openAIWSIngressTurnAbortReasonWriteUpstream, false
		case "read_upstream":
			return openAIWSIngressTurnAbortReasonReadUpstream, false
		case "write_client":
			return openAIWSIngressTurnAbortReasonWriteClient, false
		}
	}
	return openAIWSIngressTurnAbortReasonUnknown, false
}

func openAIWSIngressTurnAbortDispositionForReason(reason openAIWSIngressTurnAbortReason) openAIWSIngressTurnAbortDisposition {
	switch reason {
	case openAIWSIngressTurnAbortReasonPreviousResponse,
		openAIWSIngressTurnAbortReasonToolOutput,
		openAIWSIngressTurnAbortReasonUpstreamError,
		openAIWSIngressTurnAbortReasonClientPreempted,
		openAIWSIngressTurnAbortReasonUpstreamRestart:
		return openAIWSIngressTurnAbortDispositionContinueTurn
	case openAIWSIngressTurnAbortReasonContextCanceled,
		openAIWSIngressTurnAbortReasonClientClosed:
		return openAIWSIngressTurnAbortDispositionCloseGracefully
	default:
		return openAIWSIngressTurnAbortDispositionFailRequest
	}
}

func classifyOpenAIWSReadFallbackReason(err error) string {
	if err == nil {
		return "read_event"
	}
	switch coderws.CloseStatus(err) {
	case coderws.StatusServiceRestart:
		return "service_restart"
	case coderws.StatusTryAgainLater:
		return "try_again_later"
	case coderws.StatusPolicyViolation:
		return "policy_violation"
	case coderws.StatusMessageTooBig:
		return "message_too_big"
	default:
		return "read_event"
	}
}

func classifyOpenAIWSAcquireError(err error) string {
	if err == nil {
		return "acquire_conn"
	}
	var dialErr *openAIWSDialError
	if errors.As(err, &dialErr) {
		switch dialErr.StatusCode {
		case 426:
			return "upgrade_required"
		case 401, 403:
			return "auth_failed"
		case 429:
			return "upstream_rate_limited"
		}
		if dialErr.StatusCode >= 500 {
			return "upstream_5xx"
		}
		return "dial_failed"
	}
	if errors.Is(err, errOpenAIWSConnQueueFull) {
		return "conn_queue_full"
	}
	if errors.Is(err, errOpenAIWSPreferredConnUnavailable) {
		return "preferred_conn_unavailable"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "acquire_timeout"
	}
	return "acquire_conn"
}

func classifyOpenAIWSErrorEventFromRaw(codeRaw, errTypeRaw, msgRaw string) (string, bool) {
	code := strings.ToLower(strings.TrimSpace(codeRaw))
	errType := strings.ToLower(strings.TrimSpace(errTypeRaw))
	msg := strings.ToLower(strings.TrimSpace(msgRaw))

	switch code {
	case "upgrade_required":
		return "upgrade_required", true
	case "websocket_not_supported", "websocket_unsupported":
		return "ws_unsupported", true
	case "websocket_connection_limit_reached":
		return "ws_connection_limit_reached", true
	case "previous_response_not_found":
		return "previous_response_not_found", true
	}
	if strings.Contains(msg, "upgrade required") || strings.Contains(msg, "status 426") {
		return "upgrade_required", true
	}
	if strings.Contains(errType, "upgrade") {
		return "upgrade_required", true
	}
	if strings.Contains(msg, "websocket") && strings.Contains(msg, "unsupported") {
		return "ws_unsupported", true
	}
	if strings.Contains(msg, "connection limit") && strings.Contains(msg, "websocket") {
		return "ws_connection_limit_reached", true
	}
	if strings.Contains(msg, "previous_response_not_found") ||
		(strings.Contains(msg, "previous response") && strings.Contains(msg, "not found")) {
		return "previous_response_not_found", true
	}
	// "No tool output found for function call <call_id>" / "No tool call found for function call output..."
	// 表示 previous_response_id 指向的 response 包含未完成的 function_call（例如用户在 Codex CLI
	// 按 ESC 取消 function_call 后重新发送消息）。此时 previous_response_id 本身就是问题，需要移除后重放。
	if strings.Contains(msg, "no tool output found") ||
		strings.Contains(msg, "no tool call found for function call output") ||
		(strings.Contains(msg, "no tool call found") && strings.Contains(msg, "function call output")) {
		return openAIWSIngressStageToolOutputNotFound, true
	}
	if strings.Contains(msg, "without its required following item") ||
		strings.Contains(msg, "without its required preceding item") {
		return openAIWSIngressStageToolOutputNotFound, true
	}
	if strings.Contains(errType, "server_error") || strings.Contains(code, "server_error") {
		return "upstream_error_event", true
	}
	return "event_error", false
}

func classifyOpenAIWSErrorEvent(message []byte) (string, bool) {
	if len(message) == 0 {
		return "event_error", false
	}
	return classifyOpenAIWSErrorEventFromRaw(parseOpenAIWSErrorEventFields(message))
}

func openAIWSErrorHTTPStatusFromRaw(codeRaw, errTypeRaw string) int {
	code := strings.ToLower(strings.TrimSpace(codeRaw))
	errType := strings.ToLower(strings.TrimSpace(errTypeRaw))
	switch {
	case strings.Contains(errType, "invalid_request"),
		strings.Contains(code, "invalid_request"),
		strings.Contains(code, "bad_request"),
		code == "previous_response_not_found":
		return http.StatusBadRequest
	case strings.Contains(errType, "authentication"),
		strings.Contains(code, "invalid_api_key"),
		strings.Contains(code, "unauthorized"):
		return http.StatusUnauthorized
	case strings.Contains(errType, "permission"),
		strings.Contains(code, "forbidden"):
		return http.StatusForbidden
	case strings.Contains(errType, "rate_limit"),
		strings.Contains(code, "rate_limit"),
		strings.Contains(code, "insufficient_quota"):
		return http.StatusTooManyRequests
	default:
		return http.StatusBadGateway
	}
}

func openAIWSErrorHTTPStatus(message []byte) int {
	if len(message) == 0 {
		return http.StatusBadGateway
	}
	codeRaw, errTypeRaw, _ := parseOpenAIWSErrorEventFields(message)
	return openAIWSErrorHTTPStatusFromRaw(codeRaw, errTypeRaw)
}

func (s *OpenAIGatewayService) openAIWSFallbackCooldown() time.Duration {
	if s == nil || s.cfg == nil {
		return 30 * time.Second
	}
	seconds := s.cfg.Gateway.OpenAIWS.FallbackCooldownSeconds
	if seconds <= 0 {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func (s *OpenAIGatewayService) isOpenAIWSFallbackCooling(accountID int64) bool {
	if s == nil || accountID <= 0 {
		return false
	}
	cooldown := s.openAIWSFallbackCooldown()
	if cooldown <= 0 {
		return false
	}
	rawUntil, ok := s.openaiWSFallbackUntil.Load(accountID)
	if !ok || rawUntil == nil {
		return false
	}
	until, ok := rawUntil.(time.Time)
	if !ok || until.IsZero() {
		s.openaiWSFallbackUntil.Delete(accountID)
		return false
	}
	if time.Now().Before(until) {
		return true
	}
	s.openaiWSFallbackUntil.Delete(accountID)
	return false
}

func (s *OpenAIGatewayService) markOpenAIWSFallbackCooling(accountID int64, _ string) {
	if s == nil || accountID <= 0 {
		return
	}
	cooldown := s.openAIWSFallbackCooldown()
	if cooldown <= 0 {
		return
	}
	s.openaiWSFallbackUntil.Store(accountID, time.Now().Add(cooldown))
}

func (s *OpenAIGatewayService) clearOpenAIWSFallbackCooling(accountID int64) {
	if s == nil || accountID <= 0 {
		return
	}
	s.openaiWSFallbackUntil.Delete(accountID)
}
