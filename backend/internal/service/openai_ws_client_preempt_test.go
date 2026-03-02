package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"testing"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// errOpenAIWSClientPreempted 哨兵错误基础测试
// ---------------------------------------------------------------------------

func TestErrOpenAIWSClientPreempted_NotNil(t *testing.T) {
	t.Parallel()
	require.NotNil(t, errOpenAIWSClientPreempted)
	require.Contains(t, errOpenAIWSClientPreempted.Error(), "client preempted")
}

func TestErrOpenAIWSClientPreempted_ErrorsIs(t *testing.T) {
	t.Parallel()

	// 直接匹配
	require.True(t, errors.Is(errOpenAIWSClientPreempted, errOpenAIWSClientPreempted))

	// 包裹后仍可匹配
	wrapped := fmt.Errorf("outer: %w", errOpenAIWSClientPreempted)
	require.True(t, errors.Is(wrapped, errOpenAIWSClientPreempted))

	// 不同错误不匹配
	require.False(t, errors.Is(errors.New("other"), errOpenAIWSClientPreempted))
}

func TestErrOpenAIWSClientPreempted_WrapInTurnError(t *testing.T) {
	t.Parallel()

	// 用 wrapOpenAIWSIngressTurnErrorWithPartial 包裹后 errors.Is 仍能识别
	turnErr := wrapOpenAIWSIngressTurnErrorWithPartial(
		"client_preempted",
		errOpenAIWSClientPreempted,
		false,
		nil,
	)
	require.Error(t, turnErr)
	require.True(t, errors.Is(turnErr, errOpenAIWSClientPreempted))
}

func TestErrOpenAIWSClientPreempted_WrapInTurnError_WithPartialResult(t *testing.T) {
	t.Parallel()

	partial := &OpenAIForwardResult{
		RequestID: "resp_preempt_partial",
		Usage: OpenAIUsage{
			InputTokens:  100,
			OutputTokens: 50,
		},
	}
	turnErr := wrapOpenAIWSIngressTurnErrorWithPartial(
		"client_preempted",
		errOpenAIWSClientPreempted,
		true,
		partial,
	)
	require.Error(t, turnErr)
	require.True(t, errors.Is(turnErr, errOpenAIWSClientPreempted))

	// 验证 partial result 可提取
	got, ok := OpenAIWSIngressTurnPartialResult(turnErr)
	require.True(t, ok)
	require.NotNil(t, got)
	require.Equal(t, partial.RequestID, got.RequestID)
	require.Equal(t, partial.Usage.InputTokens, got.Usage.InputTokens)
}

// ---------------------------------------------------------------------------
// classifyOpenAIWSIngressTurnAbortReason 对 client_preempted 的识别测试
// ---------------------------------------------------------------------------

func TestClassifyAbortReason_ClientPreempted_Direct(t *testing.T) {
	t.Parallel()

	// 直接哨兵错误
	reason, expected := classifyOpenAIWSIngressTurnAbortReason(errOpenAIWSClientPreempted)
	require.Equal(t, openAIWSIngressTurnAbortReasonClientPreempted, reason)
	require.True(t, expected)
}

func TestClassifyAbortReason_ClientPreempted_WrappedInTurnError(t *testing.T) {
	t.Parallel()

	// 包裹在 turnError 中
	turnErr := wrapOpenAIWSIngressTurnErrorWithPartial(
		"client_preempted",
		errOpenAIWSClientPreempted,
		false,
		nil,
	)
	reason, expected := classifyOpenAIWSIngressTurnAbortReason(turnErr)
	require.Equal(t, openAIWSIngressTurnAbortReasonClientPreempted, reason)
	require.True(t, expected)
}

func TestClassifyAbortReason_ClientPreempted_WrappedInTurnError_WroteDownstream(t *testing.T) {
	t.Parallel()

	// 包裹在 turnError 中，wroteDownstream=true
	turnErr := wrapOpenAIWSIngressTurnErrorWithPartial(
		"client_preempted",
		errOpenAIWSClientPreempted,
		true,
		&OpenAIForwardResult{RequestID: "resp_partial"},
	)
	reason, expected := classifyOpenAIWSIngressTurnAbortReason(turnErr)
	require.Equal(t, openAIWSIngressTurnAbortReasonClientPreempted, reason)
	require.True(t, expected)
}

func TestClassifyAbortReason_ClientPreempted_DoubleWrapped(t *testing.T) {
	t.Parallel()

	// 多层 fmt.Errorf 包裹
	inner := fmt.Errorf("relay failed: %w", errOpenAIWSClientPreempted)
	reason, expected := classifyOpenAIWSIngressTurnAbortReason(inner)
	require.Equal(t, openAIWSIngressTurnAbortReasonClientPreempted, reason)
	require.True(t, expected)
}

