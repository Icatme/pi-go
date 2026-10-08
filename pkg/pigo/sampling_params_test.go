package pigo

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

// Exercise both public entry points through payload construction, without an
// external request. The hook's unsupported return value stops serialization.
func captureSamplingPayload(t *testing.T, model Model, options ProviderStreamOptions, simple bool) map[string]any {
	t.Helper()
	options.APIKey = "test"
	var payload map[string]any
	options.OnPayload = func(value any, _ Model) any {
		data, err := json.Marshal(value)
		if err != nil {
			t.Errorf("marshal request: %v", err)
		} else if err := json.Unmarshal(data, &payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		return make(chan int)
	}
	var result AssistantMessage
	if simple {
		result = CompleteSimple(model, Context{}, SimpleStreamOptions{
			APIKey: "test", Reasoning: options.Reasoning, Temperature: options.Temperature,
			TopP: options.TopP, SamplingParams: options.SamplingParams, OnPayload: options.OnPayload,
		})
	} else {
		result = Complete(model, Context{}, options)
	}
	if payload == nil {
		t.Fatalf("request never reached payload hook: %s", result.ErrorMessage)
	}
	return payload
}

func TestSamplingParamsPayloadPrecedence(t *testing.T) {
	for _, api := range []API{"openai-completions", "openai-responses"} {
		for _, simple := range []bool{false, true} {
			name := string(api) + "/direct"
			if simple {
				name = string(api) + "/simple"
			}
			t.Run(name, func(t *testing.T) {
				model := *GetModel("openai", "gpt-5.6-sol")
				model.API = api
				model.Provider = "openai"
				model.ThinkingLevelMap = ThinkingLevelMap{ModelThinkingLevelLow: "", ModelThinkingLevelMedium: ""}
				model.SamplingParams = SamplingParams{"temperature": 1.0, "top_p": .95, "min_p": .1}
				model.SamplingParamsByThinkingLevel = SamplingParamsByThinkingLevel{
					ModelThinkingLevelHigh: {"temperature": .8, "top_k": 64},
				}
				zero := 0.0
				request := SamplingParams{"top_p": .5, "min_p": 0}
				payload := captureSamplingPayload(t, model, ProviderStreamOptions{
					Reasoning: ThinkingLevelLow, Temperature: &zero, SamplingParams: request,
				}, simple)
				for key, want := range map[string]float64{"temperature": .8, "top_p": .5, "top_k": 64, "min_p": 0} {
					if payload[key] != want {
						t.Errorf("%s = %#v, want %v", key, payload[key], want)
					}
				}
				if request["min_p"] != 0 || model.SamplingParams["temperature"] != 1.0 {
					t.Fatal("request construction mutated sampling inputs")
				}
			})
		}
	}
}

func TestSamplingParamsOffAndOmitted(t *testing.T) {
	for _, api := range []API{"openai-completions", "openai-responses"} {
		t.Run(string(api), func(t *testing.T) {
			model := *GetModel("openai", "gpt-5.6-sol")
			model.API = api
			model.Reasoning = false
			payload := captureSamplingPayload(t, model, ProviderStreamOptions{}, false)
			for _, key := range []string{"temperature", "top_p", "top_k", "min_p"} {
				if _, ok := payload[key]; ok {
					t.Errorf("unset %s was serialized", key)
				}
			}
			model.SamplingParamsByThinkingLevel = SamplingParamsByThinkingLevel{ModelThinkingLevelOff: {"temperature": .7}}
			payload = captureSamplingPayload(t, model, ProviderStreamOptions{Reasoning: ThinkingLevelHigh}, false)
			if payload["temperature"] != .7 {
				t.Fatalf("non-reasoning model did not select off sampling: %v", payload)
			}
		})
	}
}

func TestResponsesSamplingUsesLogicalLevelAndSummaryDefault(t *testing.T) {
	model := *GetModel("openai", "gpt-5.6-sol")
	model.ThinkingLevelMap[ModelThinkingLevelHigh] = "medium"
	model.SamplingParamsByThinkingLevel = SamplingParamsByThinkingLevel{
		ModelThinkingLevelOff:    {"temperature": .1},
		ModelThinkingLevelMedium: {"temperature": .6},
		ModelThinkingLevelHigh:   {"temperature": .8},
	}
	for _, test := range []struct {
		name        string
		options     ProviderStreamOptions
		effort      string
		temperature float64
	}{
		{"mapped", ProviderStreamOptions{Reasoning: ThinkingLevelHigh}, "medium", .8},
		{"summary-only", ProviderStreamOptions{ReasoningSummary: "auto"}, "medium", .6},
		{"explicit-off", ProviderStreamOptions{Reasoning: "off", ReasoningSummary: "auto"}, "none", .1},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := captureSamplingPayload(t, model, test.options, false)
			reasoning, _ := payload["reasoning"].(map[string]any)
			if reasoning["effort"] != test.effort || payload["temperature"] != test.temperature {
				t.Fatalf("wrong reasoning/sampling pair: %v", payload)
			}
		})
	}
}

