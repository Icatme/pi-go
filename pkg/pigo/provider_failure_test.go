package pigo

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// P1: failures retain their origin and original message through Complete,
// streamed terminal events, cloning and transcript persistence. Existing stream
// tests assert delivery/results but not this diagnostic contract.
func TestProviderFailureDiagnostics(t *testing.T) {
	prefix := "data: {\"id\":\"chatcmpl-test\",\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"
	for _, tc := range []struct {
		name, tail, kind, reason string
		readErr                  error
	}{
		{"upstream", "data: {\"error\":{\"message\":\"private upstream failure\"}}\n\n", ProviderFailureUpstream, "error_event", nil},
		{"invalid JSON", "data: {broken}\n\n", ProviderFailureParse, "invalid_json", nil},
		{"field type", "data: {\"choices\":[{\"delta\":{\"content\":[]}}]}\n\n", ProviderFailureParse, "field_type", nil},
		{"finish reason", "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"content_filter\"}]}\n\n", ProviderFailureParse, "unsupported_finish_reason", nil},
		{"missing terminal", "", ProviderFailureStream, "missing_terminal", nil},
		{"done without finish", "data: [DONE]\n\n", ProviderFailureStream, "missing_terminal", nil},
		{"read failure", "", ProviderFailureStream, "body_read", io.ErrUnexpectedEOF},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			client := &http.Client{Transport: observationRoundTripper(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				var body io.Reader = strings.NewReader(prefix + tc.tail)
				if tc.readErr != nil {
					body = io.MultiReader(body, failureReader{tc.readErr})
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(body)}, nil
			})}
			model := Model{API: "openai-completions", Provider: "commandcode", ID: "z-ai/glm-5.3-flash", BaseURL: "https://provider.invalid/v1"}
			for _, streamed := range []bool{false, true} {
				opts := ProviderStreamOptions{APIKey: "test", HTTPClient: client, RequestContext: t.Context(), MaxRetries: 0}
				var message AssistantMessage
				if !streamed {
					message = Complete(model, Context{}, opts)
				} else {
					stream := Stream(model, Context{}, opts)
					var terminal AssistantMessage
					for event := range stream.Events() {
						if event.Type == AssistantMessageEventError {
							terminal = event.Error
						}
					}
					message = stream.Result()
					if failure, ok := ProviderFailureOf(terminal); !ok || failure.Kind != tc.kind {
						t.Fatalf("terminal diagnostic=%+v", failure)
					}
				}
				failure, ok := ProviderFailureOf(message)
				if !ok || failure.Kind != tc.kind || failure.Reason != tc.reason || failure.Message == "" || failure.Message != message.ErrorMessage || message.StopReason != StopReasonError || message.ResponseID != "chatcmpl-test" {
					t.Fatalf("failure=%+v message=%+v", failure, message)
				}
				cloned := cloneAssistantMessage(message)
				data, err := json.Marshal(Context{Messages: []Message{cloned}})
				if err != nil {
					t.Fatal(err)
				}
				var restored Context
				if err = json.Unmarshal(data, &restored); err != nil {
					t.Fatal(err)
				}
				if got, ok := ProviderFailureOf(restored.Messages[0].(AssistantMessage)); !ok || got != failure {
					t.Fatalf("restored failure=%+v", got)
				}
			}
			if calls.Load() != 2 {
				t.Fatalf("diagnostics changed request count: %d", calls.Load())
			}
		})
	}
	for _, protocol := range []struct{ api, prefix, upstream string }{
		{"openai-responses", "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-test\"}}\n\n", "data: {\"type\":\"error\",\"message\":\"private upstream failure\"}\n\n"},
		{"anthropic-messages", "data: {\"type\":\"message_start\",\"message\":{\"id\":\"resp-test\"}}\n\n", "data: {\"type\":\"error\",\"error\":{\"message\":\"private upstream failure\"}}\n\n"},
		{"openai-completions", "data: {\"id\":\"resp-test\",\"choices\":[]}\n\n", "data: {\"error\":{\"message\":\"private upstream failure\"}}\n\n"},
	} {
		for _, tc := range []struct {
			name, tail, kind, reason string
			status                   int
			readErr                  error
		}{
			{"upstream event", protocol.upstream, ProviderFailureUpstream, "error_event", 200, nil},
			{"malformed event", "data: {broken}\n\n", ProviderFailureParse, "invalid_json", 200, nil},
			{"EOF", "", ProviderFailureStream, "missing_terminal", 200, nil},
			{"read error", "", ProviderFailureStream, "body_read", 200, io.ErrUnexpectedEOF},
			{"HTTP error", "{\"error\":{\"message\":\"private upstream failure\"}}", ProviderFailureUpstream, "http_error", 503, nil},
			{"interrupted HTTP error", "{\"error\":", ProviderFailureStream, "body_read", 503, io.ErrUnexpectedEOF},
		} {
			t.Run(protocol.api+"/"+tc.name, func(t *testing.T) {
				var calls atomic.Int32
				client := &http.Client{Transport: observationRoundTripper(func(*http.Request) (*http.Response, error) {
					calls.Add(1)
					bodyText := protocol.prefix + tc.tail
					contentType := "text/event-stream"
					if tc.status != 200 {
						bodyText, contentType = tc.tail, "application/json"
					}
					var body io.Reader = strings.NewReader(bodyText)
					if tc.readErr != nil {
						body = io.MultiReader(body, failureReader{tc.readErr})
					}
					return &http.Response{StatusCode: tc.status, Status: http.StatusText(tc.status), Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(body)}, nil
				})}
				model := Model{API: API(protocol.api), Provider: "commandcode", ID: "synthetic", BaseURL: "https://provider.invalid/v1"}
				message := Complete(model, Context{}, ProviderStreamOptions{APIKey: "test", HTTPClient: client, RequestContext: t.Context(), MaxRetries: 0})
				failure, ok := ProviderFailureOf(message)
				if !ok || failure.Kind != tc.kind || failure.Reason != tc.reason || failure.Message != message.ErrorMessage || message.StopReason != StopReasonError || calls.Load() != 1 {
					t.Fatalf("failure=%+v stop=%s calls=%d", failure, message.StopReason, calls.Load())
				}
				if tc.status == 200 && message.ResponseID != "resp-test" {
					t.Fatalf("response ID lost: %q", message.ResponseID)
				}
				if tc.kind == ProviderFailureUpstream && !strings.Contains(failure.Message, "private upstream failure") {
					t.Fatal("upstream error text lost")
				}
			})
		}
	}

	t.Run("local request encoding is not response parsing", func(t *testing.T) {
		var calls atomic.Int32
		client := &http.Client{Transport: observationRoundTripper(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return nil, errors.New("unexpected dispatch")
		})}
		model := Model{API: "openai-completions", Provider: "commandcode", ID: "synthetic", BaseURL: "https://provider.invalid/v1"}
		message := Complete(model, Context{}, ProviderStreamOptions{
			APIKey: "test", HTTPClient: client, RequestContext: t.Context(),
			OnPayload: func(any, Model) any { return json.RawMessage(`{`) },
		})
		failure, ok := ProviderFailureOf(message)
		if !ok || failure.Kind != ProviderFailureSDK || failure.Reason != "unclassified" || failure.Message != message.ErrorMessage || message.StopReason != StopReasonError || calls.Load() != 0 {
			t.Fatalf("local request encoding misclassified: %+v calls=%d", failure, calls.Load())
		}
	})

	// Wrapping adds evidence without changing text or errors.Is semantics used
	// by cancellation, timeout, retry and SSE-limit handling.
	err := readSSEStream(failureReader{io.ErrUnexpectedEOF}, func(string, string) (bool, error) { return false, nil })
	if !errors.Is(err, io.ErrUnexpectedEOF) || err.Error() != io.ErrUnexpectedEOF.Error() {
		t.Fatalf("read error changed: %v", err)
	}
}

type failureReader struct{ err error }

func (r failureReader) Read([]byte) (int, error) { return 0, r.err }
