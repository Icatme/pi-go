package prebuilt

import (
	"context"
	"errors"
	"strings"
	"testing"

	core "github.com/Icatme/pi-go/agent"
	"github.com/Icatme/pi-go/agent/session"
	"github.com/Icatme/pi-go/agent/turnloop"
)

type nilPointerStream struct{}

func (*nilPointerStream) Events() <-chan core.AssistantEvent {
	panic("typed nil Events must not be called")
}
func (*nilPointerStream) Wait() (core.Message, error) { panic("typed nil Wait must not be called") }
func (*nilPointerStream) Close() error                { panic("typed nil Close must not be called") }

type nilMapStream map[string]int

func (nilMapStream) Events() <-chan core.AssistantEvent { panic("typed nil Events must not be called") }
func (nilMapStream) Wait() (core.Message, error)        { panic("typed nil Wait must not be called") }
func (nilMapStream) Close() error                       { panic("typed nil Close must not be called") }

type nilSliceStream []int

func (nilSliceStream) Events() <-chan core.AssistantEvent {
	panic("typed nil Events must not be called")
}
func (nilSliceStream) Wait() (core.Message, error) { panic("typed nil Wait must not be called") }
func (nilSliceStream) Close() error                { panic("typed nil Close must not be called") }

type nilChanStream chan int

func (nilChanStream) Events() <-chan core.AssistantEvent {
	panic("typed nil Events must not be called")
}
func (nilChanStream) Wait() (core.Message, error) { panic("typed nil Wait must not be called") }
func (nilChanStream) Close() error                { panic("typed nil Close must not be called") }

type nilFuncStream func()

func (nilFuncStream) Events() <-chan core.AssistantEvent {
	panic("typed nil Events must not be called")
}
func (nilFuncStream) Wait() (core.Message, error) { panic("typed nil Wait must not be called") }
func (nilFuncStream) Close() error                { panic("typed nil Close must not be called") }

func TestNilAssistantStreamFailsClosedAcrossEntryPoints(t *testing.T) {
	cases := []struct {
		name   string
		stream core.AssistantStream
	}{
		{"nil", nil}, {"pointer", (*nilPointerStream)(nil)}, {"map", nilMapStream(nil)},
		{"slice", nilSliceStream(nil)}, {"channel", nilChanStream(nil)}, {"function", nilFuncStream(nil)},
	}
	for _, tc := range cases {
		for _, entry := range []string{"engine", "runner", "chat", "print", "turnloop"} {
			t.Run(tc.name+"/"+entry, func(t *testing.T) {
				toolCalls := 0
				def := core.AgentDefinition{Model: core.StreamFunc(func(context.Context, core.ModelRequest) (core.AssistantStream, error) { return tc.stream, nil }), Tools: []core.ToolDefinition{{Name: "effect", Execute: func(context.Context, core.ToolExecutionContext) (core.ToolResult, error) {
					toolCalls++
					return core.ToolResult{}, nil
				}}}}
				var err error
				switch entry {
				case "engine":
					var snapshot *core.AgentSnapshot
					snapshot, err = core.NewEngine().Run(t.Context(), def, &core.AgentSnapshot{}, []core.Message{core.NewUserTextMessage("hello")}, nil)
					if snapshot == nil || snapshot.Error == "" {
						t.Fatalf("missing failed snapshot: %+v", snapshot)
					}
				case "runner":
					r, e := core.NewRunner(def)
					if e != nil {
						t.Fatal(e)
					}
					stream := r.Query(t.Context(), "hello")
					ends := 0
					for event := range stream.Events() {
						if event.Type == core.EventAgentEnd {
							ends++
							if event.Err == nil {
								t.Fatal("missing runner terminal error")
							}
						}
					}
					var snapshot core.AgentSnapshot
					snapshot, err = stream.Wait()
					if snapshot.Error == "" || ends != 1 {
						t.Fatalf("snapshot=%+v ends=%d", snapshot, ends)
					}
				case "chat", "print":
					chat, e := NewChatAgent(def)
					if e != nil {
						t.Fatal(e)
					}
					if entry == "chat" {
						_, err = chat.Chat(t.Context(), "hello")
					} else {
						err = chat.PrintStream(t.Context(), "hello", func(string) error { return nil })
					}
				case "turnloop":
					loop, e := turnloop.New(t.Context(), turnloop.Config{Definition: def, QueueCapacity: 1})
					if e != nil {
						t.Fatal(e)
					}
					receipt, e := loop.PushTracked(turnloop.Input{Delivery: turnloop.DeliveryNextRun, Message: core.NewUserTextMessage("hello")})
					if e != nil {
						t.Fatal(e)
					}
					_, err = loop.Wait(t.Context())
					result, e := receipt.Wait(t.Context())
					if e != nil || result.Disposition != turnloop.DispositionHandedOff || result.Err == nil {
						t.Fatalf("receipt=%+v waiterr=%v", result, e)
					}
				}
				if err == nil || !strings.Contains(err.Error(), "nil AssistantStream") {
					t.Fatalf("failure was not structured: %v", err)
				}
				if toolCalls != 0 {
					t.Fatal("malformed stream executed tools")
				}
			})
		}
	}
}

