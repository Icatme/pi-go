package pigo

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func awaitStreamSignal(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stream lifecycle did not terminate")
	}
}

func TestStreamCancellationReleasesUnreadEvents(t *testing.T) {
	for _, simple := range []bool{false, true} {
		for _, state := range []string{"idle", "backpressured", "finished"} {
			t.Run(fmt.Sprintf("simple=%t/%s", simple, state), func(t *testing.T) {
				parent, cancel := context.WithCancel(context.Background())
				defer cancel()
				produced, producerDone := make(chan struct{}), make(chan struct{})
				expected := AssistantMessage{StopReason: StopReasonAborted, ResponseID: "provider-evidence"}
				expected.Usage.Input = 37
				expected.UsageReported = true
				if state == "finished" {
					expected.StopReason = StopReasonStop
				}
				emit := func(ctx context.Context) *AssistantMessageEventStream {
					s := newAssistantMessageEventStream()
					go func() {
						defer close(producerDone)
						if state != "idle" {
							for index := 0; index < 1500; index++ {
								s.push(AssistantMessageEvent{Type: AssistantMessageEventTextDelta, Delta: "part"})
							}
						}
						if state == "finished" {
							s.finish(expected)
							close(produced)
							return
						}
						close(produced)
						<-ctx.Done()
						s.finish(expected)
					}()
					return s
				}
				api := API(t.Name())
				RegisterAPIModuleForSource(t.Name(), APIModule{
					API: api,
					Stream: func(_ Model, _ Context, options ProviderStreamOptions) *AssistantMessageEventStream {
						return emit(options.RequestContext)
					},
					StreamSimple: func(_ Model, _ Context, options SimpleStreamOptions) *AssistantMessageEventStream {
						return emit(options.RequestContext)
					},
				})
				t.Cleanup(func() { UnregisterAPIModules(t.Name()) })
				var stream *AssistantMessageEventStream
				if simple {
					stream = StreamSimple(Model{API: api}, Context{}, SimpleStreamOptions{RequestContext: parent})
				} else {
					stream = Stream(Model{API: api}, Context{}, ProviderStreamOptions{RequestContext: parent})
				}
				t.Cleanup(stream.Close)
				awaitStreamSignal(t, produced)
				cancel()
				// No Events consumer is running: cancellation itself must unblock send/Wait.
				awaitStreamSignal(t, stream.dispatcherDone)
				awaitStreamSignal(t, producerDone)
				stream.Close()
				if got := stream.Result(); !reflect.DeepEqual(got, expected) {
					t.Fatalf("lost final provider evidence: got %+v, want %+v", got, expected)
				}
				stream.queueMu.Lock()
				defer stream.queueMu.Unlock()
				if stream.pending != nil || stream.pendingBytes != 0 || stream.pendingDelta != 0 || len(stream.events) != 0 || stream.stopContext != nil || stream.cancelRequest != nil {
					t.Fatal("closed stream retained queued events or cancellation hooks")
				}
			})
		}
	}
}

func TestStreamCloseCancelsOwnedRequestWithoutWaitingForProvider(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	var requestCtx context.Context
	stream := managedAssistantStream(parent, func(ctx context.Context) *AssistantMessageEventStream {
		requestCtx = ctx
		return newAssistantMessageEventStream()
	})
	closed := make(chan struct{})
	go func() { stream.Close(); close(closed) }()
	awaitStreamSignal(t, closed)
	if requestCtx.Err() != context.Canceled || parent.Err() != nil {
		t.Fatal("Close must cancel only the owned request context")
	}
	// The provider can complete independently after Close; no fabricated result.
	final := AssistantMessage{StopReason: StopReasonAborted, ResponseID: "late-result"}
	stream.finish(final)
	if got := stream.Result(); !reflect.DeepEqual(got, final) {
		t.Fatalf("unexpected final result: %+v", got)
	}
}

func TestFinishedStreamPreservesEventsAndReleasesContextHook(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := managedAssistantStream(parent, func(ctx context.Context) *AssistantMessageEventStream {
		s := newAssistantMessageEventStream()
		// Provider-local cancellation on normal completion must not discard delivery.
		_, providerCancel := context.WithCancel(ctx)
		for index := 0; index < 20; index++ {
			s.push(AssistantMessageEvent{Type: AssistantMessageEventTextDelta, Delta: "part"})
		}
		s.push(AssistantMessageEvent{Type: AssistantMessageEventDone, Reason: StopReasonStop})
		s.finish(AssistantMessage{StopReason: StopReasonStop})
		providerCancel()
		return s
	})
	defer stream.Close()
	if result := stream.Result(); result.StopReason != StopReasonStop {
		t.Fatalf("unexpected result: %+v", result)
	}
	count := 0
	var last AssistantMessageEvent
	for event := range stream.Events() {
		count++
		last = event
	}
	awaitStreamSignal(t, stream.dispatcherDone)
	if count != 21 || last.Type != AssistantMessageEventDone || parent.Err() != nil {
		t.Fatalf("normal completion lost events or cancelled its parent: count=%d last=%s", count, last.Type)
	}
	stream.queueMu.Lock()
	defer stream.queueMu.Unlock()
	if stream.stopContext != nil || stream.cancelRequest != nil || stream.pending != nil {
		t.Fatal("completed dispatcher retained request hooks or pending storage")
	}
}

