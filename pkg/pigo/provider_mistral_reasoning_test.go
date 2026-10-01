package pigo

import "testing"

func TestMistralReasoningWireFieldsFollowModelCapabilities(t *testing.T) {
	for _, test := range []struct {
		name           string
		model          Model
		level          ThinkingLevel
		effort, prompt string
	}{
		{name: "custom mapped model", model: Model{ID: "custom-glm", Reasoning: true, ThinkingLevelMap: map[ModelThinkingLevel]string{ModelThinkingLevelLow: "low", ModelThinkingLevelHigh: "high"}}, level: ThinkingLevelLow, effort: "low"},
		{name: "off mapping", model: Model{ID: "custom-glm", Reasoning: true, ThinkingLevelMap: map[ModelThinkingLevel]string{ModelThinkingLevelOff: "none", ModelThinkingLevelHigh: "high"}}, level: ThinkingLevel("off"), effort: "none"},
		{name: "omitted uses off mapping", model: Model{ID: "custom-glm", Reasoning: true, ThinkingLevelMap: map[ModelThinkingLevel]string{ModelThinkingLevelOff: "none", ModelThinkingLevelHigh: "high"}}, effort: "none"},
		{name: "off without off mapping", model: Model{ID: "custom-glm", Reasoning: true, ThinkingLevelMap: map[ModelThinkingLevel]string{ModelThinkingLevelHigh: "high"}}, level: ThinkingLevel("off")},
		{name: "omitted without off mapping", model: Model{ID: "custom-glm", Reasoning: true, ThinkingLevelMap: map[ModelThinkingLevel]string{ModelThinkingLevelHigh: "high"}}},
		{name: "unmapped known id uses prompt mode", model: Model{ID: "zai-glm-5-2", Reasoning: true}, level: ThinkingLevelHigh, prompt: "reasoning"},
		{name: "non reasoning ignores map", model: Model{ID: "plain", ThinkingLevelMap: map[ModelThinkingLevel]string{ModelThinkingLevelHigh: "high"}}, level: ThinkingLevelHigh},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.model.Provider, test.model.API = "mistral", "mistral-conversations"
			for _, options := range []ProviderStreamOptions{
				BuildProviderStreamOptions(test.model, SimpleStreamOptions{Reasoning: test.level}),
				{Reasoning: test.level},
			} {
				request := buildMistralChatRequest(test.model, Context{}, options)
				if request.ReasoningEffort != test.effort || request.PromptMode != test.prompt {
					t.Fatalf("effort=%q prompt=%q, want effort=%q prompt=%q", request.ReasoningEffort, request.PromptMode, test.effort, test.prompt)
				}
			}
		})
	}
}
