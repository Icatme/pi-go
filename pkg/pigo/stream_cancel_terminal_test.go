package pigo

import (
	"context"
	"reflect"
	"runtime"
	"testing"
)

func TestCancelledStreamKeepsExactlyOneAbortEvent(t *testing.T) {
	for iteration := 0; iteration < 100; iteration++ {
		parent, cancel := context.WithCancel(context.Background())
		cancel()
		final := AssistantMessage{StopReason: StopReasonAborted, ResponseID: "actual-provider-result", ErrorMessage: context.Canceled.Error()}
		stream := managedAssistantStream(parent, func(ctx context.Context) *AssistantMessageEventStream {
			s := newAssistantMessageEventStream()
			go func() {
				<-ctx.Done()
				s.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: StopReasonAborted, Error: final})
				runtime.Gosched() // Race delivery cancellation with provider finalization.
				s.finish(final)
			}()
			return s
		})
		var events []AssistantMessageEvent
		for event := range stream.Events() {
			events = append(events, event)
		}
		result := stream.Result()
		stream.Close()
		if len(events) != 1 || events[0].Type != AssistantMessageEventError || events[0].Reason != StopReasonAborted {
			t.Fatalf("iteration %d: expected exactly one aborted event, got %+v", iteration, events)
		}
		if !reflect.DeepEqual(result, final) {
			t.Fatalf("abort notification replaced final provider evidence: %+v", result)
		}
	}
}

func TestCancellationDoesNotDuplicateDeliveredTerminalEvent(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := managedAssistantStream(parent, func(context.Context) *AssistantMessageEventStream { return newAssistantMessageEventStream() })
	defer stream.Close()
	stream.push(AssistantMessageEvent{Type: AssistantMessageEventError, Reason: StopReasonAborted})
	<-stream.Events()
	cancel()
	awaitStreamSignal(t, stream.dispatcherDone)
	for event := range stream.Events() {
		t.Fatalf("duplicate terminal event after cancellation: %+v", event)
	}
	stream.finish(AssistantMessage{StopReason: StopReasonAborted})
	stream.Result()
}
