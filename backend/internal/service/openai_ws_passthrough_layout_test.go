package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOpenAIWSPassthroughDataPlaneLayout(t *testing.T) {
	t.Parallel()

	serviceDir, err := os.Getwd()
	require.NoError(t, err)
	v2Dir := filepath.Join(serviceDir, "openai_ws_v2")
	forwarderFile := filepath.Join(serviceDir, "openai_ws_forwarder.go")

	requiredFiles := []string{
		filepath.Join(v2Dir, "entry.go"),
		filepath.Join(v2Dir, "caddy_adapter.go"),
		filepath.Join(v2Dir, "passthrough_relay.go"),
	}
	for _, file := range requiredFiles {
		info, err := os.Stat(file)
		require.NoError(t, err)
		require.False(t, info.IsDir())
	}

	content, err := os.ReadFile(forwarderFile)
	require.NoError(t, err)
	forwarder := string(content)

	// openai_ws_forwarder 允许分流入口，不承载 passthrough 数据面函数实现。
	require.Contains(t, forwarder, "proxyResponsesWebSocketV2Passthrough(")
	require.NotContains(t, forwarder, "func runUpstreamToClient(")
	require.NotContains(t, forwarder, "func observeUpstreamMessage(")
	require.NotContains(t, forwarder, "func parseUsageAndAccumulate(")
}
