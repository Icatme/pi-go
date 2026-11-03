package fstools

import (
	"context"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/executionreceipt"
	"github.com/Icatme/pi-go/internal/receiptcapture"
)

type capturedResult struct {
	agent.ToolResult
	Receipts *executionreceipt.Handle
}

func (r capturedResult) committed() bool {
	facts := r.Receipts.Snapshot()
	return len(facts) == 1 && facts[0].Completed() && facts[0].Committed()
}

func (r capturedResult) unknown() bool {
	facts := r.Receipts.Snapshot()
	return len(facts) == 1 && facts[0].Completed() && !facts[0].Committed()
}

// Each invocation gets a fresh ledger and executor slot. Bypass Agent's extra
// permission checks to preserve the original raw-executor fault injection points.
func executeCaptured(t *testing.T, ctx context.Context, execute agent.ToolExecutorFunc, in agent.ToolExecutionContext) (capturedResult, error) {
	t.Helper()
	ctx, h, err := receiptcapture.Begin(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer receiptcapture.CloseRegistration(h)
	ctx, seal, err := receiptcapture.Enter(ctx, "agent", "fixture", "fixture")
	if err != nil {
		t.Fatal(err)
	}
	defer seal()
	result, err := execute(ctx, in)
	seal()
	facts := h.Snapshot()
	if len(facts) != 1 || !facts[0].Completed() {
		t.Fatalf("raw invocation capture missing: %+v", facts)
	}
	return capturedResult{ToolResult: result, Receipts: h}, err
}
func publishCreateCaptured(t *testing.T, ctx context.Context, in agent.ToolExecutionContext, pending pendingCreation, data []byte) (capturedResult, error) {
	return executeCaptured(t, ctx, func(ctx context.Context, in agent.ToolExecutionContext) (agent.ToolResult, error) {
		return publishCreate(ctx, in, pending, data)
	}, in)
}
func publishFileCaptured(t *testing.T, ctx context.Context, in agent.ToolExecutionContext, pending pendingCreation, data []byte, operation string) (capturedResult, error) {
	return executeCaptured(t, ctx, func(ctx context.Context, in agent.ToolExecutionContext) (agent.ToolResult, error) {
		return publishFile(ctx, in, pending, data, operation)
	}, in)
}

type capturedOutcome struct {
	agent.ToolCallOutcome
	Receipts *executionreceipt.Handle
}

func (r capturedOutcome) committed() bool {
	callID := r.ToolCall.ID
	if r.Result.ChildCalls != nil && len(r.Result.ChildCalls.Calls) == 1 {
		callID = r.Result.ChildCalls.Calls[0].ToolCallID
	}
	matching, committed := 0, 0
	for _, fact := range r.Receipts.Snapshot() {
		if fact.Committed() {
			committed++
		}
		if fact.Domain() == "agent" && fact.CallID() == callID && fact.Completed() && fact.Committed() {
			matching++
		}
	}
	return matching == 1 && committed == 1
}

func runCaptured(t *testing.T, ctx context.Context, call agent.ToolCall, options agent.RunToolCallOptions) capturedOutcome {
	t.Helper()
	out, h, err := agent.RunToolCallWithReceipts(ctx, call, options, 16)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Wait(t.Context()); err != nil {
		t.Fatal(err)
	}
	return capturedOutcome{ToolCallOutcome: out, Receipts: h}
}
