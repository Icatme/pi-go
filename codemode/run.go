package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"unicode/utf8"
)

type invocation struct {
	call   HostCall
	jsID   int
	invoke func(context.Context, HostCall) (json.RawMessage, error)
}
type completion struct {
	id      int
	data    []byte
	failure *CallError
}
type runState struct {
	s           *Sandbox
	id          uint64
	ctx         context.Context
	cancel      context.CancelFunc
	tools       map[string]Tool
	catalog     []toolDescription
	queue       chan invocation
	queueSlots  chan struct{}
	completions chan completion
	mu          sync.Mutex
	accepting   bool
	exited      bool
	sequential  bool
	records     []CallRecord
	failures    map[int]*CallError
	errorBytes  int
	unsettled   int
	bridgeBytes int
	outputs     []Output
	outputBytes int
	outputLimit int
	store       map[string]string
	storeBytes  int
	storeDirty  bool
	storeLimit  int
}

func newRunState(s *Sandbox, id uint64, ctx context.Context, cancel context.CancelFunc, tools []Tool, catalog []toolDescription, tokens int, sequential bool) *runState {
	r := &runState{s: s, id: id, ctx: ctx, cancel: cancel, tools: map[string]Tool{}, catalog: catalog, queue: make(chan invocation, s.config.MaxQueue), queueSlots: make(chan struct{}, s.config.MaxQueue), completions: make(chan completion, s.config.MaxCalls), accepting: true, sequential: sequential, failures: map[int]*CallError{}, store: map[string]string{}, storeLimit: s.config.MaxStoreBytes, outputLimit: min(s.config.MaxOutputBytes, tokens*4)}
	for _, t := range tools {
		r.tools[t.Name] = t
	}
	go r.dispatch()
	return r
}

func (r *runState) stop() { r.mu.Lock(); r.accepting = false; r.cancel(); r.mu.Unlock() }
func (r *runState) snapshot() Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := Result{Outputs: append([]Output(nil), r.outputs...), Calls: append([]CallRecord(nil), r.records...)}
	for i := range out.Calls {
		if out.Calls[i].Execution.Local == "entered" {
			out.Calls[i].Code = "canceled"
			out.Calls[i].Execution.Remote = "unknown"
		} else if out.Calls[i].Execution.Local == "not_started" && out.Calls[i].Code == "" && r.ctx.Err() != nil {
			out.Calls[i].Code = "canceled"
		}
	}
	return out
}
func (r *runState) pending() int { r.mu.Lock(); defer r.mu.Unlock(); return r.unsettled }
func (r *runState) releaseCompletion(n int) {
	r.mu.Lock()
	r.bridgeBytes -= n
	r.unsettled--
	r.mu.Unlock()
}

func success(value any) string {
	raw, err := json.Marshal(struct {
		OK    bool `json:"ok"`
		Value any  `json:"value"`
	}{true, value})
	if err != nil {
		return fail("internal", err.Error())
	}
	return string(raw)
}
func fail(code, message string) string {
	return callFailure(&CallError{Code: code, Message: message, Execution: Execution{Local: "not_started", Remote: "not_dispatched"}})
}
func callFailure(e *CallError) string {
	raw, _ := json.Marshal(struct {
		OK    bool       `json:"ok"`
		Error *CallError `json:"error"`
	}{false, e})
	return string(raw)
}

