package service

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeClaudeBaseURL(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "root host unchanged", raw: "https://api.anthropic.com", want: "https://api.anthropic.com"},
		{name: "trim trailing slash", raw: "https://api.anthropic.com/", want: "https://api.anthropic.com"},
		{name: "strip v1 suffix", raw: "https://api.anthropic.com/v1", want: "https://api.anthropic.com"},
		{name: "strip messages suffix", raw: "https://api.anthropic.com/v1/messages", want: "https://api.anthropic.com"},
		{name: "strip count tokens suffix", raw: "https://api.anthropic.com/v1/messages/count_tokens", want: "https://api.anthropic.com"},
		{name: "preserve proxy prefix", raw: "https://proxy.example.com/anthropic/v1/messages", want: "https://proxy.example.com/anthropic"},
		{name: "drop query and preserve proxy prefix", raw: "https://proxy.example.com/anthropic/v1/messages?foo=1", want: "https://proxy.example.com/anthropic"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, normalizeClaudeBaseURL(tt.raw))
		})
	}
}

func TestBuildClaudeMessagesURL(t *testing.T) {
	require.Equal(t, "https://api.anthropic.com/v1/messages?beta=true", buildClaudeMessagesURL("https://api.anthropic.com/v1/messages"))
	require.Equal(t, "https://proxy.example.com/anthropic/v1/messages?beta=true", buildClaudeMessagesURL("https://proxy.example.com/anthropic/v1"))
}

func TestBuildClaudeCountTokensURL(t *testing.T) {
	require.Equal(t, "https://api.anthropic.com/v1/messages/count_tokens?beta=true", buildClaudeCountTokensURL("https://api.anthropic.com/v1/messages/count_tokens"))
	require.Equal(t, "https://proxy.example.com/anthropic/v1/messages/count_tokens?beta=true", buildClaudeCountTokensURL("https://proxy.example.com/anthropic/v1/messages"))
}
