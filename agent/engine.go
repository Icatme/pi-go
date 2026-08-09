package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"sync"
	"time"
)

// LoopHooks customize runtime behavior between turns.
type LoopHooks struct {
	ResolveDefinition   func(context.Context, AgentDefinition, AgentSnapshot) (AgentDefinition, error)
	GetSteeringMessages func(context.Context) ([]Message, error)
	GetFollowUpMessages func(context.Context) ([]Message, error)
	ToolGate            ToolGateHook
}

// Engine executes agent turns against a mutable snapshot.
type Engine struct{}

type loopRuntimeState struct {
	turnEnded bool
}

type loopExecutionState struct {
	firstTurn bool
	turn      int
	overrides turnOverrides
	durable   *loopDurableState
}

type loopDurableState struct {
	messages     []Message
	systemPrompt string
	model        ModelRef
}

type engineRunError struct {
	cause       error
	newMessages []Message
	turnEnded   bool
}

func (e *engineRunError) Error() string { return e.cause.Error() }
func (e *engineRunError) Unwrap() error { return e.cause }

func wrapEngineRunError(err error, newMessages []Message, turnEnded bool) error {
	if err == nil {
		return nil
	}
	return &engineRunError{cause: err, newMessages: cloneMessages(newMessages), turnEnded: turnEnded}
}

func engineRunErrorContext(err error) ([]Message, bool) {
	var runErr *engineRunError
	if !errors.As(err, &runErr) {
		return nil, false
	}
	return cloneMessages(runErr.newMessages), runErr.turnEnded
}

// NewEngine creates a new stateless runtime engine.
func NewEngine() *Engine {
	return &Engine{}
}

// Run appends prompts and executes turns until the agent stops.
func (e *Engine) Run(ctx context.Context, definition AgentDefinition, snapshot *AgentSnapshot, prompts []Message, emit EventSink) (out *AgentSnapshot, err error) {
	return e.RunWithHooks(ctx, definition, snapshot, prompts, emit, LoopHooks{})
}

// RunWithHooks appends prompts and executes turns with runtime hooks.
func (e *Engine) RunWithHooks(ctx context.Context, definition AgentDefinition, snapshot *AgentSnapshot, prompts []Message, emit EventSink, hooks LoopHooks) (out *AgentSnapshot, err error) {
	if hasPendingToolState(snapshot) {
		return nil, ErrPendingToolCallsRequireResume
	}
	if len(prompts) == 0 {
		return nil, ErrNoPromptMessages
	}

	definition, err = definition.Validate()
	if err != nil {
		return nil, err
	}

	next := cloneSnapshotPtr(snapshot)
	runtimeState := &loopRuntimeState{}
	var newMessages []Message
	started := false
	defer func() {
		if !started || err == nil || hooks.ToolGate == nil {
			return
		}
		if !runtimeState.turnEnded && !isToolCallsSuspended(err) {
			emitEvent(emit, AgentEvent{Type: EventTurnEnd})
			runtimeState.turnEnded = true
		}
		emitEvent(emit, AgentEvent{Type: EventAgentEnd, Messages: cloneMessages(newMessages), Err: err})
	}()

	emitEvent(emit, AgentEvent{Type: EventAgentStart})
	started = true
	emitEvent(emit, AgentEvent{Type: EventTurnStart})
	normalized := normalizeMessages(prompts)
	newMessages = cloneMessages(normalized)
	next.Messages = append(next.Messages, normalized...)
	for i := range normalized {
		msg := cloneMessage(normalized[i])
		emitEvent(emit, AgentEvent{Type: EventMessageStart, Message: &msg})
		emitEvent(emit, AgentEvent{Type: EventMessageEnd, Message: &msg})
	}

	initialPending, err := dequeueHookMessages(ctx, hooks.GetSteeringMessages)
	if err != nil {
		next.Error = err.Error()
		err = wrapEngineRunError(err, newMessages, false)
		return next, err
	}
	next, newMessages, err = e.runLoop(ctx, definition, next, emit, hooks, initialPending, normalized, runtimeState, loopExecutionState{firstTurn: true, turn: 1})
	if err != nil {
		err = wrapEngineRunError(err, newMessages, runtimeState.turnEnded)
		return next, err
	}
	emitEvent(emit, AgentEvent{Type: EventAgentEnd, Messages: cloneMessages(newMessages)})
	return next, nil
}

// Continue executes turns from the existing snapshot without appending prompts.
func (e *Engine) Continue(ctx context.Context, definition AgentDefinition, snapshot *AgentSnapshot, emit EventSink) (out *AgentSnapshot, err error) {
	return e.ContinueWithHooks(ctx, definition, snapshot, emit, LoopHooks{})
}

// ContinueWithHooks executes turns from the existing snapshot with runtime hooks.
func (e *Engine) ContinueWithHooks(ctx context.Context, definition AgentDefinition, snapshot *AgentSnapshot, emit EventSink, hooks LoopHooks) (out *AgentSnapshot, err error) {
	definition, err = definition.Validate()
	if err != nil {
		return nil, err
	}

	next := cloneSnapshotPtr(snapshot)
	if hasPendingToolState(next) {
		return nil, ErrPendingToolCallsRequireResume
	}
	if len(next.Messages) == 0 {
		return nil, ErrNoMessagesToContinue
	}
	if tail := next.Messages[len(next.Messages)-1]; tail.Role == RoleAssistant {
		return nil, ErrCannotContinueFromAssistant
	}

	runtimeState := &loopRuntimeState{}
	var newMessages []Message
	started := false
	defer func() {
		if !started || err == nil || hooks.ToolGate == nil {
			return
		}
		if !runtimeState.turnEnded && !isToolCallsSuspended(err) {
			emitEvent(emit, AgentEvent{Type: EventTurnEnd})
			runtimeState.turnEnded = true
		}
		emitEvent(emit, AgentEvent{Type: EventAgentEnd, Messages: cloneMessages(newMessages), Err: err})
	}()

	emitEvent(emit, AgentEvent{Type: EventAgentStart})
	started = true
	emitEvent(emit, AgentEvent{Type: EventTurnStart})

	initialPending, err := dequeueHookMessages(ctx, hooks.GetSteeringMessages)
	if err != nil {
		next.Error = err.Error()
		err = wrapEngineRunError(err, nil, false)
		return next, err
	}

	next, newMessages, err = e.runLoop(ctx, definition, next, emit, hooks, initialPending, nil, runtimeState, loopExecutionState{firstTurn: true, turn: 1})
	if err != nil {
		err = wrapEngineRunError(err, newMessages, runtimeState.turnEnded)
		return next, err
	}
	emitEvent(emit, AgentEvent{Type: EventAgentEnd, Messages: cloneMessages(newMessages)})
	return next, nil
}

