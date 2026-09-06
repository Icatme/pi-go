package pigo

import (
	"net/http"
	"strings"
	"testing"
)

func TestCompleteEntryPointsBoundOpenAISDKErrorBody(t *testing.T) {
	for _, simple := range []bool{false, true} {
		body := &observedReadCloser{Reader: strings.NewReader(strings.Repeat("x", 1<<20))}
		calls := 0
		client := &http.Client{Transport: observationRoundTripper(func(*http.Request) (*http.Response, error) {
			calls++
			return &http.Response{StatusCode: 502, Status: "502 Bad Gateway", Header: make(http.Header), Body: body}, nil
		})}
		model := GetModel("openai", "gpt-5.4")
		if model == nil {
			t.Fatal("expected registered OpenAI model")
		}
		input := Context{Messages: []Message{UserMessage{Content: "test"}}}
		var response AssistantMessage
		if simple {
			response = CompleteSimple(*model, input, SimpleStreamOptions{APIKey: "test-key", HTTPClient: client, MaxRetries: 0})
		} else {
			response = Complete(*model, input, ProviderStreamOptions{APIKey: "test-key", HTTPClient: client, MaxRetries: 0})
		}
		if response.StopReason != StopReasonError || calls != 1 || body.read != int(DefaultMaxProviderErrorBytes)+1 || !body.closed {
			t.Fatalf("simple=%v calls=%d bytes=%d closed=%v stop=%s", simple, calls, body.read, body.closed, response.StopReason)
		}
	}
}
