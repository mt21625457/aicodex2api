package service

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	coderws "github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

func TestCoderOpenAIWSClientDialer_ProxyHTTPClientReuse(t *testing.T) {
	dialer := newDefaultOpenAIWSClientDialer()
	impl, ok := dialer.(*coderOpenAIWSClientDialer)
	require.True(t, ok)

	c1, err := impl.proxyHTTPClient("http://127.0.0.1:8080")
	require.NoError(t, err)
	c2, err := impl.proxyHTTPClient("http://127.0.0.1:8080")
	require.NoError(t, err)
	require.Same(t, c1, c2, "同一代理地址应复用同一个 HTTP 客户端")

	c3, err := impl.proxyHTTPClient("http://127.0.0.1:8081")
	require.NoError(t, err)
	require.NotSame(t, c1, c3, "不同代理地址应分离客户端")
}

func TestCoderOpenAIWSClientDialer_ProxyHTTPClientInvalidURL(t *testing.T) {
	dialer := newDefaultOpenAIWSClientDialer()
	impl, ok := dialer.(*coderOpenAIWSClientDialer)
	require.True(t, ok)

	_, err := impl.proxyHTTPClient("://bad")
	require.Error(t, err)
}

func TestCoderOpenAIWSClientDialer_TransportMetricsSnapshot(t *testing.T) {
	dialer := newDefaultOpenAIWSClientDialer()
	impl, ok := dialer.(*coderOpenAIWSClientDialer)
	require.True(t, ok)

	_, err := impl.proxyHTTPClient("http://127.0.0.1:18080")
	require.NoError(t, err)
	_, err = impl.proxyHTTPClient("http://127.0.0.1:18080")
	require.NoError(t, err)
	_, err = impl.proxyHTTPClient("http://127.0.0.1:18081")
	require.NoError(t, err)

	snapshot := impl.SnapshotTransportMetrics()
	require.Equal(t, int64(1), snapshot.ProxyClientCacheHits)
	require.Equal(t, int64(2), snapshot.ProxyClientCacheMisses)
	require.InDelta(t, 1.0/3.0, snapshot.TransportReuseRatio, 0.0001)
}

func TestCoderOpenAIWSClientDialer_ProxyClientCacheCapacity(t *testing.T) {
	dialer := newDefaultOpenAIWSClientDialer()
	impl, ok := dialer.(*coderOpenAIWSClientDialer)
	require.True(t, ok)

	total := openAIWSProxyClientCacheMaxEntries + 32
	for i := 0; i < total; i++ {
		_, err := impl.proxyHTTPClient(fmt.Sprintf("http://127.0.0.1:%d", 20000+i))
		require.NoError(t, err)
	}

	impl.proxyMu.Lock()
	cacheSize := len(impl.proxyClients)
	impl.proxyMu.Unlock()

	require.LessOrEqual(t, cacheSize, openAIWSProxyClientCacheMaxEntries, "代理客户端缓存应受容量上限约束")
}

func TestCoderOpenAIWSClientDialer_ProxyClientCacheIdleTTL(t *testing.T) {
	dialer := newDefaultOpenAIWSClientDialer()
	impl, ok := dialer.(*coderOpenAIWSClientDialer)
	require.True(t, ok)

	oldProxy := "http://127.0.0.1:28080"
	_, err := impl.proxyHTTPClient(oldProxy)
	require.NoError(t, err)

	impl.proxyMu.Lock()
	oldEntry := impl.proxyClients[oldProxy]
	require.NotNil(t, oldEntry)
	oldEntry.lastUsedUnixNano = time.Now().Add(-openAIWSProxyClientCacheIdleTTL - time.Minute).UnixNano()
	impl.proxyMu.Unlock()

	// 触发一次新的代理获取，驱动 TTL 清理。
	_, err = impl.proxyHTTPClient("http://127.0.0.1:28081")
	require.NoError(t, err)

	impl.proxyMu.Lock()
	_, exists := impl.proxyClients[oldProxy]
	impl.proxyMu.Unlock()

	require.False(t, exists, "超过空闲 TTL 的代理客户端应被回收")
}

func TestCoderOpenAIWSClientDialer_ProxyTransportTLSHandshakeTimeout(t *testing.T) {
	dialer := newDefaultOpenAIWSClientDialer()
	impl, ok := dialer.(*coderOpenAIWSClientDialer)
	require.True(t, ok)

	client, err := impl.proxyHTTPClient("http://127.0.0.1:38080")
	require.NoError(t, err)
	require.NotNil(t, client)

	transport, ok := client.Transport.(*http.Transport)
	require.True(t, ok)
	require.NotNil(t, transport)
	require.Equal(t, 10*time.Second, transport.TLSHandshakeTimeout)
}

func TestCoderOpenAIWSClientDialer_Dial_EmptyURL(t *testing.T) {
	t.Parallel()

	dialer := newDefaultOpenAIWSClientDialer()
	impl, ok := dialer.(*coderOpenAIWSClientDialer)
	require.True(t, ok)

	conn, status, headers, err := impl.Dial(context.Background(), " ", nil, "")
	require.Error(t, err)
	require.Nil(t, conn)
	require.Equal(t, 0, status)
	require.Nil(t, headers)
}

