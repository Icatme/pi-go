package turnloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/Icatme/pi-go/agent"
)

const testWait = 5 * time.Second

type requestView struct {
	userTexts   []string
	deadline    time.Time
	hasDeadline bool
	contextVal  any
}

type recordingModel struct {
	mu         sync.Mutex
	requests   []requestView
	contextKey any
	respond    func(context.Context, agent.ModelRequest, int) (agent.AssistantStream, error)
}

func (m *recordingModel) Stream(ctx context.Context, request agent.ModelRequest) (agent.AssistantStream, error) {
	deadline, hasDeadline := ctx.Deadline()
	view := requestView{
		userTexts:   userTexts(request.Messages),
		deadline:    deadline,
		hasDeadline: hasDeadline,
	}
	if m.contextKey != nil {
		view.contextVal = ctx.Value(m.contextKey)
	}

	m.mu.Lock()
	m.requests = append(m.requests, view)
	call := len(m.requests)
	m.mu.Unlock()

	if m.respond != nil {
		return m.respond(ctx, request, call)
	}
	return newImmediateStream(assistantText(fmt.Sprintf("reply-%d", call)), nil), nil
}

func (m *recordingModel) views() []requestView {
	m.mu.Lock()
	defer m.mu.Unlock()
	views := make([]requestView, len(m.requests))
	for i, view := range m.requests {
		views[i] = view
		views[i].userTexts = append([]string(nil), view.userTexts...)
	}
	return views
}

type immediateStream struct {
	events <-chan agent.AssistantEvent
	final  agent.Message
	err    error
}

func newImmediateStream(final agent.Message, err error) *immediateStream {
	events := make(chan agent.AssistantEvent)
	close(events)
	return &immediateStream{events: events, final: final, err: err}
}

func (s *immediateStream) Events() <-chan agent.AssistantEvent { return s.events }
func (s *immediateStream) Wait() (agent.Message, error)        { return s.final, s.err }
func (s *immediateStream) Close() error                        { return nil }

type cancelStream struct {
	ctx    context.Context
	events chan agent.AssistantEvent
	done   chan struct{}
}

func newCancelStream(ctx context.Context) *cancelStream {
	stream := &cancelStream{
		ctx:    ctx,
		events: make(chan agent.AssistantEvent),
		done:   make(chan struct{}),
	}
	go func() {
		<-ctx.Done()
		close(stream.events)
		close(stream.done)
	}()
	return stream
}

func (s *cancelStream) Events() <-chan agent.AssistantEvent { return s.events }
func (s *cancelStream) Wait() (agent.Message, error) {
	<-s.done
	return agent.Message{}, s.ctx.Err()
}
func (s *cancelStream) Close() error { return nil }

