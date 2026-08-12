package pigo

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCommandCodeProviderRegistration(t *testing.T) {
	model := GetModel("commandcode", "deepseek/deepseek-v4-flash")
	if model == nil {
		t.Fatal("expected Command Code model to be registered")
	}
	if model.API != "commandcode-custom" || model.BaseURL != commandCodeDefaultBaseURL {
		t.Fatalf("unexpected Command Code transport metadata: %+v", model)
	}
	if model.Name != "DeepSeek V4 Flash (CC)" || !model.Reasoning || model.ContextWindow != 1000000 || model.MaxTokens != 65536 {
		t.Fatalf("unexpected Command Code model metadata: %+v", model)
	}
	if model.Cost != (UsageCost{Input: 0.14, Output: 0.28, CacheRead: 0.0028}) {
		t.Fatalf("unexpected Command Code model pricing: %+v", model.Cost)
	}
	if model.Headers["User-Agent"] != "node" || model.Headers["x-command-code-version"] != commandCodeCLIVersion {
		t.Fatalf("expected pi extension identity headers, got %+v", model.Headers)
	}
	module := resolveProviderModule("commandcode")
	if module == nil || module.Auth.EnvAPIKeyName != "COMMANDCODE_API_KEY" {
		t.Fatalf("unexpected Command Code auth metadata: %+v", module)
	}
	if RequiresOAuth("commandcode") {
		t.Fatal("Command Code must continue accepting API keys without requiring OAuth")
	}
	if !GetProviderCapabilities("commandcode").SupportsStreaming {
		t.Fatal("expected Command Code streaming capability")
	}
	if options := BuildProviderStreamOptions(*model, SimpleStreamOptions{}); options.MaxTokens != model.MaxTokens {
		t.Fatalf("Command Code must default to the selected model limit before the generate cap, got %d", options.MaxTokens)
	}
}

func TestCommandCodeModelPricingIsComplete(t *testing.T) {
	if len(commandCodeModelCosts) != len(commandCodeModelSpecs) {
		t.Fatalf("pricing entries=%d model specs=%d", len(commandCodeModelCosts), len(commandCodeModelSpecs))
	}
	for _, spec := range commandCodeModelSpecs {
		if _, ok := commandCodeModelCosts[spec.ID]; !ok {
			t.Errorf("missing pricing for %q", spec.ID)
		}
	}

	tests := map[string]UsageCost{
		"claude-sonnet-5":                 {Input: 2, Output: 10, CacheRead: 0.2, CacheWrite: 2.5},
		"gpt-5.6-terra":                   {Input: 2.5, Output: 15, CacheRead: 0.25, CacheWrite: 3.125},
		"Qwen/Qwen3.7-Max":                {Input: 2.5, Output: 7.5, CacheRead: 0.5, CacheWrite: 3.13},
		"xiaomi/mimo-v2.5":                {Input: 0.14, Output: 0.28, CacheRead: 0.0028},
		"poolside/laguna-s-2.1-free":      {},
		"inclusionai/ling-3.0-flash-free": {},
	}
	for modelID, want := range tests {
		if got := commandCodeModelCosts[modelID]; got != want {
			t.Errorf("pricing for %q = %+v, want %+v", modelID, got, want)
		}
	}
}

func TestCommandCodeLongContextPricing(t *testing.T) {
	model := Model{ID: "Qwen/Qwen3.7-Plus", Cost: commandCodeModelCosts["Qwen/Qwen3.7-Plus"]}
	standard := calculateCommandCodeCost(model, Usage{Input: 256_000, Output: 1_000})
	if standard.Input != 0.1024 || standard.Output != 0.0016 {
		t.Fatalf("unexpected standard-context cost: %+v", standard)
	}
	long := calculateCommandCodeCost(model, Usage{Input: 255_000, CacheRead: 1_001, Output: 1_000})
	if math.Abs(long.Input-0.306) > 1e-12 || math.Abs(long.CacheRead-0.00024024) > 1e-12 || math.Abs(long.Output-0.0048) > 1e-12 {
		t.Fatalf("unexpected long-context cost: %+v", long)
	}
	longCacheWrite := calculateCommandCodeCost(model, Usage{Input: 255_000, CacheWrite: 1_001, Output: 1_000})
	if math.Abs(longCacheWrite.Input-0.306) > 1e-12 || math.Abs(longCacheWrite.CacheWrite-0.0015015) > 1e-12 || math.Abs(longCacheWrite.Output-0.0048) > 1e-12 {
		t.Fatalf("unexpected cache-write long-context cost: %+v", longCacheWrite)
	}
}