// ResumePendingToolCallsWithHooks resumes the exact tool-call batch recorded in
// snapshot without appending a duplicate assistant message. The batch is
// resolved and preflighted again against the current definition and hooks.
func (e *Engine) ResumePendingToolCallsWithHooks(ctx context.Context, definition AgentDefinition, snapshot *AgentSnapshot, emit EventSink, hooks LoopHooks) (out *AgentSnapshot, err error) {
	if hooks.ToolGate == nil {
		return nil, ErrToolGateRequired
	}
	definition, err = definition.Validate()
	if err != nil {
		return nil, err
	}

	next := cloneSnapshotPtr(snapshot)
	assistant, err := validatePendingToolBatch(*next)
	if err != nil {
		return nil, err
	}
	pendingTurn, err := pendingToolTurn(*next)
	if err != nil {
		return nil, err
	}
	originalSystemPrompt := next.SystemPrompt
	originalModel := cloneModelRef(next.Model)
	originalPendingToolCalls := clonePendingToolCalls(next.PendingToolCalls)
	originalPendingToolControl := clonePendingToolControl(next.PendingToolControl)

	runtimeState := &loopRuntimeState{}
	var newMessages []Message
	started := false
	pendingBatchCommitted := false
	defer func() {
		if !started {
			return
		}
		if pendingBatchCommitted && err != nil && !runtimeState.turnEnded && !isToolCallsSuspended(err) {
			emitEvent(emit, AgentEvent{Type: EventTurnEnd})
			runtimeState.turnEnded = true
		}
		emitEvent(emit, AgentEvent{Type: EventAgentEnd, Messages: cloneMessages(newMessages), Err: err})
	}()

	emitEvent(emit, AgentEvent{Type: EventAgentStart})
	started = true

	resolutionSnapshot := cloneSnapshotValue(*next)
	clearPendingToolControlState(&resolutionSnapshot)
	resolvedDefinition, resolveErr := resolveLoopDefinition(ctx, hooks, definition, resolutionSnapshot)
	if resolveErr != nil {
		next.Error = resolveErr.Error()
		err = wrapEngineRunError(resolveErr, nil, false)
		return next, err
	}
	tools, resolveErr := resolvedDefinition.ResolveTools(ctx, resolutionSnapshot)
	if resolveErr != nil {
		next.Error = resolveErr.Error()
		err = wrapEngineRunError(resolveErr, nil, false)
		return next, err
	}

	toolBatch, batchErr := e.executeToolCalls(ctx, resolvedDefinition, next, assistant, tools, emit, hooks.ToolGate)
	newMessages = append(newMessages, cloneMessages(toolBatch.messages)...)
	if isToolCallsSuspended(batchErr) {
		setPendingToolControlState(next, pendingTurn, assistant)
		next.Error = ""
		err = wrapEngineRunError(batchErr, newMessages, false)
		return next, err
	}
	batchCommitted := len(toolBatch.messages) == len(assistant.ToolCalls)
	if !batchCommitted {
		next.PendingToolCalls = clonePendingToolCalls(originalPendingToolCalls)
		next.PendingToolControl = clonePendingToolControl(originalPendingToolControl)
		if batchErr == nil {
			batchErr = errors.New("agent: pending tool batch did not commit all results")
		}
		next.Error = batchErr.Error()
		err = wrapEngineRunError(batchErr, newMessages, false)
		return next, err
	}
	pendingBatchCommitted = true
	emitEvent(emit, AgentEvent{
		Type:         EventTurnEnd,
		Message:      &assistant,
		ToolMessages: cloneMessages(toolBatch.messages),
	})
	runtimeState.turnEnded = true
	if batchErr != nil {
		clearPendingToolControlState(next)
		next.Error = batchErr.Error()
		err = wrapEngineRunError(batchErr, newMessages, true)
		return next, err
	}
	clearPendingToolControlState(next)

	turnContext := ShouldStopAfterTurnContext{
		Message:     cloneMessage(assistant),
		ToolResults: cloneMessages(toolBatch.messages),
		Context:     buildAgentContext(resolvedDefinition, *next, tools),
		NewMessages: cloneMessages(newMessages),
	}
	durable := &loopDurableState{
		messages:     cloneMessages(next.Messages),
		systemPrompt: originalSystemPrompt,
		model:        originalModel,
	}
	var overrides turnOverrides
	if resolvedDefinition.PrepareNextTurn != nil {
		update, prepareErr := resolvedDefinition.PrepareNextTurn(ctx, turnContext)
		if prepareErr != nil {
			next.Error = prepareErr.Error()
			err = wrapEngineRunError(prepareErr, newMessages, true)
			return next, err
		}
		if update != nil {
			overrides.merge(update, next)
			resolvedDefinition = overrides.apply(resolvedDefinition, next)
			if update.Context != nil {
				tools = cloneTools(update.Context.Tools)
			}
			turnContext.Context = buildAgentContext(resolvedDefinition, *next, tools)
		}
	}

	if resolvedDefinition.ShouldStopAfterTurn != nil {
		stop, stopErr := resolvedDefinition.ShouldStopAfterTurn(ctx, turnContext)
		if stopErr != nil {
			restoreLoopDurable(next, durable)
			next.Error = stopErr.Error()
			err = wrapEngineRunError(stopErr, newMessages, true)
			return next, err
		}
		if stop {
			restoreLoopDurable(next, durable)
			next.Error = ""
			return next, nil
		}
	}
	if toolBatch.terminate {
		restoreLoopDurable(next, durable)
		next.Error = ""
		return next, nil
	}

	pendingMessages, dequeueErr := dequeueHookMessages(ctx, hooks.GetSteeringMessages)
	if dequeueErr != nil {
		restoreLoopDurable(next, durable)
		next.Error = dequeueErr.Error()
		err = wrapEngineRunError(dequeueErr, newMessages, true)
		return next, err
	}
	runtimeState.turnEnded = true
	next, newMessages, batchErr = e.runLoop(ctx, definition, next, emit, hooks, pendingMessages, newMessages, runtimeState, loopExecutionState{
		firstTurn: false,
		turn:      pendingTurn + 1,
		overrides: overrides,
		durable:   durable,
	})
	if batchErr != nil {
		err = wrapEngineRunError(batchErr, newMessages, runtimeState.turnEnded)
		return next, err
	}
	return next, nil
}

func hasPendingToolState(snapshot *AgentSnapshot) bool {
	return snapshot != nil && (len(snapshot.PendingToolCalls) > 0 || snapshot.PendingToolControl != nil)
}

// ValidatePendingToolState verifies that snapshot contains one intact,
// resumable assistant tool-call batch without executing hooks or tools.
func ValidatePendingToolState(snapshot AgentSnapshot) error {
	_, err := validatePendingToolBatch(snapshot)
	return err
}

func validatePendingToolBatch(snapshot AgentSnapshot) (Message, error) {
	if snapshot.PendingToolControl == nil {
		return Message{}, ErrPendingToolControlRequired
	}
	pendingTurn, err := pendingToolTurn(snapshot)
	if err != nil {
		return Message{}, err
	}
	if len(snapshot.Messages) == 0 {
		return Message{}, errors.New("agent: cannot resume pending tool calls without messages")
	}
	assistant := snapshot.Messages[len(snapshot.Messages)-1]
	if assistant.Role != RoleAssistant || len(assistant.ToolCalls) == 0 {
		return Message{}, errors.New("agent: pending tool calls require an assistant tail with tool calls")
	}
	if isErrorAssistantMessage(assistant) || assistant.StopReason == StopReasonLength {
		return Message{}, errors.New("agent: cannot resume tool calls from an incomplete or failed assistant message")
	}
	if err := validateNormalizedToolCallIDs(assistant.ToolCalls); err != nil {
		return Message{}, err
	}
	if len(snapshot.PendingToolCalls) != len(assistant.ToolCalls) {
		return Message{}, errors.New("agent: pending tool calls do not match assistant tool calls")
	}
	for i, call := range assistant.ToolCalls {
		pending := snapshot.PendingToolCalls[i]
		if pending.ToolCallID != call.ID || pending.OriginalToolCallID != call.OriginalID || pending.ToolName != call.Name {
			return Message{}, fmt.Errorf("agent: pending tool call %d does not match assistant tool call", i)
		}
	}
	if snapshot.PendingToolControl != nil {
		expectedBinding, err := pendingToolBindingDigest(assistant, pendingTurn)
		if err != nil {
			return Message{}, err
		}
		if snapshot.PendingToolControl.Binding == "" || snapshot.PendingToolControl.Binding != expectedBinding {
			return Message{}, errors.New("agent: pending assistant tool-call binding does not match")
		}
	}
	return cloneMessage(assistant), nil
}

func validateNormalizedToolCallIDs(calls []ToolCall) error {
	seenIDs := make(map[string]struct{}, len(calls))
	for i, call := range calls {
		normalizedID := strings.TrimSpace(call.ID)
		if normalizedID == "" {
			return fmt.Errorf("agent: assistant tool call %d has an empty normalized id", i)
		}
		if _, exists := seenIDs[normalizedID]; exists {
			return fmt.Errorf("agent: assistant tool call %d repeats normalized id %q", i, normalizedID)
		}
		seenIDs[normalizedID] = struct{}{}
	}
	return nil
}

func restoreLoopDurable(snapshot *AgentSnapshot, durable *loopDurableState) {
	if snapshot == nil || durable == nil {
		return
	}
	snapshot.Messages = cloneMessages(durable.messages)
	snapshot.SystemPrompt = durable.systemPrompt
	snapshot.Model = cloneModelRef(durable.model)
}

func pendingToolTurn(snapshot AgentSnapshot) (int, error) {
	if snapshot.PendingToolControl == nil {
		return 1, nil
	}
	if snapshot.PendingToolControl.Turn <= 0 {
		return 0, errors.New("agent: pending tool turn metadata is invalid")
	}
	return snapshot.PendingToolControl.Turn, nil
}

func setPendingToolControlState(snapshot *AgentSnapshot, turn int, assistant Message) {
	binding, _ := pendingToolBindingDigest(assistant, turn)
	snapshot.PendingToolControl = &PendingToolControl{Turn: turn, Binding: binding}
}

func clearPendingToolControlState(snapshot *AgentSnapshot) {
	snapshot.PendingToolControl = nil
}

