package agent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	pigo "github.com/Icatme/pi-go/pkg/pigo"
)

func TestAgentEffectiveThinkingTracksModelSwitch(t *testing.T) {
	runtime, err := NewAgent(AgentDefinition{DefaultModel: ModelRef{Provider: "google", Model: "gemini-2.0-flash"}, ThinkingLevel: ThinkingHigh})
	if err != nil {
		t.Fatal(err)
	}
	if got := runtime.State().ThinkingLevel; got != ThinkingOff {
		t.Fatalf("non-reasoning model exposed requested level %q", got)
	}
	runtime.SetModel(ModelRef{Provider: "google", Model: "gemini-2.5-flash"})
	if got := runtime.State().ThinkingLevel; got != ThinkingHigh {
		t.Fatalf("model switch lost requested preference: %q", got)
	}
	runtime.SetModel(ModelRef{Provider: "google", Model: "gemini-2.0-flash"})
	runtime.SetThinkingLevel(ThinkingMax)
	if got := runtime.State().ThinkingLevel; got != ThinkingOff {
		t.Fatalf("setter exposed unsupported level %q", got)
	}
}

func TestThinkingPreferencesSurviveSnapshotRestore(t *testing.T) {
	runtime, err := NewAgentWithOptions(AgentOptions{InitialState: AgentInitialState{ModelRef: ModelRef{Provider: "google", Model: "gemini-2.0-flash"}, ThinkingLevel: ThinkingMax}})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := runtime.Snapshot()
	if snapshot.RequestedThinkingLevel != ThinkingMax || snapshot.ThinkingLevel != ThinkingOff {
		t.Fatalf("preference/effective mismatch: %+v", snapshot)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var restored AgentSnapshot
	if err := json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	restored.ThinkingLevel = ThinkingHigh // Effective state is derived again, never trusted on restore.
	next, err := NewAgent(AgentDefinition{ThinkingLevel: ThinkingLow}, WithSnapshot(restored))
	if err != nil {
		t.Fatal(err)
	}
	if got := next.State(); got.RequestedThinkingLevel != ThinkingMax || got.ThinkingLevel != ThinkingOff {
		t.Fatalf("restored stale effective level: %+v", got)
	}
	next.SetModel(ModelRef{Provider: "google", Model: "gemini-2.5-flash"})
	if got := next.State(); got.RequestedThinkingLevel != ThinkingMax || got.ThinkingLevel != ThinkingHigh {
		t.Fatalf("lost preference when switching back: %+v", got)
	}
}

func TestThinkingResolutionForCustomBackends(t *testing.T) {
	for _, backend := range []string{"model", "stream", "resolver"} {
		for _, explicit := range []bool{false, true} {
			t.Run(backend+map[bool]string{false: "-identity", true: "-explicit"}[explicit], func(t *testing.T) {
				var received ThinkingLevel
				stream := StreamFunc(func(_ context.Context, request ModelRequest) (AssistantStream, error) {
					received = request.ThinkingLevel
					return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop}, nil), nil
				})
				options := AgentOptions{InitialState: AgentInitialState{ModelRef: ModelRef{Provider: "google", Model: "gemini-2.0-flash"}, ThinkingLevel: ThinkingMax}}
				switch backend {
				case "model":
					options.Model = stream
				case "stream":
					options.Stream = stream
				case "resolver":
					options.ModelResolver = func(context.Context, ModelRef, AgentSnapshot) (StreamModel, error) { return stream, nil }
				}
				expected := ThinkingMax
				if explicit {
					expected = ThinkingLow
					options.ThinkingLevelResolver = func(ModelRef, ThinkingLevel) ThinkingLevel { return ThinkingLow }
				}
				runtime, err := NewAgentWithOptions(options)
				if err != nil {
					t.Fatal(err)
				}
				if got := runtime.State(); got.RequestedThinkingLevel != ThinkingMax || got.ThinkingLevel != expected {
					t.Fatalf("initial thinking=%+v", got)
				}
				if err := runtime.PromptText(context.Background(), "run"); err != nil {
					t.Fatal(err)
				}
				if received != expected {
					t.Fatalf("request thinking=%q want %q", received, expected)
				}
			})
		}
	}
	runtime, err := NewAgent(AgentDefinition{Model: StreamFunc(func(context.Context, ModelRequest) (AssistantStream, error) { return nil, errors.New("unused") })})
	if err != nil {
		t.Fatal(err)
	}
	if got := runtime.State(); got.RequestedThinkingLevel != ThinkingOff || got.ThinkingLevel != ThinkingOff {
		t.Fatalf("default is not off: %+v", got)
	}
}

