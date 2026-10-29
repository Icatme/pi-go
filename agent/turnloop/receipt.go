package turnloop

import (
	"context"
	"errors"
)

// Disposition describes delivery, not model use, business success or persistence.
type Disposition string

const (
	// DispositionUnhandled means the input was not handed to an invocation.
	DispositionUnhandled Disposition = "unhandled"
	// DispositionHandedOff means a valid (stream, nil) start was returned. It does not
	// prove the model used this input, or that any journal commit succeeded.
	DispositionHandedOff Disposition = "handed-off"
	// DispositionUncertain means an invalid invocation polled hooks but returned
	// no stream, or returned both a stream and a start error. The host must reconcile; the loop never automatically replays it.
	DispositionUncertain Disposition = "uncertain"
)

// ReceiptResult describes the final disposition of one admission. Inputs in the
// same invocation share its settlement result; this is not a per-message ack.
type ReceiptResult struct {
	Disposition Disposition
	// RunID is observed from the invocation's events and may be empty (including
	// on invalid start results whose stream is only drained for cleanup).
	RunID string
	// Err is the raw invocation settlement error, or the cause that prevented
	// delivery. In particular explicit immediate cancellation is not normalized
	// to nil as it may be by TurnLoop.Wait. A nil Err proves no business outcome.
	Err error
}

// Receipt is an optional per-admission handle returned by PushTracked. It holds
// no input or snapshot. Once delivered it shares an invocation future; the loop
// retains no unbounded list of delivered receipts. Do not copy a Receipt.
type Receipt struct {
	ready  chan struct{}
	future *receiptFuture
}

type receiptFuture struct {
	done   chan struct{}
	result ReceiptResult
}

func newReceiptFuture() *receiptFuture               { return &receiptFuture{done: make(chan struct{})} }
func (f *receiptFuture) settle(result ReceiptResult) { f.result = result; close(f.done) }

// bind is called exactly once by the loop worker or a delivery hook under mu.
// A rolled-back initial reservation is deliberately not bound to its failed run.
func (r *Receipt) bind(future *receiptFuture) {
	if r != nil {
		r.future = future
		close(r.ready)
	}
}

// Wait waits for this admission's disposition and invocation settlement, not
// for loop termination. The second return value reports only waiter errors;
// inspect ReceiptResult.Err for execution/start/stop errors. Canceling ctx does
// not cancel or withdraw the input. Multiple concurrent Wait calls are safe.
// Do not wait for the current invocation from synchronous OnEvent, model/tool,
// journal or control-hook callbacks: settlement requires those callbacks to return.
func (r *Receipt) Wait(ctx context.Context) (ReceiptResult, error) {
	if ctx == nil {
		return ReceiptResult{}, errors.New("agent/turnloop: nil receipt wait context")
	}
	if r == nil || r.ready == nil {
		return ReceiptResult{}, errors.New("agent/turnloop: uninitialized receipt")
	}
	if err := waitReceiptChannel(ctx, r.ready); err != nil {
		return ReceiptResult{}, err
	}
	if err := waitReceiptChannel(ctx, r.future.done); err != nil {
		return ReceiptResult{}, err
	}
	return r.future.result, nil
}

func waitReceiptChannel(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-done:
		return nil
	default:
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		select {
		case <-done:
			return nil
		default:
			return ctx.Err()
		}
	}
}
