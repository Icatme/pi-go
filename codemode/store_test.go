package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestInvocationStoreCommitsOnlySuccessfulScripts(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	store, err := NewStore(1024)
	if err != nil {
		t.Fatal(err)
	}
	options := RunOptions{Store: store}
	if _, err := s.Run(context.Background(), `store('count',1);store('gone',2)`, options); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Run(context.Background(), `store('count',99);store('gone',undefined);throw Error('discard')`, options); err == nil {
		t.Fatal("script should fail")
	}
	r, err := s.Run(context.Background(), `return [load('count'),load('gone')]`, options)
	if err != nil || textOutput(r) != "[1,2]" {
		t.Fatalf("failed script committed %+v %v", r, err)
	}
	if _, err := s.Run(context.Background(), `store('gone',undefined);store('count',3)`, options); err != nil {
		t.Fatal(err)
	}
	r, err = s.Run(context.Background(), `return [load('count'),load('gone')===undefined]`, options)
	if err != nil || textOutput(r) != "[3,true]" {
		t.Fatalf("successful delete/commit %+v %v", r, err)
	}
	options.Timeout = 20 * time.Millisecond
	if _, err := s.Run(context.Background(), `store('count',123);while(true){}`, options); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline %v", err)
	}
	options.Timeout = 0
	r, err = s.Run(context.Background(), `return load('count')`, options)
	if err != nil || textOutput(r) != "3" {
		t.Fatalf("canceled script committed %+v %v", r, err)
	}
}

func TestStoreAtomicRevisionConflictDoesNotReplayTools(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	store, _ := NewStore(1024)
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	tool := Tool{Name: "pause", Invoke: func(context.Context, HostCall) (json.RawMessage, error) {
		ready <- struct{}{}
		<-release
		return json.RawMessage(`true`), nil
	}}
	errorsOut := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := s.Run(context.Background(), `store('key',1);await tools.pause({})`, RunOptions{Store: store, Tools: []Tool{tool}})
			errorsOut <- err
		}()
	}
	<-ready
	<-ready
	close(release)
	a, b := <-errorsOut, <-errorsOut
	if (a == nil) == (b == nil) {
		t.Fatalf("expected exactly one commit: %v %v", a, b)
	}
	if a == nil {
		a = b
	}
	var conflict *ScriptError
	if !errors.As(a, &conflict) || conflict.Code != "store_conflict" {
		t.Fatalf("conflict %v", a)
	}
	select {
	case <-ready:
		t.Fatal("tool replayed")
	default:
	}
}

func TestTimeoutHasNoImplicitDeadlineAndTakesEarliestBound(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	tool := Tool{Name: "deadline", Invoke: func(ctx context.Context, _ HostCall) (json.RawMessage, error) {
		deadline, ok := ctx.Deadline()
		if !ok {
			return json.RawMessage(`false`), nil
		}
		return json.Marshal(time.Until(deadline).Milliseconds())
	}}
	r, err := s.Run(context.Background(), `return await tools.deadline({})`, RunOptions{Tools: []Tool{tool}})
	if err != nil || textOutput(r) != "false" {
		t.Fatalf("hidden deadline %+v %v", r, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r, err = s.Run(ctx, "// @options: {\"timeout_ms\": 500}\nreturn await tools.deadline({})", RunOptions{Tools: []Tool{tool}})
	if err != nil {
		t.Fatal(err)
	}
	var ms int
	if err := json.Unmarshal([]byte(textOutput(r)), &ms); err != nil || ms < 0 || ms > 500 {
		t.Fatalf("earliest source deadline %+v", r)
	}
	if _, err := s.Run(ctx, "// @options: {\"timeout_ms\": 1000}\nreturn 1", RunOptions{Timeout: 100 * time.Millisecond}); err == nil {
		t.Fatal("source expanded explicit host timeout")
	}
}

func TestStoreUTF16ValueUnitsSeparateFromUTF8Quota(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	for _, tc := range []struct{ value, want string }{
		{`'x'.repeat(262142)`, `true`}, // quotes add two units
		{`'x'.repeat(262143)`, `store_limit`},
		{`'😀'.repeat(131071)`, `true`}, // non-BMP characters use two UTF-16 units
		{`'😀'.repeat(131072)`, `store_limit`},
	} {
		r, err := s.Run(context.Background(), `try {store('value',`+tc.value+`);return true}catch(e){return e.code}`, RunOptions{})
		if err != nil || textOutput(r) != tc.want {
			t.Fatalf("store units %s: %+v %v", tc.value, r, err)
		}
	}
}

func TestStoreRestoreRejectsInvalidAndBoundedData(t *testing.T) {
	for _, raw := range []string{`null`, `[]`, `{"version":2,"values":{}}`, `{"version":1,"values":{"":"1"}}`, `{"version":1,"values":{"x":"1","x":"2"}}`, `{"version":1,"values":{"x":"9007199254740993"}}`, `{"version":1,"values":{"x":"\"\\ud800\""}}`, `{"version":1,"values":{"x":1}}`} {
		if _, err := RestoreStore(1<<20, []byte(raw)); err == nil {
			t.Fatalf("invalid saved state accepted: %s", raw)
		}
	}
	values := make(map[string]string)
	for i := range 257 {
		values[fmt.Sprint(i)] = "0"
	}
	raw, _ := json.Marshal(struct {
		Version int               `json:"version"`
		Values  map[string]string `json:"values"`
	}{1, values})
	if _, err := RestoreStore(1<<20, raw); err == nil {
		t.Fatal("257 store keys restored")
	}
	if _, err := RestoreStore(4, []byte(`{"version":1,"values":{}}`)); err == nil {
		t.Fatal("oversized snapshot restored")
	}
	store, err := RestoreStore(1024, []byte(`{"version":1,"values":{"n":"2","obj":"{\"value\":\"😀\"}"}}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, err = store.Export()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreStore(1024, raw); err != nil {
		t.Fatal(err)
	}
	raw[0] = '!'
	if _, err := store.Export(); err != nil {
		t.Fatal("export shared storage")
	}
}

func TestStoreSnapshotEscapingPreservesPerValueLimits(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	store, _ := NewStore(1 << 20)
	if _, err := s.Run(t.Context(), `store("html","&".repeat(120000));store("unicode","\u2028".repeat(1000))`, RunOptions{Store: store}); err != nil {
		t.Fatal(err)
	}
	raw, err := store.Export()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreStore(1<<20, raw)
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.Run(t.Context(), `return load("html").length===120000 && load("unicode").length===1000`, RunOptions{Store: restored})
	if err != nil || textOutput(result) != "true" {
		t.Fatalf("escaped snapshot lost value limits: %+v %v", result, err)
	}
}
