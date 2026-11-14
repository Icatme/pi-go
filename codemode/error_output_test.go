package codemode

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestNestedErrorsKeepSafeOutputDiagnostics(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	tool := Tool{Name: "fail", Invoke: func(context.Context, HostCall) (json.RawMessage, error) {
		return nil, &CallError{Code: "policy_denied", Message: "Actionable public failure"}
	}}
	for _, output := range []string{"text(results)", "console.log(results)", "return results"} {
		r, err := s.Run(t.Context(), `const results=await Promise.allSettled([tools.fail({}),Promise.resolve(3)]);`+output, RunOptions{Tools: []Tool{tool}})
		text := textOutput(r)
		if err != nil || !strings.Contains(text, `"message":"Actionable public failure"`) || !strings.Contains(text, `"name":"Error"`) || !strings.Contains(text, `"value":3`) || strings.Contains(text, `"stack"`) {
			t.Fatalf("output=%s text=%s err=%v", output, text, err)
		}
	}
}

func TestNativeErrorOutputDoesNotLoosenToolOrStoreJSON(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	r, err := s.Run(t.Context(), `const e=new TypeError("bad input");e.secret="not output";text({nested:[e]});text(JSON.stringify(e));try{store("err",e)}catch(x){text(x.code)};return {ok:true}`, RunOptions{})
	text := textOutput(r)
	if err != nil || !strings.Contains(text, `"name":"TypeError","message":"bad input"`) || !strings.Contains(text, "unsupported_json") {
		t.Fatalf("text=%s err=%v", text, err)
	}
	if len(r.Outputs) == 0 || strings.Contains(r.Outputs[0].Text, "secret") || strings.Contains(r.Outputs[0].Text, "stack") {
		t.Fatal("diagnostic copied extra properties")
	}
	// Explicit JSON.stringify retains ordinary JavaScript enumerable-property semantics.
	if !strings.Contains(text, `{"secret":"not output"}`) {
		t.Fatalf("JSON.stringify was changed: %s", text)
	}
	r, err = s.Run(t.Context(), `let hits=0;const e=new Error("initial");Object.defineProperty(e,"message",{get(){hits++;return "hidden"}});try{text(e)}catch(x){return x.code+":"+hits}`, RunOptions{})
	if err != nil || textOutput(r) != "unsupported_json:0" {
		t.Fatalf("accessor ran: %+v %v", r, err)
	}
}

func TestErrorOutputDoesNotAuthorizeErrorArguments(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	calls := 0
	tool := Tool{Name: "echo", Invoke: func(context.Context, HostCall) (json.RawMessage, error) { calls++; return json.RawMessage(`null`), nil }}
	r, err := s.Run(t.Context(), `try {await tools.echo({error:new Error("private")})}catch(e){return e.code}`, RunOptions{Tools: []Tool{tool}})
	if err != nil || calls != 0 || len(r.Calls) != 0 || textOutput(r) != "unsupported_json" {
		t.Fatalf("invalid arguments dispatched: %d %+v %v", calls, r, err)
	}
}
