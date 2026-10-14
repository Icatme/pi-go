package pigo

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestCommandCodeOctoberModelSupport(t *testing.T) {
	for _, test := range []struct {
		id                string
		vision, reasoning bool
		maxTokens         int
		effort            ModelThinkingLevel
		cost              UsageCost
	}{
		{"claude-sonnet-5-5", true, true, 65536, ModelThinkingLevelMax, UsageCost{Input: 2, Output: 10, CacheRead: .1, CacheWrite: 2.5}},
		{"claude-haiku-5-5", true, true, 65536, ModelThinkingLevelMax, UsageCost{Input: .1, Output: .5, CacheRead: .01, CacheWrite: .125}},
		{"gpt-6-luna", true, true, 65536, ModelThinkingLevelMax, UsageCost{Input: .1, Output: .5, CacheRead: .01, CacheWrite: .125}},
		{"gpt-6.1-sol", true, true, 65536, ModelThinkingLevelMax, UsageCost{Input: 2, Output: 10, CacheRead: .1, CacheWrite: 2.5}},
		{"deepseek/deepseek-v4.1-flash", true, true, 65536, ModelThinkingLevelLow, UsageCost{Input: .15, Output: .6, CacheRead: .003}},
		{"deepseek/deepseek-v4.1-flash-fast", true, true, 65536, ModelThinkingLevelMax, UsageCost{Input: .16, Output: .58, CacheRead: .016}},
		{"xiaomi/mimo-v2.6-pro", true, false, 65536, ModelThinkingLevelOff, UsageCost{Input: .435, Output: .87, CacheRead: .0036}},
		{"xiaomi/mimo-v2.6-pro-ultraspeed", true, false, 65536, ModelThinkingLevelOff, UsageCost{Input: 4.35, Output: 8.7, CacheRead: .036}},
		{"xiaomi/mimo-v2.6-flash", true, false, 65536, ModelThinkingLevelOff, UsageCost{Input: .14, Output: .28, CacheRead: .0028}},
		{"Qwen/Qwen3.8-Omni-Flash", true, true, 131072, ModelThinkingLevelXHigh, UsageCost{Input: .15, Output: .47, CacheRead: .016}},
		{"z-ai/glm-5.3-flashx", true, true, 131072, ModelThinkingLevelMax, UsageCost{Input: .37, Output: 1.25, CacheRead: .075}},
		{"mistral/mistral-large-4", true, true, 262144, ModelThinkingLevelHigh, UsageCost{Input: 1.36, Output: 4.18, CacheRead: .14}},
		{"stepfun/Step-5-Preview", true, true, 65536, ModelThinkingLevelHigh, UsageCost{Input: 1, Output: 2.7, CacheRead: .05}},
		{"stealth/glyph-cluster:free", false, true, 256000, ModelThinkingLevelXHigh, UsageCost{}},
		{"inclusionai/ling-3.1-flash:free", false, true, 32768, ModelThinkingLevelHigh, UsageCost{}},
		{"google/gemini-4-argon", true, true, 65536, ModelThinkingLevelHigh, UsageCost{Input: 2, Output: 10, CacheRead: .1}},
		{"meta/muse-wonder-1.0", true, true, 65536, ModelThinkingLevelXHigh, UsageCost{Input: 4, Output: 20, CacheRead: .2}},
	} {
		t.Run(test.id, func(t *testing.T) {
			model := newCommandCodeProviderModule().Models[test.id]
			if model.ID == "" {
				t.Fatalf("model missing from built-in catalog: %s", test.id)
			}
			if slices.Contains(model.Input, InputImage) != test.vision || model.Reasoning != test.reasoning || model.MaxTokens != test.maxTokens || model.Cost != test.cost {
				t.Fatalf("incorrect model capabilities/price: %+v", model)
			}
			if !slices.Contains(GetSupportedThinkingLevels(model), test.effort) {
				t.Fatalf("missing reasoning effort %s", test.effort)
			}
		})
	}
}

