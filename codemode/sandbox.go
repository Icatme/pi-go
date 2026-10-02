package codemode

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/Icatme/pi-go/codemode/internal/assets"
	"github.com/Icatme/pi-go/codemode/internal/quickjs"
	"github.com/tetratelabs/wazero"
)

//go:embed prelude.js
var prelude string

// wazero v1.12.0 initializes a process-wide version cache from its compiler
// engine constructor without synchronization (wazero/wazero#2532). Serialize
// our runtime construction; module compilation and VM execution stay parallel.
var runtimeCreation sync.Mutex

type callKey struct {
	run uint64
	id  int
}

type Sandbox struct {
	config        Config
	runtime       wazero.Runtime
	compiled      wazero.CompiledModule
	ctx           context.Context
	cancel        context.CancelFunc
	runSlots      chan struct{}
	callSlots     chan struct{}
	mu            sync.Mutex
	closed        bool
	runtimeClosed bool
	nextRun       uint64
	runs          map[uint64]context.CancelFunc
	active        map[callKey]OutstandingCall
	changed       chan struct{}
}

func NewSandbox(ctx context.Context, config Config) (*Sandbox, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	hash := sha256.Sum256(assets.WASM)
	if hex.EncodeToString(hash[:]) != assets.SHA256 {
		return nil, fmt.Errorf("embedded QuickJS digest mismatch")
	}
	var rt wazero.Runtime
	func() {
		runtimeCreation.Lock()
		defer runtimeCreation.Unlock()
		rt = wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithCloseOnContextDone(true).WithMemoryLimitPages(config.MemoryLimitPages))
	}()
	compiled, err := rt.CompileModule(ctx, assets.WASM)
	if err != nil {
		_ = rt.Close(context.Background())
		return nil, err
	}
	if err := quickjs.RegisterImports(ctx, rt, compiled); err != nil {
		_ = rt.Close(context.Background())
		return nil, err
	}
	lifetime, cancel := context.WithCancel(context.WithoutCancel(ctx))
	return &Sandbox{config: config, runtime: rt, compiled: compiled, ctx: lifetime, cancel: cancel, runSlots: make(chan struct{}, config.MaxConcurrentRuns), callSlots: make(chan struct{}, config.MaxConcurrentCalls), runs: map[uint64]context.CancelFunc{}, active: map[callKey]OutstandingCall{}, changed: make(chan struct{})}, nil
}

func (s *Sandbox) notifyLocked() { close(s.changed); s.changed = make(chan struct{}) }

