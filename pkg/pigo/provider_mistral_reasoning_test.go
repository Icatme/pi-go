package pigo

import "testing"

func TestMistralCurrentReasoningWireFields(t *testing.T) {
	for _, id := range []string{"mistral-medium-3.5", "mistral-medium-latest", "mistral-medium-2608", "zai-glm-5-2"} {
		t.Run(id, func(t *testing.T) {
			model := Model{Provider: "mistral", API: "mistral-conversations", ID: id, Reasoning: true}
			request := buildMistralChatRequest(model, Context{}, BuildProviderStreamOptions(model, SimpleStreamOptions{Reasoning: ThinkingLevelHigh}))
			if request.ReasoningEffort != "high" || request.PromptMode != "" {
				t.Fatalf("expected reasoning_effort only: %+v", request)
			}
		})
	}
}
