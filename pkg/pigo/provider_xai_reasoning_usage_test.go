package pigo

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestXAIChatReasoningUsageOfficialExample(t *testing.T) {
	var usage openAICompletionsUsage
	if err := json.Unmarshal([]byte(`{"prompt_tokens":32,"completion_tokens":9,"total_tokens":135,"prompt_tokens_details":{"cached_tokens":6},"completion_tokens_details":{"reasoning_tokens":94}}`), &usage); err != nil {
		t.Fatal(err)
	}
	var response AssistantMessage
	applyOpenAICompletionsUsage(&response, Model{Provider: "xai", API: "openai-completions"}, usage)
	if response.Usage.Input != 26 || response.Usage.Output != 103 || response.Usage.CacheRead != 6 || response.Usage.TotalTokens != 135 {
		t.Fatalf("official xAI usage lost reasoning: %+v", response.Usage)
	}
}
func TestXAIChatReasoningUsageScopeAndAmbiguity(t *testing.T) {
	for _, tc := range []struct {
		name     string
		provider Provider
		api      API
		raw      string
		output   int
	}{{"official", "xai", "openai-completions", `{"prompt_tokens":32,"completion_tokens":9,"total_tokens":135,"completion_tokens_details":{"reasoning_tokens":94}}`, 103}, {"included", "xai", "openai-completions", `{"prompt_tokens":32,"completion_tokens":103,"total_tokens":135,"completion_tokens_details":{"reasoning_tokens":94}}`, 103}, {"missing total", "xai", "openai-completions", `{"prompt_tokens":32,"completion_tokens":9,"completion_tokens_details":{"reasoning_tokens":94}}`, 9}, {"inconsistent", "xai", "openai-completions", `{"prompt_tokens":32,"completion_tokens":9,"total_tokens":100,"completion_tokens_details":{"reasoning_tokens":94}}`, 9}, {"negative reasoning", "xai", "openai-completions", `{"prompt_tokens":32,"completion_tokens":9,"total_tokens":135,"completion_tokens_details":{"reasoning_tokens":-94}}`, 9}, {"openai", "openai", "openai-completions", `{"prompt_tokens":32,"completion_tokens":9,"total_tokens":135,"completion_tokens_details":{"reasoning_tokens":94}}`, 9}, {"commandcode", "commandcode", "openai-completions", `{"prompt_tokens":32,"completion_tokens":9,"total_tokens":135,"completion_tokens_details":{"reasoning_tokens":94}}`, 9}, {"responses", "xai", "openai-responses", `{"prompt_tokens":32,"completion_tokens":9,"total_tokens":135,"completion_tokens_details":{"reasoning_tokens":94}}`, 9}} {
		t.Run(tc.name, func(t *testing.T) {
			var u openAICompletionsUsage
			if e := json.Unmarshal([]byte(tc.raw), &u); e != nil {
				t.Fatal(e)
			}
			var r AssistantMessage
			err := applyOpenAICompletionsUsage(&r, Model{Provider: tc.provider, API: tc.api}, u)
			if tc.name == "negative reasoning" {
				if err == nil || r.UsageReported {
					t.Fatal("negative reasoning accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if r.Usage.Output != tc.output {
				t.Fatalf("usage %+v want output %d", r.Usage, tc.output)
			}
		})
	}
}
func TestXAIChatReasoningUsageRequiresCounts(t *testing.T) {
	for _, tc := range []struct {
		name, raw string
		output    int
	}{{"missing prompt", `{"completion_tokens":0,"total_tokens":94,"completion_tokens_details":{"reasoning_tokens":94}}`, 0}, {"missing completion", `{"prompt_tokens":0,"total_tokens":94,"completion_tokens_details":{"reasoning_tokens":94}}`, 0}, {"null prompt", `{"prompt_tokens":null,"completion_tokens":0,"total_tokens":94,"completion_tokens_details":{"reasoning_tokens":94}}`, 0}, {"null completion", `{"prompt_tokens":0,"completion_tokens":null,"total_tokens":94,"completion_tokens_details":{"reasoning_tokens":94}}`, 0}, {"null total", `{"prompt_tokens":0,"completion_tokens":0,"total_tokens":null,"completion_tokens_details":{"reasoning_tokens":94}}`, 0}, {"explicit zero", `{"prompt_tokens":0,"completion_tokens":0,"total_tokens":94,"completion_tokens_details":{"reasoning_tokens":94}}`, 94}} {
		t.Run(tc.name, func(t *testing.T) {
			var u openAICompletionsUsage
			if e := json.Unmarshal([]byte(tc.raw), &u); e != nil {
				t.Fatal(e)
			}
			var r AssistantMessage
			applyOpenAICompletionsUsage(&r, Model{Provider: "xai", API: "openai-completions"}, u)
			if r.Usage.Output != tc.output {
				t.Fatalf("usage %+v", r.Usage)
			}
		})
	}
}
func TestXAIChatReasoningUsageStreamAndCost(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\n")
		for range 2 {
			fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10120,\"completion_tokens\":1499,\"total_tokens\":14428,\"prompt_tokens_details\":{\"cached_tokens\":128},\"completion_tokens_details\":{\"reasoning_tokens\":2809}}}\n\n")
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	model := Model{ID: "fixture", Provider: "xai", API: "openai-completions", BaseURL: server.URL, MaxTokens: 100, Cost: UsageCost{Input: 1.25, Output: 2.5, CacheRead: .2}}
	result := Complete(model, Context{}, ProviderStreamOptions{APIKey: "fixture"})
	u := result.Usage
	if result.StopReason != StopReasonStop || u.Input != 9992 || u.CacheRead != 128 || u.Output != 4308 || u.TotalTokens != 14428 || math.Abs(u.Cost.Total-.0232856) > 1e-12 {
		t.Fatalf("result=%+v usage=%+v", result, u)
	}
}
func TestXAIChatReasoningUsageSubtractionBoundary(t *testing.T) {
	max := int(^uint(0) >> 1)
	for _, tc := range []struct {
		name                                       string
		prompt, completion, total, reasoning, want int
	}{{"max exact", max - 10, 2, max, 8, 10}, {"prompt exceeds total", max, 2, 5, 8, 2}, {"completion exceeds remainder", max - 2, 3, max, 8, 3}, {"negative prompt", -1, 2, 9, 8, 2}, {"negative completion", 1, -2, 7, 8, -2}, {"negative total", 1, 2, -3, 8, 2}} {
		t.Run(tc.name, func(t *testing.T) {
			raw := fmt.Sprintf(`{"prompt_tokens":%d,"completion_tokens":%d,"total_tokens":%d,"completion_tokens_details":{"reasoning_tokens":%d}}`, tc.prompt, tc.completion, tc.total, tc.reasoning)
			var u openAICompletionsUsage
			if e := json.Unmarshal([]byte(raw), &u); e != nil {
				t.Fatal(e)
			}
			var r AssistantMessage
			err := applyOpenAICompletionsUsage(&r, Model{Provider: "xai", API: "openai-completions"}, u)
			// Nonnegative but unrepresentable totals, and negative counts, are
			// rejected before xAI reconciliation rather than recorded wrapped.
			if tc.name != "max exact" {
				if err == nil || r.UsageReported {
					t.Fatal("invalid arithmetic accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if r.Usage.Output != tc.want {
				t.Fatalf("got %d want%d", r.Usage.Output, tc.want)
			}
		})
	}
}
func TestCompletionsUsagePresencePreservesGenericZeroDefaults(t *testing.T) {
	for _, tc := range []struct {
		raw           string
		input, output int
	}{{`{"completion_tokens":9,"total_tokens":103,"completion_tokens_details":{"reasoning_tokens":94}}`, 0, 9}, {`{"prompt_tokens":null,"completion_tokens":9,"total_tokens":103,"completion_tokens_details":{"reasoning_tokens":94}}`, 0, 9}, {`{"prompt_tokens":32,"completion_tokens":null,"total_tokens":126,"completion_tokens_details":{"reasoning_tokens":94}}`, 32, 0}, {`{"prompt_tokens":32,"completion_tokens":9,"total_tokens":null,"completion_tokens_details":{"reasoning_tokens":94}}`, 32, 9}} {
		var u openAICompletionsUsage
		if e := json.Unmarshal([]byte(tc.raw), &u); e != nil {
			t.Fatal(e)
		}
		var r AssistantMessage
		applyOpenAICompletionsUsage(&r, Model{Provider: "openai", API: "openai-completions"}, u)
		if r.Usage.Input != tc.input || r.Usage.Output != tc.output || r.Usage.TotalTokens != tc.input+tc.output {
			t.Fatalf("generic defaults changed: %+v", r.Usage)
		}
	}
}
func TestXAIChatReasoningUsageRequiresConsistentCacheSplit(t *testing.T) {
	max := int(^uint(0) >> 1)
	for _, raw := range []string{fmt.Sprintf(`{"prompt_tokens":0,"completion_tokens":0,"total_tokens":%d,"completion_tokens_details":{"reasoning_tokens":%d},"prompt_tokens_details":{"cached_tokens":1}}`, max, max), `{"prompt_tokens":32,"completion_tokens":9,"total_tokens":135,"prompt_cache_miss_tokens":40,"prompt_tokens_details":{"cached_tokens":6},"completion_tokens_details":{"reasoning_tokens":94}}`, `{"prompt_tokens":32,"completion_tokens":9,"total_tokens":135,"prompt_tokens_details":{"cached_tokens":32,"cache_write_tokens":1},"completion_tokens_details":{"reasoning_tokens":94}}`} {
		var u openAICompletionsUsage
		if e := json.Unmarshal([]byte(raw), &u); e != nil {
			t.Fatal(e)
		}
		var xai, generic AssistantMessage
		applyOpenAICompletionsUsage(&xai, Model{Provider: "xai", API: "openai-completions"}, u)
		applyOpenAICompletionsUsage(&generic, Model{Provider: "openai", API: "openai-completions"}, u)
		if xai.Usage != generic.Usage {
			t.Fatalf("ambiguous cache incorrectly reconciled: xai=%+v generic=%+v", xai.Usage, generic.Usage)
		}
	}
}
func TestXAIChatReasoningUsageConsistentCacheOverride(t *testing.T) {
	var u openAICompletionsUsage
	if e := json.Unmarshal([]byte(`{"prompt_tokens":32,"completion_tokens":9,"total_tokens":135,"prompt_cache_miss_tokens":24,"prompt_tokens_details":{"cached_tokens":6,"cache_write_tokens":2},"completion_tokens_details":{"reasoning_tokens":94}}`), &u); e != nil {
		t.Fatal(e)
	}
	var r AssistantMessage
	applyOpenAICompletionsUsage(&r, Model{Provider: "xai", API: "openai-completions"}, u)
	if r.Usage.Input != 24 || r.Usage.CacheRead != 6 || r.Usage.CacheWrite != 2 || r.Usage.Output != 103 || r.Usage.TotalTokens != 135 {
		t.Fatalf("consistent split=%+v", r.Usage)
	}
}