func (r *runState) host(op string, args []string) string {
	if r.ctx.Err() != nil {
		return fail("canceled", r.ctx.Err().Error())
	}
	for _, s := range args {
		if !utf8.ValidString(s) {
			return fail("unsupported_string", "Unpaired surrogate cannot cross the host UTF-8 boundary")
		}
	}
	switch op {
	case "call":
		if len(args) != 3 {
			return fail("arguments", "invalid bridge arguments")
		}
		jsID, err := strconv.Atoi(args[0])
		if err != nil || jsID < 1 || jsID > 1<<20 {
			return fail("arguments", "invalid call sequence")
		}
		return r.admit(jsID, args[1], args[2])
	case "text":
		if len(args) != 1 {
			return fail("arguments", "invalid text arguments")
		}
		return r.appendOutput(Output{Type: "text", Text: args[0]}, len(args[0]))
	case "image":
		if len(args) != 1 {
			return fail("arguments", "invalid image arguments")
		}
		return r.image(args[0])
	case "search":
		if len(args) != 1 || len(args[0]) > 8192 {
			return fail("arguments", "invalid search request")
		}
		v, err := searchTools(r.catalog, args[0])
		if err != nil {
			return fail("arguments", err.Error())
		}
		return success(v)
	case "describe", "namespace":
		if len(args) != 1 || len(args[0]) > 512 {
			return fail("arguments", "invalid description request")
		}
		var name string
		if err := json.Unmarshal([]byte(args[0]), &name); err != nil {
			return fail("arguments", "description name must be a string")
		}
		if op == "describe" {
			for _, t := range r.catalog {
				if t.Name == name {
					return success(t)
				}
			}
			return success(nil)
		}
		tools := make([]toolDescription, 0)
		for _, t := range r.catalog {
			if name != "" && t.Namespace == name {
				tools = append(tools, t)
			}
		}
		if len(tools) == 0 {
			return success(nil)
		}
		return success(struct {
			Name  string            `json:"name"`
			Tools []toolDescription `json:"tools"`
		}{name, tools})
	case "store":
		if len(args) != 2 || len(args[0]) > 256 || args[0] == "" {
			return fail("store_key", "invalid store key")
		}
		if len(args[1]) > r.storeLimit {
			return fail("store_limit", "value exceeds store byte limit")
		}
		units := 0
		for _, runeValue := range args[1] {
			units++
			if runeValue > 0xffff {
				units++
			}
			if units > 256<<10 {
				return fail("store_limit", "serialized store value exceeds 256 Ki UTF-16 code units")
			}
		}
		if err := ValidateJSON([]byte(args[1])); err != nil {
			return fail("unsupported_json", err.Error())
		}
		if _, ok := r.store[args[0]]; !ok && len(r.store) >= 256 {
			return fail("store_limit", "store entry limit exceeded")
		}
		newBytes := r.storeBytes - len(r.store[args[0]]) + len(args[1])
		if _, ok := r.store[args[0]]; !ok {
			newBytes += len(args[0])
		}
		if newBytes > r.storeLimit {
			return fail("store_limit", "store byte limit exceeded")
		}
		r.store[args[0]] = args[1]
		r.storeBytes = newBytes
		r.storeDirty = true
		return success(nil)
	case "delete":
		if len(args) != 1 || len(args[0]) > 256 || args[0] == "" {
			return fail("store_key", "invalid store key")
		}
		if value, ok := r.store[args[0]]; ok {
			delete(r.store, args[0])
			r.storeBytes -= len(args[0]) + len(value)
			r.storeDirty = true
		}
		return success(nil)
	case "load":
		if len(args) != 1 || len(args[0]) > 256 {
			return fail("store_key", "invalid store key")
		}
		if value, ok := r.store[args[0]]; ok {
			return success(value)
		}
		return success(nil)
	case "exit":
		if len(args) != 0 {
			return fail("arguments", "invalid exit arguments")
		}
		r.exited = true
		r.cancel()
		return success(nil)
	default:
		return fail("unknown_api", "unknown host operation")
	}
}

// Per-field bounds are selected before the bridge copies from linear memory.
func (r *runState) stringLimit(op string, index int) int {
	switch op {
	case "call":
		if index == 0 {
			return 16
		}
		if index == 1 {
			return 128
		}
		return r.s.config.MaxArgumentBytes
	case "text":
		return r.outputLimit
	case "image":
		return r.outputLimit * 2
	case "search":
		return 8192
	case "describe", "namespace":
		return 512
	case "store":
		if index == 0 {
			return 256
		}
		return r.s.config.MaxStoreBytes
	case "load", "delete":
		return 256
	default:
		return 256
	}
}

func (r *runState) appendOutput(out Output, n int) string {
	if n > r.outputLimit-r.outputBytes || len(r.outputs) >= r.s.config.MaxOutputItems {
		return fail("output_limit", "output byte/item limit exceeded")
	}
	r.outputBytes += n
	r.outputs = append(r.outputs, out)
	return success(nil)
}

