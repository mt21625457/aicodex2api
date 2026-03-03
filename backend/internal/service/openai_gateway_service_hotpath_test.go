package service

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestExtractOpenAIRequestMetaFromBody(t *testing.T) {
	tests := []struct {
		name          string
		body          []byte
		wantModel     string
		wantStream    bool
		wantPromptKey string
	}{
		{
			name:          "完整字段",
			body:          []byte(`{"model":"gpt-5","stream":true,"prompt_cache_key":" ses-1 "}`),
			wantModel:     "gpt-5",
			wantStream:    true,
			wantPromptKey: "ses-1",
		},
		{
			name:          "缺失可选字段",
			body:          []byte(`{"model":"gpt-4"}`),
			wantModel:     "gpt-4",
			wantStream:    false,
			wantPromptKey: "",
		},
		{
			name:          "空请求体",
			body:          nil,
			wantModel:     "",
			wantStream:    false,
			wantPromptKey: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model, stream, promptKey := extractOpenAIRequestMetaFromBody(tt.body)
			require.Equal(t, tt.wantModel, model)
			require.Equal(t, tt.wantStream, stream)
			require.Equal(t, tt.wantPromptKey, promptKey)
		})
	}
}

func TestExtractOpenAIReasoningEffortFromBody(t *testing.T) {
	tests := []struct {
		name      string
		body      []byte
		model     string
		wantNil   bool
		wantValue string
	}{
		{
			name:      "优先读取 reasoning.effort",
			body:      []byte(`{"reasoning":{"effort":"medium"}}`),
			model:     "gpt-5-high",
			wantNil:   false,
			wantValue: "medium",
		},
		{
			name:      "兼容 reasoning_effort",
			body:      []byte(`{"reasoning_effort":"x-high"}`),
			model:     "",
			wantNil:   false,
			wantValue: "xhigh",
		},
		{
			name:    "minimal 归一化为空",
			body:    []byte(`{"reasoning":{"effort":"minimal"}}`),
			model:   "gpt-5-high",
			wantNil: true,
		},
		{
			name:      "缺失字段时从模型后缀推导",
			body:      []byte(`{"input":"hi"}`),
			model:     "gpt-5-high",
			wantNil:   false,
			wantValue: "high",
		},
		{
			name:    "未知后缀不返回",
			body:    []byte(`{"input":"hi"}`),
			model:   "gpt-5-unknown",
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractOpenAIReasoningEffortFromBody(tt.body, tt.model)
			if tt.wantNil {
				require.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			require.Equal(t, tt.wantValue, *got)
		})
	}
}

func TestGetOpenAIRequestBodyMap_UsesContextCache(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	cached := map[string]any{"model": "cached-model", "stream": true}
	c.Set(OpenAIParsedRequestBodyKey, cached)

	got, err := getOpenAIRequestBodyMap(c, []byte(`{invalid-json`))
	require.NoError(t, err)
	require.Equal(t, cached, got)
}

func TestGetOpenAIRequestBodyMap_ParseErrorWithoutCache(t *testing.T) {
	_, err := getOpenAIRequestBodyMap(nil, []byte(`{invalid-json`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "parse request")
}

func TestGetOpenAIRequestBodyMap_WriteBackContextCache(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	got, err := getOpenAIRequestBodyMap(c, []byte(`{"model":"gpt-5","stream":true}`))
	require.NoError(t, err)
	require.Equal(t, "gpt-5", got["model"])

	cached, ok := c.Get(OpenAIParsedRequestBodyKey)
	require.True(t, ok)
	cachedMap, ok := cached.(map[string]any)
	require.True(t, ok)
	require.Equal(t, got, cachedMap)
}

// --- extractOpenAIRequestMeta context 缓存测试 ---

func TestExtractOpenAIRequestMeta_CacheHit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	// 预设缓存（Handler 层已提取所有字段，包括 PromptCacheKey）
	c.Set(OpenAIRequestMetaKey, &OpenAIRequestMeta{
		Model:          "gpt-5",
		Stream:         true,
		PromptCacheKey: "key-1",
	})

	body := []byte(`{"model":"gpt-4","stream":false,"prompt_cache_key":"key-other"}`)
	model, stream, promptKey := extractOpenAIRequestMeta(c, body)

	// 应返回缓存值而非 body 中的值
	require.Equal(t, "gpt-5", model)
	require.True(t, stream)
	require.Equal(t, "key-1", promptKey)
}

