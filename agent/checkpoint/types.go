// Package checkpoint adds durable, targeted tool approval to the agent runner.
package checkpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/Icatme/pi-go/agent"
)

// MaxPayloadBytes is the largest checkpoint envelope accepted by Runner.
const MaxPayloadBytes = 16 << 20

// CheckpointID identifies one durable agent run.
type CheckpointID string

// InterruptID identifies one active approval request. IDs are single-use.
type InterruptID string

// Revision is the monotonically increasing checkpoint revision.
type Revision uint64

// DecisionAction identifies an approval decision.
type DecisionAction string

const (
	DecisionActionApprove DecisionAction = "approve"
	DecisionActionReject  DecisionAction = "reject"
)

// ResumeDecision resolves one active interrupt.
type ResumeDecision struct {
	Action DecisionAction `json:"action"`
	Reason string         `json:"reason,omitempty"`
}

// ResumeParams contains targeted decisions for a resume attempt.
type ResumeParams struct {
	Decisions map[InterruptID]ResumeDecision `json:"decisions"`
}

// ToolApprovalRequest is the durable representation shown to an approver.
type ToolApprovalRequest struct {
	ToolCallID         string          `json:"tool_call_id"`
	OriginalToolCallID string          `json:"original_tool_call_id,omitempty"`
	ToolName           string          `json:"tool_name"`
	Arguments          json.RawMessage `json:"arguments"`
}

// ApprovalRequirement describes whether a tool call must be approved.
type ApprovalRequirement struct {
	Required bool   `json:"required"`
	Message  string `json:"message,omitempty"`
}

// ApprovalPolicy decides whether a fully preflighted tool call needs approval.
type ApprovalPolicy func(context.Context, ToolApprovalRequest) (ApprovalRequirement, error)

// InterruptKind identifies the reason a checkpoint is interrupted.
type InterruptKind string

const InterruptKindToolApproval InterruptKind = "tool_approval"

// Interrupt is an active, targeted request for external input.
type Interrupt struct {
	ID      InterruptID         `json:"id"`
	Kind    InterruptKind       `json:"kind"`
	Message string              `json:"message,omitempty"`
	Tool    ToolApprovalRequest `json:"tool"`
}

// Status identifies the durable checkpoint state.
type Status string

const (
	StatusRunning       Status = "running"
	StatusInterrupted   Status = "interrupted"
	StatusCompleted     Status = "completed"
	StatusFailed        Status = "failed"
	StatusIndeterminate Status = "indeterminate"
)

// Outcome is an isolated view of a checkpoint operation. Persisted is false
// when execution finished but its terminal compare-and-swap did not commit.
type Outcome struct {
	CheckpointID CheckpointID        `json:"checkpoint_id"`
	Revision     Revision            `json:"revision"`
	Status       Status              `json:"status"`
	Persisted    bool                `json:"persisted"`
	Snapshot     agent.AgentSnapshot `json:"snapshot"`
	Interrupts   []Interrupt         `json:"interrupts,omitempty"`
}

// StoredCheckpoint is the opaque record exchanged with a Store.
type StoredCheckpoint struct {
	Revision Revision
	Payload  []byte
}

// Store provides optimistic, revisioned checkpoint persistence. Implementations
// are a trusted boundary because checkpoint payloads can contain provider secrets.
type Store interface {
	Load(context.Context, CheckpointID) (StoredCheckpoint, error)
	CompareAndSwap(context.Context, CheckpointID, Revision, []byte) (StoredCheckpoint, error)
}

// RunnerConfig configures a durable approval runner. ToolResolver is rejected:
// the resumable tool set must be stable. Custom argument parsers and
// BeforeToolCall hooks must be pure and deterministic; changed final arguments
// deliberately invalidate old approvals and interrupt the whole batch again.
type RunnerConfig struct {
	Definition        agent.AgentDefinition
	DefinitionVersion string
	Store             Store
	ApprovalPolicy    ApprovalPolicy
}

var (
	ErrCheckpointNotFound        = errors.New("checkpoint not found")
	ErrRevisionConflict          = errors.New("checkpoint revision conflict")
	ErrInvalidCheckpoint         = errors.New("invalid checkpoint")
	ErrPayloadTooLarge           = errors.New("checkpoint payload too large")
	ErrDefinitionVersionMismatch = errors.New("checkpoint definition version mismatch")
	ErrInvalidResume             = errors.New("invalid checkpoint resume")
	ErrCheckpointNotResumable    = errors.New("checkpoint is not resumable")
	ErrCheckpointBusy            = errors.New("checkpoint execution is already running")
	ErrOutcomeIndeterminate      = errors.New("checkpoint outcome is indeterminate")
)

// RevisionConflictError reports a failed compare-and-swap operation.
type RevisionConflictError struct {
	CheckpointID CheckpointID
	Expected     Revision
	Actual       Revision
}

func (e *RevisionConflictError) Error() string {
	return fmt.Sprintf("%v: checkpoint %q expected revision %d, actual revision %d", ErrRevisionConflict, e.CheckpointID, e.Expected, e.Actual)
}

func (e *RevisionConflictError) Unwrap() error { return ErrRevisionConflict }

const checkpointEventBufferSize = 32

// RunStream exposes one asynchronous durable run or resume operation.
type RunStream struct {
	events chan agent.AgentEvent
	done   chan struct{}
	cancel context.CancelFunc

	closeOnce sync.Once
	resultMu  sync.RWMutex
	outcome   Outcome
	err       error
}

// Events returns a bounded, lossless event stream. It must be drained before Wait.
func (s *RunStream) Events() <-chan agent.AgentEvent { return s.events }

// Wait returns an ownership-isolated outcome after the event stream is drained.
func (s *RunStream) Wait() (Outcome, error) {
	<-s.done
	s.resultMu.RLock()
	outcome := cloneOutcome(s.outcome)
	err := s.err
	s.resultMu.RUnlock()
	return outcome, err
}

// Close cancels the operation, drains its event stream, and waits for shutdown.
func (s *RunStream) Close() error {
	s.closeOnce.Do(s.cancel)
	for range s.events {
	}
	<-s.done
	return nil
}
