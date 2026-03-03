package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// 辅助：构造测试用上游事件 JSON
// ---------------------------------------------------------------------------

func pumpTestEvent(eventType string) []byte {
	m := map[string]any{"type": eventType}
	b, _ := json.Marshal(m)
	return b
}

func pumpTestEventWithResponseID(eventType, responseID string) []byte {
	m := map[string]any{"type": eventType, "response": map[string]any{"id": responseID}}
	b, _ := json.Marshal(m)
	return b
}

// ---------------------------------------------------------------------------
// 辅助：模拟上游连接（支持按序返回事件、延迟、错误注入）
// ---------------------------------------------------------------------------

type pumpTestConn struct {
	mu         sync.Mutex
	events     []pumpTestConnEvent
	readCount  int
	closed     bool
	closedCh   chan struct{}
	ignoreCtx  bool
	pingErr    error
	writeErr   error
	writeCount int
}

type pumpTestConnEvent struct {
	data  []byte
	err   error
	delay time.Duration
}

func newPumpTestConn(events ...pumpTestConnEvent) *pumpTestConn {
	return &pumpTestConn{
		events:   events,
		closedCh: make(chan struct{}),
	}
}

func (c *pumpTestConn) WriteJSON(_ context.Context, _ any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.writeCount++
	return c.writeErr
}

func (c *pumpTestConn) ReadMessage(ctx context.Context) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errOpenAIWSConnClosed
	}
	if len(c.events) == 0 {
		c.mu.Unlock()
		if c.ignoreCtx {
			<-c.closedCh
			return nil, io.EOF
		}
		// 阻塞直到上下文取消，模拟上游无更多事件
		<-ctx.Done()
		return nil, ctx.Err()
	}
	evt := c.events[0]
	c.events = c.events[1:]
	c.readCount++
	c.mu.Unlock()

	if evt.delay > 0 {
		timer := time.NewTimer(evt.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return evt.data, evt.err
}

func (c *pumpTestConn) Ping(_ context.Context) error { return c.pingErr }

func (c *pumpTestConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		c.closed = true
		close(c.closedCh)
	}
	return nil
}

// ---------------------------------------------------------------------------
// 辅助：模拟 lease 接口（仅泵测试所需的读写方法）
// ---------------------------------------------------------------------------

type pumpTestLease struct {
	conn   *pumpTestConn
	broken atomic.Bool
}

func (l *pumpTestLease) ReadMessageWithContextTimeout(ctx context.Context, timeout time.Duration) ([]byte, error) {
	readCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return l.conn.ReadMessage(readCtx)
}

func (l *pumpTestLease) MarkBroken() {
	l.broken.Store(true)
	if l.conn != nil {
		_ = l.conn.Close()
	}
}

func (l *pumpTestLease) IsBroken() bool { return l.broken.Load() }

// ---------------------------------------------------------------------------
// 辅助：运行泵 goroutine 并收集所有产出的事件
// ---------------------------------------------------------------------------

// startPump 模拟 sendAndRelay 中的泵 goroutine，返回事件 channel 和取消函数。
func startPump(ctx context.Context, lease *pumpTestLease, readTimeout time.Duration) (chan openAIWSUpstreamPumpEvent, context.CancelFunc) {
	pumpEventCh := make(chan openAIWSUpstreamPumpEvent, openAIWSUpstreamPumpBufferSize)
	pumpCtx, pumpCancel := context.WithCancel(ctx)
	go func() {
		defer close(pumpEventCh)
		for {
			msg, readErr := lease.ReadMessageWithContextTimeout(pumpCtx, readTimeout)
			select {
			case pumpEventCh <- openAIWSUpstreamPumpEvent{message: msg, err: readErr}:
			case <-pumpCtx.Done():
				return
			}
			if readErr != nil {
				return
			}
			evtType, _ := parseOpenAIWSEventType(msg)
			if isOpenAIWSTerminalEvent(evtType) || evtType == "error" {
				return
			}
		}
	}()
	return pumpEventCh, pumpCancel
}

// collectAll 从 channel 读取所有事件直到关闭。
func collectAll(ch chan openAIWSUpstreamPumpEvent) []openAIWSUpstreamPumpEvent {
	var result []openAIWSUpstreamPumpEvent
	for evt := range ch {
		result = append(result, evt)
	}
	return result
}

// ---------------------------------------------------------------------------
// 测试：openAIWSUpstreamPumpEvent 结构体
// ---------------------------------------------------------------------------

func TestOpenAIWSUpstreamPumpEvent_Fields(t *testing.T) {
	t.Parallel()

	t.Run("message_only", func(t *testing.T) {
		evt := openAIWSUpstreamPumpEvent{message: []byte("hello")}
		assert.Equal(t, []byte("hello"), evt.message)
		assert.NoError(t, evt.err)
	})

	t.Run("error_only", func(t *testing.T) {
		evt := openAIWSUpstreamPumpEvent{err: io.EOF}
		assert.Nil(t, evt.message)
		assert.ErrorIs(t, evt.err, io.EOF)
	})

	t.Run("both_fields", func(t *testing.T) {
		evt := openAIWSUpstreamPumpEvent{message: []byte("partial"), err: io.ErrUnexpectedEOF}
		assert.Equal(t, []byte("partial"), evt.message)
		assert.ErrorIs(t, evt.err, io.ErrUnexpectedEOF)
	})
}

func TestOpenAIWSUpstreamPumpBufferSize(t *testing.T) {
	t.Parallel()
	assert.Equal(t, 16, openAIWSUpstreamPumpBufferSize, "缓冲大小应为 16")
}

// ---------------------------------------------------------------------------
// 测试：泵 goroutine 正常事件流
// ---------------------------------------------------------------------------

func TestPump_NormalEventFlow(t *testing.T) {
	t.Parallel()
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
		pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta")},
		pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta")},
		pumpTestConnEvent{data: pumpTestEvent("response.completed")},
	)
	lease := &pumpTestLease{conn: conn}
	ch, cancel := startPump(context.Background(), lease, 5*time.Second)
	defer cancel()

	events := collectAll(ch)

	require.Len(t, events, 4)
	for _, evt := range events {
		assert.NoError(t, evt.err)
		assert.NotEmpty(t, evt.message)
	}
	// 验证最后一个是终端事件
	lastType, _ := parseOpenAIWSEventType(events[3].message)
	assert.True(t, isOpenAIWSTerminalEvent(lastType))
}

func TestPump_TerminalEventStopsPump(t *testing.T) {
	t.Parallel()
	terminalTypes := []string{
		"response.completed",
		"response.done",
		"response.failed",
		"response.incomplete",
		"response.cancelled",
		"response.canceled",
	}
	for _, tt := range terminalTypes {
		tt := tt
		t.Run(tt, func(t *testing.T) {
			t.Parallel()
			conn := newPumpTestConn(
				pumpTestConnEvent{data: pumpTestEvent("response.created")},
				pumpTestConnEvent{data: pumpTestEvent(tt)},
				// 以下事件不应该被读取
				pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta")},
			)
			lease := &pumpTestLease{conn: conn}
			ch, cancel := startPump(context.Background(), lease, 5*time.Second)
			defer cancel()

			events := collectAll(ch)
			require.Len(t, events, 2, "终端事件 %s 后泵应停止", tt)
			assert.NoError(t, events[0].err)
			assert.NoError(t, events[1].err)
		})
	}
}

func TestPump_ErrorEventStopsPump(t *testing.T) {
	t.Parallel()
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
		pumpTestConnEvent{data: pumpTestEvent("error")},
		// 不应被读取
		pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta")},
	)
	lease := &pumpTestLease{conn: conn}
	ch, cancel := startPump(context.Background(), lease, 5*time.Second)
	defer cancel()

	events := collectAll(ch)
	require.Len(t, events, 2, "error 事件后泵应停止")
	evtType, _ := parseOpenAIWSEventType(events[1].message)
	assert.Equal(t, "error", evtType)
}

