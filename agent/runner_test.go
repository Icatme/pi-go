package agent

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestNewRunnerValidatesAndIsolatesDefinition(t *testing.T) {
	parameters := map[string]any{
		"type":       "object",
		"properties": map[string]any{"value": map[string]any{"type": "string"}},
	}
	runner, err := NewRunner(AgentDefinition{
		Name:  "runner-agent",
		Model: runnerStaticModel("ok"),
		Tools: []ToolDefinition{{Name: "echo", Parameters: parameters}},
	})
	if err != nil {
		t.Fatalf("NewRunner returned error: %v", err)
	}
	parameters["properties"].(map[string]any)["value"].(map[string]any)["type"] = "number"
	if got := runner.definition.Tools[0].Parameters["properties"].(map[string]any)["value"].(map[string]any)["type"]; got != "string" {
		t.Fatalf("definition mutation leaked into runner: %v", got)
	}

	if _, err := NewRunner(AgentDefinition{Tools: []ToolDefinition{{
		Name:       "invalid",
		Parameters: map[string]any{"type": 42},
	}}}); err == nil {
		t.Fatal("expected invalid tool schema to fail runner construction")
	}
}

func TestRunnerRunEmitsClonedSequencedLifecycle(t *testing.T) {
	runner, err := NewRunner(AgentDefinition{Name: "runner-agent", Model: runnerStaticModel("done")})
	if err != nil {
		t.Fatalf("NewRunner returned error: %v", err)
	}
	prompt := NewUserTextMessage("hello")
	prompt.Metadata = map[string]any{"nested": map[string]any{"value": "original"}}
	stream := runner.Run(context.Background(), AgentSnapshot{}, []Message{prompt})

	var (
		events     []AgentEvent
		runID      string
		startCount int
		endCount   int
	)
	for event := range stream.Events() {
		events = append(events, event)
		if event.RunID == "" {
			t.Fatal("event has empty run ID")
		}
		if runID == "" {
			runID = event.RunID
		} else if event.RunID != runID {
			t.Fatalf("run ID changed from %q to %q", runID, event.RunID)
		}
		if event.ParentRunID != "" {
			t.Fatalf("unexpected parent run ID %q", event.ParentRunID)
		}
		if event.AgentName != "runner-agent" {
			t.Fatalf("unexpected agent name %q", event.AgentName)
		}
		if event.Sequence != uint64(len(events)) {
			t.Fatalf("event %d has sequence %d", len(events), event.Sequence)
		}
		switch event.Type {
		case EventAgentStart:
			startCount++
		case EventAgentEnd:
			endCount++
		case EventMessageEnd:
			if event.Message != nil && event.Message.Role == RoleUser {
				event.Message.Metadata["nested"].(map[string]any)["value"] = "event mutation"
			}
		}
	}
	snapshot, err := stream.Wait()
	if err != nil {
		t.Fatalf("Wait returned error: %v", err)
	}
	if startCount != 1 || endCount != 1 {
		t.Fatalf("expected one run boundary, starts=%d ends=%d events=%v", startCount, endCount, runnerEventTypes(events))
	}
	if len(events) == 0 || events[0].Type != EventAgentStart || events[len(events)-1].Type != EventAgentEnd {
		t.Fatalf("unexpected event boundaries: %v", runnerEventTypes(events))
	}
	if len(snapshot.Messages) != 2 || snapshot.Messages[1].Parts[0].Text != "done" {
		t.Fatalf("unexpected final snapshot: %+v", snapshot)
	}
	if got := snapshot.Messages[0].Metadata["nested"].(map[string]any)["value"]; got != "original" {
		t.Fatalf("event mutation leaked into snapshot: %v", got)
	}
	if got := prompt.Metadata["nested"].(map[string]any)["value"]; got != "original" {
		t.Fatalf("run mutated caller prompt: %v", got)
	}

	snapshot.Messages[0].Metadata["nested"].(map[string]any)["value"] = "wait mutation"
	again, err := stream.Wait()
	if err != nil {
		t.Fatalf("second Wait returned error: %v", err)
	}
	if got := again.Messages[0].Metadata["nested"].(map[string]any)["value"]; got != "original" {
		t.Fatalf("Wait result mutation leaked into stored result: %v", got)
	}
	if err := stream.Close(); err != nil {
		t.Fatalf("Close after completion returned error: %v", err)
	}
}

