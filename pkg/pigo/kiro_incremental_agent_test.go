package pigo_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/pkg/pigo"
)

func TestKiroIncrementalRegisteredAgentToolPermissionAndLateCancel(t *testing.T) {
	for _, mode := range []string{"allow", "permission", "cancel-tool-end", "truncated", "close"} {
		t.Run(mode, func(t *testing.T) {
			source := fmt.Sprintf("mixed-agent-%d", kiroAgentFixtureSequence.Add(1))
			preview := make(chan struct{})
			var once sync.Once
			calls, executions, permissions, deltas, toolEnds := 0, 0, 0, 0, 0
			var conversationID string
			provider, cleanup, err := pigo.RegisterKiroIncrementalAgentFixtureForTest(source, func(raw []byte) {
				calls++
				var wire struct {
					State struct {
						ID      string `json:"conversationId"`
						History []struct {
							Assistant struct {
								Calls []struct {
									Input map[string]any `json:"input"`
								} `json:"toolUses"`
							} `json:"assistantResponseMessage"`
						} `json:"history"`
						Current struct {
							User struct {
								Context struct {
									Results []struct {
										Status string `json:"status"`
									} `json:"toolResults"`
								} `json:"userInputMessageContext"`
							} `json:"userInputMessage"`
						} `json:"currentMessage"`
					} `json:"conversationState"`
				}
				d := json.NewDecoder(bytes.NewReader(raw))
				d.UseNumber()
				if err := d.Decode(&wire); err != nil {
					t.Error(err)
				}
				if wire.State.ID == "" || wire.State.ID == "local-session" {
					t.Error("scope ID")
				}
				if calls == 1 {
					conversationID = wire.State.ID
				} else {
					if wire.State.ID != conversationID {
						t.Error("changed affinity")
					}
					found := 0
					for _, entry := range wire.State.History {
						for _, call := range entry.Assistant.Calls {
							found++
							number, ok := call.Input["id"].(json.Number)
							if !ok || number != "9007199254740993" {
								t.Error("lost wire integer")
							}
						}
					}
					expected := "success"
					if mode == "permission" {
						expected = "error"
					}
					if found != 1 || len(wire.State.Current.User.Context.Results) != 1 || wire.State.Current.User.Context.Results[0].Status != expected {
						t.Error("tool result chronology")
					}
				}
			}, preview, mode)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			definition := agent.AgentDefinition{DefaultModel: agent.ModelRef{Provider: string(provider), Model: "fixture-model"}, Tools: []agent.ToolDefinition{{Name: "lookup", Parameters: map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "integer"}}, "required": []string{"id"}}, Execute: func(_ context.Context, input agent.ToolExecutionContext) (agent.ToolResult, error) {
				executions++
				exact := false
				switch value := input.Args.(map[string]any)["id"].(type) {
				case int:
					exact = int64(value) == 9007199254740993
				case int64:
					exact = value == 9007199254740993
				case json.Number:
					exact = value == "9007199254740993"
				}
				if !exact {
					t.Error("execution lost exact wire integer")
				}
				return agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: "only local fake"}}}, nil
			}}}}
			definition.CheckToolPermission = func(context.Context, agent.BeforeToolCallContext) error {
				permissions++
				if mode == "permission" {
					return errors.New("local deny")
				}
				return nil
			}
			sink := func(e agent.AgentEvent) {
				if e.UpdateType == agent.AssistantEventTextDelta {
					deltas++
					once.Do(func() { close(preview) })
				}
				if e.UpdateType == agent.AssistantEventToolCallEnd {
					toolEnds++
					if mode == "cancel-tool-end" {
						cancel()
					}
				}
			}
			snapshot, err := agent.NewEngine().Run(ctx, definition, &agent.AgentSnapshot{SessionID: "local-session"}, []agent.Message{agent.NewUserTextMessage("lookup")}, sink)
			if ctx.Err() == context.DeadlineExceeded {
				t.Fatal("worker did not terminate cooperatively")
			}
			if deltas == 0 {
				t.Fatal("Agent did not receive preview before terminal")
			}
			if mode == "allow" || mode == "permission" {
				expected := 1
				if mode == "permission" {
					expected = 0
				}
				if err != nil || snapshot.Error != "" || calls != 2 || executions != expected || permissions != 1 || toolEnds != 1 {
					t.Fatal("normal permission flow", err, calls, executions, permissions, toolEnds)
				}
			} else {
				if (err == nil && snapshot.Error == "") || calls != 1 || executions != 0 {
					t.Fatal("failed/cancelled response executed or retried", calls, executions)
				}
				if mode == "cancel-tool-end" {
					if toolEnds != 1 {
						t.Fatal("cancellation was not after observed tool end")
					}
				} else if toolEnds != 0 || permissions != 0 {
					t.Fatal("tool evidence before successful Close")
				}
			}
		})
	}
}
