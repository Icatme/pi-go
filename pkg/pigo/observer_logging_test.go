package pigo

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"
)

type recordingObserver struct {
	startPayload any
	startContext context.Context
	requestCtx   context.Context
	events       []AssistantMessageEvent
	completed    bool
	finished     bool
	finishSignal chan struct{}
}

func (o *recordingObserver) OnRequestStart(ctx context.Context, _ Model, payload any) context.Context {
	o.startPayload = payload
	o.startContext = ctx
	return context.WithValue(ctx, observerContextKey("trace"), "observer-trace")
}

func (o *recordingObserver) OnRequestComplete(ctx context.Context, _ Model, _ AssistantMessage, _ time.Duration) {
	o.requestCtx = ctx
	o.completed = true
}

func (o *recordingObserver) OnRequestError(context.Context, Model, error, time.Duration) {}

func (o *recordingObserver) OnStreamEvent(ctx context.Context, _ Model, event AssistantMessageEvent) {
	o.requestCtx = ctx
	o.events = append(o.events, event)
}

func (o *recordingObserver) OnStreamFinish(ctx context.Context, _ Model, _ AssistantMessage, _ int, _ int, _ time.Duration) {
	o.requestCtx = ctx
	o.finished = true
	if o.finishSignal != nil {
		close(o.finishSignal)
	}
}

type observerContextKey string

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

func TestStreamObserverReceivesRequestAndEventCallbacks(t *testing.T) {
	observer := &recordingObserver{}
	model := Model{ID: "gpt-5", Provider: "openai-codex", API: "openai-codex-responses"}
	stream := newAssistantMessageEventStream()
	stream.setObserver(observer, model)

	baseContext := context.WithValue(context.Background(), observerContextKey("request"), "request-context")
	payload := map[string]any{"model": "gpt-5"}
	requestContext := stream.startRequest(baseContext, payload)
	if requestContext.Value(observerContextKey("trace")) != "observer-trace" {
		t.Fatal("expected observer-enriched request context")
	}

	stream.push(AssistantMessageEvent{Type: AssistantMessageEventStart})
	stream.finish(AssistantMessage{StopReason: StopReasonStop})
	_ = stream.Result()
	for range stream.Events() {
	}

	observedPayload, ok := observer.startPayload.(map[string]any)
	if !ok || observedPayload["model"] != "gpt-5" {
		t.Fatalf("expected final request payload, got %#v", observer.startPayload)
	}
	if observer.startContext.Value(observerContextKey("request")) != "request-context" {
		t.Fatal("expected request context to reach OnRequestStart")
	}
	if len(observer.events) != 1 || observer.events[0].Type != AssistantMessageEventStart {
		t.Fatalf("expected one stream event callback, got %+v", observer.events)
	}
	if !observer.completed || !observer.finished {
		t.Fatalf("expected completion callbacks, completed=%v finished=%v", observer.completed, observer.finished)
	}
	if observer.requestCtx.Value(observerContextKey("trace")) != "observer-trace" {
		t.Fatal("expected observer-enriched context on subsequent callbacks")
	}
}

func TestAnthropicProviderUsesObserverContextAndEmitsEvents(t *testing.T) {
	requestContextValue := make(chan any, 1)
	httpClient := &http.Client{Transport: observerRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestContextValue <- request.Context().Value(observerContextKey("trace"))
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
			Body: io.NopCloser(strings.NewReader(buildAnthropicSSE(
				map[string]any{"type": "message_start", "message": map[string]any{"id": "msg_observer", "usage": map[string]any{}}},
				map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "end_turn"}},
				map[string]any{"type": "message_stop"},
			))),
			Request: request,
		}, nil
	})}
	observer := &recordingObserver{finishSignal: make(chan struct{})}
	model := GetModel("anthropic", "claude-sonnet-4-5")
	if model == nil {
		t.Fatal("expected anthropic model")
	}

	response := CompleteSimple(*model, Context{Messages: []Message{UserMessage{Content: "hello"}}}, SimpleStreamOptions{
		APIKey:     "anthropic-test-key",
		HTTPClient: httpClient,
		Observer:   observer,
	})
	if response.StopReason != StopReasonStop {
		t.Fatalf("expected successful response, got %+v", response)
	}

	select {
	case <-observer.finishSignal:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for observer completion")
	}
	if value := <-requestContextValue; value != "observer-trace" {
		t.Fatalf("expected observer-enriched context on HTTP request, got %#v", value)
	}
	if _, ok := observer.startPayload.(anthropicRequest); !ok {
		t.Fatalf("expected final anthropic payload at request start, got %T", observer.startPayload)
	}
	if len(observer.events) == 0 {
		t.Fatal("expected stream events to reach observer")
	}
}

type observerRoundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip observerRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
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
