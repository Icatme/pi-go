package agent

import (
	"context"

	"github.com/Icatme/pi-go/executionreceipt"
	"github.com/Icatme/pi-go/internal/receiptcapture"
)

// RunToolCallWithReceipts executes a direct call with bounded in-memory capture
// for actual executor entries, including children and Codemode host calls.
// The returned handle is authoritative independently of hooks/output failures.
// Legacy outcome/Execution JSON is unchanged and does not contain these facts.
// No session, journal, checkpoint or model-runner persistence is provided.
// Existing runner contexts and nested wrappers are rejected before execution.
// Retain the handle after cancellation: outstanding bodies may still commit.
func RunToolCallWithReceipts(ctx context.Context, call ToolCall, options RunToolCallOptions, maxCalls int) (ToolCallOutcome, *executionreceipt.Handle, error) {
	ctx, h, err := receiptcapture.Begin(ctx, maxCalls)
	if err != nil {
		return ToolCallOutcome{}, nil, err
	}
	defer receiptcapture.CloseRegistration(h)
	return RunToolCall(ctx, call, options), h, nil
}
