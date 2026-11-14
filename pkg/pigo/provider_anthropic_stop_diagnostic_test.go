package pigo

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAnthropicStopReasonDiagnostic(t *testing.T) {
	for _, tc := range []struct {
		reason string
		stop   StopReason
	}{
		{"refusal", StopReasonError}, {"sensitive", StopReasonError}, {"future_reason", StopReasonError},
		{"end_turn", StopReasonStop}, {"stop_sequence", StopReasonStop}, {"pause_turn", StopReasonStop}, {"max_tokens", StopReasonLength},
	} {
		t.Run(tc.reason, func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, `data: {"type":"message_start","message":{"id":"msg_fixture","usage":{"input_tokens":3,"output_tokens":0}}}

data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"answer"}}

data: {"type":"content_block_stop","index":0}

`)
				fmt.Fprintf(w, "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":%q},\"usage\":{\"output_tokens\":2}}\n\ndata: {\"type\":\"message_stop\"}\n\n", tc.reason)
			}))
			defer server.Close()
			m := Model{ID: "requested", Provider: "anthropic", API: "anthropic-messages", BaseURL: server.URL, MaxTokens: 64}
			stream := Stream(m, Context{}, ProviderStreamOptions{APIKey: "fixture"})
			events := collectAssistantMessageEvents(stream.Events())
			result := stream.Result()
			if result.StopReason != tc.stop || result.Model != m.ID || result.API != m.API || result.Provider != m.Provider || result.Usage.Input != 3 || result.Usage.Output != 2 || requests.Load() != 1 {
				t.Fatalf("requests=%d result=%+v", requests.Load(), result)
			}
			if len(result.Content) != 1 || result.Content[0].(TextContent).Text != "answer" {
				t.Fatal(result.Content)
			}
			if tc.stop == StopReasonError {
				if !strings.Contains(result.ErrorMessage, fmt.Sprintf("%q", tc.reason)) {
					t.Fatalf("lost reason: %+v", result)
				}
			} else if result.ErrorMessage != "" {
				t.Fatal(result.ErrorMessage)
			}
			if IsContextOverflow(result, 0) {
				t.Fatal("unexpected overflow classification")
			}
			done, errors := 0, 0
			for _, e := range events {
				if e.Type == AssistantMessageEventDone {
					done++
					if e.Reason != tc.stop || e.Message.ErrorMessage != result.ErrorMessage {
						t.Fatal(e)
					}
				}
				if e.Type == AssistantMessageEventError {
					errors++
				}
			}
			if done != 1 || errors != 0 {
				t.Fatalf("done=%d errors=%d", done, errors)
			}
		})
	}
}

func TestAnthropicStopReasonDiagnosticSharedParser(t *testing.T) {
	m := Model{ID: "requested", Provider: "kimi-coding", API: "anthropic-messages"}
	response := AssistantMessage{Model: m.ID, Provider: m.Provider, API: m.API}
	states := map[int]*anthropicStreamingBlockState{}
	done, err := processAnthropicStreamEvent(`{"type":"message_delta","delta":{"stop_reason":"refusal"}}`, m, &response, nil, states, false, nil, nil, false)
	if err != nil || done || response.StopReason != StopReasonError || !strings.Contains(response.ErrorMessage, `"refusal"`) || response.Model != m.ID || response.Provider != m.Provider {
		t.Fatalf("done=%v err=%v response=%+v", done, err, response)
	}
}
