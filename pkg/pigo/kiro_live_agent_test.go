package pigo_test

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/pkg/pigo"
)

func TestKiroLiveRegisteredAgentDefaultAutoAndSession(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprintf("late-failure-%v", fail), func(t *testing.T) {
			source := fmt.Sprintf("live-agent-%d", kiroAgentFixtureSequence.Add(1))
			delta := make(chan struct{})
			var once sync.Once
			calls, updates, tools := 0, 0, 0
			var conversationID string
			provider, cleanup, err := pigo.RegisterKiroLiveAgentFixtureForTest(source, "tenant-a", func(raw []byte) {
				calls++
				var wire struct {
					ConversationState struct {
						ConversationID string `json:"conversationId"`
					} `json:"conversationState"`
				}
				if err := json.Unmarshal(raw, &wire); err != nil {
					t.Error(err)
				}
				id := wire.ConversationState.ConversationID
				if id == "" || id == "sensitive-session" {
					t.Error("unscoped ID")
				}
				if conversationID != "" && conversationID != id {
					t.Error("affinity changed")
				}
				conversationID = id
			}, delta, fail)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			definition := agent.AgentDefinition{DefaultModel: agent.ModelRef{Provider: string(provider), Model: "fixture-model"}}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			sink := func(event agent.AgentEvent) {
				if event.UpdateType == agent.AssistantEventTextDelta {
					updates++
					once.Do(func() { close(delta) })
				}
				if event.Type == agent.EventToolExecutionStart {
					tools++
				}
			}
			snapshot, err := agent.NewEngine().Run(ctx, definition, &agent.AgentSnapshot{SessionID: "sensitive-session"}, []agent.Message{agent.NewUserTextMessage("hello")}, sink)
			if updates == 0 || calls != 1 || tools != 0 {
				t.Fatal("no actual delta or unexpected retry/tool", updates, calls, tools)
			}
			if fail {
				if err == nil && snapshot.Error == "" {
					t.Fatal("truncated stream succeeded")
				}
				return
			}
			if err != nil || snapshot.Error != "" {
				t.Fatal("live agent", err)
			}
			next, err := agent.NewEngine().Run(ctx, definition, snapshot, []agent.Message{agent.NewUserTextMessage("again")}, sink)
			if err != nil || next.Error != "" || calls != 2 || tools != 0 {
				t.Fatal("second turn", err, calls)
			}
		})
	}
}
