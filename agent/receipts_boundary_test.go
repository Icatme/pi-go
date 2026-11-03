package agent

import (
	"context"

	"github.com/Icatme/pi-go/executionreceipt"
	"github.com/Icatme/pi-go/internal/receiptcapture"
	"testing"
)

func TestReceiptWrapperRejectsLegacyRunnerContext(t *testing.T) {
	// Every model runner uses withJournal, even without a persistent journal.
	// Session/checkpoint wrappers execute through that same core runner boundary.
	ctx := withJournal(context.Background(), nil)
	entered := false
	tool := ToolDefinition{Name: "write", Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
		entered = true
		return ToolResult{}, nil
	}}
	_, h, err := RunToolCallWithReceipts(ctx, ToolCall{ID: "1", Name: "write", ParsedArgs: map[string]any{}}, RunToolCallOptions{Tools: []ToolDefinition{tool}}, 1)
	if err == nil || h != nil || entered {
		t.Fatal("unsupported runner allowed in-memory capture")
	}
}
func TestLegacyRunnerCannotReuseOuterReceiptCapability(t *testing.T) {
	ctx, h, err := receiptcapture.Begin(context.Background(), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer receiptcapture.CloseRegistration(h)
	ctx, seal, err := receiptcapture.Enter(ctx, "agent", "outer", "outer")
	if err != nil {
		t.Fatal(err)
	}
	defer seal()
	ctx = withJournal(ctx, nil)
	if executionreceipt.RequireCapture(ctx) == nil || executionreceipt.ReportLocalCommit(ctx) {
		t.Fatal("runner inherited outer capture")
	}
	entered := false
	tool := ToolDefinition{Name: "write", Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
		entered = true
		return ToolResult{}, nil
	}}
	outcome := RunToolCall(ctx, ToolCall{ID: "1", Name: "write", ParsedArgs: map[string]any{}}, RunToolCallOptions{Tools: []ToolDefinition{tool}})
	if outcome.Err == nil || entered {
		t.Fatal("blocked ledger reached body")
	}
}
