package pigo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestGoogleThinkingWireCapabilities(t *testing.T) {
	tests := []struct {
		name      string
		id        string
		reasoning ThinkingLevel
		levels    ThinkingLevelMap
		budgets   ThinkingBudgets
		want      string
	}{
		{name: "omitted reasoning disables budget", id: "gemini-2.5-flash", want: `{"thinkingBudget":0}`},
		{name: "explicit off disables budget", id: "gemini-2.5-flash", reasoning: "off", want: `{"thinkingBudget":0}`},
		{name: "budget model cannot disable thinking", id: "gemini-2.5-pro",
			levels: ThinkingLevelMap{ModelThinkingLevelOff: ""}, want: `{"thinkingBudget":128}`},
		{name: "mapped medium", id: "gemini-3.1-pro-preview", reasoning: ThinkingLevelMedium,
			levels: ThinkingLevelMap{ModelThinkingLevelMedium: "high"}, want: `{"includeThoughts":true,"thinkingLevel":"HIGH"}`},
		{name: "native medium", id: "gemini-3.1-pro-preview", reasoning: ThinkingLevelMedium,
			levels: ThinkingLevelMap{ModelThinkingLevelMedium: "medium"}, want: `{"includeThoughts":true,"thinkingLevel":"MEDIUM"}`},
		{name: "uppercase mapping", id: "gemini-3.7-flash", reasoning: ThinkingLevelHigh,
			levels: ThinkingLevelMap{ModelThinkingLevelHigh: "LOW"}, want: `{"includeThoughts":true,"thinkingLevel":"LOW"}`},
		{name: "mapped max", id: "gemini-3.7-flash", reasoning: ThinkingLevelMax,
			levels: ThinkingLevelMap{ModelThinkingLevelMax: "medium"}, want: `{"includeThoughts":true,"thinkingLevel":"MEDIUM"}`},
		{name: "unsupported level clamps before mapping", id: "gemini-3.7-flash", reasoning: ThinkingLevelLow,
			levels: ThinkingLevelMap{ModelThinkingLevelLow: "", ModelThinkingLevelMedium: "high"}, want: `{"includeThoughts":true,"thinkingLevel":"HIGH"}`},
		{name: "omitted reasoning uses lowest supported level", id: "gemini-3.8-flash",
			levels: ThinkingLevelMap{ModelThinkingLevelOff: "", ModelThinkingLevelMinimal: "", ModelThinkingLevelLow: "low"}, want: `{"thinkingLevel":"LOW"}`},
		{name: "explicit off uses lowest supported level", id: "gemini-3.8-flash", reasoning: "off",
			levels: ThinkingLevelMap{ModelThinkingLevelOff: "", ModelThinkingLevelMinimal: "", ModelThinkingLevelLow: "low"}, want: `{"thinkingLevel":"LOW"}`},
		{name: "discrete model supports off", id: "gemma-4-27b-it", want: `{"thinkingBudget":0}`},
		{name: "flash alias", id: "gemini-flash-latest", reasoning: ThinkingLevelMedium, want: `{"includeThoughts":true,"thinkingLevel":"MEDIUM"}`},
		{name: "flash lite alias", id: "gemini-flash-lite-latest", reasoning: ThinkingLevelLow, want: `{"includeThoughts":true,"thinkingLevel":"LOW"}`},
		{name: "gemma compact name", id: "gemma4-27b-it", reasoning: ThinkingLevelHigh, want: `{"includeThoughts":true,"thinkingLevel":"HIGH"}`},
		{name: "mapped default budget", id: "gemini-2.5-flash", reasoning: ThinkingLevelMedium,
			levels: ThinkingLevelMap{ModelThinkingLevelMedium: "low"}, want: `{"includeThoughts":true,"thinkingBudget":2048}`},
		{name: "mapped custom budget", id: "gemini-2.5-flash", reasoning: ThinkingLevelMax,
			levels: ThinkingLevelMap{ModelThinkingLevelMax: "high"}, budgets: ThinkingBudgets{High: 1234}, want: `{"includeThoughts":true,"thinkingBudget":1234}`},
		{name: "partial budget preserves minimal", id: "gemini-2.5-flash", reasoning: ThinkingLevelMinimal,
			budgets: ThinkingBudgets{High: 1234}, want: `{"includeThoughts":true,"thinkingBudget":128}`},
		{name: "partial budget preserves low", id: "gemini-2.5-flash", reasoning: ThinkingLevelLow,
			budgets: ThinkingBudgets{High: 1234}, want: `{"includeThoughts":true,"thinkingBudget":2048}`},
		{name: "partial budget preserves medium", id: "gemini-2.5-flash", reasoning: ThinkingLevelMedium,
			budgets: ThinkingBudgets{High: 1234}, want: `{"includeThoughts":true,"thinkingBudget":8192}`},
		{name: "partial budget preserves high", id: "gemini-2.5-flash", reasoning: ThinkingLevelHigh,
			budgets: ThinkingBudgets{Low: 1234}, want: `{"includeThoughts":true,"thinkingBudget":24576}`},
		{name: "partial budget preserves mapped default", id: "gemini-2.5-flash", reasoning: ThinkingLevelMedium,
			levels: ThinkingLevelMap{ModelThinkingLevelMedium: "low"}, budgets: ThinkingBudgets{High: 1234}, want: `{"includeThoughts":true,"thinkingBudget":2048}`},
		{name: "partial budget preserves pro omitted minimum", id: "gemini-2.5-pro",
			levels: ThinkingLevelMap{ModelThinkingLevelOff: ""}, budgets: ThinkingBudgets{High: 1234}, want: `{"thinkingBudget":128}`},
		{name: "partial budget preserves pro off minimum", id: "gemini-2.5-pro", reasoning: "off",
			levels: ThinkingLevelMap{ModelThinkingLevelOff: ""}, budgets: ThinkingBudgets{High: 1234}, want: `{"thinkingBudget":128}`},
		{name: "explicit dynamic budget", id: "gemini-2.5-flash", reasoning: ThinkingLevelHigh,
			budgets: ThinkingBudgets{High: -1}, want: `{"includeThoughts":true,"thinkingBudget":-1}`},
		{name: "unknown model defaults to dynamic budget", id: "custom-thinking-model", reasoning: ThinkingLevelHigh,
			want: `{"includeThoughts":true,"thinkingBudget":-1}`},
		{name: "unknown model partial budget defaults to dynamic", id: "custom-thinking-model", reasoning: ThinkingLevelLow,
			budgets: ThinkingBudgets{High: 1234}, want: `{"includeThoughts":true,"thinkingBudget":-1}`},
		{name: "unknown model respects custom budget", id: "custom-thinking-model", reasoning: ThinkingLevelHigh,
			budgets: ThinkingBudgets{High: 1234}, want: `{"includeThoughts":true,"thinkingBudget":1234}`},
		{name: "unknown model can disable thinking", id: "custom-thinking-model", reasoning: "off",
			budgets: ThinkingBudgets{High: 1234}, want: `{"thinkingBudget":0}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requests := make(chan map[string]any, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body struct {
					GenerationConfig struct {
						Thinking map[string]any `json:"thinkingConfig"`
					} `json:"generationConfig"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("decode request: %v", err)
				}
				requests <- body.GenerationConfig.Thinking
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}]}\n\n")
			}))
			defer server.Close()
			model := Model{ID: test.id, API: "google-generative-ai", Provider: "google", BaseURL: server.URL,
				Reasoning: true, ThinkingLevelMap: test.levels, Input: []InputType{InputText}, MaxTokens: 4096}
			result := CompleteSimple(model, Context{Messages: []Message{UserMessage{Content: "hello"}}}, SimpleStreamOptions{
				APIKey: "fixture", Reasoning: test.reasoning, ThinkingBudgets: test.budgets,
			})
			if result.StopReason != StopReasonStop {
				t.Fatalf("completion failed: %s", result.ErrorMessage)
			}
			var want map[string]any
			if err := json.Unmarshal([]byte(test.want), &want); err != nil {
				t.Fatal(err)
			}
			select {
			case got := <-requests:
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("thinkingConfig = %v, want %v", got, want)
				}
			default:
				t.Fatal("provider did not receive a request")
			}
		})
	}
}

