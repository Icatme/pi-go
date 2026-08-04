package langgraphgo

import (
	"encoding/json"
	"testing"
	"time"

	agent "github.com/Icatme/pi-go/agent"
)

func TestSessionStateJSONRoundTripPreservesExpandedRuntimeFields(t *testing.T) {
	t.Parallel()

	original := SessionState{
		Snapshot: agent.AgentSnapshot{
			SessionID:    "thread-123",
			SystemPrompt: "be precise",
			Model: agent.ModelRef{
				Provider: "openai-codex",
				Model:    "gpt-5.4",
				ProviderConfig: agent.ProviderConfig{
					BaseURL: "https://example.test",
					APIKey:  "api-key",
					Headers: map[string]string{"x-trace-id": "trace-1"},
				},
			},
			Messages: []agent.Message{
				agent.NewTextMessage(agent.RoleUser, "hello"),
				{
					ID:         "assistant-1",
					Role:       agent.RoleAssistant,
					Provider:   "openai-codex",
					API:        "openai-codex-responses",
					Model:      "gpt-5.4",
					ResponseID: "resp-1",
					Parts: []agent.Part{
						{Type: agent.PartTypeText, Text: "done"},
						{Type: agent.PartTypeThinking, Text: "reason", Signature: "sig-1"},
					},
					ToolCalls: []agent.ToolCall{{
						ID:         "tool-normalized-1",
						OriginalID: "tool-raw-1",
						Name:       "lookup",
						Arguments:  json.RawMessage(`{"value":42}`),
					}},
					Timestamp:  time.Unix(1700000100, 0).UTC(),
					StopReason: agent.StopReasonToolUse,
				},
				agent.NewToolResultMessage(agent.ToolCall{
					ID:         "tool-normalized-1",
					OriginalID: "tool-raw-1",
					Name:       "lookup",
				}, agent.ToolResult{
					Content: []agent.Part{
						{Type: agent.PartTypeText, Text: "42"},
						agent.NewImagePart("img-base64", "image/png"),
					},
				}, false),
			},
			PendingToolCalls: []agent.PendingToolCall{{
				ToolCallID:         "tool-normalized-1",
				OriginalToolCallID: "tool-raw-1",
				ToolName:           "lookup",
			}},
			Error: "transient",
		},
		Prompts: []agent.Message{
			agent.NewTextMessage(agent.RoleUser, "queued prompt"),
		},
		Steering: []agent.Message{
			agent.NewTextMessage(agent.RoleUser, "queued steering"),
		},
		FollowUps: []agent.Message{
			agent.NewTextMessage(agent.RoleUser, "queued follow-up"),
		},
		Mode: RunModeContinue,
	}

	payload, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("marshal session state: %v", err)
	}

	var decoded SessionState
	if err := json.Unmarshal(payload, &decoded); err != nil {
		t.Fatalf("unmarshal session state: %v", err)
	}

	if decoded.Snapshot.SessionID != "thread-123" || decoded.Snapshot.Model.Provider != "openai-codex" || decoded.Snapshot.Model.ProviderConfig.APIKey != "api-key" {
		t.Fatalf("expected snapshot model fields to round-trip, got %+v", decoded.Snapshot)
	}
	if len(decoded.Snapshot.Messages) != 3 {
		t.Fatalf("expected snapshot messages to round-trip, got %+v", decoded.Snapshot.Messages)
	}
	if decoded.Snapshot.Messages[1].ResponseID != "resp-1" || decoded.Snapshot.Messages[1].ToolCalls[0].OriginalID != "tool-raw-1" {
		t.Fatalf("expected assistant runtime fields to round-trip, got %+v", decoded.Snapshot.Messages[1])
	}
	if decoded.Snapshot.Messages[2].ToolResult == nil || decoded.Snapshot.Messages[2].ToolResult.OriginalToolCallID != "tool-raw-1" {
		t.Fatalf("expected tool result raw id to round-trip, got %+v", decoded.Snapshot.Messages[2].ToolResult)
	}
	if len(decoded.Snapshot.PendingToolCalls) != 1 || decoded.Snapshot.PendingToolCalls[0].OriginalToolCallID != "tool-raw-1" {
		t.Fatalf("expected pending tool calls to round-trip, got %+v", decoded.Snapshot.PendingToolCalls)
	}
	if len(decoded.Prompts) != 1 || len(decoded.Steering) != 1 || len(decoded.FollowUps) != 1 || decoded.Mode != RunModeContinue {
		t.Fatalf("expected queued session state to round-trip, got %+v", decoded)
	}
}

func TestCloneSessionStatePreservesExpandedRuntimeFields(t *testing.T) {
	t.Parallel()

	original := SessionState{
		Snapshot: agent.AgentSnapshot{
			SessionID: "thread-456",
			Model: agent.ModelRef{
				Provider: "kimi-coding",
				Model:    "k2p5",
				ProviderConfig: agent.ProviderConfig{
					Headers: map[string]string{"x-trace-id": "trace-2"},
				},
			},
			Messages: []agent.Message{{
				Role:       agent.RoleAssistant,
				ResponseID: "resp-2",
				Parts:      []agent.Part{{Type: agent.PartTypeThinking, Text: "reason", Signature: "sig-2"}},
				ToolCalls: []agent.ToolCall{{
					ID:         "tool-normalized-2",
					OriginalID: "tool-raw-2",
					Name:       "inspect",
				}},
				Timestamp: time.Unix(1700000200, 0).UTC(),
			}},
			PendingToolCalls: []agent.PendingToolCall{{
				ToolCallID:         "tool-normalized-2",
				OriginalToolCallID: "tool-raw-2",
				ToolName:           "inspect",
			}},
		},
		Prompts: []agent.Message{
			agent.NewTextMessage(agent.RoleUser, "queued"),
		},
	}

	cloned := cloneSessionState(original)

	original.Snapshot.Model.ProviderConfig.Headers["x-trace-id"] = "mutated"
	original.Snapshot.Messages[0].Parts[0].Text = "mutated"
	original.Snapshot.Messages[0].ToolCalls[0].OriginalID = "mutated"
	original.Snapshot.PendingToolCalls[0].OriginalToolCallID = "mutated"
	original.Prompts[0].Parts[0].Text = "mutated"

	if cloned.Snapshot.Model.ProviderConfig.Headers["x-trace-id"] != "trace-2" {
		t.Fatalf("expected provider config header clone to remain isolated, got %+v", cloned.Snapshot.Model.ProviderConfig.Headers)
	}
	if cloned.Snapshot.Messages[0].Parts[0].Text != "reason" || cloned.Snapshot.Messages[0].Parts[0].Signature != "sig-2" {
		t.Fatalf("expected cloned thinking part to be preserved, got %+v", cloned.Snapshot.Messages[0].Parts)
	}
	if cloned.Snapshot.Messages[0].ToolCalls[0].OriginalID != "tool-raw-2" {
		t.Fatalf("expected cloned tool raw id to be preserved, got %+v", cloned.Snapshot.Messages[0].ToolCalls)
	}
	if cloned.Snapshot.PendingToolCalls[0].OriginalToolCallID != "tool-raw-2" {
		t.Fatalf("expected cloned pending tool raw id to be preserved, got %+v", cloned.Snapshot.PendingToolCalls)
	}
	if cloned.Prompts[0].Parts[0].Text != "queued" {
		t.Fatalf("expected queued prompt clone to remain isolated, got %+v", cloned.Prompts)
	}
}
