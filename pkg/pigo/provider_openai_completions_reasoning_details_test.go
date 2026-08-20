package pigo

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOpenAICompletionsPreservesStreamingReasoningDetailsInOrder(t *testing.T) {
	model := Model{API: "openai-completions", Provider: "openrouter", ID: "test-model"}
	response := AssistantMessage{}
	stream := newAssistantMessageEventStream()
	state := &openAICompletionsStreamState{ToolCalls: map[int]*openAICompletionsToolCallState{}}

	done, err := processOpenAICompletionsStreamEvent(`{
		"choices":[{"delta":{"reasoning_details":[
			{"type":"reasoning.summary","summary":"first","index":9007199254740993},
			{"type":"reasoning.unknown","text":"ignored"},
			{"type":"reasoning.text","text":"also ignored","format":null}
		]}}]
	}`, model, &response, stream, state)
	if err != nil || done {
		t.Fatalf("process first reasoning details chunk: done=%v err=%v", done, err)
	}
	if len(response.Content) != 1 {
		t.Fatalf("reasoning details without reasoning text must create a thinking block: %#v", response.Content)
	}
	thinking, ok := response.Content[0].(ThinkingContent)
	if !ok || thinking.Thinking != "" {
		t.Fatalf("expected empty thinking block carrying the replay signature, got %#v", response.Content[0])
	}
	if thinking.ThinkingSignature != `[{"type":"reasoning.summary","summary":"first","index":9007199254740993}]` {
		t.Fatalf("unexpected initial reasoning details signature: %s", thinking.ThinkingSignature)
	}

	done, err = processOpenAICompletionsStreamEvent(`{
		"choices":[{"delta":{
			"reasoning_content":"plan",
			"reasoning_details":[
				{"type":"reasoning.encrypted","id":null,"data":"cipher"},
				{"type":"reasoning.text","text":"second","signature":null}
			]
		},"finish_reason":"stop"}]
	}`, model, &response, stream, state)
	if err != nil || done {
		t.Fatalf("process second reasoning details chunk: done=%v err=%v", done, err)
	}
	thinking, ok = response.Content[0].(ThinkingContent)
	if !ok || thinking.Thinking != "plan" {
		t.Fatalf("expected reasoning text and structured details on the same block, got %#v", response.Content[0])
	}
	var preserved []map[string]any
	if err := json.Unmarshal([]byte(thinking.ThinkingSignature), &preserved); err != nil {
		t.Fatalf("decode preserved reasoning details: %v", err)
	}
	if len(preserved) != 3 || preserved[0]["type"] != "reasoning.summary" || preserved[1]["type"] != "reasoning.encrypted" || preserved[2]["type"] != "reasoning.text" {
		t.Fatalf("reasoning details order or filtering changed: %#v", preserved)
	}
}

func TestOpenAICompletionsIgnoresNonArrayReasoningDetails(t *testing.T) {
	model := Model{API: "openai-completions", Provider: "openrouter", ID: "test-model"}
	response := AssistantMessage{}
	stream := newAssistantMessageEventStream()
	state := &openAICompletionsStreamState{ToolCalls: map[int]*openAICompletionsToolCallState{}}

	_, err := processOpenAICompletionsStreamEvent(`{
		"choices":[{"delta":{"content":"ok","reasoning_details":{"type":"reasoning.text","text":"ignored"}},"finish_reason":"stop"}]
	}`, model, &response, stream, state)
	if err != nil {
		t.Fatalf("non-array reasoning_details must be ignored without rejecting the chunk: %v", err)
	}
	if len(response.Content) != 1 {
		t.Fatalf("expected only the text block, got %#v", response.Content)
	}
	if text, ok := response.Content[0].(TextContent); !ok || text.Text != "ok" {
		t.Fatalf("unexpected response content: %#v", response.Content[0])
	}
}

func TestOpenAICompletionsReplaysValidReasoningDetailsInsteadOfRawReasoning(t *testing.T) {
	signature := `[{"type":"reasoning.summary","summary":"first","index":9007199254740993},{"type":"reasoning.encrypted","id":"call_1","data":"cipher"},{"type":"reasoning.text","text":"second","signature":"sig"}]`
	converted, ok := openAICompletionsAssistantMessage(Model{Provider: "openrouter"}, AssistantMessage{Content: []ContentBlock{
		ThinkingContent{Thinking: "private plan", ThinkingSignature: signature},
		ToolCall{ID: "call_1", Name: "lookup", Arguments: map[string]any{"q": "x"}},
	}}, resolvedOpenAICompletionsCompat{})
	if !ok {
		t.Fatal("expected assistant message with tool call to be replayed")
	}
	if converted.ReasoningContent != nil || converted.Reasoning != "" || converted.ReasoningText != "" {
		t.Fatalf("structured reasoning details must replace raw reasoning fields: %#v", converted)
	}
	if len(converted.ReasoningDetails) != 3 {
		t.Fatalf("expected complete reasoning details sequence, got %#v", converted.ReasoningDetails)
	}
	payload, err := json.Marshal(converted)
	if err != nil {
		t.Fatalf("marshal replay message: %v", err)
	}
	if !strings.Contains(string(payload), `"index":9007199254740993`) {
		t.Fatalf("reasoning detail number was not preserved in replay payload: %s", payload)
	}
}

