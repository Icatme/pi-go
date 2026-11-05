package superagent

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

func TestPiMainResumesHistoryAndStripsExecutionAuthority(t *testing.T) {
	var calls atomic.Int32
	tripwire := errors.New("execution authority leaked")
	def := agent.AgentDefinition{
		DefaultModel: agent.ModelRef{Provider: "fixture", Model: "main", ProviderConfig: agent.ProviderConfig{APIKey: "do-not-persist"}},
		Tools:        []agent.ToolDefinition{{Name: "write"}},
		ToolResolver: func(context.Context, agent.AgentSnapshot) ([]agent.ToolDefinition, error) { return nil, tripwire },
		PrepareRequest: func(context.Context, agent.PrepareRequestContext) (*agent.AgentLoopTurnUpdate, error) {
			return nil, tripwire
		},
		PrepareNextTurn: func(context.Context, agent.PrepareNextTurnContext) (*agent.AgentLoopTurnUpdate, error) {
			return nil, tripwire
		},
		Model: agent.StreamFunc(func(_ context.Context, req agent.ModelRequest) (agent.AssistantStream, error) {
			if len(req.Tools) != 0 {
				return nil, tripwire
			}
			var input mainModelInput
			if err := json.Unmarshal([]byte(messageText(req.Messages[len(req.Messages)-1])), &input); err != nil {
				return nil, err
			}
			turn := calls.Add(1)
			if turn == 1 {
				return offlineAnswer(`{"action":"new"}`), nil
			}
			if input.Input.Kind != "worker_result" || input.Input.Report == nil || len(req.Messages) < 3 {
				return nil, errors.New("main session lost prior delegation/history")
			}
			return offlineAnswer(`{"action":"reply","text":"Verified result"}`), nil
		}),
	}
	router, err := PiMainRouter(def, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "state.jsonl")
	a, err := OpenMain(t.Context(), path, 1, router, mainWorker)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "1", Source: "im", Thread: "chat", Kind: "request"})
	waitIdle(t, a)
	if calls.Load() != 2 || len(a.Replies()) != 1 || len(a.MainFailures()) != 0 {
		t.Fatalf("calls=%d failures=%+v", calls.Load(), a.MainFailures())
	}
	snapshot, ok, err := a.MainSnapshot()
	if err != nil || !ok || len(snapshot.Messages) < 4 {
		t.Fatalf("snapshot: %+v %v", snapshot, err)
	}
	data, err := json.Marshal(snapshot)
	if err != nil || strings.Contains(string(data), "do-not-persist") || strings.Contains(string(data), "not for main") {
		t.Fatal("credential or worker transcript persisted in main")
	}
	userIndex := -1
	for i, message := range snapshot.Messages {
		if message.Role == agent.RoleUser && len(message.Parts) > 0 {
			userIndex = i
			break
		}
	}
	if userIndex < 0 {
		t.Fatal("main snapshot has no input message")
	}
	snapshot.Messages[userIndex].Parts[0].Text = "read mutation"
	copy, _, err := a.MainSnapshot()
	if err != nil || copy.Messages[userIndex].Parts[0].Text == "read mutation" {
		t.Fatal("main snapshot read aliases state")
	}
}

func TestPiMainContextBudgetRejectsBeforeModelCall(t *testing.T) {
	var calls atomic.Int32
	router, err := PiMainRouter(agent.AgentDefinition{Model: agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
		calls.Add(1)
		return offlineAnswer(`{"action":"ignore"}`), nil
	})}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	job := MainJob{SessionID: "main", ProviderSessionID: "wire", Snapshot: agent.AgentSnapshot{SessionID: "main"}, Input: MainInput{Trigger: Trigger{Text: strings.Repeat("x", 2048)}}}
	if _, err := router(t.Context(), job); err == nil || calls.Load() != 0 {
		t.Fatal("budget exceeded after model was called")
	}
	job.Input.Trigger.Text = "small"
	job.Snapshot.PendingToolCalls = []agent.PendingToolCall{{}}
	if _, err := router(t.Context(), job); err == nil || calls.Load() != 0 {
		t.Fatal("pending execution admitted")
	}
}

func TestPiMainContextBudgetCountsEscapedMessageEnvelope(t *testing.T) {
	job := MainJob{SessionID: "main", ProviderSessionID: "wire", Snapshot: agent.AgentSnapshot{SessionID: "main"}, Input: MainInput{ID: "input", Kind: "user", Trigger: Trigger{Text: strings.Repeat("\"\\\n", 100)}}}
	input, err := json.Marshal(mainModelInput{Input: job.Input, Tasks: job.Tasks})
	if err != nil {
		t.Fatal(err)
	}
	messages := []agent.Message{agent.NewUserTextMessage(string(input))}
	serialized, err := json.Marshal(struct {
		SystemPrompt string          `json:"system_prompt"`
		Messages     []agent.Message `json:"messages"`
	}{mainInstructions, messages})
	if err != nil {
		t.Fatal(err)
	}
	if err := checkMainContextBudget(mainInstructions, messages, len(serialized)-1); err == nil {
		t.Fatal("escaped context exceeded exact budget")
	}
	if err := checkMainContextBudget(mainInstructions, messages, len(serialized)); err != nil {
		t.Fatalf("exact context bound rejected: %v", err)
	}
	var calls atomic.Int32
	router, err := PiMainRouter(agent.AgentDefinition{Model: agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
		calls.Add(1)
		return offlineAnswer(`{"action":"ignore"}`), nil
	})}, len(serialized)-32)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := router(t.Context(), job); err == nil || calls.Load() != 0 {
		t.Fatal("escaped context over budget reached model")
	}
}

