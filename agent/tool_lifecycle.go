package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
)

func preparedFailure(call ToolCall, args any, err error, code ToolFailureCode) preparedToolCall {
	outcome := rejectedToolOutcome(call, err, code)
	outcome.args = cloneAny(args)
	return preparedToolCall{call: cloneToolCall(call), args: cloneAny(args), outcome: outcome, immediate: true}
}

// prepareToolInvocation is shared by model batches, direct calls and children.
// Only a model batch interprets suspension as a durable pending batch.
func (e *Engine) prepareToolInvocation(ctx context.Context, definition AgentDefinition, assistant Message, currentContext AgentContext, tools map[string]ToolDefinition, original ToolCall, gate ToolGateHook, start EventSink) (preparedToolCall, *SuspendedToolCall, error) {
	call := cloneToolCall(original)
	fail := func(args any, err error, code ToolFailureCode) (preparedToolCall, *SuspendedToolCall, error) {
		return preparedFailure(call, args, err, code), nil, nil
	}
	if err := ctx.Err(); err != nil {
		emitToolExecutionStart(start, call, nil)
		return fail(nil, err, ToolFailureCanceled)
	}
	tool, ok := tools[call.Name]
	if !ok {
		emitToolExecutionStart(start, call, nil)
		return fail(nil, fmt.Errorf("tool %q not found", call.Name), ToolFailurePolicyDenied)
	}
	validator, err := newToolArgumentValidator(tool)
	if err != nil {
		return preparedToolCall{}, nil, err
	}
	args, err := parseToolArguments(tool, call)
	if err == nil {
		args, err = validator(args)
	}
	emitToolExecutionStart(start, call, args)
	if err != nil {
		return fail(args, err, ToolFailureArgumentInvalid)
	}
	if err := ctx.Err(); err != nil {
		return fail(args, err, ToolFailureCanceled)
	}
	executionArgs := cloneAny(args)
	if definition.BeforeToolCall != nil {
		before, beforeErr := definition.BeforeToolCall(ctx, toolHookContext(ctx, assistant, call, executionArgs, currentContext))
		if beforeErr != nil {
			return fail(executionArgs, beforeErr, ToolFailureHook)
		}
		if err := ctx.Err(); err != nil {
			return fail(executionArgs, err, ToolFailureCanceled)
		}
		if before.Block {
			reason := before.Reason
			if reason == "" {
				reason = "tool execution was blocked"
			}
			item := preparedFailure(call, executionArgs, errors.New(reason), ToolFailurePolicyDenied)
			item.outcome.result.Terminate = before.Terminate
			return item, nil, nil
		}
		executionArgs, err = validator(executionArgs)
		if err != nil {
			return fail(executionArgs, err, ToolFailureArgumentInvalid)
		}
	}
	if err := ctx.Err(); err != nil {
		return fail(executionArgs, err, ToolFailureCanceled)
	}
	prepared := preparedToolCall{call: call, tool: tool, args: cloneAny(executionArgs), context: cloneAgentContext(currentContext), gate: gate}
	if gate != nil {
		decision, gateErr := gate(ctx, toolHookContext(ctx, assistant, call, cloneAny(executionArgs), currentContext))
		if gateErr != nil {
			return preparedToolCall{}, nil, fmt.Errorf("agent: tool gate for %q failed: %w", call.Name, gateErr)
		}
		if err := ctx.Err(); err != nil {
			return fail(executionArgs, err, ToolFailureCanceled)
		}
		switch decision.Action {
		case ToolGateActionAllow:
			if decision.Terminate {
				return preparedToolCall{}, nil, fmt.Errorf("agent: tool gate for %q cannot allow and terminate", call.Name)
			}
		case ToolGateActionBlock:
			reason := decision.Reason
			if reason == "" {
				reason = "tool execution was blocked"
			}
			item := preparedFailure(call, executionArgs, errors.New(reason), ToolFailurePolicyDenied)
			item.outcome.result.Terminate = decision.Terminate
			return item, nil, nil
		case ToolGateActionSuspend:
			if decision.Terminate {
				return preparedToolCall{}, nil, fmt.Errorf("agent: tool gate for %q cannot suspend and terminate", call.Name)
			}
			encoded, err := json.Marshal(executionArgs)
			if err != nil {
				return preparedToolCall{}, nil, fmt.Errorf("agent: tool gate for %q cannot suspend non-JSON arguments: %w", call.Name, err)
			}
			return prepared, &SuspendedToolCall{ToolCall: cloneToolCall(call), Arguments: encoded, Reason: decision.Reason}, nil
		default:
			return preparedToolCall{}, nil, fmt.Errorf("agent: tool gate for %q returned invalid action %q", call.Name, decision.Action)
		}
	}
	if tool.Execute == nil {
		return fail(executionArgs, fmt.Errorf("tool %q has no executor", call.Name), ToolFailureProtocol)
	}
	return prepared, nil, nil
}

