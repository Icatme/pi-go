package pigo

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCompletionsRejectsNamedToolIndexCollision(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, `data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_A","type":"function","function":{"name":"bash","arguments":"{\"command\":\"echo AAA\"}"}}]}}]}

data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_B","type":"function","function":{"name":"bash","arguments":"{\"command\":\"echo BBB\"}"}}]}}]}

data: {"choices":[{"delta":{},"finish_reason":"tool_calls"}]}

data: [DONE]

`)
	}))
	defer s.Close()
	result := CompleteSimple(Model{ID: "fixture", API: "openai-completions", Provider: "openai", BaseURL: s.URL, MaxTokens: 256}, Context{}, SimpleStreamOptions{APIKey: "fixture"})
	if result.StopReason != StopReasonError || result.ErrorMessage != "conflicting tool call identities for stream index" {
		t.Fatalf("ambiguous tool identity accepted: %+v", result)
	}
	if len(result.Content) != 1 || result.Content[0].(ToolCall).ID != "call_A" {
		t.Fatalf("collision mutated existing identity: %+v", result.Content)
	}
	failure, ok := ProviderFailureOf(result)
	if !ok || failure.Kind != ProviderFailureParse || failure.Reason != "tool_call_index_collision" {
		t.Fatalf("missing failure: %+v", failure)
	}
}

func TestCompletionsToolIndexIdentityCompatibility(t *testing.T) {
	for _, tc := range []struct {
		name, id, fn string
		index        int
		wantError    bool
	}{
		{"repeat same identity", "call_A", "bash", 0, false},
		{"new parallel index", "call_B", "bash", 1, false},
		{"identity only update", "call_B", "", 0, false},
		{"new named collision", "call_B", "bash", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state := &openAICompletionsStreamState{ToolCalls: map[int]*openAICompletionsToolCallState{}}
			response := AssistantMessage{}
			stream := newAssistantMessageEventStream()
			appendOpenAICompletionsToolCall(&response, stream, state, openAICompletionsDeltaToolCall{Index: 0, ID: "call_A", Function: openAICompletionsToolFunction{Name: "bash", Arguments: `{"command":`}})
			data := fmt.Sprintf(`{"choices":[{"delta":{"tool_calls":[{"index":%d,"id":%q,"function":{"name":%q,"arguments":""}}]}}]}`, tc.index, tc.id, tc.fn)
			_, err := processOpenAICompletionsStreamEvent(data, Model{}, &response, stream, state)
			if (err != nil) != tc.wantError {
				t.Fatalf("error=%v wantError=%v", err, tc.wantError)
			}
		})
	}
}

// An ID-only refresh remains compatible. It cannot be distinguished from a
// malformed endpoint changing identities without a named call boundary.
func TestCompletionsToolIDRefreshAndLateID(t *testing.T) {
	for _, initialID := range []string{"", "initial"} {
		state := &openAICompletionsStreamState{ToolCalls: map[int]*openAICompletionsToolCallState{}}
		response := AssistantMessage{}
		stream := newAssistantMessageEventStream()
		for _, delta := range []openAICompletionsDeltaToolCall{
			{Index: 0, ID: initialID, Function: openAICompletionsToolFunction{Name: "bash", Arguments: `{"command":`}},
			{Index: 0, ID: "final", Function: openAICompletionsToolFunction{Arguments: `"echo"}`}},
		} {
			appendOpenAICompletionsToolCall(&response, stream, state, delta)
		}
		got := response.Content[0].(ToolCall)
		if got.ID != "final" || got.Arguments["command"] != "echo" || len(response.Content) != 1 {
			t.Fatalf("late ID lost: %+v", got)
		}
	}
}
