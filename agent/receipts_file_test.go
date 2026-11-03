package agent_test

import (
	"context"
	"errors"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/executionreceipt"
	"os"
	"path/filepath"
	"testing"
)

func TestReceiptRetainsActualFileCommitAfterHookFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "committed")
	tool := agent.ToolDefinition{Name: "write", Execute: func(ctx context.Context, _ agent.ToolExecutionContext) (agent.ToolResult, error) {
		if err := executionreceipt.RequireCapture(ctx); err != nil {
			return agent.ToolResult{}, err
		}
		if err := os.WriteFile(path, []byte("actual committed bytes"), 0600); err != nil {
			return agent.ToolResult{}, err
		}
		if !executionreceipt.ReportLocalCommit(ctx) {
			t.Error("commit report rejected")
		}
		return agent.ToolResult{}, nil
	}}
	options := agent.RunToolCallOptions{Tools: []agent.ToolDefinition{tool}, AfterToolCall: func(ctx context.Context, _ agent.AfterToolCallContext) (agent.AfterToolCallResult, error) {
		if executionreceipt.ReportLocalCommit(ctx) {
			t.Error("hook could report on sealed body")
		}
		return agent.AfterToolCallResult{}, errors.New("result withheld")
	}}
	call := agent.ToolCall{ID: "write-1", Name: "write", ParsedArgs: map[string]any{}}
	legacy := agent.RunToolCall(context.Background(), call, options)
	if legacy.Err == nil {
		t.Fatal("legacy invocation bypassed capture guard")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy invocation changed disk: %v", err)
	}
	outcome, h, err := agent.RunToolCallWithReceipts(context.Background(), call, options, 1)
	if err != nil || outcome.Err == nil {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "actual committed bytes" {
		t.Fatalf("data=%q err=%v", data, err)
	}
	if r := h.Snapshot(); len(r) != 1 || !r[0].Committed() || !r[0].Completed() {
		t.Fatal(r)
	}
}
