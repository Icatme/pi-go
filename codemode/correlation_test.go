package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/Icatme/pi-go/executionreceipt"
)

func TestTypedNilHostErrorPreservesFailureAndReceipt(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	var cause *CallError
	tool := Tool{Name: "write", Invoke: func(ctx context.Context, _ HostCall) (json.RawMessage, error) {
		executionreceipt.ReportLocalCommit(ctx)
		return nil, cause
	}}
	r, h, err := s.RunWithReceipts(t.Context(), `await tools.write({})`, RunOptions{Tools: []Tool{tool}}, 1)
	if !errors.Is(err, cause) || len(r.Calls) != 1 || r.Calls[0].Code != "host" {
		t.Fatalf("calls=%+v err=%v", r.Calls, err)
	}
	if records := h.Snapshot(); len(records) != 1 || !records[0].Committed() || !records[0].Completed() {
		t.Fatalf("receipts=%+v", records)
	}
}

func TestOversizedHostExecutionFieldsAreBoundedBeforePublication(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	oversized := strings.Repeat("x", 33)
	for _, tt := range []struct {
		name           string
		reported, want Execution
	}{
		{"local", Execution{oversized, "complete_reported"}, Execution{"returned", "complete_reported"}},
		{"remote", Execution{"not_started", oversized}, Execution{"not_started", "unknown"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			tool := Tool{Name: "bad", Invoke: func(context.Context, HostCall) (json.RawMessage, error) {
				return nil, &CallError{Code: "host", Message: "failure", Execution: tt.reported}
			}}
			result, err := s.Run(t.Context(), `try {await tools.bad({})} catch (e) {text(e.execution.local + "/" + e.execution.remote)}`, RunOptions{Tools: []Tool{tool}})
			if err != nil {
				t.Fatal(err)
			}
			if got := textOutput(result); got != tt.want.Local+"/"+tt.want.Remote {
				t.Errorf("script received %q, want %+v", got, tt.want)
			}
			if len(result.Calls) != 1 || result.Calls[0].Execution != tt.want {
				t.Errorf("host records %+v, want %+v", result.Calls, tt.want)
			}
		})
	}
}

func TestHostFailureCorrelationUsesErrorIdentity(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	original := errors.New("original transport failure")
	tool := Tool{Name: "bad", Invoke: func(context.Context, HostCall) (json.RawMessage, error) {
		return nil, &CallError{Code: "transport", Message: "safe host message", CallID: "parent/1", Execution: Execution{Local: "returned", Remote: "unknown"}, Err: original}
	}}
	for _, test := range []struct {
		name, code string
		retains    bool
	}{
		{"original", `try {await tools.bad({})} catch(e) {throw e}`, true},
		{"original_modified_public_id", `try {await tools.bad({})} catch(e) {e.callId="changed";throw e}`, true},
		{"replacement_public_id", `try {await tools.bad({})} catch(e) {const replacement=new Error("replacement");replacement.callId=e.callId;throw replacement}`, false},
		{"copied_fields", `try {await tools.bad({})} catch(e) {throw Object.assign(new Error("replacement"),e)}`, false},
		{"inherited_fields", `try {await tools.bad({})} catch(e) {throw Object.create(e)}`, false},
		{"proxy", `try {await tools.bad({})} catch(e) {throw new Proxy(e,{})}`, false},
		{"error_cause", `try {await tools.bad({})} catch(e) {throw new Error("replacement",{cause:e})}`, false},
		{"original_after_intrinsic_mutation", `WeakMap.prototype.get=()=>undefined;WeakMap.prototype.set=()=>{};try {await tools.bad({})} catch(e) {throw e}`, true},
		{"replacement_after_intrinsic_mutation", `WeakMap.prototype.get=()=>1;try {await tools.bad({})} catch(e) {const replacement=new Error("replacement");replacement.callId=e.callId;throw replacement}`, false},
		{"replacement_after_json_prototype_mutation", `try {await tools.bad({})} catch(e) {} Object.prototype.toJSON=()=>({message:"replacement",code:"script",callId:"parent/1",causeId:1});throw new Error("replacement")`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			result, err := s.Run(context.Background(), test.code, RunOptions{Tools: []Tool{tool}})
			var host *CallError
			if err == nil || errors.Is(err, original) != test.retains || errors.As(err, &host) != test.retains {
				t.Fatalf("original cause retained=%t, want=%t; result=%+v err=%v", errors.Is(err, original), test.retains, result, err)
			}
			if len(result.Calls) != 1 || result.Calls[0].Code != "transport" || result.Calls[0].Execution != (Execution{Local: "returned", Remote: "unknown"}) {
				t.Fatalf("guest error changed host facts: %+v", result.Calls)
			}
		})
	}
}

func TestHostFailuresWithSharedPublicIDRetainTheirOwnCause(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	first, second := errors.New("first"), errors.New("second")
	tool := Tool{Name: "bad", Invoke: func(_ context.Context, call HostCall) (json.RawMessage, error) {
		cause := first
		if call.ID == 2 {
			cause = second
		}
		return nil, &CallError{Code: "host", Message: "host error", CallID: "shared", Err: cause}
	}}
	_, err := s.Run(context.Background(), `try {await tools.bad({})} catch(e) {} await tools.bad({})`, RunOptions{Tools: []Tool{tool}})
	if !errors.Is(err, second) || errors.Is(err, first) {
		t.Fatalf("shared public ID selected a different host cause: %v", err)
	}
}

func TestCaughtHostCancellationCannotCancelReplacementError(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tool := Tool{Name: "bad", Invoke: func(context.Context, HostCall) (json.RawMessage, error) {
		return nil, context.Canceled
	}}
	result, err := s.Run(ctx, `try {await tools.bad({})} catch(e) {const replacement=new Error("script failed");replacement.callId=e.callId;throw replacement}`, RunOptions{Tools: []Tool{tool}})
	var script *ScriptError
	if ctx.Err() != nil || !errors.As(err, &script) || script.Code != "script" || errors.Is(err, context.Canceled) {
		t.Fatalf("replacement inherited cancellation: context=%v result=%+v err=%v", ctx.Err(), result, err)
	}
	if len(result.Calls) != 1 || result.Calls[0].Code != "canceled" || result.Calls[0].Execution != (Execution{Local: "returned", Remote: "unknown"}) {
		t.Fatalf("canceled child facts were lost: %+v", result.Calls)
	}
}
