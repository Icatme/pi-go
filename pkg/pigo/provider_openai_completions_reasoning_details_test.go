package pigo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
	if thinking.ThinkingSignature != "" {
		t.Fatalf("replay metadata must not be serialized during deltas: %s", thinking.ThinkingSignature)
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
	finalizeOpenAICompletionsResponse(&response, stream, state)
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

func TestOpenAICompletionsMergesConsecutiveReasoningDetails(t *testing.T) {
	response := AssistantMessage{StopReason: StopReasonStop}
	stream := newAssistantMessageEventStream()
	state := &openAICompletionsStreamState{}
	for _, raw := range []string{
		`[{"type":"reasoning.text","text":"first ","id":null,"format":"","signature":null,"opaque":{"n":9007199254740993}}]`,
		`[{"type":"reasoning.text","text":"second","id":"reason_1","format":"provider.v1","index":9007199254740993,"signature":"sig1"}]`,
		`[{"type":"reasoning.text","text":" third","id":"ignored","format":"ignored","index":2,"signature":"ignored"}]`,
		`[{"type":"reasoning.summary","summary":"sum","id":""},{"type":"reasoning.summary","summary":"mary","id":"keep-empty","index":0}]`,
		`[{"type":"reasoning.encrypted","data":"cipher1","index":9007199254740993,"opaque":{"n":9007199254740995}},{"type":"reasoning.encrypted","data":"cipher2"}]`,
		`[{"type":"reasoning.text","text":"separate"}]`,
	} {
		appendOpenAICompletionsReasoningDetails(&response, stream, state, json.RawMessage(raw))
		if response.Content[0].(ThinkingContent).ThinkingSignature != "" {
			t.Fatal("delta serialized replay metadata")
		}
	}
	finalizeOpenAICompletionsResponse(&response, stream, state)
	signature := response.Content[0].(ThinkingContent).ThinkingSignature
	details := parseOpenAICompletionsReasoningDetails(signature)
	if len(details) != 5 {
		t.Fatalf("expected merged text/summary and discrete encrypted entries, got %s", signature)
	}
	first := parseOpenAICompletionsReasoningDetail(details[0])
	for name, expected := range map[string]string{
		"text": `"first second third"`, "id": `"reason_1"`, "format": `"provider.v1"`,
		"index": `9007199254740993`, "signature": `"sig1"`, "opaque": `{"n":9007199254740993}`,
	} {
		if string(first[name]) != expected {
			t.Errorf("merged text field %s = %s, want %s", name, first[name], expected)
		}
	}
	summary := parseOpenAICompletionsReasoningDetail(details[1])
	if string(summary["summary"]) != `"summary"` || string(summary["id"]) != `""` || string(summary["index"]) != "0" {
		t.Fatalf("summary merge changed identity fields: %s", details[1])
	}
	if !strings.Contains(string(details[2]), `"opaque":{"n":9007199254740995}`) || !strings.Contains(string(details[2]), `"index":9007199254740993`) {
		t.Fatalf("encrypted metadata must remain opaque: %s", details[2])
	}
	if text, _ := requiredOpenAICompletionsStringField(parseOpenAICompletionsReasoningDetail(details[4]), "text"); text != "separate" {
		t.Fatalf("text separated by encrypted details was merged: %s", details[4])
	}
	var sawStart, sawEnd, sawDone bool
	for event := range stream.Events() {
		switch event.Type {
		case AssistantMessageEventThinkingStart:
			sawStart = true
			if event.Partial.Content[0].(ThinkingContent).ThinkingSignature != "" {
				t.Fatal("completion mutated a previously queued thinking snapshot")
			}
		case AssistantMessageEventThinkingEnd:
			sawEnd = true
			if event.Partial.Content[0].(ThinkingContent).ThinkingSignature != signature {
				t.Fatal("thinking_end did not receive the final signature")
			}
		case AssistantMessageEventDone:
			sawDone = true
			if event.Message.Content[0].(ThinkingContent).ThinkingSignature != signature {
				t.Fatal("done did not receive the final signature")
			}
		}
	}
	if !sawStart || !sawEnd || !sawDone {
		t.Fatalf("missing lifecycle events: start=%v end=%v done=%v", sawStart, sawEnd, sawDone)
	}
}

func TestOpenAICompletionsFinalizesReasoningDetailsOnStreamFailure(t *testing.T) {
	for _, terminal := range []string{"", `data: {"error":{"message":"upstream interrupted"}}` + "\n\n"} {
		t.Run(fmt.Sprintf("terminal=%q", terminal), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = fmt.Fprint(w, "data: "+`{"choices":[{"delta":{"reasoning_content":"plan","reasoning_details":[{"type":"reasoning.text","text":"one ","index":9007199254740993}]}}]}`+"\n\n")
				_, _ = fmt.Fprint(w, "data: "+`{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text","text":"two","signature":"sig"}]}}]}`+"\n\n"+terminal)
			}))
			defer server.Close()
			model := Model{Provider: "openrouter", API: "openai-completions", ID: "test", BaseURL: server.URL}
			stream := Stream(model, Context{}, ProviderStreamOptions{APIKey: "test"})
			var failure AssistantMessage
			for event := range stream.Events() {
				if event.Type == AssistantMessageEventError {
					failure = event.Error
				}
			}
			result := stream.Result()
			if result.StopReason != StopReasonError || len(result.Content) != 1 || len(failure.Content) != 1 {
				t.Fatalf("expected failed response preserving thinking: result=%+v event=%+v", result, failure)
			}
			thinking := result.Content[0].(ThinkingContent)
			if thinking.Thinking != "plan" || thinking.ThinkingSignature != failure.Content[0].(ThinkingContent).ThinkingSignature {
				t.Fatalf("failure event/result signature mismatch: %+v", thinking)
			}
			converted, ok := openAICompletionsAssistantMessage(model, AssistantMessage{Content: []ContentBlock{thinking}}, resolvedOpenAICompletionsCompat{})
			if !ok || len(converted.ReasoningDetails) != 1 || converted.ReasoningContent != nil {
				t.Fatalf("failed stream details cannot be replayed: %+v", converted)
			}
			detail := parseOpenAICompletionsReasoningDetail(converted.ReasoningDetails[0])
			if string(detail["text"]) != `"one two"` || string(detail["signature"]) != `"sig"` || string(detail["index"]) != "9007199254740993" {
				t.Fatalf("lost partial replay metadata: %s", converted.ReasoningDetails[0])
			}
		})
	}
}

func BenchmarkOpenAICompletionsReasoningDetails(b *testing.B) {
	for _, chunks := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("chunks=%d", chunks), func(b *testing.B) {
			raw := json.RawMessage(`[{"type":"reasoning.text","text":"a streaming reasoning delta ","index":0}]`)
			b.ReportAllocs()
			for b.Loop() {
				response := AssistantMessage{Content: []ContentBlock{ThinkingContent{}}}
				state := &openAICompletionsStreamState{ThinkingStarted: true}
				for range chunks {
					appendOpenAICompletionsReasoningDetails(&response, nil, state, raw)
				}
				applyOpenAICompletionsReasoningDetails(&response, state)
			}
		})
	}
}
