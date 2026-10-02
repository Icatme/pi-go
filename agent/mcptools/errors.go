package mcptools

import (
	"context"
	"errors"
	"fmt"

	"github.com/Icatme/pi-go/agent"
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
		Err: fmt.Errorf("mcptools: call transport/protocol failure; execution may have occurred, do not retry automatically: %w", err),
	}
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
