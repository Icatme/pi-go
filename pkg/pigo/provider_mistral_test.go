package pigo

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMistralSimpleTextStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Errorf("expected bearer token, got %q", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"stream":true`) {
			t.Error("expected stream=true in request body")
		}

		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		chunks := []string{
			`{"id":"m1","choices":[{"delta":{"content":"Hello "}}]}`,
			`{"id":"m1","choices":[{"delta":{"content":"world"}}]}`,
			`{"id":"m1","choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`,
		}
		for _, chunk := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
			flusher.Flush()
		}
	}))
	defer server.Close()

	model := Model{
		ID:            "mistral-large-latest",
		API:           "mistral-conversations",
		Provider:      "mistral",
		BaseURL:       server.URL,
		Input:         []InputType{InputText},
		ContextWindow: 128000,
		MaxTokens:     4096,
	}

	stream := streamMistral(model, Context{
		Messages: []Message{UserMessage{Content: "hi"}},
	}, ProviderStreamOptions{APIKey: "placeholder-api-key"})

	result := stream.Result()
	if result.StopReason != StopReasonStop {
		t.Errorf("expected stop, got %q", result.StopReason)
	}
	if result.Usage.Input != 5 || result.Usage.Output != 2 {
		t.Errorf("expected input 5 output 2, got %d %d", result.Usage.Input, result.Usage.Output)
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(result.Content))
	}
	text, ok := result.Content[0].(TextContent)
	if !ok || text.Text != "Hello world" {
		t.Errorf("expected text 'Hello world', got %+v", result.Content[0])
	}
}

func TestMistralThinkingStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		chunks := []string{
			`{"choices":[{"delta":{"reasoning_content":"thinking"}}]}`,
			`{"choices":[{"delta":{"content":"answer"}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"stop"}]}`,
		}
		for _, chunk := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
			flusher.Flush()
		}
	}))
	defer server.Close()

	model := Model{
		ID:            "mistral-large-latest",
		API:           "mistral-conversations",
		Provider:      "mistral",
		BaseURL:       server.URL,
		Reasoning:     true,
		Input:         []InputType{InputText},
		ContextWindow: 128000,
		MaxTokens:     4096,
	}

	stream := streamMistral(model, Context{
		Messages: []Message{UserMessage{Content: "solve"}},
	}, ProviderStreamOptions{APIKey: "placeholder-api-key", Reasoning: ThinkingLevelHigh})

	result := stream.Result()
	if len(result.Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(result.Content))
	}
	thinking, ok := result.Content[0].(ThinkingContent)
	if !ok || thinking.Thinking != "thinking" {
		t.Errorf("expected thinking block, got %+v", result.Content[0])
	}
	text, ok := result.Content[1].(TextContent)
	if !ok || text.Text != "answer" {
		t.Errorf("expected text block, got %+v", result.Content[1])
	}
}

func TestMistralToolCallStream(t *testing.T) {
	const raw = `{"location":"discard-first","location":"NYC"}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		chunks := []string{
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_abc123","function":{"name":"get_weather"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"location\":\"discard-first\",\"loc"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"ation\":\"NYC\"}"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":1,"id":"call_no_args","function":{"name":"get_weather"}}]}}]}`,
			`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`,
		}
		for _, chunk := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
			flusher.Flush()
		}
	}))
	defer server.Close()

	model := Model{
		ID:            "mistral-large-latest",
		API:           "mistral-conversations",
		Provider:      "mistral",
		BaseURL:       server.URL,
		Input:         []InputType{InputText},
		ContextWindow: 128000,
		MaxTokens:     4096,
	}

	stream := streamMistral(model, Context{
		Messages: []Message{UserMessage{Content: "weather"}},
		Tools: []Tool{{
			Name:        "get_weather",
			Description: "Get weather",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		}},
	}, ProviderStreamOptions{APIKey: "placeholder-api-key"})

	ended := make(map[string]string)
	for event := range stream.Events() {
		if event.Type == AssistantMessageEventToolCallEnd {
			ended[event.ToolCall.ID] = event.ToolCall.RawArguments
		}
	}
	result := stream.Result()
	if result.StopReason != StopReasonToolUse {
		t.Errorf("expected toolUse, got %q", result.StopReason)
	}
	if len(result.Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(result.Content))
	}
	call, ok := result.Content[0].(ToolCall)
	if !ok || call.Name != "get_weather" {
		t.Errorf("expected tool call, got %+v", result.Content[0])
	}
	if call.Arguments["location"] != "NYC" {
		t.Errorf("expected location NYC, got %+v", call.Arguments)
	}
	if len(call.ID) != mistralToolCallIDLength {
		t.Errorf("expected tool call id length %d, got %d (%q)", mistralToolCallIDLength, len(call.ID), call.ID)
	}
	missing := result.Content[1].(ToolCall)
	if len(ended) != 2 || ended[call.ID] != raw || call.RawArguments != raw || ended[missing.ID] != "" || missing.RawArguments != "" || missing.Arguments == nil || len(missing.Arguments) != 0 {
		t.Fatalf("incorrect completed-call evidence: events=%q calls=%+v", ended, result.Content)
	}
	_, replay := convertMistralAssistantContent(result.Content)
	if len(replay) != 2 || replay[0].Function.Arguments != `{"location":"NYC"}` || replay[1].Function.Arguments != "{}" {
		t.Errorf("unexpected request replay: %+v", replay)
	}
}