func (r *runState) admit(jsID int, name, payload string) string {
	t, ok := r.tools[name]
	if !ok {
		return fail("unknown_tool", "tool is not in the fixed allowlist")
	}
	if len(payload) > r.s.config.MaxArgumentBytes {
		return fail("argument_limit", "arguments exceed byte limit")
	}
	if err := ValidateJSON([]byte(payload)); err != nil {
		return fail("argument_invalid", err.Error())
	}
	r.mu.Lock()
	if !r.accepting || r.ctx.Err() != nil {
		r.mu.Unlock()
		return fail("canceled", "script is no longer accepting calls")
	}
	if len(r.records) >= r.s.config.MaxCalls || len(payload) > r.s.config.MaxCompletionBytes-r.bridgeBytes {
		r.mu.Unlock()
		return fail("call_limit", "call count or bridge byte budget exceeded")
	}
	select {
	case r.queueSlots <- struct{}{}:
	default:
		r.mu.Unlock()
		return fail("queue_limit", "host call queue is full")
	}
	id := len(r.records) + 1
	r.records = append(r.records, CallRecord{ID: id, Name: name, Execution: Execution{Local: "not_started", Remote: "not_dispatched"}})
	r.bridgeBytes += len(payload)
	r.unsettled++
	r.s.mu.Lock()
	r.s.active[callKey{r.id, id}] = OutstandingCall{RunID: r.id, ID: id, Name: name, Execution: Execution{Local: "not_started", Remote: "not_dispatched"}}
	r.s.notifyLocked()
	r.s.mu.Unlock()
	// Budget and queue slots are reserved before copying arguments.
	call := invocation{call: HostCall{ID: id, Name: name, Arguments: json.RawMessage(payload)}, jsID: jsID, invoke: t.Invoke}
	r.queue <- call
	r.mu.Unlock()
	return success(nil)
}

func (r *runState) dispatch() {
	for {
		select {
		case call := <-r.queue:
			<-r.queueSlots
			if r.ctx.Err() != nil {
				r.finish(call, nil, r.ctx.Err(), false)
				r.retire(call)
				continue
			}
			if r.sequential {
				done := make(chan struct{})
				go func() { r.execute(call); close(done) }()
				select {
				case <-done:
				case <-r.ctx.Done():
					r.drainQueue()
					return
				}
			} else {
				go r.execute(call)
			}
		case <-r.ctx.Done():
			r.drainQueue()
			return
		}
	}
}

func (r *runState) drainQueue() {
	r.mu.Lock()
	r.accepting = false
	r.mu.Unlock()
	for {
		select {
		case c := <-r.queue:
			<-r.queueSlots
			r.finish(c, nil, r.ctx.Err(), false)
			r.retire(c)
		default:
			return
		}
	}
}

func (r *runState) execute(call invocation) {
	select {
	case r.s.callSlots <- struct{}{}:
	case <-r.ctx.Done():
		r.finish(call, nil, r.ctx.Err(), false)
		r.retire(call)
		return
	}
	// This permit belongs to the real host invocation until Invoke returns.
	defer func() { <-r.s.callSlots; r.retire(call) }()
	if r.ctx.Err() != nil {
		r.finish(call, nil, r.ctx.Err(), false)
		return
	}
	r.recordExecution(call.call.ID, Execution{Local: "entered", Remote: "unknown"}, "")
	var raw json.RawMessage
	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				err = &CallError{Code: "host_panic", Message: fmt.Sprintf("host invocation panicked: %v", p), Execution: Execution{Local: "returned", Remote: "unknown"}}
			}
		}()
		raw, err = call.invoke(r.ctx, call.call)
	}()
	r.finish(call, raw, err, true)
}

func (r *runState) recordExecution(id int, facts Execution, code string) {
	r.mu.Lock()
	r.records[id-1].Execution = facts
	r.records[id-1].Code = code
	r.mu.Unlock()
	r.s.mu.Lock()
	key := callKey{r.id, id}
	v := r.s.active[key]
	v.Execution = facts
	r.s.active[key] = v
	r.s.notifyLocked()
	r.s.mu.Unlock()
}

