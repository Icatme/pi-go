package prebuilt

import (
	"context"
	"errors"
	core "github.com/Icatme/pi-go/agent"
	"testing"
)

func TestReflectionPreservesRuntimeCause(t *testing.T) {
	for _, phase := range []string{"generation", "evaluation"} {
		t.Run(phase, func(t *testing.T) {
			cause := &terminalStatusError{502}
			model := core.StreamFunc(func(context.Context, core.ModelRequest) (core.AssistantStream, error) { return nil, cause })
			var err error
			if phase == "generation" {
				evaluatorCalls := 0
				a, e := CreateReflectionAgent(ReflectionAgentConfig{Model: model, Evaluator: ReflectionEvaluatorFunc(func(context.Context, ReflectionInput) (ReflectionEvaluation, error) {
					evaluatorCalls++
					return ReflectionEvaluation{Verdict: ReflectionVerdictAccept}, nil
				})})
				if e != nil {
					t.Fatal(e)
				}
				result, e := a.PromptText(t.Context(), "request")
				err = e
				if evaluatorCalls != 0 || result.StopReason != "" || len(result.Steps) != 0 {
					t.Fatalf("failed draft evaluated or accepted: %+v calls=%d", result, evaluatorCalls)
				}
			} else {
				a, e := NewModelReflectionEvaluator(ModelReflectionEvaluatorConfig{Model: model})
				if e != nil {
					t.Fatal(e)
				}
				_, err = a.Evaluate(t.Context(), ReflectionInput{Request: []core.Message{core.NewUserTextMessage("request")}, Draft: core.NewTextMessage(core.RoleAssistant, "draft"), Iteration: 1})
			}
			if !errors.Is(err, cause) {
				t.Fatalf("lost runtime cause: %v", err)
			}
			var typed *terminalStatusError
			if !errors.As(err, &typed) || typed != cause {
				t.Fatalf("lost typed cause: %v", err)
			}
		})
	}
}

func TestReflectionTerminalEventAndCleanupCauses(t *testing.T) {
	eventErr := errors.New("event failure")
	waitErr := errors.New("wait failure")
	closeErr := errors.New("close failure")
	model := terminalModel{events: []core.AssistantEvent{{Type: core.AssistantEventError, Err: eventErr}}, final: core.Message{Role: core.RoleAssistant, Parts: []core.Part{core.NewTextPart(`{"verdict":"accept","summary":"ok","revision_instructions":[]}`)}, StopReason: core.StopReasonStop}, waitErr: waitErr, closeErr: closeErr}
	evaluator, err := NewModelReflectionEvaluator(ModelReflectionEvaluatorConfig{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	result, err := evaluator.Evaluate(t.Context(), ReflectionInput{Request: []core.Message{core.NewUserTextMessage("request")}, Draft: core.NewTextMessage(core.RoleAssistant, "draft"), Iteration: 1})
	for _, cause := range []error{eventErr, waitErr, closeErr} {
		if !errors.Is(err, cause) {
			t.Errorf("lost %v: %v", cause, err)
		}
	}
	if result.Verdict != "" {
		t.Fatalf("false acceptance: %+v", result)
	}
}

func TestReflectionImportedFailureIsNotCurrentFailure(t *testing.T) {
	old := core.Message{Role: core.RoleAssistant, StopReason: core.StopReasonError, ErrorMessage: "historical failure"}
	a, err := CreateReflectionAgent(ReflectionAgentConfig{Model: reflectionScriptedModel("valid draft"), Evaluator: ReflectionEvaluatorFunc(func(context.Context, ReflectionInput) (ReflectionEvaluation, error) {
		return ReflectionEvaluation{Verdict: ReflectionVerdictAccept, Summary: "ok"}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	result, err := a.Run(t.Context(), []core.Message{core.NewUserTextMessage("old"), old, core.NewUserTextMessage("new")})
	if err != nil || result.StopReason != ReflectionStopReasonAccepted {
		t.Fatalf("historical failure contaminated run: %+v %v", result, err)
	}
}

func TestReflectionEvaluatorCancellationCause(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	evaluator, err := NewModelReflectionEvaluator(ModelReflectionEvaluatorConfig{Model: terminalModel{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = evaluator.Evaluate(ctx, ReflectionInput{Request: []core.Message{core.NewUserTextMessage("request")}, Draft: core.NewTextMessage(core.RoleAssistant, "draft"), Iteration: 1})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
}

func TestReflectionWhitespaceErrorMarkerIsFailure(t *testing.T) {
	model := terminalModel{final: core.Message{Role: core.RoleAssistant, StopReason: core.StopReasonStop, ErrorMessage: " ", Parts: []core.Part{core.NewTextPart(`{"verdict":"accept","summary":"ok","revision_instructions":[]}`)}}}
	evaluator, err := NewModelReflectionEvaluator(ModelReflectionEvaluatorConfig{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	result, err := evaluator.Evaluate(t.Context(), ReflectionInput{Request: []core.Message{core.NewUserTextMessage("request")}, Draft: core.NewTextMessage(core.RoleAssistant, "draft"), Iteration: 1})
	if err == nil || result.Verdict != "" {
		t.Fatalf("failed terminal accepted: %+v %v", result, err)
	}
}
