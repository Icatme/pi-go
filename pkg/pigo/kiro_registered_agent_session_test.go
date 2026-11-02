package pigo_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/pkg/pigo"
)

func TestKiroRegisteredAgentSessionPermissionsAndReplay(t *testing.T) {
	for _, mode := range []string{"allow", "permission", "gate"} {
		t.Run(mode, func(t *testing.T) {
			source := fmt.Sprintf("public-session-%d", kiroAgentFixtureSequence.Add(1))
			calls, executions, hooks := 0, 0, 0
			var conversationID string
			provider, cleanup, err := pigo.RegisterKiroSessionAgentFixtureForTest(source, "local-test-tenant", func(_ context.Context, body []byte) (string, error) {
				calls++
				var envelope struct {
					ConversationState struct {
						ConversationID string `json:"conversationId"`
						History        []struct {
							Assistant struct {
								Calls []struct {
									ID    string         `json:"toolUseId"`
									Name  string         `json:"name"`
									Input map[string]any `json:"input"`
								} `json:"toolUses"`
							} `json:"assistantResponseMessage"`
						} `json:"history"`
						Current struct {
							User struct {
								Context struct {
									Results []struct {
										ID     string `json:"toolUseId"`
										Status string `json:"status"`
									} `json:"toolResults"`
								} `json:"userInputMessageContext"`
							} `json:"userInputMessage"`
						} `json:"currentMessage"`
					} `json:"conversationState"`
				}
				decoder := json.NewDecoder(bytes.NewReader(body))
				decoder.UseNumber()
				if decoder.Decode(&envelope) != nil || envelope.ConversationState.ConversationID == "" {
					t.Error("missing conversation")
				}
				if strings.Contains(string(body), "sensitive-agent-session") {
					t.Error("raw session on wire")
				}
				if calls == 1 {
					conversationID = envelope.ConversationState.ConversationID
					return "tool", nil
				}
				if calls != 2 {
					return "", errors.New("unexpected attempt")
				}
				if envelope.ConversationState.ConversationID != conversationID {
					t.Error("affinity changed")
				}
				totalCalls := 0
				for _, entry := range envelope.ConversationState.History {
					for _, call := range entry.Assistant.Calls {
						totalCalls++
						number, ok := call.Input["id"].(json.Number)
						if !ok || number != "9007199254740993" || call.ID != "http-tool" || call.Name != "lookup" {
							t.Error("typed precise tool replay")
						}
					}
				}
				status := "success"
				if mode != "allow" {
					status = "error"
				}
				results := envelope.ConversationState.Current.User.Context.Results
				if totalCalls != 1 || len(results) != 1 || results[0].ID != "http-tool" || results[0].Status != status {
					t.Error("exact tool/result correspondence")
				}
				return "text", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			definition := agent.AgentDefinition{Transport: agent.TransportSSE, DefaultModel: agent.ModelRef{Provider: string(provider), Model: "fixture-model"}, Tools: []agent.ToolDefinition{{Name: "lookup", Parameters: map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "integer"}}, "required": []string{"id"}}, Execute: func(_ context.Context, in agent.ToolExecutionContext) (agent.ToolResult, error) {
				executions++
				exact := false
				switch number := in.Args.(map[string]any)["id"].(type) {
				case int:
					exact = int64(number) == 9007199254740993
				case int64:
					exact = number == 9007199254740993
				case json.Number:
					exact = number == "9007199254740993"
				}
				if !exact {
					t.Error("execution argument is not an exact integer representation")
				}
				return agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: "local fake result"}}}, nil
			}}}}
			if mode == "permission" {
				definition.CheckToolPermission = func(context.Context, agent.BeforeToolCallContext) error { hooks++; return errors.New("local deny") }
			}
			if mode == "gate" {
				definition.BeforeToolCall = func(context.Context, agent.BeforeToolCallContext) (agent.BeforeToolCallResult, error) {
					hooks++
					return agent.BeforeToolCallResult{Block: true, Reason: "local deny"}, nil
				}
			}
			snapshot, err := agent.NewEngine().Run(context.Background(), definition, &agent.AgentSnapshot{SessionID: "sensitive-agent-session"}, []agent.Message{agent.NewUserTextMessage("find")}, nil)
			if err != nil {
				t.Fatal(err)
			}
			expected := 1
			if mode != "allow" {
				expected = 0
			}
			if calls != 2 || executions != expected || snapshot.SessionID != "sensitive-agent-session" || snapshot.Error != "" {
				t.Fatal("public registered agent flow", calls, executions, snapshot.Error)
			}
			if mode != "allow" && hooks != 1 {
				t.Fatal("permission hook bypassed")
			}
		})
	}
}

