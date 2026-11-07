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

// TaskCandidate is a host-selected context boundary. Its capabilities are
// determined by the host; Status and question text never grant permission.
// Hosts revalidate the exact target and current authority before acting.
type TaskCandidate struct {
	ID            string `json:"id"`
	Question      string `json:"question"`
	RootQuestion  string `json:"root_question,omitempty"`
	Status        string `json:"status"`
	CanAttach     bool   `json:"can_attach"`
	CanSteer      bool   `json:"can_steer,omitempty"`
	CanReadResult bool   `json:"can_read_result,omitempty"`
	CanStop       bool   `json:"can_stop,omitempty"`
}

// TaskAssessment selects new, attach, steer, clarify, status, result, list or
// stop. Attach, steer, status, result and stop carry one exact offered TaskID.
// Result, list and stop are optional host capabilities and use light workload.
// Classification does not execute work, deliver results or cancel a task.
type TaskAssessment struct {
	Action   string   `json:"action"`
	TaskID   string   `json:"task_id"`
	Workload Workload `json:"workload"`
}

type TaskAssessmentInput struct {
	SessionID    string          `json:"-"`
	Input        string          `json:"input"`
	Candidates   []TaskCandidate `json:"candidates"`
	CanListTasks bool            `json:"can_list_tasks,omitempty"`
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
