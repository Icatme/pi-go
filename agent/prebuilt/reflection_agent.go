package prebuilt

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	core "github.com/Icatme/pi-go/agent"
)

// ReflectionVerdict is the evaluator's decision for one generated draft.
type ReflectionVerdict string

const (
	// ReflectionVerdictAccept ends the reflection loop with the current draft.
	ReflectionVerdictAccept ReflectionVerdict = "accept"
	// ReflectionVerdictRevise requests another generation pass.
	ReflectionVerdictRevise ReflectionVerdict = "revise"
)

// ReflectionInput is the detached input supplied to a ReflectionEvaluator.
type ReflectionInput struct {
	Request   []core.Message
	Draft     core.Message
	Iteration int
}

// ReflectionEvaluation is the structured result for one draft.
type ReflectionEvaluation struct {
	Verdict              ReflectionVerdict `json:"verdict"`
	Summary              string            `json:"summary"`
	RevisionInstructions []string          `json:"revision_instructions"`
}

// ReflectionEvaluator evaluates a generated draft against its complete request.
type ReflectionEvaluator interface {
	Evaluate(context.Context, ReflectionInput) (ReflectionEvaluation, error)
}

// ReflectionEvaluatorFunc adapts a function to ReflectionEvaluator.
type ReflectionEvaluatorFunc func(context.Context, ReflectionInput) (ReflectionEvaluation, error)

// Evaluate calls f with the detached reflection input.
func (f ReflectionEvaluatorFunc) Evaluate(ctx context.Context, input ReflectionInput) (ReflectionEvaluation, error) {
	if f == nil {
		return ReflectionEvaluation{}, errors.New("prebuilt: reflection evaluator function is nil")
	}
	return f(ctx, input)
}

// ReflectionStep pairs each generated draft with the evaluation of that draft.
type ReflectionStep struct {
	Draft      core.Message
	Evaluation ReflectionEvaluation
}

// ReflectionStopReason explains why a reflection loop completed.
type ReflectionStopReason string

const (
	// ReflectionStopReasonAccepted means the evaluator accepted the final draft.
	ReflectionStopReasonAccepted ReflectionStopReason = "accepted"
	// ReflectionStopReasonMaxIterations means the last evaluated draft still needed revision.
	ReflectionStopReasonMaxIterations ReflectionStopReason = "max_iterations"
)

// ReflectionAgentConfig configures the sequential reflection helper.
type ReflectionAgentConfig struct {
	Model         core.StreamModel
	Evaluator     ReflectionEvaluator
	MaxIterations int
	SystemMessage string
}

// ModelReflectionEvaluatorConfig configures a strict model-backed evaluator.
type ModelReflectionEvaluatorConfig struct {
	Model        core.StreamModel
	SystemPrompt string
}

// ModelReflectionEvaluator evaluates each draft with an independent one-turn agent.
type ModelReflectionEvaluator struct {
	definition core.AgentDefinition
}

// NewModelReflectionEvaluator creates a model-backed evaluator that accepts one strict JSON object.
func NewModelReflectionEvaluator(config ModelReflectionEvaluatorConfig) (*ModelReflectionEvaluator, error) {
	if config.Model == nil {
		return nil, errors.New("prebuilt: reflection evaluator model is required")
	}
	if strings.TrimSpace(config.SystemPrompt) == "" {
		config.SystemPrompt = defaultReflectionEvaluatorSystemPrompt()
	}
	return &ModelReflectionEvaluator{
		definition: core.AgentDefinition{
			Model:        config.Model,
			SystemPrompt: config.SystemPrompt,
			MaxTurns:     1,
		},
	}, nil
}

// Evaluate evaluates one draft and decodes the final assistant response as strict JSON.
func (e *ModelReflectionEvaluator) Evaluate(ctx context.Context, input ReflectionInput) (ReflectionEvaluation, error) {
	agent, err := core.NewAgent(e.definition)
	if err != nil {
		return ReflectionEvaluation{}, err
	}

	messages := cloneReflectionMessages(input.Request)
	messages = append(messages, cloneReflectionMessage(input.Draft))
	messages = append(messages, core.NewUserTextMessage(reflectionEvaluationInstruction(input.Iteration)))
	agent.ReplaceMessages(messages)
	if err := agent.Continue(ctx); err != nil {
		return ReflectionEvaluation{}, fmt.Errorf("prebuilt: reflection evaluation failed: %w", err)
	}

	_, text, err := finalReflectionAssistant(agent.State().Messages, "reflection evaluation")
	if err != nil {
		return ReflectionEvaluation{}, err
	}

	evaluation, err := decodeReflectionEvaluation(text)
	if err != nil {
		return ReflectionEvaluation{}, err
	}
	return normalizeReflectionEvaluation(evaluation)
}