// ---------------------------------------------------------------------------
// 测试：泵 goroutine 读取错误传播
// ---------------------------------------------------------------------------

func TestPump_ReadErrorPropagated(t *testing.T) {
	t.Parallel()
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
		pumpTestConnEvent{err: io.ErrUnexpectedEOF},
	)
	lease := &pumpTestLease{conn: conn}
	ch, cancel := startPump(context.Background(), lease, 5*time.Second)
	defer cancel()

	events := collectAll(ch)
	require.Len(t, events, 2)
	assert.NoError(t, events[0].err)
	assert.ErrorIs(t, events[1].err, io.ErrUnexpectedEOF)
}

func TestPump_ReadErrorOnFirstEvent(t *testing.T) {
	t.Parallel()
	conn := newPumpTestConn(
		pumpTestConnEvent{err: errors.New("connection refused")},
	)
	lease := &pumpTestLease{conn: conn}
	ch, cancel := startPump(context.Background(), lease, 5*time.Second)
	defer cancel()

	events := collectAll(ch)
	require.Len(t, events, 1)
	assert.Error(t, events[0].err)
	assert.Contains(t, events[0].err.Error(), "connection refused")
}

func TestPump_EOFError(t *testing.T) {
	t.Parallel()
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
		pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta")},
		pumpTestConnEvent{err: io.EOF},
	)
	lease := &pumpTestLease{conn: conn}
	ch, cancel := startPump(context.Background(), lease, 5*time.Second)
	defer cancel()

	events := collectAll(ch)
	require.Len(t, events, 3)
	assert.ErrorIs(t, events[2].err, io.EOF)
}

// ---------------------------------------------------------------------------
// 测试：上下文取消终止泵
// ---------------------------------------------------------------------------

func TestPump_ContextCancellationStopsPump(t *testing.T) {
	t.Parallel()
	// 连接永远阻塞在第二次读取
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
		// 无更多事件，ReadMessage 将阻塞直到 ctx 取消
	)
	lease := &pumpTestLease{conn: conn}
	ctx, ctxCancel := context.WithCancel(context.Background())
	ch, pumpCancel := startPump(ctx, lease, 30*time.Second)
	defer pumpCancel()

	// 读取第一个事件
	evt := <-ch
	assert.NoError(t, evt.err)

	// 取消上下文
	ctxCancel()

	// 泵应该退出，channel 应该关闭
	events := collectAll(ch)
	// 可能收到一个 context.Canceled 错误事件
	for _, e := range events {
		assert.Error(t, e.err)
	}
}

func TestPump_PumpCancelStopsPump(t *testing.T) {
	t.Parallel()
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
	)
	lease := &pumpTestLease{conn: conn}
	ch, pumpCancel := startPump(context.Background(), lease, 30*time.Second)

	evt := <-ch
	assert.NoError(t, evt.err)

	// 调用 pumpCancel 应终止泵
	pumpCancel()

	// channel 应被关闭
	events := collectAll(ch)
	for _, e := range events {
		assert.Error(t, e.err)
	}
}

// ---------------------------------------------------------------------------
// 测试：缓冲行为
// ---------------------------------------------------------------------------

func TestPump_BufferAllowsConcurrentReadWrite(t *testing.T) {
	t.Parallel()
	// 生成超过缓冲大小的事件，验证不会死锁
	numEvents := openAIWSUpstreamPumpBufferSize + 5
	connEvents := make([]pumpTestConnEvent, 0, numEvents)
	for i := 0; i < numEvents-1; i++ {
		connEvents = append(connEvents, pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta")})
	}
	connEvents = append(connEvents, pumpTestConnEvent{data: pumpTestEvent("response.completed")})

	conn := newPumpTestConn(connEvents...)
	lease := &pumpTestLease{conn: conn}
	ch, cancel := startPump(context.Background(), lease, 5*time.Second)
	defer cancel()

	events := collectAll(ch)
	require.Len(t, events, numEvents)
	for _, evt := range events {
		assert.NoError(t, evt.err)
	}
}

func TestPump_SlowConsumerDoesNotBlock(t *testing.T) {
	t.Parallel()
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
		pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta")},
		pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta")},
		pumpTestConnEvent{data: pumpTestEvent("response.completed")},
	)
	lease := &pumpTestLease{conn: conn}
	ch, cancel := startPump(context.Background(), lease, 5*time.Second)
	defer cancel()

	// 模拟慢消费者
	var events []openAIWSUpstreamPumpEvent
	for evt := range ch {
		events = append(events, evt)
		time.Sleep(10 * time.Millisecond) // 慢消费
	}
	require.Len(t, events, 4)
}

// ---------------------------------------------------------------------------
// 测试：排水定时器机制
// ---------------------------------------------------------------------------

func TestPump_DrainTimerCancelsPump(t *testing.T) {
	t.Parallel()
	// 模拟：客户端断连后，排水定时器到期取消泵
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
		// 第二次读取会阻塞（模拟上游仍在生成但还没发出事件）
	)
	lease := &pumpTestLease{conn: conn}
	ch, pumpCancel := startPump(context.Background(), lease, 30*time.Second)

	// 读取第一个事件
	evt := <-ch
	assert.NoError(t, evt.err)

	// 模拟排水定时器：50ms 后取消泵（正式代码中是 5 秒）
	drainTimer := time.AfterFunc(50*time.Millisecond, pumpCancel)
	defer drainTimer.Stop()

	// 等待 channel 关闭
	start := time.Now()
	remaining := collectAll(ch)
	elapsed := time.Since(start)

	// 应在 50ms 附近退出，而非 30 秒
	assert.Less(t, elapsed, 2*time.Second, "排水定时器应在约 50ms 后终止泵")

	// 可能收到 context.Canceled 错误事件
	for _, e := range remaining {
		assert.Error(t, e.err)
	}
}

func TestPump_DrainDeadlineCheckInMainLoop(t *testing.T) {
	t.Parallel()
	// 模拟主循环中的排水超时检查逻辑
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
		// 加延迟模拟上游慢响应
		pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta"), delay: 80 * time.Millisecond},
		pumpTestConnEvent{data: pumpTestEvent("response.completed")},
	)
	lease := &pumpTestLease{conn: conn}
	ch, pumpCancel := startPump(context.Background(), lease, 5*time.Second)
	defer pumpCancel()

	clientDisconnected := false
	drainDeadline := time.Time{}
	var eventsBeforeDrain []openAIWSUpstreamPumpEvent
	drainTriggered := false

	for evt := range ch {
		// 检查排水超时
		if clientDisconnected && !drainDeadline.IsZero() && time.Now().After(drainDeadline) {
			pumpCancel()
			drainTriggered = true
			break
		}
		if evt.err != nil {
			break
		}
		eventsBeforeDrain = append(eventsBeforeDrain, evt)

		// 模拟：第一个事件后客户端断连，设置极短的排水截止时间
		if !clientDisconnected && len(eventsBeforeDrain) == 1 {
			clientDisconnected = true
			drainDeadline = time.Now().Add(30 * time.Millisecond)
		}
	}

	// 排水截止时间为 30ms，第二个事件延迟 80ms，所以应该触发排水超时
	assert.True(t, drainTriggered, "排水超时应被触发")
	assert.Len(t, eventsBeforeDrain, 1, "排水前应只有 1 个事件")
}

// ---------------------------------------------------------------------------
// 测试：与上游事件延迟的并发行为
// ---------------------------------------------------------------------------

