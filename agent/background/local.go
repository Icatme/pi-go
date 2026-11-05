// Package background runs explicitly supplied native tools asynchronously in one
// process. It does not provide process isolation, durable jobs, or safe retries.
package background

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/executionreceipt"
)

const maxOutputBytes = 64 << 10
const maxRequestBytes = 1 << 20
const maxReceipts = 256

type Request struct {
	ID    string         `json:"id"`
	Scope string         `json:"scope"`
	Call  agent.ToolCall `json:"call"`
}
type Receipt struct {
	Token       uint64 `json:"token"`
	ParentToken uint64 `json:"parent_token"`
	Domain      string `json:"domain"`
	CallID      string `json:"call_id"`
	ToolName    string `json:"tool_name"`
	Committed   bool   `json:"committed"`
	Completed   bool   `json:"completed"`
}
type Observation struct {
	ID       string          `json:"id"`
	State    string          `json:"state"`
	Output   json.RawMessage `json:"output,omitempty"`
	Failed   bool            `json:"failed"`
	Receipts []Receipt       `json:"receipts,omitempty"`
}

// Backend owns immutable operation identities within its configured scope.
// A successful Start admits at most one physical invocation for an identity:
// repeating the same request must not execute it again, even after exit or Stop.
// Conflicting identity reuse must fail. Stop must latch cancellation, including
// a tombstone for an unseen valid identity so a later Start cannot execute it.
// An unbound tombstone remains unknown: a replacement backend cannot prove a
// lost operation exited. Only receiving the immutable Start request may bind
// that tombstone and prove that this instance prevented that dispatch.
//
// Inspect may expose partial, monotonic receipt facts while running. An exited
// observation guarantees all admitted native executor bodies actually exited;
// cancellation alone is insufficient. Unknown never establishes absence of an
// invocation or effects. Start errors may have uncertain outcomes, so callers
// must reconcile the original identity rather than blindly retrying new IDs.
// Implementations must return detached snapshots and preserve receipts across
// cancellation, output failures, and late commits until actual body exit.
type Backend interface {
	Start(context.Context, Request) error
	Inspect(context.Context, string) (Observation, error)
	Stop(context.Context, string) error
}

type operation struct {
	identity    []byte
	observation Observation
	cancel      context.CancelFunc
	handle      *executionreceipt.Handle
	stopped     bool
}

// Local owns a volatile identity registry and concurrency quota. Host callbacks
// and tools remain responsible for authorization and truthful commit reporting.
// A Local must not be copied after first use.
type Local struct {
	mu            sync.Mutex
	root          context.Context
	scope         string
	options       agent.RunToolCallOptions
	allowed       map[string]bool
	operations    map[string]*operation
	active, limit int
}

var _ Backend = (*Local)(nil)

// NewLocal uses ctx as the explicit host lifetime, never a dispatch lifetime.
// The host must keep callback captures immutable or synchronize them itself.
func NewLocal(ctx context.Context, scope string, options agent.RunToolCallOptions, maxConcurrent int) (*Local, error) {
	if ctx == nil || !validID(scope) || maxConcurrent < 1 {
		return nil, errors.New("background: invalid host configuration")
	}
	// Freeze definition data without serializing function capabilities.
	frozen, err := freezeTools(options.Tools)
	if err != nil {
		return nil, err
	}
	options.Tools = frozen
	raw, err := json.Marshal(options.Context)
	if err != nil {
		return nil, errors.New("background: invalid host context")
	}
	var frozenContext agent.AgentContext
	if err = json.Unmarshal(raw, &frozenContext); err != nil {
		return nil, errors.New("background: invalid host context")
	}
	frozenContext.Tools, err = freezeTools(options.Context.Tools)
	if err != nil {
		return nil, err
	}
	options.Context = frozenContext
	raw, err = json.Marshal(options.AssistantMessage)
	if err != nil {
		return nil, errors.New("background: invalid assistant message")
	}
	var frozenMessage agent.Message
	if err = json.Unmarshal(raw, &frozenMessage); err != nil {
		return nil, errors.New("background: invalid assistant message")
	}
	options.AssistantMessage = frozenMessage
	allowed := make(map[string]bool, len(frozen))
	for _, tool := range frozen {
		if !validID(tool.Name) || allowed[tool.Name] || tool.Execute == nil {
			return nil, errors.New("background: invalid tool allowlist")
		}
		allowed[tool.Name] = true
	}
	return &Local{root: ctx, scope: scope, options: options, allowed: allowed, operations: make(map[string]*operation), limit: maxConcurrent}, nil
}
func freezeTools(in []agent.ToolDefinition) ([]agent.ToolDefinition, error) {
	out := make([]agent.ToolDefinition, len(in))
	for i, t := range in {
		raw, err := json.Marshal(t)
		if err != nil {
			return nil, errors.New("background: invalid tool definition")
		}
		var data agent.ToolDefinition
		if json.Unmarshal(raw, &data) != nil {
			return nil, errors.New("background: invalid tool definition")
		}
		t.Parameters = data.Parameters
		t.OutputSchema = data.OutputSchema
		t.ConstrainedSampling = data.ConstrainedSampling
		t.ChildTools, err = freezeTools(t.ChildTools)
		if err != nil {
			return nil, err
		}
		out[i] = t
	}
	return out, nil
}
func validID(id string) bool {
	return id != "" && len(id) <= 4096 && utf8.ValidString(id) && strings.TrimSpace(id) == id && !strings.ContainsAny(id, "\x00\r\n")
}
func checkContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("background: context required")
	}
	return ctx.Err()
}

