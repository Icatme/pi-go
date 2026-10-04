package agent

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
)

func TestToolFinalizationRequiresRawAndEffectiveSuccessForState(t *testing.T) {
	for _, test := range []struct {
		name           string
		rawError       bool
		effectiveError bool
		wantState      bool
	}{
		{name: "success", wantState: true},
		{name: "hook clears raw business error", rawError: true},
		{name: "hook introduces business error", effectiveError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var saved JournalRecord
			records, hooks := 0, 0
			ctx := withJournal(t.Context(), fixtureJournal{record: func(_ context.Context, record JournalRecord) error {
				records++
				saved = record
				return nil
			}})
			tool := ToolDefinition{Name: "stateful", ExecutionMode: ToolExecutionSequential,
				Execute: func(_ context.Context, input ToolExecutionContext) (ToolResult, error) {
					if input.State == nil {
						t.Fatal("sequential journaled tool received no state capability")
					}
					if err := input.State.Stage("binding", json.RawMessage(`{"n":1}`)); err != nil {
						t.Fatal(err)
					}
					return ToolResult{IsError: test.rawError}, nil
				},
			}
			outcome := RunToolCall(ctx, ToolCall{ID: "call", Name: tool.Name}, RunToolCallOptions{
				Tools: []ToolDefinition{tool},
				AfterToolCall: func(_ context.Context, input AfterToolCallContext) (AfterToolCallResult, error) {
					hooks++
					if input.Err != nil || input.IsError != test.rawError {
						t.Fatalf("unexpected raw outcome: %+v", input)
					}
					return AfterToolCallResult{IsError: &test.effectiveError}, nil
				},
			})
			if hooks != 1 || outcome.Err != nil || outcome.IsError != test.effectiveError || outcome.Result.IsError != test.effectiveError {
				t.Fatalf("hooks=%d effective outcome=%+v", hooks, outcome)
			}
			var wantFailure *ToolFailure
			if test.effectiveError {
				wantFailure = &ToolFailure{Code: ToolFailureToolReportedError}
			}
			if !reflect.DeepEqual(outcome.Failure, wantFailure) {
				t.Fatalf("failure=%+v, want %+v", outcome.Failure, wantFailure)
			}
			if err := recordJournal(ctx, nil, false); err != nil {
				t.Fatal(err)
			}
			var wantState []ToolStateChange
			if test.wantState {
				wantState = []ToolStateChange{{Tool: tool.Name, Namespace: "binding", Value: json.RawMessage(`{"n":1}`)}}
			}
			if records != 1 || !reflect.DeepEqual(saved.State, wantState) {
				t.Fatalf("records=%d state=%+v, want %+v", records, saved.State, wantState)
			}
		})
	}
}
