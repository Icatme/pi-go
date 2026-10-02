package codemode

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tetratelabs/wazero"
)

func sandboxForTest(t *testing.T, c Config) *Sandbox {
	t.Helper()
	s, err := NewSandbox(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return s
}
func textOutput(r Result) string {
	var out []string
	for _, o := range r.Outputs {
		if o.Type == "text" {
			out = append(out, o.Text)
		}
	}
	return strings.Join(out, "\n")
}

func TestScriptIsolationStoreAndDiscovery(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	tool := Tool{Name: "issue_read", Description: "Read project issue details", Namespace: "github", Parameters: json.RawMessage(`{"type":"object"}`), Invoke: func(ctx context.Context, c HostCall) (json.RawMessage, error) { return c.Arguments, nil }}
	r, err := s.Run(context.Background(), `store("nul", {value:"a\u0000b"}); const x=load("nul"); x.value="changed";
text(load("nul")); text(await searchTools("issue",{limit:1}));
text(await describeTool("issue_read"));text((await describeTool("hidden"))===undefined);
text((await describeNamespace("hidden"))===undefined);text(await describeNamespace("github"));
text("issue_read" in tools);return [typeof process,typeof require,typeof fetch,typeof setTimeout,typeof WebAssembly];`, RunOptions{Tools: []Tool{tool}})
	if err != nil {
		t.Fatal(err)
	}
	out := textOutput(r)
	for _, want := range []string{`a\u0000b`, `issue_read`, `"github"`, `true`, `["undefined","undefined","undefined","undefined","undefined"]`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s in %s", want, out)
		}
	}
	r, err = s.Run(context.Background(), `return load("nul")===undefined`, RunOptions{})
	if err != nil || textOutput(r) != "true" {
		t.Fatalf("store leaked: %+v %v", r, err)
	}
}