func TestGoogleRESTRequestLayout(t *testing.T) {
	requests := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models/gemini-2.5-pro:streamGenerateContent" || r.URL.Query().Get("alt") != "sse" {
			t.Errorf("unexpected request URL: %s", r.URL)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests <- body
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}]}\n\n")
	}))
	defer server.Close()
	model := *GetModel("google", "gemini-2.5-pro")
	model.BaseURL = server.URL
	if got := ClampThinkingLevel(model, ModelThinkingLevelOff); got != ModelThinkingLevelMinimal {
		t.Errorf("built-in Pro Off capability = %s, want minimal", got)
	}
	temperature := 0.4
	result := Complete(model, Context{
		SystemPrompt: "Be concise.", Messages: []Message{UserMessage{Content: "hello"}},
		Tools: []Tool{{Name: "lookup", Description: "Look up a value", Parameters: map[string]any{"type": "object"}}},
	}, ProviderStreamOptions{APIKey: "fixture", Reasoning: "off", Temperature: &temperature, MaxTokens: 1024, ToolChoice: "required"})
	if result.StopReason != StopReasonStop {
		t.Fatalf("completion failed: %s", result.ErrorMessage)
	}
	wantJSON := `{
		"contents":[{"role":"user","parts":[{"text":"hello"}]}],
		"systemInstruction":{"role":"user","parts":[{"text":"Be concise."}]},
		"tools":[{"functionDeclarations":[{"name":"lookup","description":"Look up a value","parameters":{"type":"object"}}]}],
		"toolConfig":{"functionCallingConfig":{"mode":"ANY"}},
		"generationConfig":{"thinkingConfig":{"thinkingBudget":128},"temperature":0.4,"maxOutputTokens":1024}
	}`
	var want map[string]any
	if err := json.Unmarshal([]byte(wantJSON), &want); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-requests:
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("REST request = %v, want %v", got, want)
		}
	default:
		t.Fatal("provider did not receive a request")
	}
}

