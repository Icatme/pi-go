package pigo

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeepSeekReportsRequestUsageAndCost(t *testing.T) {
	for _, modelID := range []string{"deepseek-v4-pro", "deepseek-flash"} {
		for _, test := range []struct {
			name                   string
			prompt, cached, output int
			miss                   *int
			wantInput              int
			pro, flash             UsageCost
		}{
			{"cache miss reported", 1000, 400, 10, new(600), 600,
				UsageCost{Input: 0.000792, Output: 0.0000396, CacheRead: 0.0000176, Total: 0.0008492},
				UsageCost{Input: 0.00018, Output: 0.000012, CacheRead: 0.0000024, Total: 0.0001944}},
			{"cache miss omitted", 1000, 400, 10, nil, 600,
				UsageCost{Input: 0.000792, Output: 0.0000396, CacheRead: 0.0000176, Total: 0.0008492},
				UsageCost{Input: 0.00018, Output: 0.000012, CacheRead: 0.0000024, Total: 0.0001944}},
			{"uncached input", 1000, 0, 10, nil, 1000,
				UsageCost{Input: 0.00132, Output: 0.0000396, Total: 0.0013596},
				UsageCost{Input: 0.0003, Output: 0.000012, Total: 0.000312}},
			{"fully cached input", 1000, 1000, 10, new(0), 0,
				UsageCost{Output: 0.0000396, CacheRead: 0.000044, Total: 0.0000836},
				UsageCost{Output: 0.000012, CacheRead: 0.000006, Total: 0.000018}},
			{"reported zero usage", 0, 0, 0, nil, 0, UsageCost{}, UsageCost{}},
		} {
			for _, simple := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/simple=%t", modelID, test.name, simple), func(t *testing.T) {
					usage := map[string]any{
						"prompt_tokens": test.prompt, "completion_tokens": test.output,
						"prompt_cache_hit_tokens": test.cached, "total_tokens": test.prompt + test.output,
					}
					if test.miss != nil {
						usage["prompt_cache_miss_tokens"] = *test.miss
					}
					chunk, err := json.Marshal(map[string]any{
						"model": modelID, "usage": usage,
						"choices": []any{map[string]any{"index": 0, "delta": map[string]any{"content": "ok"}, "finish_reason": "stop"}},
					})
					if err != nil {
						t.Fatal(err)
					}
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "text/event-stream")
						fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
					}))
					defer server.Close()
					model := *GetModel("deepseek", modelID)
					model.BaseURL = server.URL
					var stream *AssistantMessageEventStream
					context := Context{Messages: []Message{UserMessage{Content: "hi"}}}
					if simple {
						stream = StreamSimple(model, context, SimpleStreamOptions{APIKey: "test"})
					} else {
						stream = Stream(model, context, ProviderStreamOptions{APIKey: "test"})
					}
					var done AssistantMessage
					for event := range stream.Events() {
						if event.Type == AssistantMessageEventDone {
							done = event.Message
						}
					}
					result := stream.Result()
					wantCost := test.pro
					if modelID == "deepseek-flash" {
						wantCost = test.flash
					}
					for name, message := range map[string]AssistantMessage{"done": done, "result": result} {
						got := message.Usage
						if message.StopReason != StopReasonStop || !message.UsageReported || got.Input != test.wantInput || got.CacheRead != test.cached || got.Output != test.output || got.TotalTokens != test.prompt+test.output {
							t.Fatalf("%s has incorrect usage: %+v", name, message)
						}
						if math.Abs(got.Cost.Input-wantCost.Input) > 1e-12 || math.Abs(got.Cost.Output-wantCost.Output) > 1e-12 || math.Abs(got.Cost.CacheRead-wantCost.CacheRead) > 1e-12 || got.Cost.CacheWrite != 0 || math.Abs(got.Cost.Total-wantCost.Total) > 1e-12 {
							t.Fatalf("%s cost=%+v, want %+v", name, got.Cost, wantCost)
						}
					}
				})
			}
		}
	}
}
