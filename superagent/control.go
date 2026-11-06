package superagent

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/Icatme/pi-go/agent"
)

// A mailbox belongs to exactly one event's worker invocation. Its fields
// (other than the goroutine-safe context) are protected by SuperAgent.mu.
type activeRun struct {
	watchdogTurns, watchdogToolsStarted, watchdogToolsCompleted uint64
	target                                                      RunTarget
	ctx                                                         context.Context
	cancel                                                      context.CancelFunc
	returned                                                    bool
	aborted                                                     bool
	queue                                                       []steering
}

func (r *activeRun) phase() string {
	switch {
	case r.returned:
		return "finalizing"
	case r.aborted:
		return "abort_requested"
	default:
		return "running"
	}
}

type steering struct {
	text      string
	controlID string
	receipt   *SteerReceipt
}

const maxPendingSteers = 16
const maxSteerBytes = 4 << 10

// SteerReceipt reports whether a worker dequeued the instruction at an input
// boundary. Delivery does not guarantee a subsequent model or tool result.
type SteerReceipt struct {
	done      chan struct{}
	delivered bool // written once before done closes
}

// Wait returns false with no error when an acknowledged queue entry was never
// delivered. Canceling a wait does not withdraw the instruction. Wait can be
// called again, or concurrently by multiple observers.
func (r *SteerReceipt) Wait(ctx context.Context) (bool, error) {
	if r == nil || r.done == nil || ctx == nil {
		return false, errors.New("steering wait requires a receipt and context")
	}
	select {
	case <-r.done:
		return r.delivered, nil
	case <-ctx.Done():
		select {
		case <-r.done:
			return r.delivered, nil
		default:
			return false, ctx.Err()
		}
	}
}

func (a *SuperAgent) dropRunSteeringLocked(r *activeRun) {
	var ids []string
	for _, s := range r.queue {
		if s.controlID != "" {
			ids = append(ids, s.controlID)
		}
	}
	// A failed append has already cancelled the coordinator. Do not recursively
	// write during fatal cleanup; replay settles the remaining queued records.
	if len(ids) > 0 && a.fatal == nil {
		a.commitControls("not_delivered", ids)
	}
	for _, s := range r.queue {
		close(s.receipt.done)
	}
	r.queue = nil
}

func (a *SuperAgent) dropSteeringLocked() {
	for _, t := range a.tasks {
		if t.active != nil {
			a.dropRunSteeringLocked(t.active)
		}
	}
}

func (a *SuperAgent) activeLocked(target RunTarget) (*activeRun, error) {
	if a.fatal != nil {
		return nil, a.fatal
	}
	if err := a.ctx.Err(); err != nil {
		return nil, err
	}
	t := a.tasks[target.TaskID]
	if t == nil || t.active == nil || t.active.target != target || t.active.returned {
		return nil, errors.New("control target is not the active invocation")
	}
	return t.active, nil
}

// Steer queues a transient instruction for the specified active worker. The
// receipt settles when the worker polls, exits, or is aborted. The mailbox is
// bounded to 16 entries of at most 4 KiB each.
func (a *SuperAgent) Steer(target RunTarget, text string) (*SteerReceipt, error) {
	if len(text) > maxSteerBytes || strings.TrimSpace(text) == "" || !utf8.ValidString(text) {
		return nil, errors.New("steering requires 1–4096 UTF-8 bytes of nonblank text")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	r, err := a.activeLocked(target)
	if err != nil {
		return nil, err
	}
	if r.aborted {
		return nil, errors.New("worker cancellation already requested")
	}
	if len(r.queue) >= maxPendingSteers {
		return nil, errors.New("worker steering queue is full")
	}
	receipt := &SteerReceipt{done: make(chan struct{})}
	r.queue = append(r.queue, steering{text: text, receipt: receipt})
	a.publishLocked(target, "steer_queued")
	return receipt, nil
}

// Abort requests cancellation of this invocation only. Acknowledgement is not
// completion: the worker retains its slot until it exits and its report is
// recorded. An accepted abort requires reconciliation even on late success.
func (a *SuperAgent) Abort(target RunTarget) error {
	a.mu.Lock()
	r, err := a.activeLocked(target)
	if err != nil {
		a.mu.Unlock()
		return err
	}
	if !r.aborted {
		r.aborted = true
		a.dropRunSteeringLocked(r)
		a.publishLocked(target, "abort_requested")
	}
	a.mu.Unlock()
	r.cancel()
	return nil
}

func (a *SuperAgent) takeSteering(ctx context.Context, r *activeRun, mode agent.QueueMode) ([]agent.Message, error) {
	if ctx == nil {
		return nil, errors.New("steering poll requires a context")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.aborted {
		return nil, context.Canceled
	}
	if err := r.ctx.Err(); err != nil {
		return nil, err
	}
	if r.returned || a.tasks[r.target.TaskID].active != r {
		return nil, errors.New("steering poll is no longer active")
	}
	n := len(r.queue)
	switch mode {
	case "", agent.QueueModeOneAtATime:
		n = min(n, 1)
	case agent.QueueModeAll:
	default:
		return nil, errors.New("invalid steering queue mode")
	}
	if n == 0 {
		return nil, nil
	}
	var ids []string
	for _, s := range r.queue[:n] {
		if s.controlID != "" {
			ids = append(ids, s.controlID)
		}
	}
	if len(ids) > 0 {
		// QueueModeAll is one durable boundary, never a partial batch.
		if err := a.commitControls("delivered", ids); err != nil {
			return nil, err
		}
	}
	messages := make([]agent.Message, 0, n)
	for _, s := range r.queue[:n] {
		message := agent.NewUserTextMessage(s.text)
		if c := a.mainControl(s.controlID); c != nil && c.evidence {
			message = controlMessage(*c)
		}
		messages = append(messages, message)
		s.receipt.delivered = true
		close(s.receipt.done)
		a.publishLocked(r.target, "steer_delivered")
	}
	clear(r.queue[:n])
	r.queue = r.queue[n:]
	return messages, nil
}

func (a *SuperAgent) notifyWorkerProgress(r *activeRun, kind string) {
	switch kind {
	case "turn_start", "turn_end", "tool_start", "tool_end":
	default:
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !r.returned && !r.aborted && r.ctx.Err() == nil && a.tasks[r.target.TaskID].active == r {
		switch kind {
		case "turn_end":
			r.watchdogTurns++
		case "tool_start":
			r.watchdogToolsStarted++
		case "tool_end":
			if r.watchdogToolsCompleted < r.watchdogToolsStarted {
				r.watchdogToolsCompleted++
			}
		}
		a.publishLocked(r.target, kind)
	}
}