func TestCoderOpenAIWSClientDialer_Dial_InvalidProxyURL(t *testing.T) {
	t.Parallel()

	dialer := newDefaultOpenAIWSClientDialer()
	impl, ok := dialer.(*coderOpenAIWSClientDialer)
	require.True(t, ok)

	conn, status, headers, err := impl.Dial(context.Background(), "ws://example.com", nil, "://bad-proxy")
	require.Error(t, err)
	require.Nil(t, conn)
	require.Equal(t, 0, status)
	require.Nil(t, headers)
}

func TestCoderOpenAIWSClientConn_NilGuards(t *testing.T) {
	t.Parallel()

	var nilConn *coderOpenAIWSClientConn
	require.ErrorIs(t, nilConn.WriteJSON(context.Background(), map[string]any{"a": 1}), errOpenAIWSConnClosed)
	_, err := nilConn.ReadMessage(context.Background())
	require.ErrorIs(t, err, errOpenAIWSConnClosed)
	_, _, err = nilConn.ReadFrame(context.Background())
	require.ErrorIs(t, err, errOpenAIWSConnClosed)
	require.ErrorIs(t, nilConn.WriteFrame(context.Background(), coderws.MessageText, []byte("x")), errOpenAIWSConnClosed)
	require.ErrorIs(t, nilConn.Ping(context.Background()), errOpenAIWSConnClosed)
	require.NoError(t, nilConn.Close())

	empty := &coderOpenAIWSClientConn{}
	var nilCtx context.Context
	require.ErrorIs(t, empty.WriteJSON(nilCtx, map[string]any{"a": 1}), errOpenAIWSConnClosed)
	_, err = empty.ReadMessage(nilCtx)
	require.ErrorIs(t, err, errOpenAIWSConnClosed)
	_, _, err = empty.ReadFrame(nilCtx)
	require.ErrorIs(t, err, errOpenAIWSConnClosed)
	require.ErrorIs(t, empty.WriteFrame(nilCtx, coderws.MessageText, []byte("x")), errOpenAIWSConnClosed)
	require.ErrorIs(t, empty.Ping(nilCtx), errOpenAIWSConnClosed)
	require.NoError(t, empty.Close())
}

func TestCoderOpenAIWSClientDialer_DialAndConnWrappers(t *testing.T) {
	t.Parallel()

	serverErrCh := make(chan error, 1)
	wsServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			_ = conn.CloseNow()
		}()

		readCtx, cancelRead := context.WithTimeout(r.Context(), 3*time.Second)
		_, _, err = conn.Read(readCtx)
		cancelRead()
		if err != nil {
			serverErrCh <- err
			return
		}

		writeCtx, cancelWrite := context.WithTimeout(r.Context(), 3*time.Second)
		if err = conn.Write(writeCtx, coderws.MessageText, []byte(`{"type":"response.output_text.delta","delta":"ok"}`)); err != nil {
			cancelWrite()
			serverErrCh <- err
			return
		}
		if err = conn.Write(writeCtx, coderws.MessageBinary, []byte{0x01, 0x02, 0x03}); err != nil {
			cancelWrite()
			serverErrCh <- err
			return
		}
		cancelWrite()

		readCtx2, cancelRead2 := context.WithTimeout(r.Context(), 3*time.Second)
		_, payload, err := conn.Read(readCtx2)
		cancelRead2()
		if err != nil {
			serverErrCh <- err
			return
		}
		if len(payload) == 0 {
			serverErrCh <- fmt.Errorf("expected client payload")
			return
		}
		serverErrCh <- nil
	}))
	defer wsServer.Close()

	dialer := newDefaultOpenAIWSClientDialer()
	impl, ok := dialer.(*coderOpenAIWSClientDialer)
	require.True(t, ok)

	dialCtx, cancelDial := context.WithTimeout(context.Background(), 3*time.Second)
	conn, status, headers, err := impl.Dial(
		dialCtx,
		"ws"+strings.TrimPrefix(wsServer.URL, "http"),
		http.Header{"User-Agent": []string{"unit-test-agent/1.0"}},
		"",
	)
	cancelDial()
	require.NoError(t, err)
	require.NotNil(t, conn)
	require.Equal(t, 0, status)
	_ = headers // 成功建连时状态码为 0；headers 仅用于握手失败诊断。

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, conn.WriteJSON(ctx, map[string]any{"type": "response.create"}))

	msg, err := conn.ReadMessage(ctx)
	require.NoError(t, err)
	require.JSONEq(t, `{"type":"response.output_text.delta","delta":"ok"}`, string(msg))

	typedConn, ok := conn.(*coderOpenAIWSClientConn)
	require.True(t, ok)
	msgType, payload, err := typedConn.ReadFrame(ctx)
	require.NoError(t, err)
	require.Equal(t, coderws.MessageBinary, msgType)
	require.Equal(t, []byte{0x01, 0x02, 0x03}, payload)

	require.NoError(t, typedConn.WriteFrame(ctx, coderws.MessageText, []byte(`{"client":"ack"}`)))
	pingCtx, cancelPing := context.WithTimeout(context.Background(), 100*time.Millisecond)
	_ = typedConn.Ping(pingCtx)
	cancelPing()
	require.NoError(t, typedConn.Close())
	require.NoError(t, <-serverErrCh)
}
