package pigo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestChatUsageOverflowPreservesPrior(t *testing.T) {
	max := int(^uint(0) >> 1)
	usage := openAICompletionsUsage{PromptTokens: 8, CompletionTokens: 3}
	usage.PromptTokensDetails.CachedTokens = &max
	usage.PromptTokensDetails.CacheWriteTokens = &max
	prior := Usage{Input: 5, Output: 2, TotalTokens: 7}
	response := AssistantMessage{Usage: prior, UsageReported: true}
	err := applyOpenAICompletionsUsage(&response, Model{}, usage)
	if err == nil {
		t.Fatal("overflow accepted")
	}
	if !reflect.DeepEqual(response.Usage, prior) {
		t.Fatalf("invalid snapshot replaced prior: %+v", response.Usage)
	}
}

func TestChatUsageCheckedBounds(t *testing.T) {
	max := int(^uint(0) >> 1)
	neg := -1
	two := 2
	one := 1
	for _, tc := range []struct {
		name         string
		usage        openAICompletionsUsage
		valid        bool
		input, total int
	}{
		{"zero", openAICompletionsUsage{}, true, 0, 0},
		{"max", openAICompletionsUsage{PromptTokens: max - 1, CompletionTokens: 1}, true, max - 1, max},
		{"overflow", openAICompletionsUsage{PromptTokens: max, CompletionTokens: 1}, false, 0, 0},
		{"negative", openAICompletionsUsage{PromptTokens: -1}, false, 0, 0},
		{"ignored-total-negative", openAICompletionsUsage{TotalTokens: -1}, false, 0, 0},
		{"reasoning-negative", openAICompletionsUsage{CompletionTokensDetails: struct {
			ReasoningTokens int `json:"reasoning_tokens"`
		}{-1}}, false, 0, 0},
		{"hidden-alias-negative", openAICompletionsUsage{CachedTokens: &neg, PromptCacheHitTokens: &one}, false, 0, 0},
		{"small-inconsistent", openAICompletionsUsage{PromptTokens: 1, CompletionTokens: 1, CachedTokens: &two}, true, 0, 3},
		{"explicit-miss", openAICompletionsUsage{PromptTokens: 1, CompletionTokens: 1, PromptCacheMissTokens: 3, CachedTokens: &two}, true, 3, 6},
		{"provider-total-not-equality", openAICompletionsUsage{PromptTokens: 3, CompletionTokens: 2, TotalTokens: 1}, true, 3, 5},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var response AssistantMessage
			err := applyOpenAICompletionsUsage(&response, Model{}, tc.usage)
			if (err == nil) != tc.valid {
				t.Fatalf("err=%v", err)
			}
			if tc.valid {
				if response.Usage.Input != tc.input || response.Usage.TotalTokens != tc.total || !response.UsageReported {
					t.Fatal(response)
				}
			} else if response.UsageReported {
				t.Fatal("invalid marked reported")
			}
		})
	}
}

func TestChatInvalidUsageStreamRetainsEvidence(t *testing.T) {
	max := int(^uint(0) >> 1)
	for _, choiceUsage := range []bool{false, true} {
		for _, bad := range []string{fmt.Sprintf(`{"prompt_tokens":%d,"completion_tokens":1}`, max), `{"prompt_tokens":-1}`, `{"prompt_tokens":9223372036854775808}`, `{"cached_tokens":-1}`} {
			t.Run(fmt.Sprintf("%v/%s", choiceUsage, bad), func(t *testing.T) {
				count := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					count++
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, `data: {"id":"response_fixture","choices":[{"delta":{"content":"partial"}}],"usage":{"prompt_tokens":5,"completion_tokens":2}}

`)
					if choiceUsage {
						fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{},\"usage\":%s}]}\n\n", bad)
					} else {
						fmt.Fprintf(w, "data: {\"choices\":[],\"usage\":%s}\n\n", bad)
					}
				}))
				defer server.Close()
				model := Model{ID: "fixture", Provider: "openai", API: "openai-completions", BaseURL: server.URL}
				stream := Stream(model, Context{}, ProviderStreamOptions{APIKey: "fixture"})
				errors, done := 0, 0
				for e := range stream.Events() {
					if e.Type == AssistantMessageEventError {
						errors++
					}
					if e.Type == AssistantMessageEventDone {
						done++
					}
				}
				result := stream.Result()
				failure, ok := ProviderFailureOf(result)
				if !ok || failure.Kind != ProviderFailureParse || failure.Reason != "invalid_usage" || result.StopReason != StopReasonError || errors != 1 || done != 0 || count != 1 {
					t.Fatalf("failure%+v result%+v events%d/%d count%d", failure, result, errors, done, count)
				}
				if result.Usage.Input != 5 || result.Usage.Output != 2 || !result.UsageReported || len(result.Content) != 1 || result.Content[0].(TextContent).Text != "partial" || result.Model != "fixture" || result.ResponseID != "response_fixture" {
					t.Fatal(result)
				}
				if !strings.Contains(result.ErrorMessage, "invalid chat usage") {
					t.Fatal(result.ErrorMessage)
				}
			})
		}
	}
}

func TestChatNegativeUsageFields(t *testing.T) {
	for _, raw := range []string{`{"completion_tokens":-1}`, `{"total_tokens":-1}`, `{"prompt_cache_miss_tokens":-1}`, `{"cache_read_input_tokens":-1}`, `{"cache_creation_input_tokens":-1}`, `{"prompt_tokens_details":{"cached_tokens":-1}}`, `{"prompt_tokens_details":{"cache_write_tokens":-1}}`, `{"prompt_tokens_details":{"cache_creation_tokens":-1}}`, `{"prompt_cache_hit_tokens":-1}`} {
		var usage openAICompletionsUsage
		if err := json.Unmarshal([]byte(raw), &usage); err != nil {
			t.Fatal(err)
		}
		var result AssistantMessage
		if err := applyOpenAICompletionsUsage(&result, Model{}, usage); err == nil || result.UsageReported {
			t.Fatalf("accepted %s", raw)
		}
	}
}