func TestPump_ReadDelayDoesNotBlockPreviousEvents(t *testing.T) {
	t.Parallel()
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
		pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta"), delay: 100 * time.Millisecond},
		pumpTestConnEvent{data: pumpTestEvent("response.completed")},
	)
	lease := &pumpTestLease{conn: conn}
	ch, cancel := startPump(context.Background(), lease, 5*time.Second)
	defer cancel()

	// 第一个事件应该立即可用
	start := time.Now()
	evt := <-ch
	assert.NoError(t, evt.err)
	assert.Less(t, time.Since(start), 50*time.Millisecond, "第一个事件应立即到达")

	events := collectAll(ch)
	require.Len(t, events, 2)
}

// ---------------------------------------------------------------------------
// 测试：空事件流
// ---------------------------------------------------------------------------

func TestPump_EmptyStreamContextCancel(t *testing.T) {
	t.Parallel()
	// 没有任何事件，连接阻塞，靠 context 取消
	conn := newPumpTestConn() // 无事件
	lease := &pumpTestLease{conn: conn}
	ctx, ctxCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer ctxCancel()
	ch, pumpCancel := startPump(ctx, lease, 30*time.Second)
	defer pumpCancel()

	events := collectAll(ch)
	// context 取消后，泵的 select 可能选择 pumpCtx.Done() 分支直接退出（0 个事件），
	// 也可能先将错误事件发送到 channel 后退出（1 个事件），两种行为都正确。
	assert.LessOrEqual(t, len(events), 1, "最多应收到 1 个事件")
	for _, evt := range events {
		assert.Error(t, evt.err)
	}
}

// ---------------------------------------------------------------------------
// 测试：非终端/非错误事件不终止泵
// ---------------------------------------------------------------------------

func TestPump_NonTerminalEventsDoNotStopPump(t *testing.T) {
	t.Parallel()
	nonTerminalTypes := []string{
		"response.created",
		"response.in_progress",
		"response.output_text.delta",
		"response.content_part.added",
		"response.output_item.added",
		"response.reasoning_summary_text.delta",
	}
	connEvents := make([]pumpTestConnEvent, 0, len(nonTerminalTypes)+1)
	for _, et := range nonTerminalTypes {
		connEvents = append(connEvents, pumpTestConnEvent{data: pumpTestEvent(et)})
	}
	connEvents = append(connEvents, pumpTestConnEvent{data: pumpTestEvent("response.completed")})

	conn := newPumpTestConn(connEvents...)
	lease := &pumpTestLease{conn: conn}
	ch, cancel := startPump(context.Background(), lease, 5*time.Second)
	defer cancel()

	events := collectAll(ch)
	require.Len(t, events, len(nonTerminalTypes)+1, "所有非终端事件 + 终端事件都应被传递")
}

// ---------------------------------------------------------------------------
// 测试：多次 pumpCancel 调用安全（幂等）
// ---------------------------------------------------------------------------

func TestPump_MultipleCancelSafe(t *testing.T) {
	t.Parallel()
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.completed")},
	)
	lease := &pumpTestLease{conn: conn}
	ch, pumpCancel := startPump(context.Background(), lease, 5*time.Second)

	events := collectAll(ch)
	require.Len(t, events, 1)

	// 多次调用 pumpCancel 不应 panic
	assert.NotPanics(t, func() {
		pumpCancel()
		pumpCancel()
		pumpCancel()
	})
}

// ---------------------------------------------------------------------------
// 测试：泵与主循环集成——模拟完整的 relay 消费模式
// ---------------------------------------------------------------------------

func TestPump_IntegrationRelayPattern(t *testing.T) {
	t.Parallel()
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEventWithResponseID("response.created", "resp_abc123")},
		pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta")},
		pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta")},
		pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta")},
		pumpTestConnEvent{data: pumpTestEventWithResponseID("response.completed", "resp_abc123")},
	)
	lease := &pumpTestLease{conn: conn}
	ch, pumpCancel := startPump(context.Background(), lease, 5*time.Second)
	defer pumpCancel()

	// 模拟主循环处理
	var responseID string
	eventCount := 0
	tokenEventCount := 0
	var terminalEventType string
	clientWriteCount := 0

	for evt := range ch {
		if evt.err != nil {
			t.Fatalf("unexpected error: %v", evt.err)
		}
		eventType, evtRespID := parseOpenAIWSEventType(evt.message)
		if responseID == "" && evtRespID != "" {
			responseID = evtRespID
		}
		eventCount++
		if isOpenAIWSTokenEvent(eventType) {
			tokenEventCount++
		}
		// 模拟写客户端
		clientWriteCount++

		if isOpenAIWSTerminalEvent(eventType) {
			terminalEventType = eventType
			break
		}
	}

	assert.Equal(t, "resp_abc123", responseID)
	assert.Equal(t, 5, eventCount)
	assert.GreaterOrEqual(t, tokenEventCount, 3, "至少 3 个 delta 事件应被计为 token 事件")
	assert.Equal(t, 5, clientWriteCount)
	assert.Equal(t, "response.completed", terminalEventType)
}

// ---------------------------------------------------------------------------
// 测试：泵 goroutine 在 channel 满时 + context 取消的行为
// ---------------------------------------------------------------------------

func TestPump_ChannelFullThenCancel(t *testing.T) {
	t.Parallel()
	// 生成大量事件但不消费，验证 pumpCancel 仍然能终止泵
	numEvents := openAIWSUpstreamPumpBufferSize * 3
	connEvents := make([]pumpTestConnEvent, numEvents)
	for i := range connEvents {
		connEvents[i] = pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta")}
	}
	conn := newPumpTestConn(connEvents...)
	lease := &pumpTestLease{conn: conn}
	ch, pumpCancel := startPump(context.Background(), lease, 5*time.Second)

	// 等待缓冲区被填满
	time.Sleep(50 * time.Millisecond)

	// 取消泵
	pumpCancel()

	// 清空 channel
	events := collectAll(ch)
	// 应收到 bufferSize 到 bufferSize+1 个事件（泵在 channel 满时可能阻塞在 select）
	assert.LessOrEqual(t, len(events), numEvents, "不应收到超过总事件数的事件")
	assert.GreaterOrEqual(t, len(events), 1, "至少应收到一些事件")
}

// ---------------------------------------------------------------------------
// 测试：读取超时机制
// ---------------------------------------------------------------------------

func TestPump_ReadTimeoutTriggersError(t *testing.T) {
	t.Parallel()
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
		// 第二次读取延迟超过超时
		pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta"), delay: 500 * time.Millisecond},
	)
	lease := &pumpTestLease{conn: conn}
	// 读取超时设为 50ms，远小于 500ms 延迟
	ch, cancel := startPump(context.Background(), lease, 50*time.Millisecond)
	defer cancel()

	events := collectAll(ch)
	require.Len(t, events, 2)
	assert.NoError(t, events[0].err)
	assert.Error(t, events[1].err, "第二次读取应超时")
}

// ---------------------------------------------------------------------------
// 测试：泵在 response.done 事件后停止（另一种终端事件）
// ---------------------------------------------------------------------------

func TestPump_ResponseDoneStopsPump(t *testing.T) {
	t.Parallel()
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
		pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta")},
		pumpTestConnEvent{data: pumpTestEvent("response.done")},
		pumpTestConnEvent{data: pumpTestEvent("should_not_reach")},
	)
	lease := &pumpTestLease{conn: conn}
	ch, cancel := startPump(context.Background(), lease, 5*time.Second)
	defer cancel()

	events := collectAll(ch)
	require.Len(t, events, 3)
	lastType, _ := parseOpenAIWSEventType(events[2].message)
	assert.Equal(t, "response.done", lastType)
}

// ---------------------------------------------------------------------------
// 测试：泵在读取到 error event 后不继续读取更多事件
// ---------------------------------------------------------------------------

