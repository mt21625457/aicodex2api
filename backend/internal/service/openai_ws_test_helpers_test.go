package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"
)

type openAIWSQueueDialer struct {
	mu        sync.Mutex
	conns     []openAIWSClientConn
	dialCount int
}

func (d *openAIWSQueueDialer) Dial(
	ctx context.Context,
	wsURL string,
	headers http.Header,
	proxyURL string,
) (openAIWSClientConn, int, http.Header, error) {
	_ = ctx
	_ = wsURL
	_ = headers
	_ = proxyURL
	d.mu.Lock()
	defer d.mu.Unlock()
	d.dialCount++
	if len(d.conns) == 0 {
		return nil, 503, nil, errors.New("no test conn")
	}
	conn := d.conns[0]
	if len(d.conns) > 1 {
		d.conns = d.conns[1:]
	}
	return conn, 0, nil, nil
}

func (d *openAIWSQueueDialer) DialCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dialCount
}

type openAIWSCaptureConn struct {
	mu         sync.Mutex
	readDelays []time.Duration
	events     [][]byte
	writes     []map[string]any
	closed     bool
}

func (c *openAIWSCaptureConn) WriteJSON(ctx context.Context, value any) error {
	_ = ctx
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errOpenAIWSConnClosed
	}
	switch payload := value.(type) {
	case map[string]any:
		c.writes = append(c.writes, cloneMapStringAny(payload))
	case json.RawMessage:
		var parsed map[string]any
		if err := json.Unmarshal(payload, &parsed); err == nil {
			c.writes = append(c.writes, cloneMapStringAny(parsed))
		}
	case []byte:
		var parsed map[string]any
		if err := json.Unmarshal(payload, &parsed); err == nil {
			c.writes = append(c.writes, cloneMapStringAny(parsed))
		}
	}
	return nil
}

func (c *openAIWSCaptureConn) ReadMessage(ctx context.Context) ([]byte, error) {
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
		return nil, io.EOF
	}
	delay := time.Duration(0)
	if len(c.readDelays) > 0 {
		delay = c.readDelays[0]
		c.readDelays = c.readDelays[1:]
	}
	event := c.events[0]
	c.events = c.events[1:]
	c.mu.Unlock()
	if delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return event, nil
}

func (c *openAIWSCaptureConn) Ping(ctx context.Context) error {
	_ = ctx
	return nil
}

func (c *openAIWSCaptureConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func (c *openAIWSCaptureConn) Closed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func cloneMapStringAny(src map[string]any) map[string]any {
	if src == nil {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

type openAIWSAlwaysFailDialer struct {
	mu        sync.Mutex
	dialCount int
}

func (d *openAIWSAlwaysFailDialer) Dial(
	ctx context.Context,
	wsURL string,
	headers http.Header,
	proxyURL string,
) (openAIWSClientConn, int, http.Header, error) {
	_ = ctx
	_ = wsURL
	_ = headers
	_ = proxyURL
	d.mu.Lock()
	d.dialCount++
	d.mu.Unlock()
	return nil, 503, nil, errors.New("dial failed")
}

func (d *openAIWSAlwaysFailDialer) DialCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.dialCount
}

type openAIWSFakeConn struct {
	mu      sync.Mutex
	closed  bool
	payload [][]byte
}

func (c *openAIWSFakeConn) WriteJSON(ctx context.Context, value any) error {
	_ = ctx
	_ = value
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("closed")
	}
	c.payload = append(c.payload, []byte("ok"))
	return nil
}

func (c *openAIWSFakeConn) ReadMessage(ctx context.Context) ([]byte, error) {
	_ = ctx
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("closed")
	}
	return []byte(`{"type":"response.completed","response":{"id":"resp_fake"}}`), nil
}

func (c *openAIWSFakeConn) Ping(ctx context.Context) error {
	_ = ctx
	return nil
}

func (c *openAIWSFakeConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

type openAIWSPingFailConn struct{}

func (c *openAIWSPingFailConn) WriteJSON(context.Context, any) error {
	return nil
}

func (c *openAIWSPingFailConn) ReadMessage(context.Context) ([]byte, error) {
	return []byte(`{"type":"response.completed","response":{"id":"resp_ping_fail"}}`), nil
}

func (c *openAIWSPingFailConn) Ping(context.Context) error {
	return errors.New("ping failed")
}

func (c *openAIWSPingFailConn) Close() error {
	return nil
}

// openAIWSDelayedPingFailConn 是带可控延迟的 Ping 失败连接，
// 用于模拟"Ping 执行期间连接被重建"的竞态场景。
type openAIWSDelayedPingFailConn struct {
	delay    time.Duration
	pingDone chan struct{} // Ping 开始执行时关闭，通知测试可以进行下一步
	mu       sync.Mutex
	closed   bool
}

func newOpenAIWSDelayedPingFailConn(delay time.Duration) *openAIWSDelayedPingFailConn {
	return &openAIWSDelayedPingFailConn{
		delay:    delay,
		pingDone: make(chan struct{}),
	}
}

func (c *openAIWSDelayedPingFailConn) WriteJSON(context.Context, any) error { return nil }
func (c *openAIWSDelayedPingFailConn) ReadMessage(context.Context) ([]byte, error) {
	return []byte(`{"type":"response.completed","response":{"id":"resp_delayed_ping"}}`), nil
}

func (c *openAIWSDelayedPingFailConn) Ping(ctx context.Context) error {
	// 通知测试 Ping 已开始
	select {
	case <-c.pingDone:
	default:
		close(c.pingDone)
	}
	// 等待延迟或上下文取消
	timer := time.NewTimer(c.delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
	}
	return errors.New("ping failed after delay")
}

func (c *openAIWSDelayedPingFailConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func (c *openAIWSDelayedPingFailConn) Closed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}
