package pigo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestResponsesContentFilterIsNotLength(t *testing.T) {
	for _, api := range []API{"openai-responses", "openai-codex-responses"} {
		t.Run(string(api), func(t *testing.T) {
			var requests atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, `data: {"type":"response.output_text.delta","output_index":0,"content_index":0,"delta":"prior text"}`+"\n\n")
				fmt.Fprint(w, `data: {"type":"response.incomplete","response":{"id":"r_filter","status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`+"\n\n")
			}))
			defer s.Close()
			key := "fixture"
			if api == "openai-codex-responses" {
				key = makeOpenAICodexToken("fixture")
			}
			m := Model{ID: "requested", Provider: "fixture", API: api, BaseURL: s.URL, MaxTokens: 64}
			stream := Stream(m, Context{}, ProviderStreamOptions{APIKey: key, Transport: TransportSSE, MaxRetries: 2})
			events := collectAssistantMessageEvents(stream.Events())
			result := stream.Result()
			if result.StopReason != StopReasonError {
				t.Fatalf("content filter classified as %s: %+v", result.StopReason, result)
			}
			if requests.Load() != 1 || result.Usage.Input != 3 || result.Usage.Output != 2 || result.Model != m.ID || result.ResponseID != "r_filter" {
				t.Fatalf("result=%+v requests=%d", result, requests.Load())
			}
			failure, ok := ProviderFailureOf(result)
			if !ok || failure.Kind != ProviderFailureUpstream || failure.Reason != "content_filter" || failure.Code != "" {
				t.Fatalf("failure=%+v present=%v", failure, ok)
			}
			ends, dones := 0, 0
			for _, event := range events {
				if event.Type == AssistantMessageEventError {
					ends++
				}
				if event.Type == AssistantMessageEventDone {
					dones++
				}
			}
			if ends != 1 || dones != 0 {
				t.Fatalf("errors=%d dones=%d", ends, dones)
			}
			if len(result.Content) != 1 || result.Content[0].(TextContent).Text != "prior text" {
				t.Fatalf("partial lost: %+v", result.Content)
			}
		})
	}
}

func TestResponsesIncompleteReasonCompatibility(t *testing.T) {
	for _, tc := range []struct {
		reason, status string
		want           StopReason
	}{
		{"content_filter", "incomplete", StopReasonError},
		{"max_output_tokens", "incomplete", StopReasonLength},
		{"", "incomplete", StopReasonLength},
		{"future_reason", "incomplete", StopReasonLength},
		{"content_filter", "completed", StopReasonStop},
	} {
		t.Run(tc.status+"/"+tc.reason, func(t *testing.T) {
			response := AssistantMessage{}
			state := &openAIResponsesStreamingState{}
			stream := newAssistantMessageEventStream()
			go func() {
				for range stream.Events() {
				}
			}()
			data, _ := json.Marshal(map[string]any{"type": "response." + tc.status, "response": map[string]any{"status": tc.status, "incomplete_details": map[string]any{"reason": tc.reason}}})
			done, err := processOpenAIResponsesStreamEvent(string(data), Model{ID: "fixture"}, &response, stream, state, "")
			if err != nil {
				applyRequestError(&response, err)
			}
			stream.Close()
			if !done || response.StopReason != tc.want {
				t.Fatalf("done=%v err=%v response=%+v", done, err, response)
			}
		})
	}
}
