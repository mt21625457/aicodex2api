package service

import (
	"net/url"
	"strings"
)

var claudeEndpointSuffixes = []string{
	"/v1/messages/count_tokens",
	"/v1/messages",
	"/v1",
}

func normalizeClaudeBaseURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return strings.TrimRight(trimmed, "/")
	}

	parsed.RawQuery = ""
	parsed.Fragment = ""

	pathValue := strings.TrimRight(parsed.Path, "/")
	lowerPath := strings.ToLower(pathValue)
	for _, suffix := range claudeEndpointSuffixes {
		if strings.HasSuffix(lowerPath, suffix) {
			pathValue = pathValue[:len(pathValue)-len(suffix)]
			break
		}
	}

	parsed.Path = strings.TrimRight(pathValue, "/")
	parsed.RawPath = ""

	return strings.TrimRight(parsed.String(), "/")
}

func buildClaudeMessagesURL(baseURL string) string {
	if normalized := normalizeClaudeBaseURL(baseURL); normalized != "" {
		return normalized + "/v1/messages?beta=true"
	}
	return claudeAPIURL
}

func buildClaudeCountTokensURL(baseURL string) string {
	if normalized := normalizeClaudeBaseURL(baseURL); normalized != "" {
		return normalized + "/v1/messages/count_tokens?beta=true"
	}
	return claudeAPICountTokensURL
}