func (r *runState) finish(call invocation, raw json.RawMessage, invokeErr error, entered bool) {
	facts := Execution{Local: "not_started", Remote: "not_dispatched"}
	if entered {
		facts = Execution{Local: "returned", Remote: "not_applicable"}
	}
	var failure *CallError
	if invokeErr != nil {
		if !errors.As(invokeErr, &failure) {
			code := "host"
			if errors.Is(invokeErr, context.Canceled) {
				code = "canceled"
			}
			if errors.Is(invokeErr, context.DeadlineExceeded) {
				code = "deadline"
			}
			remote := "not_dispatched"
			if entered {
				remote = "unknown"
			}
			failure = &CallError{Code: code, Message: invokeErr.Error(), Execution: Execution{Local: facts.Local, Remote: remote}, Err: invokeErr}
		}
		// Copy, so setting bridge correlation does not mutate the caller's error.
		copyOf := *failure
		failure = &copyOf
		if failure.CallID == "" {
			failure.CallID = strconv.Itoa(call.jsID)
		}
		if failure.Execution.Local == "" {
			failure.Execution.Local = facts.Local
		}
		if failure.Execution.Remote == "" {
			failure.Execution.Remote = "unknown"
		}
		facts = failure.Execution
	}
	if failure == nil {
		if len(raw) > r.s.config.MaxResultBytes {
			failure = &CallError{Code: "result_rejected", Message: "result exceeds byte limit", Execution: facts}
		}
		if len(raw) == 0 {
			raw = json.RawMessage(`null`)
		}
		if failure == nil {
			if err := ValidateJSON(raw); err != nil {
				failure = &CallError{Code: "result_rejected", Message: err.Error(), Execution: facts, Err: err}
				var precision *JSONError
				if errors.As(err, &precision) {
					failure.ReasonCode = precision.Code
				}
			}
		}
		if failure != nil {
			failure.CallID = strconv.Itoa(call.jsID)
		}
	}
	// Reserve the aggregate success bytes under the same lock used for
	// admission, before copying raw or allocating an envelope. Failures have
	// separately bounded fixed metadata and are serialized only by the owner.
	var response []byte
	r.mu.Lock()
	r.bridgeBytes -= len(call.call.Arguments)
	accepting := r.accepting && r.ctx.Err() == nil
	reserved := 0
	if accepting && failure == nil {
		reserved = len(raw) + len(`{"ok":true,"value":`) + 1
		if reserved > r.s.config.MaxCompletionBytes-r.bridgeBytes {
			reserved = 0
			failure = &CallError{Code: "result_rejected", Message: "completion byte budget exceeded", CallID: strconv.Itoa(call.jsID), Execution: facts}
		} else {
			r.bridgeBytes += reserved
		}
	}
	code := ""
	if failure != nil {
		if len(failure.Message) > 8192 {
			failure.Message = failure.Message[:8192]
		}
		if failure.Code == "" || len(failure.Code) > 128 {
			failure.Code = "host"
		}
		if len(failure.CallID) > 256 {
			failure.CallID = strconv.Itoa(call.jsID)
		}
		if len(failure.Execution.Local) > 32 || len(failure.Execution.Remote) > 32 {
			failure.Execution = Execution{Local: facts.Local, Remote: "unknown"}
		}
		code = failure.Code
		if len(failure.ReasonCode) > 128 {
			failure.ReasonCode = ""
		}
		detailBytes := len(failure.Message) + len(failure.Code) + len(failure.CallID)
		if accepting && len(r.failures) < r.s.config.MaxErrorRecords && detailBytes <= r.s.config.MaxErrorBytes-r.errorBytes {
			r.failures[call.jsID] = failure
			r.errorBytes += detailBytes
		}
	}
	r.records[call.call.ID-1].Execution = facts
	r.records[call.call.ID-1].Code = code
	if failure != nil {
		r.records[call.call.ID-1].ReasonCode = failure.ReasonCode
	}
	r.mu.Unlock()
	if !accepting {
		return
	}
	if failure == nil {
		response = make([]byte, 0, reserved)
		response = append(response, `{"ok":true,"value":`...)
		response = append(response, raw...)
		response = append(response, '}')
	} else {
		copyOf := *failure
		copyOf.Err = nil
		// Every failure can be delivered even when payload budget is exhausted.
		// Queue metadata is bounded by MaxCalls; full messages/chains have the
		// independent detail budget above.
		if len(copyOf.Message) > 512 {
			copyOf.Message = copyOf.Message[:512]
		}
		failure = &copyOf
	}
	// Hosts update only Go records. They never call, schedule or close a VM.
	select {
	case r.completions <- completion{id: call.jsID, data: response, failure: failure}:
	case <-r.ctx.Done():
		r.mu.Lock()
		r.bridgeBytes -= reserved
		r.mu.Unlock()
	}
}

func (r *runState) retire(call invocation) {
	r.s.mu.Lock()
	delete(r.s.active, callKey{r.id, call.call.ID})
	r.s.notifyLocked()
	r.s.mu.Unlock()
}