func TestExtractOpenAIRequestMeta_CacheHit_PromptCacheKeyFromHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	// Handler 层已提取 PromptCacheKey，meta 设置后只读不写
	meta := &OpenAIRequestMeta{Model: "gpt-5", Stream: false, PromptCacheKey: "pk-abc"}
	c.Set(OpenAIRequestMetaKey, meta)

	body := []byte(`{"model":"gpt-4","prompt_cache_key":"pk-other"}`)

	// 应返回缓存中的值（Handler 层提取），而非 body 中的值
	_, _, promptKey1 := extractOpenAIRequestMeta(c, body)
	require.Equal(t, "pk-abc", promptKey1)

	// 多次调用结果一致
	_, _, promptKey2 := extractOpenAIRequestMeta(c, body)
	require.Equal(t, "pk-abc", promptKey2)
}

func TestExtractOpenAIRequestMeta_CacheHit_EmptyBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	c.Set(OpenAIRequestMetaKey, &OpenAIRequestMeta{
		Model:  "gpt-5",
		Stream: true,
	})

	// body 为空时不应 panic，prompt_cache_key 应为空
	model, stream, promptKey := extractOpenAIRequestMeta(c, nil)
	require.Equal(t, "gpt-5", model)
	require.True(t, stream)
	require.Equal(t, "", promptKey)
}

func TestExtractOpenAIRequestMeta_FallbackToBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	// 不设缓存，应回退到 body 解析
	body := []byte(`{"model":"gpt-4o","stream":true,"prompt_cache_key":"ses-2"}`)
	model, stream, promptKey := extractOpenAIRequestMeta(c, body)

	require.Equal(t, "gpt-4o", model)
	require.True(t, stream)
	require.Equal(t, "ses-2", promptKey)
}

func TestExtractOpenAIRequestMeta_NilContext(t *testing.T) {
	body := []byte(`{"model":"gpt-4","stream":false,"prompt_cache_key":"k"}`)
	model, stream, promptKey := extractOpenAIRequestMeta(nil, body)

	require.Equal(t, "gpt-4", model)
	require.False(t, stream)
	require.Equal(t, "k", promptKey)
}

func TestExtractOpenAIRequestMeta_InvalidCacheType(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	// 缓存类型错误，应回退到 body 解析
	c.Set(OpenAIRequestMetaKey, "invalid-type")

	body := []byte(`{"model":"gpt-4o","stream":true}`)
	model, stream, _ := extractOpenAIRequestMeta(c, body)

	require.Equal(t, "gpt-4o", model)
	require.True(t, stream)
}

func TestExtractOpenAIRequestMeta_NilCacheValue(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	c.Set(OpenAIRequestMetaKey, (*OpenAIRequestMeta)(nil))

	body := []byte(`{"model":"gpt-5","stream":false}`)
	model, stream, _ := extractOpenAIRequestMeta(c, body)

	require.Equal(t, "gpt-5", model)
	require.False(t, stream)
}

func TestOpenAIRequestMeta_Fields(t *testing.T) {
	meta := &OpenAIRequestMeta{
		Model:          "gpt-5",
		Stream:         true,
		PromptCacheKey: "pk",
	}
	require.Equal(t, "gpt-5", meta.Model)
	require.True(t, meta.Stream)
	require.Equal(t, "pk", meta.PromptCacheKey)
}