func TestPiMainDecisionParserRejectsAmbiguousObjects(t *testing.T) {
	for _, raw := range []string{
		`{"action":"new","action":"ignore"}`, `{"Action":"new"}`,
		`{"action":"reply","text":"ok","source":"other"}`, `{"action":"reply","text":null}`,
		`{"action":"reply","text":"ok","task_id":"task-1"}`, `{"action":"new","text":""}`,
		`{"action":"attach","task_id":""}`, `{"action":"reply","text":" "}`,
		`{"action":"reply","text":"ok"} {}`, `[]`,
	} {
		if _, err := parseMainDecision(raw); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	for _, raw := range []string{`{"action":"new"}`, `{"action":"attach","task_id":"task-1"}`, `{"action":"reply","text":"ok"}`, `{"action":"ignore"}`} {
		if _, err := parseMainDecision(raw); err != nil {
			t.Errorf("rejected %s: %v", raw, err)
		}
	}
}

func TestOfflinePiMainDelegatesAndReplies(t *testing.T) {
	def := agent.AgentDefinition{Model: OfflineModel()}
	router, err := PiMainRouter(def, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := PiWorker(def)
	if err != nil {
		t.Fatal(err)
	}
	a, err := OpenMain(t.Context(), filepath.Join(t.TempDir(), "state.jsonl"), 1, router, worker)
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	submit(t, a, Trigger{ID: "1", Source: "im", Thread: "chat", Kind: "request", Text: "Check the build"})
	waitIdle(t, a)
	if got := a.Replies(); len(got) != 1 || !strings.Contains(got[0].Text, "completed") || len(a.MainFailures()) != 0 {
		t.Fatalf("replies=%+v failures=%+v", got, a.MainFailures())
	}
}

func TestPiMainControlParserRequiresExactNestedTarget(t *testing.T) {
	target := `{"task_id":"task-1","source":"im","event_id":"start"}`
	for _, raw := range []string{
		`{"action":"steer","target":` + target + `,"text":"correct"}`,
		`{"action":"cancel","target":` + target + `}`,
		`{"action":"clarify","text":"Which task?"}`,
	} {
		if _, err := parseMainDecision(raw); err != nil {
			t.Fatalf("rejected %s: %v", raw, err)
		}
	}
	for _, raw := range []string{
		`{"action":"cancel","target":null}`,
		`{"action":"cancel","target":{"task_id":"task-1","source":"im"}}`,
		`{"action":"cancel","target":{"task_id":"task-1","source":"im","event_id":"start","event_id":"later"}}`,
		`{"action":"cancel","target":{"task_id":"task-1","source":"im","Event_ID":"start"}}`,
		`{"action":"cancel","target":{"task_id":"task-1","source":"im","event_id":3}}`,
		`{"action":"cancel","target":` + target + `,"target":` + target + `}`,
		`{"action":"cancel","target":` + target + `,"text":"extra"}`,
		`{"action":"steer","target":` + target + `}`,
		`{"action":"steer","target":` + target + `,"text":" "}`,
		`{"action":"reply","target":` + target + `}`,
	} {
		if _, err := parseMainDecision(raw); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}

func TestPiMainModelReceivesExactRunTargets(t *testing.T) {
	target := RunTarget{TaskID: "task-1", Source: "im", EventID: "start"}
	router, err := PiMainRouter(agent.AgentDefinition{Model: agent.StreamFunc(func(_ context.Context, req agent.ModelRequest) (agent.AssistantStream, error) {
		var input mainModelInput
		if err := json.Unmarshal([]byte(messageText(req.Messages[len(req.Messages)-1])), &input); err != nil {
			return nil, err
		}
		if len(input.Runs) != 1 || input.Runs[0] != target {
			t.Errorf("runs: %+v", input.Runs)
		}
		return offlineAnswer(`{"action":"cancel","target":{"task_id":"task-1","source":"im","event_id":"start"}}`), nil
	})}, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	out, err := router(t.Context(), MainJob{SessionID: "main", ProviderSessionID: "provider", Snapshot: agent.AgentSnapshot{SessionID: "main"}, Runs: []RunTarget{target}})
	if err != nil || out.Decision.Target == nil || *out.Decision.Target != target {
		t.Fatalf("target: %+v %v", out.Decision, err)
	}
}