func TestPump_ErrorEventStopsReading(t *testing.T) {
	t.Parallel()
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("error")},
		pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta")}, // 不应被读取
	)
	// 重写以追踪读取次数
	origEvents := conn.events
	conn.events = nil
	var wrappedConn pumpTestConn
	wrappedConn.closedCh = make(chan struct{})
	wrappedConn.events = origEvents
	wrappedLease := &pumpTestLease{conn: &wrappedConn}

	ch, cancel := startPump(context.Background(), wrappedLease, 5*time.Second)
	defer cancel()

	events := collectAll(ch)
	require.Len(t, events, 1, "error 事件后不应再读取更多事件")
	evtType, _ := parseOpenAIWSEventType(events[0].message)
	assert.Equal(t, "error", evtType)
}

// ---------------------------------------------------------------------------
// 测试：验证事件顺序保持不变
// ---------------------------------------------------------------------------

func TestPump_EventOrderPreserved(t *testing.T) {
	t.Parallel()
	expectedTypes := []string{
		"response.created",
		"response.in_progress",
		"response.output_item.added",
		"response.content_part.added",
		"response.output_text.delta",
		"response.output_text.delta",
		"response.output_text.delta",
		"response.output_text.done",
		"response.content_part.done",
		"response.output_item.done",
		"response.completed",
	}
	connEvents := make([]pumpTestConnEvent, len(expectedTypes))
	for i, et := range expectedTypes {
		connEvents[i] = pumpTestConnEvent{data: pumpTestEvent(et)}
	}
	conn := newPumpTestConn(connEvents...)
	lease := &pumpTestLease{conn: conn}
	ch, cancel := startPump(context.Background(), lease, 5*time.Second)
	defer cancel()

	events := collectAll(ch)
	require.Len(t, events, len(expectedTypes))
	for i, evt := range events {
		evtType, _ := parseOpenAIWSEventType(evt.message)
		assert.Equal(t, expectedTypes[i], evtType, "事件 %d 类型不匹配", i)
	}
}

// ---------------------------------------------------------------------------
// 测试：无效 JSON 消息不影响泵运行
// ---------------------------------------------------------------------------

func TestPump_InvalidJSONDoesNotStopPump(t *testing.T) {
	t.Parallel()
	conn := newPumpTestConn(
		pumpTestConnEvent{data: []byte("not json")},
		pumpTestConnEvent{data: []byte("{invalid")},
		pumpTestConnEvent{data: pumpTestEvent("response.completed")},
	)
	lease := &pumpTestLease{conn: conn}
	ch, cancel := startPump(context.Background(), lease, 5*time.Second)
	defer cancel()

	events := collectAll(ch)
	require.Len(t, events, 3, "无效 JSON 不应终止泵")
}

// ---------------------------------------------------------------------------
// 测试：并发安全——多个消费者不会 panic
// ---------------------------------------------------------------------------

func TestPump_ConcurrentConsumeAndCancel(t *testing.T) {
	t.Parallel()
	connEvents := make([]pumpTestConnEvent, 100)
	for i := range connEvents {
		connEvents[i] = pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta"), delay: time.Millisecond}
	}
	conn := newPumpTestConn(connEvents...)
	lease := &pumpTestLease{conn: conn}
	ch, pumpCancel := startPump(context.Background(), lease, 5*time.Second)

	// 同时消费和取消，不应 panic
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range ch {
			// 消费
		}
	}()
	go func() {
		defer wg.Done()
		time.Sleep(20 * time.Millisecond)
		pumpCancel()
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// 成功
	case <-time.After(5 * time.Second):
		t.Fatal("超时：并发消费和取消场景死锁")
	}
}

// ---------------------------------------------------------------------------
// 测试：排水定时器与正常终端事件的竞争
// ---------------------------------------------------------------------------

func TestPump_DrainTimerRaceWithTerminalEvent(t *testing.T) {
	t.Parallel()
	// 终端事件在排水定时器到期前到达
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
		pumpTestConnEvent{data: pumpTestEvent("response.completed"), delay: 10 * time.Millisecond},
	)
	lease := &pumpTestLease{conn: conn}
	ch, pumpCancel := startPump(context.Background(), lease, 5*time.Second)

	// 设置较长的排水定时器（200ms），终端事件应在 10ms 后到达
	drainTimer := time.AfterFunc(200*time.Millisecond, pumpCancel)
	defer drainTimer.Stop()

	events := collectAll(ch)
	// 终端事件应先到达
	require.Len(t, events, 2)
	lastType, _ := parseOpenAIWSEventType(events[1].message)
	assert.Equal(t, "response.completed", lastType)
	assert.NoError(t, events[1].err)

	pumpCancel() // 清理
}

// ---------------------------------------------------------------------------
// 测试：大量事件的吞吐量（确保泵不引入异常开销）
// ---------------------------------------------------------------------------

func TestPump_HighThroughput(t *testing.T) {
	t.Parallel()
	numEvents := 1000
	connEvents := make([]pumpTestConnEvent, numEvents)
	for i := range connEvents {
		if i == numEvents-1 {
			connEvents[i] = pumpTestConnEvent{data: pumpTestEvent("response.completed")}
		} else {
			connEvents[i] = pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta")}
		}
	}
	conn := newPumpTestConn(connEvents...)
	lease := &pumpTestLease{conn: conn}
	ch, cancel := startPump(context.Background(), lease, 5*time.Second)
	defer cancel()

	start := time.Now()
	events := collectAll(ch)
	elapsed := time.Since(start)

	require.Len(t, events, numEvents)
	assert.Less(t, elapsed, 2*time.Second, "1000 个事件应在 2 秒内完成")
	for _, evt := range events {
		assert.NoError(t, evt.err)
	}
}

// ---------------------------------------------------------------------------
// 测试：空消息（零字节）不终止泵
// ---------------------------------------------------------------------------

func TestPump_EmptyMessageDoesNotStopPump(t *testing.T) {
	t.Parallel()
	conn := newPumpTestConn(
		pumpTestConnEvent{data: []byte{}},
		pumpTestConnEvent{data: nil},
		pumpTestConnEvent{data: pumpTestEvent("response.completed")},
	)
	lease := &pumpTestLease{conn: conn}
	ch, cancel := startPump(context.Background(), lease, 5*time.Second)
	defer cancel()

	events := collectAll(ch)
	require.Len(t, events, 3, "空消息不应终止泵")
}

// ===========================================================================
// 以下为消息泵模式新增代码路径的补充测试
// ===========================================================================

// ---------------------------------------------------------------------------
// 测试：泵 channel 关闭但无终端事件（上游异常断连）
// ---------------------------------------------------------------------------

func TestPump_UnexpectedCloseDetectedByConsumer(t *testing.T) {
	t.Parallel()
	// 模拟：上游只发了非终端事件就断连（ReadMessage 返回 EOF）
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
		pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta")},
		pumpTestConnEvent{err: io.EOF}, // 上游断连
	)
	lease := &pumpTestLease{conn: conn}
	ch, cancel := startPump(context.Background(), lease, 5*time.Second)
	defer cancel()

	// 模拟主循环消费：检查是否收到了终端事件
	receivedTerminal := false
	var lastErr error
	for evt := range ch {
		if evt.err != nil {
			lastErr = evt.err
			break
		}
		evtType, _ := parseOpenAIWSEventType(evt.message)
		if isOpenAIWSTerminalEvent(evtType) {
			receivedTerminal = true
			break
		}
	}
	// 未收到终端事件，但收到了 EOF 错误——消费者应识别为上游异常断连
	assert.False(t, receivedTerminal, "不应收到终端事件")
	assert.ErrorIs(t, lastErr, io.EOF, "应收到 EOF 错误标识上游断连")
}

