package pigo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGoogleFinishReasonDiagnostic(t *testing.T) {
	for _, reason := range []string{"SAFETY", "RECITATION", "MALFORMED_FUNCTION_CALL", "OTHER", "FUTURE_REASON", "STOP", "MAX_TOKENS"} {
		t.Run(reason, func(t *testing.T) {
			count := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count++
				w.Header().Set("Content-Type", "text/event-stream")
				wire, _ := json.Marshal(map[string]any{"candidates": []any{map[string]any{"content": map[string]any{"parts": []any{map[string]any{"text": "partial"}}}, "finishReason": reason}}, "usageMetadata": map[string]any{"promptTokenCount": 2, "candidatesTokenCount": 3, "totalTokenCount": 5}})
				fmt.Fprintf(w, "data: %s\n\n", wire)
			}))
			defer server.Close()
			stream := Stream(Model{ID: "fixture", Provider: "google", API: "google-generative-ai", BaseURL: server.URL}, Context{}, ProviderStreamOptions{APIKey: "fixture"})
			done, terminalErrors := 0, 0
			for event := range stream.Events() {
				if event.Type == AssistantMessageEventDone {
					done++
				}
				if event.Type == AssistantMessageEventError {
					terminalErrors++
				}
			}
			response := stream.Result()
			if done != 1 || terminalErrors != 0 {
				t.Fatalf("terminal lifecycle changed: done=%d error=%d", done, terminalErrors)
			}
			if response.Model != "fixture" || response.Provider != "google" || response.API != "google-generative-ai" {
				t.Fatalf("identity changed: %+v", response)
			}
			want := StopReasonError
			if reason == "STOP" {
				want = StopReasonStop
			}
			if reason == "MAX_TOKENS" {
				want = StopReasonLength
			}
			if response.StopReason != want {
				t.Fatalf("reason %s: %+v", reason, response)
			}
			if want == StopReasonError && !strings.Contains(response.ErrorMessage, reason) {
				t.Fatalf("lost finish reason: %+v", response)
			}
			if want != StopReasonError && response.ErrorMessage != "" {
				t.Fatalf("unexpected error: %+v", response)
			}
			if count != 1 || response.Usage.TotalTokens != 5 || len(response.Content) != 1 || response.Content[0].(TextContent).Text != "partial" {
				t.Fatalf("changed response/count %d: %+v", count, response)
			}
		})
	}
}