func pendingToolBindingDigest(assistant Message, turn int) (string, error) {
	type callBinding struct {
		Index            int    `json:"index"`
		ID               string `json:"id"`
		OriginalID       string `json:"original_id"`
		Name             string `json:"name"`
		RawPresent       bool   `json:"raw_present"`
		Arguments        string `json:"arguments"`
		ParsedArgs       string `json:"parsed_args"`
		ThoughtSignature string `json:"thought_signature"`
	}
	bindings := make([]callBinding, 0, len(assistant.ToolCalls))
	for i, call := range assistant.ToolCalls {
		canonicalArguments, err := canonicalRawToolArguments(call.Arguments)
		if err != nil {
			return "", fmt.Errorf("agent: assistant tool call %d has invalid raw arguments: %w", i, err)
		}
		parsedArgs, err := canonicalJSONValue(call.ParsedArgs)
		if err != nil {
			return "", fmt.Errorf("agent: assistant tool call %d has non-durable parsed arguments: %w", i, err)
		}
		bindings = append(bindings, callBinding{
			Index:            i,
			ID:               call.ID,
			OriginalID:       call.OriginalID,
			Name:             call.Name,
			RawPresent:       len(call.Arguments) > 0,
			Arguments:        canonicalArguments,
			ParsedArgs:       parsedArgs,
			ThoughtSignature: call.ThoughtSignature,
		})
	}
	payload, err := json.Marshal(struct {
		Domain string        `json:"domain"`
		Turn   int           `json:"turn"`
		Calls  []callBinding `json:"calls"`
	}{Domain: "pi-go.agent.pending-tools.v1", Turn: turn, Calls: bindings})
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(payload)), nil
}

func canonicalRawToolArguments(arguments json.RawMessage) (string, error) {
	if len(arguments) == 0 {
		return "{}", nil
	}
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.UseNumber()
	var parsed any
	if err := decoder.Decode(&parsed); err != nil {
		return "", err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return "", errors.New("multiple JSON argument values")
		}
		return "", err
	}
	canonical, err := json.Marshal(parsed)
	if err != nil {
		return "", err
	}
	return string(canonical), nil
}

func canonicalJSONValue(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return "", err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return "", errors.New("multiple JSON values")
		}
		return "", err
	}
	canonical, err := json.Marshal(decoded)
	if err != nil {
		return "", err
	}
	return string(canonical), nil
}

func isToolCallsSuspended(err error) bool {
	var suspended *ToolCallsSuspendedError
	return errors.As(err, &suspended)
}

func (e *Engine) runLoop(ctx context.Context, definition AgentDefinition, snapshot *AgentSnapshot, emit EventSink, hooks LoopHooks, pendingMessages []Message, initialNewMessages []Message, runtimeState *loopRuntimeState, executionState loopExecutionState) (*AgentSnapshot, []Message, error) {
	firstTurn := executionState.firstTurn
	turn := executionState.turn
	if turn <= 0 {
		turn = 1
	}
	overrides := executionState.overrides
	newMessages := cloneMessages(initialNewMessages)
	transcript := cloneMessages(snapshot.Messages)
	originalSystemPrompt := snapshot.SystemPrompt
	originalModel := cloneModelRef(snapshot.Model)
	if executionState.durable != nil {
		transcript = cloneMessages(executionState.durable.messages)
		originalSystemPrompt = executionState.durable.systemPrompt
		originalModel = cloneModelRef(executionState.durable.model)
	}
	defer func() {
		snapshot.Messages = transcript
		snapshot.SystemPrompt = originalSystemPrompt
		snapshot.Model = originalModel
	}()

	for {
		hasMoreToolCalls := true

		for hasMoreToolCalls || len(pendingMessages) > 0 {
			if !firstTurn {
				emitEvent(emit, AgentEvent{Type: EventTurnStart})
			}
			runtimeState.turnEnded = false
			firstTurn = false

			if definition.MaxTurns > 0 && turn > definition.MaxTurns {
				err := fmt.Errorf("%w: %d", ErrMaxTurnsExceeded, definition.MaxTurns)
				snapshot.Error = err.Error()
				return snapshot, newMessages, err
			}

			appended := appendMessagesWithEvents(snapshot, pendingMessages, emit)
			newMessages = append(newMessages, appended...)
			transcript = append(transcript, cloneMessages(appended)...)
			pendingMessages = nil

			resolvedDefinition, err := resolveLoopDefinition(ctx, hooks, definition, *snapshot)
			if err != nil {
				snapshot.Error = err.Error()
				return snapshot, newMessages, err
			}
			resolvedDefinition = overrides.apply(resolvedDefinition, snapshot)

			assistantMessage, tools, err := e.generateAssistant(ctx, resolvedDefinition, snapshot, emit)
			if err != nil {
				snapshot.Error = err.Error()
				return snapshot, newMessages, err
			}
			newMessages = append(newMessages, cloneMessage(assistantMessage))
			transcript = append(transcript, cloneMessage(assistantMessage))

			if isErrorAssistantMessage(assistantMessage) {
				reason := "tool call was not executed because assistant generation failed"
				if assistantMessage.StopReason == StopReasonAborted {
					reason = "tool call was not executed because the operation was aborted"
				}
				toolBatch := e.failUnexecutedToolCalls(snapshot, assistantMessage.ToolCalls, reason, emit)
				newMessages = append(newMessages, cloneMessages(toolBatch.messages)...)
				transcript = append(transcript, cloneMessages(toolBatch.messages)...)
				emitEvent(emit, AgentEvent{
					Type:         EventTurnEnd,
					Message:      &assistantMessage,
					ToolMessages: cloneMessages(toolBatch.messages),
				})
				runtimeState.turnEnded = true
				snapshot.Error = assistantMessage.ErrorMessage
				return snapshot, newMessages, nil
			}

			var toolBatch executedToolBatch
			if assistantMessage.StopReason == StopReasonLength && len(assistantMessage.ToolCalls) > 0 {
				toolBatch = e.failTruncatedToolCalls(snapshot, assistantMessage.ToolCalls, emit)
			} else {
				toolBatch, err = e.executeToolCalls(ctx, resolvedDefinition, snapshot, assistantMessage, tools, emit, hooks.ToolGate)
			}
			newMessages = append(newMessages, cloneMessages(toolBatch.messages)...)
			transcript = append(transcript, cloneMessages(toolBatch.messages)...)
			if isToolCallsSuspended(err) {
				setPendingToolControlState(snapshot, turn, assistantMessage)
				snapshot.Error = ""
				return snapshot, newMessages, err
			}
			emitEvent(emit, AgentEvent{
				Type:         EventTurnEnd,
				Message:      &assistantMessage,
				ToolMessages: cloneMessages(toolBatch.messages),
			})
			runtimeState.turnEnded = true
			if err != nil {
				snapshot.Error = err.Error()
				return snapshot, newMessages, err
			}
			turnContext := ShouldStopAfterTurnContext{
				Message:     cloneMessage(assistantMessage),
				ToolResults: cloneMessages(toolBatch.messages),
				Context:     buildAgentContext(resolvedDefinition, *snapshot, tools),
				NewMessages: cloneMessages(newMessages),
			}
			if resolvedDefinition.PrepareNextTurn != nil {
				update, prepareErr := resolvedDefinition.PrepareNextTurn(ctx, turnContext)
				if prepareErr != nil {
					snapshot.Error = prepareErr.Error()
					return snapshot, newMessages, prepareErr
				}
				if update != nil {
					overrides.merge(update, snapshot)
					resolvedDefinition = overrides.apply(resolvedDefinition, snapshot)
					if update.Context != nil {
						tools = cloneTools(update.Context.Tools)
					}
					turnContext.Context = buildAgentContext(resolvedDefinition, *snapshot, tools)
				}
			}

			if resolvedDefinition.ShouldStopAfterTurn != nil {
				stop, stopErr := resolvedDefinition.ShouldStopAfterTurn(ctx, turnContext)
				if stopErr != nil {
					snapshot.Error = stopErr.Error()
					return snapshot, newMessages, stopErr
				}
				if stop {
					snapshot.Error = ""
					return snapshot, newMessages, nil
				}
			}

			hasMoreToolCalls = len(assistantMessage.ToolCalls) > 0 && !toolBatch.terminate
			pendingMessages, err = dequeueHookMessages(ctx, hooks.GetSteeringMessages)
			if err != nil {
				snapshot.Error = err.Error()
				return snapshot, newMessages, err
			}
			turn++
		}

		followUpMessages, err := dequeueHookMessages(ctx, hooks.GetFollowUpMessages)
		if err != nil {
			snapshot.Error = err.Error()
			return snapshot, newMessages, err
		}
		if len(followUpMessages) > 0 {
			pendingMessages = followUpMessages
			continue
		}

		snapshot.Error = ""
		return snapshot, newMessages, nil
	}
}