// Start reserves identity and quota synchronously. Only the same immutable
// request may reuse an ID. Rejected Start is not a general no-effect guarantee.
func (l *Local) Start(ctx context.Context, r Request) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if !validID(r.ID) || r.Scope != l.scope || !l.allowed[r.Call.Name] || (r.Call.ID != "" && !validID(r.Call.ID)) {
		return errors.New("background: invalid request identity, scope, or tool")
	}
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > maxRequestBytes {
		return errors.New("background: invalid or oversized request")
	}
	var frozen Request
	if json.Unmarshal(raw, &frozen) != nil {
		return errors.New("background: invalid request encoding")
	}
	// Validate the JSON boundary here; native parsers, schemas and policy hooks
	// remain authoritative and run once, inside the receipt-aware invocation.
	if len(frozen.Call.Arguments) > 0 {
		var args map[string]json.RawMessage
		if json.Unmarshal(frozen.Call.Arguments, &args) != nil || args == nil {
			return errors.New("background: arguments must be a JSON object")
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if old, ok := l.operations[r.ID]; ok {
		if old.stopped && old.identity == nil {
			old.identity = append([]byte(nil), raw...)
			old.observation.State = "exited"
			return errors.New("background: identity stopped before start")
		}
		if !bytes.Equal(old.identity, raw) {
			return errors.New("background: identity conflict")
		}
		if old.stopped && old.cancel == nil {
			return errors.New("background: identity stopped before start")
		}
		return nil
	}
	if err := l.root.Err(); err != nil {
		return err
	}
	if l.active >= l.limit {
		return errors.New("background: concurrency limit reached")
	}
	opctx, cancel := context.WithCancel(l.root)
	op := &operation{identity: append([]byte(nil), raw...), observation: Observation{ID: r.ID, State: "running"}, cancel: cancel}
	l.operations[r.ID] = op
	l.active++
	go l.run(opctx, op, frozen.Call)
	return nil
}
func (l *Local) run(ctx context.Context, op *operation, call agent.ToolCall) {
	defer op.cancel()
	// Unexpected panics before the receipt handle is returned cannot establish
	// executor exit. Quarantine the identity and quota rather than claiming exit.
	defer func() {
		if recover() != nil {
			l.mu.Lock()
			op.observation.State = "unknown"
			op.observation.Failed = true
			op.observation.Output = nil
			l.mu.Unlock()
		}
	}()
	outcome, handle, err := agent.RunToolCallWithReceipts(ctx, call, l.options, maxReceipts)
	l.mu.Lock()
	op.handle = handle
	op.observation.Failed = err != nil || outcome.Err != nil || outcome.IsError
	l.mu.Unlock()
	// A fresh, uncanceled wait deliberately outlives the host cancellation: the
	// underlying native body may still run and report a late committed effect.
	if handle != nil {
		_ = handle.Wait(context.Background())
	}
	var output []byte
	failed := err != nil || outcome.Err != nil || outcome.IsError
	if err == nil && outcome.Err == nil {
		output, err = encodeResult(outcome.Result)
		if err != nil || len(output) > maxOutputBytes {
			output = nil
			failed = true
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	op.observation.Output = output
	op.observation.Failed = failed
	op.observation.State = "exited"
	l.active--
}

// Inspect returns detached observations. Unknown never proves absence of effects.
func (l *Local) Inspect(ctx context.Context, id string) (Observation, error) {
	if err := checkContext(ctx); err != nil {
		return Observation{}, err
	}
	if !validID(id) {
		return Observation{}, errors.New("background: invalid identity")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	op, ok := l.operations[id]
	if !ok {
		return Observation{ID: id, State: "unknown"}, nil
	}
	o := op.observation
	o.Output = append(json.RawMessage(nil), o.Output...)
	if op.handle != nil {
		for _, r := range op.handle.Snapshot() {
			o.Receipts = append(o.Receipts, Receipt{Token: r.Token(), ParentToken: r.ParentToken(), Domain: r.Domain(), CallID: r.CallID(), ToolName: r.ToolName(), Committed: r.Committed(), Completed: r.Completed()})
		}
	}
	return o, nil
}

// Stop is cancellation intent, not proof of exit. Unknown valid IDs receive a
// permanent, unknown tombstone so an eventual racing Start cannot execute them.
// Unknown remains fenced until that exact dispatch is presented to Start; Stop
// alone cannot establish exit of an operation lost with another Local instance.
func (l *Local) Stop(ctx context.Context, id string) error {
	if err := checkContext(ctx); err != nil {
		return err
	}
	if !validID(id) {
		return errors.New("background: invalid identity")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	op, ok := l.operations[id]
	if !ok {
		l.operations[id] = &operation{stopped: true, observation: Observation{ID: id, State: "unknown", Failed: true}}
		return nil
	}
	op.stopped = true
	if op.cancel != nil {
		op.cancel()
	}
	return nil
}

// A user-supplied JSON marshaler is another host callback and may panic. Bodies
// have already exited here, so an encoding failure must not strand their quota.
func encodeResult(result agent.ToolResult) (output []byte, err error) {
	defer func() {
		if recover() != nil {
			output = nil
			err = errors.New("background: result encoding failed")
		}
	}()
	return json.Marshal(result)
}