// ReflectionResult captures the final state of one reflection run.
type ReflectionResult struct {
	Messages   []core.Message
	Draft      string
	Steps      []ReflectionStep
	StopReason ReflectionStopReason
	Iterations int
}

// ReflectionAgent runs generate-and-evaluate passes on top of native agents.
type ReflectionAgent struct {
	generatorDefinition core.AgentDefinition
	evaluator           ReflectionEvaluator
	maxIterations       int
}

// CreateReflectionAgent creates a native reflection helper.
func CreateReflectionAgent(config ReflectionAgentConfig) (*ReflectionAgent, error) {
	resolved, err := normalizeReflectionConfig(config)
	if err != nil {
		return nil, err
	}

	return &ReflectionAgent{
		generatorDefinition: core.AgentDefinition{
			Model:        resolved.Model,
			SystemPrompt: resolved.SystemMessage,
			MaxTurns:     1,
		},
		evaluator:     resolved.Evaluator,
		maxIterations: resolved.MaxIterations,
	}, nil
}

// PromptText runs the reflection loop from one user text prompt.
func (a *ReflectionAgent) PromptText(ctx context.Context, text string) (ReflectionResult, error) {
	return a.Run(ctx, []core.Message{core.NewUserTextMessage(text)})
}

// Run executes reflection passes over the provided initial messages.
func (a *ReflectionAgent) Run(ctx context.Context, messages []core.Message) (ReflectionResult, error) {
	if len(messages) == 0 {
		return ReflectionResult{}, errors.New("prebuilt: reflection agent requires at least one message")
	}

	generator, err := core.NewAgent(a.generatorDefinition)
	if err != nil {
		return ReflectionResult{}, err
	}

	request := cloneReflectionMessages(messages)
	result := ReflectionResult{Messages: cloneReflectionMessages(request)}
	var (
		previousDraft      core.Message
		previousEvaluation ReflectionEvaluation
	)

	for iteration := 1; iteration <= a.maxIterations; iteration++ {
		if err := ctx.Err(); err != nil {
			return result, fmt.Errorf("prebuilt: reflection iteration %d canceled: %w", iteration, err)
		}

		generatorMessages := cloneReflectionMessages(request)
		if iteration > 1 {
			generatorMessages = append(generatorMessages, cloneReflectionMessage(previousDraft))
			generatorMessages = append(generatorMessages, core.NewUserTextMessage(revisionInstruction(previousEvaluation.RevisionInstructions)))
		}

		if err := generator.Reset(); err != nil {
			return result, err
		}
		generator.ReplaceMessages(generatorMessages)
		if err := generator.Continue(ctx); err != nil {
			return result, fmt.Errorf("prebuilt: reflection generation iteration %d failed: %w", iteration, err)
		}

		draft, draftText, err := finalReflectionAssistant(generator.State().Messages, "reflection generation")
		if err != nil {
			return result, fmt.Errorf("prebuilt: reflection generation iteration %d: %w", iteration, err)
		}
		result.Draft = draftText
		result.Messages = append(result.Messages, cloneReflectionMessage(draft))
		result.Iterations = iteration
		if err := ctx.Err(); err != nil {
			return result, fmt.Errorf("prebuilt: reflection evaluation iteration %d canceled: %w", iteration, err)
		}

		input := ReflectionInput{
			Request:   cloneReflectionMessages(request),
			Draft:     cloneReflectionMessage(draft),
			Iteration: iteration,
		}
		evaluation, err := a.evaluator.Evaluate(ctx, input)
		if err != nil {
			return result, fmt.Errorf("prebuilt: reflection evaluation iteration %d failed: %w", iteration, err)
		}
		if err := ctx.Err(); err != nil {
			return result, fmt.Errorf("prebuilt: reflection evaluation iteration %d canceled: %w", iteration, err)
		}
		evaluation, err = normalizeReflectionEvaluation(evaluation)
		if err != nil {
			return result, fmt.Errorf("prebuilt: reflection evaluation iteration %d: %w", iteration, err)
		}

		step := ReflectionStep{
			Draft:      cloneReflectionMessage(draft),
			Evaluation: cloneReflectionEvaluation(evaluation),
		}
		result.Steps = append(result.Steps, step)

		if evaluation.Verdict == ReflectionVerdictAccept {
			result.StopReason = ReflectionStopReasonAccepted
			return result, nil
		}
		if iteration == a.maxIterations {
			result.StopReason = ReflectionStopReasonMaxIterations
			return result, nil
		}

		previousDraft = cloneReflectionMessage(draft)
		previousEvaluation = cloneReflectionEvaluation(evaluation)
	}

	return result, nil
}

