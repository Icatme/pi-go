package agent

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestAgentListenerPanicReleasesOwnedRun(t *testing.T) {
	for _, continuing := range []bool{false, true} {
		for _, target := range []EventType{EventAgentStart, EventTurnStart, EventMessageStart, EventMessageEnd, EventMessageUpdate, EventTurnEnd, EventAgentEnd} {
			t.Run(string(target)+map[bool]string{true: "/continue", false: "/prompt"}[continuing], func(t *testing.T) {
				a, err := NewAgent(AgentDefinition{Model: StreamFunc(func(context.Context, ModelRequest) (AssistantStream, error) {
					return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop}, []AssistantEvent{{Type: AssistantEventTextDelta, Message: Message{Role: RoleAssistant, Parts: []Part{{Type: PartTypeText, Text: "partial"}}}, Delta: "partial"}}), nil
				})}, WithSnapshot(AgentSnapshot{Messages: []Message{NewUserTextMessage("prior")}}))
				if err != nil {
					t.Fatal(err)
				}
				sentinel := new(int)
				var atPanic AgentSnapshot
				off := a.Subscribe(func(e AgentEvent) {
					if e.Type == target {
						atPanic = a.Snapshot()
						panic(sentinel)
					}
				})
				var caught any
				func() {
					defer func() { caught = recover() }()
					if continuing {
						_ = a.Continue(t.Context())
					} else {
						_ = a.Prompt(t.Context(), NewUserTextMessage("new"))
					}
				}()
				off()
				if caught != sentinel {
					t.Fatalf("lost panic: %v", caught)
				}
				if !reflect.DeepEqual(a.Snapshot(), atPanic) {
					t.Fatal("cleanup changed snapshot")
				}
				a.mu.RLock()
				running, cancel, idle := a.running, a.cancel, a.idle
				a.mu.RUnlock()
				if running || cancel != nil || idle != nil || a.State().IsStreaming {
					t.Fatal("run ownership leaked")
				}
				if err = a.WaitForIdle(t.Context()); err != nil {
					t.Fatal(err)
				}
				if err = a.Prompt(t.Context(), NewUserTextMessage("fresh")); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestAgentInterruptedCleanupCannotReleaseSuccessor(t *testing.T) {
	a, err := NewAgent(AgentDefinition{Model: StreamFunc(func(context.Context, ModelRequest) (AssistantStream, error) {
		return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop}, []AssistantEvent{{Type: AssistantEventTextDelta, Message: Message{Role: RoleAssistant, Parts: []Part{{Type: PartTypeText, Text: "partial"}}}, Delta: "partial"}}), nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	oldCtx, _, _, oldIdle, err := a.startRun(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	a.releaseInterruptedRun(oldIdle)
	if oldCtx.Err() != context.Canceled {
		t.Fatal("old context not canceled")
	}
	current, _, _, newIdle, err := a.startRun(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { defer close(done); a.releaseInterruptedRun(oldIdle) }()
	<-done
	if current.Err() != nil {
		t.Fatal("successor canceled")
	}
	a.mu.RLock()
	running, idle := a.running, a.idle
	a.mu.RUnlock()
	if !running || idle != newIdle {
		t.Fatal("successor released")
	}
	a.Abort()
	a.releaseInterruptedRun(newIdle)
	if err = a.WaitForIdle(t.Context()); err != nil {
		t.Fatal(err)
	}
}

type panicReleaseStream struct {
	ctx                   context.Context
	events                chan AssistantEvent
	closeCount, waitCount int
	abnormal              bool
}

func (s *panicReleaseStream) Events() <-chan AssistantEvent { return s.events }
func (s *panicReleaseStream) Wait() (Message, error) {
	s.waitCount++
	if s.closeCount != 1 {
		return Message{}, errors.New("Wait called before Close")
	}
	return Message{Role: RoleAssistant, StopReason: StopReasonStop}, nil
}
func (s *panicReleaseStream) Close() error {
	s.closeCount++
	if s.abnormal {
		select {
		case <-s.ctx.Done():
		case <-time.After(time.Second):
			return errors.New("Close ran before cancellation")
		}
	} else if s.ctx.Err() != nil {
		return errors.New("normal stream canceled before Close")
	}
	return nil
}
func TestAgentListenerPanicCancelsStreamBeforeClose(t *testing.T) {
	for _, abnormal := range []bool{false, true} {
		t.Run(fmt.Sprint(abnormal), func(t *testing.T) {
			var stream *panicReleaseStream
			var parent context.Context
			a, err := NewAgent(AgentDefinition{Model: StreamFunc(func(ctx context.Context, _ ModelRequest) (AssistantStream, error) {
				parent = ctx
				events := make(chan AssistantEvent, 1)
				events <- AssistantEvent{Type: AssistantEventStart, Message: Message{Role: RoleAssistant}}
				close(events)
				stream = &panicReleaseStream{ctx: ctx, events: events, abnormal: abnormal}
				return stream, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			marker := new(int)
			var caught any
			off := a.Subscribe(func(e AgentEvent) {
				if abnormal && e.Type == EventMessageStart && e.ModelResponse {
					panic(marker)
				}
			})
			started := time.Now()
			func() {
				defer func() { caught = recover() }()
				if err = a.Prompt(t.Context(), NewUserTextMessage("one")); err != nil {
					t.Fatal(err)
				}
			}()
			off()
			if abnormal && caught != marker || !abnormal && caught != nil {
				t.Fatalf("panic=%v", caught)
			}
			if time.Since(started) > 500*time.Millisecond {
				t.Fatal("close waited without cancellation")
			}
			if stream.closeCount != 1 || stream.waitCount != map[bool]int{false: 1, true: 0}[abnormal] {
				t.Fatalf("close=%d wait=%d", stream.closeCount, stream.waitCount)
			}
			if parent.Err() != context.Canceled {
				t.Fatal("request context still live")
			}
		})
	}
}

func TestAgentInterruptedCancelReentrantCallback(t *testing.T) {
	a, err := NewAgent(AgentDefinition{Model: StreamFunc(func(context.Context, ModelRequest) (AssistantStream, error) { return nil, errors.New("unused") })})
	if err != nil {
		t.Fatal(err)
	}
	ctx, _, _, idle, err := a.startRun(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	called := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = a.Snapshot(); a.Abort(); close(called) })
	defer stop()
	a.FollowUp(NewUserTextMessage("keep queued"))
	before := a.Snapshot()
	a.releaseInterruptedRun(idle)
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("cancel callback deadlocked")
	}
	if !reflect.DeepEqual(before, a.Snapshot()) || len(a.PeekQueuedMessages()) != 1 {
		t.Fatal("cleanup rewrote snapshot or queues")
	}
}
