package pigo

import (
	"context"
	"reflect"
	"sync"
	"testing"
)

func TestKiroCompletedTextStreamResultBeforeEvents(t *testing.T) {
	response, model, stamp := kiroTextMessageFixture()
	response.Usage.TokenCounts = kiroCompleteCounts()
	expected, err := projectKiroCompletedTextEvents(response, model, stamp, 1024, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := newKiroCompletedTextStream(context.Background(), response, model, stamp, 1024, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	result := stream.Result()
	if !reflect.DeepEqual(result, expected[len(expected)-1].Message) {
		t.Fatal("result differs from complete evidence")
	}
	var actual []AssistantMessageEvent
	for event := range stream.Events() {
		actual = append(actual, event)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatal("lifecycle events changed or dropped")
	}
	result.Content[0] = TextContent{Text: "changed"}
	if actual[len(actual)-1].Message.Content[0].(TextContent).Text != "first" {
		t.Fatal("result aliases events")
	}
}

func TestKiroCompletedTextStreamRejectsBeforeDelivery(t *testing.T) {
	response, model, stamp := kiroTextMessageFixture()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, cancelled} {
		if stream, err := newKiroCompletedTextStream(ctx, response, model, stamp, 1024, 1<<20); err == nil || stream != nil {
			t.Fatal("invalid context created stream")
		}
	}
	for _, mutate := range []func(*kiroOfflineResponse){
		func(r *kiroOfflineResponse) { r.Parts[1].Kind = "context_usage" },
		func(r *kiroOfflineResponse) { r.Usage.StopReason = "" },
		func(r *kiroOfflineResponse) {
			r.Parts = make([]kiroOfflinePart, 683)
			for i := range r.Parts {
				r.Parts[i] = kiroOfflinePart{Kind: "text", Text: "x"}
			}
		},
	} {
		response, model, stamp := kiroTextMessageFixture()
		mutate(&response)
		if stream, err := newKiroCompletedTextStream(context.Background(), response, model, stamp, 4096, assistantMessageEventByteBudget); err == nil || stream != nil {
			t.Fatal("unsupported evidence created stream")
		}
	}
}

func TestKiroCompletedTextStreamCancelAfterCompletion(t *testing.T) {
	response, model, stamp := kiroTextMessageFixture()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream, err := newKiroCompletedTextStream(ctx, response, model, stamp, 1024, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	// Completion already happened; host cancellation now stops only delivery.
	result := stream.Result()
	cancel()
	awaitStreamSignal(t, stream.dispatcherDone)
	var events []AssistantMessageEvent
	for event := range stream.Events() {
		events = append(events, event)
	}
	if result.StopReason != StopReasonStop || len(events) != 1 || events[0].Type != AssistantMessageEventError || events[0].Reason != StopReasonAborted {
		t.Fatal("host delivery cancellation contract changed")
	}
}

func TestKiroCompletedTextStreamConcurrentClose(t *testing.T) {
	response, model, stamp := kiroTextMessageFixture()
	stream, err := newKiroCompletedTextStream(context.Background(), response, model, stamp, 1024, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Go(stream.Close)
	}
	wg.Wait()
	if result := stream.Result(); result.StopReason != StopReasonStop {
		t.Fatal("Close replaced provider evidence")
	}
	for range stream.Events() {
		t.Fatal("Close left queued events")
	}
}

func TestKiroCompletedTextStreamManyPendingLifecycle(t *testing.T) {
	response, model, stamp := kiroTextMessageFixture()
	response.Parts = make([]kiroOfflinePart, 128)
	for i := range response.Parts {
		response.Parts[i] = kiroOfflinePart{Kind: "text", Text: "x"}
	}
	stream, err := newKiroCompletedTextStream(context.Background(), response, model, stamp, 4096, assistantMessageEventByteBudget)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	result := stream.Result()
	if result.StopReason != StopReasonStop {
		t.Fatal("legal pending limit overflowed", result.ErrorMessage)
	}
	count := 0
	for event := range stream.Events() {
		count++
		if event.DroppedEvents != 0 {
			t.Fatal("delta dropped")
		}
	}
	if count != 3*len(response.Parts)+2 {
		t.Fatal("lifecycle event lost")
	}
}

func TestKiroCompletedTextStreamSnapshotBudgetBelowEventLimit(t *testing.T) {
	response, model, stamp := kiroTextMessageFixture()
	response.Parts = make([]kiroOfflinePart, (assistantMessageEventPendingLimit-2)/3)
	for i := range response.Parts {
		response.Parts[i] = kiroOfflinePart{Kind: "text", Text: "x"}
	}
	// Counts alone fit; cumulative per-event snapshots do not. Both gates apply.
	stream, err := newKiroCompletedTextStream(context.Background(), response, model, stamp, 4096, assistantMessageEventByteBudget)
	if err == nil || stream != nil {
		t.Fatal("snapshot budget bypassed by event-count bound")
	}
}

func TestKiroCompletedTextStreamReservesAbortBudget(t *testing.T) {
	response, model, stamp := kiroTextMessageFixture()
	response.Parts = []kiroOfflinePart{{Kind: "text", Text: "x"}}
	events, err := projectKiroCompletedTextEvents(response, model, stamp, 1024, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	budget := 0
	for _, event := range events {
		budget += assistantEventBytes(event)
	}
	if _, err := projectKiroCompletedTextEvents(response, model, stamp, 1024, budget); err != nil {
		t.Fatal("fixture does not fit normal events", err)
	}
	stream, err := newKiroCompletedTextStream(context.Background(), response, model, stamp, 1024, budget)
	if err == nil || stream != nil {
		t.Fatal("abort terminal reserve ignored")
	}
}