func TestSamplingParamsDefaultsAndRequestOverrides(t *testing.T) {
	for _, api := range []API{"openai-completions", "openai-responses"} {
		for _, simple := range []bool{false, true} {
			name := string(api) + "/direct"
			if simple {
				name = string(api) + "/simple"
			}
			t.Run(name, func(t *testing.T) {
				model := *GetModel("openai", "gpt-5.6-sol")
				model.API = api
				model.SamplingParams = SamplingParams{"temperature": .4, "top_k": 32}
				payload := captureSamplingPayload(t, model, ProviderStreamOptions{}, simple)
				if payload["temperature"] != .4 || payload["top_k"] != 32.0 {
					t.Fatalf("model defaults missing: %v", payload)
				}
				invalid := 3.0
				payload = captureSamplingPayload(t, model, ProviderStreamOptions{
					Temperature: &invalid, SamplingParams: SamplingParams{"temperature": 0, "top_p": .3},
				}, simple)
				if payload["temperature"] != 0.0 || payload["top_p"] != .3 || payload["top_k"] != 32.0 {
					t.Fatalf("request must override defaults and named fields before validation: %v", payload)
				}
			})
		}
	}
}

func TestResponsesSamplingReusedOptionsFollowNewLevel(t *testing.T) {
	model := *GetModel("openai", "gpt-5.6-sol")
	model.ThinkingLevelMap[ModelThinkingLevelHigh] = "medium"
	model.SamplingParamsByThinkingLevel = SamplingParamsByThinkingLevel{
		ModelThinkingLevelHigh: {"temperature": .8},
		ModelThinkingLevelLow:  {"temperature": .2},
	}
	options := NormalizeProviderStreamOptions(model, ProviderStreamOptions{Reasoning: ThinkingLevelHigh})
	options.Reasoning = ThinkingLevelLow
	payload := captureSamplingPayload(t, model, options, false)
	if payload["temperature"] != .2 || payload["reasoning"].(map[string]any)["effort"] != "low" {
		t.Fatalf("normalized options retained stale thinking/sampling state: %v", payload)
	}
}

