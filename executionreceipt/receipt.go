// Package executionreceipt exposes host-reported, in-process local effect facts.
// These facts are neither persisted nor a transaction/CAS/retry guarantee.
// Only explicit receipt-aware execution wrappers establish capture. Tools that
// require this guarantee must call RequireCapture before any side effect.
package executionreceipt

import (
	"context"

	"github.com/Icatme/pi-go/internal/receiptcapture"
)

type Receipt = receiptcapture.Receipt
type Handle = receiptcapture.Handle

// RequireCapture checks that this actual invocation has reserved capacity and
// has not exited. A parent ledger alone does not grant capture. Authorization is
// separate: this does not replace the tool's current permission checks.
func RequireCapture(ctx context.Context) error { return receiptcapture.Require(ctx) }

// ReportLocalCommit records that at least one local effect committed. Report
// synchronously at the commit point. False means no live capture is available.
// True does not prove durability, complete success, remote effects, or safe retry.
// Missing reports always mean unknown, even after ordinary success or a panic.
func ReportLocalCommit(ctx context.Context) bool { return receiptcapture.Commit(ctx) }