func TestEngineThinkingSnapshotAndTurnUpdates(t *testing.T) {
	for _, resume := range []bool{false, true} {
		t.Run(map[bool]string{false: "run", true: "resume"}[resume], func(t *testing.T) {
			var requests []ModelRequest
			model := StreamFunc(func(_ context.Context, request ModelRequest) (AssistantStream, error) {
				requests = append(requests, request)
				message := Message{Role: RoleAssistant, StopReason: StopReasonStop}
				if !resume && len(requests) == 1 {
					message.ToolCalls = []ToolCall{{ID: "call", Name: "echo"}}
					message.StopReason = StopReasonToolUse
				}
				return newStaticAssistantStream(message, nil), nil
			})
			definition := AgentDefinition{
				Model: model, DefaultModel: ModelRef{Model: "base"}, ThinkingLevel: ThinkingLow,
				ThinkingLevelResolver: func(ref ModelRef, requested ThinkingLevel) ThinkingLevel {
					if ref.Model == "next" && requested == ThinkingMax {
						return ThinkingHigh
					}
					return requested
				},
				Tools: []ToolDefinition{{Name: "echo", Execute: terminatingTool(false)}},
				PrepareNextTurn: func(context.Context, PrepareNextTurnContext) (*AgentLoopTurnUpdate, error) {
					ref := ModelRef{Model: "next"}
					level := ThinkingMax
					return &AgentLoopTurnUpdate{ModelRef: &ref, ThinkingLevel: &level}, nil
				},
			}
			snapshot := AgentSnapshot{Model: ModelRef{Model: "base"}, RequestedThinkingLevel: ThinkingMedium, ThinkingLevel: ThinkingMax}
			var next *AgentSnapshot
			var err error
			if resume {
				assistant := Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "call", Name: "echo"}}, StopReason: StopReasonToolUse}
				snapshot.Messages = []Message{NewUserTextMessage("run"), assistant}
				snapshot.PendingToolCalls = []PendingToolCall{{ToolCallID: "call", ToolName: "echo"}}
				setPendingToolControlState(&snapshot, 1, assistant)
				next, err = NewEngine().ResumePendingToolCallsWithHooks(context.Background(), definition, &snapshot, nil, LoopHooks{ToolGate: func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
					return ToolGateResult{Action: ToolGateActionAllow}, nil
				}})
			} else {
				next, err = NewEngine().Run(context.Background(), definition, &snapshot, []Message{NewUserTextMessage("run")}, nil)
			}
			if err != nil {
				t.Fatal(err)
			}
			if !resume && (len(requests) != 2 || requests[0].ThinkingLevel != ThinkingMedium) {
				t.Fatalf("snapshot preference not applied: %+v", requests)
			}
			last := requests[len(requests)-1]
			if last.Model.Model != "next" || last.ThinkingLevel != ThinkingHigh {
				t.Fatalf("turn update did not resolve effective model/thinking: %+v", last)
			}
			if next.Model.Model != "base" || next.RequestedThinkingLevel != ThinkingMedium || next.ThinkingLevel != ThinkingMedium {
				t.Fatalf("turn override leaked into durable snapshot: %+v", next)
			}
		})
	}
}