func toolHookContext(ctx context.Context, assistant Message, call ToolCall, args any, current AgentContext) BeforeToolCallContext {
	return BeforeToolCallContext{AssistantMessage: cloneMessage(assistant), ToolCall: cloneToolCall(call), Args: args,
		Context: cloneAgentContext(current), ParentToolCallID: childParentID(ctx)}
}

func (e *Engine) executePreparedTool(ctx context.Context, definition AgentDefinition, assistant Message, prepared preparedToolCall, emit EventSink) (toolOutcome, error) {
	if prepared.immediate {
		outcome := prepared.outcome
		if outcome.execution.Local == "" {
			outcome.execution = ToolExecutionInfo{Local: ToolLocalNotStarted, Remote: ToolRemoteNotDispatched}
		}
		emitToolOutcome(emit, prepared, outcome)
		return outcome, nil
	}
	checkPermission := func(checkCtx context.Context) error {
		if err := checkCtx.Err(); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if definition.CheckToolPermission == nil {
			return nil
		}
		return definition.CheckToolPermission(checkCtx, toolHookContext(ctx, assistant, prepared.call, cloneAny(prepared.args), prepared.context))
	}
	if err := checkPermission(ctx); err != nil {
		outcome := rejectedToolOutcome(prepared.call, err, ToolFailurePolicyDenied)
		outcome.args = cloneAny(prepared.args)
		emitToolOutcome(emit, prepared, outcome)
		return outcome, nil
	}
	var children *childCallScope
	if prepared.tool.ChildTools != nil {
		children = newChildCallScope(ctx, e, definition, assistant, prepared, emit)
	}
	var updateMu sync.Mutex
	var updates sync.WaitGroup
	acceptUpdates := true
	onUpdate := func(partial ToolResult) {
		partial.ChildCalls = nil
		updateMu.Lock()
		if !acceptUpdates {
			updateMu.Unlock()
			return
		}
		updates.Add(1)
		updateMu.Unlock()
		defer updates.Done()
		if prepared.child {
			// Project before clone/enqueue: raw args, partial content and Details
			// never enter the default child event stream.
			emitEvent(emit, AgentEvent{Type: EventToolExecutionUpdate, ToolCallID: prepared.call.ID,
				ToolName: prepared.call.Name, ParentToolCallID: prepared.parentID,
				Execution: &ToolExecutionInfo{Local: ToolLocalEntered, Remote: ToolRemoteUnknown}})
			return
		}
		emitEvent(emit, AgentEvent{Type: EventToolExecutionUpdate, ToolCall: &prepared.call,
			ToolCallID: prepared.call.ID, OriginalToolCallID: prepared.call.OriginalID, ToolName: prepared.call.Name,
			Args: prepared.args, ToolResult: &partial, PartialToolResult: &partial})
	}
	executionContext := ToolExecutionContext{ToolCall: cloneToolCall(prepared.call), Args: cloneAny(prepared.args),
		Context: cloneAgentContext(prepared.context), OnUpdate: onUpdate, CheckPermission: checkPermission}
	if children != nil {
		executionContext.ChildCaller = children
		executionContext.ChildSequential = children.limits.MaxParallel == 1
		executionContext.Invocation = toolInvocationFromContext(ctx)
	}
	if prepared.onEntered != nil {
		prepared.onEntered()
	}
	result, execErr := prepared.tool.Execute(ctx, executionContext)
	// Only the bound scope owns child facts. Executors and hooks cannot supply
	// or replace this sidecar, including ordinary leaf tools.
	result.ChildCalls = nil
	if children != nil {
		children.close()
	}
	updateMu.Lock()
	acceptUpdates = false
	updateMu.Unlock()
	updates.Wait()
	outcome := toolOutcome{call: prepared.call, args: cloneAny(prepared.args), execution: ToolExecutionInfo{Local: ToolLocalReturned, Remote: ToolRemoteNotApplicable}}
	if result.Execution != nil {
		outcome.execution = *cloneToolExecutionInfo(result.Execution)
		outcome.execution.Local = ToolLocalReturned
	}
	if execErr != nil {
		outcome.execution.Remote = ToolRemoteUnknown
		var typed *ToolExecutionError
		if errors.As(execErr, &typed) && typed.Execution.Remote != "" {
			outcome.execution = *cloneToolExecutionInfo(&typed.Execution)
			outcome.execution.Local = ToolLocalReturned
		}
		outcome.err = newToolFailure(execErr, ToolFailureProtocol, outcome.execution)
		var failure *ToolExecutionError
		errors.As(outcome.err, &failure)
		outcome.failure = &ToolFailure{Code: failure.Code, Reason: failure.Reason}
		outcome.result = boundedErrorResult(result, execErr.Error())
		outcome.isError = true
	} else {
		if prepared.child && !valueFitsBudget(result, prepared.toolResultLimit()) {
			setOutcomeFailure(&outcome, errors.New("child tool result exceeds byte limit"), ToolFailureResource)
		} else {
			outcome.result = cloneToolResult(result)
			outcome.isError = result.IsError
			if result.IsError {
				outcome.failure = &ToolFailure{Code: ToolFailureToolReportedError}
			}
		}
	}
	if len(outcome.result.StructuredContent) > 0 && !json.Valid(outcome.result.StructuredContent) {
		setOutcomeFailure(&outcome, errors.New("tool returned invalid structured content JSON"), ToolFailureResultRejected)
	}
	if definition.AfterToolCall != nil {
		override, hookErr := definition.AfterToolCall(ctx, AfterToolCallContext{AssistantMessage: cloneMessage(assistant),
			ToolCall: cloneToolCall(prepared.call), Args: cloneAny(prepared.args), Context: cloneAgentContext(prepared.context),
			Result: cloneToolResult(outcome.result), IsError: outcome.isError, Err: outcome.err,
			Execution: *cloneToolExecutionInfo(&outcome.execution), Failure: cloneToolFailure(outcome.failure), ParentToolCallID: prepared.parentID})
		if hookErr != nil {
			// Preserve the execution chain, but discard all unprocessed output.
			setOutcomeFailure(&outcome, hookErr, ToolFailureHook)
		} else {
			if override.Result != nil {
				candidate := effectiveResultCandidate(outcome.result, *override.Result)
				if prepared.child && !valueFitsBudget(candidate, prepared.toolResultLimit()) {
					setOutcomeFailure(&outcome, errors.New("after-tool child result exceeds byte limit"), ToolFailureResource)
				} else {
					outcome.result = mergeToolResult(outcome.result, *override.Result)
					outcome.isError = outcome.isError || override.Result.IsError
				}
			}
			if override.IsError != nil {
				outcome.isError = *override.IsError
			}
			if override.Terminate != nil {
				outcome.result.Terminate = *override.Terminate
			}
		}
	}
	if len(outcome.result.StructuredContent) > 0 && !json.Valid(outcome.result.StructuredContent) {
		setOutcomeFailure(&outcome, errors.New("after-tool hook returned invalid structured content JSON"), ToolFailureResultRejected)
	}
	if outcome.err == nil {
		// Business failure reflects the effective hook result. Infrastructure
		// errors and execution facts remain immutable regardless of IsError.
		outcome.failure = nil
		if outcome.isError {
			outcome.failure = &ToolFailure{Code: ToolFailureToolReportedError}
		}
	}
	if prepared.child && outcome.err == nil && !outcome.isError {
		if err := validateToolOutput(prepared.tool, outcome.result); err != nil {
			setOutcomeFailure(&outcome, err, ToolFailureResultRejected)
		}
	}
	if outcome.err == nil && prepared.tool.ValidateResult != nil {
		effective := cloneToolResult(outcome.result)
		effective.IsError = outcome.isError
		effective.Execution = cloneToolExecutionInfo(&outcome.execution)
		effective.Failure = cloneToolFailure(outcome.failure)
		if err := prepared.tool.ValidateResult(effective); err != nil {
			setOutcomeFailure(&outcome, err, ToolFailureResultRejected)
		}
	}
	if outcome.err != nil {
		outcome.isError = true
	}
	outcome.result.IsError = outcome.isError
	outcome.result.Execution = cloneToolExecutionInfo(&outcome.execution)
	outcome.result.Failure = cloneToolFailure(outcome.failure)
	outcome.result.ChildCalls = nil
	if children != nil {
		report := children.Report()
		outcome.result.ChildCalls = &report
	}
	emitToolOutcome(emit, prepared, outcome)
	return outcome, nil
}