func TestNilAssistantStreamPreservesOriginalError(t *testing.T) {
	original := &terminalStatusError{502}
	for _, stream := range []core.AssistantStream{nil, (*nilPointerStream)(nil)} {
		chat, err := NewChatAgent(core.AgentDefinition{Model: core.StreamFunc(func(context.Context, core.ModelRequest) (core.AssistantStream, error) { return stream, original })})
		if err != nil {
			t.Fatal(err)
		}
		_, err = chat.Chat(t.Context(), "hello")
		var got *terminalStatusError
		if !errors.Is(err, original) || !errors.As(err, &got) || strings.Contains(err.Error(), "nil AssistantStream") {
			t.Fatalf("original provider failure replaced: %v", err)
		}
	}
}

func TestNilAssistantStreamDoesNotPoisonNextChat(t *testing.T) {
	calls := 0
	chat, err := NewChatAgent(core.AgentDefinition{Model: core.StreamFunc(func(context.Context, core.ModelRequest) (core.AssistantStream, error) {
		calls++
		if calls == 1 {
			return nil, nil
		}
		return terminalModel{final: core.Message{Role: core.RoleAssistant, Parts: []core.Part{{Type: core.PartTypeText, Text: "recovered"}}, StopReason: core.StopReasonStop}}.Stream(t.Context(), core.ModelRequest{})
	})})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = chat.Chat(t.Context(), "first"); err == nil {
		t.Fatal("malformed stream succeeded")
	}
	text, err := chat.Chat(t.Context(), "second")
	if err != nil || text != "recovered" {
		t.Fatalf("next chat text=%q err=%v", text, err)
	}
}

func TestNilAssistantStreamPreservesPromptContract(t *testing.T) {
	a, err := core.NewAgent(core.AgentDefinition{Model: core.StreamFunc(func(context.Context, core.ModelRequest) (core.AssistantStream, error) { return nil, nil })})
	if err != nil {
		t.Fatal(err)
	}
	ends := 0
	unsubscribe := a.Subscribe(func(e core.AgentEvent) {
		if e.Type == core.EventMessageEnd && e.Message != nil && e.Message.Role == core.RoleAssistant {
			ends++
			if e.Err == nil || e.Message.StopReason != core.StopReasonError {
				t.Errorf("missing assistant terminal error: %+v", e)
			}
		}
	})
	defer unsubscribe()
	if err := a.PromptText(t.Context(), "hello"); err != nil {
		t.Fatalf("Prompt runtime failure contract changed: %v", err)
	}
	if ends != 1 || !strings.Contains(a.State().Error, "nil AssistantStream") {
		t.Fatalf("ends=%d state=%+v", ends, a.State())
	}
}

func TestNilAssistantStreamWithSessionDoesNotReplayInput(t *testing.T) {
	storage, err := session.NewMemoryStorage(session.Header{ID: "nil-stream-session"})
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := session.New(storage, session.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer conversation.Close()
	scope := func() session.RuntimeScope { return session.RuntimeScope{Identity: "host"} }
	entered, release := make(chan struct{}), make(chan struct{})
	var binding *session.RunBinding
	loop, err := turnloop.New(t.Context(), turnloop.Config{Definition: core.AgentDefinition{Model: core.StreamFunc(func(context.Context, core.ModelRequest) (core.AssistantStream, error) { return nil, nil })}, QueueCapacity: 2, PrepareInvocation: func(context.Context) (turnloop.Invocation, error) {
		var err error
		binding, err = conversation.PrepareRun(session.MainLane, session.RuntimeOptions{Scope: scope})
		close(entered)
		<-release
		return binding, err
	}})
	if err != nil {
		t.Fatal(err)
	}
	first, err := loop.PushTracked(turnloop.Input{Delivery: turnloop.DeliveryNextRun, Message: core.NewUserTextMessage("confirmed input")})
	if err != nil {
		t.Fatal(err)
	}
	<-entered
	waiting, err := loop.PushTracked(turnloop.Input{Delivery: turnloop.DeliveryNextRun, Message: core.NewUserTextMessage("not delivered")})
	if err != nil {
		t.Fatal(err)
	}
	close(release)
	result, err := loop.Wait(t.Context())
	if err == nil || len(result.Unhandled) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	for i, receipt := range []*turnloop.Receipt{first, waiting} {
		got, e := receipt.Wait(t.Context())
		want := turnloop.DispositionHandedOff
		if i == 1 {
			want = turnloop.DispositionUnhandled
		}
		if e != nil || got.Disposition != want || got.Err == nil {
			t.Fatalf("receipt=%+v err=%v", got, e)
		}
	}
	status, err := binding.CommitStatus()
	if err != nil || status.Commits != 1 || status.Pending || status.Invalidated || len(status.Messages) != 1 {
		t.Fatalf("confirmed input status=%+v err=%v", status, err)
	}
	restored, err := conversation.PrepareRun(session.MainLane, session.RuntimeOptions{Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	baseline, err := restored.CommitStatus()
	if err != nil || baseline.Commits != 0 || len(baseline.Messages) != 1 || baseline.Messages[0].Parts[0].Text != "confirmed input" {
		t.Fatalf("restored=%+v err=%v", baseline, err)
	}
}