func TestAgentThinkingSettersApplyAtNextTurnAndSurviveCompletion(t *testing.T) {
	var runtime *Agent
	var requests []ModelRequest
	model := StreamFunc(func(_ context.Context, request ModelRequest) (AssistantStream, error) {
		requests = append(requests, request)
		message := Message{Role: RoleAssistant, StopReason: StopReasonStop}
		if len(requests) == 1 {
			runtime.SetModel(ModelRef{Model: "next"})
			runtime.SetThinkingLevel(ThinkingMax)
			message.ToolCalls = []ToolCall{{ID: "call", Name: "echo"}}
			message.StopReason = StopReasonToolUse
		}
		return newStaticAssistantStream(message, nil), nil
	})
	var err error
	runtime, err = NewAgent(AgentDefinition{Model: model, DefaultModel: ModelRef{Model: "base"}, ThinkingLevel: ThinkingLow, Tools: []ToolDefinition{{Name: "echo", Execute: terminatingTool(false)}}, ThinkingLevelResolver: func(ref ModelRef, requested ThinkingLevel) ThinkingLevel {
		if ref.Model == "next" {
			return ThinkingHigh
		}
		return requested
	}})
	if err != nil {
		t.Fatal(err)
	}
	runtime.Subscribe(func(event AgentEvent) {
		if event.Type == EventAgentEnd {
			runtime.SetModel(ModelRef{Model: "last"})
			runtime.SetThinkingLevel(ThinkingMedium)
		}
	})
	if err := runtime.PromptText(context.Background(), "run"); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[1].Model.Model != "next" || requests[1].ThinkingLevel != ThinkingHigh {
		t.Fatalf("next turn missed live setters: %+v", requests)
	}
	if got := runtime.Snapshot(); got.Model.Model != "last" || got.RequestedThinkingLevel != ThinkingMedium || got.ThinkingLevel != ThinkingMedium {
		t.Fatalf("completion overwrote setter: %+v", got)
	}
}

func TestAgentThinkingStateIsAtomicDuringModelSwitches(t *testing.T) {
	runtime, err := NewAgent(AgentDefinition{DefaultModel: ModelRef{Provider: "google", Model: "gemini-2.0-flash"}, ThinkingLevel: ThinkingMax})
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for i := 0; i < 400; i++ {
			runtime.SetModel(ModelRef{Provider: "google", Model: "gemini-2.5-flash"})
			runtime.SetModel(ModelRef{Provider: "google", Model: "gemini-2.0-flash"})
		}
	}()
	for i := 0; i < 400; i++ {
		state := runtime.State()
		snapshot := runtime.Snapshot()
		for _, observed := range []AgentSnapshot{{Model: state.Model, RequestedThinkingLevel: state.RequestedThinkingLevel, ThinkingLevel: state.ThinkingLevel}, snapshot} {
			expected := ThinkingOff
			if observed.Model.Model == "gemini-2.5-flash" {
				expected = ThinkingHigh
			}
			if observed.RequestedThinkingLevel != ThinkingMax || observed.ThinkingLevel != expected {
				t.Errorf("torn model/thinking read: %+v", observed)
				break
			}
		}
	}
	workers.Wait()
}

