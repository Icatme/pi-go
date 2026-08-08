package pigo

import "testing"

func TestCloneModelDeepCopiesCompatAndCollections(t *testing.T) {
	supportsStore := true
	allowFallbacks := true
	model := Model{
		ID:               "gpt-test",
		Provider:         "openai-codex",
		API:              "openai-codex-responses",
		Input:            []InputType{InputText, InputImage},
		CostTiers:        []ModelCostTier{{InputTokensAbove: 100_000, Rates: UsageCost{Input: 2}}},
		Headers:          map[string]string{"x-test": "value"},
		ThinkingLevelMap: ThinkingLevelMap{ModelThinkingLevelHigh: "high"},
		Compat: &OpenAICompletionsCompat{
			SupportsStore: &supportsStore,
			OpenRouterRouting: &OpenRouterRouting{
				AllowFallbacks: &allowFallbacks,
				Order:          []string{"a"},
				Only:           []string{"b"},
				Ignore:         []string{"c"},
				MaxPrice:       map[string]any{"input": 1},
			},
		},
	}

	cloned := cloneModel(model)
	compat, ok := cloned.Compat.(*OpenAICompletionsCompat)
	if !ok {
		t.Fatalf("expected OpenAICompletionsCompat clone, got %T", cloned.Compat)
	}
	if compat == model.Compat {
		t.Fatal("expected compat pointer to be deep-cloned")
	}

	cloned.Input[0] = InputType("mutated")
	cloned.CostTiers[0].Rates.Input = 9
	cloned.Headers["x-test"] = "mutated"
	cloned.ThinkingLevelMap[ModelThinkingLevelHigh] = "mutated"
	compat.OpenRouterRouting.Order[0] = "mutated"
	compat.OpenRouterRouting.MaxPrice["input"] = 9

	if model.Input[0] != InputText {
		t.Fatalf("expected model input slice to remain isolated, got %q", model.Input[0])
	}
	if model.CostTiers[0].Rates.Input != 2 {
		t.Fatalf("expected model cost tiers to remain isolated, got %+v", model.CostTiers)
	}
	if model.Headers["x-test"] != "value" {
		t.Fatalf("expected model headers to remain isolated, got %q", model.Headers["x-test"])
	}
	if model.ThinkingLevelMap[ModelThinkingLevelHigh] != "high" {
		t.Fatalf("expected thinking level map to remain isolated, got %q", model.ThinkingLevelMap[ModelThinkingLevelHigh])
	}
	originalCompat := model.Compat.(*OpenAICompletionsCompat)
	if originalCompat.OpenRouterRouting.Order[0] != "a" {
		t.Fatalf("expected compat routing order to remain isolated, got %q", originalCompat.OpenRouterRouting.Order[0])
	}
	if originalCompat.OpenRouterRouting.MaxPrice["input"] != 1 {
		t.Fatalf("expected compat max price to remain isolated, got %+v", originalCompat.OpenRouterRouting.MaxPrice)
	}
}

func TestCloneModelPreservesCompatConcreteTypes(t *testing.T) {
	models := []Model{
		{Compat: &OpenAICompletionsCompat{}},
		{Compat: &OpenAIResponsesCompat{}},
		{Compat: &AnthropicMessagesCompat{}},
	}

	for _, model := range models {
		cloned := cloneModel(model)
		if cloned.Compat == nil {
			t.Fatalf("expected compat clone for %T", model.Compat)
		}
		if cloned.Compat.compatAPI() != model.Compat.compatAPI() {
			t.Fatalf("expected compat API to remain stable for %T", model.Compat)
		}
	}
}

func TestCloneMessagesDeepCopiesNestedData(t *testing.T) {
	messages := []Message{
		UserMessage{Content: map[string]any{"items": []any{"a", map[string]any{"nested": "b"}}}},
		AssistantMessage{
			Content: []ContentBlock{
				ToolCall{ID: "call-1", Name: "echo", Arguments: map[string]any{"count": 1}},
			},
			HostedToolExecutions: []HostedToolExecution{{ID: "exec-1", Arguments: map[string]any{"k": "v"}, Result: map[string]any{"done": true}}},
			Diagnostics:          []AssistantMessageDiagnostic{{Type: "warn", Details: map[string]any{"step": 1}, Error: &DiagnosticErrorInfo{Name: "boom"}}},
		},
		ToolResultMessage{ToolCallID: "call-1", Details: map[string]any{"status": "ok"}, Content: []ContentBlock{TextContent{Text: "done"}}},
	}

	cloned := cloneMessages(messages)
	userContent := cloned[0].(UserMessage).Content.(map[string]any)
	userItems := userContent["items"].([]any)
	userItems[1].(map[string]any)["nested"] = "mutated"

	assistant := cloned[1].(AssistantMessage)
	assistantCall := assistant.Content[0].(ToolCall)
	assistantCall.Arguments["count"] = 2
	assistant.HostedToolExecutions[0].Arguments["k"] = "mutated"
	assistant.Diagnostics[0].Details["step"] = 2

	toolResult := cloned[2].(ToolResultMessage)
	toolResult.Details.(map[string]any)["status"] = "mutated"

	originalUser := messages[0].(UserMessage).Content.(map[string]any)
	if originalUser["items"].([]any)[1].(map[string]any)["nested"] != "b" {
		t.Fatal("expected user message nested content to be cloned")
	}
	originalAssistant := messages[1].(AssistantMessage)
	if originalAssistant.Content[0].(ToolCall).Arguments["count"] != 1 {
		t.Fatal("expected assistant tool call arguments to be cloned")
	}
	if originalAssistant.HostedToolExecutions[0].Arguments["k"] != "v" {
		t.Fatal("expected hosted tool execution arguments to be cloned")
	}
	if originalAssistant.Diagnostics[0].Details["step"] != 1 {
		t.Fatal("expected assistant diagnostics to be cloned")
	}
	if messages[2].(ToolResultMessage).Details.(map[string]any)["status"] != "ok" {
		t.Fatal("expected tool result details to be cloned")
	}
}

func TestCloneHelpersAreNilSafe(t *testing.T) {
	if cloneMessages(nil) != nil {
		t.Fatal("expected nil messages clone")
	}
	if cloneBlocks(nil) != nil {
		t.Fatal("expected nil block clone")
	}
	if cloneMap(nil) != nil {
		t.Fatal("expected nil map clone")
	}
	if cloneCompat(nil) != nil {
		t.Fatal("expected nil compat clone")
	}
}
