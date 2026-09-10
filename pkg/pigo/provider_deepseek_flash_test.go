package pigo

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestDeepSeekFlashCapabilityFacts(t *testing.T) {
	snapshot, ok := LookupModelCapabilities("deepseek", "deepseek-flash")
	if !ok {
		t.Fatal("missing exact deepseek-flash model")
	}
	if snapshot.WireAPI != "deepseek-chat-completions" || snapshot.BaseURL != "https://api.deepseek.com" ||
		snapshot.ContextWindow != 1000000 || snapshot.MaxOutputTokens != 384000 ||
		!slices.Equal(snapshot.Input, []InputType{InputText, InputImage}) {
		t.Fatalf("unexpected Flash model facts: %+v", snapshot)
	}
	caps := snapshot.Capabilities
	if caps.DefaultReasoningLevel != ModelThinkingLevelHigh || caps.Temperature != CapabilitySupported ||
		!slices.Equal(caps.TemperatureReasoningLevels, []ModelThinkingLevel{ModelThinkingLevelOff}) ||
		caps.TopP != CapabilitySupported || slices.Contains(caps.TopPReasoningLevels, ModelThinkingLevelOff) ||
		caps.StrictTools != CapabilityUnsupported || caps.ParallelToolCalls != CapabilityUnsupported {
		t.Fatalf("unexpected Flash capability facts: %+v", caps)
	}
	if snapshot.ResponseFormats.JSONObject != CapabilitySupported || snapshot.ResponseFormats.JSONSchema != CapabilityUnsupported {
		t.Fatalf("Chat structured output facts must not claim Responses-only JSON schema: %+v", snapshot.ResponseFormats)
	}
	if !slices.Equal(caps.SupportedReasoningLevels, extendedThinkingLevels) {
		t.Fatalf("missing explicitly mapped reasoning levels: %v", caps.SupportedReasoningLevels)
	}
	if _, ok := LookupModelCapabilities("deepseek", "deepseek-flash-custom"); ok {
		t.Fatal("model prefix must not imply capabilities")
	}
	for _, id := range []string{"deepseek-v4-flash", "deepseek-v4-pro"} {
		legacy := GetModel("deepseek", id)
		if legacy == nil || legacy.MaxTokens != 64000 || !slices.Equal(legacy.Input, []InputType{InputText}) ||
			legacy.ThinkingLevelMap[ModelThinkingLevelXHigh] != "max" {
			t.Fatalf("legacy model facts changed for %s: %+v", id, legacy)
		}
	}
}

func TestDeepSeekFlashThinkingEffortWireFormat(t *testing.T) {
	model := *GetModel("deepseek", "deepseek-flash")
	for _, tc := range []struct {
		level  ThinkingLevel
		kind   string
		effort string
	}{
		{"", "enabled", "high"},
		{ThinkingLevelMinimal, "enabled", "low"},
		{ThinkingLevelLow, "enabled", "low"},
		{ThinkingLevelMedium, "enabled", "high"},
		{ThinkingLevelHigh, "enabled", "high"},
		{ThinkingLevelXHigh, "enabled", "high"},
		{ThinkingLevelMax, "enabled", "max"},
		{"off", "disabled", ""},
		{"none", "disabled", ""},
	} {
		t.Run(string(tc.level), func(t *testing.T) {
			request := buildDeepSeekChatRequest(model, Context{}, ProviderStreamOptions{Reasoning: tc.level})
			if request.Thinking == nil || request.Thinking.Type != tc.kind || request.ReasoningEffort != tc.effort || request.Thinking.ReasoningEffort != "" {
				t.Fatalf("unexpected thinking request: %+v", request)
			}
		})
	}
}

