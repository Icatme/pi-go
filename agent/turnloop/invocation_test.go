package turnloop

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/session"
)

type invocationFunc func(context.Context, *agent.Runner, []agent.Message, agent.LoopHooks) (*agent.RunStream, error)

func (f invocationFunc) RunWithHooks(ctx context.Context, r *agent.Runner, p []agent.Message, h agent.LoopHooks) (*agent.RunStream, error) {
	return f(ctx, r, p, h)
}

func TestInvocationRejectsInitialSnapshot(t *testing.T) {
	_, err := New(t.Context(), Config{Definition: agent.AgentDefinition{Model: &recordingModel{}}, QueueCapacity: 1, Initial: agent.AgentSnapshot{SessionID: "ignored"}, PrepareInvocation: func(context.Context) (Invocation, error) { return nil, nil }})
	if err == nil {
		t.Fatal("silently ignored initial snapshot")
	}
}
func TestInvocationPrepareFailureLeavesAllInputs(t *testing.T) {
	failure := errors.New("prepare failed")
	loop := mustNewLoop(t, t.Context(), Config{Definition: agent.AgentDefinition{Model: &recordingModel{}}, QueueCapacity: 2, PrepareInvocation: func(context.Context) (Invocation, error) { return nil, failure }})
	admitBeforeRun(t, loop, textInput(DeliveryNextRun, "a"), textInput(DeliverySteering, "b"))
	result, err := waitLoop(t, loop)
	if !errors.Is(err, failure) || !reflect.DeepEqual(inputTexts(result.Unhandled), []string{"a", "b"}) {
		t.Fatalf("err=%v unhandled=%v", err, result.Unhandled)
	}
}
func TestInvocationStartFailureRestoresOriginalQueueOrder(t *testing.T) {
	for _, delivery := range []Delivery{DeliverySteering, DeliveryFollowUp} {
		t.Run(string(delivery), func(t *testing.T) {
			failure := errors.New("not started")
			var loop *TurnLoop
			loop = mustNewLoop(t, t.Context(), Config{Definition: agent.AgentDefinition{Model: &recordingModel{}, SteeringMode: agent.QueueModeAll, FollowUpMode: agent.QueueModeAll}, QueueCapacity: 5, PrepareInvocation: func(context.Context) (Invocation, error) {
				return invocationFunc(func(_ context.Context, _ *agent.Runner, prompts []agent.Message, _ agent.LoopHooks) (*agent.RunStream, error) {
					if got := userTexts(prompts); !reflect.DeepEqual(got, []string{"a", "c"}) {
						return nil, errors.New("incorrect prompt selection")
					}
					if err := loop.Push(textInput(DeliveryNextRun, "new")); err != nil {
						return nil, err
					}
					return nil, failure
				}), nil
			}})
			admitBeforeRun(t, loop, textInput(delivery, "a"), textInput(DeliveryNextRun, "b"), textInput(delivery, "c"), textInput(DeliveryNextRun, "d"))
			result, err := waitLoop(t, loop)
			if !errors.Is(err, failure) || !reflect.DeepEqual(inputTexts(result.Unhandled), []string{"a", "b", "c", "d", "new"}) {
				t.Fatalf("err=%v unhandled=%v", err, inputTexts(result.Unhandled))
			}
		})
	}
}
func TestInvocationStopDuringPrepareLeavesInputs(t *testing.T) {
	entered := make(chan struct{})
	loop := mustNewLoop(t, t.Context(), Config{Definition: agent.AgentDefinition{Model: &recordingModel{}}, QueueCapacity: 2, PrepareInvocation: func(ctx context.Context) (Invocation, error) { close(entered); <-ctx.Done(); return nil, ctx.Err() }})
	admitBeforeRun(t, loop, textInput(DeliveryNextRun, "a"), textInput(DeliverySteering, "b"))
	<-entered
	if err := loop.Stop(StopImmediate); err != nil {
		t.Fatal(err)
	}
	result, err := waitLoop(t, loop)
	if err != nil || result.StopMode != StopImmediate || !reflect.DeepEqual(inputTexts(result.Unhandled), []string{"a", "b"}) {
		t.Fatalf("err=%v result=%+v", err, result)
	}
}
func TestInvocationSessionRestoresHistoryAndKeepsTurnBudget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conversation.jsonl")
	storage, err := session.CreateJSONLStorage(path, session.Header{ID: "integration"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := session.New(storage, session.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()
	scope := session.RuntimeScope{Identity: "host", AuthEpoch: 1}
	prepared, finished := 0, 0
	model := &recordingModel{}
	loop := mustNewLoop(t, t.Context(), Config{Definition: agent.AgentDefinition{Model: model, MaxTurns: 1, FinishTurn: func(context.Context, agent.AgentTurnContext) (agent.AgentTurnDecision, error) {
		finished++
		return agent.AgentTurnDecision{}, nil
	}}, QueueCapacity: 2, PrepareInvocation: func(context.Context) (Invocation, error) {
		prepared++
		if prepared == 2 {
			if err := s.Close(); err != nil {
				return nil, err
			}
			reopened, err := session.OpenJSONLStorage(path)
			if err != nil {
				return nil, err
			}
			s, err = session.New(reopened, session.Options{})
			if err != nil {
				return nil, err
			}
		}
		return s.PrepareRun(session.MainLane, session.RuntimeOptions{Scope: func() session.RuntimeScope { return scope }})
	}})
	admitBeforeRun(t, loop, textInput(DeliverySteering, "first"), textInput(DeliverySteering, "second"))
	_ = loop.Stop(StopGraceful)
	result, err := waitLoop(t, loop)
	if err != nil || prepared != 2 || finished != 2 || len(result.Unhandled) != 0 {
		t.Fatalf("err=%v prepare=%d finish=%d", err, prepared, finished)
	}
	views := model.views()
	if len(views) != 2 || !reflect.DeepEqual(views[1].userTexts, []string{"first", "second"}) {
		t.Fatalf("requests=%+v", views)
	}
	if _, err := s.PrepareRun(session.MainLane, session.RuntimeOptions{Scope: func() session.RuntimeScope { return scope }}); err != nil {
		t.Fatal(err)
	}
}
func TestInvocationSessionCommitFailureDoesNotReplayDeliveredInput(t *testing.T) {
	storage, _ := session.NewMemoryStorage(session.Header{ID: "conflict"})
	s, err := session.New(storage, session.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	model := &recordingModel{}
	loop := mustNewLoop(t, t.Context(), Config{Definition: agent.AgentDefinition{Model: model}, QueueCapacity: 2, PrepareInvocation: func(context.Context) (Invocation, error) {
		binding, err := s.PrepareRun(session.MainLane, session.RuntimeOptions{Scope: func() session.RuntimeScope { return session.RuntimeScope{Identity: "host"} }})
		if err != nil {
			return nil, err
		}
		if err := s.MoveLane(session.MainLane, ""); err != nil {
			return nil, err
		}
		return binding, nil
	}})
	admitBeforeRun(t, loop, textInput(DeliveryNextRun, "delivered"), textInput(DeliveryNextRun, "waiting"))
	result, err := waitLoop(t, loop)
	if err == nil || len(model.views()) != 0 || !reflect.DeepEqual(inputTexts(result.Unhandled), []string{"waiting"}) || !reflect.DeepEqual(userTexts(result.Snapshot.Messages), []string{"delivered"}) {
		t.Fatalf("err=%v result=%+v", err, result)
	}
}

func TestInvocationScopeChangeBeforeStartPreservesInput(t *testing.T) {
	storage, _ := session.NewMemoryStorage(session.Header{ID: "scope"})
	s, err := session.New(storage, session.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	scope := session.RuntimeScope{Identity: "host", AuthEpoch: 1}
	loop := mustNewLoop(t, t.Context(), Config{Definition: agent.AgentDefinition{Model: &recordingModel{}}, QueueCapacity: 2, PrepareInvocation: func(context.Context) (Invocation, error) {
		b, err := s.PrepareRun(session.MainLane, session.RuntimeOptions{Scope: func() session.RuntimeScope { return scope }})
		scope.AuthEpoch++
		return b, err
	}})
	admitBeforeRun(t, loop, textInput(DeliveryNextRun, "first"), textInput(DeliveryNextRun, "waiting"))
	result, err := waitLoop(t, loop)
	if !errors.Is(err, session.ErrScopeMismatch) || !reflect.DeepEqual(inputTexts(result.Unhandled), []string{"first", "waiting"}) {
		t.Fatalf("err=%v unhandled=%v", err, result.Unhandled)
	}
}
func TestInvocationRejectsInvalidFactoryResults(t *testing.T) {
	for _, kind := range []string{"nil", "typed-nil", "nil-start"} {
		t.Run(kind, func(t *testing.T) {
			loop := mustNewLoop(t, t.Context(), Config{Definition: agent.AgentDefinition{Model: &recordingModel{}}, QueueCapacity: 1, PrepareInvocation: func(context.Context) (Invocation, error) {
				if kind == "nil" {
					return nil, nil
				}
				if kind == "typed-nil" {
					var f invocationFunc
					return f, nil
				}
				return invocationFunc(func(context.Context, *agent.Runner, []agent.Message, agent.LoopHooks) (*agent.RunStream, error) {
					return nil, nil
				}), nil
			}})
			admitBeforeRun(t, loop, textInput(DeliveryNextRun, "first"))
			result, err := waitLoop(t, loop)
			if err == nil || len(result.Unhandled) != 1 {
				t.Fatalf("err=%v unhandled=%v", err, result.Unhandled)
			}
		})
	}
}
func TestInvocationStartContractViolationDoesNotReplay(t *testing.T) {
	failure := errors.New("invalid start")
	loop := mustNewLoop(t, t.Context(), Config{Definition: agent.AgentDefinition{Model: &recordingModel{}}, QueueCapacity: 3, PrepareInvocation: func(context.Context) (Invocation, error) {
		return invocationFunc(func(ctx context.Context, _ *agent.Runner, _ []agent.Message, hooks agent.LoopHooks) (*agent.RunStream, error) {
			_, _ = hooks.GetSteeringMessages(ctx)
			return nil, failure
		}), nil
	}})
	admitBeforeRun(t, loop, textInput(DeliveryNextRun, "first"), textInput(DeliverySteering, "taken"), textInput(DeliveryNextRun, "waiting"))
	result, err := waitLoop(t, loop)
	if !errors.Is(err, failure) || !reflect.DeepEqual(inputTexts(result.Unhandled), []string{"waiting"}) {
		t.Fatalf("err=%v unhandled=%v", err, result.Unhandled)
	}
}

func TestInvocationRejectedPromptMutationDoesNotAlterUnhandled(t *testing.T) {
	failure := errors.New("rejected")
	loop := mustNewLoop(t, t.Context(), Config{Definition: agent.AgentDefinition{Model: &recordingModel{}}, QueueCapacity: 1, PrepareInvocation: func(context.Context) (Invocation, error) {
		return invocationFunc(func(_ context.Context, _ *agent.Runner, prompts []agent.Message, _ agent.LoopHooks) (*agent.RunStream, error) {
			prompts[0].Parts[0].Text = "mutated"
			prompts[0].Metadata["nested"].(map[string]any)["value"] = "mutated"
			return nil, failure
		}), nil
	}})
	input := textInput(DeliveryNextRun, "original")
	input.Message.Metadata = map[string]any{"nested": map[string]any{"value": "original"}}
	admitBeforeRun(t, loop, input)
	result, err := waitLoop(t, loop)
	if !errors.Is(err, failure) || len(result.Unhandled) != 1 || textOf(result.Unhandled[0].Message) != "original" || result.Unhandled[0].Message.Metadata["nested"].(map[string]any)["value"] != "original" {
		t.Fatalf("err=%v result=%+v", err, result)
	}
}

func TestInvocationStopDuringStartRestoresOriginalOrder(t *testing.T) {
	entered := make(chan struct{})
	loop := mustNewLoop(t, t.Context(), Config{Definition: agent.AgentDefinition{Model: &recordingModel{}, SteeringMode: agent.QueueModeAll}, QueueCapacity: 3, PrepareInvocation: func(context.Context) (Invocation, error) {
		return invocationFunc(func(ctx context.Context, _ *agent.Runner, _ []agent.Message, _ agent.LoopHooks) (*agent.RunStream, error) {
			close(entered)
			<-ctx.Done()
			return nil, ctx.Err()
		}), nil
	}})
	admitBeforeRun(t, loop, textInput(DeliverySteering, "a"), textInput(DeliveryNextRun, "b"), textInput(DeliverySteering, "c"))
	<-entered
	_ = loop.Stop(StopImmediate)
	result, err := waitLoop(t, loop)
	if err != nil || result.StopMode != StopImmediate || !reflect.DeepEqual(inputTexts(result.Unhandled), []string{"a", "b", "c"}) {
		t.Fatalf("err=%v result=%+v", err, result)
	}
}
func TestInvocationStopDuringSuccessfulPrepareDoesNotStart(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	started := false
	loop := mustNewLoop(t, t.Context(), Config{Definition: agent.AgentDefinition{Model: &recordingModel{}}, QueueCapacity: 1, PrepareInvocation: func(context.Context) (Invocation, error) {
		close(entered)
		<-release
		return invocationFunc(func(context.Context, *agent.Runner, []agent.Message, agent.LoopHooks) (*agent.RunStream, error) {
			started = true
			return nil, errors.New("must not start")
		}), nil
	}})
	admitBeforeRun(t, loop, textInput(DeliveryNextRun, "a"))
	<-entered
	_ = loop.Stop(StopImmediate)
	close(release)
	result, err := waitLoop(t, loop)
	if err != nil || started || len(result.Unhandled) != 1 {
		t.Fatalf("err=%v started=%v result=%+v", err, started, result)
	}
}
func TestInvocationStreamAndErrorSettlesWithoutReplay(t *testing.T) {
	failure := errors.New("bad starter")
	var supplied *agent.RunStream
	model := &recordingModel{respond: func(ctx context.Context, _ agent.ModelRequest, _ int) (agent.AssistantStream, error) {
		return newCancelStream(ctx), nil
	}}
	loop := mustNewLoop(t, t.Context(), Config{Definition: agent.AgentDefinition{Model: model}, QueueCapacity: 2, PrepareInvocation: func(context.Context) (Invocation, error) {
		return invocationFunc(func(ctx context.Context, r *agent.Runner, p []agent.Message, h agent.LoopHooks) (*agent.RunStream, error) {
			supplied = r.RunWithHooks(ctx, agent.AgentSnapshot{}, p, h)
			return supplied, failure
		}), nil
	}})
	admitBeforeRun(t, loop, textInput(DeliveryNextRun, "first"), textInput(DeliveryNextRun, "waiting"))
	result, err := waitLoop(t, loop)
	if !errors.Is(err, failure) || !reflect.DeepEqual(inputTexts(result.Unhandled), []string{"waiting"}) {
		t.Fatalf("err=%v result=%+v", err, result)
	}
	// Wait completed only after Close canceled/drained the invalid returned stream.
	if supplied == nil {
		t.Fatal("missing stream")
	}
	_, _ = supplied.Wait()
}
func TestInvocationRefreshesTrustedBindingForTools(t *testing.T) {
	storage, _ := session.NewMemoryStorage(session.Header{ID: "trusted"})
	s, err := session.New(storage, session.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var current, previous atomic.Pointer[session.RunBinding]
	var executed, rejectedOld atomic.Int64
	scope := session.RuntimeScope{Identity: "host", AuthEpoch: 1}
	model := &recordingModel{respond: func(_ context.Context, _ agent.ModelRequest, n int) (agent.AssistantStream, error) {
		if n%2 == 1 {
			return newImmediateStream(agent.Message{Role: agent.RoleAssistant, StopReason: agent.StopReasonToolUse, ToolCalls: []agent.ToolCall{{ID: "read", Name: "read", Arguments: json.RawMessage(`{}`)}}}, nil), nil
		}
		return newImmediateStream(assistantText("done"), nil), nil
	}}
	definition := agent.AgentDefinition{Model: model, MaxTurns: 3, ToolResolver: func(_ context.Context, snapshot agent.AgentSnapshot) ([]agent.ToolDefinition, error) {
		binding := current.Load()
		if binding == nil {
			return nil, errors.New("missing trusted binding")
		}
		if got, ok := binding.TrustedScope(snapshot); !ok || got != scope {
			return nil, errors.New("current binding failed authorization")
		}
		if old := previous.Load(); old != nil {
			if _, ok := old.TrustedScope(snapshot); ok {
				return nil, errors.New("old binding trusted new run")
			}
			rejectedOld.Add(1)
		}
		return []agent.ToolDefinition{{Name: "read", Execute: func(context.Context, agent.ToolExecutionContext) (agent.ToolResult, error) {
			executed.Add(1)
			return agent.ToolResult{}, nil
		}}}, nil
	}}
	loop := mustNewLoop(t, t.Context(), Config{Definition: definition, QueueCapacity: 2, PrepareInvocation: func(context.Context) (Invocation, error) {
		binding, err := s.PrepareRun(session.MainLane, session.RuntimeOptions{Scope: func() session.RuntimeScope { return scope }})
		if err != nil {
			return nil, err
		}
		previous.Store(current.Swap(binding))
		return binding, nil
	}})
	admitBeforeRun(t, loop, textInput(DeliveryNextRun, "first"), textInput(DeliveryNextRun, "second"))
	_ = loop.Stop(StopGraceful)
	result, err := waitLoop(t, loop)
	if err != nil || executed.Load() != 2 || rejectedOld.Load() < 2 || len(result.Unhandled) != 0 {
		t.Fatalf("err=%v tools=%d oldRejections=%d", err, executed.Load(), rejectedOld.Load())
	}
}

func TestInvocationSessionMixedDeliveryPersistsWithoutInjectingNextRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mixed.jsonl")
	storage, err := session.CreateJSONLStorage(path, session.Header{ID: "mixed"})
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := session.New(storage, session.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conversation.Close() }()
	scope := session.RuntimeOptions{Scope: func() session.RuntimeScope { return session.RuntimeScope{Identity: "host", AuthEpoch: 1} }}
	prepared := 0
	model := &recordingModel{}
	loop := mustNewLoop(t, t.Context(), Config{Definition: agent.AgentDefinition{Model: model}, QueueCapacity: 4, PrepareInvocation: func(context.Context) (Invocation, error) {
		prepared++
		return conversation.PrepareRun(session.MainLane, scope)
	}})
	admitBeforeRun(t, loop, textInput(DeliveryNextRun, "first"), textInput(DeliverySteering, "steer"), textInput(DeliveryFollowUp, "follow"), textInput(DeliveryNextRun, "next"))
	_ = loop.Stop(StopGraceful)
	result, err := waitLoop(t, loop)
	if err != nil || prepared != 2 || len(result.Unhandled) != 0 {
		t.Fatalf("err=%v prepared=%d unhandled=%v", err, prepared, result.Unhandled)
	}
	views := model.views()
	want := [][]string{{"first", "steer"}, {"first", "steer", "follow"}, {"first", "steer", "follow", "next"}}
	if len(views) != len(want) {
		t.Fatalf("model rounds=%d", len(views))
	}
	for index, expected := range want {
		if !reflect.DeepEqual(views[index].userTexts, expected) {
			t.Fatalf("round %d users=%v want=%v", index, views[index].userTexts, expected)
		}
	}
	if err := conversation.Close(); err != nil {
		t.Fatal(err)
	}
	storage, err = session.OpenJSONLStorage(path)
	if err != nil {
		t.Fatal(err)
	}
	conversation, err = session.New(storage, session.Options{})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := conversation.PrepareRun(session.MainLane, scope)
	if err != nil {
		t.Fatal(err)
	}
	// Inspect restored history through the public binding/Runner surface, not
	// session-private state. A separate explicit run also proves resumability.
	checkRunner, err := agent.NewRunner(agent.AgentDefinition{Model: model})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := binding.Run(t.Context(), checkRunner, []agent.Message{agent.NewUserTextMessage("after-reopen")})
	if err != nil {
		t.Fatal(err)
	}
	for range stream.Events() {
	}
	restored, err := stream.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(userTexts(restored.Messages), []string{"first", "steer", "follow", "next", "after-reopen"}) {
		t.Fatalf("restored users=%v", userTexts(restored.Messages))
	}
}
