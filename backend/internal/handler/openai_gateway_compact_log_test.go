package handler

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

var handlerStructuredLogCaptureMu sync.Mutex

type handlerInMemoryLogSink struct {
	mu     sync.Mutex
	events []*logger.LogEvent
}

func (s *handlerInMemoryLogSink) WriteLogEvent(event *logger.LogEvent) {
	if event == nil {
		return
	}
	cloned := *event
	if event.Fields != nil {
		cloned.Fields = make(map[string]any, len(event.Fields))
		for k, v := range event.Fields {
			cloned.Fields[k] = v
		}
	}
	s.mu.Lock()
	s.events = append(s.events, &cloned)
	s.mu.Unlock()
}

func (s *handlerInMemoryLogSink) ContainsMessageAtLevel(substr, level string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	wantLevel := strings.ToLower(strings.TrimSpace(level))
	for _, ev := range s.events {
		if ev == nil {
			continue
		}
		if strings.Contains(ev.Message, substr) && strings.ToLower(strings.TrimSpace(ev.Level)) == wantLevel {
			return true
		}
	}
	return false
}

func (s *handlerInMemoryLogSink) ContainsFieldValue(field, substr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ev := range s.events {
		if ev == nil || ev.Fields == nil {
			continue
		}
		if v, ok := ev.Fields[field]; ok && strings.Contains(fmt.Sprint(v), substr) {
			return true
		}
	}
	return false
}

func captureHandlerStructuredLog(t *testing.T) (*handlerInMemoryLogSink, func()) {
	t.Helper()
	handlerStructuredLogCaptureMu.Lock()

	err := logger.Init(logger.InitOptions{
		Level:       "debug",
		Format:      "json",
		ServiceName: "sub2api",
		Environment: "test",
		Output: logger.OutputOptions{
			ToStdout: true,
			ToFile:   false,
		},
		Sampling: logger.SamplingOptions{Enabled: false},
	})
	require.NoError(t, err)

	sink := &handlerInMemoryLogSink{}
	logger.SetSink(sink)
	return sink, func() {
		logger.SetSink(nil)
		handlerStructuredLogCaptureMu.Unlock()
	}
}

func TestIsOpenAIRemoteCompactPath(t *testing.T) {
	require.False(t, isOpenAIRemoteCompactPath(nil))

	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
	require.True(t, isOpenAIRemoteCompactPath(c))

	c.Request = httptest.NewRequest(http.MethodPost, "/responses/compact/", nil)
	require.True(t, isOpenAIRemoteCompactPath(c))

	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	require.False(t, isOpenAIRemoteCompactPath(c))
}

func TestLogOpenAIRemoteCompactOutcome_Succeeded(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logSink, restore := captureHandlerStructuredLog(t)
	defer restore()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.104.0")
	c.Set(opsModelKey, "gpt-5.3-codex")
	c.Set(opsAccountIDKey, int64(123))
	c.Set(opsStreamKey, false)
	c.Set(opsRequestBodyKey, []byte(`{"model":"gpt-5.3-codex","instructions":"compact now","input":[{"type":"text","text":"hi"}]}`))
	c.Set("openai_passthrough", true)
	c.Header("x-request-id", "rid-compact-ok")
	c.Header("Content-Type", "application/json; charset=utf-8")
	c.Status(http.StatusOK)

	h := &OpenAIGatewayHandler{}
	h.logOpenAIRemoteCompactOutcome(c, time.Now().Add(-8*time.Millisecond))

	require.True(t, logSink.ContainsMessageAtLevel("codex.remote_compact.succeeded", "warn"))
	require.True(t, logSink.ContainsFieldValue("compact_outcome", "succeeded"))
	require.True(t, logSink.ContainsFieldValue("status_code", "200"))
	require.True(t, logSink.ContainsFieldValue("path", "/v1/responses/compact"))
	require.True(t, logSink.ContainsFieldValue("request_model", "gpt-5.3-codex"))
	require.True(t, logSink.ContainsFieldValue("account_id", "123"))
	require.True(t, logSink.ContainsFieldValue("upstream_request_id", "rid-compact-ok"))
	require.True(t, logSink.ContainsFieldValue("request_stream", "false"))
	require.True(t, logSink.ContainsFieldValue("passthrough", "true"))
	require.True(t, logSink.ContainsFieldValue("request_body_preview", `"model":"gpt-5.3-codex"`))
	require.True(t, logSink.ContainsFieldValue("request_input_items", "1"))
	require.True(t, logSink.ContainsFieldValue("request_instructions_bytes", "11"))
	require.True(t, logSink.ContainsFieldValue("response_content_type", "application/json"))
}

