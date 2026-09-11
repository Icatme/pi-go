package pigo

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestCompleteDrainsEventsBeforeReturning(t *testing.T) {
	for _, simple := range []bool{false, true} {
		name := "Complete"
		if simple {
			name = "CompleteSimple"
		}
		for _, reason := range []StopReason{StopReasonStop, StopReasonError, StopReasonAborted} {
			t.Run(name+"/"+string(reason), func(t *testing.T) {
				requestContext, cancel := context.WithCancel(context.Background())
				defer cancel()
				stream := newAssistantMessageEventStream()
				observer := &completeBlockingObserver{
					release: make(chan struct{}),
				}
				producerDone := make(chan struct{})
				eventsProduced := make(chan struct{})
				t.Cleanup(func() {
					cancel()
					close(observer.release)
					for range stream.Events() {
					}
					waitForStreamProducer(t, producerDone, "during completion cleanup")
				})

				const deltaCount = 1500
				const delta = "0123456789abcdef"
				expected := AssistantMessage{
					StopReason: reason,
					Content:    []ContentBlock{TextContent{Text: strings.Repeat(delta, deltaCount)}},
				}
				switch reason {
				case StopReasonError:
					expected.ErrorMessage = "provider failed"
				case StopReasonAborted:
					expected.ErrorMessage = context.Canceled.Error()
				}
				emit := func(model Model, ctx context.Context, obs Observer) *AssistantMessageEventStream {
					stream.setObserver(obs, model)
					ctx = stream.startRequest(ctx, nil)
					go func() {
						defer close(producerDone)
						stream.push(AssistantMessageEvent{Type: AssistantMessageEventStart})
						stream.push(AssistantMessageEvent{Type: AssistantMessageEventTextStart})
						for index := 0; index < deltaCount; index++ {
							stream.push(AssistantMessageEvent{
								Type:  AssistantMessageEventTextDelta,
								Delta: delta,
								Partial: AssistantMessage{Content: []ContentBlock{
									TextContent{Text: strings.Repeat(delta, index+1)},
								}},
							})
						}
						close(eventsProduced)
						if reason == StopReasonAborted {
							<-ctx.Done()
						}
						stream.push(AssistantMessageEvent{Type: AssistantMessageEventTextEnd, Content: strings.Repeat(delta, deltaCount)})
						if reason == StopReasonStop {
							stream.push(AssistantMessageEvent{Type: AssistantMessageEventDone, Reason: reason, Message: expected})
						} else {
							stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: reason, Error: expected})
						}
						stream.finish(expected)
					}()
					return stream
				}
				api := API(t.Name())
				RegisterAPIModuleForSource(t.Name(), APIModule{
					API: api,
					Stream: func(model Model, _ Context, options ProviderStreamOptions) *AssistantMessageEventStream {
						return emit(model, options.RequestContext, options.Observer)
					},
					StreamSimple: func(model Model, _ Context, options SimpleStreamOptions) *AssistantMessageEventStream {
						return emit(model, options.RequestContext, options.Observer)
					},
				})
				t.Cleanup(func() { UnregisterAPIModules(t.Name()) })
				model := Model{API: api}
				completed := make(chan AssistantMessage, 1)
				go func() {
					if simple {
						completed <- CompleteSimple(model, Context{}, SimpleStreamOptions{RequestContext: requestContext, Observer: observer})
					} else {
						completed <- Complete(model, Context{}, ProviderStreamOptions{RequestContext: requestContext, Observer: observer})
					}
				}()
				waitForStreamProducer(t, eventsProduced, "after more events than the channel capacity")
				if reason == StopReasonAborted {
					cancel()
				}
				select {
				case result := <-completed:
					if !reflect.DeepEqual(result, expected) {
						t.Fatalf("expected unchanged completion result, got %+v", result)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("completion waited for a blocked finish observer")
				}

				// A result alone does not prove the dispatcher released its queued snapshots.
				select {
				case _, ok := <-stream.Events():
					if ok {
						t.Fatal("completion returned with unconsumed events and a blocked dispatcher")
					}
				default:
					t.Fatal("completion returned before the event dispatcher closed")
				}
				if len(observer.events) != deltaCount+4 {
					t.Fatalf("expected all events to reach the observer, got %d", len(observer.events))
				}
			})
		}
	}
}

type completeBlockingObserver struct {
	recordingObserver
	release chan struct{}
}

func (o *completeBlockingObserver) OnStreamFinish(ctx context.Context, model Model, final AssistantMessage, eventCount int, droppedCount int, duration time.Duration) {
	<-o.release
	o.recordingObserver.OnStreamFinish(ctx, model, final, eventCount, droppedCount, duration)
}