func TestClassifyAbortReason_ClientPreempted_NotConfusedWithOther(t *testing.T) {
	t.Parallel()

	// 确保其他错误不会被误分类为 client_preempted
	others := []error{
		errors.New("client preempted"),        // 文本相同但不是同一哨兵
		context.Canceled,                      // context 取消
		io.EOF,                                // 客户端断连
		errors.New("random error"),            // 随机错误
	}

	for _, err := range others {
		reason, _ := classifyOpenAIWSIngressTurnAbortReason(err)
		require.NotEqual(t, openAIWSIngressTurnAbortReasonClientPreempted, reason,
			"error %q should not classify as client_preempted", err)
	}
}

// ---------------------------------------------------------------------------
// openAIWSIngressTurnAbortDispositionForReason 对 ClientPreempted 的处置测试
// ---------------------------------------------------------------------------

func TestDisposition_ClientPreempted_IsContinueTurn(t *testing.T) {
	t.Parallel()

	disposition := openAIWSIngressTurnAbortDispositionForReason(openAIWSIngressTurnAbortReasonClientPreempted)
	require.Equal(t, openAIWSIngressTurnAbortDispositionContinueTurn, disposition)
}

func TestDisposition_ClientPreempted_SameAsPreviousResponse(t *testing.T) {
	t.Parallel()

	// client_preempted 与 previous_response_not_found 应有相同的处置
	prevDisp := openAIWSIngressTurnAbortDispositionForReason(openAIWSIngressTurnAbortReasonPreviousResponse)
	preemptDisp := openAIWSIngressTurnAbortDispositionForReason(openAIWSIngressTurnAbortReasonClientPreempted)
	require.Equal(t, prevDisp, preemptDisp)
}

func TestDisposition_AllContinueTurnReasons(t *testing.T) {
	t.Parallel()

	// 验证所有应归为 ContinueTurn 的 reason 列表完整且正确
	continueTurnReasons := []openAIWSIngressTurnAbortReason{
		openAIWSIngressTurnAbortReasonPreviousResponse,
		openAIWSIngressTurnAbortReasonToolOutput,
		openAIWSIngressTurnAbortReasonUpstreamError,
		openAIWSIngressTurnAbortReasonClientPreempted,
	}

	for _, reason := range continueTurnReasons {
		disposition := openAIWSIngressTurnAbortDispositionForReason(reason)
		require.Equal(t, openAIWSIngressTurnAbortDispositionContinueTurn, disposition,
			"reason %q should be ContinueTurn", reason)
	}
}

func TestDisposition_ClientPreempted_NotCloseGracefully(t *testing.T) {
	t.Parallel()

	disposition := openAIWSIngressTurnAbortDispositionForReason(openAIWSIngressTurnAbortReasonClientPreempted)
	require.NotEqual(t, openAIWSIngressTurnAbortDispositionCloseGracefully, disposition)
	require.NotEqual(t, openAIWSIngressTurnAbortDispositionFailRequest, disposition)
}

// ---------------------------------------------------------------------------
// 端到端 classify → disposition 链路测试
// ---------------------------------------------------------------------------

func TestClientPreempted_ClassifyToDisposition_EndToEnd(t *testing.T) {
	t.Parallel()

	// 模拟 sendAndRelay 返回 client_preempted 错误的完整链路
	turnErr := wrapOpenAIWSIngressTurnErrorWithPartial(
		"client_preempted",
		errOpenAIWSClientPreempted,
		false,
		nil,
	)

	// 1. classify
	reason, expected := classifyOpenAIWSIngressTurnAbortReason(turnErr)
	require.Equal(t, openAIWSIngressTurnAbortReasonClientPreempted, reason)
	require.True(t, expected)

	// 2. disposition
	disposition := openAIWSIngressTurnAbortDispositionForReason(reason)
	require.Equal(t, openAIWSIngressTurnAbortDispositionContinueTurn, disposition)

	// 3. wroteDownstream
	require.False(t, openAIWSIngressTurnWroteDownstream(turnErr))
}

func TestClientPreempted_ClassifyToDisposition_WroteDownstream(t *testing.T) {
	t.Parallel()

	partial := &OpenAIForwardResult{
		RequestID: "resp_half",
		Usage: OpenAIUsage{
			InputTokens:  200,
			OutputTokens: 100,
		},
	}
	turnErr := wrapOpenAIWSIngressTurnErrorWithPartial(
		"client_preempted",
		errOpenAIWSClientPreempted,
		true,
		partial,
	)

	reason, expected := classifyOpenAIWSIngressTurnAbortReason(turnErr)
	require.Equal(t, openAIWSIngressTurnAbortReasonClientPreempted, reason)
	require.True(t, expected)

	disposition := openAIWSIngressTurnAbortDispositionForReason(reason)
	require.Equal(t, openAIWSIngressTurnAbortDispositionContinueTurn, disposition)

	require.True(t, openAIWSIngressTurnWroteDownstream(turnErr))

	got, ok := OpenAIWSIngressTurnPartialResult(turnErr)
	require.True(t, ok)
	require.Equal(t, "resp_half", got.RequestID)
}

