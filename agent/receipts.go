package agent

import (
	"context"
	"errors"

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

// RunWithReceipts starts an explicitly non-persistent model run with bounded
// local effect capture. Drain stream.Events before stream.Wait as usual, then
// retain the separate handle until all actual bodies have been reconciled.
// Hooks.Journal must be nil. Legacy runs, sessions and checkpoints remain
// receipt-unaware; this is not a restart/recovery or automatic retry protocol.
// Existing snapshots/messages/events retain their old shape and omit receipts.
// Error scheduling is unchanged: models/JS can issue another call after failure.
// Capture is not a retry barrier, idempotency policy or automatic write safety.
func (r *Runner) RunWithReceipts(ctx context.Context, snapshot AgentSnapshot, prompts []Message, hooks LoopHooks, maxCalls int) (*RunStream, *executionreceipt.Handle, error) {
	if hooks.Journal != nil {
		return nil, nil, errors.New("agent: model receipt runs do not support journals")
	}
	ctx, h, err := receiptcapture.Begin(ctx, maxCalls)
	if err != nil {
		return nil, nil, err
	}
	return r.start(ctx, snapshot, prompts, runnerStartRun, hooks, h), h, nil
}
