package pigo

import (
	"net/http"
	"testing"
	"time"
)

func TestProviderTimeoutMsCancelsHTTPRequests(t *testing.T) {
	tests := []struct {
		name   string
		model  Model
		stream func(Model, Context, ProviderStreamOptions) *AssistantMessageEventStream
	}{
		{
			name:   "openai responses",
			model:  Model{ID: "gpt-test", Provider: "openai", API: "openai-responses", BaseURL: "https://example.invalid/v1", Input: []InputType{InputText}, MaxTokens: 16},
			stream: streamOpenAIResponses,
		},
		{
			name:   "deepseek",
			model:  Model{ID: "deepseek-test", Provider: "deepseek", API: "deepseek-chat-completions", BaseURL: "https://example.invalid", Input: []InputType{InputText}, MaxTokens: 16},
			stream: streamDeepSeekChatCompletions,
		},
		{
			name:   "google",
			model:  Model{ID: "gemini-test", Provider: "google", API: "google-generative-ai", BaseURL: "https://example.invalid", Input: []InputType{InputText}, MaxTokens: 16},
			stream: streamGoogle,
		},
		{
			name:   "mistral",
			model:  Model{ID: "mistral-test", Provider: "mistral", API: "mistral-conversations", BaseURL: "https://example.invalid", Input: []InputType{InputText}, MaxTokens: 16},
			stream: streamMistral,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requestStarted := make(chan struct{}, 1)
			httpClient := &http.Client{Transport: providerTimeoutRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				requestStarted <- struct{}{}
				<-request.Context().Done()
				return nil, request.Context().Err()
			})}

			startedAt := time.Now()
			stream := test.stream(test.model, Context{Messages: []Message{UserMessage{Content: "hello"}}}, ProviderStreamOptions{
				APIKey:     "test-key",
				HTTPClient: httpClient,
				TimeoutMs:  25,
			})
			response := stream.Result()
			for range stream.Events() {
			}

			if response.StopReason != StopReasonAborted {
				t.Fatalf("expected timeout to abort request, got %+v", response)
			}
			if time.Since(startedAt) > time.Second {
				t.Fatalf("expected TimeoutMs to bound request lifetime, took %s", time.Since(startedAt))
			}
			select {
			case <-requestStarted:
			default:
				t.Fatal("expected request to reach transport before timeout")
			}
		})
	}
}

type providerTimeoutRoundTripFunc func(*http.Request) (*http.Response, error)

func (roundTrip providerTimeoutRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return roundTrip(request)
}