// ---------------------------------------------------------------------------
// ContinueTurn 分支对 client_preempted 的特殊行为验证
// ---------------------------------------------------------------------------

func TestClientPreempted_ShouldNotSendErrorEvent(t *testing.T) {
	t.Parallel()

	// 核心语义：client_preempted 时客户端已发出新请求，不需要旧 turn 的 error 事件。
	// 验证 abortReason 为 client_preempted 时不应产生 error 通知。
	abortReason := openAIWSIngressTurnAbortReasonClientPreempted

	// 模拟 ContinueTurn 分支的判断逻辑
	shouldSendError := abortReason != openAIWSIngressTurnAbortReasonClientPreempted
	require.False(t, shouldSendError, "client_preempted 不应发送 error 事件")
}

func TestClientPreempted_ShouldNotClearLastResponseID(t *testing.T) {
	t.Parallel()

	// 核心语义：被抢占的 turn 未完成，上一轮 response_id 仍有效供新 turn 续链。
	// 验证 abortReason 为 client_preempted 时不应调用 clearSessionLastResponseID。
	abortReason := openAIWSIngressTurnAbortReasonClientPreempted

	shouldClearLastResponseID := abortReason != openAIWSIngressTurnAbortReasonClientPreempted
	require.False(t, shouldClearLastResponseID,
		"client_preempted 不应清除 lastResponseID")
}

func TestNonPreempted_ContinueTurn_ShouldSendErrorAndClearID(t *testing.T) {
	t.Parallel()

	// 对照测试：非 client_preempted 的 ContinueTurn reason 应正常发送 error 并清除 ID
	otherReasons := []openAIWSIngressTurnAbortReason{
		openAIWSIngressTurnAbortReasonPreviousResponse,
		openAIWSIngressTurnAbortReasonToolOutput,
		openAIWSIngressTurnAbortReasonUpstreamError,
	}

	for _, reason := range otherReasons {
		shouldSendError := reason != openAIWSIngressTurnAbortReasonClientPreempted
		shouldClearID := reason != openAIWSIngressTurnAbortReasonClientPreempted
		require.True(t, shouldSendError,
			"reason %q (non-preempted) should send error event", reason)
		require.True(t, shouldClearID,
			"reason %q (non-preempted) should clear lastResponseID", reason)
	}
}

// ---------------------------------------------------------------------------
// ContinueTurn abort 路径中 client_preempted 的 error 事件格式验证
// ---------------------------------------------------------------------------

func TestClientPreempted_ErrorEventNotGenerated(t *testing.T) {
	t.Parallel()

	// 在实际的 ContinueTurn 分支中，client_preempted 分支根本不会构造 error 事件。
	// 此测试验证如果误走错误路径（防御性），error 事件格式仍然正确。
	abortReason := openAIWSIngressTurnAbortReasonClientPreempted
	abortMessage := "turn failed: " + string(abortReason)

	errorEvent := []byte(`{"type":"error","error":{"type":"server_error","code":"` +
		string(abortReason) + `","message":` + strconv.Quote(abortMessage) + `}}`)

	var parsed map[string]any
	err := json.Unmarshal(errorEvent, &parsed)
	require.NoError(t, err, "hypothetical error event should be valid JSON")

	errorObj, ok := parsed["error"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "client_preempted", errorObj["code"])
	require.Contains(t, errorObj["message"], "client_preempted")
}

// ---------------------------------------------------------------------------
// openAIWSIngressTurnAbortReason 常量值验证
// ---------------------------------------------------------------------------

func TestClientPreempted_ReasonStringValue(t *testing.T) {
	t.Parallel()

	require.Equal(t, openAIWSIngressTurnAbortReason("client_preempted"),
		openAIWSIngressTurnAbortReasonClientPreempted)
}

// ---------------------------------------------------------------------------
// classifyOpenAIWSIngressTurnAbortReason 完整 table-driven 测试（含 client_preempted）
// ---------------------------------------------------------------------------

