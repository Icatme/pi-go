package pigo

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func waitForStreamProducer(t *testing.T, producerDone <-chan struct{}, description string) {
	t.Helper()
	timeout := 10 * time.Second
	if deadline, ok := t.Deadline(); ok {
		remaining := time.Until(deadline) / 20
		if remaining > 30*time.Second {
			remaining = 30 * time.Second
		}
		if remaining >= 5*time.Second {
			timeout = remaining
		}
	}

	select {
	case <-producerDone:
	case <-time.After(timeout):
		t.Fatalf("expected producer to finish %s before %s", description, timeout)
	}
}

func TestAssistantMessageEventStreamStressPreservesNonDroppableEvents(t *testing.T) {
	stream := newAssistantMessageEventStream()
	producerDone := make(chan struct{})

	go func() {
		defer close(producerDone)
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventStart})
		for index := 0; index < assistantMessageEventDeltaBuffer*4; index++ {
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventTextDelta, Delta: strings.Repeat("x", 4)})
		}
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventTextEnd, ContentIndex: 0, Content: "stable tail"})
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventDone, Reason: StopReasonStop, Message: AssistantMessage{StopReason: StopReasonStop}})
		stream.finish(AssistantMessage{StopReason: StopReasonStop})
	}()

	waitForStreamProducer(t, producerDone, "under stream backpressure")

	var (
		sawStart   bool
		sawTextEnd bool
		sawDone    bool
	)
	for event := range stream.Events() {
		switch event.Type {
		case AssistantMessageEventStart:
			sawStart = true
		case AssistantMessageEventTextEnd:
			sawTextEnd = true
		case AssistantMessageEventDone:
			sawDone = true
		}
	}

	if !sawStart || !sawTextEnd || !sawDone {
		t.Fatalf("expected non-droppable lifecycle events to survive, got start=%v textEnd=%v done=%v", sawStart, sawTextEnd, sawDone)
	}
}

func TestAssistantMessageEventStreamStressHandlesLargeNonDroppableBurstWithoutConsumers(t *testing.T) {
	stream := newAssistantMessageEventStream()
	producerDone := make(chan struct{})
	const nonDroppableBurst = assistantMessageEventDeltaBuffer + 128

	go func() {
		defer close(producerDone)
		for index := 0; index < nonDroppableBurst; index++ {
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventTextStart, ContentIndex: index})
		}
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventDone, Reason: StopReasonStop, Message: AssistantMessage{StopReason: StopReasonStop}})
		stream.finish(AssistantMessage{StopReason: StopReasonStop})
	}()

	waitForStreamProducer(t, producerDone, "even when non-droppable events exceed channel capacity")

	textStartCount := 0
	sawDone := false
	for event := range stream.Events() {
		if event.Type == AssistantMessageEventTextStart {
			textStartCount++
		}
		if event.Type == AssistantMessageEventDone {
			sawDone = true
		}
	}

	if textStartCount != nonDroppableBurst {
		t.Fatalf("expected all non-droppable events to survive, got %d of %d", textStartCount, nonDroppableBurst)
	}
	if !sawDone {
		t.Fatal("expected done event to survive after a large non-droppable burst")
	}
}

func TestAssistantMessageEventStreamStressDropsDroppableEvents(t *testing.T) {
	stream := newAssistantMessageEventStream()
	producerDone := make(chan struct{})
	const droppableBurst = assistantMessageEventDeltaBuffer * 3

	go func() {
		defer close(producerDone)
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventStart})
		for index := 0; index < droppableBurst; index++ {
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventThinkingDelta, Delta: "x"})
		}
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventThinkingEnd, ContentIndex: 0, Content: "done"})
		stream.push(AssistantMessageEvent{Type: AssistantMessageEventDone, Reason: StopReasonStop, Message: AssistantMessage{StopReason: StopReasonStop}})
		stream.finish(AssistantMessage{StopReason: StopReasonStop})
	}()

	waitForStreamProducer(t, producerDone, "without consuming events")

	var (
		thinkingDeltaCount int
		droppedEvents      int
	)
	for event := range stream.Events() {
		if event.Type == AssistantMessageEventThinkingDelta {
			thinkingDeltaCount++
		}
		droppedEvents += event.DroppedEvents
	}

	if thinkingDeltaCount >= droppableBurst {
		t.Fatalf("expected some droppable events to be discarded, got %d", thinkingDeltaCount)
	}
	if droppedEvents == 0 {
		t.Fatal("expected dropped event count to be reported")
	}
}

func TestAssistantMessageEventStreamFinalizeOnlyOnce(t *testing.T) {
	stream := newAssistantMessageEventStream()
	first := AssistantMessage{StopReason: StopReasonStop, Content: []ContentBlock{TextContent{Text: "first"}}}
	second := AssistantMessage{StopReason: StopReasonError, ErrorMessage: "second"}

	var wait sync.WaitGroup
	wait.Add(2)
	go func() {
		defer wait.Done()
		stream.finish(first)
	}()
	go func() {
		defer wait.Done()
		stream.finish(second)
	}()
	wait.Wait()

	result := stream.Result()
	if result.StopReason == StopReasonStop {
		text, _ := result.Content[0].(TextContent)
		if text.Text != "first" {
			t.Fatalf("expected stop result to preserve first payload, got %+v", result)
		}
	} else if result.StopReason == StopReasonError {
		if result.ErrorMessage != "second" {
			t.Fatalf("expected error result to preserve second payload, got %+v", result)
		}
	} else {
		t.Fatalf("expected exactly one submitted result to be published, got %+v", result)
	}
	if followUp := stream.Result(); followUp.StopReason != StopReasonError || followUp.ErrorMessage != "stream result unavailable" {
		t.Fatalf("expected result channel to stay closed after first finish, got %+v", followUp)
	}
}
