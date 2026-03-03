package service

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
)

var (
	benchmarkOpenAIWSPayloadJSONSink string
	benchmarkOpenAIWSStringSink      string
	benchmarkOpenAIWSBoolSink        bool
	benchmarkOpenAIWSBytesSink       []byte
)

func BenchmarkOpenAIWSForwarderHotPath(b *testing.B) {
	cfg := &config.Config{}
	svc := &OpenAIGatewayService{cfg: cfg}
	account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
	reqBody := benchmarkOpenAIWSHotPathRequest()

	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		payload := svc.buildOpenAIWSCreatePayload(reqBody, account)
		_, _ = applyOpenAIWSRetryPayloadStrategy(payload, 2)
		setOpenAIWSTurnMetadata(payload, `{"trace":"bench","turn":"1"}`)

		benchmarkOpenAIWSStringSink = openAIWSPayloadString(payload, "previous_response_id")
		benchmarkOpenAIWSBoolSink = payload["tools"] != nil
		benchmarkOpenAIWSStringSink = summarizeOpenAIWSPayloadKeySizes(payload, openAIWSPayloadKeySizeTopN)
		benchmarkOpenAIWSStringSink = summarizeOpenAIWSInput(payload["input"])
		benchmarkOpenAIWSPayloadJSONSink = payloadAsJSON(payload)
	}
}

func benchmarkOpenAIWSHotPathRequest() map[string]any {
	tools := make([]map[string]any, 0, 24)
	for i := 0; i < 24; i++ {
		tools = append(tools, map[string]any{
			"type":        "function",
			"name":        fmt.Sprintf("tool_%02d", i),
			"description": "benchmark tool schema",
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"query": map[string]any{"type": "string"},
					"limit": map[string]any{"type": "number"},
				},
				"required": []string{"query"},
			},
		})
	}

	input := make([]map[string]any, 0, 16)
	for i := 0; i < 16; i++ {
		input = append(input, map[string]any{
			"role":    "user",
			"type":    "message",
			"content": fmt.Sprintf("benchmark message %d", i),
		})
	}

	return map[string]any{
		"type":                 "response.create",
		"model":                "gpt-5.3-codex",
		"input":                input,
		"tools":                tools,
		"parallel_tool_calls":  true,
		"previous_response_id": "resp_benchmark_prev",
		"prompt_cache_key":     "bench-cache-key",
		"reasoning":            map[string]any{"effort": "medium"},
		"instructions":         "benchmark instructions",
		"store":                false,
	}
}

func BenchmarkOpenAIWSEventEnvelopeParse(b *testing.B) {
	event := []byte(`{"type":"response.completed","response":{"id":"resp_bench_1","model":"gpt-5.1","usage":{"input_tokens":12,"output_tokens":8}}}`)
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		eventType, responseID, response := parseOpenAIWSEventEnvelope(event)
		benchmarkOpenAIWSStringSink = eventType
		benchmarkOpenAIWSStringSink = responseID
		benchmarkOpenAIWSBoolSink = response.Exists()
	}
}

func BenchmarkOpenAIWSErrorEventFieldReuse(b *testing.B) {
	event := []byte(`{"type":"error","error":{"type":"invalid_request_error","code":"invalid_request","message":"invalid input"}}`)
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		codeRaw, errTypeRaw, errMsgRaw := parseOpenAIWSErrorEventFields(event)
		benchmarkOpenAIWSStringSink, benchmarkOpenAIWSBoolSink = classifyOpenAIWSErrorEventFromRaw(codeRaw, errTypeRaw, errMsgRaw)
		code, errType, errMsg := summarizeOpenAIWSErrorEventFieldsFromRaw(codeRaw, errTypeRaw, errMsgRaw)
		benchmarkOpenAIWSStringSink = code
		benchmarkOpenAIWSStringSink = errType
		benchmarkOpenAIWSStringSink = errMsg
		benchmarkOpenAIWSBoolSink = openAIWSErrorHTTPStatusFromRaw(codeRaw, errTypeRaw) > 0
	}
}

func BenchmarkReplaceOpenAIWSMessageModel_NoMatchFastPath(b *testing.B) {
	event := []byte(`{"type":"response.output_text.delta","delta":"hello world"}`)
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		benchmarkOpenAIWSBytesSink = replaceOpenAIWSMessageModel(event, "gpt-5.1", "custom-model")
	}
}

func BenchmarkReplaceOpenAIWSMessageModel_DualReplace(b *testing.B) {
	event := []byte(`{"type":"response.completed","model":"gpt-5.1","response":{"id":"resp_1","model":"gpt-5.1"}}`)
	b.ReportAllocs()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		benchmarkOpenAIWSBytesSink = replaceOpenAIWSMessageModel(event, "gpt-5.1", "custom-model")
	}
}

