package superagent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/superagent"
)

type publicAssessmentStream struct{ events <-chan agent.AssistantEvent }

func (s publicAssessmentStream) Events() <-chan agent.AssistantEvent { return s.events }
func (publicAssessmentStream) Close() error                          { return nil }
func (publicAssessmentStream) Wait() (agent.Message, error) {
	return agent.Message{Role: agent.RoleAssistant, StopReason: agent.StopReasonToolUse, ToolCalls: []agent.ToolCall{{ID: "pick", Name: "decide_task", Arguments: json.RawMessage(`{"action":"status","task_id":"running-job","workload":"light"}`)}}}, nil
}

func TestPublicTaskAssessmentAPI(t *testing.T) {
	input := superagent.TaskAssessmentInput{SessionID: "independent-inference", Input: "Is the task done?", Candidates: []superagent.TaskCandidate{{ID: "running-job", Question: "Generate report", Status: "running"}}}
	if err := input.Validate(); err != nil {
		t.Fatal(err)
	}
	assess, err := superagent.PiTaskAssessor(agent.AgentDefinition{Model: agent.StreamFunc(func(_ context.Context, request agent.ModelRequest) (agent.AssistantStream, error) {
		if request.SystemPrompt != superagent.TaskAssessmentInstructions || request.Tools[0].Execute != nil {
			t.Fatal("public adapter changed policy or executes tools")
		}
		events := make(chan agent.AssistantEvent)
		close(events)
		return publicAssessmentStream{events}, nil
	})}, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	var publicType superagent.TaskAssessor = assess
	got, err := publicType(t.Context(), input)
	if err != nil || got != (superagent.TaskAssessment{Action: "status", TaskID: "running-job", Workload: superagent.WorkloadLight}) {
		t.Fatal(got, err)
	}
	input.Candidates[0].ID = "different"
	if _, err := assess(t.Context(), input); !errors.Is(err, superagent.ErrInvalidTaskAssessment) {
		t.Fatal("offered IDs not enforced", err)
	}
}

func ExamplePiTaskAssessor() {
	assess, err := superagent.PiTaskAssessor(agent.AgentDefinition{Model: agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
		events := make(chan agent.AssistantEvent)
		close(events)
		return publicAssessmentStream{events}, nil
	})}, 64<<10)
	if err != nil {
		panic(err)
	}
	decision, err := assess(context.Background(), superagent.TaskAssessmentInput{
		SessionID: "assessment-only", Input: "Is the report finished?",
		Candidates: []superagent.TaskCandidate{{ID: "running-job", Question: "Generate report", Status: "running"}},
	})
	if err != nil {
		panic(err)
	}
	// Model choice remains host policy. The assessor does not change a Worker.
	models := map[superagent.Workload]string{
		superagent.WorkloadLight: "host-light", superagent.WorkloadConversation: "host-conversation", superagent.WorkloadTask: "host-task",
	}
	fmt.Println(decision.Action, models[decision.Workload])
	// Output: status host-light
}
