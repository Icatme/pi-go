package superagent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

func TestPiTaskAssessorOptionalReadAndControlCapabilities(t *testing.T) {
	for _, action := range []string{"result", "stop", "list"} {
		for _, tc := range []struct {
			name            string
			enabled, mutate bool
			target          string
			wantValid       bool
		}{
			{name: "host enabled", enabled: true, target: "first", wantValid: true},
			{name: "legacy host", target: "first"},
			{name: "other candidate", enabled: true, target: "second"},
			{name: "invented id", enabled: true, target: "missing"},
			{name: "provider cannot grant", target: "first", mutate: true},
			{name: "provider cannot replace", enabled: true, target: "first", mutate: true, wantValid: true},
		} {
			if action == "list" && (tc.name == "other candidate" || tc.name == "invented id") {
				continue
			}
			t.Run(action+"/"+tc.name, func(t *testing.T) {
				input := assessmentInput()
				target := tc.target
				switch action {
				case "result":
					input.Candidates[0].CanReadResult = tc.enabled
				case "stop":
					input.Candidates[0].CanStop = tc.enabled
				case "list":
					input.CanListTasks = tc.enabled
					target = ""
				}
				want := TaskAssessment{Action: action, TaskID: target, Workload: WorkloadLight}
				raw, _ := json.Marshal(want)
				assess, err := PiTaskAssessor(agent.AgentDefinition{Model: agent.StreamFunc(func(_ context.Context, request agent.ModelRequest) (agent.AssistantStream, error) {
					actions := request.Tools[0].Parameters["properties"].(map[string]any)["action"].(map[string]any)["enum"].([]string)
					if slices.Contains(actions, action) != tc.enabled {
						t.Fatalf("action schema %v enabled=%v", actions, tc.enabled)
					}
					for _, other := range []string{"result", "list", "stop", "steer"} {
						if other != action && slices.Contains(actions, other) {
							t.Fatalf("unrequested capability %s", other)
						}
					}
					var payload TaskAssessmentInput
					if err := json.Unmarshal([]byte(messageText(request.Messages[0])), &payload); err != nil {
						t.Fatal(err)
					}
					if payload.CanListTasks != input.CanListTasks || payload.Candidates[0].CanReadResult != input.Candidates[0].CanReadResult || payload.Candidates[0].CanStop != input.Candidates[0].CanStop {
						t.Fatal("changed host capability")
					}
					if tc.mutate {
						input.CanListTasks = !input.CanListTasks
						input.Candidates[0].CanReadResult = !input.Candidates[0].CanReadResult
						input.Candidates[0].CanStop = !input.Candidates[0].CanStop
					}
					return assessmentAnswer(assessmentMessage(string(raw))), nil
				})}, 64<<10)
				if err != nil {
					t.Fatal(err)
				}
				got, err := assess(t.Context(), input)
				if tc.wantValid {
					if err != nil || got != want {
						t.Fatal(got, err)
					}
				} else if !errors.Is(err, ErrInvalidTaskAssessment) || got != (TaskAssessment{}) {
					t.Fatal(got, err)
				}
			})
		}
	}
}

func TestPiTaskAssessorLegacySchemaAndExactSingleTargets(t *testing.T) {
	input := assessmentInput()
	for _, raw := range []string{
		`{"action":"result","task_id":"first","workload":"light"}`,
		`{"action":"stop","task_id":"first","workload":"light"}`,
		`{"action":"list","task_id":"","workload":"light"}`,
	} {
		assess, err := PiTaskAssessor(agent.AgentDefinition{Model: agent.StreamFunc(func(_ context.Context, request agent.ModelRequest) (agent.AssistantStream, error) {
			actions := request.Tools[0].Parameters["properties"].(map[string]any)["action"].(map[string]any)["enum"].([]string)
			if !reflect.DeepEqual(actions, []string{"new", "attach", "clarify", "status"}) {
				t.Fatal(actions)
			}
			return assessmentAnswer(assessmentMessage(raw)), nil
		})}, 64<<10)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := assess(t.Context(), input); !errors.Is(err, ErrInvalidTaskAssessment) || got != (TaskAssessment{}) {
			t.Fatal(got, err)
		}
	}
	for _, raw := range []string{
		`{"action":"result","task_id":"","workload":"light"}`,
		`{"action":"stop","task_id":"","workload":"light"}`,
		`{"action":"list","task_id":"first","workload":"light"}`,
		`{"action":"result","task_id":"first","workload":"task"}`,
		`{"action":"stop","task_id":"first","workload":"conversation"}`,
		`{"action":"list","task_id":"","workload":"task"}`,
		`{"action":"result","task_id":["first","second"],"workload":"light"}`,
		`{"action":"stop","task_id":"first","task_id":"second","workload":"light"}`,
	} {
		if _, err := parseTaskAssessment(json.RawMessage(raw)); !errors.Is(err, ErrInvalidTaskAssessment) {
			t.Fatalf("accepted %s: %v", raw, err)
		}
	}
}
