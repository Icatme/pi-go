package session

import (
	"context"
	"encoding/json"
	"github.com/Icatme/pi-go/agent"
	"testing"
)

func TestSummaryCallbackOwnsDetachedPrefix(t *testing.T) {
	shared := map[string]any{"nested": []any{"original"}}
	call := agent.Message{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{{ID: "one", Name: "write", Arguments: json.RawMessage(`{"n":9007199254740993}`)}}, Metadata: shared}
	tail := agent.NewUserTextMessage("retained")
	tail.Metadata = shared
	plan := CompactionPlan{MessagesToSummarize: []agent.Message{agent.NewUserTextMessage("old"), call, agent.NewToolResultMessage(call.ToolCalls[0], agent.ToolResult{}, false)}, RetainedTail: []agent.Message{tail}, TokensBefore: 4}
	before, _ := json.Marshal(plan)
	var retainedRequest SummaryRequest
	data, err := Compact(context.Background(), plan, func(_ context.Context, r SummaryRequest) (SummaryResult, error) {
		retainedRequest = r
		r.Messages[1].Metadata["nested"].([]any)[0] = "changed"
		r.Messages[1].ToolCalls[0].Arguments[0] = '['
		return SummaryResult{Summary: "summary"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	after, _ := json.Marshal(plan)
	if string(before) != string(after) {
		t.Fatal("callback mutated caller plan")
	}
	if data.RetainedTail[0].Metadata["nested"].([]any)[0] != "original" {
		t.Fatal("callback mutated retained tail")
	}
	retainedRequest.Messages[1].Metadata["nested"].([]any)[0] = "later"
	if data.RetainedTail[0].Metadata["nested"].([]any)[0] != "original" {
		t.Fatal("late callback reference aliases result")
	}
}
