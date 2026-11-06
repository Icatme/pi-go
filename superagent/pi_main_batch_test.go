package superagent

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Icatme/pi-go/agent"
)

func piBatchJob() MainBatchJob {
	return MainBatchJob{SessionID: "main", ProviderSessionID: "wire", Snapshot: agent.AgentSnapshot{SessionID: "main"}, Inputs: []MainInput{{ID: "one", Kind: "user", Trigger: Trigger{Text: "First request"}}, {ID: "two", Kind: "worker_result", Report: &Report{}}}, Tasks: []TaskInfo{{ID: "task-1", Summary: "existing"}}, Runs: []RunTarget{{TaskID: "task-1", Source: "im", EventID: "start"}}}
}

const piBatchAnswer = `{"decisions":[{"input_id":"one","decision":{"action":"new"}},{"input_id":"two","decision":{"action":"reply","text":"partial result"}}]}`

func TestPiMainBatchOneCallResumesHistoryAndIsolatesAuthority(t *testing.T) {
	var calls, hooks atomic.Int32
	tripwire := errors.New("execution authority leaked")
	def := agent.AgentDefinition{
		SystemPrompt: "Host policy", DefaultModel: agent.ModelRef{Provider: "fixture", Model: "main", ProviderConfig: agent.ProviderConfig{APIKey: "host-secret"}},
		Tools: []agent.ToolDefinition{{Name: "write"}},
		ToolResolver: func(context.Context, agent.AgentSnapshot) ([]agent.ToolDefinition, error) {
			hooks.Add(1)
			return nil, tripwire
		},
		PrepareRequest: func(context.Context, agent.PrepareRequestContext) (*agent.AgentLoopTurnUpdate, error) {
			hooks.Add(1)
			return nil, tripwire
		},
		PrepareNextTurn: func(context.Context, agent.PrepareNextTurnContext) (*agent.AgentLoopTurnUpdate, error) {
			hooks.Add(1)
			return nil, tripwire
		},
		FinishTurn: func(context.Context, agent.AgentTurnContext) (agent.AgentTurnDecision, error) {
			hooks.Add(1)
			return agent.AgentTurnDecision{}, tripwire
		},
		TransformContext: func(context.Context, []agent.Message) ([]agent.Message, error) { hooks.Add(1); return nil, tripwire },
		ConvertToLLM:     func(context.Context, []agent.Message) ([]agent.Message, error) { hooks.Add(1); return nil, tripwire },
	}
	job := piBatchJob()
	job.Snapshot.Model = agent.ModelRef{Provider: "fixture", Model: "main", ProviderConfig: agent.ProviderConfig{APIKey: "snapshot-secret"}}
	job.Snapshot.Messages = []agent.Message{agent.NewUserTextMessage("Prior context")}
	before, _ := json.Marshal(job)
	def.Model = agent.StreamFunc(func(_ context.Context, req agent.ModelRequest) (agent.AssistantStream, error) {
		n := calls.Add(1)
		if req.SessionID != "wire" || len(req.Tools) != 0 || req.Model.ProviderConfig.APIKey != "host-secret" {
			t.Errorf("identity/authority leaked: session=%s tools=%d", req.SessionID, len(req.Tools))
		}
		if !strings.Contains(req.SystemPrompt, mainBatchInstructions) || !strings.Contains(req.SystemPrompt, "Host policy") {
			t.Error("missing batch or host instructions")
		}
		var got mainBatchModelInput
		if err := json.Unmarshal([]byte(messageText(req.Messages[len(req.Messages)-1])), &got); err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(got.Inputs, job.Inputs) || !reflect.DeepEqual(got.Tasks, job.Tasks) || !reflect.DeepEqual(got.Runs, job.Runs) {
			t.Error("batch prompt lost input or offered context")
		}
		if len(req.Messages) != int(2*n+1) || messageText(req.Messages[1]) != "Prior context" {
			t.Errorf("lost history: %d messages", len(req.Messages))
		}
		return offlineAnswer(piBatchAnswer), nil
	})
	router, err := PiMainBatchRouter(def, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	out, err := router(t.Context(), job)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || hooks.Load() != 0 || len(out.Decisions) != 2 || out.Snapshot.SessionID != "main" {
		t.Fatalf("calls=%d hooks=%d out=%+v", calls.Load(), hooks.Load(), out)
	}
	after, _ := json.Marshal(job)
	if string(before) != string(after) {
		t.Fatal("callback mutated caller-owned job")
	}
	persisted, _ := json.Marshal(out.Snapshot)
	if strings.Contains(string(persisted), "secret") {
		t.Fatal("credential persisted")
	}
	job.Snapshot = out.Snapshot
	if _, err := router(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 || hooks.Load() != 0 {
		t.Fatalf("calls=%d hooks=%d", calls.Load(), hooks.Load())
	}
}

func TestPiMainBatchFullContextBudget(t *testing.T) {
	job := piBatchJob()
	job.Inputs[0].Trigger.Text = strings.Repeat("\"\\\n", 100)
	job.Snapshot.Messages = []agent.Message{agent.NewUserTextMessage("preserve causal history")}
	payload, _ := json.Marshal(mainBatchModelInput{Inputs: job.Inputs, Tasks: job.Tasks, Runs: job.Runs})
	messages := append([]agent.Message{}, job.Snapshot.Messages...)
	messages = append(messages, agent.NewUserTextMessage(string(payload)))
	prompt := "Host policy\n\n" + mainInstructions + "\n\n" + mainBatchInstructions
	encoded, _ := json.Marshal(struct {
		SystemPrompt string          `json:"system_prompt"`
		Messages     []agent.Message `json:"messages"`
	}{prompt, messages})
	// The router creates a timestamped message; allow for RFC3339 fractional precision.
	if err := checkMainContextBudget(prompt, messages, len(encoded)-1); err == nil {
		t.Fatal("exact serialized budget not enforced")
	}
	if err := checkMainContextBudget(prompt, messages, len(encoded)); err != nil {
		t.Fatal(err)
	}
	for _, delta := range []int{-32, 32} {
		var calls atomic.Int32
		router, err := PiMainBatchRouter(agent.AgentDefinition{SystemPrompt: "Host policy", Model: agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
			calls.Add(1)
			return offlineAnswer(piBatchAnswer), nil
		})}, len(encoded)+delta)
		if err != nil {
			t.Fatal(err)
		}
		_, err = router(t.Context(), job)
		if delta < 0 && (err == nil || calls.Load() != 0) {
			t.Fatal("over-budget context reached model")
		}
		if delta > 0 && (err != nil || calls.Load() != 1) {
			t.Fatalf("sufficient budget failed: %v", err)
		}
	}
	for _, budget := range []int{1023, maxSnapshotBytes + 1} {
		if _, err := PiMainBatchRouter(agent.AgentDefinition{}, budget); err == nil {
			t.Fatal("invalid budget accepted")
		}
	}
}

