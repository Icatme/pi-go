package pigo

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type openAIResponsesBlockingCloseBody struct {
	io.Reader
	closeStarted chan struct{}
	releaseClose chan struct{}
	closeOnce    sync.Once
	closed       atomic.Bool
}

func (body *openAIResponsesBlockingCloseBody) Close() error {
	body.closeOnce.Do(func() { close(body.closeStarted) })
	<-body.releaseClose
	body.closed.Store(true)
	return nil
}

func TestOpenAIResponsesResultWaitsForTransportCleanup(t *testing.T) {
	for _, api := range []API{"openai-responses", "openai-codex-responses"} {
		for _, simple := range []bool{false, true} {
			t.Run(string(api)+"/"+map[bool]string{false: "stream", true: "simple"}[simple], func(t *testing.T) {
				requestContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				body := &openAIResponsesBlockingCloseBody{
					Reader:       strings.NewReader("data: " + `{"type":"response.completed","response":{"id":"resp_closed","status":"completed","output":[{"type":"message","id":"msg_closed","content":[{"type":"output_text","text":"ok"}]}],"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n"),
					closeStarted: make(chan struct{}),
					releaseClose: make(chan struct{}),
				}
				var releaseOnce sync.Once
				release := func() { releaseOnce.Do(func() { close(body.releaseClose) }) }
				defer release()
				client := &http.Client{Transport: observationRoundTripper(func(request *http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body, Request: request}, nil
				})}
				provider := Provider("openai")
				apiKey := "test"
				if api == "openai-codex-responses" {
					provider = "openai-codex"
					apiKey = makeOpenAICodexToken("account-test")
				}
				model := Model{API: api, Provider: provider, ID: "fixture", BaseURL: "https://fixture.invalid", MaxTokens: 256}
				var stream *AssistantMessageEventStream
				if simple {
					stream = StreamSimple(model, Context{}, SimpleStreamOptions{APIKey: apiKey, HTTPClient: client, RequestContext: requestContext, Transport: TransportSSE})
				} else {
					stream = Stream(model, Context{}, ProviderStreamOptions{APIKey: apiKey, HTTPClient: client, RequestContext: requestContext, Transport: TransportSSE})
				}
				select {
				case <-body.closeStarted:
				case <-requestContext.Done():
					t.Fatal("transport did not begin closing the response body")
				}
				// Close is blocked, so this checks ownership without relying on scheduling
				// delays: a parser-owned finish always publishes the result before Close.
				select {
				case result := <-stream.result:
					t.Fatalf("result published before transport cleanup: %+v", result)
				default:
				}
				release()
				result := stream.Result()
				if !body.closed.Load() || result.StopReason != StopReasonStop || result.ResponseID != "resp_closed" {
					t.Fatalf("unexpected cleaned-up result: closed=%v result=%+v", body.closed.Load(), result)
				}
				doneCount := 0
				for event := range stream.Events() {
					if event.Type == AssistantMessageEventDone {
						doneCount++
						if event.Message.ResponseID != result.ResponseID || event.Message.Usage != result.Usage {
							t.Fatalf("done event differs from result: %+v", event.Message)
						}
					}
				}
				if doneCount != 1 {
					t.Fatalf("done event count = %d, want 1", doneCount)
				}
			})
		}
	}
}
