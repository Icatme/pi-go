package pigo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestMistralCancellationClosesCustomTransportBody(t *testing.T) {
	for _, status := range []int{200, 503} {
		for _, mode := range []string{"cancel", "timeout"} {
			t.Run(fmt.Sprintf("%s/status=%d", mode, status), func(t *testing.T) {
				reader, writer := io.Pipe()
				defer reader.Close()
				defer writer.Close()
				entered := make(chan struct{})
				client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
					close(entered)
					return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader}, nil
				})}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				options := ProviderStreamOptions{APIKey: "fixture", RequestContext: ctx, HTTPClient: client}
				if mode == "timeout" {
					options.TimeoutMs = 50
				}
				model := Model{ID: "fixture", Provider: "mistral", API: "mistral-conversations", BaseURL: "https://example.invalid", MaxTokens: 1024}
				s := Stream(model, Context{Messages: []Message{UserMessage{Content: "hello"}}}, options)
				defer s.Close()
				<-entered
				if mode == "cancel" {
					cancel()
				}
				done := make(chan AssistantMessage, 1)
				go func() { done <- s.Result() }()
				select {
				case result := <-done:
					if result.StopReason != StopReasonAborted {
						t.Fatalf("stop=%s error=%s", result.StopReason, result.ErrorMessage)
					}
					wantReason := "cancelled"
					if mode == "timeout" {
						wantReason = "timeout"
					}
					if failure, ok := ProviderFailureOf(result); !ok || failure.Kind != ProviderFailureSDK || failure.Reason != wantReason {
						t.Fatalf("lost cancellation classification: %+v, %v", failure, ok)
					}
				case <-time.After(time.Second):
					reader.Close()
					<-done
					t.Fatal("Result remained blocked after cancellation")
				}
			})
		}
	}
}

func TestMistralErrorBodyPreservesReadCause(t *testing.T) {
	cause := errors.New("synthetic interrupted body")
	body := &failingObservedBody{cause: cause}
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 503, Header: make(http.Header), Body: body}, nil
	})}
	model := Model{BaseURL: "https://example.invalid"}
	err := streamMistralSSE(model, t.Context(), client, ProviderStreamOptions{}, []byte(`{}`), "fixture", &AssistantMessage{}, newAssistantMessageEventStream())
	if !errors.Is(err, cause) || calls != 1 || body.closes != 1 {
		t.Fatalf("error=%v calls=%d closes=%d", err, calls, body.closes)
	}
}

func TestMistralBrokenErrorBodyRemainsError(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 503, Header: make(http.Header), Body: &failingObservedBody{cause: io.ErrUnexpectedEOF}}, nil
	})}
	result := Complete(Model{ID: "fixture", Provider: "mistral", API: "mistral-conversations", BaseURL: "https://example.invalid"}, Context{}, ProviderStreamOptions{RequestContext: t.Context(), APIKey: "fixture", HTTPClient: client})
	if result.StopReason != StopReasonError || !strings.Contains(result.ErrorMessage, io.ErrUnexpectedEOF.Error()) {
		t.Fatalf("unexpected result: %+v", result)
	}
}