func TestStreamConcurrentCloseAndFinish(t *testing.T) {
	stream := newAssistantMessageEventStream()
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		for index := 0; index < 2000; index++ {
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventTextDelta, Delta: "part"})
		}
		stream.finish(AssistantMessage{StopReason: StopReasonStop})
	}()
	for index := 0; index < 16; index++ {
		workers.Add(1)
		go func() { defer workers.Done(); stream.Close() }()
	}
	done := make(chan struct{})
	go func() { workers.Wait(); close(done) }()
	awaitStreamSignal(t, done)
}

func TestStreamLifecycleAndPayloadBudgetsFailExplicitly(t *testing.T) {
	for _, payload := range []bool{false, true} {
		t.Run(fmt.Sprintf("payload=%t", payload), func(t *testing.T) {
			stream := newAssistantMessageEventStream()
			defer stream.Close()
			event := AssistantMessageEvent{Type: AssistantMessageEventTextStart}
			count := assistantMessageEventPendingLimit * 4
			if payload {
				event.Content = strings.Repeat("x", assistantMessageEventByteBudget/4)
				count = 20
			}
			for index := 0; index < count; index++ {
				stream.push(event)
				stream.queueMu.Lock()
				bounded := len(stream.pending) <= assistantMessageEventPendingLimit && stream.pendingBytes <= assistantMessageEventByteBudget
				stream.queueMu.Unlock()
				if !bounded {
					t.Fatal("pending event budget was exceeded")
				}
			}
			final := AssistantMessage{StopReason: StopReasonStop, ResponseID: "retained-id", Content: []ContentBlock{TextContent{Text: "retained-content"}}}
			final.Usage.Input, final.UsageReported = 37, true
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventDone, Message: final})
			stream.finish(final)
			var last AssistantMessageEvent
			for event := range stream.Events() {
				if event.Type == AssistantMessageEventDone {
					t.Fatal("buffer overflow exposed successful Done")
				}
				last = event
			}
			if last.Type != AssistantMessageEventError || last.DroppedEvents == 0 || last.Error.ErrorMessage != ErrStreamBufferExceeded.Error() {
				t.Fatalf("missing explicit overflow error: %+v", last)
			}
			want := final
			want.StopReason, want.ErrorMessage = StopReasonError, ErrStreamBufferExceeded.Error()
			if got := stream.Result(); !reflect.DeepEqual(got, want) {
				t.Fatalf("overflow lost provider evidence: %+v", got)
			}
		})
	}
}

func TestStreamPayloadBudgetEvictsDeltasBeforeLifecycle(t *testing.T) {
	stream := newAssistantMessageEventStream()
	defer stream.Close()
	delta := AssistantMessageEvent{Type: AssistantMessageEventTextDelta, Partial: AssistantMessage{Content: []ContentBlock{TextContent{Text: strings.Repeat("x", 1<<20)}}}}
	for index := 0; index < 80; index++ {
		stream.push(delta)
	}
	stream.push(AssistantMessageEvent{Type: AssistantMessageEventTextEnd, Content: "final"})
	stream.push(AssistantMessageEvent{Type: AssistantMessageEventDone, Reason: StopReasonStop})
	stream.finish(AssistantMessage{StopReason: StopReasonStop})
	var last AssistantMessageEvent
	dropped, ended := false, false
	for event := range stream.Events() {
		dropped = dropped || event.DroppedEvents > 0
		ended = ended || event.Type == AssistantMessageEventTextEnd
		last = event
	}
	if !dropped || !ended || last.Type != AssistantMessageEventDone || stream.Result().StopReason != StopReasonStop {
		t.Fatal("byte pressure did not preserve lifecycle and final result")
	}
}

func TestEventBudgetStopsBeforeCloningRecursiveArguments(t *testing.T) {
	args := map[string]any{}
	args["self"] = args
	event := AssistantMessageEvent{Type: AssistantMessageEventToolCallEnd, ToolCall: ToolCall{Arguments: args}}
	if size := assistantEventBytes(event); size <= assistantMessageEventByteBudget {
		t.Fatalf("recursive payload must fail bounded traversal: %d", size)
	}
	stream := newAssistantMessageEventStream()
	defer stream.Close()
	stream.push(event)
	stream.finish(AssistantMessage{StopReason: StopReasonStop})
	for range stream.Events() {
	}
	if stream.Result().ErrorMessage != ErrStreamBufferExceeded.Error() {
		t.Fatal("recursive payload did not fail explicitly")
	}
}
