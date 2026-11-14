package pigo

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestGoogleContinuationStopsAtSingleHop(t *testing.T) {
	for _, reason := range []string{"STOP", "MAX_TOKENS", "CONTINUATION"} {
		for _, withTool := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/tool=%v", reason, withTool), func(t *testing.T) {
				var requests atomic.Int32
				parts := `{"text":"partial answer"}`
				if withTool {
					parts += `,{"functionCall":{"id":"call_1","name":"echo","args":{"text":"hello"}}}`
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[%s]},\"finishReason\":%q,\"continuationToken\":\"b3BhcXVl\"}],\"usageMetadata\":{\"promptTokenCount\":3,\"candidatesTokenCount\":4,\"totalTokenCount\":7}}\n\n", parts, reason)
				}))
				defer server.Close()
				model := Model{ID: "fixture", API: "google-generative-ai", Provider: "google", BaseURL: server.URL, MaxTokens: 1024}
				result := Complete(model, Context{}, ProviderStreamOptions{APIKey: "fixture", MaxRetries: 2})
				want := StopReasonLength
				if reason == "STOP" {
					want = StopReasonStop
					if withTool {
						want = StopReasonToolUse
					}
				}
				wantBlocks := 1
				if withTool {
					wantBlocks++
				}
				if result.StopReason != want || len(result.Content) != wantBlocks || result.Content[0].(TextContent).Text != "partial answer" || result.Usage.Output != 4 || result.Usage.TotalTokens != 7 || requests.Load() != 1 {
					t.Fatalf("result=%+v requests=%d wantStop=%s", result, requests.Load(), want)
				}
				if withTool {
					call, ok := result.Content[1].(ToolCall)
					if !ok || call.ID != "call_1" || call.Name != "echo" || call.Arguments["text"] != "hello" {
						t.Fatalf("tool=%+v", result.Content[1])
					}
				}
			})
		}
	}
}
func TestGoogleContinuationFinishReasonBoundaries(t *testing.T) {
	for _, tc := range []struct {
		reason string
		want   StopReason
	}{{"STOP", StopReasonStop}, {"MAX_TOKENS", StopReasonLength}, {"CONTINUATION", StopReasonLength}, {"MALFORMED_FUNCTION_CALL", StopReasonError}, {"SAFETY", StopReasonError}, {"UNRECOGNIZED", StopReasonError}} {
		if got := mapGoogleStopReason(tc.reason, nil); got != tc.want {
			t.Errorf("%s: %s want%s", tc.reason, got, tc.want)
		}
	}
}

func TestGoogleContinuationWithToolRetainsLengthBoundary(t *testing.T) {
	tool := []ContentBlock{ToolCall{ID: "call", Name: "echo", Arguments: map[string]any{}}}
	if got := mapGoogleStopReason("STOP", tool); got != StopReasonToolUse {
		t.Fatalf("normal tool stop: %s", got)
	}
	for _, reason := range []string{"MAX_TOKENS", "CONTINUATION"} {
		if got := mapGoogleStopReason(reason, tool); got != StopReasonLength {
			t.Fatalf("%s tool boundary: %s", reason, got)
		}
	}
}