// Run owns one new reactor instance until it is actually closed. Its host
// executions can outlive cancellation, and remain tracked by Sandbox.Close.
func (s *Sandbox) Run(ctx context.Context, code string, options RunOptions) (result Result, err error) {
	// Even a pre-VM failure returns bounded presentation caps to integrations.
	tokens := s.config.MaxOutputTokens
	if options.MaxOutputTokens > 0 {
		tokens = min(tokens, options.MaxOutputTokens)
	}
	result.OutputLimitBytes = min(s.config.MaxOutputBytes, tokens*4)
	result.OutputLimitItems = s.config.MaxOutputItems
	result.OutputReservedBytes = min(max(0, options.OutputReserveBytes), (result.OutputLimitBytes+3)/4)
	source, timeout, tokens, err := sourceOptions(code, s.config, options)
	if err != nil {
		return result, &ScriptError{Code: "options", Message: err.Error(), Diagnostic: boundedDiagnostic(err.Error()), Err: err}
	}
	result.OutputLimitBytes = min(s.config.MaxOutputBytes, tokens*4)
	result.OutputReservedBytes = min(max(0, options.OutputReserveBytes), (result.OutputLimitBytes+3)/4)
	tools, catalog, err := copyTools(options.Tools, s.config.MaxCatalogBytes)
	if err != nil {
		return result, &ScriptError{Code: "catalog", Message: err.Error(), Err: err}
	}
	namespaces, err := copyNamespaces(options.Namespaces, catalog, s.config.MaxCatalogBytes)
	if err != nil {
		return result, &ScriptError{Code: "catalog", Message: err.Error(), Err: err}
	}
	catalogJSON, err := json.Marshal(catalog)
	namespaceJSON, namespaceErr := json.Marshal(namespaces)
	if err != nil || namespaceErr != nil || len(catalogJSON)+len(namespaceJSON) > s.config.MaxCatalogBytes {
		return result, &ScriptError{Code: "catalog", Message: "serialized catalog exceeds limit", Err: err}
	}
	var runCtx context.Context
	var cancel context.CancelFunc
	if timeout > 0 {
		runCtx, cancel = context.WithTimeout(ctx, timeout)
	} else {
		runCtx, cancel = context.WithCancel(ctx)
	}
	defer cancel()
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	select {
	case s.runSlots <- struct{}{}:
	case <-runCtx.Done():
		return result, runCtx.Err()
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		<-s.runSlots
		return result, &ScriptError{Code: "closed", Message: "sandbox is closed"}
	}
	s.nextRun++
	runID := s.nextRun
	s.runs[runID] = cancel
	s.notifyLocked()
	s.mu.Unlock()
	r := newRunState(s, runID, runCtx, cancel, tools, catalog, namespaces, tokens, options.OutputReserveBytes, options.Sequential)
	var storeRevision uint64
	var vm *quickjs.VM
	defer func() {
		contextErr := runCtx.Err()
		r.stop()
		if vm != nil {
			vm.Close()
		}
		<-s.runSlots
		s.mu.Lock()
		delete(s.runs, runID)
		s.notifyLocked()
		s.mu.Unlock()
		result = r.snapshot()
		if r.exited {
			err = nil
			contextErr = nil
		}
		if err != nil && contextErr != nil {
			code := "canceled"
			if errors.Is(contextErr, context.DeadlineExceeded) {
				code = "deadline"
			}
			err = &ScriptError{Code: code, Message: contextErr.Error(), Diagnostic: contextErr.Error(), Err: contextErr}
		} else if err != nil {
			var script *ScriptError
			if !errors.As(err, &script) {
				diagnostic := ""
				// Parse faults are guest-generated and name only the source location.
				// WASM traps, runtime errors and other Go causes are host-only.
				if strings.HasPrefix(err.Error(), "SyntaxError:") {
					diagnostic = boundedDiagnostic(err.Error())
				}
				err = &ScriptError{Code: "script", Message: err.Error(), Diagnostic: diagnostic, Err: err}
			}
		}
		if err == nil && contextErr == nil && options.Store != nil && r.storeDirty {
			err = options.Store.commit(storeRevision, r.store, r.storeBytes)
		}
	}()
	defer quickjs.Recover(&err)
	if options.Store != nil {
		r.storeLimit = min(r.storeLimit, options.Store.maxBytes)
		r.store, r.storeBytes, storeRevision, err = options.Store.snapshot(r.storeLimit)
		if err != nil {
			return result, &ScriptError{Code: "store_limit", Message: err.Error(), Err: err}
		}
	}
	vm, err = quickjs.New(runCtx, s.runtime, s.compiled, fmt.Sprintf("codemode-%d", runID), s.config.HeapBytes, s.config.StackBytes, max(s.config.MaxArgumentBytes, s.config.MaxOutputBytes, s.config.MaxCatalogBytes)+8192, r.host, r.stringLimit)
	if err != nil {
		return result, &ScriptError{Code: "sandbox", Message: err.Error(), Err: err}
	}
	control := vm.Eval(prelude+"("+string(catalogJSON)+",{storeValueBytes:"+strconv.Itoa(r.storeLimit)+"})", "codemode-prelude.js")
	settle := vm.Prop(control, "settle")
	finish := vm.Prop(control, "finish")
	errorReport := vm.Prop(control, "error")
	fn := vm.Eval("(async function(){"+source+"\n})", "codemode.js")
	promise := vm.Invoke(fn)
	vm.Free(fn)
	vm.Call("qjs_promise_mark_as_handled", promise)
	for {
		for vm.PendingJobs() {
			vm.Job()
		}
		state := vm.Call("qjs_promise_state", promise)
		if state != 0 {
			value := vm.Call("qjs_promise_result", promise)
			if state == 2 {
				return result, r.scriptFailure(vm, errorReport, value)
			}
			out := vm.Invoke(finish, value)
			ok := vm.Prop(out, "ok")
			completed := vm.String(ok, 5) == "true"
			vm.Free(ok)
			if !completed {
				failure := vm.Prop(out, "error")
				return result, r.scriptFailure(vm, errorReport, failure)
			}
			vm.Free(out)
			vm.Free(value)
			for vm.PendingJobs() {
				vm.Job()
			}
			if unhandled := vm.Rejections(); len(unhandled) > 0 {
				return result, r.scriptFailure(vm, errorReport, unhandled[0])
			}
			vm.Free(promise)
			vm.Free(settle)
			vm.Free(finish)
			vm.Free(errorReport)
			vm.Free(control)
			return result, nil
		}
		if r.pending() == 0 {
			return result, &ScriptError{Code: "pending_promise", Message: "script awaits a Promise with no pending host work", Diagnostic: "script awaits a Promise with no pending host work; await tool calls and use Promise.all for concurrent work"}
		}
		select {
		case reply := <-r.completions:
			r.releaseCompletion(len(reply.data))
			id := vm.Eval(strconv.Itoa(reply.id), "codemode-settle.js")
			response := string(reply.data)
			if reply.failure != nil {
				response = callFailure(reply.failure)
			}
			data := vm.NewString(response)
			out := vm.Invoke(settle, id, data)
			vm.Free(out)
			vm.Free(id)
			vm.Free(data)
		case <-runCtx.Done():
			return result, runCtx.Err()
		}
	}
}

