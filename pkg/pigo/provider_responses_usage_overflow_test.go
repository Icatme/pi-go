package pigo

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestResponsesUsageOverflowRejected(t *testing.T) {
	huge := math.MaxInt - 1023
	cases := []openAIResponsesUsage{
		{InputTokens: 8, OutputTokens: 3, TotalTokens: 11, InputDetails: openAIResponsesInputTokenDetails{CachedTokens: huge, CacheWriteTokens: huge}},
		{InputTokens: math.MaxInt, OutputTokens: 1},
		{InputTokens: -1}, {OutputTokens: -1}, {TotalTokens: -1},
		{InputDetails: openAIResponsesInputTokenDetails{CachedTokens: -1}},
		{InputDetails: openAIResponsesInputTokenDetails{CacheWriteTokens: -1}},
	}
	for i, u := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			before := AssistantMessage{Usage: Usage{Input: 2, Output: 3, TotalTokens: 5}, UsageReported: true}
			after := before
			err := applyOpenAIResponsesUsage(Model{}, &after, u, "", "")
			if err == nil {
				t.Fatalf("invalid usage accepted: %+v", after.Usage)
			}
			if !reflect.DeepEqual(after, before) {
				t.Fatal("invalid frame replaced earlier valid usage")
			}
		})
	}
	// Preserve established valid-zero and small cache-exceeds-input clamping behavior.
	for _, u := range []openAIResponsesUsage{{}, {InputTokens: 2, OutputTokens: 1, TotalTokens: 3, InputDetails: openAIResponsesInputTokenDetails{CachedTokens: 3}}, {InputTokens: math.MaxInt, InputDetails: openAIResponsesInputTokenDetails{CachedTokens: math.MaxInt}}} {
		var r AssistantMessage
		if err := applyOpenAIResponsesUsage(Model{}, &r, u, "", ""); err != nil || !r.UsageReported || r.Usage.Input != 0 {
			t.Fatalf("legacy/boundary changed %+v %v", r, err)
		}
	}
}
func TestResponsesInvalidUsageHTTP(t *testing.T) {
	huge := math.MaxInt - 1023
	for _, api := range []API{"openai-responses", "openai-codex-responses"} {
		for _, event := range []string{"response.completed", "response.done", "response.incomplete", "response.failed"} {
			t.Run(string(api)+"/"+event, func(t *testing.T) {
				count := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					count++
					w.Header().Set("Content-Type", "text/event-stream")
					body := map[string]any{"type": event, "response": map[string]any{"status": "completed", "usage": map[string]any{"input_tokens": 8, "output_tokens": 3, "total_tokens": 11, "input_tokens_details": map[string]any{"cached_tokens": huge, "cache_write_tokens": huge}}}}
					terminal := body["response"].(map[string]any)
					if event == "response.failed" {
						terminal["status"] = "failed"
						terminal["error"] = map[string]any{"code": "server_error", "message": "upstream retryable failure"}
					} else if event == "response.incomplete" {
						terminal["status"] = "incomplete"
					}
					partial, _ := json.Marshal(map[string]any{"type": "response.output_text.delta", "output_index": 0, "content_index": 0, "delta": "partial answer"})
					fmt.Fprintf(w, "data: %s\n\n", partial)
					w.(http.Flusher).Flush()
					raw, _ := json.Marshal(body)
					fmt.Fprintf(w, "data: %s\n\n", raw)
				}))
				defer server.Close()
				stream := Stream(Model{ID: "fixture", Provider: "openai", API: api, BaseURL: server.URL}, Context{}, ProviderStreamOptions{APIKey: makeOpenAICodexToken("acct"), Transport: TransportSSE})
				done, fail := 0, 0
				for e := range stream.Events() {
					if e.Type == AssistantMessageEventDone {
						done++
					}
					if e.Type == AssistantMessageEventError {
						fail++
					}
				}
				r := stream.Result()
				text := ""
				for _, block := range r.Content {
					if b, ok := block.(TextContent); ok {
						text += b.Text
					}
				}
				if text != "partial answer" {
					t.Fatalf("partial output lost: %q", text)
				}
				if !strings.Contains(r.ErrorMessage, "usage") || strings.Contains(r.ErrorMessage, "upstream retryable failure") {
					t.Fatalf("invalid usage must take precedence: %q", r.ErrorMessage)
				}
				f, ok := ProviderFailureOf(r)
				if r.StopReason != StopReasonError || r.UsageReported || !ok || f.Kind != ProviderFailureParse || f.Reason != "invalid_usage" || done != 0 || fail != 1 || count != 1 {
					t.Fatalf("bad outcome count%d done%d fail%d result%+v failure%+v", count, done, fail, r, f)
				}
			})
		}
	}
}
