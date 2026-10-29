package prebuilt

import (
	"context"
	"fmt"
	core "github.com/Icatme/pi-go/agent"
	"strings"
	"testing"
)

func TestChatStreamPerMessageDeltaFallback(t *testing.T) {
	for _, mode := range []string{"print", "async"} {
		for mask := 0; mask < 8; mask++ {
			t.Run(fmt.Sprintf("%s/delta-mask-%d", mode, mask), func(t *testing.T) {
				var responses []chatScriptedResponse
				want := "Looking up.Checking.Final result."
				for turn, text := range []string{"Looking up.", "Checking.", "Final result."} {
					response := chatScriptedResponse{final: core.Message{Role: core.RoleAssistant, Parts: []core.Part{core.NewTextPart(text)}, StopReason: core.StopReasonStop}}
					if mask&(1<<turn) != 0 {
						response.events = []core.AssistantEvent{{Type: core.AssistantEventTextDelta, Message: core.Message{Role: core.RoleAssistant}, Delta: text}}
					}
					if turn < 2 {
						response.final.StopReason = core.StopReasonToolUse
						response.final.ToolCalls = []core.ToolCall{{ID: fmt.Sprintf("call%d", turn), Name: "lookup", Arguments: []byte("{}")}}
					}
					responses = append(responses, response)
				}
				model := &chatScriptedModel{responses: responses}
				calls := 0
				chat, err := NewChatAgent(core.AgentDefinition{Model: model, Tools: []core.ToolDefinition{{Name: "lookup", Execute: func(context.Context, core.ToolExecutionContext) (core.ToolResult, error) {
					calls++
					return core.ToolResult{}, nil
				}}}})
				if err != nil {
					t.Fatal(err)
				}
				var output strings.Builder
				if mode == "print" {
					err = chat.PrintStream(context.Background(), "look up", func(s string) error { output.WriteString(s); return nil })
				} else {
					var chunks <-chan string
					chunks, err = chat.AsyncChat(context.Background(), "look up")
					if err == nil {
						for s := range chunks {
							output.WriteString(s)
						}
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				if got := output.String(); got != want {
					t.Fatalf("missing or duplicated message output: %q want %q", got, want)
				}
				if calls != 2 || model.callCount != 3 {
					t.Fatalf("tool calls=%d model calls=%d", calls, model.callCount)
				}
			})
		}
	}
}