func TestLogOpenAIRemoteCompactOutcome_Failed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logSink, restore := captureHandlerStructuredLog(t)
	defer restore()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/responses/compact", nil)
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.104.0")
	c.Set(opsStreamKey, false)
	c.Set(opsRequestBodyKey, []byte(`{"model":"gpt-5.3-codex","input":[{"type":"text","text":"hi"}]}`))
	c.Set("openai_passthrough", true)
	c.Set(service.OpsUpstreamErrorMessageKey, "Upstream returned invalid compact response JSON")
	c.Set(service.OpsUpstreamErrorDetailKey, "not-json")
	c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{Kind: "http_error", Message: "Upstream returned invalid compact response JSON", Detail: "not-json", Passthrough: true, UpstreamStatusCode: 200}})
	w := acquireOpsCaptureWriter(c.Writer)
	defer releaseOpsCaptureWriter(w)
	c.Writer = w
	c.Header("Content-Type", "application/json; charset=utf-8")
	c.Status(http.StatusBadGateway)
	_, _ = c.Writer.Write([]byte(`{"error":{"type":"upstream_error","message":"Upstream returned invalid compact response JSON"}}`))

	h := &OpenAIGatewayHandler{}
	h.logOpenAIRemoteCompactOutcome(c, time.Now())

	require.True(t, logSink.ContainsMessageAtLevel("codex.remote_compact.failed", "warn"))
	require.True(t, logSink.ContainsFieldValue("compact_outcome", "failed"))
	require.True(t, logSink.ContainsFieldValue("status_code", "502"))
	require.True(t, logSink.ContainsFieldValue("path", "/responses/compact"))
	require.True(t, logSink.ContainsFieldValue("request_stream", "false"))
	require.True(t, logSink.ContainsFieldValue("passthrough", "true"))
	require.True(t, logSink.ContainsFieldValue("upstream_error_message", "invalid compact response JSON"))
	require.True(t, logSink.ContainsFieldValue("upstream_error_detail", "not-json"))
	require.True(t, logSink.ContainsFieldValue("upstream_error_count", "1"))
	require.True(t, logSink.ContainsFieldValue("response_body_preview", `"type":"upstream_error"`))
}

func TestLogOpenAIRemoteCompactOutcome_NonCompactSkips(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logSink, restore := captureHandlerStructuredLog(t)
	defer restore()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Status(http.StatusOK)

	h := &OpenAIGatewayHandler{}
	h.logOpenAIRemoteCompactOutcome(c, time.Now())

	require.False(t, logSink.ContainsMessageAtLevel("codex.remote_compact.succeeded", "info"))
	require.False(t, logSink.ContainsMessageAtLevel("codex.remote_compact.failed", "warn"))
}

func TestOpenAIResponses_CompactUnauthorizedLogsFailed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	logSink, restore := captureHandlerStructuredLog(t)
	defer restore()

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(`{"model":"gpt-5.3-codex"}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Request.Header.Set("User-Agent", "codex_cli_rs/0.104.0")

	h := &OpenAIGatewayHandler{}
	h.Responses(c)

	require.Equal(t, http.StatusUnauthorized, rec.Code)
	require.True(t, logSink.ContainsMessageAtLevel("codex.remote_compact.failed", "warn"))
	require.True(t, logSink.ContainsFieldValue("status_code", "401"))
	require.True(t, logSink.ContainsFieldValue("path", "/v1/responses/compact"))
}