func (e *Engine) generateAssistant(ctx context.Context, definition AgentDefinition, snapshot *AgentSnapshot, emit EventSink) (Message, []ToolDefinition, error) {
	modelMessages, err := definition.TransformContext(ctx, snapshot.Messages)
	if err != nil {
		return Message{}, nil, err
	}
	modelMessages, err = definition.ConvertToLLM(ctx, modelMessages)
	if err != nil {
		return Message{}, nil, err
	}

	model, modelRef, err := definition.ResolveModel(ctx, *snapshot)
	if err != nil {
		return Message{}, nil, err
	}
	tools, err := definition.ResolveTools(ctx, *snapshot)
	if err != nil {
		return Message{}, nil, err
	}

	systemPrompt := snapshot.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = definition.SystemPrompt
	}

	stream, err := model.Stream(ctx, ModelRequest{
		Model:           modelRef,
		SystemPrompt:    systemPrompt,
		Messages:        modelMessages,
		Tools:           tools,
		ThinkingLevel:   definition.ThinkingLevel,
		SessionID:       snapshot.SessionID,
		Transport:       definition.Transport,
		MaxRetryDelayMs: definition.MaxRetryDelayMs,
		ThinkingBudgets: cloneThinkingBudgets(definition.ThinkingBudgets),
	})
	if err != nil {
		return Message{}, nil, err
	}

	started := false
	var lastPartial Message
	var eventErr error
	events := stream.Events()
eventLoop:
	for {
		var event AssistantEvent
		var ok bool
		select {
		case <-ctx.Done():
			eventErr = ctx.Err()
			break eventLoop
		case event, ok = <-events:
			if !ok {
				break eventLoop
			}
		}

		partial := cloneMessage(event.Message)
		if partial.Role == "" {
			partial.Role = RoleAssistant
		}
		if partial.Timestamp.IsZero() {
			partial.Timestamp = time.Now().UTC()
		}
		lastPartial = cloneMessage(partial)

		if event.Type == AssistantEventStart {
			if !started {
				started = true
				emitEvent(emit, AgentEvent{Type: EventMessageStart, Message: &partial})
			}
			continue
		}
		if event.Type == AssistantEventDone || event.Type == AssistantEventError {
			continue
		}
		if !started {
			started = true
			emitEvent(emit, AgentEvent{Type: EventMessageStart, Message: &partial})
		}
		emitEvent(emit, AgentEvent{
			Type:         EventMessageUpdate,
			Message:      &partial,
			Delta:        event.Delta,
			UpdateType:   event.Type,
			ContentIndex: event.ContentIndex,
			Reason:       event.Reason,
			ToolCall:     cloneToolCallPtr(event.ToolCall),
			Err:          event.Err,
		})
	}

	closeErr := stream.Close()
	finalMessage, waitErr := stream.Wait()
	streamErr := errors.Join(eventErr, waitErr, closeErr)
	if streamErr != nil {
		if finalMessage.Role == "" && len(finalMessage.Parts) == 0 && len(finalMessage.ToolCalls) == 0 && started {
			finalMessage = cloneMessage(lastPartial)
		}
		if !isErrorAssistantMessage(finalMessage) {
			finalMessage.StopReason = StopReasonError
			if errors.Is(streamErr, context.Canceled) {
				finalMessage.StopReason = StopReasonAborted
			}
		}
		if finalMessage.ErrorMessage == "" || finalMessage.ErrorMessage == streamErr.Error() {
			finalMessage.ErrorMessage = streamErr.Error()
		} else {
			finalMessage.ErrorMessage += "\n" + streamErr.Error()
		}
	}
	finalMessage = cloneMessage(finalMessage)
	if finalMessage.Role == "" {
		finalMessage.Role = RoleAssistant
	}
	if finalMessage.Timestamp.IsZero() {
		finalMessage.Timestamp = time.Now().UTC()
	}
	snapshot.Messages = append(snapshot.Messages, finalMessage)

	if !started {
		emitEvent(emit, AgentEvent{Type: EventMessageStart, Message: &finalMessage})
	}
	emitEvent(emit, AgentEvent{Type: EventMessageEnd, Message: &finalMessage})
	return finalMessage, tools, nil
}

func (e *Engine) executeToolCalls(ctx context.Context, definition AgentDefinition, snapshot *AgentSnapshot, assistant Message, tools []ToolDefinition, emit EventSink, gate ToolGateHook) (executedToolBatch, error) {
	if gate != nil {
		return e.executeToolCallsGated(ctx, definition, snapshot, assistant, tools, emit, gate)
	}
	return e.executeToolCallsOrdinary(ctx, definition, snapshot, assistant, tools, emit)
}

func (e *Engine) executeToolCallsOrdinary(ctx context.Context, definition AgentDefinition, snapshot *AgentSnapshot, assistant Message, tools []ToolDefinition, emit EventSink) (executedToolBatch, error) {
	if len(assistant.ToolCalls) == 0 {
		return executedToolBatch{}, nil
	}
	toolMap := make(map[string]ToolDefinition, len(tools))
	for _, tool := range tools {
		toolMap[tool.Name] = tool
	}
	currentContext := buildAgentContext(definition, *snapshot, tools)
	snapshot.PendingToolCalls = make([]PendingToolCall, 0, len(assistant.ToolCalls))
	defer func() {
		snapshot.PendingToolCalls = nil
	}()

	sequential := definition.ToolExecution == ToolExecutionSequential
	if !sequential {
		for _, call := range assistant.ToolCalls {
			if tool, ok := toolMap[call.Name]; ok && tool.ExecutionMode == ToolExecutionSequential {
				sequential = true
				break
			}
		}
	}

	var (
		outcomes []toolOutcome
		err      error
	)
	if sequential {
		outcomes, err = e.executeToolCallsSequential(ctx, definition, snapshot, assistant, currentContext, toolMap, emit)
	} else {
		outcomes, err = e.executeToolCallsParallel(ctx, definition, snapshot, assistant, currentContext, toolMap, emit)
	}
	if ctx.Err() != nil && len(outcomes) < len(assistant.ToolCalls) {
		for _, original := range assistant.ToolCalls[len(outcomes):] {
			call := cloneToolCall(original)
			outcomes = append(outcomes, toolOutcome{
				call:    call,
				result:  errorToolResult("operation aborted before tool execution"),
				isError: true,
			})
		}
	}

	toolMessages := make([]Message, 0, len(outcomes))
	for _, outcome := range outcomes {
		toolMessage := NewToolResultMessage(outcome.call, outcome.result, outcome.isError)
		snapshot.Messages = append(snapshot.Messages, toolMessage)
		toolMessages = append(toolMessages, toolMessage)
		msg := cloneMessage(toolMessage)
		emitEvent(emit, AgentEvent{Type: EventMessageStart, Message: &msg})
		emitEvent(emit, AgentEvent{Type: EventMessageEnd, Message: &msg})
	}
	batch := executedToolBatch{
		messages:  toolMessages,
		terminate: shouldTerminateToolBatch(outcomes),
	}
	if err != nil {
		return batch, err
	}
	return batch, ctx.Err()
}

func (e *Engine) executeToolCallsGated(ctx context.Context, definition AgentDefinition, snapshot *AgentSnapshot, assistant Message, tools []ToolDefinition, emit EventSink, gate ToolGateHook) (executedToolBatch, error) {
	if len(assistant.ToolCalls) == 0 {
		return executedToolBatch{}, nil
	}
	if err := validateNormalizedToolCallIDs(assistant.ToolCalls); err != nil {
		return executedToolBatch{}, err
	}
	if _, err := pendingToolBindingDigest(assistant, 1); err != nil {
		return executedToolBatch{}, err
	}
	toolMap := make(map[string]ToolDefinition, len(tools))
	for _, tool := range tools {
		toolMap[tool.Name] = tool
	}
	currentContext := buildAgentContext(definition, *snapshot, tools)
	snapshot.PendingToolCalls = pendingToolCallsForAssistant(assistant)
	clearPending := true
	defer func() {
		if clearPending {
			snapshot.PendingToolCalls = nil
		}
	}()

	prepared := make([]preparedToolCall, 0, len(assistant.ToolCalls))
	suspended := make([]SuspendedToolCall, 0)
	for _, original := range assistant.ToolCalls {
		item, suspension, err := e.prepareGatedToolCall(ctx, definition, assistant, currentContext, toolMap, original, gate)
		if err != nil {
			return executedToolBatch{}, err
		}
		prepared = append(prepared, item)
		if suspension != nil {
			suspended = append(suspended, SuspendedToolCall{
				ToolCall:  cloneToolCall(suspension.ToolCall),
				Arguments: append(json.RawMessage(nil), suspension.Arguments...),
				Reason:    suspension.Reason,
			})
		}
	}
	if ctx.Err() != nil {
		return executedToolBatch{}, ctx.Err()
	}
	if len(suspended) > 0 {
		clearPending = false
		return executedToolBatch{}, &ToolCallsSuspendedError{Calls: suspended}
	}

	sequential := definition.ToolExecution == ToolExecutionSequential
	if !sequential {
		for _, item := range prepared {
			if item.tool.ExecutionMode == ToolExecutionSequential {
				sequential = true
				break
			}
		}
	}

	var (
		outcomes []toolOutcome
		err      error
	)
	if sequential {
		outcomes, err = e.executePreparedToolCallsSequential(ctx, definition, assistant, prepared, emit)
	} else {
		outcomes, err = e.executePreparedToolCallsParallel(ctx, definition, assistant, prepared, emit)
	}
	if ctx.Err() != nil && len(outcomes) < len(prepared) {
		for _, item := range prepared[len(outcomes):] {
			outcomes = append(outcomes, toolOutcome{
				call:    cloneToolCall(item.call),
				args:    cloneAny(item.args),
				result:  errorToolResult("operation aborted before tool execution"),
				isError: true,
			})
		}
	}

	toolMessages := make([]Message, 0, len(outcomes))
	for _, outcome := range outcomes {
		toolMessage := NewToolResultMessage(outcome.call, outcome.result, outcome.isError)
		snapshot.Messages = append(snapshot.Messages, toolMessage)
		toolMessages = append(toolMessages, toolMessage)
		msg := cloneMessage(toolMessage)
		emitEvent(emit, AgentEvent{Type: EventMessageStart, Message: &msg})
		emitEvent(emit, AgentEvent{Type: EventMessageEnd, Message: &msg})
	}
	batch := executedToolBatch{
		messages:  toolMessages,
		terminate: shouldTerminateToolBatch(outcomes),
	}
	if err != nil {
		return batch, err
	}
	return batch, ctx.Err()
}