func TestKiroRegisteredAgentIncompleteOrCancelledNeverExecutes(t *testing.T) {
	for _, scenario := range []string{"truncated", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			source := fmt.Sprintf("public-failure-%d", kiroAgentFixtureSequence.Add(1))
			calls, executions := 0, 0
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			provider, cleanup, err := pigo.RegisterKiroSessionAgentFixtureForTest(source, "local-test-tenant", func(context.Context, []byte) (string, error) {
				calls++
				if scenario == "cancelled" {
					cancel()
					return "tool", nil
				}
				return "truncated", nil
			})
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			definition := agent.AgentDefinition{Transport: agent.TransportSSE, DefaultModel: agent.ModelRef{Provider: string(provider), Model: "fixture-model"}, Tools: []agent.ToolDefinition{{Name: "lookup", Parameters: map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "integer"}}, "required": []string{"id"}}, Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
				executions++
				return agent.ToolResult{}, nil
			}}}}
			_, _ = agent.NewEngine().Run(ctx, definition, &agent.AgentSnapshot{SessionID: "local-session"}, []agent.Message{agent.NewUserTextMessage("find")}, nil)
			if calls != 1 || executions != 0 {
				t.Fatal("failed response authorized tool or retried")
			}
		})
	}
}

func TestKiroRegisteredAgentTenantIsolationAndStableReturn(t *testing.T) {
	source := fmt.Sprintf("public-tenant-%d", kiroAgentFixtureSequence.Add(1))
	var derived []string
	for _, tenant := range []string{"tenant-a", "tenant-b", "tenant-a"} {
		calls := 0
		provider, cleanup, err := pigo.RegisterKiroSessionAgentFixtureForTest(source, tenant, func(_ context.Context, body []byte) (string, error) {
			calls++
			var envelope struct {
				ConversationState struct {
					ConversationID string `json:"conversationId"`
				} `json:"conversationState"`
			}
			if json.Unmarshal(body, &envelope) != nil {
				t.Error("body")
			}
			derived = append(derived, envelope.ConversationState.ConversationID)
			if strings.Contains(string(body), "same-sensitive-session") {
				t.Error("raw session exposed")
			}
			return "text", nil
		})
		if err != nil {
			t.Fatal(err)
		}
		definition := agent.AgentDefinition{Transport: agent.TransportSSE, DefaultModel: agent.ModelRef{Provider: string(provider), Model: "fixture-model"}}
		_, err = agent.NewEngine().Run(context.Background(), definition, &agent.AgentSnapshot{SessionID: "same-sensitive-session"}, []agent.Message{agent.NewUserTextMessage("hello")}, nil)
		cleanup()
		if err != nil || calls != 1 {
			t.Fatal("tenant attempt", err, calls)
		}
	}
	if len(derived) != 3 || derived[0] == "" || derived[0] == derived[1] || derived[0] != derived[2] {
		t.Fatal("tenant isolation or deterministic reuse")
	}
}

func TestKiroRegisteredAgentDefaultAutoTransportIsRejected(t *testing.T) {
	source := fmt.Sprintf("public-auto-%d", kiroAgentFixtureSequence.Add(1))
	calls := 0
	provider, cleanup, err := pigo.RegisterKiroSessionAgentFixtureForTest(source, "local-test-tenant", func(context.Context, []byte) (string, error) { calls++; return "text", nil })
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	// AgentDefinition.Validate intentionally defaults an empty transport to Auto.
	// The Kiro subset requires explicit SSE instead of silently choosing a path.
	definition := agent.AgentDefinition{DefaultModel: agent.ModelRef{Provider: string(provider), Model: "fixture-model"}}
	snapshot, err := agent.NewEngine().Run(context.Background(), definition, &agent.AgentSnapshot{SessionID: "local-session"}, []agent.Message{agent.NewUserTextMessage("hello")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 || snapshot.Error != "api options rejected" {
		t.Fatal("auto was silently accepted")
	}
}
