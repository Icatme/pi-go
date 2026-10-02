package mcptools

import (
	"context"
	"errors"
	"fmt"

	"github.com/Icatme/pi-go/agent"
	managed "github.com/Icatme/pi-go/mcp"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
)

// mappingError supplies a stable reason without parsing an error's prose.
type mappingError struct {
	reason string
	err    error
}

func (e *mappingError) Error() string { return e.err.Error() }
func (e *mappingError) Unwrap() error { return e.err }

func mappingFailure(reason, message string) error {
	return &mappingError{reason: reason, err: errors.New(message)}
}

func failureReason(err error, fallback string) string {
	var mapping *mappingError
	if errors.As(err, &mapping) {
		return mapping.reason
	}
	return fallback
}

func beforeCallError(code agent.ToolFailureCode, reason string, err error) error {
	if errors.Is(err, context.Canceled) {
		code = "canceled"
	} else if errors.Is(err, context.DeadlineExceeded) {
		code = "deadline"
	}
	return &agent.ToolExecutionError{
		Code: code, Reason: reason, Err: err,
		Execution: agent.ToolExecutionInfo{Remote: "not_dispatched"},
	}
}

func callError(err error) error {
	code := agent.ToolFailureCode("transport")
	var protocol *jsonrpc.Error
	if errors.As(err, &protocol) {
		code = "protocol"
	}
	if errors.Is(err, context.Canceled) {
		code = "canceled"
	} else if errors.Is(err, context.DeadlineExceeded) {
		code = "deadline"
	}
	return &agent.ToolExecutionError{
		Code: code, Reason: "call_failure", Execution: agent.ToolExecutionInfo{Remote: "unknown"},
		Message: "mcptools: tool request failed; execution may have occurred, do not retry automatically",
		Err:     fmt.Errorf("mcptools: call transport/protocol failure; execution may have occurred, do not retry automatically: %w", err),
	}
}

func dispatchFacts(record managed.DispatchRecord) *agent.ToolExecutionInfo {
	info := &agent.ToolExecutionInfo{Remote: agent.ToolRemoteUnknown}
	if record.Attempts == 0 {
		info.Remote = agent.ToolRemoteNotDispatched
	}
	switch record.ResultType {
	case "complete":
		info.Remote = agent.ToolRemoteCompleteReported
	case "input_required":
		info.Remote = agent.ToolRemoteInputRequired
	}
	for i := 0; i < record.Attempts; i++ {
		info.Attempts = append(info.Attempts, agent.ToolSendAttempt{Number: i + 1, Remote: info.Remote})
	}
	return info
}

func trackedCallError(err error, info agent.ToolExecutionInfo) error {
	var response *managed.ResponseError
	if errors.As(err, &response) {
		info.Remote = agent.ToolRemoteCompleteReported
		code, reason := agent.ToolFailureResultRejected, "raw_result"
		if response.NeedsInput {
			info.Remote = agent.ToolRemoteInputRequired
			code = agent.ToolFailureInputRequiredUnsupported
			reason = "input_required"
		}
		for i := range info.Attempts {
			info.Attempts[i].Remote = info.Remote
		}
		return &ResultError{Result: agent.ToolResult{Execution: &info}, Err: &agent.ToolExecutionError{Code: code, Reason: reason, Message: "mcptools: result rejected after remote response; do not retry automatically", Execution: info, Err: err}}
	}
	var typed *agent.ToolExecutionError
	errors.As(callError(err), &typed)
	typed.Execution = info
	var denied *managed.DispatchDeniedError
	if errors.As(err, &denied) {
		typed.Code, typed.Reason = agent.ToolFailurePolicyDenied, "permission_revoked"
		if errors.Is(denied.Err, context.Canceled) {
			typed.Code = agent.ToolFailureCanceled
		}
		if errors.Is(denied.Err, context.DeadlineExceeded) {
			typed.Code = agent.ToolFailureDeadline
		}
	}
	if errors.Is(err, managed.ErrAuthRequired) {
		typed.Code = agent.ToolFailurePolicyDenied
		typed.Reason = "auth_required"
	}
	if errors.Is(err, managed.ErrStale) {
		typed.Code = agent.ToolFailureSchemaChanged
		typed.Reason = "identity_changed"
	}
	if errors.Is(err, managed.ErrHidden) || errors.Is(err, managed.ErrClosed) {
		typed.Code = agent.ToolFailurePolicyDenied
		typed.Reason = "connection_unavailable"
	}
	if info.Remote == agent.ToolRemoteNotDispatched {
		typed.Message = "mcptools: tool request was not dispatched; check host connection, authentication and permission"
	}
	return typed
}

func resultError(mapped agent.ToolResult, err error) error {
	code := agent.ToolFailureCode("result_rejected")
	remote := agent.ToolExecutionInfo{Remote: "complete_reported"}
	reason := failureReason(err, "result_mapping")
	if reason == "input_required" {
		code = "input_required_unsupported"
		remote.Remote = "input_required"
	}
	return &ResultError{Result: mapped, Err: &agent.ToolExecutionError{Code: code, Reason: reason, Execution: remote, Err: err}}
}