func pendingToolCallsForAssistant(assistant Message) []PendingToolCall {
	pending := make([]PendingToolCall, 0, len(assistant.ToolCalls))
	for _, call := range assistant.ToolCalls {
		pending = append(pending, PendingToolCall{
			ToolCallID:         call.ID,
			OriginalToolCallID: call.OriginalID,
			ToolName:           call.Name,
		})
	}
	return pending
}

func (e *Engine) prepareGatedToolCall(ctx context.Context, definition AgentDefinition, assistant Message, currentContext AgentContext, tools map[string]ToolDefinition, original ToolCall, gate ToolGateHook) (preparedToolCall, *SuspendedToolCall, error) {
	call := cloneToolCall(original)
	if ctx.Err() != nil {
		return immediateToolCall(call, nil, errorToolResult("operation aborted"), true), nil, nil
	}

	tool, ok := tools[call.Name]
	if !ok {
		return immediateToolCall(call, nil, errorToolResult(fmt.Sprintf("tool %q not found", call.Name)), true), nil, nil
	}
	validator, err := newToolArgumentValidator(tool)
	if err != nil {
		return preparedToolCall{}, nil, err
	}
	args, err := parseToolArguments(tool, call)
	if err == nil {
		args, err = validator(args)
	}
	if err != nil {
		return immediateToolCall(call, args, errorToolResult(err.Error()), true), nil, nil
	}
	if ctx.Err() != nil {
		return immediateToolCall(call, args, errorToolResult("operation aborted"), true), nil, nil
	}

	executionArgs := cloneAny(args)
	if definition.BeforeToolCall != nil {
		beforeResult, beforeErr := definition.BeforeToolCall(ctx, BeforeToolCallContext{
			AssistantMessage: cloneMessage(assistant),
			ToolCall:         cloneToolCall(call),
			Args:             executionArgs,
			Context:          cloneAgentContext(currentContext),
		})
		if beforeErr != nil {
			return immediateToolCall(call, executionArgs, errorToolResult(beforeErr.Error()), true), nil, nil
		}
		if ctx.Err() != nil {
			return immediateToolCall(call, executionArgs, errorToolResult("operation aborted"), true), nil, nil
		}
		if beforeResult.Block {
			reason := beforeResult.Reason
			if reason == "" {
				reason = "tool execution was blocked"
			}
			result := errorToolResult(reason)
			result.Terminate = beforeResult.Terminate
			return immediateToolCall(call, executionArgs, result, true), nil, nil
		}
		executionArgs, err = validator(executionArgs)
		if err != nil {
			return immediateToolCall(call, executionArgs, errorToolResult(err.Error()), true), nil, nil
		}
	}
	if ctx.Err() != nil {
		return immediateToolCall(call, executionArgs, errorToolResult("operation aborted"), true), nil, nil
	}

	gateResult, gateErr := gate(ctx, BeforeToolCallContext{
		AssistantMessage: cloneMessage(assistant),
		ToolCall:         cloneToolCall(call),
		Args:             cloneAny(executionArgs),
		Context:          cloneAgentContext(currentContext),
	})
	if gateErr != nil {
		return preparedToolCall{}, nil, fmt.Errorf("agent: tool gate for %q failed: %w", call.Name, gateErr)
	}
	if ctx.Err() != nil {
		return immediateToolCall(call, executionArgs, errorToolResult("operation aborted"), true), nil, nil
	}
	switch gateResult.Action {
	case ToolGateActionAllow:
		if gateResult.Terminate {
			return preparedToolCall{}, nil, fmt.Errorf("agent: tool gate for %q cannot allow and terminate", call.Name)
		}
	case ToolGateActionBlock:
		reason := gateResult.Reason
		if reason == "" {
			reason = "tool execution was blocked"
		}
		result := errorToolResult(reason)
		result.Terminate = gateResult.Terminate
		return immediateToolCall(call, executionArgs, result, true), nil, nil
	case ToolGateActionSuspend:
		if gateResult.Terminate {
			return preparedToolCall{}, nil, fmt.Errorf("agent: tool gate for %q cannot suspend and terminate", call.Name)
		}
		canonicalArgs, marshalErr := json.Marshal(executionArgs)
		if marshalErr != nil {
			return preparedToolCall{}, nil, fmt.Errorf("agent: tool gate for %q cannot suspend non-JSON arguments: %w", call.Name, marshalErr)
		}
		return preparedToolCall{call: call, tool: tool, args: cloneAny(executionArgs), context: cloneAgentContext(currentContext)}, &SuspendedToolCall{
			ToolCall:  cloneToolCall(call),
			Arguments: canonicalArgs,
			Reason:    gateResult.Reason,
		}, nil
	default:
		return preparedToolCall{}, nil, fmt.Errorf("agent: tool gate for %q returned invalid action %q", call.Name, gateResult.Action)
	}

	if tool.Execute == nil {
		return immediateToolCall(call, executionArgs, errorToolResult(fmt.Sprintf("tool %q has no executor", call.Name)), true), nil, nil
	}
	return preparedToolCall{
		call:    call,
		tool:    tool,
		args:    cloneAny(executionArgs),
		context: cloneAgentContext(currentContext),
	}, nil, nil
}

func (e *Engine) executePreparedToolCallsSequential(ctx context.Context, definition AgentDefinition, assistant Message, prepared []preparedToolCall, emit EventSink) ([]toolOutcome, error) {
	outcomes := make([]toolOutcome, 0, len(prepared))
	for _, item := range prepared {
		if ctx.Err() != nil && !item.immediate {
			item = immediateToolCall(item.call, item.args, errorToolResult("operation aborted"), true)
		}
		emitToolExecutionStart(emit, item.call, item.args)
		outcome, err := e.executePreparedTool(ctx, definition, assistant, item, emit)
		if err != nil {
			return nil, err
		}
		outcomes = append(outcomes, outcome)
		if ctx.Err() != nil {
			break
		}
	}
	return outcomes, nil
}

func (e *Engine) executePreparedToolCallsParallel(ctx context.Context, definition AgentDefinition, assistant Message, prepared []preparedToolCall, emit EventSink) ([]toolOutcome, error) {
	if ctx.Err() != nil {
		for i := range prepared {
			if !prepared[i].immediate {
				prepared[i] = immediateToolCall(prepared[i].call, prepared[i].args, errorToolResult("operation aborted"), true)
			}
		}
	}

	outcomes := make([]toolOutcome, len(prepared))
	var (
		wg       sync.WaitGroup
		firstErr error
		errMu    sync.Mutex
	)
	for i, item := range prepared {
		emitToolExecutionStart(emit, item.call, item.args)
		if item.immediate {
			outcome, err := e.executePreparedTool(ctx, definition, assistant, item, emit)
			if err != nil {
				return nil, err
			}
			outcomes[i] = outcome
			continue
		}

		wg.Add(1)
		go func(index int, current preparedToolCall) {
			defer wg.Done()
			outcome, err := e.executePreparedTool(ctx, definition, assistant, current, emit)
			if err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
				return
			}
			outcomes[index] = outcome
		}(i, item)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return outcomes, nil
}

