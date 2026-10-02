package agent

import (
	"context"
	"errors"
)

// ToolFailureCode distinguishes failures without interpreting model-facing text.
type ToolFailureCode string

const (
	ToolFailureArgumentInvalid          ToolFailureCode = "argument_invalid"
	ToolFailurePolicyDenied             ToolFailureCode = "policy_denied"
	ToolFailureSchemaChanged            ToolFailureCode = "schema_changed"
	ToolFailureNestedSuspendUnsupported ToolFailureCode = "nested_suspend_unsupported"
	ToolFailureToolReportedError        ToolFailureCode = "tool_reported_error"
	ToolFailureResultRejected           ToolFailureCode = "result_rejected"
	ToolFailureInputRequiredUnsupported ToolFailureCode = "input_required_unsupported"
	ToolFailureTransport                ToolFailureCode = "transport"
	ToolFailureProtocol                 ToolFailureCode = "protocol"
	ToolFailureCanceled                 ToolFailureCode = "canceled"
	ToolFailureDeadline                 ToolFailureCode = "deadline"
	ToolFailureHook                     ToolFailureCode = "hook"
	ToolFailureResource                 ToolFailureCode = "resource"
	ToolFailureScript                   ToolFailureCode = "script"
	ToolFailureSandbox                  ToolFailureCode = "sandbox"
)

type ToolLocalState string
type ToolRemoteState string

const (
	ToolLocalNotStarted        ToolLocalState  = "not_started"
	ToolLocalEntered           ToolLocalState  = "entered"
	ToolLocalReturned          ToolLocalState  = "returned"
	ToolRemoteNotDispatched    ToolRemoteState = "not_dispatched"
	ToolRemoteCompleteReported ToolRemoteState = "complete_reported"
	ToolRemoteInputRequired    ToolRemoteState = "input_required"
	ToolRemoteUnknown          ToolRemoteState = "unknown"
	ToolRemoteNotApplicable    ToolRemoteState = "not_applicable"
)

// ToolSendAttempt describes a physical send only when the transport can observe
// it. An empty list does not prove that an executor sent no requests.
type ToolSendAttempt struct {
	Number int             `json:"number"`
	Remote ToolRemoteState `json:"remote"`
}

type ToolExecutionInfo struct {
	Local    ToolLocalState    `json:"local"`
	Remote   ToolRemoteState   `json:"remote"`
	Attempts []ToolSendAttempt `json:"attempts,omitempty"`
}

type ToolFailure struct {
	Code   ToolFailureCode `json:"code"`
	Reason string          `json:"reason,omitempty"`
}

// ToolExecutionError preserves both the original error chain and trusted
// execution facts. A returned error never becomes successful through IsError.
type ToolExecutionError struct {
	Code   ToolFailureCode
	Reason string
	// Message is an explicitly safe presentation after hook processing. Err
	// remains available to trusted Go callers even when its text is redacted.
	Message   string
	Execution ToolExecutionInfo
	Err       error
}

func (e *ToolExecutionError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	if e.Reason != "" {
		return e.Reason
	}
	return string(e.Code)
}

func (e *ToolExecutionError) Unwrap() error { return e.Err }

// ToolPermissionHook checks current authorization without changing arguments.
// It runs after scheduling, and adapters call CheckPermission again immediately
// before crossing their actual side-effect boundary.
type ToolPermissionHook func(context.Context, BeforeToolCallContext) error

type ToolExecutionContext struct {
	ToolCall        ToolCall
	Args            any
	Context         AgentContext
	OnUpdate        ToolUpdateFunc
	CheckPermission func(context.Context) error
	ChildCaller     ChildCaller
	ChildSequential bool
	Invocation      *ToolInvocation
}

func cloneToolExecutionInfo(info *ToolExecutionInfo) *ToolExecutionInfo {
	if info == nil {
		return nil
	}
	copy := *info
	copy.Attempts = append([]ToolSendAttempt(nil), info.Attempts...)
	return &copy
}

func cloneToolFailure(failure *ToolFailure) *ToolFailure {
	if failure == nil {
		return nil
	}
	copy := *failure
	return &copy
}

func failureCode(err error, fallback ToolFailureCode) ToolFailureCode {
	var typed *ToolExecutionError
	if errors.As(err, &typed) && typed.Code != "" {
		return typed.Code
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return ToolFailureDeadline
	}
	if errors.Is(err, context.Canceled) {
		return ToolFailureCanceled
	}
	return fallback
}

func newToolFailure(err error, code ToolFailureCode, execution ToolExecutionInfo) *ToolExecutionError {
	reason := ""
	var typed *ToolExecutionError
	if errors.As(err, &typed) {
		reason = typed.Reason
	}
	return &ToolExecutionError{Code: failureCode(err, code), Reason: reason, Execution: execution, Err: err}
}

func rejectedToolOutcome(call ToolCall, err error, code ToolFailureCode) toolOutcome {
	info := ToolExecutionInfo{Local: ToolLocalNotStarted, Remote: ToolRemoteNotDispatched}
	typed := newToolFailure(err, code, info)
	return toolOutcome{call: cloneToolCall(call), result: errorToolResult(typed.Error()), isError: true,
		err: typed, execution: info, failure: &ToolFailure{Code: typed.Code, Reason: typed.Reason}}
}

func publicToolOutcome(outcome toolOutcome) ToolCallOutcome {
	outcome.result.IsError = outcome.isError
	outcome.result.Execution = cloneToolExecutionInfo(&outcome.execution)
	outcome.result.Failure = cloneToolFailure(outcome.failure)
	return ToolCallOutcome{ToolCall: cloneToolCall(outcome.call), Result: cloneToolResult(outcome.result),
		IsError: outcome.isError, Err: outcome.err, Execution: *cloneToolExecutionInfo(&outcome.execution),
		Failure: cloneToolFailure(outcome.failure)}
}
