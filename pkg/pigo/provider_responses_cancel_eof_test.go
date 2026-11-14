package pigo

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type responsesCancelEOFBody struct{ cancel context.CancelFunc }

func (b responsesCancelEOFBody) Read([]byte) (int, error) {
	if b.cancel != nil {
		b.cancel()
	}
	return 0, io.EOF
}
func (b responsesCancelEOFBody) Close() error { return nil }
func TestResponsesCancellationQuietEOF(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "real-truncation", true: "canceled"}[canceled], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			body := responsesCancelEOFBody{}
			if canceled {
				body.cancel = cancel
			}
			client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: body}, nil
			})}
			model := Model{ID: "fixture", Provider: "openai", API: "openai-responses", BaseURL: "https://example.invalid", MaxTokens: 64}
			stream := Stream(model, Context{Messages: []Message{UserMessage{Content: "hello"}}}, ProviderStreamOptions{APIKey: "fixture", RequestContext: ctx, HTTPClient: client})
			defer stream.Close()
			result := stream.Result()
			if canceled {
				if result.StopReason != StopReasonAborted || result.ErrorMessage != context.Canceled.Error() {
					t.Fatalf("cancellation: %+v", result)
				}
			} else if result.StopReason != StopReasonError || !strings.Contains(result.ErrorMessage, "missing terminal") {
				t.Fatalf("truncation: %+v", result)
			}
		})
	}
}