func TestOpenAICompletionsInvalidReasoningDetailsFallBackToExistingReasoningField(t *testing.T) {
	tests := []string{
		`not-json`,
		`[]`,
		`[{"type":"reasoning.unknown","text":"no"}]`,
		`[{"type":"reasoning.summary","summary":"ok"},{"type":"reasoning.text","text":null}]`,
		`[{"type":"reasoning.summary","summary":"ok","index":"1"}]`,
	}
	for _, signature := range tests {
		t.Run(signature, func(t *testing.T) {
			converted, ok := openAICompletionsAssistantMessage(Model{Provider: "openrouter"}, AssistantMessage{Content: []ContentBlock{
				ThinkingContent{Thinking: "fallback", ThinkingSignature: signature},
			}}, resolvedOpenAICompletionsCompat{})
			if !ok {
				t.Fatal("expected raw reasoning fallback message")
			}
			if len(converted.ReasoningDetails) != 0 {
				t.Fatalf("invalid reasoning details must not be replayed: %#v", converted.ReasoningDetails)
			}
			if converted.ReasoningContent == nil || *converted.ReasoningContent != "fallback" {
				t.Fatalf("expected existing reasoning_content fallback, got %#v", converted)
			}
		})
	}
}

func TestOpenAICompletionsUsageUsesUpstreamCacheFieldPrecedence(t *testing.T) {
	tests := []struct {
		name      string
		payload   string
		wantInput int
		wantCache int
		wantWrite int
		wantTotal int
	}{
		{
			name:      "prompt details wins over larger aliases",
			payload:   `{"prompt_tokens":20,"completion_tokens":3,"cached_tokens":12,"prompt_cache_hit_tokens":8,"cache_read_input_tokens":10,"prompt_tokens_details":{"cached_tokens":9}}`,
			wantInput: 11,
			wantCache: 9,
			wantTotal: 23,
		},
		{
			name:      "explicit zero prompt details wins",
			payload:   `{"prompt_tokens":20,"completion_tokens":3,"cached_tokens":12,"prompt_cache_hit_tokens":8,"prompt_tokens_details":{"cached_tokens":0}}`,
			wantInput: 20,
			wantCache: 0,
			wantTotal: 23,
		},
		{
			name:      "prompt cache hit wins over top level",
			payload:   `{"prompt_tokens":20,"completion_tokens":3,"cached_tokens":12,"prompt_cache_hit_tokens":8}`,
			wantInput: 12,
			wantCache: 8,
			wantTotal: 23,
		},
		{
			name:      "documented cache write wins over aliases",
			payload:   `{"prompt_tokens":20,"completion_tokens":3,"cache_creation_input_tokens":8,"prompt_tokens_details":{"cached_tokens":4,"cache_write_tokens":2,"cache_creation_tokens":6}}`,
			wantInput: 14,
			wantCache: 4,
			wantWrite: 2,
			wantTotal: 23,
		},
		{
			name:      "cache read clamps input at zero",
			payload:   `{"prompt_tokens":20,"completion_tokens":3,"cached_tokens":25}`,
			wantInput: 0,
			wantCache: 25,
			wantTotal: 28,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var usage openAICompletionsUsage
			if err := json.Unmarshal([]byte(test.payload), &usage); err != nil {
				t.Fatalf("decode usage: %v", err)
			}
			response := AssistantMessage{}
			applyOpenAICompletionsUsage(&response, Model{}, usage)
			if response.Usage.Input != test.wantInput || response.Usage.CacheRead != test.wantCache || response.Usage.CacheWrite != test.wantWrite || response.Usage.Output != 3 || response.Usage.TotalTokens != test.wantTotal {
				t.Fatalf("unexpected usage: %+v", response.Usage)
			}
			if response.Usage.TotalTokens != response.Usage.Input+response.Usage.Output+response.Usage.CacheRead+response.Usage.CacheWrite {
				t.Fatalf("total tokens must equal normalized components: %+v", response.Usage)
			}
		})
	}
}
