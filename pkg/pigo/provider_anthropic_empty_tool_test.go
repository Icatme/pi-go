package pigo

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestAnthropicEmptyToolUseIsError(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"type":"message_start","message":{"id":"m1","content":[],"usage":{"input_tokens":2}}}

data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":168}}

data: {"type":"message_stop"}

`)
	}))
	defer server.Close()
	model := Model{ID: "fixture", API: "anthropic-messages", Provider: "anthropic", BaseURL: server.URL, MaxTokens: 1024}
	result := Complete(model, Context{}, ProviderStreamOptions{APIKey: "fixture", MaxRetries: 2})
	if result.StopReason != StopReasonError {
		t.Fatalf("expected malformed error, got %+v", result)
	}
	if failure, ok := ProviderFailureOf(result); !ok || failure.Kind != ProviderFailureParse || failure.Reason != "missing_tool_call" {
		t.Fatalf("failure=%+v", failure)
	}
	if result.Usage.Output != 168 || requests.Load() != 1 {
		t.Fatal(result, requests.Load())
	}
}

func TestAnthropicEmptyToolUseTerminalBoundaries(t *testing.T) {
	model := Model{ID: "fixture", API: "anthropic-messages", Provider: "anthropic"}
	for _, tc := range []struct {
		name      string
		content   []ContentBlock
		stop      StopReason
		hosted    []HostedTool
		emit      bool
		wantError bool
	}{
		{name: "thinking only", content: []ContentBlock{ThinkingContent{Thinking: "interrupted"}}, stop: StopReasonToolUse, emit: true, wantError: true},
		{name: "text only", content: []ContentBlock{TextContent{Text: "working"}}, stop: StopReasonToolUse, emit: true, wantError: true},
		{name: "normal empty", stop: StopReasonStop, emit: true},
		{name: "tool exists", content: []ContentBlock{ToolCall{ID: "call_a", Name: "read"}}, stop: StopReasonToolUse, emit: true},
		{name: "hosted deferred", stop: StopReasonToolUse, hosted: []HostedTool{{Type: HostedToolTypeWebSearch}}, emit: false},
		{name: "hosted terminal", stop: StopReasonToolUse, hosted: []HostedTool{{Type: HostedToolTypeWebSearch}}, emit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			response := AssistantMessage{StopReason: tc.stop, Content: tc.content}
			done, err := processAnthropicStreamEvent(`{"type":"message_stop"}`, model, &response, nil, map[int]*anthropicStreamingBlockState{}, false, nil, tc.hosted, tc.emit)
			if (err != nil) != tc.wantError || done == tc.wantError {
				t.Fatal(done, err, response)
			}
		})
	}
}
