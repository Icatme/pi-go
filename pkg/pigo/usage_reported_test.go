package pigo

import (
	"encoding/json"
	"testing"
)

func TestUsageWritersReportExplicitZero(t *testing.T) {
	tests := []struct {
		name  string
		apply func(*AssistantMessage)
	}{
		{name: "anthropic", apply: func(response *AssistantMessage) { applyAnthropicUsage(response, Model{}, anthropicUsage{}) }},
		{name: "commandcode", apply: func(response *AssistantMessage) {
			applyCommandCodeUsage(map[string]any{"totalUsage": map[string]any{}}, Model{}, response)
		}},
		{name: "deepseek", apply: func(response *AssistantMessage) { applyDeepSeekUsage(response, Model{}, deepSeekChatUsage{}) }},
		{name: "google", apply: func(response *AssistantMessage) { applyGoogleUsage(response, Model{}, googleUsageMetadata{}) }},
		{name: "mistral", apply: func(response *AssistantMessage) { applyMistralUsage(response, Model{}, mistralChatUsage{}) }},
		{name: "openai completions", apply: func(response *AssistantMessage) {
			applyOpenAICompletionsUsage(response, Model{}, openAICompletionsUsage{})
		}},
		{name: "openai responses", apply: func(response *AssistantMessage) {
			applyOpenAIResponsesUsage(Model{}, response, openAIResponsesUsage{}, "", "")
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			response := AssistantMessage{}
			test.apply(&response)
			if !response.UsageReported {
				t.Fatal("explicit zero usage must be reported")
			}
			if response.Usage != (Usage{}) {
				t.Fatalf("explicit zero usage changed token or cost values: %+v", response.Usage)
			}
		})
	}
}

func TestProviderUsageJSONDistinguishesMissingFromExplicitZero(t *testing.T) {
	tests := []struct {
		name         string
		explicitJSON string
		missingJSON  string
		hasUsage     func([]byte) (bool, error)
	}{
		{
			name:         "anthropic message",
			explicitJSON: `{"usage":{}}`,
			missingJSON:  `{}`,
			hasUsage: func(data []byte) (bool, error) {
				var value anthropicResponse
				err := json.Unmarshal(data, &value)
				return value.Usage != nil, err
			},
		},
		{
			name:         "deepseek chunk",
			explicitJSON: `{"usage":{}}`,
			missingJSON:  `{}`,
			hasUsage: func(data []byte) (bool, error) {
				var value deepSeekChatChunk
				err := json.Unmarshal(data, &value)
				return value.Usage != nil, err
			},
		},
		{
			name:         "google chunk",
			explicitJSON: `{"usageMetadata":{}}`,
			missingJSON:  `{}`,
			hasUsage: func(data []byte) (bool, error) {
				var value googleStreamChunk
				err := json.Unmarshal(data, &value)
				return value.UsageMetadata != nil, err
			},
		},
		{
			name:         "mistral chunk",
			explicitJSON: `{"usage":{}}`,
			missingJSON:  `{}`,
			hasUsage: func(data []byte) (bool, error) {
				var value mistralChatChunk
				err := json.Unmarshal(data, &value)
				return value.Usage != nil, err
			},
		},
		{
			name:         "openai completions chunk",
			explicitJSON: `{"usage":{}}`,
			missingJSON:  `{}`,
			hasUsage: func(data []byte) (bool, error) {
				var value openAICompletionsChunk
				err := json.Unmarshal(data, &value)
				return value.Usage != nil, err
			},
		},
		{
			name:         "openai completions choice",
			explicitJSON: `{"choices":[{"usage":{}}]}`,
			missingJSON:  `{"choices":[{}]}`,
			hasUsage: func(data []byte) (bool, error) {
				var value openAICompletionsChunk
				err := json.Unmarshal(data, &value)
				return len(value.Choices) == 1 && value.Choices[0].Usage != nil, err
			},
		},
		{
			name:         "openai responses terminal",
			explicitJSON: `{"usage":{}}`,
			missingJSON:  `{}`,
			hasUsage: func(data []byte) (bool, error) {
				var value openAIResponsesResponse
				err := json.Unmarshal(data, &value)
				return value.Usage != nil, err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			explicit, err := test.hasUsage([]byte(test.explicitJSON))
			if err != nil {
				t.Fatalf("decode explicit zero usage: %v", err)
			}
			if !explicit {
				t.Fatal("explicit zero usage object was treated as missing")
			}

			missing, err := test.hasUsage([]byte(test.missingJSON))
			if err != nil {
				t.Fatalf("decode missing usage: %v", err)
			}
			if missing {
				t.Fatal("missing usage was treated as explicit zero")
			}
		})
	}
}