func TestPromiseConcurrencyAndHostFailure(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	var current, maxSeen atomic.Int32
	type mark struct {
		ID int `json:"id"`
	}
	tool := Tool{Name: "work", Invoke: func(ctx context.Context, c HostCall) (json.RawMessage, error) {
		n := current.Add(1)
		defer current.Add(-1)
		for {
			old := maxSeen.Load()
			if n <= old || maxSeen.CompareAndSwap(old, n) {
				break
			}
		}
		select {
		case <-time.After(10 * time.Millisecond):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return json.Marshal(mark{c.ID})
	}}
	r, err := s.Run(context.Background(), `const values=await Promise.all([tools.work({}),tools.work({}),tools.work({})]);return values`, RunOptions{Tools: []Tool{tool}})
	if err != nil || maxSeen.Load() < 2 || len(r.Calls) != 3 {
		t.Fatalf("parallel %+v %v max=%d", r, err, maxSeen.Load())
	}
	original := errors.New("transport sentinel")
	tool.Invoke = func(context.Context, HostCall) (json.RawMessage, error) {
		return nil, &CallError{Code: "transport", Message: "bounded public message", CallID: "parent/1", Execution: Execution{Local: "returned", Remote: "unknown"}, Err: original}
	}
	r, err = s.Run(context.Background(), `await tools.work({})`, RunOptions{Tools: []Tool{tool}})
	if !errors.Is(err, original) || len(r.Calls) != 1 || r.Calls[0].Execution.Remote != "unknown" {
		t.Fatalf("failure chain %+v %v", r, err)
	}
	r, err = s.Run(context.Background(), `try {await tools.work({})} catch(e) {return {code:e.code,id:e.callId,remote:e.execution.remote}}`, RunOptions{Tools: []Tool{tool}})
	if err != nil || !strings.Contains(textOutput(r), `"id":"parent/1"`) || !strings.Contains(textOutput(r), `"code":"transport"`) {
		t.Fatalf("JS error facts %+v %v", r, err)
	}
}

func TestSequentialAdmissionAndAllBasicRecords(t *testing.T) {
	c := DefaultConfig()
	s := sandboxForTest(t, c)
	var order []int
	var mu sync.Mutex
	tool := Tool{Name: "write", Invoke: func(ctx context.Context, c HostCall) (json.RawMessage, error) {
		mu.Lock()
		order = append(order, c.ID)
		mu.Unlock()
		return json.RawMessage(`true`), nil
	}}
	r, err := s.Run(context.Background(), `await Promise.all(Array.from({length:1024},(_,i)=>tools.write({i})));return "ok"`, RunOptions{Tools: []Tool{tool}, Sequential: true})
	if err != nil || len(r.Calls) != 1024 || len(order) != 1024 {
		t.Fatalf("all records=%d order=%d err=%v", len(r.Calls), len(order), err)
	}
	for i, id := range order {
		if id != i+1 {
			t.Fatalf("admission order at %d: %d", i, id)
		}
	}
}

func TestUnsafeNumbersFailBeforeCallAndAfterResponse(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	var calls atomic.Int32
	tool := Tool{Name: "write", Invoke: func(ctx context.Context, c HostCall) (json.RawMessage, error) { calls.Add(1); return c.Arguments, nil }}
	for _, value := range []string{"9007199254740993", "NaN", "Infinity", "1n"} {
		r, err := s.Run(context.Background(), `try {await tools.write({id:`+value+`})} catch(e) {return e.code}`, RunOptions{Tools: []Tool{tool}})
		if err != nil || len(r.Calls) != 0 || calls.Load() != 0 {
			t.Fatalf("unsafe %s sent: %+v %v", value, r, err)
		}
	}
	r, err := s.Run(context.Background(), `return await tools.write({price:19.99, rate:0.1,id:9007199254740991})`, RunOptions{Tools: []Tool{tool}})
	if err != nil || calls.Load() != 1 || !strings.Contains(textOutput(r), "19.99") {
		t.Fatalf("valid decimals %+v %v", r, err)
	}
	tool.Invoke = func(context.Context, HostCall) (json.RawMessage, error) {
		return json.RawMessage(`{"id":9007199254740993}`), nil
	}
	r, err = s.Run(context.Background(), `await tools.write({})`, RunOptions{Tools: []Tool{tool}})
	var script *ScriptError
	if !errors.As(err, &script) || script.Code != "result_rejected" || len(r.Calls) != 1 || r.Calls[0].Execution.Local != "returned" {
		t.Fatalf("post execution precision %+v %v", r, err)
	}
	tool.Parameters = json.RawMessage(`{"const":9007199254740993}`)
	if _, err = s.Run(context.Background(), `return 1`, RunOptions{Tools: []Tool{tool}}); err == nil {
		t.Fatal("unsafe schema was accepted")
	}
}

func TestStackHeapAndCancellation(t *testing.T) {
	for _, stack := range []uint64{64 << 10, 512 << 10} {
		c := DefaultConfig()
		c.StackBytes = stack
		s := sandboxForTest(t, c)
		r, err := s.Run(context.Background(), `try {(function recurse(){return recurse()})()} catch(e) {return {name:e.name,message:e.message,alive:1+2}}`, RunOptions{})
		if err != nil || !strings.Contains(textOutput(r), `"name":"RangeError"`) || !strings.Contains(textOutput(r), `"alive":3`) {
			t.Fatalf("stack %d %+v %v", stack, r, err)
		}
	}
	c := DefaultConfig()
	c.HeapBytes = 8 << 20
	s := sandboxForTest(t, c)
	r, err := s.Run(context.Background(), `try {let x=[];for(let i=0;i<1000000;i++)x.push({i,s:"abc"});return "unexpected"}catch(e){return String(e)}`, RunOptions{})
	if err != nil || textOutput(r) == "unexpected" {
		t.Fatalf("heap %+v %v", r, err)
	}
	for _, code := range []string{`while(true){}`, `while(true)await null`} {
		start := time.Now()
		_, err = s.Run(context.Background(), code, RunOptions{Timeout: 30 * time.Millisecond})
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
			t.Fatalf("CPU/microtask cancellation %v elapsed=%s", err, time.Since(start))
		}
	}
	r, err = s.Run(context.Background(), `return 3`, RunOptions{})
	if err != nil || textOutput(r) != "3" {
		t.Fatalf("cancellation poisoned other instances %+v %v", r, err)
	}
}