func TestCommandCodeResponsesWireAndToolReplay(t *testing.T) {
	for _, level := range []ThinkingLevel{ThinkingLevelHigh, "off"} {
		t.Run(string(level), func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path != "/provider/v1/responses" || r.Header.Get("Authorization") != "Bearer cc-key" {
					t.Errorf("incorrect request: %s %v", r.URL.Path, r.Header)
				}
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				input := commandCodeAnySlice(payload["input"])
				if len(input) == 0 || commandCodeRecord(input[0])["role"] != "system" {
					t.Errorf("Command Code must use the system role: %+v", input)
				}
				encodedInput, _ := json.Marshal(input)
				if !strings.Contains(string(encodedInput), `"type":"input_image"`) || !strings.Contains(string(encodedInput), `"image_url":"data:image/png;base64,aGVsbG8="`) {
					t.Errorf("image prompt not preserved: %s", encodedInput)
				}
				if level == "off" {
					if payload["reasoning"] != nil {
						t.Errorf("off enabled reasoning: %+v", payload["reasoning"])
					}
				} else if commandCodeRecord(payload["reasoning"])["effort"] != "high" {
					t.Errorf("reasoning effort lost: %+v", payload)
				}
				if payload["max_output_tokens"] != float64(4096) {
					t.Errorf("lost output limit: %+v", payload)
				}
				if requests == 2 {
					if !strings.Contains(string(encodedInput), `"type":"function_call_output"`) || !strings.Contains(string(encodedInput), `"output":"tool result"`) {
						t.Errorf("tool result not replayed: %s", encodedInput)
					}
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if requests == 1 {
					fmt.Fprint(w, buildOpenAICodexSSE(
						map[string]any{"type": "response.output_item.added", "output_index": 0, "item": map[string]any{"id": "rs_cc", "type": "reasoning", "summary": []any{}}},
						map[string]any{"type": "response.reasoning.delta", "output_index": 0, "delta": "check file"},
						map[string]any{"type": "response.output_item.done", "output_index": 0, "item": map[string]any{"id": "rs_cc", "type": "reasoning", "summary": []any{map[string]any{"type": "summary_text", "text": "check file"}}}},
						map[string]any{"type": "response.output_item.added", "output_index": 1, "item": map[string]any{"id": "fc_cc", "type": "function_call", "call_id": "call_cc", "name": "read", "arguments": ""}},
						map[string]any{"type": "response.function_call_arguments.delta", "output_index": 1, "item_id": "fc_cc", "delta": "{\"path\":\"a.go\"}"},
						map[string]any{"type": "response.output_item.done", "output_index": 1, "item": map[string]any{"id": "fc_cc", "type": "function_call", "call_id": "call_cc", "name": "read", "arguments": "{\"path\":\"a.go\"}"}},
						map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_cc", "status": "completed", "usage": map[string]any{"input_tokens": 100, "output_tokens": 5, "input_tokens_details": map[string]any{"cached_tokens": 80}}}},
					))
				} else {
					fmt.Fprint(w, buildOpenAICodexSSE(
						map[string]any{"type": "response.output_item.done", "item": map[string]any{"id": "msg_cc", "type": "message", "content": []any{map[string]any{"type": "output_text", "text": "done"}}}},
						map[string]any{"type": "response.completed", "response": map[string]any{"id": "resp_cc2", "status": "completed", "usage": map[string]any{"input_tokens": 10, "output_tokens": 1}}},
					))
				}
			}))
			defer server.Close()
			model := newCommandCodeProviderModule().Models["gpt-6-luna"]
			model.BaseURL = server.URL
			ctx := Context{SystemPrompt: "Follow instructions", Tools: []Tool{{Name: "read", Parameters: map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}}}}, Messages: []Message{UserMessage{Content: []ContentBlock{TextContent{Text: "inspect"}, ImageContent{Data: "aGVsbG8=", MIMEType: "image/png"}}}}}
			options := SimpleStreamOptions{APIKey: "cc-key", Reasoning: level, MaxTokens: 4096}
			stream := StreamSimple(model, ctx, options)
			events := collectCommandCodeEvents(stream)
			first := stream.Result()
			if first.StopReason != StopReasonToolUse || first.API != "openai-responses" || first.Usage.Input != 20 || first.Usage.CacheRead != 80 {
				t.Fatalf("incorrect first result: %+v", first)
			}
			thinkingDelta := false
			for _, event := range events {
				if event.Type == AssistantMessageEventThinkingDelta && event.Delta == "check file" {
					thinkingDelta = true
				}
			}
			if !thinkingDelta {
				t.Error("Command Code reasoning delta was dropped")
			}
			var calls []ToolCall
			for _, block := range first.Content {
				if call, ok := block.(ToolCall); ok {
					calls = append(calls, call)
				}
			}
			if len(calls) != 1 || calls[0].Arguments["path"] != "a.go" {
				t.Fatalf("tool call lost: %+v", calls)
			}
			ctx.Messages = append(ctx.Messages, first, ToolResultMessage{ToolCallID: calls[0].ID, ToolName: "read", Content: []ContentBlock{TextContent{Text: "tool result"}}})
			last := CompleteSimple(model, ctx, options)
			if last.StopReason != StopReasonStop || commandCodeTextContent(last.Content) != "done" || requests != 2 {
				t.Fatalf("tool continuation failed: %+v requests=%d", last, requests)
			}
		})
	}
}

