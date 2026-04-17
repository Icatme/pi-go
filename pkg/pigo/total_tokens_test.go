package pigo

import "testing"

func TestApplyAnthropicUsageComputesTotalTokensFromComponents(t *testing.T) {
	model := GetModel("kimi-coding", "kimi-k2-thinking")
	if model == nil {
		t.Fatal("expected kimi model")
	}

	response := &AssistantMessage{}
	applyAnthropicUsage(response, *model, anthropicUsage{
		InputTokens:         10,
		OutputTokens:        4,
		CacheReadTokens:     3,
		CacheCreationTokens: 7,
	})

	if response.Usage.TotalTokens != 24 {
		t.Fatalf("expected total tokens 24, got %+v", response.Usage)
	}
	if response.Usage.Input+response.Usage.Output+response.Usage.CacheRead+response.Usage.CacheWrite != response.Usage.TotalTokens {
		t.Fatalf("expected totalTokens to equal sum of components, got %+v", response.Usage)
	}
}

func TestApplyOpenAICodexTerminalPreservesTotalTokensAndCachedSplit(t *testing.T) {
	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected codex model")
	}

	response := &AssistantMessage{}
	applyOpenAICodexTerminal(*model, response, openAICodexResponse{
		ID:     "resp_total_tokens",
		Status: "completed",
		Usage: openAICodexUsage{
			InputTokens:  10,
			OutputTokens: 4,
			TotalTokens:  14,
			InputDetails: openAICodexInputTokenDetails{
				CachedTokens: 3,
			},
		},
	}, "")

	if response.Usage.Input != 7 || response.Usage.CacheRead != 3 || response.Usage.Output != 4 {
		t.Fatalf("expected cached split to be preserved, got %+v", response.Usage)
	}
	if response.Usage.TotalTokens != 14 {
		t.Fatalf("expected terminal total tokens to be preserved, got %+v", response.Usage)
	}
	if response.Usage.Input+response.Usage.Output+response.Usage.CacheRead+response.Usage.CacheWrite != response.Usage.TotalTokens {
		t.Fatalf("expected totalTokens to equal sum of components, got %+v", response.Usage)
	}
}

func TestResolveAnthropicCacheControlLongAddsTTLOnlyForOfficialBaseURL(t *testing.T) {
	cacheControl := resolveAnthropicCacheControl("https://api.anthropic.com", CacheRetentionLong)
	if cacheControl == nil || cacheControl.Type != "ephemeral" || cacheControl.TTL != "1h" {
		t.Fatalf("expected official anthropic base url to receive ttl, got %+v", cacheControl)
	}

	cacheControl = resolveAnthropicCacheControl("https://api.kimi.com/coding", CacheRetentionLong)
	if cacheControl == nil || cacheControl.Type != "ephemeral" || cacheControl.TTL != "" {
		t.Fatalf("expected kimi base url to omit ttl, got %+v", cacheControl)
	}
}
