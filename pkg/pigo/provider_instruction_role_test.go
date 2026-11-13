package pigo

import "testing"

func TestInstructionRoleCapabilityIndependentOfReasoning(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name       string
		reasoning  bool
		capability *bool
		base, want string
	}{
		{"unknown reasoning", true, nil, "https://example.invalid/v1", "system"},
		{"unknown ordinary", false, nil, "https://example.invalid/v1", "system"},
		{"explicit enabled ordinary", false, &yes, "https://example.invalid/v1", "developer"},
		{"explicit enabled reasoning", true, &yes, "https://example.invalid/v1", "developer"},
		{"explicit disabled", true, &no, "https://api.openai.com/v1", "system"},
		{"native reasoning", true, nil, "https://api.openai.com/v1", "developer"},
		{"lookalike endpoint", true, nil, "https://api.openai.com.example.invalid/v1", "system"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			model := Model{ID: "fixture", API: "openai-completions", Provider: "custom", BaseURL: tc.base, Reasoning: tc.reasoning,
				Compat: &OpenAICompletionsCompat{SupportsDeveloperRole: tc.capability}}
			ctx := Context{SystemPrompt: "Follow instructions", Messages: []Message{UserMessage{Content: "hello"}}}
			chat := convertOpenAICompletionsMessages(model, ctx, resolveOpenAICompletionsCompat(model))
			if len(chat) == 0 || chat[0].Role != tc.want {
				t.Errorf("Chat first role = %v; want %s", chat, tc.want)
			}
			model.API = "openai-responses"
			model.Compat = &OpenAIResponsesCompat{SupportsDeveloperRole: tc.capability}
			responses := convertOpenAIResponsesMessages(nil, nil, model, ResolveTranscript(NormalizeContext(ctx), true), true)
			if len(responses) == 0 || responses[0]["role"] != tc.want {
				t.Errorf("Responses first role = %v; want %s", responses, tc.want)
			}
		})
	}
}
