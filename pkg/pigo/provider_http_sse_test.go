package pigo

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type providerSSEObservedBody struct {
	io.ReadCloser
	closed *atomic.Bool
}

func (b *providerSSEObservedBody) Close() error {
	b.closed.Store(true)
	return b.ReadCloser.Close()
}

func TestProviderHTTPCumulativeSSEBoundary(t *testing.T) {
	// Every physical line is small; one event's accumulated data exceeds 16 MiB.
	oversized := strings.Repeat("data: "+strings.Repeat("x", 8191)+"\n", defaultMaxSSEEventBytes/8192+1)
	// A successful event exceeds the HTTP error-body limit and must remain intact.
	visibleText := strings.Repeat("partial\noutput", 6000)
	for _, provider := range []struct {
		name  string
		api   API
		first string
		done  string
	}{
		{
			name:  "shared HTTP stream",
			first: "event: update\ndata: " + strings.ReplaceAll(visibleText, "\n", "\ndata: ") + "\n\n",
			done:  "event: done\ndata: [DONE]\n\n",
		},
		{
			name: "Responses SDK",
			api:  "openai-responses",
			first: buildOpenAICodexSSE(map[string]any{
				"type": "response.output_item.done",
				"item": map[string]any{
					"type": "message",
					"id":   "msg_partial",
					"content": []map[string]any{
						{"type": "output_text", "text": visibleText},
					},
				},
			}),
			done: buildOpenAICodexSSE(map[string]any{
				"type": "response.completed",
				"response": map[string]any{
					"id":     "resp_completed",
					"status": "completed",
				},
			}),
		},
		{
			name: "Completions",
			api:  "openai-completions",
			first: buildOpenAICodexSSE(map[string]any{
				"id": "chatcmpl_partial",
				"choices": []map[string]any{
					{"index": 0, "delta": map[string]any{"content": visibleText}},
				},
			}),
			done: buildOpenAICodexSSE(map[string]any{
				"id": "chatcmpl_partial",
				"choices": []map[string]any{
					{"index": 0, "delta": map[string]any{}, "finish_reason": "stop"},
				},
			}),
		},
	} {
		t.Run(provider.name, func(t *testing.T) {
			first := strings.TrimSuffix(provider.first, "data: [DONE]\n\n")
			for _, tc := range []struct {
				name     string
				payload  string
				wantText string
				wantErr  bool
			}{
				{name: "limit before output", payload: oversized, wantErr: true},
				{name: "limit after output", payload: first + oversized, wantText: visibleText, wantErr: true},
				{name: "successful stream", payload: first + provider.done, wantText: visibleText},
			} {
				t.Run(tc.name, func(t *testing.T) {
					var calls atomic.Int32
					var closed atomic.Bool
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						calls.Add(1)
						w.Header().Set("content-type", "text/event-stream")
						_, _ = io.WriteString(w, tc.payload)
					}))
					defer server.Close()
					client := server.Client()
					transport := client.Transport
					client.Transport = observationRoundTripper(func(request *http.Request) (*http.Response, error) {
						response, err := transport.RoundTrip(request)
						if err == nil {
							response.Body = &providerSSEObservedBody{ReadCloser: response.Body, closed: &closed}
						}
						return response, err
					})

					var gotText string
					if provider.api == "" {
						streamClient := HTTPStreamClient{HTTPClient: client, MaxRetries: 3, BaseRetryDelay: time.Millisecond}
						err := streamClient.postStream(context.Background(), server.URL, httpStreamRequest{
							Body: []byte(`{}`),
							OnEvent: func(event, data string) (bool, error) {
								if event == "done" {
									return true, nil
								}
								gotText += data
								return false, nil
							},
							ShouldRetry:         shouldRetryOpenAIResponsesRequest,
							CanRetryStreamError: func() bool { return gotText == "" },
						})
						if tc.wantErr && !errors.Is(err, ErrSSEEventTooLarge) || !tc.wantErr && err != nil {
							t.Fatalf("err=%v wantLimitError=%v", err, tc.wantErr)
						}
					} else {
						model := Model{API: provider.api, Provider: "openai", ID: "test-model", BaseURL: server.URL, MaxTokens: 256, ContextWindow: 4096}
						response := CompleteSimple(model, Context{Messages: []Message{UserMessage{Content: "test"}}}, SimpleStreamOptions{
							APIKey: "test-key", HTTPClient: client, MaxRetries: 3, MaxRetryDelay: 1,
						})
						if tc.wantErr {
							if response.StopReason != StopReasonError || response.ErrorMessage != ErrSSEEventTooLarge.Error() {
								t.Fatalf("stop=%s error=%q", response.StopReason, response.ErrorMessage)
							}
						} else if response.StopReason != StopReasonStop {
							t.Fatalf("stop=%s error=%q", response.StopReason, response.ErrorMessage)
						}
						for _, content := range response.Content {
							text, ok := content.(TextContent)
							if !ok {
								t.Fatalf("unexpected content type %T", content)
							}
							gotText += text.Text
						}
					}
					if gotText != tc.wantText {
						t.Fatalf("content mismatch: got %d bytes, want %d", len(gotText), len(tc.wantText))
					}
					if calls.Load() != 1 || !closed.Load() {
						t.Fatalf("upstream calls=%d body closed=%v", calls.Load(), closed.Load())
					}
				})
			}
		})
	}
}