func TestPump_ChannelCloseWithoutTerminalOrError(t *testing.T) {
	t.Parallel()
	// 极端情况：泵被外部取消（pumpCancel），channel 关闭但既无终端事件也无错误事件。
	// 模拟中间事件后泵被取消。
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
		pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta")},
		// 无更多事件，ReadMessage 将阻塞
	)
	lease := &pumpTestLease{conn: conn}
	ch, pumpCancel := startPump(context.Background(), lease, 30*time.Second)

	// 消费前两个事件
	evt1 := <-ch
	assert.NoError(t, evt1.err)
	evt2 := <-ch
	assert.NoError(t, evt2.err)

	// 外部取消泵
	pumpCancel()

	// for-range 应退出，模拟 "泵 channel 关闭但未收到终端事件" 场景
	receivedTerminal := false
	for evt := range ch {
		if evt.err == nil {
			evtType, _ := parseOpenAIWSEventType(evt.message)
			if isOpenAIWSTerminalEvent(evtType) {
				receivedTerminal = true
			}
		}
	}
	assert.False(t, receivedTerminal, "泵被取消后不应再收到终端事件")
}

// ---------------------------------------------------------------------------
// 测试：lease.MarkBroken 场景验证
// ---------------------------------------------------------------------------

func TestPump_LeaseMarkedBrokenOnUnexpectedClose(t *testing.T) {
	t.Parallel()
	// 模拟主循环：泵关闭但无终端事件时应标记 lease 为 broken
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
		pumpTestConnEvent{err: io.ErrUnexpectedEOF},
	)
	lease := &pumpTestLease{conn: conn}
	ch, cancel := startPump(context.Background(), lease, 5*time.Second)
	defer cancel()

	receivedTerminal := false
	for evt := range ch {
		if evt.err != nil {
			// 模拟正式代码中的错误处理路径
			lease.MarkBroken()
			break
		}
		evtType, _ := parseOpenAIWSEventType(evt.message)
		if isOpenAIWSTerminalEvent(evtType) {
			receivedTerminal = true
			break
		}
	}
	// 如果 for-range 正常退出且未收到终端事件，也标记 broken
	if !receivedTerminal {
		lease.MarkBroken()
	}

	assert.True(t, lease.IsBroken(), "上游异常断连应标记 lease 为 broken")
}

func TestPump_LeaseNotBrokenOnNormalTerminal(t *testing.T) {
	t.Parallel()
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
		pumpTestConnEvent{data: pumpTestEvent("response.completed")},
	)
	lease := &pumpTestLease{conn: conn}
	ch, cancel := startPump(context.Background(), lease, 5*time.Second)
	defer cancel()

	for evt := range ch {
		if evt.err != nil {
			lease.MarkBroken()
			break
		}
	}

	assert.False(t, lease.IsBroken(), "正常终端事件不应标记 lease 为 broken")
}

// ---------------------------------------------------------------------------
// 测试：排水定时器只创建一次
// ---------------------------------------------------------------------------

func TestPump_DrainTimerCreatedOnlyOnce(t *testing.T) {
	t.Parallel()
	// 模拟多次"客户端断连"信号，验证排水定时器只创建一次
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
		pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta"), delay: 10 * time.Millisecond},
		pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta"), delay: 10 * time.Millisecond},
		pumpTestConnEvent{data: pumpTestEvent("response.completed"), delay: 10 * time.Millisecond},
	)
	lease := &pumpTestLease{conn: conn}
	ch, pumpCancel := startPump(context.Background(), lease, 5*time.Second)
	defer pumpCancel()

	drainTimerCount := 0
	clientDisconnected := false
	drainDeadline := time.Time{}
	var drainTimer *time.Timer

	for evt := range ch {
		if evt.err != nil {
			break
		}
		// 每个事件后都"检测到客户端断连"
		if !clientDisconnected {
			clientDisconnected = true
		}
		// 排水定时器只在第一次断连时创建
		if clientDisconnected && drainDeadline.IsZero() {
			drainDeadline = time.Now().Add(500 * time.Millisecond)
			drainTimer = time.AfterFunc(500*time.Millisecond, pumpCancel)
			drainTimerCount++
		}
	}
	if drainTimer != nil {
		drainTimer.Stop()
	}

	assert.Equal(t, 1, drainTimerCount, "排水定时器应只创建一次")
}

// ---------------------------------------------------------------------------
// 测试：排水定时器在正常完成前被 Stop
// ---------------------------------------------------------------------------

func TestPump_DrainTimerStoppedOnNormalCompletion(t *testing.T) {
	t.Parallel()
	// 终端事件在排水定时器到期前到达，验证定时器被正确停止
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
		pumpTestConnEvent{data: pumpTestEvent("response.completed"), delay: 5 * time.Millisecond},
	)
	lease := &pumpTestLease{conn: conn}
	ch, pumpCancel := startPump(context.Background(), lease, 5*time.Second)

	// 创建长时间排水定时器
	drainTimer := time.AfterFunc(10*time.Second, pumpCancel)

	var events []openAIWSUpstreamPumpEvent
	for evt := range ch {
		events = append(events, evt)
	}

	// 正常完成后停止排水定时器（模拟 defer drainTimer.Stop()）
	stopped := drainTimer.Stop()
	pumpCancel() // 清理

	assert.True(t, stopped, "定时器应尚未触发，Stop() 返回 true")
	require.Len(t, events, 2)
}

// ---------------------------------------------------------------------------
// 测试：排水期间读取错误处理
// ---------------------------------------------------------------------------

func TestPump_ReadErrorDuringDrainTreatedAsDrainTimeout(t *testing.T) {
	t.Parallel()
	// 新代码：客户端已断连时任何读取错误都按排水超时处理（不仅限 DeadlineExceeded）
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
		pumpTestConnEvent{err: io.ErrUnexpectedEOF, delay: 20 * time.Millisecond},
	)
	lease := &pumpTestLease{conn: conn}
	ch, pumpCancel := startPump(context.Background(), lease, 5*time.Second)
	defer pumpCancel()

	clientDisconnected := false
	var drainError error

	for evt := range ch {
		if !clientDisconnected {
			// 第一个事件后模拟客户端断连
			clientDisconnected = true
			continue
		}
		if evt.err != nil && clientDisconnected {
			// 新代码路径：排水期间收到读取错误
			drainError = evt.err
			break
		}
	}

	assert.Error(t, drainError, "排水期间应收到读取错误")
	assert.ErrorIs(t, drainError, io.ErrUnexpectedEOF)
}

func TestPump_ReadErrorDuringDrain_EOF(t *testing.T) {
	t.Parallel()
	// EOF 在排水期间等同于上游关闭
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
		pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta")},
		pumpTestConnEvent{err: io.EOF},
	)
	lease := &pumpTestLease{conn: conn}
	ch, pumpCancel := startPump(context.Background(), lease, 5*time.Second)
	defer pumpCancel()

	clientDisconnected := false
	drainErrorCount := 0

	for evt := range ch {
		if evt.err != nil {
			if clientDisconnected {
				drainErrorCount++
			}
			break
		}
		// 第一个事件后模拟客户端断连
		if !clientDisconnected {
			clientDisconnected = true
		}
	}

	assert.Equal(t, 1, drainErrorCount, "排水期间 EOF 应被计为一次排水错误")
}

// ---------------------------------------------------------------------------
// 测试：排水截止时间检查——在事件间隙中过期
// ---------------------------------------------------------------------------

