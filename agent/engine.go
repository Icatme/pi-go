package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"time"
)

// LoopHooks customize runtime behavior between turns.
type LoopHooks struct {
	ResolveDefinition   func(context.Context, AgentDefinition, AgentSnapshot) (AgentDefinition, error)
	GetSteeringMessages func(context.Context) ([]Message, error)
	GetFollowUpMessages func(context.Context) ([]Message, error)
}

// Engine executes agent turns against a mutable snapshot.
type Engine struct{}

type loopRuntimeState struct {
	turnEnded bool
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
	if len(prompts) == 0 {
		return nil, ErrNoPromptMessages
	}

	definition, err = definition.Validate()
	if err != nil {
		return nil, err
	}

	next := cloneSnapshotPtr(snapshot)
	emitEvent(emit, AgentEvent{Type: EventAgentStart})
	emitEvent(emit, AgentEvent{Type: EventTurnStart})
	normalized := normalizeMessages(prompts)
	next.Messages = append(next.Messages, normalized...)
	for i := range normalized {
		msg := cloneMessage(normalized[i])
		emitEvent(emit, AgentEvent{Type: EventMessageStart, Message: &msg})
		emitEvent(emit, AgentEvent{Type: EventMessageEnd, Message: &msg})
	}

	initialPending, err := dequeueHookMessages(ctx, hooks.GetSteeringMessages)
	if err != nil {
		next.Error = err.Error()
		return next, wrapEngineRunError(err, normalized, false)
	}
	runtimeState := &loopRuntimeState{}
	var newMessages []Message
	next, newMessages, err = e.runLoop(ctx, definition, next, emit, hooks, initialPending, normalized, runtimeState)
	if err != nil {
		return next, wrapEngineRunError(err, newMessages, runtimeState.turnEnded)
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
	if len(next.Messages) == 0 {
		return nil, ErrNoMessagesToContinue
	}
	if tail := next.Messages[len(next.Messages)-1]; tail.Role == RoleAssistant {
		return nil, ErrCannotContinueFromAssistant
	}

	emitEvent(emit, AgentEvent{Type: EventAgentStart})
	emitEvent(emit, AgentEvent{Type: EventTurnStart})

	initialPending, err := dequeueHookMessages(ctx, hooks.GetSteeringMessages)
	if err != nil {
		next.Error = err.Error()
		return next, wrapEngineRunError(err, nil, false)
	}

	runtimeState := &loopRuntimeState{}
	var newMessages []Message
	next, newMessages, err = e.runLoop(ctx, definition, next, emit, hooks, initialPending, nil, runtimeState)
	if err != nil {
		return next, wrapEngineRunError(err, newMessages, runtimeState.turnEnded)
	}
	emitEvent(emit, AgentEvent{Type: EventAgentEnd, Messages: cloneMessages(newMessages)})
	return next, nil
}

func (e *Engine) runLoop(ctx context.Context, definition AgentDefinition, snapshot *AgentSnapshot, emit EventSink, hooks LoopHooks, pendingMessages []Message, initialNewMessages []Message, runtimeState *loopRuntimeState) (*AgentSnapshot, []Message, error) {
	firstTurn := true
	turn := 1
	var overrides turnOverrides
	newMessages := cloneMessages(initialNewMessages)
	transcript := cloneMessages(snapshot.Messages)
	originalSystemPrompt := snapshot.SystemPrompt
	originalModel := cloneModelRef(snapshot.Model)
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
				toolBatch, err = e.executeToolCalls(ctx, resolvedDefinition, snapshot, assistantMessage, tools, emit)
			}
			newMessages = append(newMessages, cloneMessages(toolBatch.messages)...)
			transcript = append(transcript, cloneMessages(toolBatch.messages)...)
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
	for event := range stream.Events() {
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
		assistantEvent := event
		emitEvent(emit, AgentEvent{
			Type:           EventMessageUpdate,
			Message:        &partial,
			Delta:          event.Delta,
			AssistantEvent: &assistantEvent,
			ToolCall:       cloneToolCallPtr(event.ToolCall),
			Err:            event.Err,
		})
	}

	finalMessage, err := stream.Wait()
	if err != nil && !isErrorAssistantMessage(finalMessage) {
		if finalMessage.Role == "" && len(finalMessage.Parts) == 0 && len(finalMessage.ToolCalls) == 0 && started {
			finalMessage = cloneMessage(lastPartial)
		}
		finalMessage.StopReason = StopReasonError
		if ctx.Err() == context.Canceled {
			finalMessage.StopReason = StopReasonAborted
		}
		finalMessage.ErrorMessage = err.Error()
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

func (e *Engine) executeToolCalls(ctx context.Context, definition AgentDefinition, snapshot *AgentSnapshot, assistant Message, tools []ToolDefinition, emit EventSink) (executedToolBatch, error) {
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
	if event.AssistantEvent != nil {
		assistantEvent := *event.AssistantEvent
		assistantEvent.Message = cloneMessage(event.AssistantEvent.Message)
		assistantEvent.ToolCall = cloneToolCallPtr(event.AssistantEvent.ToolCall)
		cloned.AssistantEvent = &assistantEvent
	}
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
		SessionID:        snapshot.SessionID,
		SystemPrompt:     snapshot.SystemPrompt,
		Model:            cloneModelRef(snapshot.Model),
		Messages:         cloneMessages(snapshot.Messages),
		PendingToolCalls: clonePendingToolCalls(snapshot.PendingToolCalls),
		Error:            snapshot.Error,
		Metadata:         cloneStringAnyMap(snapshot.Metadata),
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
