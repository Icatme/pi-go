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

func TestSupportsXHighForSonnet46(t *testing.T) {
	model := Model{ID: "claude-sonnet-4-6"}
	if !SupportsXHigh(model) {
		t.Fatal("expected sonnet-4-6 to support xhigh")
	}
}

func TestSupportsXHighForSonnet46WithDot(t *testing.T) {
	model := Model{ID: "claude-sonnet-4.6"}
	if !SupportsXHigh(model) {
		t.Fatal("expected sonnet-4.6 to support xhigh")
	}
}

func TestBuiltInsDoNotExposeOpenRouter(t *testing.T) {
	model := GetModel("openrouter", "anthropic/claude-opus-4.6")
	if model != nil {
		t.Fatalf("expected openrouter model to be removed from built-ins, got %+v", *model)
	}
}

func TestKimiCodingModelsUseUpstreamUserAgent(t *testing.T) {
	for _, modelID := range []string{"k2p5", "k2p6", "kimi-k2-thinking"} {
		model := GetModel("kimi-coding", modelID)
		if model == nil {
			t.Fatalf("expected kimi model %q to exist", modelID)
		}
		if got := model.Headers["User-Agent"]; got != "KimiCLI/1.5" {
			t.Fatalf("expected kimi model %q user-agent KimiCLI/1.5, got %q", modelID, got)
		}
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

func TestClampThinkingLevelClampsToNearestAvailable(t *testing.T) {
	model := Model{
		Reasoning: true,
		ThinkingLevelMap: ThinkingLevelMap{
			ModelThinkingLevelLow:    "low",
			ModelThinkingLevelMedium: "",
			ModelThinkingLevelHigh:   "high",
		},
	}
	// medium is explicitly excluded (mapped to empty string), should clamp to high
	got := ClampThinkingLevel(model, ModelThinkingLevelMedium)
	if got != ModelThinkingLevelHigh {
		t.Fatalf("expected medium to clamp to high when explicitly excluded, got %v", got)
	}
}

func TestClampThinkingLevelReturnsOffForEmptyAvailableLevels(t *testing.T) {
	model := Model{Reasoning: false}
	if got := ClampThinkingLevel(model, ModelThinkingLevelXHigh); got != ModelThinkingLevelOff {
		t.Fatalf("expected off for non-reasoning model with any level request, got %v", got)
	}
}

func TestGetSupportedThinkingLevelsWithExplicitlyExcludedLevel(t *testing.T) {
	model := Model{
		Reasoning: true,
		ThinkingLevelMap: ThinkingLevelMap{
			ModelThinkingLevelMinimal: "low",
			ModelThinkingLevelLow:     "low",
			ModelThinkingLevelMedium:  "",
			ModelThinkingLevelHigh:    "high",
		},
	}
	levels := GetSupportedThinkingLevels(model)
	for _, level := range levels {
		if level == ModelThinkingLevelMedium {
			t.Fatal("expected medium to be excluded when explicitly mapped to empty string")
		}
	}
}

func TestCalculateCostComputesTotalCorrectly(t *testing.T) {
	model := Model{
		Cost: UsageCost{Input: 1.0, Output: 2.0, CacheRead: 0.5, CacheWrite: 0.25},
	}
	usage := Usage{Input: 1000000, Output: 500000, CacheRead: 200000, CacheWrite: 100000}
	cost := CalculateCost(model, usage)
	if cost.Input != 1.0 {
		t.Fatalf("expected input cost 1.0, got %f", cost.Input)
	}
	if cost.Output != 1.0 {
		t.Fatalf("expected output cost 1.0, got %f", cost.Output)
	}
	// Use tolerance for floating point comparison
	if cost.CacheRead < 0.099 || cost.CacheRead > 0.101 {
		t.Fatalf("expected cache read cost ~0.1, got %f", cost.CacheRead)
	}
	if cost.CacheWrite < 0.024 || cost.CacheWrite > 0.026 {
		t.Fatalf("expected cache write cost ~0.025, got %f", cost.CacheWrite)
	}
	if cost.Total < 2.124 || cost.Total > 2.126 {
		t.Fatalf("expected total cost ~2.125, got %f", cost.Total)
	}
}

func TestCalculateCostWithRealisticRates(t *testing.T) {
	model := Model{
		Cost: UsageCost{Input: 1.25, Output: 10.0, CacheRead: 0.125},
	}
	usage := Usage{Input: 1000000, Output: 100000, CacheRead: 1000000}
	cost := CalculateCost(model, usage)
	if cost.Input != 1.25 {
		t.Fatalf("expected input cost 1.25, got %f", cost.Input)
	}
	if cost.Output != 1.0 {
		t.Fatalf("expected output cost 1.0, got %f", cost.Output)
	}
	if cost.CacheRead != 0.125 {
		t.Fatalf("expected cache read cost 0.125, got %f", cost.CacheRead)
	}
}

func TestCalculateCostHandlesZeroUsage(t *testing.T) {
	model := Model{
		Cost: UsageCost{Input: 1.0, Output: 2.0, CacheRead: 0.5, CacheWrite: 0.25},
	}
	usage := Usage{}
	cost := CalculateCost(model, usage)
	if cost.Total != 0 {
		t.Fatalf("expected zero cost for zero usage, got %f", cost.Total)
	}
}