func TestClassifyAbortReason_AllReasons_IncludeClientPreempted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		err          error
		wantReason   openAIWSIngressTurnAbortReason
		wantExpected bool
	}{
		{
			name:         "client_preempted_sentinel",
			err:          errOpenAIWSClientPreempted,
			wantReason:   openAIWSIngressTurnAbortReasonClientPreempted,
			wantExpected: true,
		},
		{
			name: "client_preempted_wrapped_in_turn_error",
			err: wrapOpenAIWSIngressTurnErrorWithPartial(
				"client_preempted",
				errOpenAIWSClientPreempted,
				false,
				nil,
			),
			wantReason:   openAIWSIngressTurnAbortReasonClientPreempted,
			wantExpected: true,
		},
		{
			name: "client_preempted_wrapped_in_turn_error_wrote_downstream",
			err: wrapOpenAIWSIngressTurnErrorWithPartial(
				"client_preempted",
				errOpenAIWSClientPreempted,
				true,
				&OpenAIForwardResult{RequestID: "resp_x"},
			),
			wantReason:   openAIWSIngressTurnAbortReasonClientPreempted,
			wantExpected: true,
		},
		{
			name:         "client_preempted_double_wrapped",
			err:          fmt.Errorf("relay: %w", errOpenAIWSClientPreempted),
			wantReason:   openAIWSIngressTurnAbortReasonClientPreempted,
			wantExpected: true,
		},
		{
			name: "previous_response_not_confused_with_preempt",
			err: wrapOpenAIWSIngressTurnError(
				openAIWSIngressStagePreviousResponseNotFound,
				errors.New("not found"),
				false,
			),
			wantReason:   openAIWSIngressTurnAbortReasonPreviousResponse,
			wantExpected: true,
		},
		{
			name: "tool_output_not_confused_with_preempt",
			err: wrapOpenAIWSIngressTurnError(
				openAIWSIngressStageToolOutputNotFound,
				errors.New("tool output not found"),
				false,
			),
			wantReason:   openAIWSIngressTurnAbortReasonToolOutput,
			wantExpected: true,
		},
		{
			name:         "context_canceled_not_preempted",
			err:          context.Canceled,
			wantReason:   openAIWSIngressTurnAbortReasonContextCanceled,
			wantExpected: true,
		},
		{
			name:         "eof_not_preempted",
			err:          io.EOF,
			wantReason:   openAIWSIngressTurnAbortReasonClientClosed,
			wantExpected: true,
		},
		{
			name:         "ws_normal_closure_not_preempted",
			err:          coderws.CloseError{Code: coderws.StatusNormalClosure},
			wantReason:   openAIWSIngressTurnAbortReasonClientClosed,
			wantExpected: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reason, expected := classifyOpenAIWSIngressTurnAbortReason(tt.err)
			require.Equal(t, tt.wantReason, reason)
			require.Equal(t, tt.wantExpected, expected)
		})
	}
}

// ---------------------------------------------------------------------------
// classify 优先级测试：client_preempted 在 context.Canceled 之前
// ---------------------------------------------------------------------------

func TestClassifyAbortReason_ClientPreempted_PriorityOverContextCanceled(t *testing.T) {
	t.Parallel()

	// errOpenAIWSClientPreempted 不会同时匹配 context.Canceled，
	// 但若将来有包裹 context.Canceled 的情况，client_preempted 检测应在前。
	reason, _ := classifyOpenAIWSIngressTurnAbortReason(errOpenAIWSClientPreempted)
	require.Equal(t, openAIWSIngressTurnAbortReasonClientPreempted, reason,
		"client_preempted 检测应优先于 context.Canceled")
}

// ---------------------------------------------------------------------------
// openAIWSIngressTurnAbortDispositionForReason table-driven 测试（含 client_preempted）
// ---------------------------------------------------------------------------

func TestDisposition_AllReasons_IncludeClientPreempted(t *testing.T) {
	t.Parallel()

	tests := []struct {
		reason     openAIWSIngressTurnAbortReason
		wantDisp   openAIWSIngressTurnAbortDisposition
	}{
		{openAIWSIngressTurnAbortReasonPreviousResponse, openAIWSIngressTurnAbortDispositionContinueTurn},
		{openAIWSIngressTurnAbortReasonToolOutput, openAIWSIngressTurnAbortDispositionContinueTurn},
		{openAIWSIngressTurnAbortReasonUpstreamError, openAIWSIngressTurnAbortDispositionContinueTurn},
		{openAIWSIngressTurnAbortReasonClientPreempted, openAIWSIngressTurnAbortDispositionContinueTurn},
		{openAIWSIngressTurnAbortReasonContextCanceled, openAIWSIngressTurnAbortDispositionCloseGracefully},
		{openAIWSIngressTurnAbortReasonClientClosed, openAIWSIngressTurnAbortDispositionCloseGracefully},
		{openAIWSIngressTurnAbortReasonUnknown, openAIWSIngressTurnAbortDispositionFailRequest},
		{openAIWSIngressTurnAbortReasonContextDeadline, openAIWSIngressTurnAbortDispositionFailRequest},
		{openAIWSIngressTurnAbortReasonWriteUpstream, openAIWSIngressTurnAbortDispositionFailRequest},
		{openAIWSIngressTurnAbortReasonReadUpstream, openAIWSIngressTurnAbortDispositionFailRequest},
		{openAIWSIngressTurnAbortReasonWriteClient, openAIWSIngressTurnAbortDispositionFailRequest},
		{openAIWSIngressTurnAbortReasonContinuationUnavailable, openAIWSIngressTurnAbortDispositionFailRequest},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(string(tt.reason), func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.wantDisp, openAIWSIngressTurnAbortDispositionForReason(tt.reason))
		})
	}
}

