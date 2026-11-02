package pigo_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/pkg/pigo"
)

var kiroAgentFixtureSequence atomic.Int64

func TestKiroAgentFixtureMultiTurnAndPermission(t *testing.T) {
	for _, mode := range []string{"allow", "permission", "gate"} {
		t.Run(mode, func(t *testing.T) {
			calls, bodies, hooks, dispatches := 0, 0, 0, 0
			var observedRaw []string
			source := fmt.Sprintf("kiro-agent-%d", kiroAgentFixtureSequence.Add(1))
			provider, cleanup := pigo.RegisterKiroAgentFixtureForTest(source, func(ctx context.Context, request []byte) (string, error) {
				calls++
				if calls == 1 {
					return "tool", nil
				}
				if calls != 2 {
					return "", errors.New("unexpected fixture attempt")
				}
				text := string(request)
				if !strings.Contains(text, "9007199254740993") || strings.Contains(text, "9007199254740992") || !strings.Contains(text, "agent-wire-1") || !strings.Contains(text, "toolResults") {
					t.Error("next-turn precise history missing")
				}
				expected := `"status":"success"`
				if mode != "allow" {
					expected = `"status":"error"`
				}
				if !strings.Contains(text, expected) {
					t.Errorf("tool status missing: %s", expected)
				}
				return "text", nil
			}, func(ctx pigo.Context) {
				dispatches++
				for _, message := range ctx.Messages {
					if m, ok := message.(pigo.AssistantMessage); ok {
						for _, block := range m.Content {
							if call, ok := block.(pigo.ToolCall); ok {
								observedRaw = append(observedRaw, call.RawArguments)
							}
						}
					}
				}
			})
			defer cleanup()
			definition := agent.AgentDefinition{
				DefaultModel: agent.ModelRef{Provider: string(provider), Model: "fixture-model"},
				Tools: []agent.ToolDefinition{{Name: "lookup", Parameters: map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "integer"}}, "required": []string{"id"}}, Execute: func(_ context.Context, in agent.ToolExecutionContext) (agent.ToolResult, error) {
					bodies++
					if fmt.Sprint(in.Args.(map[string]any)["id"]) != "9007199254740993" {
						t.Error("execution argument precision lost")
					}
					return agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: "local fake result"}}}, nil
				}}},
			}
			if mode == "permission" {
				definition.CheckToolPermission = func(context.Context, agent.BeforeToolCallContext) error { hooks++; return errors.New("fixture deny") }
			}
			if mode == "gate" {
				definition.BeforeToolCall = func(context.Context, agent.BeforeToolCallContext) (agent.BeforeToolCallResult, error) {
					hooks++
					return agent.BeforeToolCallResult{Block: true, Reason: "fixture deny"}, nil
				}
			}
			snapshot, err := agent.NewEngine().Run(context.Background(), definition, &agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("find")}, nil)
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if mode != "allow" {
				want = 0
			}
			if len(observedRaw) != 1 || observedRaw[0] != " { \"id\" : 9007199254740993 } " {
				t.Fatal("successful second turn did not contain exact original raw")
			}
			if dispatches != 2 || calls != 2 || bodies != want || (mode != "allow" && hooks != 1) {
				t.Fatalf("calls=%d bodies=%d hooks=%d", calls, bodies, hooks)
			}
			if len(snapshot.Messages) == 0 || snapshot.Messages[len(snapshot.Messages)-1].StopReason != agent.StopReasonStop {
				t.Fatal("final text missing")
			}
			// Persisted JSON cannot acquire original provider raw evidence.
			persisted, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			var restored agent.AgentSnapshot
			if err := json.Unmarshal(persisted, &restored); err != nil {
				t.Fatal(err)
			}
			afterRestore, err := agent.NewEngine().Run(context.Background(), definition, &restored, []agent.Message{agent.NewUserTextMessage("next")}, nil)
			if len(observedRaw) != 2 || observedRaw[1] != "" {
				t.Fatal("restore did not present missing original evidence to production projector")
			}
			if dispatches != 3 || calls != 2 || bodies != want || (err == nil && afterRestore.Error == "") {
				t.Fatal("restored history did not reach and fail strict provider projection")
			}
		})
	}
}

func TestKiroAgentFixtureTruncationAndCancellationNeverExecute(t *testing.T) {
	for _, mode := range []string{"truncated", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls, bodies := 0, 0
			source := fmt.Sprintf("kiro-agent-%d", kiroAgentFixtureSequence.Add(1))
			provider, cleanup := pigo.RegisterKiroAgentFixtureForTest(source, func(context.Context, []byte) (string, error) {
				calls++
				if mode == "cancel" {
					cancel()
					return "tool", nil
				}
				return "truncated", nil
			}, nil)
			defer cleanup()
			definition := agent.AgentDefinition{DefaultModel: agent.ModelRef{Provider: string(provider), Model: "fixture-model"}, Tools: []agent.ToolDefinition{{Name: "lookup", Parameters: map[string]any{"type": "object"}, Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
				bodies++
				return agent.ToolResult{}, nil
			}}}}
			_, _ = agent.NewEngine().Run(ctx, definition, &agent.AgentSnapshot{}, []agent.Message{agent.NewUserTextMessage("find")}, nil)
			if calls != 1 || bodies != 0 {
				t.Fatalf("calls=%d bodies=%d", calls, bodies)
			}
		})
	}
}
