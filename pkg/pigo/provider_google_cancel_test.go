package pigo

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestGoogleCancellationClosesCustomTransportBody(t *testing.T) {
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
				model := Model{ID: "fixture", Provider: "google", API: "google-generative-ai", BaseURL: "https://example.invalid", MaxTokens: 1024}
				input := Context{Messages: []Message{UserMessage{Content: "hello"}}}
				s := Stream(model, input, options)
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
						t.Fatalf("stop = %s, error=%s", result.StopReason, result.ErrorMessage)
					}
					wantReason := "cancelled"
					if mode == "timeout" {
						wantReason = "timeout"
					}
					if failure, ok := ProviderFailureOf(result); !ok || failure.Kind != ProviderFailureSDK || failure.Reason != wantReason {
						t.Fatalf("lost cancellation classification: %+v, %v", failure, ok)
					}
					got, ok := ProviderHTTPStatusOf(result)
					if status == 503 && (!ok || got != status) {
						t.Fatalf("cancelled non-2xx lost HTTP status: %d, %v", got, ok)
					}
					if status == 200 && ok {
						t.Fatalf("successful HTTP response gained failure status: %d", got)
					}
				case <-time.After(time.Second):
					reader.Close() // Release the intentionally cancellation-unaware fixture.
					<-done
					t.Fatal("Result remained blocked after cancellation")
				}
			})
		}
	}
}
