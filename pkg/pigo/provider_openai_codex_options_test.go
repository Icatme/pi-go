package pigo

import "testing"

func TestOpenAICodexOffReasoningUsesModelWireMapping(t *testing.T) {
	for _, test := range []struct {
		name      string
		reasoning bool
		levelMap  ThinkingLevelMap
		want      string
	}{
		{name: "default none", reasoning: true, want: "none"},
		{name: "explicit none", reasoning: true, levelMap: ThinkingLevelMap{ModelThinkingLevelOff: "none"}, want: "none"},
		{name: "custom off effort", reasoning: true, levelMap: ThinkingLevelMap{ModelThinkingLevelOff: "minimal"}, want: "minimal"},
		{name: "unsupported off", reasoning: true, levelMap: ThinkingLevelMap{ModelThinkingLevelOff: "", ModelThinkingLevelLow: "low"}},
		{name: "non reasoning model"},
	} {
		for _, requested := range []ThinkingLevel{"", ThinkingLevel(ModelThinkingLevelOff), "none"} {
			t.Run(test.name+"/"+string(requested), func(t *testing.T) {
				model := Model{API: "openai-codex-responses", Provider: "openai-codex", ID: "fixture", Reasoning: test.reasoning, ThinkingLevelMap: test.levelMap}
				options := BuildProviderStreamOptions(model, SimpleStreamOptions{Reasoning: requested})
				options = NormalizeProviderStreamOptions(model, options)
				request := buildOpenAICodexRequest(model, Context{}, options)
				if test.want == "" {
					if request.Reasoning != nil {
						t.Fatalf("unsupported off must be omitted, got %+v", request.Reasoning)
					}
				} else if request.Reasoning == nil || request.Reasoning.Effort != test.want {
					t.Fatalf("off effort = %+v, want %q", request.Reasoning, test.want)
				}
			})
		}
	}
}