func effectiveResultCandidate(base, override ToolResult) ToolResult {
	if override.Content != nil {
		base.Content = override.Content
		base.StructuredContent = nil
	}
	if override.StructuredContent != nil {
		base.StructuredContent = override.StructuredContent
	}
	if override.Details != nil {
		base.Details = override.Details
	}
	return base
}

func setOutcomeFailure(outcome *toolOutcome, err error, code ToolFailureCode) {
	combined := err
	if outcome.err != nil {
		combined = errors.Join(err, outcome.err)
	}
	reason := ""
	var typed *ToolExecutionError
	if errors.As(err, &typed) {
		reason = typed.Reason
	}
	outcome.err = &ToolExecutionError{Code: code, Reason: reason, Message: err.Error(), Err: combined, Execution: outcome.execution}
	outcome.failure = &ToolFailure{Code: code, Reason: reason}
	outcome.result = errorToolResult(err.Error())
	outcome.isError = true
}

func boundedErrorResult(result ToolResult, message string) ToolResult {
	bounded := ToolResult{IsError: true, Terminate: result.Terminate}
	// Go-only execution reports remain useful on failure, but an untrusted
	// arbitrary Details value does not get an unlimited copy.
	if valueFitsBudget(result.Details, 1<<20) {
		bounded.Details = cloneAny(result.Details)
	}
	remaining := 1 << 20
	for _, part := range result.Content {
		if remaining == 0 {
			continue
		}
		switch part.Type {
		case PartTypeText:
			text := boundedUTF8(part.Text, remaining)
			remaining -= len(text)
			bounded.Content = append(bounded.Content, Part{Type: PartTypeText, Text: text})
		case PartTypeImage:
			if len(part.Data)+len(part.MIMEType) <= remaining {
				bounded.Content = append(bounded.Content, Part{Type: PartTypeImage, Data: strings.Clone(part.Data), MIMEType: strings.Clone(part.MIMEType)})
				remaining -= len(part.Data) + len(part.MIMEType)
			}
		}
	}
	if len(result.StructuredContent) <= 1<<20 {
		bounded.StructuredContent = append(json.RawMessage(nil), result.StructuredContent...)
	}
	if len(bounded.Content) == 0 {
		bounded.Content = []Part{{Type: PartTypeText, Text: boundedUTF8(message, 8192)}}
	}
	return bounded
}

func emitToolOutcome(emit EventSink, prepared preparedToolCall, outcome toolOutcome) {
	event := AgentEvent{Type: EventToolExecutionEnd, ToolCallID: outcome.call.ID, OriginalToolCallID: outcome.call.OriginalID,
		ToolName: outcome.call.Name, ParentToolCallID: prepared.parentID, IsError: outcome.isError,
		Execution: cloneToolExecutionInfo(&outcome.execution), Failure: cloneToolFailure(outcome.failure)}
	if prepared.child {
		event.ToolResult = childResultSummary(outcome.result, 8192)
	} else {
		result := publicToolOutcome(outcome).Result
		event.ToolCall = &outcome.call
		event.Args = outcome.args
		event.ToolResult = &result
		event.ToolErr = outcome.err
	}
	emitEvent(emit, event)
}

func (p preparedToolCall) toolResultLimit() int {
	if p.resultLimit > 0 {
		return p.resultLimit
	}
	return 16 << 20
}