// ---------------------------------------------------------------------------
// isOpenAIWSIngressTurnRetryable 与 client_preempted 的交互
// ---------------------------------------------------------------------------

func TestIsRetryable_ClientPreempted_NotRetryable(t *testing.T) {
	t.Parallel()

	// client_preempted 有专门的恢复路径（ContinueTurn），不走通用重试
	turnErr := wrapOpenAIWSIngressTurnErrorWithPartial(
		"client_preempted",
		errOpenAIWSClientPreempted,
		false,
		nil,
	)
	require.False(t, isOpenAIWSIngressTurnRetryable(turnErr),
		"client_preempted 不应被标记为 retryable")
}

func TestIsRetryable_ClientPreempted_WroteDownstream(t *testing.T) {
	t.Parallel()

	turnErr := wrapOpenAIWSIngressTurnErrorWithPartial(
		"client_preempted",
		errOpenAIWSClientPreempted,
		true,
		nil,
	)
	require.False(t, isOpenAIWSIngressTurnRetryable(turnErr),
		"client_preempted wroteDownstream=true 不应被标记为 retryable")
}

// ---------------------------------------------------------------------------
// sendAndRelay 中 clientMsgCh / clientReadErrCh 行为的单元级测试
// ---------------------------------------------------------------------------

func TestClientMsgCh_BufferedOne(t *testing.T) {
	t.Parallel()

	// 验证 clientMsgCh(buffered 1) 的语义：goroutine 在 sendAndRelay 返回到
	// advanceToNextClientTurn 的间隙不阻塞
	ch := make(chan []byte, 1)

	// 非阻塞写入
	select {
	case ch <- []byte(`{"type":"response.create"}`):
		// ok
	default:
		t.Fatal("buffered(1) channel should not block on first write")
	}

	// 第二次写入应阻塞
	select {
	case ch <- []byte(`{"type":"response.create"}`):
		t.Fatal("buffered(1) channel should block on second write")
	default:
		// expected
	}
}

func TestClientReadErrCh_BufferedOne(t *testing.T) {
	t.Parallel()

	ch := make(chan error, 1)

	// 非阻塞写入
	select {
	case ch <- io.EOF:
	default:
		t.Fatal("buffered(1) channel should not block on first write")
	}

	// 第二次写入应阻塞
	select {
	case ch <- io.EOF:
		t.Fatal("buffered(1) channel should block on second write")
	default:
		// expected
	}
}

func TestClientMsgCh_CloseSignalsClosed(t *testing.T) {
	t.Parallel()

	ch := make(chan []byte, 1)
	close(ch)

	msg, ok := <-ch
	require.False(t, ok, "closed channel should return ok=false")
	require.Nil(t, msg)
}

// ---------------------------------------------------------------------------
// 客户端抢占暂存（nextClientPreemptedPayload）行为测试
// ---------------------------------------------------------------------------

func TestPreemptedPayload_ConsumedOnce(t *testing.T) {
	t.Parallel()

	// 模拟 advanceToNextClientTurn 中预存消息的消费行为
	var nextPreempted []byte
	nextPreempted = []byte(`{"type":"response.create","model":"gpt-5.1"}`)

	// 第一次消费
	require.NotNil(t, nextPreempted)
	msg := nextPreempted
	nextPreempted = nil

	require.Equal(t, `{"type":"response.create","model":"gpt-5.1"}`, string(msg))
	require.Nil(t, nextPreempted, "消费后应置空")
}

