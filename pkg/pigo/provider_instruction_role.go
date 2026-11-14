package pigo

import (
	"net/url"
	"strings"
)

// Unknown OpenAI-compatible endpoints must opt in to the developer role.
// Retain the established default for reasoning models on the native OpenAI
// endpoint. Explicit compatibility metadata always overrides this default.
func defaultOpenAIDeveloperRole(model Model) bool {
	if !model.Reasoning {
		return false
	}
	base := strings.TrimSpace(model.BaseURL)
	if base == "" {
		return true // Both OpenAI adapters default to api.openai.com.
	}
	u, err := url.Parse(base)
	if err != nil || u.Scheme != "https" {
		return false
	}
	return strings.EqualFold(u.Hostname(), "api.openai.com") ||
		(model.API == "openai-codex-responses" && strings.EqualFold(u.Hostname(), "chatgpt.com"))
}