// --- Optimization benchmarks ---

var benchmarkOpenAIWSConnSink openAIWSClientConn

func BenchmarkTouchLease_Full(b *testing.B) {
	ctx := &openAIWSIngressContext{}
	ttl := 10 * time.Minute
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx.touchLease(time.Now(), ttl)
	}
}

func BenchmarkMaybeTouchLease_Throttled(b *testing.B) {
	ctx := &openAIWSIngressContext{}
	ttl := 10 * time.Minute
	ctx.touchLease(time.Now(), ttl) // seed the initial touch
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx.maybeTouchLease(ttl)
	}
}

func BenchmarkActiveConn_CachedPath(b *testing.B) {
	conn := &benchmarkOpenAIWSNoopConn{}
	ctx := &openAIWSIngressContext{ownerID: "bench_owner", upstream: conn}
	lease := &openAIWSIngressContextLease{context: ctx, ownerID: "bench_owner"}
	// Prime the cache
	_, _ = lease.activeConn()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkOpenAIWSConnSink, _ = lease.activeConn()
	}
}

func BenchmarkActiveConn_MutexPath(b *testing.B) {
	conn := &benchmarkOpenAIWSNoopConn{}
	ctx := &openAIWSIngressContext{ownerID: "bench_owner", upstream: conn}
	lease := &openAIWSIngressContextLease{context: ctx, ownerID: "bench_owner"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		lease.cachedConn = nil // force mutex path each iteration
		benchmarkOpenAIWSConnSink, _ = lease.activeConn()
	}
}

func BenchmarkParseOpenAIWSEventType_Lightweight(b *testing.B) {
	event := []byte(`{"type":"response.output_text.delta","delta":"hello","response":{"id":"resp_1","model":"gpt-5.1"}}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		et, rid := parseOpenAIWSEventType(event)
		benchmarkOpenAIWSStringSink = et
		benchmarkOpenAIWSStringSink = rid
	}
}

func BenchmarkParseOpenAIWSEventEnvelope_Full(b *testing.B) {
	event := []byte(`{"type":"response.output_text.delta","delta":"hello","response":{"id":"resp_1","model":"gpt-5.1"}}`)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		et, rid, resp := parseOpenAIWSEventEnvelope(event)
		benchmarkOpenAIWSStringSink = et
		benchmarkOpenAIWSStringSink = rid
		benchmarkOpenAIWSBoolSink = resp.Exists()
	}
}

func BenchmarkSessionTurnStateKey_Strconv(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkOpenAIWSStringSink = openAIWSSessionTurnStateKey(int64(i%1000+1), "session_hash_bench")
	}
}

func BenchmarkResponseAccountCacheKey_XXHash(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkOpenAIWSStringSink = openAIWSResponseAccountCacheKey(fmt.Sprintf("resp_bench_%d", i%1000))
	}
}

func BenchmarkIsOpenAIWSTerminalEvent(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkOpenAIWSBoolSink = isOpenAIWSTerminalEvent("response.completed")
	}
}

func BenchmarkIsOpenAIWSTokenEvent(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkOpenAIWSBoolSink = isOpenAIWSTokenEvent("response.output_text.delta")
	}
}

func BenchmarkStateStore_ShardedBindGet(b *testing.B) {
	storeAny := NewOpenAIWSStateStore(nil)
	store, ok := storeAny.(*defaultOpenAIWSStateStore)
	if !ok {
		b.Fatal("expected *defaultOpenAIWSStateStore")
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("resp_%d", i%1000)
		store.BindResponseConn(key, "conn_bench", time.Minute)
		benchmarkOpenAIWSStringSink, benchmarkOpenAIWSBoolSink = store.GetResponseConn(key)
	}
}

func BenchmarkDeriveOpenAISessionHash(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkOpenAIWSStringSink = deriveOpenAISessionHash("session-id-benchmark-value")
	}
}

func BenchmarkDeriveOpenAILegacySessionHash(b *testing.B) {
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		benchmarkOpenAIWSStringSink = deriveOpenAILegacySessionHash("session-id-benchmark-value")
	}
}

type benchmarkOpenAIWSNoopConn struct{}

func (c *benchmarkOpenAIWSNoopConn) WriteJSON(_ context.Context, _ any) error      { return nil }
func (c *benchmarkOpenAIWSNoopConn) ReadMessage(_ context.Context) ([]byte, error) { return nil, nil }
func (c *benchmarkOpenAIWSNoopConn) Ping(_ context.Context) error                  { return nil }
func (c *benchmarkOpenAIWSNoopConn) Close() error                                  { return nil }