func TestRunnerQueryAndContinueUseIndependentSnapshots(t *testing.T) {
	requests := make(chan ModelRequest, 2)
	runner, err := NewRunner(AgentDefinition{
		Model: StreamFunc(func(_ context.Context, request ModelRequest) (AssistantStream, error) {
			requests <- request
			return newStaticAssistantStream(Message{
				Role:       RoleAssistant,
				Parts:      []Part{{Type: PartTypeText, Text: "ok"}},
				StopReason: StopReasonStop,
				Timestamp:  time.Now().UTC(),
			}, nil), nil
		}),
	})
	if err != nil {
		t.Fatalf("NewRunner returned error: %v", err)
	}

	queryStream := runner.Query(context.Background(), "question")
	drainRunnerEvents(queryStream.Events())
	querySnapshot, err := queryStream.Wait()
	if err != nil {
		t.Fatalf("query Wait returned error: %v", err)
	}
	queryRequest := <-requests
	if len(queryRequest.Messages) != 1 || queryRequest.Messages[0].Parts[0].Text != "question" {
		t.Fatalf("unexpected query request: %+v", queryRequest.Messages)
	}
	if len(querySnapshot.Messages) != 2 {
		t.Fatalf("unexpected query snapshot: %+v", querySnapshot.Messages)
	}

	input := AgentSnapshot{Messages: []Message{NewUserTextMessage("resume")}}
	continueStream := runner.Continue(context.Background(), input)
	drainRunnerEvents(continueStream.Events())
	continued, err := continueStream.Wait()
	if err != nil {
		t.Fatalf("continue Wait returned error: %v", err)
	}
	continueRequest := <-requests
	if len(continueRequest.Messages) != 1 || continueRequest.Messages[0].Parts[0].Text != "resume" {
		t.Fatalf("unexpected continue request: %+v", continueRequest.Messages)
	}
	if len(continued.Messages) != 2 || len(input.Messages) != 1 {
		t.Fatalf("continue mutated input or returned wrong snapshot: input=%+v final=%+v", input.Messages, continued.Messages)
	}
}

func TestRunnerSupportsConcurrentIndependentRuns(t *testing.T) {
	entered := make(chan string, 2)
	release := make(chan struct{})
	runner, err := NewRunner(AgentDefinition{
		Name: "concurrent-runner",
		Model: StreamFunc(func(_ context.Context, request ModelRequest) (AssistantStream, error) {
			text := request.Messages[len(request.Messages)-1].Parts[0].Text
			entered <- text
			<-release
			return newStaticAssistantStream(Message{
				Role:       RoleAssistant,
				Parts:      []Part{{Type: PartTypeText, Text: text}},
				StopReason: StopReasonStop,
				Timestamp:  time.Now().UTC(),
			}, nil), nil
		}),
	})
	if err != nil {
		t.Fatalf("NewRunner returned error: %v", err)
	}

	first := runner.Query(context.Background(), "first")
	second := runner.Query(context.Background(), "second")
	seen := map[string]bool{<-entered: true, <-entered: true}
	if !seen["first"] || !seen["second"] {
		t.Fatalf("concurrent model requests did not stay independent: %v", seen)
	}
	close(release)

	type result struct {
		events   []AgentEvent
		snapshot AgentSnapshot
		err      error
	}
	collect := func(stream *RunStream) <-chan result {
		done := make(chan result, 1)
		go func() {
			var events []AgentEvent
			for event := range stream.Events() {
				events = append(events, event)
			}
			snapshot, waitErr := stream.Wait()
			done <- result{events: events, snapshot: snapshot, err: waitErr}
		}()
		return done
	}
	firstResult := <-collect(first)
	secondResult := <-collect(second)
	if firstResult.err != nil || secondResult.err != nil {
		t.Fatalf("concurrent runs failed: first=%v second=%v", firstResult.err, secondResult.err)
	}
	if got := firstResult.snapshot.Messages[1].Parts[0].Text; got != "first" {
		t.Fatalf("first result crossed run boundary: %q", got)
	}
	if got := secondResult.snapshot.Messages[1].Parts[0].Text; got != "second" {
		t.Fatalf("second result crossed run boundary: %q", got)
	}
	firstRunID := firstResult.events[0].RunID
	secondRunID := secondResult.events[0].RunID
	if firstRunID == "" || secondRunID == "" || firstRunID == secondRunID {
		t.Fatalf("expected distinct non-empty run ids, first=%q second=%q", firstRunID, secondRunID)
	}
}