func TestAgentDeepSeekOffIsExplicitOnTheWire(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"id\":\"test\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer server.Close()
	runtime, err := NewAgent(AgentDefinition{DefaultModel: ModelRef{Provider: "deepseek", Model: "deepseek-v4-pro", ProviderConfig: ProviderConfig{BaseURL: server.URL, APIKey: "test"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.PromptText(context.Background(), "run"); err != nil {
		t.Fatal(err)
	}
	if state := runtime.State(); state.Error != "" || state.ThinkingLevel != ThinkingOff || state.RequestedThinkingLevel != ThinkingOff {
		t.Fatalf("unexpected state: %+v", state)
	}
	thinking, _ := payload["thinking"].(map[string]any)
	if thinking["type"] != "disabled" {
		t.Fatalf("off became default thinking: %#v", payload)
	}
	if _, exists := payload["reasoning_effort"]; exists {
		t.Fatalf("disabled thinking still has effort: %#v", payload)
	}
	anthropic := buildPigoStreamOptions(context.Background(), ModelRequest{ThinkingLevel: ThinkingOff}, pigo.Provider("anthropic"))
	if anthropic.Reasoning != "" {
		t.Fatalf("DeepSeek off encoding leaked to Anthropic: %+v", anthropic)
	}
}

func TestThinkingStateUsesResolvedFallbackModel(t *testing.T) {
	var request ModelRequest
	runtime, err := NewAgent(AgentDefinition{
		DefaultModel: ModelRef{Model: "fallback"}, ThinkingLevel: ThinkingMax,
		Model: StreamFunc(func(_ context.Context, input ModelRequest) (AssistantStream, error) {
			request = input
			return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop}, nil), nil
		}),
		ThinkingLevelResolver: func(ref ModelRef, requested ThinkingLevel) ThinkingLevel {
			if ref.Model == "fallback" {
				return ThinkingOff
			}
			return requested
		},
	}, WithSnapshot(AgentSnapshot{Model: ModelRef{Provider: "incomplete"}}))
	if err != nil {
		t.Fatal(err)
	}
	if state := runtime.State(); state.Model.Model != "fallback" || state.ThinkingLevel != ThinkingOff {
		t.Fatalf("state disagrees with fallback model: %+v", state)
	}
	if err := runtime.PromptText(context.Background(), "run"); err != nil {
		t.Fatal(err)
	}
	if request.Model.Model != "fallback" || request.ThinkingLevel != ThinkingOff {
		t.Fatalf("request disagrees with resolved state: %+v", request)
	}
}

func TestAgentErrorSettlementPreservesThinkingSetter(t *testing.T) {
	runtime, err := NewAgent(AgentDefinition{Model: StreamFunc(func(context.Context, ModelRequest) (AssistantStream, error) { return nil, errors.New("model failed") })})
	if err != nil {
		t.Fatal(err)
	}
	runtime.Subscribe(func(event AgentEvent) {
		if event.Type == EventAgentEnd {
			runtime.SetModel(ModelRef{Model: "changed"})
			runtime.SetThinkingLevel(ThinkingHigh)
		}
	})
	if err := runtime.PromptText(context.Background(), "run"); err != nil {
		t.Fatal(err)
	}
	if state := runtime.State(); state.Model.Model != "changed" || state.RequestedThinkingLevel != ThinkingHigh || state.ThinkingLevel != ThinkingHigh || state.Error == "" {
		t.Fatalf("error settlement replaced current choices: %+v", state)
	}
}

func TestEngineDynamicDefaultModelDoesNotBecomeExplicit(t *testing.T) {
	for _, entry := range []string{"run", "continue", "resume"} {
		t.Run(entry, func(t *testing.T) {
			var requests []ModelRequest
			model := StreamFunc(func(_ context.Context, request ModelRequest) (AssistantStream, error) {
				requests = append(requests, request)
				message := Message{Role: RoleAssistant, StopReason: StopReasonStop}
				if len(requests) == 1 {
					message.ToolCalls = []ToolCall{{ID: "call", Name: "echo"}}
					message.StopReason = StopReasonToolUse
				}
				return newStaticAssistantStream(message, nil), nil
			})
			definition := AgentDefinition{Model: model, DefaultModel: ModelRef{Model: "fallback"}, ThinkingLevel: ThinkingMax, Tools: []ToolDefinition{{Name: "echo", Execute: terminatingTool(false)}}, ThinkingLevelResolver: func(ref ModelRef, requested ThinkingLevel) ThinkingLevel {
				if ref.Model == "first" {
					return ThinkingLow
				}
				if ref.Model == "second" {
					return ThinkingHigh
				}
				return ThinkingOff
			}}
			hooks := LoopHooks{ResolveDefinition: func(_ context.Context, current AgentDefinition, _ AgentSnapshot) (AgentDefinition, error) {
				if len(requests) == 0 {
					current.DefaultModel = ModelRef{Model: "first"}
				} else {
					current.DefaultModel = ModelRef{Model: "second"}
				}
				return current, nil
			}}
			snapshot := AgentSnapshot{RequestedThinkingLevel: ThinkingMax, Messages: []Message{NewUserTextMessage("root")}}
			var next *AgentSnapshot
			var err error
			switch entry {
			case "run":
				snapshot.Messages = nil
				next, err = NewEngine().RunWithHooks(context.Background(), definition, &snapshot, []Message{NewUserTextMessage("root")}, nil, hooks)
			case "continue":
				next, err = NewEngine().ContinueWithHooks(context.Background(), definition, &snapshot, nil, hooks)
			case "resume":
				assistant := Message{Role: RoleAssistant, ToolCalls: []ToolCall{{ID: "pending", Name: "echo"}}, StopReason: StopReasonToolUse}
				snapshot.Messages = append(snapshot.Messages, assistant)
				snapshot.PendingToolCalls = []PendingToolCall{{ToolCallID: "pending", ToolName: "echo"}}
				setPendingToolControlState(&snapshot, 1, assistant)
				hooks.ToolGate = func(context.Context, BeforeToolCallContext) (ToolGateResult, error) {
					return ToolGateResult{Action: ToolGateActionAllow}, nil
				}
				next, err = NewEngine().ResumePendingToolCallsWithHooks(context.Background(), definition, &snapshot, nil, hooks)
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(requests) != 2 || requests[0].Model.Model != "first" || requests[1].Model.Model != "second" || requests[0].ThinkingLevel != ThinkingLow || requests[1].ThinkingLevel != ThinkingHigh {
				t.Fatalf("dynamic defaults or effective thinking ignored: %+v", requests)
			}
			if next.Model.Model != "" || next.RequestedThinkingLevel != ThinkingMax || next.ThinkingLevel != ThinkingOff {
				t.Fatalf("resolved default became durable override: %+v", next)
			}
		})
	}
}

func TestAgentDefaultModelRemainsFallbackAcrossRestoreAndSetters(t *testing.T) {
	definition := AgentDefinition{DefaultModel: ModelRef{Provider: "google", Model: "gemini-2.0-flash"}, ThinkingLevel: ThinkingMax}
	runtime, err := NewAgent(definition)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := runtime.Snapshot()
	if snapshot.Model.Model != "" || runtime.State().Model.Model != "gemini-2.0-flash" || snapshot.ThinkingLevel != ThinkingOff {
		t.Fatalf("default model became explicit: %+v state=%+v", snapshot, runtime.State())
	}
	definition.DefaultModel = ModelRef{Provider: "google", Model: "gemini-2.5-flash"}
	restored, err := NewAgent(definition, WithSnapshot(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	if state := restored.State(); state.Model.Model != "gemini-2.5-flash" || state.RequestedThinkingLevel != ThinkingMax || state.ThinkingLevel != ThinkingHigh {
		t.Fatalf("restore froze the previous default: %+v", state)
	}
	restored.SetModel(ModelRef{Provider: "google", Model: "gemini-2.0-flash"})
	if got := restored.Snapshot(); got.Model.Model != "gemini-2.0-flash" || got.ThinkingLevel != ThinkingOff || got.RequestedThinkingLevel != ThinkingMax {
		t.Fatalf("explicit model was not saved: %+v", got)
	}
	restored.SetModel(ModelRef{})
	restored.SetThinkingLevel(ThinkingLow)
	if state := restored.State(); state.Model.Model != "gemini-2.5-flash" || state.ThinkingLevel != ThinkingLow || state.RequestedThinkingLevel != ThinkingLow || restored.Snapshot().Model.Model != "" {
		t.Fatalf("clearing explicit model did not restore default: %+v", state)
	}
}

func TestAgentDynamicDefaultModelHonorsExplicitSelection(t *testing.T) {
	var requests []ModelRequest
	model := StreamFunc(func(_ context.Context, request ModelRequest) (AssistantStream, error) {
		requests = append(requests, request)
		message := Message{Role: RoleAssistant, StopReason: StopReasonStop}
		if len(requests) == 1 {
			message.ToolCalls = []ToolCall{{ID: "call", Name: "echo"}}
			message.StopReason = StopReasonToolUse
		}
		return newStaticAssistantStream(message, nil), nil
	})
	definition := AgentDefinition{Model: model, DefaultModel: ModelRef{Model: "fallback"}, ThinkingLevel: ThinkingMax, Tools: []ToolDefinition{{Name: "echo", Execute: terminatingTool(false)}}, ThinkingLevelResolver: func(ref ModelRef, requested ThinkingLevel) ThinkingLevel {
		switch ref.Model {
		case "first":
			return ThinkingLow
		case "second":
			return ThinkingHigh
		case "explicit":
			return ThinkingMedium
		default:
			return ThinkingOff
		}
	}}
	runtime, err := NewAgent(definition, WithDefinitionResolver(func(context.Context, AgentSnapshot) (AgentDefinition, error) {
		resolved := definition
		if len(requests) == 0 {
			resolved.DefaultModel = ModelRef{Model: "first"}
		} else {
			resolved.DefaultModel = ModelRef{Model: "second"}
		}
		return resolved, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.PromptText(context.Background(), "root"); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[0].Model.Model != "first" || requests[1].Model.Model != "second" || requests[0].ThinkingLevel != ThinkingLow || requests[1].ThinkingLevel != ThinkingHigh {
		t.Fatalf("agent froze dynamic defaults: %+v", requests)
	}
	if got := runtime.Snapshot(); got.Model.Model != "" || got.RequestedThinkingLevel != ThinkingMax || got.ThinkingLevel != ThinkingOff {
		t.Fatalf("dynamic request became saved selection: %+v", got)
	}
	runtime.SetModel(ModelRef{Model: "explicit"})
	if err := runtime.PromptText(context.Background(), "again"); err != nil {
		t.Fatal(err)
	}
	if request := requests[len(requests)-1]; request.Model.Model != "explicit" || request.ThinkingLevel != ThinkingMedium {
		t.Fatalf("dynamic default replaced explicit selection: %+v", request)
	}
	if state := runtime.State(); state.Model.Model != "explicit" || state.RequestedThinkingLevel != ThinkingMax || state.ThinkingLevel != ThinkingMedium {
		t.Fatalf("effective state lost explicit selection: %+v", state)
	}
}

func TestEngineTemporaryModelOverridePreservesDefaultSelection(t *testing.T) {
	var requests []ModelRequest
	model := StreamFunc(func(_ context.Context, request ModelRequest) (AssistantStream, error) {
		requests = append(requests, request)
		message := Message{Role: RoleAssistant, StopReason: StopReasonStop}
		if len(requests) == 1 {
			message.ToolCalls = []ToolCall{{ID: "call", Name: "echo"}}
			message.StopReason = StopReasonToolUse
		}
		return newStaticAssistantStream(message, nil), nil
	})
	definition := AgentDefinition{Model: model, DefaultModel: ModelRef{Model: "first-default"}, ThinkingLevel: ThinkingLow, Tools: []ToolDefinition{{Name: "echo", Execute: terminatingTool(false)}}, PrepareNextTurn: func(context.Context, PrepareNextTurnContext) (*AgentLoopTurnUpdate, error) {
		ref, level := ModelRef{Model: "temporary"}, ThinkingMax
		return &AgentLoopTurnUpdate{ModelRef: &ref, ThinkingLevel: &level}, nil
	}}
	next, err := NewEngine().Run(context.Background(), definition, &AgentSnapshot{}, []Message{NewUserTextMessage("root")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 || requests[0].Model.Model != "first-default" || requests[1].Model.Model != "temporary" || requests[1].ThinkingLevel != ThinkingMax {
		t.Fatalf("temporary model/thinking override was not applied: %+v", requests)
	}
	if next.Model.Model != "" || next.RequestedThinkingLevel != ThinkingLow || next.ThinkingLevel != ThinkingLow {
		t.Fatalf("temporary override was saved: %+v", next)
	}
	definition.DefaultModel = ModelRef{Model: "next-default"}
	definition.PrepareNextTurn = nil
	next, err = NewEngine().Run(context.Background(), definition, next, []Message{NewUserTextMessage("again")}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := requests[len(requests)-1]; got.Model.Model != "next-default" || got.ThinkingLevel != ThinkingLow {
		t.Fatalf("next run froze old default or temporary override: %+v", got)
	}
	if next.Model.Model != "" {
		t.Fatalf("next default became explicit: %+v", next.Model)
	}
}
