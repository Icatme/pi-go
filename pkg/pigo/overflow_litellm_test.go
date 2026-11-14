package pigo

import "testing"

func TestLiteLLMExplicitContextOverflow(t *testing.T) {
	for _, text := range []string{
		"litellm.ContextWindowExceededError: litellm.BadRequestError: context rejected",
		"litellm._pre_call_checks: Context Window exceeded for given call. No models have context window large enough for this call.",
		"CONTEXTWINDOWEXCEEDEDERROR: limit",
	} {
		if !IsContextOverflow(AssistantMessage{StopReason: StopReasonError, ErrorMessage: text}, 0) {
			t.Errorf("not recognized: %q", text)
		}
		if IsContextOverflow(AssistantMessage{StopReason: StopReasonStop, ErrorMessage: text}, 0) {
			t.Errorf("nonerror classified: %q", text)
		}
	}
	for _, text := range []string{
		"rate limit: ContextWindowExceededError",
		"too many requests: Context Window exceeded for given call",
		"Max Input Tokens=100, Got=99",
		"NotContextWindowExceededErrorLike",
		"Context Window exceeded for given calling",
		"context_window exceeded for given call",
		"Context window configured for given call",
	} {
		if IsContextOverflow(AssistantMessage{StopReason: StopReasonError, ErrorMessage: text}, 0) {
			t.Errorf("false positive: %q", text)
		}
	}
}
