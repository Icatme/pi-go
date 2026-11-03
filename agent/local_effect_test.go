package agent

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type effectJournal struct{}

func (effectJournal) Record(context.Context, JournalRecord) error       { return nil }
func (effectJournal) LoadState(string, string) (json.RawMessage, error) { return nil, nil }

func TestLocalCommitSurvivesPresentationFailureAndCanceledFinalization(t *testing.T) {
	for _, mode := range []string{"hook error", "hook panic", "result rejection", "cancel finalization", "hook tries to erase"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(withJournal(t.Context(), effectJournal{}))
			defer cancel()
			file := filepath.Join(t.TempDir(), "created")
			calls := 0
			tool := ToolDefinition{Name: "create", ExecutionMode: ToolExecutionSequential, Execute: func(_ context.Context, in ToolExecutionContext) (ToolResult, error) {
				calls++
				if in.State == nil {
					t.Fatal("missing state fixture")
				}
				if err := os.WriteFile(file, []byte("complete"), 0600); err != nil {
					t.Fatal(err)
				}
				if mode == "cancel finalization" {
					cancel()
				}
				return ToolResult{StructuredContent: json.RawMessage(`{"published":true}`), Execution: &ToolExecutionInfo{LocalEffect: ToolLocalEffectCommittedReported}}, nil
			}}
			if mode == "result rejection" {
				tool.ValidateResult = func(ToolResult) error { return errors.New("rejected output") }
			}
			out := RunToolCall(ctx, ToolCall{Name: "create"}, RunToolCallOptions{Tools: []ToolDefinition{tool}, AfterToolCall: func(_ context.Context, in AfterToolCallContext) (AfterToolCallResult, error) {
				if in.Execution.LocalEffect != ToolLocalEffectCommittedReported {
					t.Fatal("hook did not see fact")
				}
				switch mode {
				case "hook error":
					return AfterToolCallResult{}, errors.New("withheld")
				case "hook panic":
					panic("private panic")
				case "hook tries to erase":
					return AfterToolCallResult{Result: &ToolResult{Execution: &ToolExecutionInfo{LocalEffect: ToolLocalEffectNoneReported}}}, nil
				}
				return AfterToolCallResult{}, nil
			}})
			data, err := os.ReadFile(file)
			if err != nil || string(data) != "complete" || calls != 1 {
				t.Fatalf("effect lost/replayed %d %v", calls, err)
			}
			if mode != "hook tries to erase" && out.Err == nil {
				t.Fatal("missing finalization error")
			}
			if out.Execution.LocalEffect != ToolLocalEffectCommittedReported || out.Result.Execution.LocalEffect != ToolLocalEffectCommittedReported {
				t.Fatalf("commit fact lost: %+v", out)
			}
			encoded, err := json.Marshal(NewToolResultMessage(out.ToolCall, out.Result, out.IsError))
			if err != nil || !strings.Contains(string(encoded), `"local_effect":"committed_reported"`) {
				t.Fatalf("transcript fact lost: %s %v", encoded, err)
			}
		})
	}
}
func TestLocalEffectOnlyErrorAndConflictingFacts(t *testing.T) {
	for _, tt := range []struct {
		name                  string
		result, failure, want ToolLocalEffect
	}{
		{"effect-only error", "", ToolLocalEffectCommittedReported, ToolLocalEffectCommittedReported},
		{"error omits effect", ToolLocalEffectCommittedReported, "", ToolLocalEffectCommittedReported},
		{"conflict preserves commit", ToolLocalEffectCommittedReported, ToolLocalEffectNoneReported, ToolLocalEffectCommittedReported},
		{"unknown dominates none", ToolLocalEffectNoneReported, ToolLocalEffectUnknown, ToolLocalEffectUnknown},
		{"invalid report", ToolLocalEffect("private invalid text"), "", ToolLocalEffectUnknown},
		{"legacy empty", "", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			out := RunToolCall(t.Context(), ToolCall{Name: "effect"}, RunToolCallOptions{Tools: []ToolDefinition{{Name: "effect", Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
				return ToolResult{Execution: &ToolExecutionInfo{LocalEffect: tt.result}}, &ToolExecutionError{Code: ToolFailureResource, Message: "failed", Execution: ToolExecutionInfo{LocalEffect: tt.failure}}
			}}}})
			if out.Err == nil || out.Execution.LocalEffect != tt.want {
				t.Fatalf("%+v", out)
			}
		})
	}
}
func TestUnreportedPanicDoesNotClaimNoLocalEffect(t *testing.T) {
	calls := 0
	out := RunToolCall(t.Context(), ToolCall{Name: "effect"}, RunToolCallOptions{Tools: []ToolDefinition{{Name: "effect", Execute: func(context.Context, ToolExecutionContext) (ToolResult, error) {
		calls++
		panic("after possible effect")
	}}}})
	if out.Err == nil || calls != 1 || out.Execution.LocalEffect == ToolLocalEffectNoneReported || out.Execution.LocalEffect == ToolLocalEffectCommittedReported {
		t.Fatalf("%+v", out)
	}
	raw, _ := json.Marshal(ToolExecutionInfo{Local: ToolLocalReturned, Remote: ToolRemoteNotApplicable})
	if strings.Contains(string(raw), "local_effect") {
		t.Fatal("legacy zero field changed encoding")
	}
}