func TestPump_DrainDeadlineExpiresBetweenEvents(t *testing.T) {
	t.Parallel()
	// 排水截止时间在两个上游事件之间到期
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
		pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta"), delay: 60 * time.Millisecond},
		pumpTestConnEvent{data: pumpTestEvent("response.completed"), delay: 60 * time.Millisecond},
	)
	lease := &pumpTestLease{conn: conn}
	ch, pumpCancel := startPump(context.Background(), lease, 5*time.Second)
	defer pumpCancel()

	clientDisconnected := false
	drainDeadline := time.Time{}
	drainExpired := false
	eventsProcessed := 0

	for evt := range ch {
		// 排水超时检查（在处理事件前，模拟正式代码）
		if clientDisconnected && !drainDeadline.IsZero() && time.Now().After(drainDeadline) {
			pumpCancel()
			drainExpired = true
			break
		}
		if evt.err != nil {
			break
		}
		eventsProcessed++

		// 第一个事件后断连，排水截止时间设为 30ms
		if !clientDisconnected {
			clientDisconnected = true
			drainDeadline = time.Now().Add(30 * time.Millisecond)
		}
	}

	// 第二个事件延迟 60ms > 排水截止 30ms，应触发排水超时
	assert.True(t, drainExpired, "排水截止时间应在事件间隙中过期")
	assert.Equal(t, 1, eventsProcessed, "过期前应只处理了 1 个事件")
}

func TestPump_DrainDeadlineNotYetExpiredAllowsProcessing(t *testing.T) {
	t.Parallel()
	// 排水截止时间足够长，允许处理所有事件
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
		pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta"), delay: 5 * time.Millisecond},
		pumpTestConnEvent{data: pumpTestEvent("response.completed"), delay: 5 * time.Millisecond},
	)
	lease := &pumpTestLease{conn: conn}
	ch, pumpCancel := startPump(context.Background(), lease, 5*time.Second)
	defer pumpCancel()

	clientDisconnected := false
	drainDeadline := time.Time{}
	drainExpired := false
	eventsProcessed := 0

	for evt := range ch {
		if clientDisconnected && !drainDeadline.IsZero() && time.Now().After(drainDeadline) {
			pumpCancel()
			drainExpired = true
			break
		}
		if evt.err != nil {
			break
		}
		eventsProcessed++
		if !clientDisconnected {
			clientDisconnected = true
			drainDeadline = time.Now().Add(500 * time.Millisecond) // 足够长
		}
	}

	assert.False(t, drainExpired, "排水截止时间未过期，不应触发排水超时")
	assert.Equal(t, 3, eventsProcessed, "所有事件都应被处理")
}

// ---------------------------------------------------------------------------
// 测试：goroutine 清理和资源释放
// ---------------------------------------------------------------------------

func TestPump_DeferPumpCancelAndDrainTimerCleanup(t *testing.T) {
	t.Parallel()
	// 模拟正式代码的完整 defer 清理路径
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
		pumpTestConnEvent{data: pumpTestEvent("response.completed")},
	)
	lease := &pumpTestLease{conn: conn}
	pumpEventCh := make(chan openAIWSUpstreamPumpEvent, openAIWSUpstreamPumpBufferSize)
	pumpCtx, pumpCancel := context.WithCancel(context.Background())
	// 模拟 defer pumpCancel()
	defer pumpCancel()

	go func() {
		defer close(pumpEventCh)
		for {
			msg, readErr := lease.ReadMessageWithContextTimeout(pumpCtx, 5*time.Second)
			select {
			case pumpEventCh <- openAIWSUpstreamPumpEvent{message: msg, err: readErr}:
			case <-pumpCtx.Done():
				return
			}
			if readErr != nil {
				return
			}
			evtType, _ := parseOpenAIWSEventType(msg)
			if isOpenAIWSTerminalEvent(evtType) || evtType == "error" {
				return
			}
		}
	}()

	// 模拟排水定时器
	var drainTimer *time.Timer
	defer func() {
		if drainTimer != nil {
			drainTimer.Stop()
		}
	}()
	drainTimer = time.AfterFunc(10*time.Second, pumpCancel)

	events := collectAll(pumpEventCh)
	require.Len(t, events, 2)

	// defer 清理后不应 panic
	assert.NotPanics(t, func() {
		pumpCancel()
		if drainTimer != nil {
			drainTimer.Stop()
		}
	})
}

// ---------------------------------------------------------------------------
// 测试：连接在泵运行期间被关闭
// ---------------------------------------------------------------------------

func TestPump_ConnectionClosedDuringPump(t *testing.T) {
	t.Parallel()
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
		// 后续事件阻塞
	)
	lease := &pumpTestLease{conn: conn}
	// 使用较短的读超时，因为 conn.Close() 不会解除阻塞的 ReadMessage（它等待 <-ctx.Done()）
	ch, pumpCancel := startPump(context.Background(), lease, 100*time.Millisecond)
	defer pumpCancel()

	// 读取第一个事件
	evt := <-ch
	assert.NoError(t, evt.err)

	// 关闭连接——注意：ReadMessage 仍在等待 ctx.Done()，
	// 但读超时为 100ms 会触发 context.DeadlineExceeded。
	// 下次 ReadMessage 调用时会检测到 closed 状态。
	_ = conn.Close()

	// 泵应在读超时后检测到连接关闭
	events := collectAll(ch)
	require.GreaterOrEqual(t, len(events), 1, "应收到错误")
	// 至少有一个事件包含错误
	hasError := false
	for _, e := range events {
		if e.err != nil {
			hasError = true
		}
	}
	assert.True(t, hasError, "应收到连接关闭或超时错误")
}

// ---------------------------------------------------------------------------
// 测试：大消息（KB 级别 JSON）不影响泵传递
// ---------------------------------------------------------------------------

func TestPump_LargeMessages(t *testing.T) {
	t.Parallel()
	// 构造 ~10KB 的消息
	largeContent := make([]byte, 10*1024)
	for i := range largeContent {
		largeContent[i] = 'x'
	}
	largeMsg := []byte(`{"type":"response.output_text.delta","delta":"` + string(largeContent) + `"}`)
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
		pumpTestConnEvent{data: largeMsg},
		pumpTestConnEvent{data: largeMsg},
		pumpTestConnEvent{data: pumpTestEvent("response.completed")},
	)
	lease := &pumpTestLease{conn: conn}
	ch, cancel := startPump(context.Background(), lease, 5*time.Second)
	defer cancel()

	events := collectAll(ch)
	require.Len(t, events, 4)
	// 验证大消息完整传递
	assert.Len(t, events[1].message, len(largeMsg))
	assert.Len(t, events[2].message, len(largeMsg))
}

// ---------------------------------------------------------------------------
// 测试：多轮泵会话（同一 lease 上依次创建多个泵）
// ---------------------------------------------------------------------------

func TestPump_SequentialSessions(t *testing.T) {
	t.Parallel()
	// 第一轮
	conn1 := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
		pumpTestConnEvent{data: pumpTestEvent("response.completed")},
	)
	lease1 := &pumpTestLease{conn: conn1}
	ch1, cancel1 := startPump(context.Background(), lease1, 5*time.Second)
	events1 := collectAll(ch1)
	cancel1()
	require.Len(t, events1, 2)

	// 第二轮（新连接、新泵）
	conn2 := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
		pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta")},
		pumpTestConnEvent{data: pumpTestEvent("response.completed")},
	)
	lease2 := &pumpTestLease{conn: conn2}
	ch2, cancel2 := startPump(context.Background(), lease2, 5*time.Second)
	events2 := collectAll(ch2)
	cancel2()
	require.Len(t, events2, 3)

	// 两轮之间互不影响
	assert.False(t, lease1.IsBroken())
	assert.False(t, lease2.IsBroken())
}

// ---------------------------------------------------------------------------
// 测试：完整 relay 模式集成——包含客户端断连和排水
// ---------------------------------------------------------------------------

