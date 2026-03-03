package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateOpenAIWSModeExtraValues(t *testing.T) {
	t.Parallel()

	t.Run("accepts supported mode values", func(t *testing.T) {
		t.Parallel()

		err := validateOpenAIWSModeExtraValues(map[string]any{
			"openai_oauth_responses_websockets_v2_mode":  " passthrough ",
			"openai_apikey_responses_websockets_v2_mode": "CTX_POOL",
		})
		require.NoError(t, err)
	})

	t.Run("accepts missing mode keys", func(t *testing.T) {
		t.Parallel()

		err := validateOpenAIWSModeExtraValues(map[string]any{
			"codex_cli_only": true,
		})
		require.NoError(t, err)
	})

	t.Run("rejects invalid mode value", func(t *testing.T) {
		t.Parallel()

		err := validateOpenAIWSModeExtraValues(map[string]any{
			"openai_oauth_responses_websockets_v2_mode": "shared",
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "INVALID_OPENAI_WS_MODE")
		require.Contains(t, err.Error(), "off, ctx_pool, passthrough")
	})

	t.Run("rejects non-string mode value", func(t *testing.T) {
		t.Parallel()

		err := validateOpenAIWSModeExtraValues(map[string]any{
			"openai_apikey_responses_websockets_v2_mode": true,
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "INVALID_OPENAI_WS_MODE")
		require.Contains(t, err.Error(), "must be a string")
	})
}