func (r *runState) scriptFailure(vm *quickjs.VM, report, value uint64) error {
	h := vm.Invoke(report, value)
	raw := vm.String(h, 16384)
	vm.Free(h)
	var detail struct {
		Message, Stack, Code string
		CauseID              int `json:"causeId"`
	}
	if err := json.Unmarshal([]byte(raw), &detail); err != nil {
		return &ScriptError{Code: "script", Message: "cannot decode script failure", Err: err}
	}
	var cause error
	r.mu.Lock()
	if e := r.failures[detail.CauseID]; e != nil {
		cause = e
		// Public JS properties can be mutated, but private identity always selects
		// the original safe host presentation and execution failure.
		detail.Message, detail.Code = e.Message, e.Code
	}
	r.mu.Unlock()
	message := detail.Message
	if location := scriptLocation.FindString(detail.Stack); location != "" {
		message = location + ": " + message
	}
	diagnostic := boundedDiagnostic(message)
	return &ScriptError{Code: detail.Code, Message: diagnostic, Diagnostic: diagnostic, Err: cause}
}

var scriptLocation = regexp.MustCompile(`codemode\.js:[0-9]+(?::[0-9]+)?`)

func boundedDiagnostic(value string) string {
	value = strings.ToValidUTF8(value, "\uFFFD")
	if len(value) <= 4096 {
		return value
	}
	limit := 4093
	for !utf8.ValidString(value[:limit]) {
		limit--
	}
	return value[:limit] + "…"
}

// Close stops admission, cancels VMs and tools, and waits only as long as ctx.
// It never returns success while hosts are outstanding. Repeated Close calls
// can finish cleanup after an earlier bounded CloseError.
func (s *Sandbox) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.cancel()
	s.notifyLocked()
	s.mu.Unlock()
	for {
		s.mu.Lock()
		vms := len(s.runs)
		hosts := len(s.active)
		changed := s.changed
		if vms == 0 && !s.runtimeClosed {
			s.runtimeClosed = true
			s.mu.Unlock()
			if err := s.runtime.Close(ctx); err != nil {
				return err
			}
			continue
		}
		if vms == 0 && hosts == 0 {
			s.mu.Unlock()
			return nil
		}
		s.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			s.mu.Lock()
			out := make([]OutstandingCall, 0, len(s.active))
			for _, call := range s.active {
				out = append(out, call)
			}
			s.mu.Unlock()
			return &CloseError{Outstanding: out, Err: ctx.Err()}
		}
	}
}