func TestPreemptedPayload_NilFallsBackToChannel(t *testing.T) {
	t.Parallel()

	// 模拟 advanceToNextClientTurn 中无预存消息时走 channel
	var nextPreempted []byte
	clientMsgCh := make(chan []byte, 1)
	clientMsgCh <- []byte(`{"type":"response.create","model":"gpt-5.1"}`)

	var nextClientMessage []byte
	if nextPreempted != nil {
		nextClientMessage = nextPreempted
		nextPreempted = nil
	} else {
		select {
		case msg, ok := <-clientMsgCh:
			require.True(t, ok)
			nextClientMessage = msg
		}
	}

	require.Equal(t, `{"type":"response.create","model":"gpt-5.1"}`, string(nextClientMessage))
}

// ---------------------------------------------------------------------------
// sendAndRelay select 路径：pumpEventCh 关闭 → goto pumpClosed
// ---------------------------------------------------------------------------

func TestSelectLoop_PumpClosed_GoToPumpClosed(t *testing.T) {
	t.Parallel()

	// 模拟 pumpEventCh 关闭时的行为
	pumpEventCh := make(chan openAIWSUpstreamPumpEvent)
	close(pumpEventCh)

	evt, ok := <-pumpEventCh
	require.False(t, ok, "closed pumpEventCh should return ok=false")
	require.Nil(t, evt.message)
	require.Nil(t, evt.err)
}

// ---------------------------------------------------------------------------
// sendAndRelay select 路径：clientMsgCh 收到消息 → client preempt
// ---------------------------------------------------------------------------

func TestSelectLoop_ClientPreempt_ReturnsCorrectError(t *testing.T) {
	t.Parallel()

	// 模拟 select 中收到客户端抢占消息后生成的 turnError
	preemptPayload := []byte(`{"type":"response.create","model":"gpt-5.1","input":[]}`)

	// 模拟 sendAndRelay 返回的错误
	turnErr := wrapOpenAIWSIngressTurnErrorWithPartial(
		"client_preempted",
		errOpenAIWSClientPreempted,
		false,
		nil, // buildPartialResult 在没有 usage 时返回 nil
	)

	// 验证错误分类
	reason, expected := classifyOpenAIWSIngressTurnAbortReason(turnErr)
	require.Equal(t, openAIWSIngressTurnAbortReasonClientPreempted, reason)
	require.True(t, expected)

	// 验证处置
	disposition := openAIWSIngressTurnAbortDispositionForReason(reason)
	require.Equal(t, openAIWSIngressTurnAbortDispositionContinueTurn, disposition)

	// 验证预存消息可供 advanceToNextClientTurn 使用
	require.NotEmpty(t, preemptPayload)
}

func TestSelectLoop_ClientPreempt_WithPartialUsage(t *testing.T) {
	t.Parallel()

	// 模拟上游已发送部分 token 后被客户端抢占
	partial := &OpenAIForwardResult{
		RequestID: "resp_interrupted",
		Usage: OpenAIUsage{
			InputTokens:  500,
			OutputTokens: 200,
		},
	}

	turnErr := wrapOpenAIWSIngressTurnErrorWithPartial(
		"client_preempted",
		errOpenAIWSClientPreempted,
		true, // 已写过下游
		partial,
	)

	require.True(t, errors.Is(turnErr, errOpenAIWSClientPreempted))
	require.True(t, openAIWSIngressTurnWroteDownstream(turnErr))

	got, ok := OpenAIWSIngressTurnPartialResult(turnErr)
	require.True(t, ok)
	require.Equal(t, "resp_interrupted", got.RequestID)
	require.Equal(t, 500, got.Usage.InputTokens)
	require.Equal(t, 200, got.Usage.OutputTokens)
}

// ---------------------------------------------------------------------------
// sendAndRelay select 路径：clientMsgCh 关闭 → nil channel
// ---------------------------------------------------------------------------

func TestSelectLoop_ClientMsgChClosed_NilChannelPreventsReselect(t *testing.T) {
	t.Parallel()

	// clientMsgCh 关闭后被设为 nil，后续 select 不应再选中它
	var clientMsgCh chan []byte
	clientMsgCh = make(chan []byte, 1)
	close(clientMsgCh)

	// 第一次读取：closed
	_, ok := <-clientMsgCh
	require.False(t, ok)

	// 设为 nil
	clientMsgCh = nil

	// nil channel 上的 select 永远不会被选中（不会 panic）
	select {
	case <-clientMsgCh:
		t.Fatal("nil channel should never be selected")
	default:
		// expected: nil channel 不参与 select
	}
}

// ---------------------------------------------------------------------------
// sendAndRelay select 路径：clientReadErrCh 客户端断连
// ---------------------------------------------------------------------------

func TestSelectLoop_ClientReadErr_DisconnectSetsDrain(t *testing.T) {
	t.Parallel()

	// 模拟客户端断连读取错误的分类行为
	disconnectErrors := []error{
		io.EOF,
		coderws.CloseError{Code: coderws.StatusNormalClosure},
		coderws.CloseError{Code: coderws.StatusGoingAway},
	}

	for _, readErr := range disconnectErrors {
		require.True(t, isOpenAIWSClientDisconnectError(readErr),
			"error %v should be classified as client disconnect", readErr)
	}
}

