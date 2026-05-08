package pigo

import "testing"

func TestSupportsXHighForAnthropicOpus46(t *testing.T) {
	model := GetModel("anthropic", "claude-opus-4-6")
	if model == nil {
		t.Fatal("expected model to exist")
	}
	if !SupportsXHigh(*model) {
		t.Fatal("expected opus 4.6 to support xhigh")
	}
}

func TestSupportsXHighFalseForNonOpusAnthropic(t *testing.T) {
	model := GetModel("anthropic", "claude-sonnet-4-5")
	if model == nil {
		t.Fatal("expected model to exist")
	}
	if SupportsXHigh(*model) {
		t.Fatal("expected claude-sonnet-4-5 to not support xhigh")
	}
}

func TestSupportsXHighForGPT54(t *testing.T) {
	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected model to exist")
	}
	if !SupportsXHigh(*model) {
		t.Fatal("expected gpt-5.4 to support xhigh")
	}
}

func TestBuiltInsDoNotExposeOpenRouter(t *testing.T) {
	model := GetModel("openrouter", "anthropic/claude-opus-4.6")
	if model != nil {
		t.Fatalf("expected openrouter model to be removed from built-ins, got %+v", *model)
	}
}

func TestGetSupportedThinkingLevelsForNonReasoningModel(t *testing.T) {
	model := Model{Reasoning: false}
	levels := GetSupportedThinkingLevels(model)
	if len(levels) != 1 || levels[0] != ModelThinkingLevelOff {
		t.Fatalf("expected only 'off' for non-reasoning model, got %v", levels)
	}
}

func TestGetSupportedThinkingLevelsForReasoningModel(t *testing.T) {
	model := Model{
		Reasoning: true,
		ThinkingLevelMap: ThinkingLevelMap{
			ModelThinkingLevelMinimal: "low",
			ModelThinkingLevelLow:     "low",
			ModelThinkingLevelMedium:  "medium",
			ModelThinkingLevelHigh:    "high",
		},
	}
	levels := GetSupportedThinkingLevels(model)
	if len(levels) != 5 {
		t.Fatalf("expected 5 supported levels (including off), got %d: %v", len(levels), levels)
	}
	expected := []ModelThinkingLevel{ModelThinkingLevelOff, ModelThinkingLevelMinimal, ModelThinkingLevelLow, ModelThinkingLevelMedium, ModelThinkingLevelHigh}
	for i, level := range expected {
		if levels[i] != level {
			t.Fatalf("expected level %d to be %v, got %v", i, level, levels[i])
		}
	}
}

func TestGetSupportedThinkingLevelsExcludesXHighWithoutMapping(t *testing.T) {
	model := Model{
		Reasoning: true,
		ThinkingLevelMap: ThinkingLevelMap{
			ModelThinkingLevelMinimal: "low",
			ModelThinkingLevelLow:     "low",
			ModelThinkingLevelMedium:  "medium",
			ModelThinkingLevelHigh:    "high",
		},
	}
	levels := GetSupportedThinkingLevels(model)
	for _, level := range levels {
		if level == ModelThinkingLevelXHigh {
			t.Fatal("expected xhigh to be excluded when no mapping exists")
		}
	}
}

func TestGetSupportedThinkingLevelsIncludesXHighWithMapping(t *testing.T) {
	model := Model{
		Reasoning: true,
		ThinkingLevelMap: ThinkingLevelMap{
			ModelThinkingLevelMinimal: "low",
			ModelThinkingLevelLow:     "low",
			ModelThinkingLevelMedium:  "medium",
			ModelThinkingLevelHigh:    "high",
			ModelThinkingLevelXHigh:   "max",
		},
	}
	levels := GetSupportedThinkingLevels(model)
	hasXHigh := false
	for _, level := range levels {
		if level == ModelThinkingLevelXHigh {
			hasXHigh = true
			break
		}
	}
	if !hasXHigh {
		t.Fatal("expected xhigh to be included when mapping exists")
	}
}

func TestClampThinkingLevelReturnsValidLevel(t *testing.T) {
	model := Model{
		Reasoning: true,
		ThinkingLevelMap: ThinkingLevelMap{
			ModelThinkingLevelMinimal: "low",
			ModelThinkingLevelLow:     "low",
			ModelThinkingLevelMedium:  "medium",
			ModelThinkingLevelHigh:    "high",
		},
	}
	if got := ClampThinkingLevel(model, ModelThinkingLevelMedium); got != ModelThinkingLevelMedium {
		t.Fatalf("expected medium to be valid, got %v", got)
	}
}

func TestClampThinkingLevelClampsToOffForNonReasoningModel(t *testing.T) {
	model := Model{Reasoning: false}
	if got := ClampThinkingLevel(model, ModelThinkingLevelHigh); got != ModelThinkingLevelOff {
		t.Fatalf("expected off for non-reasoning model, got %v", got)
	}
}

func TestClampThinkingLevelClampsXHighToHighWhenNoMapping(t *testing.T) {
	model := Model{
		Reasoning: true,
		ThinkingLevelMap: ThinkingLevelMap{
			ModelThinkingLevelMinimal: "low",
			ModelThinkingLevelLow:     "low",
			ModelThinkingLevelMedium:  "medium",
			ModelThinkingLevelHigh:    "high",
		},
	}
	if got := ClampThinkingLevel(model, ModelThinkingLevelXHigh); got != ModelThinkingLevelHigh {
		t.Fatalf("expected xhigh to clamp to high, got %v", got)
	}
}

func TestClampThinkingLevelPreservesMinimalEvenWithoutExplicitMapping(t *testing.T) {
	model := Model{
		Reasoning: true,
		ThinkingLevelMap: ThinkingLevelMap{
			ModelThinkingLevelLow:    "low",
			ModelThinkingLevelMedium: "medium",
			ModelThinkingLevelHigh:   "high",
		},
	}
	// minimal is implicitly supported for reasoning models (not XHigh, not explicitly excluded)
	if got := ClampThinkingLevel(model, ModelThinkingLevelMinimal); got != ModelThinkingLevelMinimal {
		t.Fatalf("expected minimal to be preserved, got %v", got)
	}
}
