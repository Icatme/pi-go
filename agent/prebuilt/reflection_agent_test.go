package prebuilt

import (
	"context"
	"errors"
	"strings"
	"testing"

	core "github.com/Icatme/pi-go/agent"
)

func TestCreateReflectionAgentValidatesConfig(t *testing.T) {
	t.Run("model is required", func(t *testing.T) {
		if _, err := CreateReflectionAgent(ReflectionAgentConfig{}); err == nil {
			t.Fatal("expected error when model is missing")
		}
	})

	t.Run("negative max iterations is rejected", func(t *testing.T) {
		_, err := CreateReflectionAgent(ReflectionAgentConfig{
			Model:         reflectionScriptedModel("draft"),
			MaxIterations: -1,
		})
		if err == nil {
			t.Fatal("expected negative max iterations to be rejected")
		}
	})
}

func TestReflectionAgentAcceptsFirstDraft(t *testing.T) {
	model := reflectionScriptedModel(
		"Initial response",
		`{"verdict":"accept","summary":"The response satisfies the request.","revision_instructions":[]}`,
	)

	agent, err := CreateReflectionAgent(ReflectionAgentConfig{
		Model:         model,
		MaxIterations: 3,
	})
	if err != nil {
		t.Fatalf("CreateReflectionAgent returned error: %v", err)
	}

	result, err := agent.PromptText(context.Background(), "Test")
	if err != nil {
		t.Fatalf("PromptText returned error: %v", err)
	}

	if result.StopReason != ReflectionStopReasonAccepted {
		t.Fatalf("expected accepted stop reason, got %q", result.StopReason)
	}
	if result.Iterations != 1 || len(result.Steps) != 1 {
		t.Fatalf("expected one complete reflection step, got iterations=%d steps=%d", result.Iterations, len(result.Steps))
	}
	if result.Draft != "Initial response" || reflectionMessageText(result.Steps[0].Draft) != result.Draft {
		t.Fatalf("unexpected final draft: result=%q step=%q", result.Draft, reflectionMessageText(result.Steps[0].Draft))
	}
	if result.Steps[0].Evaluation.Verdict != ReflectionVerdictAccept {
		t.Fatalf("unexpected verdict: %q", result.Steps[0].Evaluation.Verdict)
	}
	if len(result.Messages) != 2 {
		t.Fatalf("expected initial request plus final draft, got %d messages", len(result.Messages))
	}
	if len(model.requests) != 2 {
		t.Fatalf("expected one generation and one evaluation request, got %d", len(model.requests))
	}
}

func TestReflectionAgentRevisesThenAcceptsWithCompleteRequest(t *testing.T) {
	generator := reflectionScriptedModel("Draft one", "Draft two")
	critic := reflectionScriptedModel(
		`{"verdict":"revise","summary":"The answer omits the visual evidence.","revision_instructions":["Discuss the supplied image."]}`,
		`{"verdict":"accept","summary":"The revision now covers the full request.","revision_instructions":[]}`,
	)
	evaluator := mustModelReflectionEvaluator(t, critic)
	image := core.NewImagePart("image-data", "image/png")
	request := []core.Message{
		core.NewUserTextMessage("Earlier request", image),
		core.NewTextMessage(core.RoleAssistant, "Earlier answer"),
		core.NewUserTextMessage("Please improve it"),
	}

	agent, err := CreateReflectionAgent(ReflectionAgentConfig{
		Model:         generator,
		Evaluator:     evaluator,
		MaxIterations: 3,
	})
	if err != nil {
		t.Fatalf("CreateReflectionAgent returned error: %v", err)
	}
	result, err := agent.Run(context.Background(), request)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if result.StopReason != ReflectionStopReasonAccepted || result.Iterations != 2 {
		t.Fatalf("unexpected completion: stop=%q iterations=%d", result.StopReason, result.Iterations)
	}
	if result.Draft != "Draft two" || len(result.Steps) != 2 {
		t.Fatalf("unexpected final reflection result: draft=%q steps=%d", result.Draft, len(result.Steps))
	}
	if reflectionMessageText(result.Steps[1].Draft) != "Draft two" || result.Steps[1].Evaluation.Verdict != ReflectionVerdictAccept {
		t.Fatalf("final draft was not paired with its own evaluation: %+v", result.Steps[1])
	}
	if len(generator.requests) != 2 || len(critic.requests) != 2 {
		t.Fatalf("expected two generations and two evaluations, got generation=%d evaluation=%d", len(generator.requests), len(critic.requests))
	}

	for i, criticRequest := range critic.requests {
		if len(criticRequest.Messages) != len(request)+2 {
			t.Fatalf("evaluation %d received %d messages, want complete request plus draft and instruction", i+1, len(criticRequest.Messages))
		}
		assertRequestPrefix(t, criticRequest.Messages, request)
		if got := criticRequest.Messages[0].Parts[1]; got.Type != core.PartTypeImage || got.Data != "image-data" || got.MIMEType != "image/png" {
			t.Fatalf("evaluation %d lost image input: %+v", i+1, got)
		}
	}

	secondGeneration := generator.requests[1].Messages
	if len(secondGeneration) != len(request)+2 {
		t.Fatalf("second generation received %d messages, want original request plus prior draft and revision instruction", len(secondGeneration))
	}
	assertRequestPrefix(t, secondGeneration, request)
	if got := reflectionMessageText(secondGeneration[len(request)]); got != "Draft one" {
		t.Fatalf("second generation did not receive prior draft: %q", got)
	}
	revision := reflectionMessageText(secondGeneration[len(request)+1])
	if !strings.Contains(revision, "Discuss the supplied image.") {
		t.Fatalf("second generation did not receive structured revision instruction: %q", revision)
	}
}