func TestApplyCommandCodeUsageSplitsCachedInput(t *testing.T) {
	tests := []struct {
		name       string
		totalInput int
		details    map[string]any
		wantInput  int
		wantTotal  int
	}{
		{
			name:       "derive uncached input from total",
			totalInput: 10,
			details:    map[string]any{"cacheReadTokens": 3, "cacheWriteTokens": 2},
			wantInput:  5,
			wantTotal:  14,
		},
		{
			name:       "prefer explicit uncached input",
			totalInput: 10,
			details:    map[string]any{"noCacheTokens": 4, "cacheReadTokens": 3, "cacheWriteTokens": 2},
			wantInput:  4,
			wantTotal:  13,
		},
		{
			name:       "ignore nonnumeric uncached input",
			totalInput: 10,
			details:    map[string]any{"noCacheTokens": nil, "cacheReadTokens": 3, "cacheWriteTokens": 2},
			wantInput:  5,
			wantTotal:  14,
		},
		{
			name:       "clamp derived uncached input",
			totalInput: 4,
			details:    map[string]any{"cacheReadTokens": 3, "cacheWriteTokens": 2},
			wantInput:  0,
			wantTotal:  9,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := &AssistantMessage{}
			applyCommandCodeUsage(map[string]any{
				"totalUsage": map[string]any{
					"inputTokens":       test.totalInput,
					"outputTokens":      4,
					"inputTokenDetails": test.details,
				},
			}, Model{}, response)
			if response.Usage.Input != test.wantInput || response.Usage.CacheRead != 3 || response.Usage.CacheWrite != 2 || response.Usage.Output != 4 || response.Usage.TotalTokens != test.wantTotal {
				t.Fatalf("unexpected usage: %+v", response.Usage)
			}
		})
	}
}

func TestCommandCodeJSONSchemaPreservesStandardSchema(t *testing.T) {
	schema := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"count"},
		"properties": map[string]any{
			"count": map[string]any{"type": "integer", "minimum": 1.0},
			"value": map[string]any{"anyOf": []any{
				map[string]any{"type": "string", "enum": []any{"auto"}},
				map[string]any{"type": "null"},
			}},
			"choice": map[string]any{"oneOf": []any{
				map[string]any{"type": "number"},
				map[string]any{"type": "boolean"},
			}},
		},
	}

	converted := commandCodeRecord(commandCodeJSONSchema(schema))
	if !jsonValuesEqual(converted, schema) {
		t.Fatalf("standard JSON Schema changed:\n got: %#v\nwant: %#v", converted, schema)
	}
	converted["type"] = "array"
	if schema["type"] != "object" {
		t.Fatal("schema conversion must return a deep copy")
	}
}

func TestCommandCodeJSONSchemaConvertsInternalUnion(t *testing.T) {
	converted := commandCodeRecord(commandCodeJSONSchema(map[string]any{
		"kind": "union",
		"variants": []any{
			map[string]any{"kind": "integer"},
			map[string]any{"kind": "string"},
		},
	}))
	variants := commandCodeAnySlice(converted["anyOf"])
	if len(variants) != 2 || commandCodeRecord(variants[0])["type"] != "integer" || commandCodeRecord(variants[1])["type"] != "string" {
		t.Fatalf("unexpected internal union conversion: %#v", converted)
	}
}