func TestOutputOptionsSyntaxAndPendingPromises(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	for _, code := range []string{`// @options: {"unknown":1}`, `// @options: {"timeout_ms":999999999}`, `// @options: {"max_output_tokens":0}`} {
		if _, err := s.Run(context.Background(), code, RunOptions{}); err == nil {
			t.Fatalf("invalid option %s", code)
		}
	}
	r, err := s.Run(context.Background(), "// @options: {\"max_output_tokens\": 2}\ntext('12345678');return 'x'", RunOptions{})
	if err == nil || textOutput(r) != "12345678" {
		t.Fatalf("combined return budget %+v %v", r, err)
	}
	_, err = s.Run(context.Background(), "// @options: {\"timeout_ms\": 1000}\nconst a=1;\nconst =;", RunOptions{})
	if err == nil || !strings.Contains(err.Error(), "codemode.js:3") {
		t.Fatalf("source line number %v", err)
	}
	_, err = s.Run(context.Background(), `await new Promise(()=>{})`, RunOptions{})
	var script *ScriptError
	if !errors.As(err, &script) || script.Code != "pending_promise" {
		t.Fatalf("pending promise %v", err)
	}
	_, err = s.Run(context.Background(), `Promise.reject(new Error('forgotten'));return 1`, RunOptions{})
	if err == nil || !strings.Contains(err.Error(), "forgotten") {
		t.Fatalf("unhandled promise %v", err)
	}
	r, err = s.Run(context.Background(), `text('before');exit();text('after');return 4`, RunOptions{})
	if err != nil || textOutput(r) != "before" {
		t.Fatalf("exit %+v %v", r, err)
	}
}

func TestPreVMFailuresHonorSourceOutputLimit(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	code := "// @options: {\"max_output_tokens\":1}\ntext('must not run');"
	for _, name := range []string{"catalog", "namespace", "canceled", "closed"} {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			options := RunOptions{MaxOutputTokens: 100, OutputReserveBytes: 1024}
			switch name {
			case "catalog":
				options.Tools = []Tool{{Name: "missing_executor"}}
			case "namespace":
				options.Namespaces = []Namespace{{Name: "unavailable"}}
			case "canceled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "closed":
				if err := s.Close(ctx); err != nil {
					t.Fatal(err)
				}
			}
			result, err := s.Run(ctx, code, options)
			if err == nil || len(result.Outputs) != 0 || len(result.Calls) != 0 || result.OutputLimitBytes != 4 || result.OutputReservedBytes != 1 || result.OutputLimitItems != s.config.MaxOutputItems {
				t.Fatalf("pre-VM failure widened source cap: result=%+v err=%v", result, err)
			}
		})
	}
}

