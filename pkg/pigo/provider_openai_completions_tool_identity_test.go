package pigo

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCompletionsRejectsMalformedFinalToolBatch(t *testing.T) {
	for _, finish := range []string{"tool_calls", "stop", "length"} {
		for _, tc := range []struct{ name, id, tool string }{{"missing name", "bad", ""}, {"blank name", "bad", " \t"}, {"missing id", "", "echo"}, {"blank id", " \t", "echo"}} {
			t.Run(finish+"/"+tc.name, func(t *testing.T) {
				var requests atomic.Int32
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					requests.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"text retained\",\"reasoning_content\":\"thought retained\"}}]}\n\n")
					fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"good\",\"function\":{\"name\":\"echo\",\"arguments\":\"{}\"}},{\"index\":1,\"id\":%q,\"function\":{\"name\":%q,\"arguments\":\"{}\"}}]},\"finish_reason\":%q}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":3,\"total_tokens\":8}}\n\ndata: [DONE]\n\n", tc.id, tc.tool, finish)
				}))
				defer server.Close()
				model := Model{ID: "test", API: "openai-completions", Provider: "fixture", BaseURL: server.URL, MaxTokens: 1024}
				stream := Stream(model, Context{}, ProviderStreamOptions{APIKey: "fixture", MaxRetries: 2})
				events := collectAssistantMessageEvents(stream.Events())
				result := stream.Result()
				if result.StopReason != StopReasonError {
					t.Fatalf("not error: %#v", result)
				}
				failure, ok := ProviderFailureOf(result)
				if !ok || failure.Kind != ProviderFailureParse || failure.Reason != "malformed_tool_call" {
					t.Fatalf("failure: %#v", failure)
				}
				if result.Usage.Input != 5 || result.Usage.Output != 3 {
					t.Fatalf("usage: %#v", result.Usage)
				}
				var text, thought bool
				for _, b := range result.Content {
					switch b := b.(type) {
					case ToolCall:
						t.Fatal("executable call survived")
					case TextContent:
						text = b.Text == "text retained"
					case ThinkingContent:
						thought = b.Thinking == "thought retained"
					}
				}
				if !text || !thought {
					t.Fatalf("text/thinking lost: %#v", result.Content)
				}
				partialToolSeen := false
				for _, event := range events {
					if event.Type == AssistantMessageEventDone || event.Type == AssistantMessageEventToolCallEnd {
						t.Fatalf("unexpected terminal event: %s", event.Type)
					}
					for _, b := range event.Partial.Content {
						if _, ok := b.(ToolCall); ok {
							partialToolSeen = true
						}
					}
				}
				if !partialToolSeen {
					t.Fatal("previous partial evidence lost")
				}
				if requests.Load() != 1 {
					t.Fatalf("retried %d", requests.Load())
				}
			})
		}
	}
}

func TestCompletionsAllowsLateToolIdentityAndUnknownName(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{}\"}}]}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"late\",\"function\":{\"name\":\"unknown_valid\"}}]},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	model := Model{ID: "test", API: "openai-completions", Provider: "fixture", BaseURL: server.URL, MaxTokens: 1024}
	result := Complete(model, Context{}, ProviderStreamOptions{APIKey: "fixture"})
	if result.StopReason != StopReasonToolUse || len(result.Content) != 1 {
		t.Fatalf("result: %#v", result)
	}
	call := result.Content[0].(ToolCall)
	if call.ID != "late" || call.Name != "unknown_valid" {
		t.Fatalf("call: %#v", call)
	}
}

func TestCompletionsMalformedBatchKeepsIndexedReasoning(t *testing.T) {
	response := AssistantMessage{Content: []ContentBlock{ToolCall{ID: "bad"}, ThinkingContent{Thinking: "thought"}, TextContent{Text: "text"}}}
	original := response.Content
	state := openAICompletionsStreamState{ThinkingIndex: 1, ToolCalls: map[int]*openAICompletionsToolCallState{0: {ID: "bad"}}, ReasoningDetails: []*openAICompletionsReasoningDetailState{{Kind: "reasoning.text", Fields: map[string]json.RawMessage{"type": json.RawMessage(`"reasoning.text"`)}}}}
	state.Thinking.WriteString("thought")
	state.ReasoningDetails[0].Text.WriteString("thought")
	if err := validateOpenAICompletionsToolBatch(&response, &state); err == nil {
		t.Fatal("missing error")
	}
	// Existing outer error handling may safely call this again after filtering.
	applyOpenAICompletionsReasoningDetails(&response, &state)
	if len(response.Content) != 2 {
		t.Fatalf("content: %#v", response.Content)
	}
	thinking := response.Content[0].(ThinkingContent)
	if thinking.Thinking != "thought" || !strings.Contains(thinking.ThinkingSignature, `"text":"thought"`) {
		t.Fatalf("thinking: %#v", thinking)
	}
	if _, ok := original[0].(ToolCall); !ok {
		t.Fatal("old partial tool block modified")
	}
}

func TestCompletionsTruncatedArgumentsWithIdentityRetainsLength(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"partial\",\"function\":{\"name\":\"echo\",\"arguments\":\"{\"}}]},\"finish_reason\":\"length\"}]}\n\ndata: [DONE]\n\n")
	}))
	defer server.Close()
	result := Complete(Model{ID: "test", API: "openai-completions", Provider: "fixture", BaseURL: server.URL, MaxTokens: 1024}, Context{}, ProviderStreamOptions{APIKey: "fixture"})
	if result.StopReason != StopReasonLength || len(result.Content) != 1 {
		t.Fatalf("length contract changed: %#v", result)
	}
	if _, ok := result.Content[0].(ToolCall); !ok {
		t.Fatal("truncated evidence lost")
	}
	if f, ok := ProviderFailureOf(result); ok && f.Reason == "malformed_tool_call" {
		t.Fatal("length classified as parse error")
	}
}