func TestPiMainBatchRejectsInvalidJobBeforeCallback(t *testing.T) {
	var calls atomic.Int32
	router, err := PiMainBatchRouter(agent.AgentDefinition{Model: agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
		calls.Add(1)
		return offlineAnswer(piBatchAnswer), nil
	})}, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]func(*MainBatchJob){
		"empty":            func(j *MainBatchJob) { j.Inputs = nil },
		"too many":         func(j *MainBatchJob) { j.Inputs = make([]MainInput, 65) },
		"duplicate":        func(j *MainBatchJob) { j.Inputs[1].ID = j.Inputs[0].ID },
		"blank":            func(j *MainBatchJob) { j.Inputs[0].ID = " " },
		"long":             func(j *MainBatchJob) { j.Inputs[0].ID = strings.Repeat("a", 257) },
		"invalid utf8":     func(j *MainBatchJob) { j.Inputs[0].ID = string([]byte{255}) },
		"session":          func(j *MainBatchJob) { j.SessionID = "" },
		"provider":         func(j *MainBatchJob) { j.ProviderSessionID = "" },
		"foreign snapshot": func(j *MainBatchJob) { j.Snapshot.SessionID = "another" },
		"pending tools":    func(j *MainBatchJob) { j.Snapshot.PendingToolCalls = []agent.PendingToolCall{{}} },
		"pending control":  func(j *MainBatchJob) { j.Snapshot.PendingToolControl = &agent.PendingToolControl{} },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			job := piBatchJob()
			change(&job)
			if _, err := router(t.Context(), job); err == nil {
				t.Fatal("invalid job accepted")
			}
		})
	}
	if _, err := router(nil, piBatchJob()); err == nil {
		t.Fatal("nil context accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("invalid job reached callback")
	}
}

