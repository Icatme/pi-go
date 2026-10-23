package session

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

func TestCompactionProtectedTailKeepsHostContextAndToolPairs(t *testing.T) {
	call := agent.ToolCall{ID: "call", Name: "read", Arguments: json.RawMessage(`{}`)}
	messages := []agent.Message{
		agent.NewUserTextMessage("old goal"), agent.NewTextMessage(agent.RoleAssistant, "old answer"),
		agent.NewUserTextMessage("protected host context"), agent.NewTextMessage(agent.RoleAssistant, "current steps"),
		agent.NewUserTextMessage("latest request"),
		{Role: agent.RoleAssistant, ToolCalls: []agent.ToolCall{call}},
		agent.NewToolResultMessage(call, agent.ToolResult{Content: []agent.Part{agent.NewTextPart("current evidence")}}, false),
	}
	branch := compactionMessageBranch(t, messages...)
	before, _ := json.Marshal(branch)
	for _, count := range []int{4, 5} { // A mid-turn boundary expands to the whole turn.
		plan, err := PrepareCompaction(branch, CompactionOptions{ProtectedTailMessages: count, EstimateTokens: func(agent.Message) int64 { return 1 }})
		if err != nil || plan == nil {
			t.Fatal(plan, err)
		}
		if !sameCompactionJSON(plan.MessagesToSummarize, messages[:2]) || !sameCompactionJSON(plan.RetainedTail, messages[2:]) {
			t.Fatal("protected run was split", plan)
		}
		_, err = Compact(t.Context(), *plan, func(_ context.Context, r SummaryRequest) (SummaryResult, error) {
			if !sameCompactionJSON(r.Messages, messages[:2]) {
				t.Fatal("protected context reached summarizer")
			}
			return SummaryResult{Summary: "old reference"}, nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	after, _ := json.Marshal(branch)
	if string(before) != string(after) {
		t.Fatal("source branch changed")
	}
}

func sameCompactionJSON(a, b any) bool {
	x, xe := json.Marshal(a)
	y, ye := json.Marshal(b)
	return xe == nil && ye == nil && string(x) == string(y)
}

func TestCompactionProtectedTailBoundariesAndSystemDeclarations(t *testing.T) {
	system := agent.NewSystemMessage(agent.SystemMessagePayload{Content: "policy"})
	branch := compactionMessageBranch(t, agent.NewUserTextMessage("old"), agent.NewTextMessage(agent.RoleAssistant, "answer"), system, agent.NewUserTextMessage("new"), agent.NewTextMessage(agent.RoleAssistant, "answer"))
	for _, count := range []int{-1, 5} {
		if _, err := PrepareCompaction(branch, CompactionOptions{ProtectedTailMessages: count}); err == nil {
			t.Fatalf("invalid count %d accepted", count)
		}
	}
	plan, err := PrepareCompaction(branch, CompactionOptions{ProtectedTailMessages: 4})
	if err != nil || plan != nil {
		t.Fatal("fully protected branch should have no cut", plan, err)
	}
	plan, err = PrepareCompaction(branch, CompactionOptions{ProtectedTailMessages: 2})
	if err != nil || plan == nil || len(plan.MessagesToSummarize) != 2 || len(plan.RetainedTail) != 3 || plan.RetainedTail[0].System == nil {
		t.Fatal("system declaration counted or lost", plan, err)
	}
	plan, err = PrepareCompaction(branch, CompactionOptions{ProtectedTailMessages: 2, KeepRecentTokens: 1000})
	if err != nil || plan != nil {
		t.Fatal("protection bypassed keep-recent token policy", plan, err)
	}
}
