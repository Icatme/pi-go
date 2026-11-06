package superagent

import (
	"context"
	"errors"
	"fmt"
)

// Workload is a routing hint, not execution or model authorization.
type Workload string

const (
	WorkloadLight        Workload = "light"
	WorkloadConversation Workload = "conversation"
	WorkloadTask         Workload = "task"
)

// TaskCandidate is a host-selected context boundary. CanAttach is determined
// by the host; Status is descriptive and never grants attachment permission.
type TaskCandidate struct {
	ID           string `json:"id"`
	Question     string `json:"question"`
	RootQuestion string `json:"root_question,omitempty"`
	Status       string `json:"status"`
	CanAttach    bool   `json:"can_attach"`
}

// TaskAssessment selects new, attach, clarify or status. Only attach and status
// carry a TaskID, copied exactly from the offered candidates.
type TaskAssessment struct {
	Action   string   `json:"action"`
	TaskID   string   `json:"task_id"`
	Workload Workload `json:"workload"`
}

type TaskAssessmentInput struct {
	SessionID  string          `json:"-"`
	Input      string          `json:"input"`
	Candidates []TaskCandidate `json:"candidates"`
}

// Validate checks the host envelope before a model is constructed. The assessor
// separately checks the complete serialized context against its byte budget.
func (input TaskAssessmentInput) Validate() error {
	if !validAssessmentIdentity(input.SessionID) || !validAssessmentText(input.Input, 1<<20) || len(input.Candidates) > 128 {
		return fmt.Errorf("%w: invalid input", ErrInvalidTaskAssessment)
	}
	seen := make(map[string]bool, len(input.Candidates))
	for _, candidate := range input.Candidates {
		if !validAssessmentIdentity(candidate.ID) || seen[candidate.ID] || !validAssessmentText(candidate.Question, 64<<10) ||
			candidate.RootQuestion != "" && !validAssessmentText(candidate.RootQuestion, 64<<10) || !validAssessmentText(candidate.Status, 256) {
			return fmt.Errorf("%w: invalid candidate", ErrInvalidTaskAssessment)
		}
		seen[candidate.ID] = true
	}
	return nil
}

// TaskAssessor performs no admission, journal mutation or business execution.
// The host revalidates the selected context and persists its routing decision.
type TaskAssessor func(context.Context, TaskAssessmentInput) (TaskAssessment, error)

// ErrInvalidTaskAssessment identifies invalid configuration, input or model
// protocol output. Provider and cancellation errors retain their own identity.
var ErrInvalidTaskAssessment = errors.New("invalid task assessment")