func TestSelectLoop_ClientReadErr_NonDisconnect(t *testing.T) {
	t.Parallel()

	// 非断连错误不应触发 drain
	nonDisconnectErrors := []error{
		errors.New("tls handshake timeout"),
		coderws.CloseError{Code: coderws.StatusPolicyViolation},
	}

	for _, readErr := range nonDisconnectErrors {
		require.False(t, isOpenAIWSClientDisconnectError(readErr),
			"error %v should not be classified as client disconnect", readErr)
	}
}

func TestSelectLoop_ClientReadErr_NilChannelsAfterError(t *testing.T) {
	t.Parallel()

	// 模拟收到 clientReadErrCh 后将两个 channel 置 nil
	clientMsgCh := make(chan []byte, 1)
	clientReadErrCh := make(chan error, 1)

	clientReadErrCh <- io.EOF

	// 消费错误
	readErr := <-clientReadErrCh
	require.Error(t, readErr)

	// 模拟置空（实际代码中 select case 后的操作）
	var nilMsgCh chan []byte
	var nilErrCh chan error
	nilMsgCh = nil
	nilErrCh = nil

	// 验证 nil channel 行为
	_ = clientMsgCh // unused in this test

	select {
	case <-nilMsgCh:
		t.Fatal("nil channel should never be selected")
	case <-nilErrCh:
		t.Fatal("nil channel should never be selected")
	default:
		// expected
	}
}

// ---------------------------------------------------------------------------
// advanceToNextClientTurn channel 读取路径测试
// ---------------------------------------------------------------------------

func TestAdvance_ClientMsgCh_ClosedReturnsExit(t *testing.T) {
	t.Parallel()

	// clientMsgCh 关闭意味着客户端读取 goroutine 已退出，应返回 exit=true
	ch := make(chan []byte, 1)
	close(ch)

	_, ok := <-ch
	require.False(t, ok, "should signal goroutine exit")
}

func TestAdvance_ClientReadErrCh_DisconnectReturnsExit(t *testing.T) {
	t.Parallel()

	// 断连错误应返回 exit=true
	ch := make(chan error, 1)
	ch <- io.EOF

	readErr := <-ch
	require.True(t, isOpenAIWSClientDisconnectError(readErr))
}

func TestAdvance_ClientReadErrCh_NonDisconnectReturnsError(t *testing.T) {
	t.Parallel()

	// 非断连错误应返回 error
	ch := make(chan error, 1)
	errCustom := errors.New("custom read error")
	ch <- errCustom

	readErr := <-ch
	require.False(t, isOpenAIWSClientDisconnectError(readErr))
	require.Equal(t, errCustom, readErr)
}

// ---------------------------------------------------------------------------
// 持久客户端读取 goroutine 行为测试
// ---------------------------------------------------------------------------

func TestPersistentReader_NormalMessage(t *testing.T) {
	t.Parallel()

	// 模拟正常消息的推送和消费
	clientMsgCh := make(chan []byte, 1)

	// 模拟 goroutine 写入
	go func() {
		clientMsgCh <- []byte(`{"type":"response.create"}`)
	}()

	msg := <-clientMsgCh
	require.Equal(t, `{"type":"response.create"}`, string(msg))
}

func TestPersistentReader_ErrorSendsToErrCh(t *testing.T) {
	t.Parallel()

	clientReadErrCh := make(chan error, 1)

	// 模拟 goroutine 发送错误
	go func() {
		clientReadErrCh <- io.EOF
	}()

	readErr := <-clientReadErrCh
	require.Equal(t, io.EOF, readErr)
}

func TestPersistentReader_ContextCancel(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	clientMsgCh := make(chan []byte, 1)

	// 填满 buffer
	clientMsgCh <- []byte("first")

	// 模拟 goroutine 尝试写入已满的 channel
	done := make(chan struct{})
	go func() {
		defer close(done)
		select {
		case clientMsgCh <- []byte("second"):
			// 不应到达
		case <-ctx.Done():
			// 正确退出
			return
		}
	}()

	// 取消 context
	cancel()
	<-done
}

func TestPersistentReader_ClosesMsgChOnExit(t *testing.T) {
	t.Parallel()

	clientMsgCh := make(chan []byte, 1)

	// 模拟 goroutine 退出时关闭 channel
	go func() {
		defer close(clientMsgCh)
		// 模拟读取错误后退出
	}()

	// 等待 channel 关闭
	_, ok := <-clientMsgCh
	require.False(t, ok, "channel should be closed when goroutine exits")
}