func TestPiMainBatchStrictDecisionEnvelope(t *testing.T) {
	inputs := piBatchJob().Inputs
	entry := `{"input_id":"one","decision":{"action":"ignore"}}`
	other := `{"input_id":"two","decision":{"action":"ignore"}}`
	wrap := func(s string) string { return `{"decisions":[` + s + `,` + other + `]}` }
	bad := []string{
		`[]`, `null`, `{}`, `{"Decisions":[]}`, `{"decisions":null}`, `{"decisions":{}}`,
		`{"decisions":[]}`, `{"decisions":[` + entry + `]}`, `{"decisions":[` + entry + `,` + other + `,` + other + `]}`,
		wrap(entry) + ` {}`,
		`{"decisions":[` + entry + `,` + other + `],"decisions":[]}`,
		`{"decisions":[` + entry + `,` + other + `],"unknown":true}`,
		wrap(`{"input_id":"one","input_id":"one","decision":{"action":"ignore"}}`),
		wrap(`{"Input_ID":"one","decision":{"action":"ignore"}}`),
		wrap(`{"input_id":null,"decision":{"action":"ignore"}}`),
		wrap(`{"input_id":1,"decision":{"action":"ignore"}}`),
		wrap(`{"input_id":"foreign","decision":{"action":"ignore"}}`),
		wrap(`{"input_id":" ","decision":{"action":"ignore"}}`),
		wrap(`{"input_id":"one"}`), wrap(`{"decision":{"action":"ignore"}}`),
		wrap(`{"input_id":"one","decision":null}`),
		wrap(`{"input_id":"one","Decision":{"action":"ignore"}}`),
		wrap(`{"input_id":"one","decision":{"action":"ignore"},"decision":{"action":"new"}}`),
		wrap(`{"input_id":"one","decision":{"action":"ignore"},"source":"im"}`),
		wrap(`{"input_id":"one","decision":{"action":"ignore","action":"new"}}`),
		wrap(`{"input_id":"one","decision":{"Action":"ignore"}}`),
		wrap(`{"input_id":"one","decision":{"action":"ignore","unknown":true}}`),
		wrap(`{"input_id":"one","decision":{"action":"cancel","target":{"task_id":"t","source":"im","event_id":"1","event_id":"2"}}}`),
		`{"decisions":[` + entry + `,` + entry + `]}`,
	}
	for _, raw := range bad {
		if _, err := parseMainBatchDecisions(raw, inputs); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	for _, raw := range []string{piBatchAnswer, `{"decisions":[` + other + `,` + entry + `]}`, wrap(`{"decision":{"action":"ignore"},"input_id":"one"}`)} {
		if _, err := parseMainBatchDecisions(raw, inputs); err != nil {
			t.Errorf("rejected %s: %v", raw, err)
		}
	}
}

func TestPiMainBatchInvalidResultReturnsNoPartialOutcome(t *testing.T) {
	job := piBatchJob()
	before, _ := json.Marshal(job)
	router, err := PiMainBatchRouter(agent.AgentDefinition{Model: agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
		return offlineAnswer(`{"decisions":[{"input_id":"one","decision":{"action":"new"}}]}`), nil
	})}, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	out, err := router(t.Context(), job)
	if err == nil || !reflect.DeepEqual(out, MainBatchOutcome{}) {
		t.Fatalf("partial result escaped: %+v %v", out, err)
	}
	after, _ := json.Marshal(job)
	if string(before) != string(after) {
		t.Fatal("failed callback mutated job")
	}
}

func TestPiMainBatchModelToolRequestCannotExecute(t *testing.T) {
	var effects, calls atomic.Int32
	def := agent.AgentDefinition{
		Tools: []agent.ToolDefinition{{Name: "write", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
			effects.Add(1)
			return agent.ToolResult{}, nil
		}}},
		BeforeToolCall: func(context.Context, agent.BeforeToolCallContext) (agent.BeforeToolCallResult, error) {
			effects.Add(1)
			return agent.BeforeToolCallResult{}, nil
		},
		AfterToolCall: func(context.Context, agent.AfterToolCallContext) (agent.AfterToolCallResult, error) {
			effects.Add(1)
			return agent.AfterToolCallResult{}, nil
		},
		CheckToolPermission: func(context.Context, agent.BeforeToolCallContext) error { effects.Add(1); return nil },
		Model: agent.StreamFunc(func(context.Context, agent.ModelRequest) (agent.AssistantStream, error) {
			calls.Add(1)
			msg := agent.NewTextMessage(agent.RoleAssistant, piBatchAnswer)
			msg.StopReason = agent.StopReasonToolUse
			msg.ToolCalls = []agent.ToolCall{{ID: "call", Name: "write", Arguments: json.RawMessage(`{}`)}}
			events := make(chan agent.AssistantEvent, 1)
			events <- agent.AssistantEvent{Type: agent.AssistantEventDone, Message: msg}
			close(events)
			return &offlineStream{final: msg, events: events}, nil
		}),
	}
	router, err := PiMainBatchRouter(def, 64<<10)
	if err != nil {
		t.Fatal(err)
	}
	out, err := router(t.Context(), piBatchJob())
	if err == nil || !reflect.DeepEqual(out, MainBatchOutcome{}) || effects.Load() != 0 || calls.Load() != 1 {
		t.Fatalf("tool request escaped read-only callback: out=%+v err=%v effects=%d calls=%d", out, err, effects.Load(), calls.Load())
	}
}