func TestDeepSeekFlashChatWireAndToolRoundTrip(t *testing.T) {
	model := *GetModel("deepseek", "deepseek-flash")
	model.BaseURL = "https://gateway.example/tenant/deepseek"
	var captured map[string]any
	topP := 0.97
	response := CompleteSimple(model, Context{
		SystemPrompt: "Use JSON when returning the result.",
		Tools:        []Tool{{Name: "inspect", Parameters: map[string]any{"type": "object"}}},
		Messages: []Message{
			UserMessage{Content: []ContentBlock{TextContent{Text: "Read the image"}, ImageContent{Data: "aW1hZ2U=", MIMEType: "image/png"}}},
			AssistantMessage{Provider: model.Provider, API: model.API, Model: model.ID, Content: []ContentBlock{
				ThinkingContent{Thinking: " inspect "}, TextContent{Text: "Checking"},
				ToolCall{ID: "call_1", Name: "inspect", Arguments: map[string]any{}},
			}},
			ToolResultMessage{ToolCallID: "call_1", ToolName: "inspect", Content: []ContentBlock{TextContent{Text: "ready"}}},
		},
	}, SimpleStreamOptions{
		APIKey: "test-key", TopP: &topP, ResponseFormat: &ResponseFormat{Type: ResponseFormatJSON},
		HTTPClient: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			if request.URL.String() != "https://gateway.example/tenant/deepseek/chat/completions" || request.Header.Get("Authorization") != "Bearer test-key" {
				return nil, fmt.Errorf("wrong URL/auth: %s", request.URL)
			}
			if err := json.NewDecoder(request.Body).Decode(&captured); err != nil {
				return nil, err
			}
			return sseResponse(`data: {"id":"chat_1","model":"deepseek-flash","choices":[{"delta":{"reasoning_content":"verified"}}]}` + "\n\n" +
				`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_2","type":"function","function":{"name":"inspect","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":4,"total_tokens":14,"prompt_cache_hit_tokens":2}}` + "\n\n" + `data: [DONE]` + "\n\n"), nil
		}).Client(),
	})
	if response.StopReason != StopReasonToolUse || len(response.Content) != 2 || response.Usage.Input != 8 || response.Usage.CacheRead != 2 {
		t.Fatalf("invalid streamed response: %+v", response)
	}
	if captured["model"] != "deepseek-flash" || captured["reasoning_effort"] != "high" || captured["top_p"] != topP || captured["stream"] != true {
		t.Fatalf("wrong wire options: %#v", captured)
	}
	thinking := captured["thinking"].(map[string]any)
	if len(thinking) != 1 || thinking["type"] != "enabled" {
		t.Fatalf("reasoning_effort must be top level: %#v", thinking)
	}
	messages := captured["messages"].([]any)
	parts := messages[1].(map[string]any)["content"].([]any)
	if parts[1].(map[string]any)["image_url"].(map[string]any)["url"] != "data:image/png;base64,aW1hZ2U=" {
		t.Fatalf("image was not transmitted: %#v", parts)
	}
	assistant := messages[2].(map[string]any)
	if assistant["content"] != "Checking" || assistant["reasoning_content"] != " inspect " || messages[3].(map[string]any)["tool_call_id"] != "call_1" {
		t.Fatalf("reasoning/tool history was not preserved: %#v", messages)
	}
	if captured["response_format"].(map[string]any)["type"] != "json_object" {
		t.Fatalf("wrong JSON request: %#v", captured)
	}
	replay := convertDeepSeekChatMessages(model, Context{Messages: []Message{response}, Tools: []Tool{{Name: "inspect"}}})
	if replay[0].ReasoningContent != "verified" || len(replay[0].ToolCalls) != 1 || replay[0].ToolCalls[0].ID != "call_2" {
		t.Fatalf("streamed thinking/tool call could not be replayed: %+v", replay)
	}
}

func TestDeepSeekFlashRejectsIneffectiveSamplingBeforeDispatch(t *testing.T) {
	model := *GetModel("deepseek", "deepseek-flash")
	temperature, lowTopP, topP, invalid := 0.5, 0.94, 0.95, math.NaN()
	parallel := false
	for _, tc := range []struct {
		name    string
		options ProviderStreamOptions
		want    string
	}{
		{"thinking temperature", ProviderStreamOptions{Temperature: &temperature}, "temperature is not supported"},
		{"nonthinking top_p", ProviderStreamOptions{Reasoning: "off", TopP: &topP}, "top_p is fixed"},
		{"below thinking minimum", ProviderStreamOptions{TopP: &lowTopP}, "between 0.95 and 1"},
		{"invalid top_p", ProviderStreamOptions{TopP: &invalid}, "between 0.95 and 1"},
		{"invalid temperature", ProviderStreamOptions{Reasoning: "off", Temperature: &invalid}, "between 0 and 2"},
		{"fixed parallel tools", ProviderStreamOptions{ParallelToolCalls: &parallel}, "not configurable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dispatched := false
			tc.options.APIKey = "test-key"
			tc.options.HTTPClient = roundTripFunc(func(*http.Request) (*http.Response, error) {
				dispatched = true
				return nil, fmt.Errorf("unexpected dispatch")
			}).Client()
			response := Complete(model, Context{Messages: []Message{UserMessage{Content: "ping"}}}, tc.options)
			if response.StopReason != StopReasonError || !strings.Contains(response.ErrorMessage, tc.want) || dispatched {
				t.Fatalf("expected pre-dispatch rejection %q, got %+v, dispatched=%v", tc.want, response, dispatched)
			}
		})
	}
	for _, options := range []ProviderStreamOptions{{Reasoning: "off", Temperature: &temperature}, {Reasoning: ThinkingLevelMax, TopP: &topP}} {
		if err := validateDeepSeekFlashOptions(model, options); err != nil {
			t.Fatalf("supported sampling rejected: %v", err)
		}
	}
}

func TestDeepSeekFlashResponsesFormatRequiresExplicitAdapter(t *testing.T) {
	model := *GetModel("deepseek", "deepseek-flash")
	format := &ResponseFormat{Type: ResponseFormatJSONSchema, Name: "result", JSONSchema: json.RawMessage(`{"type":"object"}`)}
	if err := ValidateResponseFormat(model, format); err == nil {
		t.Fatal("Chat must reject Responses-only JSON schema")
	}
	model.API = "openai-responses"
	model.Compat = &OpenAIResponsesCompat{SupportsJSONOutput: true, SupportsJSONSchema: true}
	if err := ValidateResponseFormat(model, format); err != nil {
		t.Fatalf("explicit Responses adapter rejected JSON schema: %v", err)
	}
	request := buildOpenAIResponsesRequest(model, Context{}, ProviderStreamOptions{Reasoning: "off", ResponseFormat: format})
	if request.Reasoning == nil || request.Reasoning.Effort != "none" || request.Text == nil || request.Text.Format.Type != "json_schema" ||
		!reflect.DeepEqual(request.Text.Format.Schema, format.JSONSchema) {
		t.Fatalf("invalid Responses format/reasoning request: %+v", request)
	}
}