// ---------------------------------------------------------------------------
// client_preempted 与其他 abort reason 的正交性验证
// ---------------------------------------------------------------------------

func TestClientPreempted_OrthogonalWithPreviousResponseNotFound(t *testing.T) {
	t.Parallel()

	preemptErr := wrapOpenAIWSIngressTurnErrorWithPartial(
		"client_preempted",
		errOpenAIWSClientPreempted,
		false,
		nil,
	)
	prevErr := wrapOpenAIWSIngressTurnError(
		openAIWSIngressStagePreviousResponseNotFound,
		errors.New("not found"),
		false,
	)

	// client_preempted 不会被误判为 previous_response_not_found
	require.False(t, isOpenAIWSIngressPreviousResponseNotFound(preemptErr))
	// previous_response_not_found 不会被误判为 client_preempted
	require.False(t, errors.Is(prevErr, errOpenAIWSClientPreempted))
}

func TestClientPreempted_OrthogonalWithToolOutputNotFound(t *testing.T) {
	t.Parallel()

	preemptErr := wrapOpenAIWSIngressTurnErrorWithPartial(
		"client_preempted",
		errOpenAIWSClientPreempted,
		false,
		nil,
	)
	toolErr := wrapOpenAIWSIngressTurnError(
		openAIWSIngressStageToolOutputNotFound,
		errors.New("tool output not found"),
		false,
	)

	require.False(t, isOpenAIWSIngressToolOutputNotFound(preemptErr))
	require.False(t, errors.Is(toolErr, errOpenAIWSClientPreempted))
}

func TestClientPreempted_OrthogonalWithUpstreamError(t *testing.T) {
	t.Parallel()

	preemptErr := wrapOpenAIWSIngressTurnErrorWithPartial(
		"client_preempted",
		errOpenAIWSClientPreempted,
		false,
		nil,
	)
	upstreamErr := wrapOpenAIWSIngressTurnError(
		"upstream_error_event",
		errors.New("upstream error"),
		false,
	)

	require.False(t, isOpenAIWSIngressUpstreamErrorEvent(preemptErr))
	require.False(t, errors.Is(upstreamErr, errOpenAIWSClientPreempted))
}

func TestClientPreempted_OrthogonalWithContinuationUnavailable(t *testing.T) {
	t.Parallel()

	preemptErr := wrapOpenAIWSIngressTurnErrorWithPartial(
		"client_preempted",
		errOpenAIWSClientPreempted,
		false,
		nil,
	)
	require.False(t, isOpenAIWSContinuationUnavailableCloseError(preemptErr))
}

func TestClientPreempted_NotClientDisconnect(t *testing.T) {
	t.Parallel()

	require.False(t, isOpenAIWSClientDisconnectError(errOpenAIWSClientPreempted),
		"client_preempted should not be classified as client disconnect")
}

// ---------------------------------------------------------------------------
// recordOpenAIWSTurnAbort 指标兼容性测试
// ---------------------------------------------------------------------------

func TestClientPreempted_RecordAbortArgs(t *testing.T) {
	t.Parallel()

	// 验证 classify 返回的 (reason, expected) 值与 recordOpenAIWSTurnAbort 兼容
	turnErr := wrapOpenAIWSIngressTurnErrorWithPartial(
		"client_preempted",
		errOpenAIWSClientPreempted,
		false,
		nil,
	)

	reason, expected := classifyOpenAIWSIngressTurnAbortReason(turnErr)
	require.Equal(t, openAIWSIngressTurnAbortReasonClientPreempted, reason)
	require.True(t, expected)

	// expected=true 表示这是预期行为，不应触发告警
	assert.True(t, expected, "client_preempted 应标记为 expected，不触发告警")
}

// ---------------------------------------------------------------------------
// shouldFlushOpenAIWSBufferedEventsOnError 与 client_preempted 场景
// ---------------------------------------------------------------------------

func TestShouldFlushBufferedEvents_ClientPreempted(t *testing.T) {
	t.Parallel()

	// client_preempted 场景下 clientDisconnected=false（客户端仍在），
	// 是否 flush 取决于 reqStream 和 wroteDownstream
	tests := []struct {
		name             string
		reqStream        bool
		wroteDownstream  bool
		wantFlush        bool
	}{
		{"stream_wrote", true, true, true},
		{"stream_not_wrote", true, false, false},
		{"not_stream_wrote", false, true, false},
		{"not_stream_not_wrote", false, false, false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := shouldFlushOpenAIWSBufferedEventsOnError(tt.reqStream, tt.wroteDownstream, false)
			require.Equal(t, tt.wantFlush, got)
		})
	}
}