func TestReflectionAgentEvaluatesEveryDraftThroughMaxIterations(t *testing.T) {
	tests := []struct {
		name       string
		configured int
		want       int
	}{
		{name: "max one", configured: 1, want: 1},
		{name: "explicit max", configured: 3, want: 3},
		{name: "zero defaults to three", configured: 0, want: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			generator := reflectionScriptedModel("draft")
			evaluations := 0
			evaluator := ReflectionEvaluatorFunc(func(_ context.Context, input ReflectionInput) (ReflectionEvaluation, error) {
				evaluations++
				if input.Iteration != evaluations {
					t.Fatalf("evaluation iteration=%d, want %d", input.Iteration, evaluations)
				}
				return ReflectionEvaluation{
					Verdict:              ReflectionVerdictRevise,
					Summary:              "More work is required.",
					RevisionInstructions: []string{"Improve it."},
				}, nil
			})
			agent, err := CreateReflectionAgent(ReflectionAgentConfig{
				Model:         generator,
				Evaluator:     evaluator,
				MaxIterations: tt.configured,
			})
			if err != nil {
				t.Fatalf("CreateReflectionAgent returned error: %v", err)
			}

			result, err := agent.PromptText(context.Background(), "request")
			if err != nil {
				t.Fatalf("PromptText returned error: %v", err)
			}
			if len(generator.requests) != tt.want || evaluations != tt.want {
				t.Fatalf("expected %d generation/evaluation pairs, got generation=%d evaluation=%d", tt.want, len(generator.requests), evaluations)
			}
			if result.Iterations != tt.want || len(result.Steps) != tt.want {
				t.Fatalf("unexpected completed steps: iterations=%d steps=%d", result.Iterations, len(result.Steps))
			}
			if result.StopReason != ReflectionStopReasonMaxIterations {
				t.Fatalf("expected max-iterations stop, got %q", result.StopReason)
			}
		})
	}
}

func TestReflectionVerdictDoesNotUseSummaryKeywords(t *testing.T) {
	evaluator := ReflectionEvaluatorFunc(func(context.Context, ReflectionInput) (ReflectionEvaluation, error) {
		return ReflectionEvaluation{
			Verdict:              ReflectionVerdictRevise,
			Summary:              "The response is not excellent; saying no major issues would be inaccurate.",
			RevisionInstructions: []string{"Correct the factual error."},
		}, nil
	})
	agent, err := CreateReflectionAgent(ReflectionAgentConfig{
		Model:         reflectionScriptedModel("draft"),
		Evaluator:     evaluator,
		MaxIterations: 1,
	})
	if err != nil {
		t.Fatalf("CreateReflectionAgent returned error: %v", err)
	}

	result, err := agent.PromptText(context.Background(), "request")
	if err != nil {
		t.Fatalf("PromptText returned error: %v", err)
	}
	if result.StopReason != ReflectionStopReasonMaxIterations || result.Steps[0].Evaluation.Verdict != ReflectionVerdictRevise {
		t.Fatalf("summary keywords overrode structured verdict: %+v", result)
	}
}