func normalizeReflectionConfig(config ReflectionAgentConfig) (ReflectionAgentConfig, error) {
	if config.Model == nil {
		return ReflectionAgentConfig{}, errors.New("prebuilt: reflection agent model is required")
	}
	if config.MaxIterations < 0 {
		return ReflectionAgentConfig{}, errors.New("prebuilt: reflection agent max iterations must not be negative")
	}
	if config.MaxIterations == 0 {
		config.MaxIterations = 3
	}
	if strings.TrimSpace(config.SystemMessage) == "" {
		config.SystemMessage = "You are a helpful assistant. Generate a high-quality response to the user's request."
	}
	if config.Evaluator == nil {
		evaluator, err := NewModelReflectionEvaluator(ModelReflectionEvaluatorConfig{Model: config.Model})
		if err != nil {
			return ReflectionAgentConfig{}, err
		}
		config.Evaluator = evaluator
	}
	return config, nil
}

func finalReflectionAssistant(messages []core.Message, phase string) (core.Message, string, error) {
	if len(messages) == 0 || messages[len(messages)-1].Role != core.RoleAssistant {
		return core.Message{}, "", fmt.Errorf("prebuilt: %s produced no final assistant message", phase)
	}

	message := cloneReflectionMessage(messages[len(messages)-1])
	if strings.TrimSpace(message.ErrorMessage) != "" {
		return core.Message{}, "", fmt.Errorf("prebuilt: %s failed: %s", phase, message.ErrorMessage)
	}
	if message.StopReason != core.StopReasonStop {
		return core.Message{}, "", fmt.Errorf("prebuilt: %s ended with stop reason %q", phase, message.StopReason)
	}

	text := reflectionMessageText(message)
	if strings.TrimSpace(text) == "" {
		return core.Message{}, "", fmt.Errorf("prebuilt: %s produced no text content", phase)
	}
	return message, text, nil
}

func decodeReflectionEvaluation(text string) (ReflectionEvaluation, error) {
	decoder := json.NewDecoder(strings.NewReader(text))
	start, err := decoder.Token()
	if err != nil {
		return ReflectionEvaluation{}, fmt.Errorf("prebuilt: reflection evaluation must be one JSON object: %w", err)
	}
	if delimiter, ok := start.(json.Delim); !ok || delimiter != '{' {
		return ReflectionEvaluation{}, errors.New("prebuilt: reflection evaluation must be one JSON object")
	}

	fields := make(map[string]json.RawMessage, 3)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return ReflectionEvaluation{}, fmt.Errorf("prebuilt: reflection evaluation contains an invalid field: %w", err)
		}
		name, ok := token.(string)
		if !ok {
			return ReflectionEvaluation{}, errors.New("prebuilt: reflection evaluation field name must be a string")
		}
		switch name {
		case "verdict", "summary", "revision_instructions":
		default:
			return ReflectionEvaluation{}, fmt.Errorf("prebuilt: reflection evaluation contains unknown field %q", name)
		}
		if _, exists := fields[name]; exists {
			return ReflectionEvaluation{}, fmt.Errorf("prebuilt: reflection evaluation contains duplicate field %q", name)
		}

		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil {
			return ReflectionEvaluation{}, fmt.Errorf("prebuilt: reflection evaluation field %q is invalid: %w", name, err)
		}
		if strings.TrimSpace(string(raw)) == "null" {
			return ReflectionEvaluation{}, fmt.Errorf("prebuilt: reflection evaluation field %q must not be null", name)
		}
		fields[name] = append(json.RawMessage(nil), raw...)
	}
	if end, err := decoder.Token(); err != nil {
		return ReflectionEvaluation{}, fmt.Errorf("prebuilt: reflection evaluation object is incomplete: %w", err)
	} else if delimiter, ok := end.(json.Delim); !ok || delimiter != '}' {
		return ReflectionEvaluation{}, errors.New("prebuilt: reflection evaluation must be one JSON object")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return ReflectionEvaluation{}, errors.New("prebuilt: reflection evaluation contains trailing JSON")
		}
		return ReflectionEvaluation{}, fmt.Errorf("prebuilt: reflection evaluation contains trailing content: %w", err)
	}

	for _, name := range [...]string{"verdict", "summary", "revision_instructions"} {
		if _, ok := fields[name]; !ok {
			return ReflectionEvaluation{}, fmt.Errorf("prebuilt: reflection evaluation is missing required field %q", name)
		}
	}

	var evaluation ReflectionEvaluation
	if err := json.Unmarshal(fields["verdict"], &evaluation.Verdict); err != nil {
		return ReflectionEvaluation{}, fmt.Errorf("prebuilt: reflection evaluation field %q is invalid: %w", "verdict", err)
	}
	if err := json.Unmarshal(fields["summary"], &evaluation.Summary); err != nil {
		return ReflectionEvaluation{}, fmt.Errorf("prebuilt: reflection evaluation field %q is invalid: %w", "summary", err)
	}
	if err := json.Unmarshal(fields["revision_instructions"], &evaluation.RevisionInstructions); err != nil {
		return ReflectionEvaluation{}, fmt.Errorf("prebuilt: reflection evaluation field %q is invalid: %w", "revision_instructions", err)
	}
	return evaluation, nil
}