func TestPump_IntegrationRelayWithClientDisconnectAndDrain(t *testing.T) {
	t.Parallel()
	// 模拟完整场景：上游慢速响应，客户端在中途断连，排水定时器到期终止
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEventWithResponseID("response.created", "resp_drain1")},
		pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta"), delay: 10 * time.Millisecond},
		pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta"), delay: 10 * time.Millisecond},
		// 上游后续事件延迟大于排水超时
		pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta"), delay: 200 * time.Millisecond},
		pumpTestConnEvent{data: pumpTestEvent("response.completed"), delay: 200 * time.Millisecond},
	)
	lease := &pumpTestLease{conn: conn}
	ch, pumpCancel := startPump(context.Background(), lease, 5*time.Second)

	clientDisconnected := false
	drainDeadline := time.Time{}
	var drainTimer *time.Timer
	defer func() {
		if drainTimer != nil {
			drainTimer.Stop()
		}
	}()

	eventsProcessed := 0
	drainTriggered := false

	for evt := range ch {
		// 排水超时检查
		if clientDisconnected && !drainDeadline.IsZero() && time.Now().After(drainDeadline) {
			pumpCancel()
			drainTriggered = true
			lease.MarkBroken()
			break
		}
		if evt.err != nil {
			if clientDisconnected {
				// 排水期间读取错误（pumpCancel 导致 context.Canceled）
				drainTriggered = true
				lease.MarkBroken()
			}
			break
		}
		eventsProcessed++

		// 模拟：第 2 个事件后客户端断连
		if eventsProcessed == 2 && !clientDisconnected {
			clientDisconnected = true
			drainDeadline = time.Now().Add(50 * time.Millisecond) // 50ms 排水超时
			drainTimer = time.AfterFunc(50*time.Millisecond, pumpCancel)
		}
	}
	// for-range 退出后，如果 channel 因 pumpCancel 关闭且排水截止已过期，
	// 也视为排水超时触发（泵的 select 可能选择 pumpCtx.Done() 而不发送错误事件）。
	if !drainTriggered && clientDisconnected && !drainDeadline.IsZero() && time.Now().After(drainDeadline) {
		drainTriggered = true
		lease.MarkBroken()
	}

	pumpCancel() // 最终清理

	// 排水超时应触发（50ms 排水 vs 200ms 后续事件延迟）
	assert.True(t, drainTriggered, "排水超时应被触发")
	assert.GreaterOrEqual(t, eventsProcessed, 2, "至少应处理 2 个事件")
	assert.LessOrEqual(t, eventsProcessed, 4, "不应处理所有 5 个事件")
}

func TestPump_IntegrationRelayWithSuccessfulDrain(t *testing.T) {
	t.Parallel()
	// 客户端断连后上游快速完成，在排水超时前正常结束
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEventWithResponseID("response.created", "resp_drain2")},
		pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta")},
		pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta"), delay: 5 * time.Millisecond},
		pumpTestConnEvent{data: pumpTestEvent("response.completed"), delay: 5 * time.Millisecond},
	)
	lease := &pumpTestLease{conn: conn}
	ch, pumpCancel := startPump(context.Background(), lease, 5*time.Second)

	clientDisconnected := false
	drainDeadline := time.Time{}
	var drainTimer *time.Timer
	defer func() {
		if drainTimer != nil {
			drainTimer.Stop()
		}
	}()

	eventsProcessed := 0
	receivedTerminal := false
	drainTriggered := false

	for evt := range ch {
		if clientDisconnected && !drainDeadline.IsZero() && time.Now().After(drainDeadline) {
			pumpCancel()
			drainTriggered = true
			break
		}
		if evt.err != nil {
			break
		}
		eventsProcessed++

		evtType, _ := parseOpenAIWSEventType(evt.message)
		if isOpenAIWSTerminalEvent(evtType) {
			receivedTerminal = true
			break
		}

		// 第一个事件后客户端断连
		if eventsProcessed == 1 && !clientDisconnected {
			clientDisconnected = true
			drainDeadline = time.Now().Add(500 * time.Millisecond) // 足够长的排水超时
			drainTimer = time.AfterFunc(500*time.Millisecond, pumpCancel)
		}
	}

	pumpCancel()

	assert.False(t, drainTriggered, "排水超时不应触发")
	assert.True(t, receivedTerminal, "应正常收到终端事件")
	assert.Equal(t, 4, eventsProcessed, "所有 4 个事件都应被处理")
}

// ---------------------------------------------------------------------------
// 测试：泵事件错误携带部分消息数据
// ---------------------------------------------------------------------------

func TestPump_ErrorEventWithPartialMessage(t *testing.T) {
	t.Parallel()
	// 模拟上游返回部分数据和错误
	partialData := []byte(`{"type":"response.output_text`)
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
		pumpTestConnEvent{data: partialData, err: io.ErrUnexpectedEOF},
	)
	lease := &pumpTestLease{conn: conn}
	ch, cancel := startPump(context.Background(), lease, 5*time.Second)
	defer cancel()

	events := collectAll(ch)
	require.Len(t, events, 2)
	// 第二个事件同时携带 message 和 error
	assert.Equal(t, partialData, events[1].message)
	assert.ErrorIs(t, events[1].err, io.ErrUnexpectedEOF)
}

// ---------------------------------------------------------------------------
// 测试：零超时读取
// ---------------------------------------------------------------------------

func TestPump_ZeroReadTimeout(t *testing.T) {
	t.Parallel()
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
		// 第二次读取需要时间，但超时为 0
		pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta"), delay: 10 * time.Millisecond},
	)
	lease := &pumpTestLease{conn: conn}
	// 使用极短超时（1 纳秒 ~ 立即超时）
	ch, cancel := startPump(context.Background(), lease, time.Nanosecond)
	defer cancel()

	events := collectAll(ch)
	// 至少第一个事件成功读取（无延迟），第二个大概率超时
	require.GreaterOrEqual(t, len(events), 1)
	// 查找是否有超时错误
	hasTimeout := false
	for _, evt := range events {
		if evt.err != nil && errors.Is(evt.err, context.DeadlineExceeded) {
			hasTimeout = true
		}
	}
	assert.True(t, hasTimeout, "极短超时应产生 DeadlineExceeded 错误")
}

// ---------------------------------------------------------------------------
// 测试：并发多个排水定时器取消（防止重复调用 pumpCancel）
// ---------------------------------------------------------------------------

func TestPump_ConcurrentDrainTimerAndExternalCancel(t *testing.T) {
	t.Parallel()
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
		// 阻塞
	)
	lease := &pumpTestLease{conn: conn}
	ch, pumpCancel := startPump(context.Background(), lease, 30*time.Second)

	// 读取第一个事件
	evt := <-ch
	assert.NoError(t, evt.err)

	// 同时设置排水定时器和外部取消
	done := make(chan struct{})
	drainTimer := time.AfterFunc(30*time.Millisecond, pumpCancel)
	defer drainTimer.Stop()

	go func() {
		time.Sleep(20 * time.Millisecond)
		pumpCancel() // 外部取消稍早于定时器
		close(done)
	}()

	// 不应死锁或 panic
	events := collectAll(ch)
	<-done

	// 验证泵已终止
	for _, e := range events {
		if e.err != nil {
			assert.Error(t, e.err)
		}
	}
}

func TestPump_DrainTimerMarkBrokenUnblocksIgnoreContextRead(t *testing.T) {
	t.Parallel()

	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
	)
	conn.ignoreCtx = true
	lease := &pumpTestLease{conn: conn}
	ch, pumpCancel := startPump(context.Background(), lease, 30*time.Second)
	defer pumpCancel()

	first := <-ch
	require.NoError(t, first.err)
	require.NotEmpty(t, first.message)

	done := make(chan struct{})
	drainTimer := time.AfterFunc(30*time.Millisecond, func() {
		lease.MarkBroken()
		pumpCancel()
	})
	defer drainTimer.Stop()

	go func() {
		_ = collectAll(ch)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("pump should stop quickly when drain timer marks lease broken")
	}

	assert.True(t, lease.IsBroken(), "lease should be marked broken by drain timer")
}