func TestModelReflectionEvaluatorRejectsInvalidJSONContracts(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{name: "malformed", text: `not json`},
		{name: "code fence", text: "```json\n{\"verdict\":\"accept\",\"summary\":\"ok\",\"revision_instructions\":[]}\n```"},
		{name: "unknown field", text: `{"verdict":"accept","summary":"ok","revision_instructions":[],"confidence":1}`},
		{name: "case variant field", text: `{"Verdict":"accept","summary":"ok","revision_instructions":[]}`},
		{name: "duplicate field", text: `{"verdict":"revise","verdict":"accept","summary":"ok","revision_instructions":[]}`},
		{name: "missing field", text: `{"verdict":"accept","summary":"ok"}`},
		{name: "null field", text: `{"verdict":"accept","summary":"ok","revision_instructions":null}`},
		{name: "unknown verdict", text: `{"verdict":"approved","summary":"ok","revision_instructions":[]}`},
		{name: "revise without instructions", text: `{"verdict":"revise","summary":"needs work","revision_instructions":[]}`},
		{name: "accept with instructions", text: `{"verdict":"accept","summary":"ok","revision_instructions":["change it"]}`},
		{name: "empty summary", text: `{"verdict":"accept","summary":"  ","revision_instructions":[]}`},
		{name: "trailing object", text: `{"verdict":"accept","summary":"ok","revision_instructions":[]} {}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := reflectionScriptedModel(tt.text)
			evaluator := mustModelReflectionEvaluator(t, model)
			_, err := evaluator.Evaluate(context.Background(), ReflectionInput{
				Request:   []core.Message{core.NewUserTextMessage("request")},
				Draft:     core.NewTextMessage(core.RoleAssistant, "draft"),
				Iteration: 1,
			})
			if err == nil {
				t.Fatalf("expected invalid evaluation %q to fail", tt.text)
			}
		})
	}
}

func TestReflectionAgentReturnsPartialResultAndProtectsOwnershipOnEvaluatorError(t *testing.T) {
	sentinel := errors.New("critic unavailable")
	request := []core.Message{core.NewUserTextMessage("request", core.NewImagePart("original-image", "image/png"))}
	request[0].Metadata = map[string]any{"nested": map[string]any{"value": "original"}}

	evaluator := ReflectionEvaluatorFunc(func(_ context.Context, input ReflectionInput) (ReflectionEvaluation, error) {
		input.Request[0].Parts[0].Text = "mutated request"
		input.Request[0].Parts[1].Data = "mutated-image"
		input.Request[0].Metadata["nested"].(map[string]any)["value"] = "mutated"
		input.Draft.Parts[0].Text = "mutated draft"
		return ReflectionEvaluation{}, sentinel
	})
	agent, err := CreateReflectionAgent(ReflectionAgentConfig{
		Model:     reflectionScriptedModel("generated draft"),
		Evaluator: evaluator,
	})
	if err != nil {
		t.Fatalf("CreateReflectionAgent returned error: %v", err)
	}

	result, err := agent.Run(context.Background(), request)
	if !errors.Is(err, sentinel) {
		t.Fatalf("Run error=%v, want wrapped evaluator error", err)
	}
	if request[0].Parts[0].Text != "request" || request[0].Parts[1].Data != "original-image" {
		t.Fatalf("evaluator mutated caller request: %+v", request[0].Parts)
	}
	if got := request[0].Metadata["nested"].(map[string]any)["value"]; got != "original" {
		t.Fatalf("evaluator mutated caller metadata: %v", got)
	}
	if result.Draft != "generated draft" || len(result.Messages) != 2 {
		t.Fatalf("partial result did not retain generated draft: %+v", result)
	}
	if got := reflectionMessageText(result.Messages[1]); got != "generated draft" {
		t.Fatalf("evaluator mutated partial result draft: %q", got)
	}
	if result.Iterations != 1 || len(result.Steps) != 0 || result.StopReason != "" {
		t.Fatalf("failed evaluation must not create a completed step: %+v", result)
	}
}

func TestReflectionAgentDoesNotAcceptCanceledCustomEvaluation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	evaluator := ReflectionEvaluatorFunc(func(context.Context, ReflectionInput) (ReflectionEvaluation, error) {
		cancel()
		return ReflectionEvaluation{
			Verdict: ReflectionVerdictAccept,
			Summary: "accepted after cancellation",
		}, nil
	})
	agent, err := CreateReflectionAgent(ReflectionAgentConfig{
		Model:     reflectionScriptedModel("generated draft"),
		Evaluator: evaluator,
	})
	if err != nil {
		t.Fatalf("CreateReflectionAgent returned error: %v", err)
	}

	result, err := agent.PromptText(ctx, "request")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("PromptText error=%v, want context cancellation", err)
	}
	if result.Draft != "generated draft" || result.Iterations != 1 {
		t.Fatalf("canceled run lost its partial draft: %+v", result)
	}
	if len(result.Steps) != 0 || result.StopReason != "" {
		t.Fatalf("canceled evaluation was incorrectly accepted: %+v", result)
	}
}

func TestReflectionAgentRejectsGeneratorTerminalFailures(t *testing.T) {
	tests := []struct {
		name  string
		final core.Message
	}{
		{name: "error", final: reflectionFinal("draft", core.StopReasonError, "")},
		{name: "aborted", final: reflectionFinal("draft", core.StopReasonAborted, "")},
		{name: "length", final: reflectionFinal("draft", core.StopReasonLength, "")},
		{name: "unset stop reason", final: reflectionFinal("draft", "", "")},
		{name: "unknown stop reason", final: reflectionFinal("draft", core.StopReason("future"), "")},
		{name: "error message", final: reflectionFinal("draft", core.StopReasonStop, "provider failed")},
		{name: "no content", final: reflectionFinal("", core.StopReasonStop, "")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := &chatScriptedModel{responses: []chatScriptedResponse{{final: tt.final}}}
			evaluations := 0
			agent, err := CreateReflectionAgent(ReflectionAgentConfig{
				Model: model,
				Evaluator: ReflectionEvaluatorFunc(func(context.Context, ReflectionInput) (ReflectionEvaluation, error) {
					evaluations++
					return ReflectionEvaluation{}, nil
				}),
			})
			if err != nil {
				t.Fatalf("CreateReflectionAgent returned error: %v", err)
			}
			result, err := agent.PromptText(context.Background(), "request")
			if err == nil {
				t.Fatal("expected generator terminal failure")
			}
			if evaluations != 0 || result.Iterations != 0 || len(result.Steps) != 0 {
				t.Fatalf("terminal generator response reached evaluator: evaluations=%d result=%+v", evaluations, result)
			}
		})
	}
}

func TestModelReflectionEvaluatorRejectsTerminalFailures(t *testing.T) {
	tests := []struct {
		name  string
		final core.Message
	}{
		{name: "error", final: reflectionFinal(`{"verdict":"accept","summary":"ok","revision_instructions":[]}`, core.StopReasonError, "")},
		{name: "aborted", final: reflectionFinal(`{"verdict":"accept","summary":"ok","revision_instructions":[]}`, core.StopReasonAborted, "")},
		{name: "length", final: reflectionFinal(`{"verdict":"accept","summary":"ok","revision_instructions":[]}`, core.StopReasonLength, "")},
		{name: "unset stop reason", final: reflectionFinal(`{"verdict":"accept","summary":"ok","revision_instructions":[]}`, "", "")},
		{name: "unknown stop reason", final: reflectionFinal(`{"verdict":"accept","summary":"ok","revision_instructions":[]}`, core.StopReason("future"), "")},
		{name: "error message", final: reflectionFinal(`{"verdict":"accept","summary":"ok","revision_instructions":[]}`, core.StopReasonStop, "provider failed")},
		{name: "no content", final: reflectionFinal("", core.StopReasonStop, "")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := &chatScriptedModel{responses: []chatScriptedResponse{{final: tt.final}}}
			evaluator := mustModelReflectionEvaluator(t, model)
			_, err := evaluator.Evaluate(context.Background(), ReflectionInput{
				Request:   []core.Message{core.NewUserTextMessage("request")},
				Draft:     core.NewTextMessage(core.RoleAssistant, "draft"),
				Iteration: 1,
			})
			if err == nil {
				t.Fatal("expected evaluator terminal failure")
			}
		})
	}
}

func mustModelReflectionEvaluator(t *testing.T, model core.StreamModel) *ModelReflectionEvaluator {
	t.Helper()
	evaluator, err := NewModelReflectionEvaluator(ModelReflectionEvaluatorConfig{Model: model})
	if err != nil {
		t.Fatalf("NewModelReflectionEvaluator returned error: %v", err)
	}
	return evaluator
}

func reflectionScriptedModel(texts ...string) *chatScriptedModel {
	responses := make([]chatScriptedResponse, len(texts))
	for i, text := range texts {
		responses[i] = chatScriptedResponse{final: reflectionFinal(text, core.StopReasonStop, "")}
	}
	return &chatScriptedModel{responses: responses}
}

func reflectionFinal(text string, reason core.StopReason, errorMessage string) core.Message {
	message := core.Message{
		Role:         core.RoleAssistant,
		StopReason:   reason,
		ErrorMessage: errorMessage,
	}
	if text != "" {
		message.Parts = []core.Part{{Type: core.PartTypeText, Text: text}}
	}
	return message
}

func assertRequestPrefix(t *testing.T, got, want []core.Message) {
	t.Helper()
	if len(got) < len(want) {
		t.Fatalf("message list has %d entries, want at least %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Role != want[i].Role || reflectionMessageText(got[i]) != reflectionMessageText(want[i]) {
			t.Fatalf("message %d changed: got=%+v want=%+v", i, got[i], want[i])
		}
	}
}
