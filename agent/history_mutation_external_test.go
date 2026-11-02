package agent_test

import (
	"github.com/Icatme/pi-go/agent"
	"testing"
)

// These method-expression assignments pin existing external call signatures.
var (
	_ func(*agent.Agent, []agent.Message) = (*agent.Agent).ReplaceMessages
	_ func(*agent.Agent)                  = (*agent.Agent).ClearMessages
	_ func(*agent.Agent, string)          = (*agent.Agent).SetSessionID
)

func TestHistoryMutationPublicCallsRemainSourceCompatible(t *testing.T) {
	a, err := agent.NewAgent(agent.AgentDefinition{})
	if err != nil {
		t.Fatal(err)
	}
	a.ReplaceMessages([]agent.Message{agent.NewUserTextMessage("legacy")})
	a.SetSessionID("legacy")
	a.ClearMessages()
	if err := a.TryReplaceMessages([]agent.Message{agent.NewUserTextMessage("checked")}); err != nil {
		t.Fatal(err)
	}
	if err := a.TrySetSessionID("checked"); err != nil {
		t.Fatal(err)
	}
	if err := a.TryClearMessages(); err != nil {
		t.Fatal(err)
	}
}
