package pigo

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"
)

func TestAnthropicCancellationClosesCustomTransportBody(t *testing.T) {
	for _, hosted := range []bool{false, true} {
		for _, status := range []int{200, 503} {
			for _, mode := range []string{"cancel", "timeout"} {
				t.Run(fmt.Sprintf("%s/hosted=%t/status=%d", mode, hosted, status), func(t *testing.T) {
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
					model := Model{ID: "fixture", Provider: "anthropic", API: "anthropic-messages", BaseURL: "https://example.invalid", MaxTokens: 1024}
					input := Context{Messages: []Message{UserMessage{Content: "hello"}}}
					if hosted {
						model = *GetModel("kimi-coding", "k2p5")
						model.BaseURL = "https://example.invalid"
						input.HostedTools = []HostedTool{{Type: HostedToolTypeWebSearch, Name: "web_search"}}
					}
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
						if !hosted {
							gotStatus, recorded := ProviderHTTPStatusOf(result)
							if (status == 503 && (!recorded || gotStatus != status)) || (status == 200 && recorded) {
								t.Fatalf("cancelled HTTP %d: status=%d recorded=%v", status, gotStatus, recorded)
							}
						}
						wantReason := "cancelled"
						if mode == "timeout" {
							wantReason = "timeout"
						}
						if failure, ok := ProviderFailureOf(result); !ok || failure.Reason != wantReason {
							t.Fatalf("lost cancellation cause: %+v", failure)
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
}

func TestProviderResponseBodyCloseStopsCancellationAndClosesOnce(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	body := &failingObservedBody{cause: io.EOF}
	wrapped := bindProviderResponseBody(ctx, body)
	if err := wrapped.Close(); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := wrapped.Close(); err != nil {
		t.Fatal(err)
	}
	if body.closes != 1 {
		t.Fatalf("closed %d times", body.closes)
	}
}
