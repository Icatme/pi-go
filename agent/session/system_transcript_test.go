package session

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/pkg/pigo"
)

func TestSystemTranscriptSnapshotsAreDetached(t *testing.T) {
	section := "original section"
	message := agent.NewSystemMessage(agent.SystemMessagePayload{
		Content:  "base prompt",
		Sections: map[string]*string{"policy": &section, "deleted": nil},
		ToolsAdded: []agent.ToolDeclaration{{
			ConstrainedSampling: &pigo.ToolConstrainedSampling{Type: "grammar", Syntax: "regex", Definition: "[a-z]+"},
			Name:                "lookup",
			Parameters:          map[string]any{"properties": map[string]any{"query": map[string]any{"type": "string"}}},
			OutputSchema:        map[string]any{"properties": map[string]any{"result": map[string]any{"type": "string"}}},
		}},
		ToolsRemoved: []agent.ToolReference{{Name: "retired"}},
	})
	state, err := Reduce([]LogItem{reducerEntryItem(1, MainLane, "system", "", message)})
	if err != nil {
		t.Fatal(err)
	}
	first, err := state.Context(MainLane)
	if err != nil {
		t.Fatal(err)
	}
	*first.Messages[0].System.Sections["policy"] = "mutated section"
	first.Messages[0].System.ToolsAdded[0].Parameters["properties"].(map[string]any)["query"].(map[string]any)["type"] = "number"
	first.Messages[0].System.ToolsAdded[0].OutputSchema["properties"].(map[string]any)["result"].(map[string]any)["type"] = "number"
	first.Messages[0].System.ToolsAdded[0].ConstrainedSampling.Definition = "mutated"
	first.Messages[0].System.ToolsRemoved[0].Name = "mutated removal"
	again, err := state.Context(MainLane)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again.Messages[0].System, message.System) {
		t.Fatalf("system snapshot aliased reducer state: %+v", again.Messages[0].System)
	}
	branch, err := state.Branch(MainLane)
	if err != nil {
		t.Fatal(err)
	}
	*branch[0].Message.System.Sections["policy"] = "branch mutation"
	again, _ = state.Context(MainLane)
	if *again.Messages[0].System.Sections["policy"] != "original section" {
		t.Fatal("branch snapshot aliased reducer state")
	}
}