func TestMistralMissingAPIKey(t *testing.T) {
	model := Model{
		ID:            "mistral-large-latest",
		API:           "mistral-conversations",
		Provider:      "mistral",
		BaseURL:       "http://localhost",
		Input:         []InputType{InputText},
		ContextWindow: 128000,
		MaxTokens:     4096,
	}

	stream := streamMistral(model, Context{
		Messages: []Message{UserMessage{Content: "hi"}},
	}, ProviderStreamOptions{})

	result := stream.Result()
	if result.StopReason != StopReasonError {
		t.Errorf("expected error stop reason, got %q", result.StopReason)
	}
	if !strings.Contains(result.ErrorMessage, "missing api key") {
		t.Errorf("expected missing api key error, got %q", result.ErrorMessage)
	}
}

func TestMistralErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid API key"}}`))
	}))
	defer server.Close()

	model := Model{
		ID:            "mistral-large-latest",
		API:           "mistral-conversations",
		Provider:      "mistral",
		BaseURL:       server.URL,
		Input:         []InputType{InputText},
		ContextWindow: 128000,
		MaxTokens:     4096,
	}

	stream := streamMistral(model, Context{Messages: []Message{UserMessage{Content: "hi"}}}, ProviderStreamOptions{APIKey: "bad"})
	result := stream.Result()
	if result.StopReason != StopReasonError {
		t.Errorf("expected error stop reason, got %q", result.StopReason)
	}
	if !strings.Contains(result.ErrorMessage, "Invalid API key") {
		t.Errorf("expected invalid api key error, got %q", result.ErrorMessage)
	}
}

func TestMistralImageDowngradeNonVisionModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodyStr := string(body)
		if strings.Contains(bodyStr, "image_url") {
			t.Error("non-vision model should not send image_url")
		}
		if !strings.Contains(bodyStr, "image omitted") {
			t.Error("expected image placeholder in request")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
	}))
	defer server.Close()

	model := Model{
		ID:            "codestral-latest",
		API:           "mistral-conversations",
		Provider:      "mistral",
		BaseURL:       server.URL,
		Input:         []InputType{InputText},
		ContextWindow: 256000,
		MaxTokens:     4096,
	}

	stream := streamMistral(model, Context{
		Messages: []Message{UserMessage{Content: []ContentBlock{
			TextContent{Text: "look"},
			ImageContent{MIMEType: "image/png", Data: "abc123"},
		}}},
	}, ProviderStreamOptions{APIKey: "placeholder-api-key"})

	result := stream.Result()
	if result.StopReason != StopReasonStop {
		t.Errorf("expected stop, got %q", result.StopReason)
	}
}

func TestMistralStreamSimpleViaRegistry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	model := *GetModel("mistral", "mistral-large-latest")
	model.BaseURL = server.URL

	stream := StreamSimple(model, Context{Messages: []Message{UserMessage{Content: "hello"}}}, SimpleStreamOptions{APIKey: "placeholder-api-key"})
	result := stream.Result()
	if result.StopReason != StopReasonStop {
		t.Errorf("expected stop, got %q", result.StopReason)
	}
}