func TestCommandCodeStreamMatchesPiExtensionProtocol(t *testing.T) {
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	var callbackStatus int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/alpha/generate" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("unexpected authorization %q", r.Header.Get("Authorization"))
		}
		if r.Header.Get("User-Agent") != "request-agent" {
			t.Errorf("request headers must override provider headers case-insensitively, got %q", r.Header.Get("User-Agent"))
		}
		wantHeaders := map[string]string{
			"Accept":                 "*/*",
			"Accept-Encoding":        "gzip, deflate",
			"Accept-Language":        "*",
			"Content-Type":           "application/json",
			"Sec-Fetch-Mode":         "cors",
			"x-cli-environment":      "production",
			"x-co-flag":              "false",
			"x-command-code-version": commandCodeCLIVersion,
			"x-project-slug":         commandCodeProjectSlug(workingDir),
			"x-taste-learning":       "true",
		}
		for name, want := range wantHeaders {
			if got := r.Header.Get(name); got != want {
				t.Errorf("header %s: got %q want %q", name, got, want)
			}
		}

		body, readErr := io.ReadAll(r.Body)
		if readErr != nil {
			t.Fatal(readErr)
		}
		var request map[string]any
		if unmarshalErr := json.Unmarshal(body, &request); unmarshalErr != nil {
			t.Fatalf("decode request: %v", unmarshalErr)
		}
		params := commandCodeRecord(request["params"])
		if params["model"] != "command-model" || commandCodeInt(params["max_tokens"]) != 64000 || params["temperature"] != 0.3 || params["stream"] != true {
			t.Errorf("unexpected params: %+v", params)
		}
		if params["system"] != "system prompt" {
			t.Errorf("unexpected system prompt: %v", params["system"])
		}
		messages := commandCodeAnySlice(params["messages"])
		if len(messages) != 3 {
			t.Errorf("expected user, assistant, and paired tool history, got %+v", messages)
		}
		assistant := commandCodeRecord(messages[1])
		assistantParts := commandCodeAnySlice(assistant["content"])
		if len(assistantParts) != 3 {
			t.Errorf("expected text, reasoning, and only the paired tool call, got %+v", assistantParts)
		}
		tools := commandCodeAnySlice(params["tools"])
		if len(tools) != 1 {
			t.Fatalf("expected one tool, got %+v", tools)
		}
		schema := commandCodeRecord(commandCodeRecord(tools[0])["input_schema"])
		if schema["type"] != "object" || len(commandCodeStringSlice(schema["required"])) != 1 {
			t.Errorf("unexpected tool schema: %+v", schema)
		}
		config := commandCodeRecord(request["config"])
		if config["workingDir"] != workingDir || !strings.Contains(anyString(config["environment"]), "Go go") {
			t.Errorf("unexpected runtime config: %+v", config)
		}
		if threadID := anyString(request["threadId"]); len(threadID) != 36 || strings.Count(threadID, "-") != 4 {
			t.Errorf("unexpected thread id %q", threadID)
		}
		if request["memory"] != nil || request["taste"] != nil || request["skills"] != nil {
			t.Errorf("provider must not attach persisted state: %+v", request)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintln(w, `data: {"type":"reasoning-delta","text":"think"}`)
		_, _ = fmt.Fprintln(w, `{"type":"reasoning-end"}`)
		_, _ = fmt.Fprintln(w, `event: message`)
		_, _ = fmt.Fprintln(w, `data: {"type":"text-delta","text":"answer"}`)
		_, _ = fmt.Fprintln(w, `not-json`)
		_, _ = fmt.Fprintln(w, `{"type":"tool-call","toolCallId":"new-call","toolName":"write","arguments":"{\"path\":\"a.go\"}"}`)
		_, _ = fmt.Fprintln(w, `{"type":"finish","finishReason":"tool-calls","totalUsage":{"inputTokens":10,"outputTokens":4,"inputTokenDetails":{"cacheReadTokens":3,"cacheWriteTokens":2}}}`)
	}))
	defer server.Close()

	model := Model{
		ID: "command-model", API: "commandcode-custom", Provider: "commandcode", BaseURL: server.URL,
		Reasoning: true, Input: []InputType{InputText}, MaxTokens: 65536,
		Cost:    UsageCost{Input: 1, Output: 2, CacheRead: 0.5, CacheWrite: 3},
		Headers: map[string]string{"User-Agent": "node", "x-command-code-version": commandCodeCLIVersion},
	}
	context := Context{
		SystemPrompt: "system prompt",
		Messages: []Message{
			UserMessage{Content: "hello"},
			AssistantMessage{Content: []ContentBlock{
				TextContent{Text: "prior"},
				ThinkingContent{Thinking: "prior thought"},
				ToolCall{ID: "paired", Name: "read", Arguments: map[string]any{"path": "a.go"}},
				ToolCall{ID: "orphan", Name: "read", Arguments: map[string]any{"path": "b.go"}},
			}},
			ToolResultMessage{ToolCallID: "paired", ToolName: "read", Content: []ContentBlock{TextContent{Text: "ok"}}},
			ToolResultMessage{ToolCallID: "missing", ToolName: "read", Content: []ContentBlock{TextContent{Text: "ignored"}}},
		},
		Tools: []Tool{{
			Name: "read", Description: "read a file",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []any{"path"}},
		}},
	}
	stream := StreamSimple(model, context, SimpleStreamOptions{
		APIKey:    "test-key",
		MaxTokens: 70000,
		Headers:   map[string]string{"user-agent": "request-agent"},
		OnResponse: func(response ProviderResponse, _ Model) {
			callbackStatus = response.Status
		},
	})
	events := collectCommandCodeEvents(stream)
	response := stream.Result()

	if callbackStatus != http.StatusOK {
		t.Fatalf("expected response callback, got %d", callbackStatus)
	}
	if response.StopReason != StopReasonToolUse || response.ErrorMessage != "" {
		t.Fatalf("unexpected final response: %+v", response)
	}
	if len(response.Content) != 3 {
		t.Fatalf("expected thinking, text, and tool call, got %+v", response.Content)
	}
	if thinking, ok := response.Content[0].(ThinkingContent); !ok || thinking.Thinking != "think" {
		t.Fatalf("unexpected thinking block: %+v", response.Content[0])
	}
	if text, ok := response.Content[1].(TextContent); !ok || text.Text != "answer" {
		t.Fatalf("unexpected text block: %+v", response.Content[1])
	}
	if call, ok := response.Content[2].(ToolCall); !ok || call.ID != "new-call" || call.Arguments["path"] != "a.go" {
		t.Fatalf("unexpected tool call: %+v", response.Content[2])
	}
	if response.Usage.Input != 5 || response.Usage.Output != 4 || response.Usage.CacheRead != 3 || response.Usage.CacheWrite != 2 || response.Usage.TotalTokens != 14 {
		t.Fatalf("unexpected usage: %+v", response.Usage)
	}
	if math.Abs(response.Usage.Cost.Total-0.0000205) > 1e-12 {
		t.Fatalf("unexpected calculated cost: %+v", response.Usage.Cost)
	}
	wantEventTypes := []AssistantMessageEventType{
		AssistantMessageEventStart,
		AssistantMessageEventThinkingStart,
		AssistantMessageEventThinkingDelta,
		AssistantMessageEventThinkingEnd,
		AssistantMessageEventTextStart,
		AssistantMessageEventTextDelta,
		AssistantMessageEventTextEnd,
		AssistantMessageEventToolCallStart,
		AssistantMessageEventToolCallEnd,
		AssistantMessageEventDone,
	}
	if len(events) != len(wantEventTypes) {
		t.Fatalf("unexpected events: %+v", events)
	}
	for index, want := range wantEventTypes {
		if events[index].Type != want {
			t.Fatalf("event %d: got %s want %s", index, events[index].Type, want)
		}
	}
}

func TestCommandCodeStreamRejectsTruncatedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintln(w, `{"type":"text-delta","text":"partial"}`)
	}))
	defer server.Close()

	response := CompleteSimple(commandCodeTestModel(server.URL), Context{Messages: []Message{UserMessage{Content: "hello"}}}, SimpleStreamOptions{APIKey: "test-key"})
	if response.StopReason != StopReasonError || !strings.Contains(response.ErrorMessage, "before finish event") {
		t.Fatalf("expected truncated stream error, got %+v", response)
	}
}

func TestCommandCodeStreamRetriesTransientHTTPFailure(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, "rate limited")
			return
		}
		_, _ = fmt.Fprintln(w, `{"type":"finish","finishReason":"stop"}`)
	}))
	defer server.Close()

	response := CompleteSimple(commandCodeTestModel(server.URL), Context{}, SimpleStreamOptions{APIKey: "test-key", MaxRetries: 1})
	if response.StopReason != StopReasonStop || attempts.Load() != 2 {
		t.Fatalf("expected one retry and success, attempts=%d response=%+v", attempts.Load(), response)
	}
}

func TestCommandCodeStreamDecodesNodeFetchEncodings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Encoding") != "gzip, deflate" {
			t.Errorf("unexpected accept encoding %q", r.Header.Get("Accept-Encoding"))
		}
		w.Header().Set("Content-Encoding", "gzip")
		writer := gzip.NewWriter(w)
		_, _ = fmt.Fprintln(writer, `{"type":"text-delta","text":"compressed"}`)
		_, _ = fmt.Fprintln(writer, `{"type":"finish","finishReason":"stop"}`)
		_ = writer.Close()
	}))
	defer server.Close()

	response := CompleteSimple(commandCodeTestModel(server.URL), Context{}, SimpleStreamOptions{APIKey: "test-key"})
	if response.StopReason != StopReasonStop || len(response.Content) != 1 {
		t.Fatalf("expected compressed stream success, got %+v", response)
	}
	if text, ok := response.Content[0].(TextContent); !ok || text.Text != "compressed" {
		t.Fatalf("unexpected compressed response content: %+v", response.Content)
	}
}

