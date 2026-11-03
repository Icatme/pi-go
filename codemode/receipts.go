package codemode

import (
	"context"

	"github.com/Icatme/pi-go/executionreceipt"
	"github.com/Icatme/pi-go/internal/receiptcapture"
)

// RunWithReceipts adds bounded host-only local effect capture without changing
// Tool.Invoke, Execution, Result or JavaScript success/error schemas. Inspect
// the handle, not script output, before deciding whether any retry is safe.
// It is in-memory only: no session/checkpoint persistence or recovery support.
// Cancellation may return before a host call exits; retain the handle and use
// Wait or later snapshots to reconcile it. Missing reports are always unknown.
func (s *Sandbox) RunWithReceipts(ctx context.Context, code string, options RunOptions, maxCalls int) (Result, *executionreceipt.Handle, error) {
	ctx, h, err := receiptcapture.Begin(ctx, maxCalls)
	if err != nil {
		return Result{}, nil, err
	}
	defer receiptcapture.CloseRegistration(h)
	result, err := s.Run(ctx, code, options)
	return result, h, err
}