func (e *Engine) executeToolCallsSequential(ctx context.Context, definition AgentDefinition, snapshot *AgentSnapshot, assistant Message, currentContext AgentContext, tools map[string]ToolDefinition, emit EventSink) ([]toolOutcome, error) {
	outcomes := make([]toolOutcome, 0, len(assistant.ToolCalls))
	for _, call := range assistant.ToolCalls {
		prepared, aborted, err := e.prepareToolCall(ctx, definition, snapshot, assistant, currentContext, tools, call, emit)
		if err != nil {
			return nil, err
		}
		outcome, err := e.executePreparedTool(ctx, definition, assistant, prepared, emit)
		if err != nil {
			return nil, err
		}
		outcomes = append(outcomes, outcome)
		if aborted || ctx.Err() != nil {
			break
		}
	}
	return outcomes, nil
}

func (e *Engine) executeToolCallsParallel(ctx context.Context, definition AgentDefinition, snapshot *AgentSnapshot, assistant Message, currentContext AgentContext, tools map[string]ToolDefinition, emit EventSink) ([]toolOutcome, error) {
	prepared := make([]preparedToolCall, 0, len(assistant.ToolCalls))
	for _, call := range assistant.ToolCalls {
		item, aborted, err := e.prepareToolCall(ctx, definition, snapshot, assistant, currentContext, tools, call, emit)
		if err != nil {
			return nil, err
		}
		prepared = append(prepared, item)
		if aborted {
			break
		}
	}
	if ctx.Err() != nil {
		for i := range prepared {
			if prepared[i].immediate {
				continue
			}
			prepared[i] = immediateToolCall(
				prepared[i].call,
				prepared[i].args,
				errorToolResult("operation aborted"),
				true,
			)
		}
	}

	outcomes := make([]toolOutcome, len(prepared))
	var (
		wg       sync.WaitGroup
		firstErr error
		errMu    sync.Mutex
	)
	for i, item := range prepared {
		if item.immediate {
			outcome, err := e.executePreparedTool(ctx, definition, assistant, item, emit)
			if err != nil {
				return nil, err
			}
			outcomes[i] = outcome
			continue
		}

		wg.Add(1)
		go func(index int, current preparedToolCall) {
			defer wg.Done()
			outcome, err := e.executePreparedTool(ctx, definition, assistant, current, emit)
			if err != nil {
				errMu.Lock()
				if firstErr == nil {
					firstErr = err
				}
				errMu.Unlock()
				return
			}
			outcomes[index] = outcome
		}(i, item)
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return outcomes, nil
}

func (e *Engine) prepareToolCall(ctx context.Context, definition AgentDefinition, snapshot *AgentSnapshot, assistant Message, currentContext AgentContext, tools map[string]ToolDefinition, original ToolCall, emit EventSink) (preparedToolCall, bool, error) {
	call := cloneToolCall(original)
	snapshot.PendingToolCalls = append(snapshot.PendingToolCalls, PendingToolCall{
		ToolCallID:         call.ID,
		OriginalToolCallID: call.OriginalID,
		ToolName:           call.Name,
	})
	if ctx.Err() != nil {
		emitToolExecutionStart(emit, call, nil)
		return immediateToolCall(call, nil, errorToolResult("operation aborted"), true), true, nil
	}

	tool, ok := tools[call.Name]
	if !ok {
		emitToolExecutionStart(emit, call, nil)
		return immediateToolCall(call, nil, errorToolResult(fmt.Sprintf("tool %q not found", call.Name)), true), false, nil
	}

	validator, err := newToolArgumentValidator(tool)
	if err != nil {
		return preparedToolCall{}, false, err
	}
	args, err := parseToolArguments(tool, call)
	if err == nil {
		args, err = validator(args)
	}
	emitToolExecutionStart(emit, call, args)
	if err != nil {
		return immediateToolCall(call, args, errorToolResult(err.Error()), true), false, nil
	}
	if ctx.Err() != nil {
		return immediateToolCall(call, args, errorToolResult("operation aborted"), true), true, nil
	}

	executionArgs := cloneAny(args)
	if definition.BeforeToolCall != nil {
		beforeResult, err := definition.BeforeToolCall(ctx, BeforeToolCallContext{
			AssistantMessage: cloneMessage(assistant),
			ToolCall:         call,
			Args:             executionArgs,
			Context:          cloneAgentContext(currentContext),
		})
		if err != nil {
			return immediateToolCall(call, executionArgs, errorToolResult(err.Error()), true), false, nil
		}
		if ctx.Err() != nil {
			return immediateToolCall(call, executionArgs, errorToolResult("operation aborted"), true), true, nil
		}
		if beforeResult.Block {
			reason := beforeResult.Reason
			if reason == "" {
				reason = "tool execution was blocked"
			}
			result := errorToolResult(reason)
			result.Terminate = beforeResult.Terminate
			return immediateToolCall(call, executionArgs, result, true), false, nil
		}

		executionArgs, err = validator(executionArgs)
		if err != nil {
			return immediateToolCall(call, executionArgs, errorToolResult(err.Error()), true), false, nil
		}
	}
	if ctx.Err() != nil {
		return immediateToolCall(call, executionArgs, errorToolResult("operation aborted"), true), true, nil
	}
	if tool.Execute == nil {
		return immediateToolCall(call, executionArgs, errorToolResult(fmt.Sprintf("tool %q has no executor", call.Name)), true), false, nil
	}

	return preparedToolCall{
		call:    call,
		tool:    tool,
		args:    cloneAny(executionArgs),
		context: cloneAgentContext(currentContext),
	}, false, nil
}

func immediateToolCall(call ToolCall, args any, result ToolResult, isError bool) preparedToolCall {
	return preparedToolCall{
		call: call,
		outcome: toolOutcome{
			call:    call,
			args:    cloneAny(args),
			result:  result,
			isError: isError,
		},
		immediate: true,
	}
}

func emitToolExecutionStart(emit EventSink, call ToolCall, args any) {
	emitEvent(emit, AgentEvent{
		Type:               EventToolExecutionStart,
		ToolCall:           &call,
		ToolCallID:         call.ID,
		OriginalToolCallID: call.OriginalID,
		ToolName:           call.Name,
		Args:               cloneAny(args),
	})
}

func (e *Engine) executePreparedTool(ctx context.Context, definition AgentDefinition, assistant Message, prepared preparedToolCall, emit EventSink) (toolOutcome, error) {
	if prepared.immediate {
		emitEvent(emit, AgentEvent{
			Type:               EventToolExecutionEnd,
			ToolCall:           &prepared.outcome.call,
			ToolCallID:         prepared.outcome.call.ID,
			OriginalToolCallID: prepared.outcome.call.OriginalID,
			ToolName:           prepared.outcome.call.Name,
			Args:               cloneAny(prepared.outcome.args),
			ToolResult:         &prepared.outcome.result,
			IsError:            prepared.outcome.isError,
		})
		return prepared.outcome, nil
	}

	var updateMu sync.Mutex
	var inFlightUpdates sync.WaitGroup
	acceptingUpdates := true
	result, execErr := prepared.tool.Execute(ctx, prepared.call.ID, cloneAny(prepared.args), func(partial ToolResult) {
		updateMu.Lock()
		if !acceptingUpdates {
			updateMu.Unlock()
			return
		}
		inFlightUpdates.Add(1)
		updateMu.Unlock()
		defer inFlightUpdates.Done()
		emitEvent(emit, AgentEvent{
			Type:               EventToolExecutionUpdate,
			ToolCall:           &prepared.call,
			ToolCallID:         prepared.call.ID,
			OriginalToolCallID: prepared.call.OriginalID,
			ToolName:           prepared.call.Name,
			Args:               cloneAny(prepared.args),
			ToolResult:         &partial,
			PartialToolResult:  &partial,
		})
	})
	updateMu.Lock()
	acceptingUpdates = false
	updateMu.Unlock()
	inFlightUpdates.Wait()

	outcome := toolOutcome{call: prepared.call, args: cloneAny(prepared.args)}
	if execErr != nil {
		outcome.result = errorToolResult(execErr.Error())
		outcome.isError = true
	} else {
		outcome.result = cloneToolResult(result)
	}

	if definition.AfterToolCall != nil {
		override, err := definition.AfterToolCall(ctx, AfterToolCallContext{
			AssistantMessage: cloneMessage(assistant),
			ToolCall:         cloneToolCall(prepared.call),
			Args:             cloneAny(prepared.args),
			Context:          cloneAgentContext(prepared.context),
			Result:           cloneToolResult(outcome.result),
			IsError:          outcome.isError,
		})
		if err != nil {
			outcome.result = errorToolResult(err.Error())
			outcome.isError = true
		} else {
			if override.Result != nil {
				outcome.result = mergeToolResult(outcome.result, *override.Result)
			}
			if override.IsError != nil {
				outcome.isError = *override.IsError
			}
			if override.Terminate != nil {
				outcome.result.Terminate = *override.Terminate
			}
		}
	}

	emitEvent(emit, AgentEvent{
		Type:               EventToolExecutionEnd,
		ToolCall:           &outcome.call,
		ToolCallID:         outcome.call.ID,
		OriginalToolCallID: outcome.call.OriginalID,
		ToolName:           outcome.call.Name,
		Args:               cloneAny(outcome.args),
		ToolResult:         &outcome.result,
		IsError:            outcome.isError,
	})
	return outcome, nil
}

type preparedToolCall struct {
	call      ToolCall
	tool      ToolDefinition
	args      any
	context   AgentContext
	outcome   toolOutcome
	immediate bool
}

type toolOutcome struct {
	call    ToolCall
	args    any
	result  ToolResult
	isError bool
}

func emitEvent(emit EventSink, event AgentEvent) {
	if emit == nil {
		return
	}
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}
	emit(cloneAgentEvent(event))
}

