package pigo

import (
	"encoding/json"
	"math"
	"testing"
)

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
	applyOpenAIResponsesTerminal(*model, response, openAIResponsesResponse{
		ID:     "resp_total_tokens",
		Status: "completed",
		Usage: &openAIResponsesUsage{
			InputTokens:  10,
			OutputTokens: 4,
			TotalTokens:  14,
			InputDetails: openAIResponsesInputTokenDetails{
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

func TestApplyOpenAIResponsesUsagePreservesCacheWriteSplitAndCost(t *testing.T) {
	model := GetModel("openai", "gpt-5.6-sol")
	if model == nil {
		t.Fatal("expected GPT-5.6 Sol model")
	}

	var terminal openAIResponsesResponse
	if err := json.Unmarshal([]byte(`{
		"id":"resp_cache_write",
		"status":"completed",
		"usage":{
			"input_tokens":100,
			"output_tokens":10,
			"total_tokens":110,
			"input_tokens_details":{"cached_tokens":20,"cache_write_tokens":30}
		}
	}`), &terminal); err != nil {
		t.Fatalf("decode terminal Responses usage: %v", err)
	}

	response := &AssistantMessage{}
	applyOpenAIResponsesTerminal(*model, response, terminal, "")

	if !response.UsageReported || response.Usage.Input != 50 || response.Usage.Output != 10 || response.Usage.CacheRead != 20 || response.Usage.CacheWrite != 30 || response.Usage.TotalTokens != 110 {
		t.Fatalf("expected exact Responses cache read/write split, got %+v", response.Usage)
	}
	if math.Abs(response.Usage.Cost.Input-0.0002) > 1e-12 ||
		math.Abs(response.Usage.Cost.Output-0.0002) > 1e-12 ||
		math.Abs(response.Usage.Cost.CacheRead-0.000008) > 1e-12 ||
		math.Abs(response.Usage.Cost.CacheWrite-0.00015) > 1e-12 ||
		math.Abs(response.Usage.Cost.Total-0.000558) > 1e-12 {
		t.Fatalf("expected GPT-5.6 Sol cache-write pricing, got %+v", response.Usage.Cost)
	}
}

func TestOpenAIGPT56ResponsesCacheWritePricingByModel(t *testing.T) {
	tests := []struct {
		modelID string
		want    float64
	}{
		{modelID: "gpt-5.6", want: 0.0005},
		{modelID: "gpt-5.6-sol", want: 0.0005},
		{modelID: "gpt-5.6-terra", want: 0.00025},
		{modelID: "gpt-5.6-luna", want: 0.000025},
	}

	for _, test := range tests {
		t.Run(test.modelID, func(t *testing.T) {
			model := GetModel("openai", test.modelID)
			if model == nil {
				t.Fatalf("expected exact OpenAI model %q", test.modelID)
			}
			response := &AssistantMessage{}
			applyOpenAIResponsesUsage(*model, response, openAIResponsesUsage{
				InputTokens: 100,
				TotalTokens: 100,
				InputDetails: openAIResponsesInputTokenDetails{
					CacheWriteTokens: 100,
				},
			}, "", "")

			if response.Usage.Input != 0 || response.Usage.CacheWrite != 100 {
				t.Fatalf("expected cache writes to be removed from uncached input, got %+v", response.Usage)
			}
			if math.Abs(response.Usage.Cost.CacheWrite-test.want) > 1e-12 || math.Abs(response.Usage.Cost.Total-test.want) > 1e-12 {
				t.Fatalf("cache-write cost = %+v, want %f", response.Usage.Cost, test.want)
			}
		})
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
