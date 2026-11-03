// Package receiptcapture owns bounded, invocation-local in-memory facts.
package receiptcapture

import (
	"context"
	"errors"
	"sync"
	"unicode/utf8"
)

var (
	ErrUnavailable = errors.New("execution receipt capture is unavailable")
	ErrCapacity    = errors.New("execution receipt capacity exhausted")
	ErrUnsupported = errors.New("execution receipts are not supported by this execution context")
)

// Receipt is an immutable observation, not a durable transaction receipt.
// Unreported effects are unknown, including for completed invocations.
type Receipt struct {
	token, parent            uint64
	domain, callID, toolName string
	committed, completed     bool
}

func (r Receipt) Token() uint64       { return r.token }
func (r Receipt) ParentToken() uint64 { return r.parent }
func (r Receipt) Domain() string      { return r.domain }
func (r Receipt) CallID() string      { return r.callID }
func (r Receipt) ToolName() string    { return r.toolName }

// Committed reports at least one reported local commit. False means unknown,
// never proof that no effect occurred.
func (r Receipt) Committed() bool { return r.committed }

// Completed means the executor body exited, not that it succeeded or was durable.
func (r Receipt) Completed() bool { return r.completed }

// Handle retains observations after a canceled caller returns. It must not be
// discarded until outstanding invocations have been reconciled by the host.
type Handle struct {
	mu           sync.Mutex
	records      []Receipt
	limit        int
	registration bool
	active       int
	changed      chan struct{}
}
type ledgerKey struct{}
type callKey struct{}
type blockedKey struct{}
type slot struct {
	h     *Handle
	index int
}

func Block(ctx context.Context) context.Context { return context.WithValue(ctx, blockedKey{}, true) }
func blocked(ctx context.Context) bool          { b, _ := ctx.Value(blockedKey{}).(bool); return b }

func Begin(ctx context.Context, maxCalls int) (context.Context, *Handle, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if blocked(ctx) {
		return ctx, nil, ErrUnsupported
	}
	if ctx.Value(ledgerKey{}) != nil {
		return ctx, nil, errors.New("nested receipt wrapper is unsupported; use the existing execution scope")
	}
	if maxCalls < 1 || maxCalls > 4096 {
		return ctx, nil, errors.New("receipt capacity must be between 1 and 4096")
	}
	h := &Handle{limit: maxCalls, registration: true, changed: make(chan struct{})}
	return context.WithValue(ctx, ledgerKey{}, h), h, nil
}
func (h *Handle) notify() { close(h.changed); h.changed = make(chan struct{}) }
func CloseRegistration(h *Handle) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.registration = false
	h.notify()
}
func (h *Handle) Snapshot() []Receipt {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]Receipt(nil), h.records...)
}

// Wait waits for registration to close and every admitted body to actually exit.
// Cancellation cannot stop an uncooperative executor; it only stops this wait.
func (h *Handle) Wait(ctx context.Context) error {
	for {
		h.mu.Lock()
		done := !h.registration && h.active == 0
		ch := h.changed
		h.mu.Unlock()
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ch:
		}
	}
}

// Enter is a no-op in legacy execution. In capture mode it reserves before I/O.
func Enter(ctx context.Context, domain, callID, name string) (context.Context, func(), error) {
	h, _ := ctx.Value(ledgerKey{}).(*Handle)
	if h == nil {
		return ctx, func() {}, nil
	}
	if blocked(ctx) {
		return ctx, func() {}, ErrUnsupported
	}
	if len(callID) > 4096 || len(name) > 4096 || !utf8.ValidString(callID) || !utf8.ValidString(name) {
		return ctx, func() {}, errors.New("receipt identity exceeds limits")
	}
	h.mu.Lock()
	parent := uint64(0)
	if s, ok := ctx.Value(callKey{}).(*slot); ok && s.h == h {
		parent = h.records[s.index].token
		if h.records[s.index].completed {
			h.mu.Unlock()
			return ctx, func() {}, ErrUnavailable
		}
	}
	if len(h.records) >= h.limit || (!h.registration && parent == 0) {
		h.mu.Unlock()
		return ctx, func() {}, ErrCapacity
	}
	i := len(h.records)
	h.records = append(h.records, Receipt{token: uint64(i + 1), parent: parent, domain: domain, callID: callID, toolName: name})
	h.active++
	h.notify()
	h.mu.Unlock()
	s := &slot{h: h, index: i}
	seal := func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if !h.records[i].completed {
			h.records[i].completed = true
			h.active--
			h.notify()
		}
	}
	return context.WithValue(ctx, callKey{}, s), seal, nil
}
func Require(ctx context.Context) error {
	if ctx == nil || blocked(ctx) {
		return ErrUnavailable
	}
	s, _ := ctx.Value(callKey{}).(*slot)
	if s == nil {
		return ErrUnavailable
	}
	s.h.mu.Lock()
	defer s.h.mu.Unlock()
	if s.h.records[s.index].completed {
		return ErrUnavailable
	}
	return nil
}
func Commit(ctx context.Context) bool {
	if ctx == nil || blocked(ctx) {
		return false
	}
	s, _ := ctx.Value(callKey{}).(*slot)
	if s == nil {
		return false
	}
	s.h.mu.Lock()
	defer s.h.mu.Unlock()
	if s.h.records[s.index].completed {
		return false
	}
	s.h.records[s.index].committed = true
	s.h.notify()
	return true
}

// MarshalJSON refuses a misleading empty-object encoding. No persistence format
// is defined for in-process receipts.
func (Receipt) MarshalJSON() ([]byte, error) { return nil, ErrUnsupported }
func (*Receipt) UnmarshalJSON([]byte) error  { return ErrUnsupported }

// Handle must not be copied. Keep the pointer returned by the wrapper.
func (*Handle) MarshalJSON() ([]byte, error) { return nil, ErrUnsupported }
func (*Handle) UnmarshalJSON([]byte) error   { return ErrUnsupported }