func TestCompactionRetainsCurrentSystemDeclarations(t *testing.T) {
	for _, test := range storageCases() {
		t.Run(test.name, func(t *testing.T) {
			storage := test.new(t)
			session, err := New(storage, Options{})
			if err != nil {
				t.Fatal(err)
			}
			oldSection, currentSection := "old policy", "current policy"
			messages := []agent.Message{
				agent.NewSystemMessage(agent.SystemMessagePayload{
					Content: "base prompt", Sections: map[string]*string{"old": &oldSection},
					ToolsAdded: []agent.ToolDeclaration{{Name: "retired"}, {Name: "lookup", Parameters: map[string]any{"type": "object"}}},
				}),
				agent.NewTextMessage(agent.RoleUser, "old user"),
				agent.NewTextMessage(agent.RoleAssistant, "old answer"),
				agent.NewSystemMessage(agent.SystemMessagePayload{
					Content: "later instruction", Sections: map[string]*string{"old": nil, "current": &currentSection},
					ToolsRemoved: []agent.ToolReference{{Name: "retired"}},
					ToolsAdded:   []agent.ToolDeclaration{{Name: "lookup", OutputSchema: map[string]any{"type": "string"}}},
				}),
				agent.NewTextMessage(agent.RoleUser, "retained user"),
				agent.NewTextMessage(agent.RoleAssistant, "retained answer"),
			}
			for index, message := range messages {
				id := string(rune('a' + index))
				if _, err := storage.AppendEntry(MainLane, NewEntry{Type: EntryTypeMessage, ID: id, Message: &message}); err != nil {
					t.Fatal(err)
				}
			}
			state, err := Reduce(storage.Log())
			if err != nil {
				t.Fatal(err)
			}
			branch, err := state.Branch(MainLane)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := PrepareCompaction(branch, CompactionOptions{EstimateTokens: func(agent.Message) int64 { return 1 }})
			if err != nil || plan == nil {
				t.Fatalf("prepare compaction: plan=%+v err=%v", plan, err)
			}
			if plan.TokensBefore != 5 {
				t.Fatalf("system state must be estimated once: %d", plan.TokensBefore)
			}
			assertMessageTexts(t, plan.MessagesToSummarize, "old user", "old answer")
			if len(plan.RetainedTail) != 3 || plan.RetainedTail[0].Role != agent.RoleSystem {
				t.Fatalf("missing retained system prefix: %+v", plan.RetainedTail)
			}
			data, err := Compact(context.Background(), *plan, func(_ context.Context, request SummaryRequest) (SummaryResult, error) {
				assertMessageTexts(t, request.Messages, "old user", "old answer")
				return SummaryResult{Summary: "summary"}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := storage.AppendEntry(MainLane, NewEntry{Type: EntryTypeCompaction, ID: "compacted", Compaction: &data}); err != nil {
				t.Fatal(err)
			}
			view, err := session.Context(MainLane)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := agent.GetCurrentSystemPrompt(view.Messages), agent.GetCurrentSystemPrompt(messages); got != want {
				t.Fatalf("prompt changed across compaction: got %q want %q", got, want)
			}
			if got, want := agent.GetCurrentTools(view.Messages), agent.GetCurrentTools(messages); !reflect.DeepEqual(got, want) {
				t.Fatalf("tools changed across compaction: got %+v want %+v", got, want)
			}
			if _, exists := view.Messages[0].System.Sections["old"]; exists || len(view.Messages[0].System.ToolsRemoved) != 0 {
				t.Fatalf("old system deltas survived folding: %+v", view.Messages[0].System)
			}
		})
	}
}

func TestCompactManualPlanKeepsSystemStateOutOfSummary(t *testing.T) {
	plan := CompactionPlan{
		MessagesToSummarize: []agent.Message{
			agent.NewSystemMessage(agent.SystemMessagePayload{Content: "base", ToolsAdded: []agent.ToolDeclaration{{Name: "removed"}}}),
			agent.NewTextMessage(agent.RoleUser, "u1"),
			agent.NewTextMessage(agent.RoleAssistant, "a1"),
		},
		RetainedTail: []agent.Message{
			agent.NewSystemMessage(agent.SystemMessagePayload{Content: "update", ToolsRemoved: []agent.ToolReference{{Name: "removed"}}, ToolsAdded: []agent.ToolDeclaration{{Name: "active"}}}),
			agent.NewTextMessage(agent.RoleUser, "u2"),
			agent.NewTextMessage(agent.RoleAssistant, "a2"),
		},
	}
	data, err := Compact(context.Background(), plan, func(_ context.Context, request SummaryRequest) (SummaryResult, error) {
		assertMessageTexts(t, request.Messages, "u1", "a1")
		return SummaryResult{Summary: "summary"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := agent.GetCurrentSystemPrompt(data.RetainedTail); got != "base\n\nupdate" {
		t.Fatalf("prompt = %q", got)
	}
	if tools := agent.GetCurrentTools(data.RetainedTail); len(tools) != 1 || tools[0].Name != "active" {
		t.Fatalf("tool changes lost: %+v", tools)
	}
}

func TestEstimateMessageTokensIncludesSystemSchema(t *testing.T) {
	message := agent.NewSystemMessage(agent.SystemMessagePayload{
		Content:    strings.Repeat("policy ", 50),
		ToolsAdded: []agent.ToolDeclaration{{Name: "lookup", Description: strings.Repeat("search ", 50)}},
	})
	if got := EstimateMessageTokens(message); got < 100 {
		t.Fatalf("system content and tools omitted from token estimate: %d", got)
	}
}
