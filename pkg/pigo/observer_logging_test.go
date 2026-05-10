package pigo

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"time"
)

func TestLoggingObserverOnRequestStart(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	obs := NewLoggingObserver(logger, slog.LevelInfo)

	model := Model{ID: "gpt-5", Provider: "openai-codex", API: "openai-codex-responses"}
	obs.OnRequestStart(context.Background(), model, nil)

	output := buf.String()
	if output == "" {
		t.Fatal("expected log output for request start")
	}
}

func TestLoggingObserverOnRequestComplete(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	obs := NewLoggingObserver(logger, slog.LevelInfo)

	model := Model{ID: "gpt-5", Provider: "openai-codex", API: "openai-codex-responses"}
	result := AssistantMessage{
		Model:      "gpt-5",
		Provider:   "openai-codex",
		StopReason: StopReasonStop,
		ResponseID: "resp-123",
		Usage: Usage{
			Input:  100,
			Output: 50,
			Cost:   UsageCost{Total: 0.001},
		},
	}
	obs.OnRequestComplete(context.Background(), model, result, 100*time.Millisecond)

	output := buf.String()
	if output == "" {
		t.Fatal("expected log output for request complete")
	}
}

func TestLoggingObserverOnRequestError(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	obs := NewLoggingObserver(logger, slog.LevelInfo)

	model := Model{ID: "kimi-k2", Provider: "kimi-coding", API: "anthropic-messages"}
	obs.OnRequestError(context.Background(), model, context.DeadlineExceeded, 5*time.Second)

	output := buf.String()
	if output == "" {
		t.Fatal("expected log output for request error")
	}
}

func TestLoggingObserverDefaultLogger(t *testing.T) {
	obs := NewLoggingObserver(nil, slog.LevelInfo)
	if obs.logger == nil {
		t.Fatal("expected default logger to be assigned when nil is passed")
	}
}

func TestNilObserverNoPanic(t *testing.T) {
	var obs Observer = nil

	model := Model{ID: "test", Provider: "test"}
	result := AssistantMessage{StopReason: StopReasonStop}

	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("nil observer caused panic: %v", r)
			}
		}()
		if obs != nil {
			obs.OnRequestStart(context.Background(), model, nil)
			obs.OnRequestComplete(context.Background(), model, result, time.Second)
			obs.OnRequestError(context.Background(), model, context.Canceled, time.Second)
			obs.OnStreamEvent(context.Background(), model, AssistantMessageEvent{})
			obs.OnStreamFinish(context.Background(), model, result, 10, 2, time.Second)
		}
	}()
}

func TestStreamObserverIntegration(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	obs := NewLoggingObserver(logger, slog.LevelDebug)

	model := Model{ID: "gpt-5", Provider: "openai-codex", API: "openai-codex-responses"}

	stream := newAssistantMessageEventStream()
	stream.setObserver(obs, model)

	stream.push(AssistantMessageEvent{Type: AssistantMessageEventStart})
	stream.push(AssistantMessageEvent{Type: AssistantMessageEventTextDelta, Delta: "hello"})

	result := AssistantMessage{
		Model:      "gpt-5",
		Provider:   "openai-codex",
		StopReason: StopReasonStop,
		Usage: Usage{
			Input:       100,
			Output:      2,
			TotalTokens: 102,
		},
	}
	stream.finish(result)

	finalResult := stream.Result()
	if finalResult.StopReason != StopReasonStop {
		t.Fatalf("expected StopReasonStop, got %q", finalResult.StopReason)
	}

	events := stream.Events()
	eventCount := 0
	for range events {
		eventCount++
	}
	if eventCount != 2 {
		t.Fatalf("expected 2 stream events, got %d", eventCount)
	}
}

func TestStreamObserverErrorFinish(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	obs := NewLoggingObserver(logger, slog.LevelDebug)

	model := Model{ID: "gpt-5", Provider: "openai-codex"}
	stream := newAssistantMessageEventStream()
	stream.setObserver(obs, model)

	result := AssistantMessage{
		StopReason:   StopReasonError,
		ErrorMessage: "missing api key",
	}
	stream.finish(result)

	output := buf.String()
	if output == "" {
		t.Fatal("expected log output for error finish")
	}
}
