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

func TestChatEmptyToolFinishReportsFailure(t *testing.T) {
	for _, finish := range []string{"tool_calls", "function_call"} {
		t.Run(finish, func(t *testing.T) {
			var requests atomic.Int32
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprintf(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":%q}]}\n\ndata: [DONE]\n\n", finish)
			}))
			defer s.Close()
			m := pigo.Model{ID: "fixture", API: "openai-completions", Provider: "openai", BaseURL: s.URL, MaxTokens: 100}
			d := AgentDefinition{Model: StreamFunc(func(ctx context.Context, r ModelRequest) (AssistantStream, error) {
				c, cancel := context.WithCancel(ctx)
				return newPigoAssistantStream(c, cancel, pigo.Stream(m, buildPigoContext(r.SystemPrompt, r.Messages, r.Tools), pigo.ProviderStreamOptions{APIKey: "fixture", RequestContext: c, MaxRetries: 2})), nil
			})}
			next, err := NewEngine().Run(t.Context(), d, &AgentSnapshot{}, []Message{NewUserTextMessage("run")}, nil)
			if err != nil {
				t.Fatal(err)
			}
			last := next.Messages[len(next.Messages)-1]
			t.Logf("HTTP=%d stop=%s calls=%d parts=%d error=%q", requests.Load(), last.StopReason, len(last.ToolCalls), len(last.Parts), next.Error)
			if requests.Load() != 1 || len(last.ToolCalls) != 0 || next.Error == "" || last.StopReason != StopReasonError {
				t.Fatalf("empty tool terminal accepted: %+v", last)
			}
		})
	}
}
