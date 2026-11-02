package pigo

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"
)

func TestKiroCompletedToolStreamResultBeforeEvents(t *testing.T) {
	_, model, stamp := kiroTextMessageFixture()
	r := kiroToolMessageFixture()
	expected, err := projectKiroCompletedToolEvents(context.Background(), r, model, stamp, []string{"lookup"}, 4096, 1, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	s, err := newKiroCompletedToolStream(context.Background(), r, model, stamp, []string{"lookup"}, 4096, 1, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	result := s.Result()
	var events []AssistantMessageEvent
	for e := range s.Events() {
		events = append(events, e)
		if e.DroppedEvents != 0 {
			t.Fatal("dropped")
		}
	}
	if !reflect.DeepEqual(events, expected) || !reflect.DeepEqual(result, expected[len(expected)-1].Message) {
		t.Fatal("queue altered lifecycle")
	}
	result.Content[1].(ToolCall).Arguments["id"] = "changed"
	if events[len(events)-1].Message.Content[1].(ToolCall).Arguments["id"] != json.Number("9007199254740993") {
		t.Fatal("result alias")
	}
}

func TestKiroCompletedToolStreamPreflightAndAbortReserve(t *testing.T) {
	_, model, stamp := kiroTextMessageFixture()
	r := kiroToolMessageFixture()
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, ctx := range []context.Context{nil, cancelled} {
		s, err := newKiroCompletedToolStream(ctx, r, model, stamp, []string{"lookup"}, 4096, 1, 1<<20)
		if err == nil || s != nil {
			t.Fatal("cancelled construction")
		}
	}
	events, err := projectKiroCompletedToolEvents(context.Background(), r, model, stamp, []string{"lookup"}, 4096, 1, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	cost := 0
	for _, e := range events {
		cost += assistantEventBytes(e)
	}
	aborted := AssistantMessageEvent{Type: AssistantMessageEventError, Reason: StopReasonAborted, Error: AssistantMessage{API: model.API, Provider: model.Provider, Model: model.ID, Timestamp: stamp, StopReason: StopReasonAborted, ErrorMessage: "kiro: buffered tool delivery cancelled"}}
	for _, budget := range []int{cost, cost + assistantEventBytes(aborted) - 1} {
		s, err := newKiroCompletedToolStream(context.Background(), r, model, stamp, []string{"lookup"}, 4096, 1, budget)
		if err == nil || s != nil {
			t.Fatal("missing abort reserve")
		}
	}
	s, err := newKiroCompletedToolStream(context.Background(), r, model, stamp, []string{"lookup"}, 4096, 1, cost+assistantEventBytes(aborted))
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	for _, reason := range []StopReason{StopReasonAborted, StopReasonError, StopReasonLength} {
		r.StopReason = reason
		if s, err := newKiroCompletedToolStream(context.Background(), r, model, stamp, []string{"lookup"}, 4096, 1, 1<<20); err == nil || s != nil {
			t.Fatal("failed evidence opened queue")
		}
	}
}

func TestKiroCompletedToolStreamSlowConsumerLimits(t *testing.T) {
	_, model, stamp := kiroTextMessageFixture()
	r := kiroToolMessageFixture()
	r.Parts = nil
	for i := 0; i < 128; i++ {
		r.Parts = append(r.Parts, kiroOfflinePart{Kind: "tool", Tool: &kiroAssembledTool{ID: fmt.Sprintf("call-%d", i), Name: "lookup", Arguments: json.RawMessage("{}")}})
	}
	s, err := newKiroCompletedToolStream(context.Background(), r, model, stamp, []string{"lookup"}, 4096, 128, assistantMessageEventByteBudget)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := s.Result(); got.StopReason != StopReasonToolUse || len(got.Content) != 128 {
		t.Fatal("max tools result")
	}
	count := 0
	for e := range s.Events() {
		count++
		if e.DroppedEvents != 0 {
			t.Fatal("lost events")
		}
	}
	if count != 386 {
		t.Fatal(count)
	}
	// 683 parts cannot fit even if almost all are empty text; no queue is built.
	r.Parts = append(make([]kiroOfflinePart, 682), r.Parts[0])
	for i := 0; i < 682; i++ {
		r.Parts[i] = kiroOfflinePart{Kind: "text"}
	}
	if s, err := newKiroCompletedToolStream(context.Background(), r, model, stamp, []string{"lookup"}, 4096, 1, assistantMessageEventByteBudget); err == nil || s != nil {
		t.Fatal("pending-count overflow")
	}
}

func TestKiroCompletedToolStreamCancelAfterCompletion(t *testing.T) {
	_, model, stamp := kiroTextMessageFixture()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s, err := newKiroCompletedToolStream(ctx, kiroToolMessageFixture(), model, stamp, []string{"lookup"}, 4096, 1, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	result := s.Result()
	cancel()
	awaitStreamSignal(t, s.dispatcherDone)
	var events []AssistantMessageEvent
	for e := range s.Events() {
		events = append(events, e)
	}
	if result.StopReason != StopReasonToolUse || len(result.Content) != 2 || len(events) != 1 || events[0].Type != AssistantMessageEventError || events[0].Reason != StopReasonAborted {
		t.Fatal("host cancellation contract")
	}
	// Completed evidence is not permission to execute after cancellation.
}

func TestKiroCompletedToolStreamConcurrentClose(t *testing.T) {
	_, model, stamp := kiroTextMessageFixture()
	s, err := newKiroCompletedToolStream(context.Background(), kiroToolMessageFixture(), model, stamp, []string{"lookup"}, 4096, 1, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Go(s.Close)
	}
	wg.Wait()
	if result := s.Result(); result.StopReason != StopReasonToolUse {
		t.Fatal("Close rewrote completed evidence")
	}
	for range s.Events() {
		t.Fatal("Close retained delivery")
	}
}