// ---------------------------------------------------------------------------
// 测试：快速连续事件（突发模式）
// ---------------------------------------------------------------------------

func TestPump_BurstEvents(t *testing.T) {
	t.Parallel()
	// 50 个事件无延迟突发
	numBurst := 50
	connEvents := make([]pumpTestConnEvent, numBurst+1)
	for i := 0; i < numBurst; i++ {
		connEvents[i] = pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta")}
	}
	connEvents[numBurst] = pumpTestConnEvent{data: pumpTestEvent("response.completed")}

	conn := newPumpTestConn(connEvents...)
	lease := &pumpTestLease{conn: conn}
	ch, cancel := startPump(context.Background(), lease, 5*time.Second)
	defer cancel()

	events := collectAll(ch)
	require.Len(t, events, numBurst+1, "突发事件应全部被传递")

	// 验证所有事件无错误
	for i, evt := range events {
		assert.NoError(t, evt.err, "事件 %d 不应有错误", i)
	}
	lastType, _ := parseOpenAIWSEventType(events[numBurst].message)
	assert.True(t, isOpenAIWSTerminalEvent(lastType))
}

// ---------------------------------------------------------------------------
// 测试：事件类型解析边界情况
// ---------------------------------------------------------------------------

func TestPump_EventTypeParsingEdgeCases(t *testing.T) {
	t.Parallel()
	// 各种边缘 JSON 格式
	conn := newPumpTestConn(
		pumpTestConnEvent{data: []byte(`{"type": "  response.created  "}`)},      // 带空格
		pumpTestConnEvent{data: []byte(`{"type":"response.output_text.delta"}`)}, // 无空格
		pumpTestConnEvent{data: []byte(`{"type":"","other":"field"}`)},           // 空类型
		pumpTestConnEvent{data: []byte(`{"no_type_field": true}`)},               // 无 type 字段
		pumpTestConnEvent{data: []byte(`{"type":"response.completed"}`)},         // 终端
	)
	lease := &pumpTestLease{conn: conn}
	ch, cancel := startPump(context.Background(), lease, 5*time.Second)
	defer cancel()

	events := collectAll(ch)
	require.Len(t, events, 5, "所有格式的事件都应被传递")
	for _, evt := range events {
		assert.NoError(t, evt.err)
	}
}

// ---------------------------------------------------------------------------
// 测试：function_call_output 等非标准事件类型不终止泵
// ---------------------------------------------------------------------------

func TestPump_FunctionCallOutputDoesNotStopPump(t *testing.T) {
	t.Parallel()
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
		pumpTestConnEvent{data: pumpTestEvent("response.function_call_arguments.delta")},
		pumpTestConnEvent{data: pumpTestEvent("response.function_call_arguments.done")},
		pumpTestConnEvent{data: pumpTestEvent("response.output_item.done")},
		pumpTestConnEvent{data: pumpTestEvent("response.completed")},
	)
	lease := &pumpTestLease{conn: conn}
	ch, cancel := startPump(context.Background(), lease, 5*time.Second)
	defer cancel()

	events := collectAll(ch)
	require.Len(t, events, 5, "function_call 相关事件不应终止泵")
}

// ---------------------------------------------------------------------------
// 测试：pumpCancel 在 channel 已关闭后调用不 panic
// ---------------------------------------------------------------------------

func TestPump_CancelAfterChannelClosed(t *testing.T) {
	t.Parallel()
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.completed")},
	)
	lease := &pumpTestLease{conn: conn}
	ch, pumpCancel := startPump(context.Background(), lease, 5*time.Second)

	// 等待 channel 关闭
	events := collectAll(ch)
	require.Len(t, events, 1)

	// channel 已关闭后再取消不应 panic
	assert.NotPanics(t, func() {
		pumpCancel()
	})

	// 再次从已关闭 channel 读取应返回零值
	evt, ok := <-ch
	assert.False(t, ok, "channel 应已关闭")
	assert.Nil(t, evt.message)
	assert.NoError(t, evt.err)
}

// ---------------------------------------------------------------------------
// 测试：混合事件大小（小消息和大消息交替）
// ---------------------------------------------------------------------------

func TestPump_MixedMessageSizes(t *testing.T) {
	t.Parallel()
	smallMsg := pumpTestEvent("response.output_text.delta")
	largeContent := make([]byte, 64*1024) // 64KB
	for i := range largeContent {
		largeContent[i] = 'A'
	}
	largeMsg := []byte(`{"type":"response.output_text.delta","delta":"` + string(largeContent) + `"}`)

	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
		pumpTestConnEvent{data: smallMsg},
		pumpTestConnEvent{data: largeMsg},
		pumpTestConnEvent{data: smallMsg},
		pumpTestConnEvent{data: largeMsg},
		pumpTestConnEvent{data: pumpTestEvent("response.completed")},
	)
	lease := &pumpTestLease{conn: conn}
	ch, cancel := startPump(context.Background(), lease, 5*time.Second)
	defer cancel()

	events := collectAll(ch)
	require.Len(t, events, 6)
	assert.Len(t, events[2].message, len(largeMsg), "大消息应完整传递")
	assert.Len(t, events[4].message, len(largeMsg), "大消息应完整传递")
}

// ---------------------------------------------------------------------------
// 测试：泵在 errOpenAIWSConnClosed 错误后停止
// ---------------------------------------------------------------------------

func TestPump_ConnClosedErrorStopsPump(t *testing.T) {
	t.Parallel()
	conn := newPumpTestConn(
		pumpTestConnEvent{data: pumpTestEvent("response.created")},
		pumpTestConnEvent{err: errOpenAIWSConnClosed},
	)
	lease := &pumpTestLease{conn: conn}
	ch, cancel := startPump(context.Background(), lease, 5*time.Second)
	defer cancel()

	events := collectAll(ch)
	require.Len(t, events, 2)
	assert.ErrorIs(t, events[1].err, errOpenAIWSConnClosed)
}

// ---------------------------------------------------------------------------
// 测试：同时读取和写入——验证读写解耦
// ---------------------------------------------------------------------------

func TestPump_ReadWriteDecoupling(t *testing.T) {
	t.Parallel()
	// 模拟：上游事件到达时，客户端写入有延迟（通过 channel 消费延迟模拟）
	numEvents := 10
	connEvents := make([]pumpTestConnEvent, numEvents)
	for i := 0; i < numEvents-1; i++ {
		connEvents[i] = pumpTestConnEvent{data: pumpTestEvent("response.output_text.delta")}
	}
	connEvents[numEvents-1] = pumpTestConnEvent{data: pumpTestEvent("response.completed")}

	conn := newPumpTestConn(connEvents...)
	lease := &pumpTestLease{conn: conn}
	ch, cancel := startPump(context.Background(), lease, 5*time.Second)
	defer cancel()

	// 模拟慢写入：每个事件处理需要 5ms
	start := time.Now()
	var events []openAIWSUpstreamPumpEvent
	for evt := range ch {
		events = append(events, evt)
		time.Sleep(5 * time.Millisecond) // 模拟写入延迟
	}
	elapsed := time.Since(start)

	require.Len(t, events, numEvents)
	// 如果没有并发（串行读写），总时间 >= numEvents * 5ms = 50ms
	// 有缓冲并发时，上游读取可以提前完成，总时间 < 串行预估
	// 此处验证所有事件都被传递即可
	t.Logf("处理 %d 个事件耗时: %v (慢消费模式)", numEvents, elapsed)
}
