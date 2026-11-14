package agent

import (
	"context"
	"fmt"
	"github.com/Icatme/pi-go/pkg/pigo"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestResponsesContentFilterBlocksToolExecution(t *testing.T) {
	for _, gated := range []bool{false, true} {
		t.Run(fmt.Sprint(gated), func(t *testing.T) {
			var requests atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: "+`{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"echo","arguments":""}}`+"\n\n")
				fmt.Fprint(w, "data: "+`{"type":"response.function_call_arguments.delta","output_index":0,"delta":"{}"}`+"\n\n")
				fmt.Fprint(w, "data: "+`{"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"echo","arguments":"{}"}}`+"\n\n")
				fmt.Fprint(w, "data: "+`{"type":"response.incomplete","response":{"id":"r_filter","status":"incomplete","incomplete_details":{"reason":"content_filter"},"output":[{"type":"function_call","id":"fc_1","call_id":"call_1","name":"echo","arguments":"{}"}],"usage":{"input_tokens":3,"output_tokens":2,"total_tokens":5}}}`+"\n\n")
			}))
			defer s.Close()
			entered, gates := 0, 0
			m := pigo.Model{ID: "fixture", API: "openai-responses", Provider: "openai", BaseURL: s.URL, MaxTokens: 100}
			d := AgentDefinition{Model: StreamFunc(func(ctx context.Context, r ModelRequest) (AssistantStream, error) {
				c, cancel := context.WithCancel(ctx)
				return newPigoAssistantStream(c, cancel, pigo.Stream(m, buildPigoContext(r.SystemPrompt, r.Messages, r.Tools), pigo.ProviderStreamOptions{APIKey: "fixture", RequestContext: c, Transport: pigo.TransportSSE, MaxRetries: 2})), nil
			}), Tools: []ToolDefinition{{Name: "echo", Execute: func(ctx context.Context, in ToolExecutionContext) (ToolResult, error) {
				entered++
				return terminatingTool(false)(ctx, in)
			}}}}
			hooks := LoopHooks{}
			if gated {
				hooks.ToolGate = func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
					gates++
					return ToolGateResult{Action: ToolGateActionAllow}, nil
				}
			}
			next, err := NewEngine().RunWithHooks(t.Context(), d, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, nil, hooks)
			if err != nil {
				t.Fatal(err)
			}
			if requests.Load() != 1 || entered != 0 || gates != 0 {
				t.Fatalf("requests=%d executed=%d gates=%d", requests.Load(), entered, gates)
			}
			var found bool
			for _, msg := range next.Messages {
				if msg.Role == RoleAssistant {
					found = true
					if msg.StopReason != StopReasonError || len(msg.ToolCalls) != 1 {
						t.Fatalf("lost blocked audit %+v", msg)
					}
				}
			}
			if !found {
				t.Fatal("no assistant")
			}
		})
	}
}