func TestCommandCodeEndpointDiscoveryAndCache(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"object":"list","data":[
		{"id":"gpt-6-luna","name":"Luna","context_length":1050000,"supported_endpoints":["/chat/completions","/responses"]},
		{"id":"vendor/claude","name":"Claude","context_length":1000000,"supported_endpoints":["/messages"]},
		{"id":"chat-only","name":"Chat","context_length":32000,"supported_endpoints":["/chat/completions"]},
		{"id":"typesafe/jev","name":"Jev","context_length":32000,"supported_endpoints":["/systemone"]}]}`)
	}))
	defer server.Close()
	t.Setenv("COMMANDCODE_MODELS_URL", server.URL)
	models, err := FetchCommandCodeModels(context.Background(), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 3 {
		t.Fatalf("expected three chat models, got %d", len(models))
	}
	check := func(models []Model) {
		t.Helper()
		for i, want := range []API{"openai-responses", "anthropic-messages", "openai-completions"} {
			if got := commandCodeNativeModel(models[i]).API; got != want {
				t.Errorf("model %s selected %s, want %s", models[i].ID, got, want)
			}
		}
	}
	check(models)
	path := filepath.Join(t.TempDir(), "models.json")
	if err := writeCommandCodeModelsCache(path, models); err != nil {
		t.Fatal(err)
	}
	cached, err := readCommandCodeModelsCache(path)
	if err != nil {
		t.Fatal(err)
	}
	check(cached)
}

func TestCommandCodeNewModelCostTiers(t *testing.T) {
	for _, test := range []struct {
		id          string
		threshold   int
		base, above UsageCost
	}{
		{"claude-haiku-5-5", 100000, UsageCost{Input: .1, Output: .5, CacheRead: .01, CacheWrite: .125}, UsageCost{Input: .5, Output: 2.5, CacheRead: .05, CacheWrite: .625}},
		{"gpt-6-luna", 272000, UsageCost{Input: .1, Output: .5, CacheRead: .01, CacheWrite: .125}, UsageCost{Input: .2, Output: .75, CacheRead: .02, CacheWrite: .25}},
		{"gpt-6.1-sol", 272000, UsageCost{Input: 2, Output: 10, CacheRead: .1, CacheWrite: 2.5}, UsageCost{Input: 4, Output: 15, CacheRead: .2, CacheWrite: 5}},
		{"xai/grok-4.7", 200000, UsageCost{Input: 2, Output: 6, CacheRead: .5}, UsageCost{Input: 4, Output: 12, CacheRead: 1}},
	} {
		t.Run(test.id, func(t *testing.T) {
			model := newCommandCodeProviderModule().Models[test.id]
			for _, extra := range []int{0, 1} {
				usage := Usage{Input: test.threshold - 20, CacheRead: 10 + extra, CacheWrite: 10, Output: 1000}
				rates := test.base
				if extra == 1 {
					rates = test.above
				}
				got := CalculateCost(model, usage)
				want := UsageCost{Input: float64(usage.Input) * rates.Input / 1e6, Output: .001 * rates.Output, CacheRead: float64(usage.CacheRead) * rates.CacheRead / 1e6, CacheWrite: float64(usage.CacheWrite) * rates.CacheWrite / 1e6}
				if math.Abs(got.Input-want.Input) > 1e-12 || math.Abs(got.Output-want.Output) > 1e-12 || math.Abs(got.CacheRead-want.CacheRead) > 1e-12 || math.Abs(got.CacheWrite-want.CacheWrite) > 1e-12 {
					t.Fatalf("extra=%d got %+v want %+v", extra, got, want)
				}
			}
		})
	}
}

func TestCommandCodeDiscoveryRejectsUnusableEndpoints(t *testing.T) {
	for _, endpoints := range []string{`[]`, `["/systemone"]`, `["/future-api"]`, `"/responses"`, `[42]`} {
		t.Run(endpoints, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprintf(w, `{"object":"list","data":[{"id":"new","name":"New","context_length":1000,"supported_endpoints":%s}]}`, endpoints)
			}))
			defer server.Close()
			t.Setenv("COMMANDCODE_MODELS_URL", server.URL)
			if _, err := FetchCommandCodeModels(context.Background(), server.Client()); err == nil {
				t.Fatal("unusable catalog accepted")
			}
		})
	}
}

func TestCommandCodeProtocolSelectionCloneAndInvalidAPI(t *testing.T) {
	model := newCommandCodeProviderModule().Models["gpt-6-luna"]
	cloned := cloneModel(model)
	cloned.Compat.(*CommandCodeCompat).API = "anthropic-messages"
	if commandCodeNativeModel(model).API != "openai-responses" {
		t.Fatal("cloned model mutated original protocol")
	}
	cloned.Compat.(*CommandCodeCompat).API = "invalid"
	requests := 0
	client := &http.Client{Transport: commandCodeRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		return nil, fmt.Errorf("unexpected request")
	})}
	result := CompleteSimple(cloned, Context{}, SimpleStreamOptions{APIKey: "key", HTTPClient: client})
	if requests != 0 || result.StopReason != StopReasonError || !strings.Contains(result.ErrorMessage, "unsupported Command Code provider API") {
		t.Fatalf("invalid protocol reached network: %+v", result)
	}
}

func TestCommandCodeReasoningDeltaIsProviderScoped(t *testing.T) {
	for _, provider := range []Provider{"commandcode", "openai"} {
		for _, delta := range []string{`"thinking"`, `42`} {
			model := Model{Provider: provider, API: "openai-responses"}
			response := AssistantMessage{}
			stream := newAssistantMessageEventStream()
			state := &openAIResponsesStreamingState{CurrentTextIndex: -1, CurrentThinkingIndex: -1}
			_, err := processOpenAIResponsesStreamEvent(fmt.Sprintf(`{"type":"response.reasoning.delta","output_index":0,"delta":%s}`, delta), model, &response, stream, state, "")
			if provider == "commandcode" && delta == `42` {
				if err == nil {
					t.Fatal("malformed reasoning delta silently accepted")
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if provider == "openai" && len(response.Content) != 0 {
				t.Fatal("Command Code event affected unrelated provider")
			}
		}
	}
}

func TestCommandCodeResponsesOmitsUnsupportedReasoning(t *testing.T) {
	for _, test := range []struct {
		name, id, summary string
		level             ThinkingLevel
	}{
		{"trimmed off", "gpt-6-luna", "auto", " off "},
		{"no reasoning", "xiaomi/mimo-v2.6-pro", "auto", ThinkingLevelHigh},
		{"no selectable effort", "moonshotai/Kimi-K2.7-Code", "", ThinkingLevelHigh},
		{"summary without selectable effort", "moonshotai/Kimi-K2.7-Code", "auto", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					t.Error(err)
				}
				if r.URL.Path != "/provider/v1/responses" {
					t.Errorf("unexpected path: %s", r.URL.Path)
				}
				if payload["reasoning"] != nil {
					t.Errorf("unsupported reasoning sent to Responses: %+v", payload["reasoning"])
				}
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, buildOpenAICodexSSE(map[string]any{"type": "response.completed", "response": map[string]any{"status": "completed"}}))
			}))
			defer server.Close()
			model := newCommandCodeProviderModule().Models[test.id]
			model.BaseURL = server.URL
			result := Complete(model, Context{}, ProviderStreamOptions{APIKey: "key", Reasoning: test.level, ReasoningSummary: test.summary})
			if result.StopReason != StopReasonStop {
				t.Fatalf("request failed: %+v", result)
			}
		})
	}
}

func TestCommandCodeReasoningDeltaRejectsInvalidIndex(t *testing.T) {
	for _, index := range []string{`null`, `"0"`, `-1`, `0.5`} {
		t.Run(index, func(t *testing.T) {
			model := Model{Provider: "commandcode", API: "openai-responses"}
			response := AssistantMessage{}
			stream := newAssistantMessageEventStream()
			state := &openAIResponsesStreamingState{CurrentTextIndex: -1, CurrentThinkingIndex: -1}
			_, err := processOpenAIResponsesStreamEvent(fmt.Sprintf(`{"type":"response.reasoning.delta","output_index":%s,"delta":"thinking"}`, index), model, &response, stream, state, "")
			if err == nil || len(response.Content) != 0 {
				t.Fatalf("malformed reasoning event accepted: response=%+v err=%v", response, err)
			}
		})
	}
}
