package pigo

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGoogleSimpleTextStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-goog-api-key") != "placeholder-api-key" {
			t.Errorf("expected api key header, got %q", r.Header.Get("x-goog-api-key"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected flusher")
		}

		chunks := []string{
			`{"responseId":"r1","candidates":[{"content":{"parts":[{"text":"Hello "}]}}]}`,
			`{"candidates":[{"content":{"parts":[{"text":"world"}]}}]}`,
			`{"candidates":[{"content":{"parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2,"totalTokenCount":12}}`,
		}
		for _, chunk := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
			flusher.Flush()
		}
	}))
	defer server.Close()

	model := Model{
		ID:            "gemini-2.0-flash",
		API:           "google-generative-ai",
		Provider:      "google",
		BaseURL:       server.URL,
		Input:         []InputType{InputText, InputImage},
		ContextWindow: 1048576,
		MaxTokens:     8192,
	}

	stream := streamGoogle(model, Context{
		Messages: []Message{UserMessage{Content: "hi"}},
	}, ProviderStreamOptions{APIKey: "placeholder-api-key"})

	events := collectAssistantMessageEvents(stream.Events())
	result := stream.Result()

	if result.StopReason != StopReasonStop {
		t.Errorf("expected stop reason stop, got %q", result.StopReason)
	}
	if result.Usage.Input != 10 || result.Usage.Output != 2 {
		t.Errorf("expected input 10 output 2, got %d %d", result.Usage.Input, result.Usage.Output)
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(result.Content))
	}
	text, ok := result.Content[0].(TextContent)
	if !ok || text.Text != "Hello world" {
		t.Errorf("expected text 'Hello world', got %+v", result.Content[0])
	}

	if !hasEventType(events, AssistantMessageEventTextStart) {
		t.Error("expected text_start event")
	}
	if !hasEventType(events, AssistantMessageEventTextEnd) {
		t.Error("expected text_end event")
	}
	if !hasEventType(events, AssistantMessageEventDone) {
		t.Error("expected done event")
	}
}

func TestGoogleThinkingStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		chunks := []string{
			`{"candidates":[{"content":{"parts":[{"thought":true,"text":"Let me think"}]}}]}`,
			`{"candidates":[{"content":{"parts":[{"thought":true,"text":"..."}]}}]}`,
			`{"candidates":[{"content":{"parts":[{"text":"Done"}]}}]}`,
			`{"candidates":[{"content":{"parts":[]},"finishReason":"STOP"}]}`,
		}
		for _, chunk := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
			flusher.Flush()
		}
	}))
	defer server.Close()

	model := Model{
		ID:            "gemini-2.5-flash",
		API:           "google-generative-ai",
		Provider:      "google",
		BaseURL:       server.URL,
		Reasoning:     true,
		Input:         []InputType{InputText},
		ContextWindow: 1048576,
		MaxTokens:     65536,
	}

	stream := streamGoogle(model, Context{
		Messages: []Message{UserMessage{Content: "solve"}},
	}, ProviderStreamOptions{APIKey: "placeholder-api-key", Reasoning: ThinkingLevelHigh})

	result := stream.Result()
	if len(result.Content) != 2 {
		t.Fatalf("expected 2 content blocks, got %d", len(result.Content))
	}
	thinking, ok := result.Content[0].(ThinkingContent)
	if !ok || thinking.Thinking != "Let me think..." {
		t.Errorf("expected thinking block, got %+v", result.Content[0])
	}
	text, ok := result.Content[1].(TextContent)
	if !ok || text.Text != "Done" {
		t.Errorf("expected text block, got %+v", result.Content[1])
	}
}

func TestGoogleToolCallStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		chunks := []string{
			`{"candidates":[{"content":{"parts":[{"functionCall":{"name":"get_weather","args":{"location":"NYC"}}}]}}]}`,
			`{"candidates":[{"content":{"parts":[]},"finishReason":"STOP"}]}`,
		}
		for _, chunk := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", chunk)
			flusher.Flush()
		}
	}))
	defer server.Close()

	model := Model{
		ID:            "gemini-2.0-flash",
		API:           "google-generative-ai",
		Provider:      "google",
		BaseURL:       server.URL,
		Input:         []InputType{InputText},
		ContextWindow: 1048576,
		MaxTokens:     8192,
	}

	stream := streamGoogle(model, Context{
		Messages: []Message{UserMessage{Content: "weather"}},
		Tools: []Tool{{
			Name:        "get_weather",
			Description: "Get weather",
			Parameters:  map[string]any{"type": "object", "properties": map[string]any{}},
		}},
	}, ProviderStreamOptions{APIKey: "placeholder-api-key"})

	result := stream.Result()
	if result.StopReason != StopReasonToolUse {
		t.Errorf("expected stop reason toolUse, got %q", result.StopReason)
	}
	if len(result.Content) != 1 {
		t.Fatalf("expected 1 content block, got %d", len(result.Content))
	}
	call, ok := result.Content[0].(ToolCall)
	if !ok || call.Name != "get_weather" {
		t.Errorf("expected tool call, got %+v", result.Content[0])
	}
	if call.Arguments["location"] != "NYC" {
		t.Errorf("expected location NYC, got %+v", call.Arguments)
	}
}

func TestGoogleMissingAPIKey(t *testing.T) {
	model := Model{
		ID:            "gemini-2.0-flash",
		API:           "google-generative-ai",
		Provider:      "google",
		BaseURL:       "http://localhost",
		Input:         []InputType{InputText},
		ContextWindow: 1048576,
		MaxTokens:     8192,
	}

	stream := streamGoogle(model, Context{
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

func TestGoogleErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"Invalid API key"}}`))
	}))
	defer server.Close()

	model := Model{
		ID:            "gemini-2.0-flash",
		API:           "google-generative-ai",
		Provider:      "google",
		BaseURL:       server.URL,
		Input:         []InputType{InputText},
		ContextWindow: 1048576,
		MaxTokens:     8192,
	}

	stream := streamGoogle(model, Context{Messages: []Message{UserMessage{Content: "hi"}}}, ProviderStreamOptions{APIKey: "bad"})
	result := stream.Result()
	if result.StopReason != StopReasonError {
		t.Errorf("expected error stop reason, got %q", result.StopReason)
	}
	if !strings.Contains(result.ErrorMessage, "Invalid API key") {
		t.Errorf("expected invalid api key error, got %q", result.ErrorMessage)
	}
}

func TestGoogleImageDowngradeNonVisionModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		bodyStr := string(body)
		if strings.Contains(bodyStr, "inlineData") {
			t.Error("non-vision model should not send inline image data")
		}
		if !strings.Contains(bodyStr, "image omitted") {
			t.Error("expected image placeholder in request")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}]}\n\n")
	}))
	defer server.Close()

	model := Model{
		ID:            "gemini-2.0-flash-lite",
		API:           "google-generative-ai",
		Provider:      "google",
		BaseURL:       server.URL,
		Input:         []InputType{InputText}, // no image support
		ContextWindow: 1048576,
		MaxTokens:     8192,
	}

	stream := streamGoogle(model, Context{
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

func collectAssistantMessageEvents(events <-chan AssistantMessageEvent) []AssistantMessageEvent {
	var result []AssistantMessageEvent
	for event := range events {
		result = append(result, event)
	}
	return result
}

func hasEventType(events []AssistantMessageEvent, eventType AssistantMessageEventType) bool {
	for _, event := range events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

func TestGoogleStreamSimpleViaRegistry(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]},\"finishReason\":\"STOP\"}]}\n\n")
		flusher.Flush()
	}))
	defer server.Close()

	model := *GetModel("google", "gemini-2.0-flash")
	model.BaseURL = server.URL

	stream := StreamSimple(model, Context{Messages: []Message{UserMessage{Content: "hello"}}}, SimpleStreamOptions{APIKey: "placeholder-api-key"})
	result := stream.Result()
	if result.StopReason != StopReasonStop {
		t.Errorf("expected stop, got %q", result.StopReason)
	}
}
