package agent

import (
	"errors"
	"fmt"
)

var (
	// ErrModelNotConfigured indicates that no model was configured for a run.
	ErrModelNotConfigured = errors.New("agent: model not configured")
	// ErrAlreadyRunning indicates that an agent is already processing a request.
	ErrAlreadyRunning = errors.New("agent: agent is already running")
	// ErrNoPromptMessages indicates that prompt was called without messages.
	ErrNoPromptMessages = errors.New("agent: no prompt messages provided")
	// ErrNoMessagesToContinue indicates that continue was called without history.
	ErrNoMessagesToContinue = errors.New("agent: no messages to continue from")
	// ErrCannotContinueFromAssistant indicates that continue was called from an assistant tail message.
	ErrCannotContinueFromAssistant = errors.New("agent: cannot continue from assistant message")
	// ErrMaxTurnsExceeded indicates that a run exceeded its configured turn budget.
	ErrMaxTurnsExceeded = errors.New("agent: maximum turns exceeded")
	// ErrPendingToolCallsRequireResume prevents a normal run from bypassing an outstanding tool batch.
	ErrPendingToolCallsRequireResume = errors.New("agent: pending tool calls require explicit resume")
	// ErrToolGateRequired prevents pending tool execution without an explicit gate decision.
	ErrToolGateRequired = errors.New("agent: pending tool calls require a tool gate")
	// ErrPendingToolControlRequired indicates that a pending batch has no durable turn/binding state.
	ErrPendingToolControlRequired = errors.New("agent: pending tool calls require durable control state")
)

// ToolCallsSuspendedError reports prepared calls that requested suspension.
// The assistant message and pending-call identities remain in the returned
// snapshot so the batch can be resumed explicitly.
type ToolCallsSuspendedError struct {
	Calls []SuspendedToolCall
}

func (e *ToolCallsSuspendedError) Error() string {
	return fmt.Sprintf("agent: %d tool call(s) suspended", len(e.Calls))
}
