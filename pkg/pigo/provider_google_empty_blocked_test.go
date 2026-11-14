package pigo

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGoogleEmptyContentOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, wire string
		want       StopReason
		diagnostic string
	}{
		{"blocked candidate", `{"candidates":[{"finishReason":"SAFETY"}]}`, StopReasonError, "SAFETY"},
		{"malformed call", `{"candidates":[{"finishReason":"MALFORMED_FUNCTION_CALL"}]}`, StopReasonError, "MALFORMED_FUNCTION_CALL"},
		{"length", `{"candidates":[{"finishReason":"MAX_TOKENS"}]}`, StopReasonLength, ""},
		{"stop", `{"candidates":[{"finishReason":"STOP"}]}`, StopReasonStop, ""},
		{"candidate no reason", `{"candidates":[{}]}`, StopReasonStop, ""},
		{"prompt blocked", `{"promptFeedback":{"blockReason":"SAFETY"}}`, StopReasonError, "SAFETY"},
		{"future block", `{"promptFeedback":{"blockReason":"FUTURE_BLOCK"}}`, StopReasonError, "FUTURE_BLOCK"},
		{"unspecified", `{"promptFeedback":{"blockReason":"BLOCK_REASON_UNSPECIFIED"}}`, StopReasonStop, ""},
		{"empty block", `{"promptFeedback":{"blockReason":""}}`, StopReasonStop, ""},
		{"ratings only", `{"promptFeedback":{"safetyRatings":[{"category":"HARM_CATEGORY_DANGEROUS_CONTENT","probability":"LOW"}]}}`, StopReasonStop, ""},
		{"null feedback", `{"promptFeedback":null}`, StopReasonStop, ""},
		{"usage only", `{}`, StopReasonStop, ""},
		{"empty stream", "", StopReasonStop, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			count := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				count++
				w.Header().Set("Content-Type", "text/event-stream")
				if tc.wire != "" {
					fmt.Fprint(w, "data: {\"responseId\":\"response_test\",\"usageMetadata\":{\"promptTokenCount\":2,\"totalTokenCount\":2}}\n\n")
					fmt.Fprintf(w, "data: %s\n\n", tc.wire)
				}
			}))
			defer server.Close()
			stream := Stream(Model{ID: "fixture", Provider: "google", API: "google-generative-ai", BaseURL: server.URL}, Context{}, ProviderStreamOptions{APIKey: "fixture"})
			done, errors := 0, 0
			for e := range stream.Events() {
				if e.Type == AssistantMessageEventDone {
					done++
				}
				if e.Type == AssistantMessageEventError {
					errors++
				}
			}
			result := stream.Result()
			if result.StopReason != tc.want || count != 1 || done != 1 || errors != 0 {
				t.Fatalf("classification/lifecycle count=%d done=%d error=%d: %+v", count, done, errors, result)
			}
			if tc.diagnostic != "" && !strings.Contains(result.ErrorMessage, tc.diagnostic) {
				t.Fatalf("missing diagnostic: %+v", result)
			}
			if tc.diagnostic == "" && result.ErrorMessage != "" {
				t.Fatalf("invented error: %+v", result)
			}
			if tc.wire != "" && (result.Usage.TotalTokens != 2 || result.ResponseID != "response_test") {
				t.Fatalf("lost metadata: %+v", result)
			}
			if len(result.Content) != 0 {
				t.Fatalf("invented content: %+v", result)
			}
		})
	}
}

func TestGoogleNoContentFinishPreservesEarlierText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"candidates":[{"content":{"parts":[{"text":"partial answer"}]}}]}

data: {"candidates":[{"finishReason":"SAFETY"}],"usageMetadata":{"promptTokenCount":2,"candidatesTokenCount":3,"totalTokenCount":5}}

`)
	}))
	defer server.Close()
	result := Complete(Model{ID: "fixture", Provider: "google", API: "google-generative-ai", BaseURL: server.URL}, Context{}, ProviderStreamOptions{APIKey: "fixture"})
	if result.StopReason != StopReasonError || !strings.Contains(result.ErrorMessage, "SAFETY") || len(result.Content) != 1 || result.Content[0].(TextContent).Text != "partial answer" || result.Usage.TotalTokens != 5 {
		t.Fatalf("lost partial or finish outcome: %+v", result)
	}
}