func TestRunnerSynthesizesSingleEndOnEngineError(t *testing.T) {
	wantErr := errors.New("runner model failed")
	runner, err := NewRunner(AgentDefinition{Model: StreamFunc(func(context.Context, ModelRequest) (AssistantStream, error) {
		return nil, wantErr
	})})
	if err != nil {
		t.Fatalf("NewRunner returned error: %v", err)
	}
	stream := runner.Query(context.Background(), "run")

	var events []AgentEvent
	for event := range stream.Events() {
		events = append(events, event)
	}
	snapshot, err := stream.Wait()
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected model error, got %v", err)
	}
	if len(snapshot.Messages) != 1 || snapshot.Messages[0].Role != RoleUser {
		t.Fatalf("unexpected error snapshot: %+v", snapshot)
	}
	var starts, ends int
	for _, event := range events {
		switch event.Type {
		case EventAgentStart:
			starts++
		case EventAgentEnd:
			ends++
			if !errors.Is(event.Err, wantErr) {
				t.Fatalf("agent end lost final error: %v", event.Err)
			}
		}
	}
	if starts != 1 || ends != 1 || events[len(events)-1].Type != EventAgentEnd {
		t.Fatalf("unbalanced error lifecycle starts=%d ends=%d events=%v", starts, ends, runnerEventTypes(events))
	}
}

func TestRunnerBoundedBackpressureAndConcurrentCloseWait(t *testing.T) {
	updates := make([]AssistantEvent, runnerEventBufferSize*4)
	for i := range updates {
		updates[i] = AssistantEvent{
			Type:    AssistantEventTextDelta,
			Message: Message{Role: RoleAssistant, Parts: []Part{{Type: PartTypeText, Text: fmt.Sprintf("%d", i)}}, Timestamp: time.Now().UTC()},
			Delta:   "x",
		}
	}
	runner, err := NewRunner(AgentDefinition{Model: staticModel{streamFn: func(context.Context, ModelRequest) (AssistantStream, error) {
		return newStaticAssistantStream(Message{Role: RoleAssistant, StopReason: StopReasonStop, Timestamp: time.Now().UTC()}, updates), nil
	}}})
	if err != nil {
		t.Fatalf("NewRunner returned error: %v", err)
	}
	stream := runner.Query(context.Background(), "run")
	if got := cap(stream.Events()); got != runnerEventBufferSize {
		t.Fatalf("expected event capacity %d, got %d", runnerEventBufferSize, got)
	}

	deadline := time.Now().Add(2 * time.Second)
	for len(stream.events) != cap(stream.events) && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(stream.events) != cap(stream.events) {
		t.Fatalf("event buffer never reached bounded backpressure: len=%d cap=%d", len(stream.events), cap(stream.events))
	}

	type waitResult struct {
		snapshot AgentSnapshot
		err      error
	}
	waits := make(chan waitResult, 4)
	for range 4 {
		go func() {
			snapshot, err := stream.Wait()
			waits <- waitResult{snapshot: snapshot, err: err}
		}()
	}
	select {
	case result := <-waits:
		t.Fatalf("Wait bypassed full event backpressure: %+v", result)
	case <-time.After(20 * time.Millisecond):
	}

	var closeWG sync.WaitGroup
	closeErrs := make(chan error, 4)
	for range 4 {
		closeWG.Add(1)
		go func() {
			defer closeWG.Done()
			closeErrs <- stream.Close()
		}()
	}
	closeWG.Wait()
	close(closeErrs)
	for err := range closeErrs {
		if err != nil {
			t.Fatalf("Close returned error: %v", err)
		}
	}
	for range 4 {
		select {
		case result := <-waits:
			if result.err != nil {
				t.Fatalf("Wait returned error after Close: %v", result.err)
			}
			if len(result.snapshot.Messages) != 2 {
				t.Fatalf("unexpected final snapshot: %+v", result.snapshot)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent Wait did not complete after Close drained events")
		}
	}
}

func runnerStaticModel(text string) StreamModel {
	return StreamFunc(func(context.Context, ModelRequest) (AssistantStream, error) {
		return newStaticAssistantStream(Message{
			Role:       RoleAssistant,
			Parts:      []Part{{Type: PartTypeText, Text: text}},
			StopReason: StopReasonStop,
			Timestamp:  time.Now().UTC(),
		}, nil), nil
	})
}

func drainRunnerEvents(events <-chan AgentEvent) {
	for range events {
	}
}

func runnerEventTypes(events []AgentEvent) []EventType {
	types := make([]EventType, len(events))
	for i, event := range events {
		types[i] = event.Type
	}
	return types
}