func TestNewAndPublicValidation(t *testing.T) {
	model := &recordingModel{}
	definition := agent.AgentDefinition{Name: "test", Model: model}

	tests := []struct {
		name   string
		ctx    context.Context
		config Config
		want   error
	}{
		{name: "nil context", config: Config{Definition: definition, QueueCapacity: 1}},
		{name: "zero capacity", ctx: context.Background(), config: Config{Definition: definition}, want: errors.New("capacity")},
		{name: "negative capacity", ctx: context.Background(), config: Config{Definition: definition, QueueCapacity: -1}, want: errors.New("capacity")},
		{name: "invalid steering mode", ctx: context.Background(), config: Config{Definition: agent.AgentDefinition{Model: model, SteeringMode: agent.QueueMode("newest")}, QueueCapacity: 1}, want: errors.New("steering")},
		{name: "invalid follow-up mode", ctx: context.Background(), config: Config{Definition: agent.AgentDefinition{Model: model, FollowUpMode: agent.QueueMode("newest")}, QueueCapacity: 1}, want: errors.New("follow-up")},
		{name: "pending calls", ctx: context.Background(), config: Config{Definition: definition, Initial: agent.AgentSnapshot{PendingToolCalls: []agent.PendingToolCall{{ToolCallID: "call-1"}}}, QueueCapacity: 1}, want: ErrPendingSnapshot},
		{name: "pending control", ctx: context.Background(), config: Config{Definition: definition, Initial: agent.AgentSnapshot{PendingToolControl: &agent.PendingToolControl{Turn: 1, Binding: "bound"}}, QueueCapacity: 1}, want: ErrPendingSnapshot},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loop, err := New(tt.ctx, tt.config)
			if loop != nil {
				t.Fatal("New returned a loop for invalid configuration")
			}
			if err == nil {
				t.Fatal("New succeeded for invalid configuration")
			}
			if tt.want == ErrPendingSnapshot && !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}

	loop, err := New(context.Background(), Config{Definition: definition, QueueCapacity: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := loop.Push(Input{Delivery: Delivery("later")}); !errors.Is(err, ErrInvalidDelivery) {
		t.Fatalf("invalid delivery error = %v", err)
	}
	if err := loop.Stop(StopMode("later")); !errors.Is(err, ErrInvalidStopMode) {
		t.Fatalf("invalid stop error = %v", err)
	}
	if _, err := loop.Wait(nil); err == nil {
		t.Fatal("Wait(nil) succeeded")
	}
	if err := loop.Stop(StopImmediate); err != nil {
		t.Fatal(err)
	}
	result, err := waitLoop(t, loop)
	if err != nil || result.StopMode != StopImmediate {
		t.Fatalf("immediate result = %+v, err = %v", result, err)
	}
}

func TestQueueRejectNewestAndGracefulUpgrade(t *testing.T) {
	started := make(chan struct{}, 1)
	model := &recordingModel{respond: func(ctx context.Context, _ agent.ModelRequest, _ int) (agent.AssistantStream, error) {
		select {
		case started <- struct{}{}:
		default:
		}
		return newCancelStream(ctx), nil
	}}
	loop := mustNewLoop(t, context.Background(), Config{
		Definition:    agent.AgentDefinition{Name: "blocking", Model: model},
		QueueCapacity: 1,
	})

	if err := loop.Push(textInput(DeliveryNextRun, "first")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(testWait):
		t.Fatal("model did not start")
	}
	if err := loop.Push(textInput(DeliveryNextRun, "second")); err != nil {
		t.Fatal(err)
	}
	if err := loop.Push(textInput(DeliveryNextRun, "rejected")); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("third push error = %v, want ErrQueueFull", err)
	}
	if err := loop.Stop(StopGraceful); err != nil {
		t.Fatal(err)
	}
	if err := loop.Stop(StopImmediate); err != nil {
		t.Fatal(err)
	}
	result, err := waitLoop(t, loop)
	if err != nil {
		t.Fatalf("explicit immediate stop returned %v", err)
	}
	if result.StopMode != StopImmediate {
		t.Fatalf("stop mode = %q", result.StopMode)
	}
	if got := inputTexts(result.Unhandled); !reflect.DeepEqual(got, []string{"second"}) {
		t.Fatalf("unhandled = %v", got)
	}
	if err := loop.Push(textInput(DeliveryNextRun, "late")); !errors.Is(err, ErrNotAccepting) {
		t.Fatalf("late push error = %v", err)
	}
}

type typedPayload struct {
	Values []string
	Labels map[string]string
}

func TestPushInitialAndWaitOwnership(t *testing.T) {
	initialValues := []string{"initial"}
	initial := agent.AgentSnapshot{Metadata: map[string]any{"values": initialValues}}
	model := &recordingModel{}
	loop := mustNewLoop(t, context.Background(), Config{
		Definition:    agent.AgentDefinition{Name: "clone", Model: model},
		Initial:       initial,
		QueueCapacity: 2,
	})
	initialValues[0] = "mutated"

	typed := &typedPayload{Values: []string{"owned"}, Labels: map[string]string{"k": "v"}}
	cycle := map[string]any{"value": "before"}
	cycle["self"] = cycle
	message := agent.NewUserTextMessage("clone-me")
	message.Metadata = map[string]any{"typed": typed, "cycle": cycle}
	if err := loop.Push(Input{Delivery: DeliveryNextRun, Message: message}); err != nil {
		t.Fatal(err)
	}
	typed.Values[0] = "mutated"
	typed.Labels["k"] = "mutated"
	cycle["value"] = "mutated"
	if err := loop.Stop(StopGraceful); err != nil {
		t.Fatal(err)
	}
	first, err := waitLoop(t, loop)
	if err != nil {
		t.Fatal(err)
	}
	if got := first.Snapshot.Metadata["values"].([]string)[0]; got != "initial" {
		t.Fatalf("initial snapshot alias leaked: %q", got)
	}
	user := findUserMessage(t, first.Snapshot.Messages, "clone-me")
	gotTyped, ok := user.Metadata["typed"].(*typedPayload)
	if !ok {
		t.Fatalf("typed metadata became %T", user.Metadata["typed"])
	}
	if gotTyped.Values[0] != "owned" || gotTyped.Labels["k"] != "v" {
		t.Fatalf("typed metadata mutated: %+v", gotTyped)
	}
	gotCycle := user.Metadata["cycle"].(map[string]any)
	if gotCycle["value"] != "before" {
		t.Fatalf("cycle metadata mutated: %+v", gotCycle)
	}
	self := gotCycle["self"].(map[string]any)
	self["linked"] = true
	if gotCycle["linked"] != true {
		t.Fatal("cycle identity was not preserved")
	}

	gotTyped.Values[0] = "changed-result"
	gotCycle["value"] = "changed-result"
	first.Snapshot.Metadata["values"].([]string)[0] = "changed-result"
	second, err := waitLoop(t, loop)
	if err != nil {
		t.Fatal(err)
	}
	secondUser := findUserMessage(t, second.Snapshot.Messages, "clone-me")
	secondTyped := secondUser.Metadata["typed"].(*typedPayload)
	if secondTyped.Values[0] != "owned" || secondUser.Metadata["cycle"].(map[string]any)["value"] != "before" {
		t.Fatal("repeated Wait returned aliased message state")
	}
	if second.Snapshot.Metadata["values"].([]string)[0] != "initial" {
		t.Fatal("repeated Wait returned aliased snapshot metadata")
	}
}

func TestIdleDeliveryQueueModes(t *testing.T) {
	tests := []struct {
		name     string
		delivery Delivery
		mode     agent.QueueMode
		want     [][]string
	}{
		{name: "steering one", delivery: DeliverySteering, mode: agent.QueueModeOneAtATime, want: [][]string{{"one"}, {"one", "two"}}},
		{name: "steering all", delivery: DeliverySteering, mode: agent.QueueModeAll, want: [][]string{{"one", "two"}}},
		{name: "follow-up one", delivery: DeliveryFollowUp, mode: agent.QueueModeOneAtATime, want: [][]string{{"one"}, {"one", "two"}}},
		{name: "follow-up all", delivery: DeliveryFollowUp, mode: agent.QueueModeAll, want: [][]string{{"one", "two"}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := &recordingModel{}
			definition := agent.AgentDefinition{Name: tt.name, Model: model}
			if tt.delivery == DeliverySteering {
				definition.SteeringMode = tt.mode
			} else {
				definition.FollowUpMode = tt.mode
			}
			loop := mustNewLoop(t, context.Background(), Config{
				Definition:    definition,
				Initial:       agent.AgentSnapshot{Messages: []agent.Message{assistantText("seed")}},
				QueueCapacity: 4,
			})
			admitBeforeRun(t, loop, textInput(tt.delivery, "one"), textInput(tt.delivery, "two"))
			if err := loop.Stop(StopGraceful); err != nil {
				t.Fatal(err)
			}
			result, err := waitLoop(t, loop)
			if err != nil {
				t.Fatal(err)
			}
			if result.StopMode != StopGraceful {
				t.Fatalf("stop mode = %q", result.StopMode)
			}
			if got := requestTexts(model.views()); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("requests = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestNextRunIsolationAndRunIDs(t *testing.T) {
	model := &recordingModel{}
	var eventMu sync.Mutex
	var runIDs []string
	loop := mustNewLoop(t, context.Background(), Config{
		Definition:    agent.AgentDefinition{Name: "next-run", Model: model},
		QueueCapacity: 4,
		OnEvent: func(event agent.AgentEvent) {
			if event.Type == agent.EventAgentStart {
				eventMu.Lock()
				runIDs = append(runIDs, event.RunID)
				eventMu.Unlock()
			}
		},
	})
	admitBeforeRun(t, loop, textInput(DeliveryNextRun, "one"), textInput(DeliveryNextRun, "two"))
	if err := loop.Stop(StopGraceful); err != nil {
		t.Fatal(err)
	}
	result, err := waitLoop(t, loop)
	if err != nil {
		t.Fatal(err)
	}
	if result.StopMode != StopGraceful || len(result.Unhandled) != 0 {
		t.Fatalf("result = %+v", result)
	}
	if got, want := requestTexts(model.views()), [][]string{{"one"}, {"one", "two"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("requests = %v, want %v", got, want)
	}
	eventMu.Lock()
	defer eventMu.Unlock()
	if len(runIDs) != 2 || runIDs[0] == "" || runIDs[1] == "" || runIDs[0] == runIDs[1] {
		t.Fatalf("run ids = %v", runIDs)
	}
}

func TestSteeringPriorityAndShouldStopHandoff(t *testing.T) {
	t.Run("steering before follow-up", func(t *testing.T) {
		model := &recordingModel{}
		loop := mustNewLoop(t, context.Background(), Config{
			Definition:    agent.AgentDefinition{Name: "priority", Model: model},
			QueueCapacity: 4,
		})
		admitBeforeRun(t, loop,
			textInput(DeliveryNextRun, "start"),
			textInput(DeliveryFollowUp, "follow"),
			textInput(DeliverySteering, "steer"),
		)
		if err := loop.Stop(StopGraceful); err != nil {
			t.Fatal(err)
		}
		if _, err := waitLoop(t, loop); err != nil {
			t.Fatal(err)
		}
		want := [][]string{{"start", "steer"}, {"start", "steer", "follow"}}
		if got := requestTexts(model.views()); !reflect.DeepEqual(got, want) {
			t.Fatalf("requests = %v, want %v", got, want)
		}
	})

	t.Run("ShouldStop leaves follow-up for outer invocation", func(t *testing.T) {
		model := &recordingModel{}
		var eventMu sync.Mutex
		var starts int
		loop := mustNewLoop(t, context.Background(), Config{
			Definition: agent.AgentDefinition{
				Name:  "should-stop",
				Model: model,
				ShouldStopAfterTurn: func(context.Context, agent.ShouldStopAfterTurnContext) (bool, error) {
					return true, nil
				},
			},
			QueueCapacity: 4,
			OnEvent: func(event agent.AgentEvent) {
				if event.Type == agent.EventAgentStart {
					eventMu.Lock()
					starts++
					eventMu.Unlock()
				}
			},
		})
		admitBeforeRun(t, loop, textInput(DeliveryNextRun, "start"), textInput(DeliveryFollowUp, "follow"))
		if err := loop.Stop(StopGraceful); err != nil {
			t.Fatal(err)
		}
		if _, err := waitLoop(t, loop); err != nil {
			t.Fatal(err)
		}
		want := [][]string{{"start"}, {"start", "follow"}}
		if got := requestTexts(model.views()); !reflect.DeepEqual(got, want) {
			t.Fatalf("requests = %v, want %v", got, want)
		}
		eventMu.Lock()
		defer eventMu.Unlock()
		if starts != 2 {
			t.Fatalf("agent starts = %d, want 2", starts)
		}
	})
}

func TestMaxTurnsPreservesQueuedInputs(t *testing.T) {
	t.Run("normal completion starts another invocation", func(t *testing.T) {
		model := &recordingModel{}
		loop := mustNewLoop(t, context.Background(), Config{
			Definition:    agent.AgentDefinition{Name: "max-normal", Model: model, MaxTurns: 1},
			QueueCapacity: 4,
		})
		admitBeforeRun(t, loop, textInput(DeliveryNextRun, "start"), textInput(DeliveryFollowUp, "follow"))
		if err := loop.Stop(StopGraceful); err != nil {
			t.Fatal(err)
		}
		if _, err := waitLoop(t, loop); err != nil {
			t.Fatal(err)
		}
		want := [][]string{{"start"}, {"start", "follow"}}
		if got := requestTexts(model.views()); !reflect.DeepEqual(got, want) {
			t.Fatalf("requests = %v, want %v", got, want)
		}
	})

	t.Run("required tool continuation returns queued input", func(t *testing.T) {
		model := &recordingModel{respond: func(_ context.Context, _ agent.ModelRequest, call int) (agent.AssistantStream, error) {
			if call != 1 {
				return nil, fmt.Errorf("unexpected model call %d", call)
			}
			return newImmediateStream(agent.Message{
				Role:       agent.RoleAssistant,
				StopReason: agent.StopReasonToolUse,
				ToolCalls: []agent.ToolCall{{
					ID:        "call-1",
					Name:      "echo",
					Arguments: json.RawMessage(`{}`),
				}},
			}, nil), nil
		}}
		loop := mustNewLoop(t, context.Background(), Config{
			Definition: agent.AgentDefinition{
				Name:     "max-tool",
				Model:    model,
				MaxTurns: 1,
				Tools: []agent.ToolDefinition{{
					Name:       "echo",
					Parameters: map[string]any{"type": "object", "additionalProperties": false},
					Execute: func(context.Context, string, any, agent.ToolUpdateFunc) (agent.ToolResult, error) {
						return agent.ToolResult{Content: []agent.Part{{Type: agent.PartTypeText, Text: "ok"}}}, nil
					},
				}},
			},
			QueueCapacity: 4,
		})
		admitBeforeRun(t, loop, textInput(DeliveryNextRun, "start"), textInput(DeliveryFollowUp, "follow"))
		result, err := waitLoop(t, loop)
		if !errors.Is(err, agent.ErrMaxTurnsExceeded) {
			t.Fatalf("Wait error = %v, want ErrMaxTurnsExceeded", err)
		}
		if result.StopMode != "" {
			t.Fatalf("stop mode = %q", result.StopMode)
		}
		if got := inputTexts(result.Unhandled); !reflect.DeepEqual(got, []string{"follow"}) {
			t.Fatalf("unhandled = %v", got)
		}
		if views := model.views(); len(views) != 1 {
			t.Fatalf("model calls = %d, want 1", len(views))
		}
		if len(result.Snapshot.Messages) != 3 || result.Snapshot.Messages[2].Role != agent.RoleTool {
			t.Fatalf("snapshot messages = %+v", result.Snapshot.Messages)
		}
	})
}

type contextKey string

func TestParentCancellationPreservesContextAndCause(t *testing.T) {
	key := contextKey("value")
	base, cancelDeadline := context.WithDeadline(context.WithValue(context.Background(), key, "kept"), time.Now().Add(time.Minute))
	defer cancelDeadline()
	parent, cancel := context.WithCancelCause(base)
	customCause := fmt.Errorf("shutdown: %w", context.Canceled)
	started := make(chan struct{}, 1)
	model := &recordingModel{
		contextKey: key,
		respond: func(ctx context.Context, _ agent.ModelRequest, _ int) (agent.AssistantStream, error) {
			started <- struct{}{}
			return newCancelStream(ctx), nil
		},
	}
	loop := mustNewLoop(t, parent, Config{
		Definition:    agent.AgentDefinition{Name: "parent", Model: model},
		QueueCapacity: 2,
	})
	if err := loop.Push(textInput(DeliveryNextRun, "active")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(testWait):
		t.Fatal("model did not start")
	}
	if err := loop.Push(textInput(DeliveryNextRun, "waiting")); err != nil {
		t.Fatal(err)
	}
	cancel(customCause)
	if err := loop.Push(textInput(DeliveryNextRun, "late")); !errors.Is(err, ErrNotAccepting) {
		t.Fatalf("post-cancel push error = %v", err)
	}
	if err := loop.Stop(StopImmediate); err != nil {
		t.Fatal(err)
	}
	result, err := waitLoop(t, loop)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, customCause) {
		t.Fatalf("parent error = %v", err)
	}
	if result.StopMode != StopImmediate {
		t.Fatalf("stop mode = %q", result.StopMode)
	}
	if got := inputTexts(result.Unhandled); !reflect.DeepEqual(got, []string{"waiting"}) {
		t.Fatalf("unhandled = %v", got)
	}
	views := model.views()
	if len(views) != 1 || !views[0].hasDeadline || views[0].contextVal != "kept" {
		t.Fatalf("context view = %+v", views)
	}
}

func TestWaitContextAndConcurrentDetachedResults(t *testing.T) {
	model := &recordingModel{}
	loop := mustNewLoop(t, context.Background(), Config{
		Definition:    agent.AgentDefinition{Name: "wait", Model: model},
		QueueCapacity: 1,
	})
	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := loop.Wait(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("waiter error = %v", err)
	}
	if err := loop.Push(textInput(DeliveryNextRun, "after-timeout")); err != nil {
		t.Fatalf("waiter timeout stopped loop: %v", err)
	}
	if err := loop.Stop(StopGraceful); err != nil {
		t.Fatal(err)
	}
	if _, err := waitLoop(t, loop); err != nil {
		t.Fatal(err)
	}

	const waiters = 8
	results := make(chan Result, waiters)
	errs := make(chan error, waiters)
	var wg sync.WaitGroup
	for i := 0; i < waiters; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := loop.Wait(context.Background())
			results <- result
			errs <- err
		}()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var detached []Result
	for result := range results {
		detached = append(detached, result)
	}
	if len(detached) != waiters {
		t.Fatalf("results = %d", len(detached))
	}
	detached[0].Snapshot.Messages[0].Parts[0].Text = "mutated"
	for i := 1; i < len(detached); i++ {
		if detached[i].Snapshot.Messages[0].Parts[0].Text != "after-timeout" {
			t.Fatal("concurrent Wait results share message storage")
		}
	}
}

func TestOnEventSlowAndReentrant(t *testing.T) {
	t.Run("slow callback backpressures completion", func(t *testing.T) {
		entered := make(chan struct{})
		release := make(chan struct{})
		var once sync.Once
		loop := mustNewLoop(t, context.Background(), Config{
			Definition:    agent.AgentDefinition{Name: "slow-event", Model: &recordingModel{}},
			QueueCapacity: 1,
			OnEvent: func(event agent.AgentEvent) {
				if event.Type == agent.EventAgentStart {
					once.Do(func() {
						close(entered)
						<-release
					})
				}
			},
		})
		if err := loop.Push(textInput(DeliveryNextRun, "slow")); err != nil {
			t.Fatal(err)
		}
		select {
		case <-entered:
		case <-time.After(testWait):
			t.Fatal("callback did not start")
		}
		if err := loop.Stop(StopGraceful); err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			_, _ = loop.Wait(context.Background())
			close(done)
		}()
		select {
		case <-done:
			t.Fatal("Wait completed while OnEvent was blocked")
		case <-time.After(30 * time.Millisecond):
		}
		close(release)
		select {
		case <-done:
		case <-time.After(testWait):
			t.Fatal("Wait did not complete after callback release")
		}
	})

	t.Run("callback may push and stop", func(t *testing.T) {
		model := &recordingModel{}
		var loop *TurnLoop
		var once sync.Once
		callbackErr := make(chan error, 1)
		loop = mustNewLoop(t, context.Background(), Config{
			Definition:    agent.AgentDefinition{Name: "reentrant", Model: model},
			QueueCapacity: 2,
			OnEvent: func(event agent.AgentEvent) {
				if event.Type != agent.EventAgentStart {
					return
				}
				once.Do(func() {
					if err := loop.Push(textInput(DeliveryFollowUp, "from-callback")); err != nil {
						callbackErr <- err
						return
					}
					callbackErr <- loop.Stop(StopGraceful)
				})
			},
		})
		if err := loop.Push(textInput(DeliveryNextRun, "start")); err != nil {
			t.Fatal(err)
		}
		result, err := waitLoop(t, loop)
		if err != nil {
			t.Fatal(err)
		}
		if err := <-callbackErr; err != nil {
			t.Fatal(err)
		}
		if got := userTexts(result.Snapshot.Messages); !reflect.DeepEqual(got, []string{"start", "from-callback"}) {
			t.Fatalf("snapshot user messages = %v", got)
		}
		if len(model.views()) != 2 {
			t.Fatalf("model calls = %d, want 2", len(model.views()))
		}
	})
}

func TestRuntimeErrorPreservesSnapshotAndUnhandled(t *testing.T) {
	runErr := errors.New("model unavailable")
	model := &recordingModel{respond: func(context.Context, agent.ModelRequest, int) (agent.AssistantStream, error) {
		return nil, runErr
	}}
	loop := mustNewLoop(t, context.Background(), Config{
		Definition:    agent.AgentDefinition{Name: "error", Model: model},
		QueueCapacity: 2,
	})
	admitBeforeRun(t, loop, textInput(DeliveryNextRun, "started"), textInput(DeliveryNextRun, "waiting"))
	first, err := waitLoop(t, loop)
	if !errors.Is(err, runErr) {
		t.Fatalf("Wait error = %v", err)
	}
	if first.StopMode != "" {
		t.Fatalf("stop mode = %q", first.StopMode)
	}
	if got := userTexts(first.Snapshot.Messages); !reflect.DeepEqual(got, []string{"started"}) {
		t.Fatalf("snapshot users = %v", got)
	}
	if got := inputTexts(first.Unhandled); !reflect.DeepEqual(got, []string{"waiting"}) {
		t.Fatalf("unhandled = %v", got)
	}
	first.Unhandled[0].Message.Parts[0].Text = "mutated"
	second, err := waitLoop(t, loop)
	if !errors.Is(err, runErr) || textOf(second.Unhandled[0].Message) != "waiting" {
		t.Fatal("repeated Wait returned aliased unhandled input")
	}
}

func TestPushStopRacePartitionsAcceptedInput(t *testing.T) {
	for iteration := 0; iteration < 50; iteration++ {
		model := &recordingModel{respond: func(ctx context.Context, _ agent.ModelRequest, _ int) (agent.AssistantStream, error) {
			return newCancelStream(ctx), nil
		}}
		loop := mustNewLoop(t, context.Background(), Config{
			Definition:    agent.AgentDefinition{Name: "race", Model: model},
			QueueCapacity: 1,
		})
		start := make(chan struct{})
		pushErr := make(chan error, 1)
		stopErr := make(chan error, 1)
		go func() {
			<-start
			pushErr <- loop.Push(textInput(DeliveryNextRun, "raced"))
		}()
		go func() {
			<-start
			stopErr <- loop.Stop(StopImmediate)
		}()
		close(start)
		pushed := <-pushErr
		if err := <-stopErr; err != nil {
			t.Fatal(err)
		}
		result, err := waitLoop(t, loop)
		if err != nil {
			t.Fatalf("iteration %d: Wait error = %v", iteration, err)
		}
		occurrences := countText(result.Snapshot.Messages, "raced") + countInputText(result.Unhandled, "raced")
		switch {
		case pushed == nil && occurrences != 1:
			t.Fatalf("iteration %d: accepted input occurrences = %d", iteration, occurrences)
		case errors.Is(pushed, ErrNotAccepting) && occurrences != 0:
			t.Fatalf("iteration %d: rejected input occurrences = %d", iteration, occurrences)
		case pushed != nil && !errors.Is(pushed, ErrNotAccepting):
			t.Fatalf("iteration %d: push error = %v", iteration, pushed)
		}
	}
}

func mustNewLoop(t *testing.T, ctx context.Context, config Config) *TurnLoop {
	t.Helper()
	loop, err := New(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	return loop
}

func waitLoop(t *testing.T, loop *TurnLoop) (Result, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), testWait)
	defer cancel()
	return loop.Wait(ctx)
}

func admitBeforeRun(t *testing.T, loop *TurnLoop, inputs ...Input) {
	t.Helper()
	loop.mu.Lock()
	defer loop.mu.Unlock()
	if !loop.accepting || len(loop.queue)+len(inputs) > loop.queueCapacity {
		t.Fatalf("cannot admit %d test inputs into queue of %d", len(inputs), loop.queueCapacity)
	}
	for _, input := range inputs {
		if !validDelivery(input.Delivery) {
			t.Fatalf("invalid test delivery %q", input.Delivery)
		}
		loop.queue = append(loop.queue, cloneValue(input))
	}
	loop.notify()
}

func textInput(delivery Delivery, text string) Input {
	return Input{Delivery: delivery, Message: agent.NewUserTextMessage(text)}
}

func assistantText(text string) agent.Message {
	return agent.Message{
		Role:       agent.RoleAssistant,
		Parts:      []agent.Part{{Type: agent.PartTypeText, Text: text}},
		StopReason: agent.StopReasonStop,
	}
}

func userTexts(messages []agent.Message) []string {
	var texts []string
	for _, message := range messages {
		if message.Role != agent.RoleUser {
			continue
		}
		texts = append(texts, textOf(message))
	}
	return texts
}

func requestTexts(views []requestView) [][]string {
	texts := make([][]string, len(views))
	for i, view := range views {
		texts[i] = append([]string(nil), view.userTexts...)
	}
	return texts
}

func inputTexts(inputs []Input) []string {
	texts := make([]string, len(inputs))
	for i, input := range inputs {
		texts[i] = textOf(input.Message)
	}
	return texts
}

func textOf(message agent.Message) string {
	for _, part := range message.Parts {
		if part.Type == agent.PartTypeText {
			return part.Text
		}
	}
	return ""
}

func findUserMessage(t *testing.T, messages []agent.Message, text string) agent.Message {
	t.Helper()
	for _, message := range messages {
		if message.Role == agent.RoleUser && textOf(message) == text {
			return message
		}
	}
	t.Fatalf("user message %q not found", text)
	return agent.Message{}
}

func countText(messages []agent.Message, text string) int {
	count := 0
	for _, message := range messages {
		if textOf(message) == text {
			count++
		}
	}
	return count
}

func countInputText(inputs []Input, text string) int {
	count := 0
	for _, input := range inputs {
		if textOf(input.Message) == text {
			count++
		}
	}
	return count
}
