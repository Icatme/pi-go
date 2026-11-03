package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestExecutionSidecarSurvivesRejectedSuccess(t *testing.T) {
	for _, mode := range []string{"success", "invalid JSON", "result bytes", "completion bytes", "error omits effect", "conflicting error"} {
		t.Run(mode, func(t *testing.T) {
			config := DefaultConfig()
			if mode == "result bytes" {
				config.MaxResultBytes = 8
			}
			if mode == "completion bytes" {
				config.MaxCompletionBytes = 128
			}
			s := sandboxForTest(t, config)
			calls := 0
			tool := Tool{Name: "effect", InvokeWithExecution: func(context.Context, HostCall) (json.RawMessage, Execution, error) {
				calls++
				facts := Execution{LocalEffect: LocalEffectCommittedReported}
				switch mode {
				case "invalid JSON":
					return json.RawMessage(`{"broken":`), facts, nil
				case "result bytes", "completion bytes":
					return json.RawMessage(`"` + strings.Repeat("x", 200) + `"`), facts, nil
				case "error omits effect":
					return nil, facts, errors.New("host failed after commit")
				case "conflicting error":
					return nil, facts, &CallError{Code: "host", Message: "failed", Execution: Execution{LocalEffect: LocalEffectNoneReported}}
				}
				return json.RawMessage(`"ok"`), facts, nil
			}}
			script := `text(await tools.effect({}));`
			if mode != "success" {
				script = `try {await tools.effect({})} catch(e) {text(e.execution.local_effect)}`
			}
			result, err := s.Run(t.Context(), script, RunOptions{Tools: []Tool{tool}})
			if err != nil {
				t.Fatal(err)
			}
			if calls != 1 || len(result.Calls) != 1 || result.Calls[0].Execution.LocalEffect != LocalEffectCommittedReported {
				t.Fatalf("calls=%d result=%+v", calls, result)
			}
			if mode != "success" && (len(result.Outputs) != 1 || !strings.Contains(result.Outputs[0].Text, LocalEffectCommittedReported)) {
				t.Fatalf("JS lost fact %+v", result)
			}
		})
	}
}
func TestExecutionSidecarValidationAndLegacy(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	old := func(context.Context, HostCall) (json.RawMessage, error) { return json.RawMessage(`null`), nil }
	with := func(context.Context, HostCall) (json.RawMessage, Execution, error) {
		return json.RawMessage(`null`), Execution{LocalEffect: "invalid private value"}, nil
	}
	for _, tool := range []Tool{{Name: "x"}, {Name: "x", Invoke: old, InvokeWithExecution: with}} {
		if _, err := s.Run(t.Context(), `await tools.x({})`, RunOptions{Tools: []Tool{tool}}); err == nil {
			t.Fatal("ambiguous/missing invocation accepted")
		}
	}
	for _, tt := range []struct {
		tool Tool
		want string
	}{{Tool{Name: "x", Invoke: old}, ""}, {Tool{Name: "x", InvokeWithExecution: with}, LocalEffectUnknown}, {Tool{Name: "x", InvokeWithExecution: func(context.Context, HostCall) (json.RawMessage, Execution, error) { panic("possible commit") }}, ""}} {
		r, _ := s.Run(t.Context(), `try {await tools.x({})} catch(e) {}`, RunOptions{Tools: []Tool{tt.tool}})
		if len(r.Calls) != 1 || r.Calls[0].Execution.LocalEffect != tt.want {
			t.Fatalf("%+v", r)
		}
	}
}

func TestInvalidExecutionPresentationCannotEraseReportedCommit(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	for _, mode := range []string{"success sidecar", "error metadata"} {
		t.Run(mode, func(t *testing.T) {
			tool := Tool{Name: "x", InvokeWithExecution: func(context.Context, HostCall) (json.RawMessage, Execution, error) {
				facts := Execution{LocalEffect: LocalEffectCommittedReported, Local: strings.Repeat("x", 100), Remote: strings.Repeat("y", 100)}
				if mode == "error metadata" {
					return nil, Execution{LocalEffect: LocalEffectCommittedReported}, &CallError{Code: "host", Message: "failed", Execution: facts}
				}
				return json.RawMessage(`null`), facts, nil
			}}
			result, err := s.Run(t.Context(), `try {await tools.x({})} catch(e) {text(e.execution.local_effect)}`, RunOptions{Tools: []Tool{tool}})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Calls) != 1 || result.Calls[0].Execution.LocalEffect != LocalEffectCommittedReported || len(result.Calls[0].Execution.Local) > 32 || len(result.Calls[0].Execution.Remote) > 32 {
				t.Fatalf("%+v", result)
			}
		})
	}
}

func TestExecutionSidecarFillsOmittedErrorFacts(t *testing.T) {
	s := sandboxForTest(t, DefaultConfig())
	for _, mode := range []string{"ordinary error", "typed error"} {
		t.Run(mode, func(t *testing.T) {
			tool := Tool{Name: "x", InvokeWithExecution: func(context.Context, HostCall) (json.RawMessage, Execution, error) {
				facts := Execution{Local: "returned", Remote: "complete_reported", LocalEffect: LocalEffectCommittedReported}
				var err error = errors.New("mapping failed")
				if mode == "typed error" {
					err = &CallError{Code: "mapping", Message: "mapping failed"}
				}
				return nil, facts, err
			}}
			result, err := s.Run(t.Context(), `try {await tools.x({})} catch(e) {text(e.execution.remote)}`, RunOptions{Tools: []Tool{tool}})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Calls) != 1 || result.Calls[0].Execution.Remote != "complete_reported" || result.Calls[0].Execution.LocalEffect != LocalEffectCommittedReported || len(result.Outputs) != 1 || !strings.Contains(result.Outputs[0].Text, "complete_reported") {
				t.Fatalf("%+v", result)
			}
		})
	}
}