func cloneAgentEvent(event AgentEvent) AgentEvent {
	cloned := event
	cloned.Message = cloneMessagePtr(event.Message)
	cloned.Messages = cloneMessages(event.Messages)
	cloned.ToolCall = cloneToolCallPtr(event.ToolCall)
	cloned.Args = cloneAny(event.Args)
	if event.ToolResult != nil {
		result := cloneToolResult(*event.ToolResult)
		cloned.ToolResult = &result
	}
	if event.PartialToolResult != nil {
		result := cloneToolResult(*event.PartialToolResult)
		cloned.PartialToolResult = &result
	}
	cloned.ToolMessages = cloneMessages(event.ToolMessages)
	return cloned
}

func errorToolResult(message string) ToolResult {
	return ToolResult{
		Content: []Part{{Type: PartTypeText, Text: message}},
	}
}

func normalizeMessages(messages []Message) []Message {
	normalized := make([]Message, 0, len(messages))
	for _, message := range messages {
		msg := cloneMessage(message)
		if msg.Timestamp.IsZero() {
			msg.Timestamp = time.Now().UTC()
		}
		normalized = append(normalized, msg)
	}
	return normalized
}

func cloneSnapshotPtr(snapshot *AgentSnapshot) *AgentSnapshot {
	if snapshot == nil {
		return &AgentSnapshot{}
	}
	value := cloneSnapshotValue(*snapshot)
	return &value
}

func cloneSnapshotValue(snapshot AgentSnapshot) AgentSnapshot {
	return AgentSnapshot{
		SessionID:          snapshot.SessionID,
		SystemPrompt:       snapshot.SystemPrompt,
		Model:              cloneModelRef(snapshot.Model),
		Messages:           cloneMessages(snapshot.Messages),
		PendingToolCalls:   clonePendingToolCalls(snapshot.PendingToolCalls),
		PendingToolControl: clonePendingToolControl(snapshot.PendingToolControl),
		Error:              snapshot.Error,
		Metadata:           cloneStringAnyMap(snapshot.Metadata),
	}
}

func cloneMessages(messages []Message) []Message {
	cloned := make([]Message, 0, len(messages))
	for _, message := range messages {
		cloned = append(cloned, cloneMessage(message))
	}
	return cloned
}

func cloneMessage(message Message) Message {
	return Message{
		ID:           message.ID,
		Role:         message.Role,
		Kind:         message.Kind,
		Parts:        cloneParts(message.Parts),
		ToolCalls:    cloneToolCalls(message.ToolCalls),
		ToolResult:   cloneToolResultPayload(message.ToolResult),
		Timestamp:    message.Timestamp,
		API:          message.API,
		Provider:     message.Provider,
		Model:        message.Model,
		ResponseID:   message.ResponseID,
		Metadata:     cloneStringAnyMap(message.Metadata),
		Payload:      cloneStringAnyMap(message.Payload),
		StopReason:   message.StopReason,
		ErrorMessage: message.ErrorMessage,
	}
}

func cloneParts(parts []Part) []Part {
	cloned := make([]Part, len(parts))
	copy(cloned, parts)
	return cloned
}

func cloneToolCalls(calls []ToolCall) []ToolCall {
	cloned := make([]ToolCall, 0, len(calls))
	for _, call := range calls {
		cloned = append(cloned, cloneToolCall(call))
	}
	return cloned
}

func cloneToolCall(call ToolCall) ToolCall {
	var arguments []byte
	if len(call.Arguments) > 0 {
		arguments = append(arguments, call.Arguments...)
	}
	return ToolCall{
		ID:               call.ID,
		OriginalID:       call.OriginalID,
		Name:             call.Name,
		Arguments:        arguments,
		ParsedArgs:       cloneStringAnyMap(call.ParsedArgs),
		ThoughtSignature: call.ThoughtSignature,
	}
}

func cloneToolCallPtr(call *ToolCall) *ToolCall {
	if call == nil {
		return nil
	}
	cloned := cloneToolCall(*call)
	return &cloned
}

func cloneToolResult(result ToolResult) ToolResult {
	return ToolResult{
		Content:   cloneParts(result.Content),
		Details:   cloneAny(result.Details),
		Terminate: result.Terminate,
	}
}

func mergeToolResult(base ToolResult, override ToolResult) ToolResult {
	merged := cloneToolResult(base)
	if override.Content != nil {
		merged.Content = cloneParts(override.Content)
	}
	if override.Details != nil {
		merged.Details = cloneAny(override.Details)
	}
	if override.Terminate {
		merged.Terminate = true
	}
	return merged
}

type executedToolBatch struct {
	messages  []Message
	terminate bool
}

func shouldTerminateToolBatch(outcomes []toolOutcome) bool {
	if len(outcomes) == 0 {
		return false
	}
	for _, outcome := range outcomes {
		if !outcome.result.Terminate {
			return false
		}
	}
	return true
}

func (e *Engine) failTruncatedToolCalls(snapshot *AgentSnapshot, calls []ToolCall, emit EventSink) executedToolBatch {
	messages := make([]Message, 0, len(calls))
	for _, original := range calls {
		call := cloneToolCall(original)
		emitToolExecutionStart(emit, call, nil)
		result := errorToolResult("tool call was not executed because the model response was truncated")
		emitEvent(emit, AgentEvent{
			Type:               EventToolExecutionEnd,
			ToolCall:           &call,
			ToolCallID:         call.ID,
			OriginalToolCallID: call.OriginalID,
			ToolName:           call.Name,
			ToolResult:         &result,
			IsError:            true,
		})
		message := NewToolResultMessage(call, result, true)
		snapshot.Messages = append(snapshot.Messages, message)
		messages = append(messages, message)
		msg := cloneMessage(message)
		emitEvent(emit, AgentEvent{Type: EventMessageStart, Message: &msg})
		emitEvent(emit, AgentEvent{Type: EventMessageEnd, Message: &msg})
	}
	return executedToolBatch{messages: messages}
}

func (e *Engine) failUnexecutedToolCalls(snapshot *AgentSnapshot, calls []ToolCall, reason string, emit EventSink) executedToolBatch {
	messages := make([]Message, 0, len(calls))
	for _, original := range calls {
		call := cloneToolCall(original)
		result := errorToolResult(reason)
		message := NewToolResultMessage(call, result, true)
		snapshot.Messages = append(snapshot.Messages, message)
		messages = append(messages, message)
		msg := cloneMessage(message)
		emitEvent(emit, AgentEvent{Type: EventMessageStart, Message: &msg})
		emitEvent(emit, AgentEvent{Type: EventMessageEnd, Message: &msg})
	}
	return executedToolBatch{messages: messages}
}

type turnOverrides struct {
	context       *AgentContext
	model         StreamModel
	modelRef      *ModelRef
	thinkingLevel *ThinkingLevel
}

func (o *turnOverrides) merge(update *AgentLoopTurnUpdate, snapshot *AgentSnapshot) {
	if update.Context != nil {
		context := cloneAgentContext(*update.Context)
		o.context = &context
		snapshot.SystemPrompt = context.SystemPrompt
		snapshot.Messages = cloneMessages(context.Messages)
	}
	if update.Model != nil {
		o.model = update.Model
	}
	if update.ModelRef != nil {
		modelRef := cloneModelRef(*update.ModelRef)
		o.modelRef = &modelRef
		snapshot.Model = cloneModelRef(modelRef)
	}
	if update.ThinkingLevel != nil {
		thinkingLevel := *update.ThinkingLevel
		o.thinkingLevel = &thinkingLevel
	}
}