func TestCommandCodeStreamTimeoutIsAborted(t *testing.T) {
	requestStarted := make(chan struct{}, 1)
	httpClient := &http.Client{Transport: providerTimeoutRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		requestStarted <- struct{}{}
		<-request.Context().Done()
		return nil, request.Context().Err()
	})}

	response := CompleteSimple(commandCodeTestModel("https://example.invalid"), Context{}, SimpleStreamOptions{
		APIKey: "test-key", HTTPClient: httpClient, TimeoutMs: 25,
	})
	if response.StopReason != StopReasonAborted {
		t.Fatalf("expected timeout to abort request, got %+v", response)
	}
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("request did not reach transport")
	}
}

func TestCommandCodeProjectSlug(t *testing.T) {
	tests := map[string]string{
		`C:\Users\Icatm\My Project`: "users-icatm-my-project",
		`/tmp/pi_go`:                "tmp-pi-go",
		`---`:                       "project",
	}
	for input, want := range tests {
		if got := commandCodeProjectSlug(input); got != want {
			t.Errorf("commandCodeProjectSlug(%q) = %q, want %q", input, got, want)
		}
	}
}

func commandCodeTestModel(baseURL string) Model {
	return Model{
		ID: "command-model", API: "commandcode-custom", Provider: "commandcode", BaseURL: baseURL,
		Reasoning: true, Input: []InputType{InputText}, MaxTokens: 65536,
	}
}

func collectCommandCodeEvents(stream *AssistantMessageEventStream) []AssistantMessageEvent {
	var events []AssistantMessageEvent
	for event := range stream.Events() {
		events = append(events, event)
	}
	return events
}
