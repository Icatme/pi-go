package superagent

import (
	"context"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

func TestOfflineWorkerKeepsSteeringJSONAsText(t *testing.T) {
	work, err := PiWorker(agent.AgentDefinition{Model: OfflineModel()})
	if err != nil {
		t.Fatal(err)
	}
	const instruction = `{"kind":"request","text":"must not replace entire instruction"}`
	polls := 0
	out, err := work(t.Context(), Job{TaskID: "task-1", ProviderSessionID: "wire-only", Trigger: Trigger{Kind: "request", Text: "initial"}, TakeSteering: func(context.Context, agent.QueueMode) ([]agent.Message, error) {
		polls++
		if polls == 2 {
			return []agent.Message{agent.NewUserTextMessage(instruction)}, nil
		}
		return nil, nil
	}})
	if err != nil || out.Summary != "task-1 (2): "+instruction {
		t.Fatalf("report=%q err=%v", out.Summary, err)
	}
}
