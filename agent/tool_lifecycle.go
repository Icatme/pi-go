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
	argumentLimit := childCallContextFrom(ctx).limits.MaxArgumentBytes
	if err := checkArgumentStrings(original.ParsedArgs); err != nil {
		// Drop rejected host values before cloneToolCall can traverse them.
		call := original
		call.ParsedArgs = nil
		emitToolExecutionStart(start, call, nil)
		return preparedFailure(call, nil, err, ToolFailureArgumentInvalid), nil, nil
	}
	call := cloneToolCall(original)
	fail := func(args any, err error, code ToolFailureCode) (preparedToolCall, *SuspendedToolCall, error) {
		if childParentID(ctx) != "" {
			err = safeChildError(err, code, "Child tool preflight failed; check its input schema and host permissions")
		}
		if budgetErr := checkArgumentBudget(args, argumentLimit); budgetErr != nil {
			budgetErr.Err = err
			return preparedFailure(call, nil, budgetErr, ToolFailureResource), nil, nil
		}
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
	validator, err := newToolArgumentValidator(tool, argumentLimit)
	if err != nil {
		return preparedToolCall{}, nil, err
	}
	args, err := parseToolArguments(tool, call)
	if budgetErr := checkArgumentBudget(args, argumentLimit); budgetErr != nil {
		budgetErr.Err = err
		args, err = nil, budgetErr
	}
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
		// Hooks may mutate Args even when they block or return an error. Check
		// that value before validation, suspension serialization or failure copy.
		if budgetErr := checkArgumentBudget(executionArgs, argumentLimit); budgetErr != nil {
			budgetErr.Err = beforeErr
			return fail(nil, budgetErr, ToolFailureResource)
		}
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
			if childParentID(ctx) != "" {
				gateErr = safeChildError(gateErr, ToolFailureHook, "Child tool approval hook failed")
			}
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
		if prepared.child {
			err = safeChildError(err, ToolFailurePolicyDenied, "Child tool permission was denied before execution")
		}
		outcome := rejectedToolOutcome(prepared.call, err, ToolFailurePolicyDenied)
		outcome.args = cloneAny(prepared.args)
		if prepared.child {
			outcome.result = boundedErrorResult(outcome.result, outcome.err.Error(), prepared.failureResultLimit())
		}
		emitToolOutcome(emit, prepared, outcome)
		return outcome, nil
	}
	var children *childCallScope
	if prepared.tool.ResolveChildTools != nil {
		resolved, err := resolveToolChildren(ctx, prepared.tool.ResolveChildTools, ToolExecutionContext{
			ToolCall: cloneToolCall(prepared.call), Args: cloneAny(prepared.args), Context: cloneAgentContext(prepared.context),
			CheckPermission: checkPermission, Invocation: toolInvocationFromContext(ctx),
		})
		if err == nil {
			prepared.tool.ChildTools = cloneTools(resolved)
			if prepared.tool.ChildTools == nil {
				prepared.tool.ChildTools = []ToolDefinition{}
			}
			prepared.tool.ResolveChildTools = nil
			err = validateChildTools(prepared.tool)
		}
		if err == nil {
			err = checkPermission(ctx)
		}
		if err != nil {
			outcome := rejectedToolOutcome(prepared.call, err, ToolFailurePolicyDenied)
			outcome.args = cloneAny(prepared.args)
			emitToolOutcome(emit, prepared, outcome)
			return outcome, nil
		}
	}
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
	result, execErr := executeToolBody(ctx, prepared.tool.Execute, executionContext)
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
		if result.Execution == nil || result.Execution.Remote == "" {
			outcome.execution.Remote = ToolRemoteUnknown
		}
		var typed *ToolExecutionError
		if errors.As(execErr, &typed) && typed.Execution.Remote != "" {
			outcome.execution = *cloneToolExecutionInfo(&typed.Execution)
			outcome.execution.Local = ToolLocalReturned
		}
		outcome.err = newToolFailure(execErr, ToolFailureProtocol, outcome.execution)
		var failure *ToolExecutionError
		errors.As(outcome.err, &failure)
		outcome.failure = &ToolFailure{Code: failure.Code, Reason: failure.Reason}
		if prepared.child && !valueFitsBudget(result, prepared.toolResultLimit()) {
			setOutcomeFailure(&outcome, errors.New("child tool result exceeds byte limit"), ToolFailureResource, prepared.failureResultLimit())
		} else {
			outcome.result = boundedErrorResult(result, execErr.Error(), prepared.failureResultLimit())
			outcome.isError = true
		}
	} else {
		if prepared.child && !valueFitsBudget(result, prepared.toolResultLimit()) {
			setOutcomeFailure(&outcome, errors.New("child tool result exceeds byte limit"), ToolFailureResource, prepared.failureResultLimit())
		} else {
			outcome.result = cloneToolResult(result)
			outcome.isError = result.IsError
			if result.IsError {
				outcome.failure = &ToolFailure{Code: ToolFailureToolReportedError}
			}
		}
	}
	if len(outcome.result.StructuredContent) > 0 && !json.Valid(outcome.result.StructuredContent) {
		setOutcomeFailure(&outcome, errors.New("tool returned invalid structured content JSON"), ToolFailureResultRejected, prepared.failureResultLimit())
	}
	if definition.AfterToolCall != nil {
		override, hookErr := executeAfterToolHook(ctx, definition.AfterToolCall, AfterToolCallContext{AssistantMessage: cloneMessage(assistant),
			ToolCall: cloneToolCall(prepared.call), Args: cloneAny(prepared.args), Context: cloneAgentContext(prepared.context),
			Result: cloneToolResult(outcome.result), IsError: outcome.isError, Err: outcome.err,
			Execution: *cloneToolExecutionInfo(&outcome.execution), Failure: cloneToolFailure(outcome.failure), ParentToolCallID: prepared.parentID})
		if hookErr != nil {
			if prepared.child {
				hookErr = safeChildError(hookErr, ToolFailureHook, "Child tool output hook failed; output withheld")
			}
			// Preserve the execution chain, but discard all unprocessed output.
			setOutcomeFailure(&outcome, hookErr, ToolFailureHook, prepared.failureResultLimit())
		} else {
			if override.Result != nil {
				candidate := effectiveResultCandidate(outcome.result, *override.Result)
				if prepared.child && !valueFitsBudget(candidate, prepared.toolResultLimit()) {
					setOutcomeFailure(&outcome, errors.New("after-tool child result exceeds byte limit"), ToolFailureResource, prepared.failureResultLimit())
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
		setOutcomeFailure(&outcome, errors.New("after-tool hook returned invalid structured content JSON"), ToolFailureResultRejected, prepared.failureResultLimit())
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
			setOutcomeFailure(&outcome, err, ToolFailureResultRejected, prepared.failureResultLimit())
		}
	}
	if outcome.err == nil && prepared.tool.ValidateResult != nil {
		effective := cloneToolResult(outcome.result)
		effective.IsError = outcome.isError
		effective.Execution = cloneToolExecutionInfo(&outcome.execution)
		effective.Failure = cloneToolFailure(outcome.failure)
		if err := prepared.tool.ValidateResult(effective); err != nil {
			setOutcomeFailure(&outcome, err, ToolFailureResultRejected, prepared.failureResultLimit())
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

func safeChildError(err error, code ToolFailureCode, message string) error {
	var typed *ToolExecutionError
	if errors.As(err, &typed) && typed.Message != "" {
		return err
	}
	reason := ""
	if typed != nil {
		reason = typed.Reason
	}
	return &ToolExecutionError{Code: failureCode(err, code), Reason: reason, Message: message, Err: err}
}

func resolveToolChildren(ctx context.Context, resolver func(context.Context, ToolExecutionContext) ([]ToolDefinition, error), execution ToolExecutionContext) (tools []ToolDefinition, err error) {
	defer func() {
		if value := recover(); value != nil {
			tools = nil
			err = &ToolExecutionError{Code: ToolFailureHook, Reason: "child_resolver_panicked", Message: "child tool resolver panicked", Execution: ToolExecutionInfo{Local: ToolLocalNotStarted, Remote: ToolRemoteNotDispatched}, Err: &toolCallbackPanic{value: value}}
		}
	}()
	return resolver(ctx, execution)
}

// A host executor panic is an execution failure, not a way to skip the common
// after-hook, scope-close and reporting lifecycle. The original value stays in
// the Go error chain; its potentially private text is never the presentation.
type toolCallbackPanic struct{ value any }

func (*toolCallbackPanic) Error() string { return "tool callback panicked" }
func (e *toolCallbackPanic) Unwrap() error {
	err, _ := e.value.(error)
	return err
}

func executeToolBody(ctx context.Context, execute ToolExecutorFunc, in ToolExecutionContext) (result ToolResult, err error) {
	defer func() {
		if value := recover(); value != nil {
			err = &ToolExecutionError{Code: ToolFailureProtocol, Reason: "executor_panic", Message: "tool executor panicked",
				Execution: ToolExecutionInfo{Local: ToolLocalReturned, Remote: ToolRemoteUnknown}, Err: &toolCallbackPanic{value: value}}
		}
	}()
	return execute(ctx, in)
}

func executeAfterToolHook(ctx context.Context, hook AfterToolCallHook, in AfterToolCallContext) (result AfterToolCallResult, err error) {
	defer func() {
		if value := recover(); value != nil {
			err = &ToolExecutionError{Code: ToolFailureHook, Reason: "after_hook_panic", Message: "after-tool hook panicked", Err: &toolCallbackPanic{value: value}}
		}
	}()
	return hook(ctx, in)
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

func setOutcomeFailure(outcome *toolOutcome, err error, code ToolFailureCode, resultLimit int) {
	combined := err
	if outcome.err != nil {
		combined = errors.Join(err, outcome.err)
	}
	reason := ""
	var typed *ToolExecutionError
	if errors.As(err, &typed) {
		reason = typed.Reason
	}
	message := boundedUTF8(err.Error(), min(8192, resultLimit))
	outcome.err = &ToolExecutionError{Code: code, Reason: reason, Message: message, Err: combined, Execution: outcome.execution}
	outcome.failure = &ToolFailure{Code: code, Reason: reason}
	outcome.result = boundedErrorResult(ToolResult{}, message, resultLimit)
	outcome.isError = true
}

func boundedErrorResult(result ToolResult, message string, resultLimit int) ToolResult {
	bounded := ToolResult{IsError: true, Terminate: result.Terminate}
	// Go-only execution reports remain useful on failure, but an untrusted
	// arbitrary Details value does not get an unlimited copy. All retained
	// payload shares one limit, including generated failure text.
	remaining := min(resultLimit, 1<<20)
	if cost, fits := valueByteCost(result.Details, remaining); fits {
		bounded.Details = cloneAny(result.Details)
		remaining -= cost
	}
	textPartCost, _ := valueByteCost(Part{Type: PartTypeText}, 1<<20)
	for i, part := range result.Content {
		// Even empty or rejected parts have bounded traversal and copy work.
		if remaining < textPartCost || i >= min(resultLimit, 1<<20)/textPartCost {
			break
		}
		switch part.Type {
		case PartTypeText:
			text := boundedUTF8(part.Text, remaining-textPartCost)
			remaining -= textPartCost + len(text)
			bounded.Content = append(bounded.Content, Part{Type: PartTypeText, Text: text})
		case PartTypeImage:
			image := Part{Type: PartTypeImage, Data: part.Data, MIMEType: part.MIMEType}
			if cost, fits := valueByteCost(image, remaining); fits {
				bounded.Content = append(bounded.Content, Part{Type: PartTypeImage, Data: strings.Clone(part.Data), MIMEType: strings.Clone(part.MIMEType)})
				remaining -= cost
			}
		}
	}
	if _, fits := valueByteCost(result.StructuredContent, remaining); fits {
		bounded.StructuredContent = append(json.RawMessage(nil), result.StructuredContent...)
		remaining -= 8 + len(result.StructuredContent)
	}
	if len(bounded.Content) == 0 && remaining >= textPartCost {
		bounded.Content = []Part{{Type: PartTypeText, Text: boundedUTF8(message, min(8192, remaining-textPartCost))}}
	}
	return bounded
}

func emitToolOutcome(emit EventSink, prepared preparedToolCall, outcome toolOutcome) {
	event := AgentEvent{Type: EventToolExecutionEnd, ToolCallID: outcome.call.ID, OriginalToolCallID: outcome.call.OriginalID,
		ToolName: outcome.call.Name, ParentToolCallID: prepared.parentID, IsError: outcome.isError,
		Execution: cloneToolExecutionInfo(&outcome.execution), Failure: cloneToolFailure(outcome.failure)}
	if prepared.child {
		event.ToolResult = childResultSummary(outcome.result, prepared.toolSummaryLimit())
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

func (p preparedToolCall) toolSummaryLimit() int {
	if p.summaryLimit > 0 {
		return p.summaryLimit
	}
	return 8192
}

func (p preparedToolCall) failureResultLimit() int {
	if p.child {
		return p.toolResultLimit()
	}
	return 1 << 20
}