func normalizeReflectionEvaluation(evaluation ReflectionEvaluation) (ReflectionEvaluation, error) {
	evaluation.Summary = strings.TrimSpace(evaluation.Summary)
	if evaluation.Summary == "" {
		return ReflectionEvaluation{}, errors.New("prebuilt: reflection evaluation summary must not be empty")
	}

	switch evaluation.Verdict {
	case ReflectionVerdictAccept:
		if len(evaluation.RevisionInstructions) != 0 {
			return ReflectionEvaluation{}, errors.New("prebuilt: accepted reflection evaluation must not include revision instructions")
		}
		evaluation.RevisionInstructions = nil
	case ReflectionVerdictRevise:
		instructions := make([]string, 0, len(evaluation.RevisionInstructions))
		for _, instruction := range evaluation.RevisionInstructions {
			if trimmed := strings.TrimSpace(instruction); trimmed != "" {
				instructions = append(instructions, trimmed)
			}
		}
		if len(instructions) == 0 {
			return ReflectionEvaluation{}, errors.New("prebuilt: revise reflection evaluation requires at least one revision instruction")
		}
		evaluation.RevisionInstructions = instructions
	default:
		return ReflectionEvaluation{}, fmt.Errorf("prebuilt: unknown reflection verdict %q", evaluation.Verdict)
	}
	return evaluation, nil
}

func reflectionEvaluationInstruction(iteration int) string {
	return fmt.Sprintf(`Evaluate the assistant draft immediately above against the complete request history. This is reflection iteration %d.
Return exactly one JSON object and no other text, with this shape:
{"verdict":"accept|revise","summary":"non-empty assessment","revision_instructions":["specific instruction"]}
Use verdict "accept" only when the draft satisfies the request, and then revision_instructions must be empty. Use verdict "revise" otherwise, with at least one non-empty revision instruction.`, iteration)
}

func revisionInstruction(instructions []string) string {
	var builder strings.Builder
	builder.WriteString("Revise the assistant draft immediately above. Apply every instruction below:\n")
	for i, instruction := range instructions {
		fmt.Fprintf(&builder, "%d. %s\n", i+1, instruction)
	}
	builder.WriteString("Return only the revised response.")
	return builder.String()
}

func defaultReflectionEvaluatorSystemPrompt() string {
	return "You are a rigorous response evaluator. Judge the draft against the full request and follow the required structured-output contract exactly."
}

func reflectionMessageText(message core.Message) string {
	var builder strings.Builder
	for _, part := range message.Parts {
		if part.Type == core.PartTypeText {
			builder.WriteString(part.Text)
		}
	}
	return builder.String()
}

func cloneReflectionMessages(messages []core.Message) []core.Message {
	if len(messages) == 0 {
		return nil
	}
	cloned := make([]core.Message, len(messages))
	for i, message := range messages {
		cloned[i] = cloneReflectionMessage(message)
	}
	return cloned
}

func cloneReflectionMessage(message core.Message) core.Message {
	cloned := message
	if len(message.Parts) > 0 {
		cloned.Parts = append([]core.Part(nil), message.Parts...)
	}
	if len(message.ToolCalls) > 0 {
		cloned.ToolCalls = make([]core.ToolCall, len(message.ToolCalls))
		for i, call := range message.ToolCalls {
			cloned.ToolCalls[i] = call
			if len(call.Arguments) > 0 {
				cloned.ToolCalls[i].Arguments = append([]byte(nil), call.Arguments...)
			}
			cloned.ToolCalls[i].ParsedArgs = cloneStringAnyMap(call.ParsedArgs)
		}
	}
	if message.ToolResult != nil {
		payload := *message.ToolResult
		if len(message.ToolResult.Content) > 0 {
			payload.Content = append([]core.Part(nil), message.ToolResult.Content...)
		}
		payload.Details = cloneAny(message.ToolResult.Details)
		cloned.ToolResult = &payload
	}
	cloned.Metadata = cloneStringAnyMap(message.Metadata)
	cloned.Payload = cloneStringAnyMap(message.Payload)
	return cloned
}

func cloneReflectionEvaluation(evaluation ReflectionEvaluation) ReflectionEvaluation {
	cloned := evaluation
	if len(evaluation.RevisionInstructions) > 0 {
		cloned.RevisionInstructions = append([]string(nil), evaluation.RevisionInstructions...)
	}
	return cloned
}
