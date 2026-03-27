package pigo

import (
	"math"
	"testing"
)

func TestGetProvidersIncludesTargetProviders(t *testing.T) {
	providers := GetProviders()

	if !containsProvider(providers, "openai-codex") {
		t.Fatal("expected openai-codex provider to be registered")
	}
	if !containsProvider(providers, "kimi-coding") {
		t.Fatal("expected kimi-coding provider to be registered")
	}
}

func TestGetModelsReturnsTargetProviderModels(t *testing.T) {
	models := GetModels("kimi-coding")
	if len(models) != 2 {
		t.Fatalf("expected 2 kimi-coding models, got %d", len(models))
	}

	model := GetModel("kimi-coding", "kimi-k2-thinking")
	if model == nil {
		t.Fatal("expected kimi-k2-thinking model to exist")
	}
	if model.API != "anthropic-messages" {
		t.Fatalf("expected kimi-k2-thinking to use anthropic-messages, got %q", model.API)
	}
}

func TestGetModelsReturnsClonedInputSlices(t *testing.T) {
	models := GetModels("openai-codex")
	if len(models) == 0 {
		t.Fatal("expected openai-codex models")
	}

	models[0].Input[0] = "mutated"

	model := GetModel("openai-codex", models[0].ID)
	if model == nil {
		t.Fatal("expected model to still exist")
	}
	if model.Input[0] == "mutated" {
		t.Fatal("expected model input slice to be cloned")
	}
}

func TestCalculateCostUsesPerMillionRates(t *testing.T) {
	model := GetModel("openai-codex", "gpt-5.4")
	if model == nil {
		t.Fatal("expected gpt-5.4 model to exist")
	}

	cost := CalculateCost(*model, Usage{
		Input:      1000,
		Output:     2000,
		CacheRead:  3000,
		CacheWrite: 4000,
	})

	if cost.Input != 0.0025 {
		t.Fatalf("expected input cost 0.0025, got %v", cost.Input)
	}
	if !almostEqual(cost.Output, 0.03) {
		t.Fatalf("expected output cost 0.03, got %v", cost.Output)
	}
	if !almostEqual(cost.CacheRead, 0.00075) {
		t.Fatalf("expected cache read cost 0.00075, got %v", cost.CacheRead)
	}
	if cost.CacheWrite != 0 {
		t.Fatalf("expected cache write cost 0, got %v", cost.CacheWrite)
	}
	if !almostEqual(cost.Total, 0.03325) {
		t.Fatalf("expected total cost 0.03325, got %v", cost.Total)
	}
}

func TestModelsAreEqualComparesIDAndProvider(t *testing.T) {
	left := GetModel("openai-codex", "gpt-5.4")
	right := GetModel("openai-codex", "gpt-5.4")
	other := GetModel("kimi-coding", "k2p5")

	if !ModelsAreEqual(left, right) {
		t.Fatal("expected identical provider/model pair to compare equal")
	}
	if ModelsAreEqual(left, other) {
		t.Fatal("expected different provider/model pair to compare unequal")
	}
	if ModelsAreEqual(left, nil) {
		t.Fatal("expected nil model comparison to be false")
	}
}

func containsProvider(providers []Provider, target Provider) bool {
	for _, provider := range providers {
		if provider == target {
			return true
		}
	}
	return false
}

func almostEqual(left, right float64) bool {
	return math.Abs(left-right) < 1e-12
}
