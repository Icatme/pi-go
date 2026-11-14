package pigo

import (
	"fmt"
	"strings"
	"testing"
)

func TestCompletionsNativeFinishReasonDiagnostic(t *testing.T) {
	state := &openAICompletionsStreamState{ToolCalls: map[int]*openAICompletionsToolCallState{}}
	result := AssistantMessage{}
	_, err := processOpenAICompletionsStreamEvent(`{"choices":[{"delta":{},"finish_reason":"error","native_finish_reason":"MALFORMED_FUNCTION_CALL"}]}`, Model{}, &result, newAssistantMessageEventStream(), state)
	if err == nil || !strings.Contains(err.Error(), "MALFORMED_FUNCTION_CALL") || result.StopReason != StopReasonError {
		t.Fatalf("lost native diagnostic: %v %+v", err, result)
	}
}

func TestCompletionsNativeFinishReasonOnlyEnrichesErrors(t *testing.T) {
	for _, tc := range []struct {
		finish, native string
		want           StopReason
		suffix         bool
	}{
		{"error", "", StopReasonError, false}, {"error", "error", StopReasonError, false},
		{"content_filter", "SAFETY", StopReasonError, true}, {"stop", "MALFORMED_FUNCTION_CALL", StopReasonStop, false},
	} {
		state := &openAICompletionsStreamState{ToolCalls: map[int]*openAICompletionsToolCallState{}}
		response := AssistantMessage{}
		_, err := processOpenAICompletionsStreamEvent(fmt.Sprintf(`{"choices":[{"delta":{},"finish_reason":%q,"native_finish_reason":%q}]}`, tc.finish, tc.native), Model{}, &response, newAssistantMessageEventStream(), state)
		if response.StopReason != tc.want {
			t.Fatalf("classification changed: %+v", response)
		}
		if tc.want == StopReasonError && (err == nil || strings.Contains(err.Error(), "native_finish_reason") != tc.suffix) {
			t.Fatalf("diagnostic: %v", err)
		}
		if tc.want == StopReasonStop && err != nil {
			t.Fatal(err)
		}
	}
}