func TestCommandCodeUsagePresenceAcceptsEmptyObjectsOnly(t *testing.T) {
	for _, value := range []any{map[string]any{}, `{}`} {
		_, ok := commandCodeUsageRecord(value)
		if !ok {
			t.Fatalf("explicit empty usage object was treated as missing: %#v", value)
		}
	}
	for _, value := range []any{nil, "", "null", "not-json"} {
		if _, ok := commandCodeUsageRecord(value); ok {
			t.Fatalf("invalid or missing usage was treated as reported: %#v", value)
		}
	}
}

func TestOpenAIResponsesFailedEventReportsExplicitZeroUsage(t *testing.T) {
	response := AssistantMessage{}
	state := openAIResponsesStreamingState{CurrentTextIndex: -1, CurrentThinkingIndex: -1, CurrentToolIndex: -1}
	done, err := processOpenAIResponsesStreamEvent(`{
		"type":"response.failed",
		"response":{
			"status":"failed",
			"usage":{},
			"error":{"message":"failed after submission"}
		}
	}`, Model{}, &response, nil, &state, "")
	if !done || err == nil {
		t.Fatalf("expected terminal failure, done=%t err=%v", done, err)
	}
	if !response.UsageReported || response.Usage != (Usage{}) {
		t.Fatalf("failed response lost explicit zero usage: %+v", response)
	}
}

func TestOpenAIResponsesTerminalUsagePresenceReachesDoneAndResult(t *testing.T) {
	for _, test := range []struct {
		name         string
		usage        any
		wantReported bool
	}{
		{name: "explicit zero", usage: map[string]any{}, wantReported: true},
		{name: "missing", usage: nil, wantReported: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			terminal := map[string]any{"id": "resp_usage_presence", "status": "completed"}
			if test.usage != nil {
				terminal["usage"] = test.usage
			}
			payload, err := json.Marshal(map[string]any{"type": "response.completed", "response": terminal})
			if err != nil {
				t.Fatal(err)
			}

			response := AssistantMessage{}
			state := openAIResponsesStreamingState{CurrentTextIndex: -1, CurrentThinkingIndex: -1, CurrentToolIndex: -1}
			stream := newAssistantMessageEventStream()
			done, err := processOpenAIResponsesStreamEvent(string(payload), Model{}, &response, stream, &state, "")
			if err != nil || !done {
				t.Fatalf("process terminal event: done=%t err=%v", done, err)
			}

			var doneMessage AssistantMessage
			for event := range stream.Events() {
				if event.Type == AssistantMessageEventDone {
					doneMessage = event.Message
				}
			}
			result := stream.Result()
			if response.UsageReported != test.wantReported || doneMessage.UsageReported != test.wantReported || result.UsageReported != test.wantReported {
				t.Fatalf("usage presence mismatch: response=%t done=%t result=%t", response.UsageReported, doneMessage.UsageReported, result.UsageReported)
			}
			if response.Usage != (Usage{}) || doneMessage.Usage != (Usage{}) || result.Usage != (Usage{}) {
				t.Fatalf("zero usage changed during terminal delivery: response=%+v done=%+v result=%+v", response.Usage, doneMessage.Usage, result.Usage)
			}
		})
	}
}