func TestGoogleInvalidThinkingMappingFailsBeforeRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"STOP\"}]}\n\n")
	}))
	defer server.Close()
	model := Model{ID: "gemini-3.7-flash", API: "google-generative-ai", Provider: "google", BaseURL: server.URL,
		Reasoning: true, ThinkingLevelMap: ThinkingLevelMap{ModelThinkingLevelHigh: "extreme"}}
	for _, simple := range []bool{false, true} {
		t.Run(fmt.Sprintf("simple=%v", simple), func(t *testing.T) {
			var stream *AssistantMessageEventStream
			if simple {
				stream = StreamSimple(model, Context{}, SimpleStreamOptions{APIKey: "fixture", Reasoning: ThinkingLevelHigh})
			} else {
				stream = Stream(model, Context{}, ProviderStreamOptions{APIKey: "fixture", Reasoning: ThinkingLevelHigh})
			}
			events := collectAssistantMessageEvents(stream.Events())
			result := stream.Result()
			if result.StopReason != StopReasonError || !strings.Contains(result.ErrorMessage, "high -> extreme") {
				t.Fatalf("expected explicit mapping error, got %+v", result)
			}
			if len(events) != 1 || events[0].Type != AssistantMessageEventError {
				t.Fatalf("expected one error event, got %+v", events)
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid configuration reached provider %d times", requests.Load())
	}
}
