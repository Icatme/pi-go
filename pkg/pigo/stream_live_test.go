package pigo

import (
	"context"
	"os"
	"strings"
	"testing"
)

func abortLiveStreamAfterFirstDelta(t *testing.T, model Model, options SimpleStreamOptions) AssistantMessage {
	t.Helper()

	requestContext, cancel := context.WithCancel(context.Background())
	defer cancel()

	stream := StreamSimple(model, Context{
		SystemPrompt: "You are a helpful assistant.",
		Messages: []Message{
			UserMessage{Content: "What is 15 + 27? Think step by step, then list 50 first names."},
		},
	}, SimpleStreamOptions{
		APIKey:         options.APIKey,
		Auth:           options.Auth,
		HTTPClient:     options.HTTPClient,
		Headers:        options.Headers,
		MaxTokens:      options.MaxTokens,
		Temperature:    options.Temperature,
		Transport:      options.Transport,
		CacheRetention: options.CacheRetention,
		SessionID:      options.SessionID,
		OnPayload:      options.OnPayload,
		MaxRetryDelay:  options.MaxRetryDelay,
		Metadata:       options.Metadata,
		RequestContext: requestContext,
		Reasoning:      options.Reasoning,
	})

	aborted := false
	for event := range stream.Events() {
		if aborted {
			continue
		}
		if (event.Type == AssistantMessageEventTextDelta || event.Type == AssistantMessageEventThinkingDelta) && strings.TrimSpace(event.Delta) != "" {
			aborted = true
			cancel()
		}
	}

	result := stream.Result()
	if !aborted {
		t.Fatalf("expected live stream to emit at least one delta before completion, got %+v", result)
	}
	if result.StopReason != StopReasonAborted {
		t.Fatalf("expected aborted live stream result, got %+v", result)
	}
	return result
}

func TestStreamSimpleKimiCodingLive(t *testing.T) {
	if os.Getenv("PIGO_LIVE_TEST") != "1" {
		t.Skip("set PIGO_LIVE_TEST=1 to run live kimi-coding stream test")
	}

	apiKey := GetEnvAPIKey("kimi-coding")
	if apiKey == "" {
		t.Skip("missing KIMI_API_KEY for live stream test")
	}

	model := GetModel("kimi-coding", "kimi-k2-thinking")
	if model == nil {
		t.Fatal("expected kimi model")
	}

	stream := StreamSimple(*model, Context{
		SystemPrompt: "Reply with exactly OK.",
		Messages: []Message{
			UserMessage{Content: "Say OK"},
		},
	}, SimpleStreamOptions{
		APIKey: apiKey,
	})

	sawTextDelta := false
	for event := range stream.Events() {
		if event.Type == AssistantMessageEventTextDelta && strings.TrimSpace(event.Delta) != "" {
			sawTextDelta = true
		}
	}

	response := stream.Result()
	if response.StopReason != StopReasonStop {
		t.Fatalf("expected live stop reason stop, got %q with error %q", response.StopReason, response.ErrorMessage)
	}
	if response.ResponseID == "" {
		t.Fatal("expected live kimi stream response id")
	}
	if !sawTextDelta {
		t.Fatal("expected live kimi stream text delta")
	}
}

func TestStreamSimpleOpenAICodexLive(t *testing.T) {
	if os.Getenv("PIGO_LIVE_TEST") != "1" {
		t.Skip("set PIGO_LIVE_TEST=1 to run live openai-codex stream test")
	}

	token := loadOpenAICodexTestToken("01_auth.json")
	if token == "" {
		t.Skip("missing test-only openai codex token in 01_auth.json")
	}

	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected openai-codex model")
	}

	stream := StreamSimple(*model, Context{
		SystemPrompt: "Reply with exactly OK.",
		Messages: []Message{
			UserMessage{Content: "Say OK"},
		},
	}, SimpleStreamOptions{
		APIKey: token,
	})

	sawTextDelta := false
	for event := range stream.Events() {
		if event.Type == AssistantMessageEventTextDelta && strings.TrimSpace(event.Delta) != "" {
			sawTextDelta = true
		}
	}

	response := stream.Result()
	if response.StopReason != StopReasonStop {
		t.Fatalf("expected live stop reason stop, got %q with error %q", response.StopReason, response.ErrorMessage)
	}
	if response.ResponseID == "" {
		t.Fatal("expected live codex stream response id")
	}
	if !sawTextDelta {
		t.Fatal("expected live codex stream text delta")
	}
}

func TestStreamSimpleOpenAICodexLiveAbortAndReplay(t *testing.T) {
	if os.Getenv("PIGO_LIVE_TEST") != "1" {
		t.Skip("set PIGO_LIVE_TEST=1 to run live openai-codex abort test")
	}

	token := loadOpenAICodexTestToken("01_auth.json")
	if token == "" {
		t.Skip("missing test-only openai codex token in 01_auth.json")
	}

	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected openai-codex model")
	}

	aborted := abortLiveStreamAfterFirstDelta(t, *model, SimpleStreamOptions{
		APIKey: token,
	})
	if aborted.Usage.Input != 0 || aborted.Usage.Output != 0 || aborted.Usage.TotalTokens != 0 {
		t.Fatalf("expected live codex abort to keep zero token stats, got %+v", aborted.Usage)
	}

	followup := CompleteSimple(*model, Context{
		Messages: []Message{
			UserMessage{Content: "What is 15 + 27?"},
			aborted,
			UserMessage{Content: "Please continue, but only answer with the number."},
		},
	}, SimpleStreamOptions{
		APIKey: token,
	})

	if followup.StopReason != StopReasonStop {
		t.Fatalf("expected successful live follow-up after abort, got %+v", followup)
	}
}

func TestStreamSimpleKimiCodingLiveAbortAndReplay(t *testing.T) {
	if os.Getenv("PIGO_LIVE_TEST") != "1" {
		t.Skip("set PIGO_LIVE_TEST=1 to run live kimi-coding abort test")
	}

	apiKey := GetEnvAPIKey("kimi-coding")
	if apiKey == "" {
		t.Skip("missing KIMI_API_KEY for live abort test")
	}

	model := GetModel("kimi-coding", "kimi-k2-thinking")
	if model == nil {
		t.Fatal("expected kimi model")
	}

	aborted := abortLiveStreamAfterFirstDelta(t, *model, SimpleStreamOptions{
		APIKey: apiKey,
	})
	if aborted.Usage.Input <= 0 {
		t.Fatalf("expected live kimi abort to keep input token stats, got %+v", aborted.Usage)
	}
	if aborted.Usage.Output != 0 {
		t.Fatalf("expected live kimi abort to keep zero output tokens, got %+v", aborted.Usage)
	}

	followup := CompleteSimple(*model, Context{
		Messages: []Message{
			UserMessage{Content: "What is 15 + 27?"},
			aborted,
			UserMessage{Content: "Please continue, but only answer with the number."},
		},
	}, SimpleStreamOptions{
		APIKey: apiKey,
	})

	if followup.StopReason != StopReasonStop {
		t.Fatalf("expected successful live follow-up after abort, got %+v", followup)
	}
}

