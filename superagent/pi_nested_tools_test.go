package superagent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/executionreceipt"
)

func TestPiWorkerEndsNestedToolBatch(t *testing.T) {
	for _, tc := range []struct {
		name        string
		callID      string
		endWithHook bool
		childFails  bool
	}{
		{name: "terminating container", callID: "outer"},
		{name: "finish hook with an unnamed parent call", endWithHook: true},
		{name: "terminating container handles child failure", callID: "outer", childFails: true},
		{name: "finish hook after child failure", callID: "outer", endWithHook: true, childFails: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const private = "private tool detail"
			leaf := agent.ToolDefinition{Name: "leaf", Execute: func(ctx context.Context, _ agent.ToolExecutionContext) (agent.ToolResult, error) {
				if !executionreceipt.ReportLocalCommit(ctx) {
					return agent.ToolResult{}, errors.New("missing receipt capture")
				}
				if tc.childFails {
					return agent.ToolResult{}, errors.New(private)
				}
				return agent.ToolResult{Content: []agent.Part{agent.NewTextPart(private)}}, nil
			}}
			container := agent.ToolDefinition{
				Name: "container", ChildTools: []agent.ToolDefinition{leaf},
				Execute: func(ctx context.Context, in agent.ToolExecutionContext) (agent.ToolResult, error) {
					// The container handles child failures locally and returns success.
					in.ChildCaller.Call(ctx, agent.ToolCall{Name: "leaf", Arguments: json.RawMessage(`{}`)})
					return agent.ToolResult{Content: []agent.Part{agent.NewTextPart(private)}, Terminate: !tc.endWithHook}, nil
				},
			}
			call := agent.ToolCall{ID: tc.callID, Name: "container", Arguments: json.RawMessage(`{}`)}
			def := agent.AgentDefinition{Model: piScript(call, agent.Message{}), Tools: []agent.ToolDefinition{container}}
			if tc.endWithHook {
				def.FinishTurn = func(context.Context, agent.AgentTurnContext) (agent.AgentTurnDecision, error) {
					return agent.AgentTurnDecision{Action: agent.TurnActionEnd}, nil
				}
			}
			work, err := PiWorker(def)
			if err != nil {
				t.Fatal(err)
			}
			out, err := work(t.Context(), Job{TaskID: "T"})
			if err != nil {
				t.Fatal(err)
			}
			wantStatus := "completed"
			if tc.childFails {
				wantStatus = "completed_with_tool_errors"
			}
			if out.Status != wantStatus || out.NeedsReview != tc.childFails || out.Summary == "" || strings.Contains(out.Summary, private) {
				t.Fatalf("status=%q review=%v summary=%q", out.Status, out.NeedsReview, out.Summary)
			}
			if len(out.Receipts) != 2 {
				t.Fatalf("receipts: %+v", out.Receipts)
			}
			parent, child := out.Receipts[0], out.Receipts[1]
			if parent.ToolName != "container" || parent.ParentToken != 0 || !parent.Completed || parent.Committed ||
				child.ToolName != "leaf" || child.ParentToken != parent.Token || !child.Completed || !child.Committed {
				t.Fatalf("lost executor facts: %+v", out.Receipts)
			}
		})
	}
}
