package service

import (
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestBuildOpenAIWSHeaders_OAuthNormalization(t *testing.T) {
	gin.SetMode(gin.TestMode)

	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
		Credentials: map[string]any{
			"chatgpt_account_id": "chatgpt_acc_1",
		},
	}

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/openai/v1/responses", nil)
	c.Request.Header.Set("accept-language", "zh-CN")
	c.Request.Header.Set("User-Agent", "custom-client/1.0")
	c.Request.Header.Set("session_id", "sess_hdr")
	c.Request.Header.Set("conversation_id", "conv_hdr")

	headers, resolution := svc.buildOpenAIWSHeaders(
		c,
		account,
		"test_token",
		OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
		false,
		"turn_state_1",
		"turn_meta_1",
		"prompt_cache_fallback",
	)

	require.Equal(t, "Bearer test_token", headers.Get("authorization"))
	require.Equal(t, "zh-CN", headers.Get("accept-language"))
	require.Equal(t, "sess_hdr", headers.Get("session_id"))
	require.Equal(t, "conv_hdr", headers.Get("conversation_id"))
	require.Equal(t, "turn_state_1", headers.Get(openAIWSTurnStateHeader))
	require.Equal(t, "turn_meta_1", headers.Get(openAIWSTurnMetadataHeader))
	require.Equal(t, "chatgpt_acc_1", headers.Get("chatgpt-account-id"))
	require.Equal(t, openAIWSBetaV2Value, headers.Get("OpenAI-Beta"))
	require.Equal(t, codexCLIUserAgent, headers.Get("user-agent"))
	require.Equal(t, "codex_cli_rs", headers.Get("originator"))

	require.Equal(t, "sess_hdr", resolution.SessionID)
	require.Equal(t, "conv_hdr", resolution.ConversationID)
	require.Equal(t, "header_session_id", resolution.SessionSource)
	require.Equal(t, "header_conversation_id", resolution.ConversationSource)
}

func TestBuildOpenAIWSHeaders_APIKeyForceCodexAndV1(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	cfg.Gateway.ForceCodexCLI = true
	svc := &OpenAIGatewayService{cfg: cfg}
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeAPIKey,
		Credentials: map[string]any{
			"user_agent": "my-custom-ua/1.0",
		},
	}

	headers, resolution := svc.buildOpenAIWSHeaders(
		nil,
		account,
		"token_apikey",
		OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocket},
		false,
		"",
		"",
		"pcache_1",
	)

	require.Equal(t, "Bearer token_apikey", headers.Get("authorization"))
	require.Equal(t, openAIWSBetaV1Value, headers.Get("OpenAI-Beta"))
	require.Equal(t, codexCLIUserAgent, headers.Get("user-agent"))
	require.Equal(t, "", headers.Get("originator"))
	require.Equal(t, "pcache_1", headers.Get("session_id"))
	require.Equal(t, "pcache_1", resolution.SessionID)
	require.Equal(t, "prompt_cache_key", resolution.SessionSource)
}

func TestBuildOpenAIWSHeaders_OAuthCodexCLIInputKeepsOriginator(t *testing.T) {
	gin.SetMode(gin.TestMode)

	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	account := &Account{
		Platform: PlatformOpenAI,
		Type:     AccountTypeOAuth,
	}

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/openai/v1/responses", nil)
	c.Request.Header.Set("User-Agent", codexCLIUserAgent)

	headers, _ := svc.buildOpenAIWSHeaders(
		c,
		account,
		"token_oauth",
		OpenAIWSProtocolDecision{Transport: OpenAIUpstreamTransportResponsesWebsocketV2},
		true,
		"",
		"",
		"",
	)

	require.Equal(t, codexCLIUserAgent, headers.Get("user-agent"))
	require.Equal(t, "codex_cli_rs", headers.Get("originator"))
}