func TestResponsesSamplingParamsCannotBypassValidation(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); w.WriteHeader(500) }))
	defer server.Close()
	for _, test := range []struct {
		name   string
		params SamplingParams
		reason ThinkingLevel
		want   string
	}{
		{"unsupported-level", SamplingParams{"top_p": .3}, ThinkingLevelHigh, errOpenAIResponsesSamplingReasoning.Error()},
		{"range", SamplingParams{"temperature": 3}, "off", errOpenAIResponsesSamplingInvalid.Error()},
		{"type", SamplingParams{"top_p": "0.3"}, "off", errOpenAIResponsesSamplingInvalid.Error()},
	} {
		t.Run(test.name, func(t *testing.T) {
			model := *GetModel("openai", "gpt-5.4")
			model.BaseURL = server.URL
			result := Complete(model, Context{}, ProviderStreamOptions{APIKey: "test", Reasoning: test.reason, SamplingParams: test.params})
			if result.StopReason != StopReasonError || !strings.Contains(result.ErrorMessage, test.want) {
				t.Fatalf("sampling preflight = %+v, want %q", result, test.want)
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("invalid sampling sent %d requests", requests.Load())
	}
}

func TestSamplingParamsIgnoredOutsideCompatibleAPIs(t *testing.T) {
	model := Model{ID: "custom-model", Provider: "anthropic", API: "anthropic-messages", MaxTokens: 1024}
	model.SamplingParams = SamplingParams{"top_p": .8}
	model.SamplingParamsByThinkingLevel = SamplingParamsByThinkingLevel{ModelThinkingLevelOff: {"top_k": 40}}
	payload := captureSamplingPayload(t, model, ProviderStreamOptions{SamplingParams: SamplingParams{"min_p": .2}}, false)
	for _, key := range []string{"top_p", "top_k", "min_p"} {
		if _, ok := payload[key]; ok {
			t.Errorf("Anthropic serialized %s", key)
		}
	}
}

func TestSamplingParamsClonesAreIsolated(t *testing.T) {
	model := Model{
		SamplingParams:                SamplingParams{"nested": map[string]any{"values": []any{1}}},
		SamplingParamsByThinkingLevel: SamplingParamsByThinkingLevel{ModelThinkingLevelHigh: {"top_k": 4}},
	}
	cloned := cloneModel(model)
	cloned.SamplingParams["nested"].(map[string]any)["values"].([]any)[0] = 2
	cloned.SamplingParamsByThinkingLevel[ModelThinkingLevelHigh]["top_k"] = 9
	if !reflect.DeepEqual(model.SamplingParams["nested"], map[string]any{"values": []any{1}}) || model.SamplingParamsByThinkingLevel[ModelThinkingLevelHigh]["top_k"] != 4 {
		t.Fatal("model clone aliases sampling configuration")
	}
	options := NewStreamOptions(func(o *StreamOptions) { o.SamplingParams = model.SamplingParams })
	provider := options.providerStreamOptions(Model{})
	provider.SamplingParams["nested"].(map[string]any)["values"].([]any)[0] = 3
	if !reflect.DeepEqual(options.SamplingParams, model.SamplingParams) {
		t.Fatal("option conversion aliases sampling configuration")
	}
}

func TestSamplingParamsOverrideNonFiniteNamedValues(t *testing.T) {
	for _, api := range []API{"openai-completions", "openai-responses"} {
		t.Run(string(api), func(t *testing.T) {
			model := *GetModel("openai", "gpt-5.6-sol")
			model.API = api
			invalid := math.NaN()
			payload := captureSamplingPayload(t, model, ProviderStreamOptions{
				Temperature: &invalid, TopP: &invalid,
				SamplingParams: SamplingParams{"temperature": .4, "top_p": nil},
			}, false)
			if payload["temperature"] != .4 {
				t.Fatalf("valid override was lost: %v", payload)
			}
			if value, exists := payload["top_p"]; !exists || value != nil {
				t.Fatalf("explicit null override was lost: %v", payload)
			}
		})
	}
}

func TestResponsesSamplingTrimsReasoningLikeWireEffort(t *testing.T) {
	model := *GetModel("openai", "gpt-5.6-sol")
	model.SamplingParamsByThinkingLevel = SamplingParamsByThinkingLevel{
		ModelThinkingLevelHigh: {"temperature": .8},
		ModelThinkingLevelOff:  {"temperature": .1},
	}
	payload := captureSamplingPayload(t, model, ProviderStreamOptions{Reasoning: " high "}, false)
	if payload["temperature"] != .8 || payload["reasoning"].(map[string]any)["effort"] != "high" {
		t.Fatalf("wire effort and sampling level disagree: %v", payload)
	}
}

func TestSamplingParamsCannotReplaceProtocolFields(t *testing.T) {
	for _, api := range []API{"openai-completions", "openai-responses"} {
		t.Run(string(api), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(400)
			}))
			defer server.Close()
			model := *GetModel("openai", "gpt-5.4")
			model.API, model.BaseURL = api, server.URL
			for _, test := range []struct {
				key   string
				value any
			}{
				{"reasoning", map[string]any{"effort": "high"}},
				{"model", "other-model"},
				{"tools", []any{}},
				{"stream", false},
			} {
				result := Complete(model, Context{}, ProviderStreamOptions{
					APIKey: "test", Reasoning: "off", SamplingParams: SamplingParams{test.key: test.value, "top_p": .3},
				})
				if result.StopReason != StopReasonError || !strings.Contains(result.ErrorMessage, "sampling parameters cannot override") {
					t.Errorf("%s was not rejected as a protocol override: %+v", test.key, result)
				}
			}
			if requests.Load() != 0 {
				t.Errorf("protocol overrides reached HTTP: %d", requests.Load())
			}
		})
	}
}
