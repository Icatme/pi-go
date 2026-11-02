package pigo_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/pkg/pigo"
)

// Exercise real Agent ownership across runs, then its public JSON restore path.
// This checks current object semantics, not original provider wire provenance.
func TestKiroAuthoredAgentPromptContinueAndRestore(t *testing.T) {
	calls, executions := 0, 0
	wantNumber := json.Number("9007199254740993")
	var conversation string
	provider, cleanup, err := pigo.RegisterKiroSessionAgentFixtureForTest(fmt.Sprintf("authored-lifecycle-%d", kiroAgentFixtureSequence.Add(1)), "tenant", func(_ context.Context, body []byte) (string, error) {
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
			} `json:"conversationState"`
		}
		dec := json.NewDecoder(bytes.NewReader(body))
		dec.UseNumber()
		if err := dec.Decode(&envelope); err != nil {
			t.Error(err)
		}
		if calls == 1 {
			conversation = envelope.ConversationState.ConversationID
			return "tool", nil
		}
		if conversation == "" || conversation != envelope.ConversationState.ConversationID {
			t.Error("session affinity changed")
		}
		found := 0
		for _, entry := range envelope.ConversationState.History {
			for _, call := range entry.Assistant.Calls {
				found++
				number, ok := call.Input["id"].(json.Number)
				if !ok || number != wantNumber || call.ID != "http-tool" || call.Name != "lookup" {
					t.Errorf("current exact object lost: %#v", call.Input)
				}
			}
		}
		if found != 1 {
			t.Errorf("historical calls = %d", found)
		}
		return "text", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	definition := agent.AgentDefinition{Transport: agent.TransportSSE, DefaultModel: agent.ModelRef{Provider: string(provider), Model: "fixture-model"}, Tools: []agent.ToolDefinition{{Name: "lookup", Parameters: map[string]any{"type": "object", "properties": map[string]any{"id": map[string]any{"type": "integer"}}, "required": []string{"id"}}, Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
		executions++
		return agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: "local result"}}}, nil
	}}}}
	a, err := agent.NewAgent(definition, agent.WithSnapshot(agent.AgentSnapshot{SessionID: "authored-session"}))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := a.PromptText(ctx, "find"); err != nil {
		t.Fatal(err)
	}
	if err := a.PromptText(ctx, "again"); err != nil {
		t.Fatal(err)
	}
	a.FollowUp(agent.NewUserTextMessage("continue"))
	if err := a.Continue(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 4 || executions != 1 || a.Snapshot().Error != "" {
		t.Fatal("cross-run lifecycle", calls, executions, a.Snapshot().Error)
	}
	encoded, err := json.Marshal(a.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	var restored agent.AgentSnapshot
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	b, err := agent.NewAgent(definition, agent.WithSnapshot(restored))
	if err != nil {
		t.Fatal(err)
	}
	if err := b.PromptText(ctx, "restored"); err != nil {
		t.Fatal(err)
	}
	if calls != 5 || executions != 1 || b.Snapshot().Error != "" {
		t.Fatal("restored lifecycle", calls, executions, b.Snapshot().Error)
	}
}
