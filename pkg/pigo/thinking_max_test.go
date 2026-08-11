package pigo

import "testing"

func TestClampThinkingLevelMax(t *testing.T) {
	t.Run("explicit max", func(t *testing.T) {
		model := Model{
			Reasoning: true,
			ThinkingLevelMap: ThinkingLevelMap{
				ModelThinkingLevelHigh: "high",
				ModelThinkingLevelMax:  "max",
			},
		}
		if got := ClampThinkingLevel(model, ModelThinkingLevelMax); got != ModelThinkingLevelMax {
			t.Fatalf("expected max, got %q", got)
		}
	})

	t.Run("falls back to xhigh", func(t *testing.T) {
		model := Model{
			Reasoning: true,
			ThinkingLevelMap: ThinkingLevelMap{
				ModelThinkingLevelHigh:  "high",
				ModelThinkingLevelXHigh: "xhigh",
			},
		}
		if got := ClampThinkingLevel(model, ModelThinkingLevelMax); got != ModelThinkingLevelXHigh {
			t.Fatalf("expected xhigh fallback, got %q", got)
		}
	})

	t.Run("falls back to high", func(t *testing.T) {
		model := Model{Reasoning: true}
		if got := ClampThinkingLevel(model, ModelThinkingLevelMax); got != ModelThinkingLevelHigh {
			t.Fatalf("expected high fallback, got %q", got)
		}
	})
}

func TestClampOpenAIResponsesReasoningEffortMax(t *testing.T) {
	model := Model{
		ID:        "gpt-test",
		Reasoning: true,
		ThinkingLevelMap: ThinkingLevelMap{
			ModelThinkingLevelMax: "max",
		},
	}
	if got := clampOpenAIResponsesReasoningEffort(model, ThinkingLevelMax); got != "max" {
		t.Fatalf("expected explicit max mapping, got %q", got)
	}

	model.ThinkingLevelMap = nil
	if got := clampOpenAIResponsesReasoningEffort(model, ThinkingLevelMax); got != "high" {
		t.Fatalf("expected unsupported max to clamp to high, got %q", got)
	}
}