func (o turnOverrides) apply(definition AgentDefinition, snapshot *AgentSnapshot) AgentDefinition {
	if o.context != nil {
		definition.SystemPrompt = o.context.SystemPrompt
		definition.Tools = cloneTools(o.context.Tools)
		definition.ToolResolver = nil
		snapshot.SystemPrompt = o.context.SystemPrompt
	}
	if o.model != nil {
		definition.Model = o.model
		definition.ModelResolver = nil
	}
	if o.modelRef != nil {
		snapshot.Model = cloneModelRef(*o.modelRef)
		definition.DefaultModel = cloneModelRef(*o.modelRef)
	}
	if o.thinkingLevel != nil {
		definition.ThinkingLevel = *o.thinkingLevel
	}
	return definition
}

func cloneToolResultPayload(payload *ToolResultPayload) *ToolResultPayload {
	if payload == nil {
		return nil
	}
	cloned := *payload
	cloned.Content = cloneParts(payload.Content)
	cloned.Details = cloneAny(payload.Details)
	return &cloned
}

func clonePendingToolCalls(calls []PendingToolCall) []PendingToolCall {
	cloned := make([]PendingToolCall, len(calls))
	copy(cloned, calls)
	return cloned
}

func clonePendingToolControl(control *PendingToolControl) *PendingToolControl {
	if control == nil {
		return nil
	}
	cloned := *control
	return &cloned
}

func cloneStringAnyMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	cloned := make(map[string]any, len(input))
	for key, value := range input {
		cloned[key] = cloneAny(value)
	}
	return cloned
}

func cloneTools(tools []ToolDefinition) []ToolDefinition {
	cloned := make([]ToolDefinition, len(tools))
	for i, tool := range tools {
		cloned[i] = tool
		cloned[i].Parameters = cloneStringAnyMap(tool.Parameters)
	}
	return cloned
}

func cloneModelRef(ref ModelRef) ModelRef {
	return ModelRef{
		Provider:       ref.Provider,
		Model:          ref.Model,
		ProviderConfig: cloneProviderConfig(ref.ProviderConfig),
		Metadata:       cloneStringAnyMap(ref.Metadata),
	}
}

func cloneProviderConfig(config ProviderConfig) ProviderConfig {
	return ProviderConfig{
		BaseURL: config.BaseURL,
		APIKey:  config.APIKey,
		Headers: cloneStringMap(config.Headers),
		Auth:    cloneProviderAuthConfig(config.Auth),
	}
}

func cloneProviderAuthConfig(config *ProviderAuthConfig) *ProviderAuthConfig {
	if config == nil {
		return nil
	}

	cloned := *config
	cloned.OAuth = cloneOAuthCredentials(config.OAuth)
	return &cloned
}

func cloneOAuthCredentials(credentials *OAuthCredentials) *OAuthCredentials {
	if credentials == nil {
		return nil
	}

	cloned := *credentials
	return &cloned
}

func cloneStringMap(input map[string]string) map[string]string {
	if len(input) == 0 {
		return nil
	}

	cloned := make(map[string]string, len(input))
	for key, value := range input {
		cloned[key] = value
	}
	return cloned
}

func cloneThinkingBudgets(budgets ThinkingBudgets) ThinkingBudgets {
	if len(budgets) == 0 {
		return nil
	}
	cloned := make(ThinkingBudgets, len(budgets))
	for level, budget := range budgets {
		cloned[level] = budget
	}
	return cloned
}

func resolveLoopDefinition(ctx context.Context, hooks LoopHooks, definition AgentDefinition, snapshot AgentSnapshot) (AgentDefinition, error) {
	if hooks.ResolveDefinition == nil {
		return definition, nil
	}

	resolved, err := hooks.ResolveDefinition(ctx, definition, cloneSnapshotValue(snapshot))
	if err != nil {
		return AgentDefinition{}, err
	}
	return resolved.Validate()
}

func dequeueHookMessages(ctx context.Context, getter func(context.Context) ([]Message, error)) ([]Message, error) {
	if getter == nil {
		return nil, nil
	}

	messages, err := getter(ctx)
	if err != nil {
		return nil, err
	}
	return normalizeMessages(messages), nil
}

func appendMessagesWithEvents(snapshot *AgentSnapshot, messages []Message, emit EventSink) []Message {
	if len(messages) == 0 {
		return nil
	}

	normalized := normalizeMessages(messages)
	snapshot.Messages = append(snapshot.Messages, normalized...)
	for i := range normalized {
		msg := cloneMessage(normalized[i])
		emitEvent(emit, AgentEvent{Type: EventMessageStart, Message: &msg})
		emitEvent(emit, AgentEvent{Type: EventMessageEnd, Message: &msg})
	}
	return normalized
}

func buildAgentContext(definition AgentDefinition, snapshot AgentSnapshot, tools []ToolDefinition) AgentContext {
	systemPrompt := snapshot.SystemPrompt
	if systemPrompt == "" {
		systemPrompt = definition.SystemPrompt
	}
	return AgentContext{
		SystemPrompt: systemPrompt,
		Messages:     cloneMessages(snapshot.Messages),
		Tools:        cloneTools(tools),
	}
}

func cloneAgentContext(context AgentContext) AgentContext {
	return AgentContext{
		SystemPrompt: context.SystemPrompt,
		Messages:     cloneMessages(context.Messages),
		Tools:        cloneTools(context.Tools),
	}
}

func parseToolArguments(tool ToolDefinition, call ToolCall) (any, error) {
	if tool.ParseArguments != nil {
		return tool.ParseArguments(cloneToolCall(call))
	}
	if call.ParsedArgs != nil {
		return cloneStringAnyMap(call.ParsedArgs), nil
	}
	if len(call.Arguments) == 0 {
		return map[string]any{}, nil
	}

	decoder := json.NewDecoder(bytes.NewReader(call.Arguments))
	decoder.UseNumber()
	var parsed any
	if err := decoder.Decode(&parsed); err != nil {
		return nil, err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("multiple JSON argument values")
		}
		return nil, err
	}
	return cloneAny(parsed), nil
}

func isErrorAssistantMessage(message Message) bool {
	return message.StopReason == StopReasonError || message.StopReason == StopReasonAborted || message.ErrorMessage != ""
}

func cloneAny(value any) any {
	cloned := cloneReflectValue(reflect.ValueOf(value), make(map[cloneVisit]reflect.Value))
	if !cloned.IsValid() {
		return nil
	}
	return cloned.Interface()
}

type cloneVisit struct {
	typeOf   reflect.Type
	pointer  uintptr
	length   int
	capacity int
}

func cloneReflectValue(value reflect.Value, visited map[cloneVisit]reflect.Value) reflect.Value {
	if !value.IsValid() {
		return reflect.Value{}
	}

	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := cloneReflectValue(value.Elem(), visited)
		result := reflect.New(value.Type()).Elem()
		result.Set(cloned)
		return result
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		visit := cloneVisit{typeOf: value.Type(), pointer: value.Pointer()}
		if cloned, ok := visited[visit]; ok {
			return cloned
		}
		result := reflect.New(value.Type().Elem())
		visited[visit] = result
		result.Elem().Set(cloneReflectValue(value.Elem(), visited))
		return result
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		visit := cloneVisit{typeOf: value.Type(), pointer: value.Pointer()}
		if cloned, ok := visited[visit]; ok {
			return cloned
		}
		result := reflect.MakeMapWithSize(value.Type(), value.Len())
		visited[visit] = result
		iterator := value.MapRange()
		for iterator.Next() {
			result.SetMapIndex(iterator.Key(), cloneReflectValue(iterator.Value(), visited))
		}
		return result
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		visit := cloneVisit{typeOf: value.Type(), pointer: value.Pointer(), length: value.Len(), capacity: value.Cap()}
		if cloned, ok := visited[visit]; ok {
			return cloned
		}
		result := reflect.MakeSlice(value.Type(), value.Len(), value.Cap())
		visited[visit] = result
		for i := 0; i < value.Len(); i++ {
			result.Index(i).Set(cloneReflectValue(value.Index(i), visited))
		}
		return result
	case reflect.Array:
		result := reflect.New(value.Type()).Elem()
		for i := 0; i < value.Len(); i++ {
			result.Index(i).Set(cloneReflectValue(value.Index(i), visited))
		}
		return result
	case reflect.Struct:
		result := reflect.New(value.Type()).Elem()
		result.Set(value)
		for i := 0; i < value.NumField(); i++ {
			if result.Field(i).CanSet() && value.Type().Field(i).IsExported() {
				result.Field(i).Set(cloneReflectValue(value.Field(i), visited))
			}
		}
		return result
	default:
		return value
	}
}