func TestHostPermitHeldUntilActualExitAndCloseReport(t *testing.T) {
	c := DefaultConfig()
	c.MaxConcurrentCalls = 1
	s, err := NewSandbox(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	releaseHost := sync.OnceFunc(func() { close(release) })
	t.Cleanup(func() {
		releaseHost()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Errorf("cleanup: %v", err)
		}
	})
	guard, stopGuard := context.WithTimeout(t.Context(), 30*time.Second)
	defer stopGuard()
	var second atomic.Bool
	tool := Tool{Name: "blocked", Invoke: func(context.Context, HostCall) (json.RawMessage, error) {
		close(entered)
		<-release
		return json.RawMessage(`true`), nil
	}}
	firstCtx, cancelFirst := context.WithCancel(guard)
	defer cancelFirst()
	done := make(chan error, 1)
	go func() {
		_, e := s.Run(firstCtx, `await tools.blocked({})`, RunOptions{Tools: []Tool{tool}})
		done <- e
	}()
	select {
	case <-entered:
	case e := <-done:
		t.Fatalf("script finished before host entry: %v", e)
	case <-guard.Done():
		t.Fatalf("waiting for host entry: %v", guard.Err())
	}
	cancelFirst()
	select {
	case e := <-done:
		if !errors.Is(e, context.Canceled) {
			t.Fatalf("cancel: %v", e)
		}
	case <-guard.Done():
		t.Fatalf("waiting for VM cancellation: %v", guard.Err())
	}
	if len(s.callSlots) != 1 {
		t.Fatal("VM cancellation released the active host permit")
	}
	tool.Invoke = func(context.Context, HostCall) (json.RawMessage, error) {
		second.Store(true)
		return json.RawMessage(`true`), nil
	}
	secondCtx, cancelSecond := context.WithCancel(guard)
	defer cancelSecond()
	secondDone := make(chan error, 1)
	go func() {
		_, e := s.Run(secondCtx, `await tools.blocked({})`, RunOptions{Tools: []Tool{tool}})
		secondDone <- e
	}()
	// Observe actual admission instead of assuming startup fits a short timer.
	// The first callback remains blocked, so two active calls include one waiter.
	for {
		s.mu.Lock()
		active, changed := len(s.active), s.changed
		s.mu.Unlock()
		if active == 2 {
			break
		}
		select {
		case <-changed:
		case e := <-secondDone:
			t.Fatalf("second script finished before queued admission: %v", e)
		case <-guard.Done():
			t.Fatalf("waiting for queued admission: %v", guard.Err())
		}
	}
	cancelSecond()
	select {
	case e := <-secondDone:
		if !errors.Is(e, context.Canceled) || second.Load() {
			t.Fatalf("released occupied host permit: %v", e)
		}
	case <-guard.Done():
		t.Fatalf("waiting for queued cancellation: %v", guard.Err())
	}
	// Close must report the still-entered callback after the queued call retires.
	for {
		s.mu.Lock()
		active, changed := len(s.active), s.changed
		s.mu.Unlock()
		if active == 1 {
			break
		}
		select {
		case <-changed:
		case <-guard.Done():
			t.Fatalf("waiting for queued host exit: %v", guard.Err())
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err = s.Close(ctx)
	var closeErr *CloseError
	if !errors.As(err, &closeErr) || len(closeErr.Outstanding) != 1 || closeErr.Outstanding[0].Execution.Local != "entered" {
		t.Fatalf("close report %v", err)
	}
	releaseHost()
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if err = s.Close(ctx2); err != nil {
		t.Fatal(err)
	}
}

func TestStartupDeadlineCanFinishBeforeHostEntry(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	var calls atomic.Int32
	tool := Tool{Name: "blocked", Invoke: func(context.Context, HostCall) (json.RawMessage, error) {
		calls.Add(1)
		return json.RawMessage(`true`), nil
	}}
	ctx, cancel := context.WithDeadline(t.Context(), time.Unix(0, 0))
	defer cancel()
	result, err := s.Run(ctx, `await tools.blocked({})`, RunOptions{Tools: []Tool{tool}})
	if !errors.Is(err, context.DeadlineExceeded) || calls.Load() != 0 || len(result.Calls) != 0 {
		t.Fatalf("startup deadline must not require host entry: calls=%d result=%+v err=%v", calls.Load(), result, err)
	}
}

func TestConcurrentRunsHaveIndependentVMState(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Go(func() {
			r, err := s.Run(context.Background(), `store('key',1);globalThis.privateValue=4;return load('key')`, RunOptions{})
			if err != nil || textOutput(r) != "1" {
				t.Errorf("parallel Run %+v %v", r, err)
			}
		})
	}
	wg.Wait()
	r, err := s.Run(context.Background(), `return typeof privateValue`, RunOptions{})
	if err != nil || textOutput(r) != "undefined" {
		t.Fatalf("instance leaked %+v %v", r, err)
	}
}

func TestJSONAndConfigBounds(t *testing.T) {
	for _, raw := range []string{`0.1`, `19.99`, `9007199254740991`, `"9007199254740993"`, `{"nul":"a\u0000b"}`} {
		if err := ValidateJSON([]byte(raw)); err != nil {
			t.Errorf("valid %s: %v", raw, err)
		}
	}
	for _, raw := range []string{`9007199254740992`, `1e999999`, `1e-999999`, `0.1234567890123456789`, `[1] [2]`} {
		if err := ValidateJSON([]byte(raw)); err == nil {
			t.Errorf("unsafe %s", raw)
		}
	}
	c := DefaultConfig()
	c.StackBytes = 0
	if _, err := NewSandbox(context.Background(), c); err == nil {
		t.Fatal("stack protection disabled")
	}
}

func TestAggregateCompletionQuotaRejectsWithoutHanging(t *testing.T) {
	c := DefaultConfig()
	c.MaxCompletionBytes = 64
	s := sandboxForTest(t, c)
	tool := Tool{Name: "large", Invoke: func(context.Context, HostCall) (json.RawMessage, error) {
		return json.RawMessage(`"` + strings.Repeat("x", 128) + `"`), nil
	}}
	r, err := s.Run(context.Background(), `try {await tools.large({})}catch(e){return e.code}`, RunOptions{Tools: []Tool{tool}, Timeout: time.Second})
	if err != nil || textOutput(r) != "result_rejected" || len(r.Calls) != 1 {
		t.Fatalf("quota settlement %+v %v", r, err)
	}
}

func TestIntrinsicJSONMutationCannotHideUnsupportedNumbers(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	var sent atomic.Int32
	tool := Tool{Name: "echo", Invoke: func(ctx context.Context, c HostCall) (json.RawMessage, error) { sent.Add(1); return c.Arguments, nil }}
	r, err := s.Run(context.Background(), `Array.prototype.toJSON=()=>[NaN];return await tools.echo({values:[0.1,19.99]})`, RunOptions{Tools: []Tool{tool}})
	if err != nil || sent.Load() != 1 || textOutput(r) != `{"values":[0.1,19.99]}` {
		t.Fatalf("prototype altered JSON %+v %v", r, err)
	}
}

func TestPreDispatchFailureKeepsCodeAfterScriptSuccess(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	tool := Tool{Name: "denied", Invoke: func(context.Context, HostCall) (json.RawMessage, error) {
		return nil, &CallError{Code: "policy_denied", Message: "denied", Execution: Execution{Local: "not_started", Remote: "not_dispatched"}}
	}}
	r, err := s.Run(context.Background(), `try{await tools.denied({})}catch(e){return e.code}`, RunOptions{Tools: []Tool{tool}})
	if err != nil || len(r.Calls) != 1 || r.Calls[0].Code != "policy_denied" || r.Calls[0].Execution.Local != "not_started" {
		t.Fatalf("failure classification lost %+v %v", r, err)
	}
}

func TestLinearMemoryAndImageBounds(t *testing.T) {
	c := DefaultConfig()
	c.MemoryLimitPages = 64
	c.HeapBytes = 2 << 20
	s := sandboxForTest(t, c)
	m, err := s.runtime.InstantiateModule(context.Background(), s.compiled, wazero.NewModuleConfig().WithName("memory-cap-test").WithStartFunctions("_initialize"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close(context.Background())
	current, ok := m.Memory().Grow(0)
	if !ok {
		t.Fatal("cannot query memory pages")
	}
	if _, ok := m.Memory().Grow(64 - current); !ok {
		t.Fatal("cannot grow to configured page limit")
	}
	if _, ok := m.Memory().Grow(1); ok {
		t.Fatal("linear memory exceeded configured pages")
	}
	var pngBytes bytes.Buffer
	if err := png.Encode(&pngBytes, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	data := base64.StdEncoding.EncodeToString(pngBytes.Bytes())
	r, err := s.Run(context.Background(), `image({mimeType:"image/png",data:"`+data+`"});try{image({mimeType:"image/jpeg",data:"`+data+`"})}catch(e){text(e.code)};return true`, RunOptions{})
	if err != nil || len(r.Outputs) != 3 || r.Outputs[0].Type != "image" || !strings.Contains(textOutput(r), "image_invalid") {
		t.Fatalf("image validation %+v %v", r, err)
	}
}
