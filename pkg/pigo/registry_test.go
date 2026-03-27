package pigo

import (
	"math"
	"reflect"
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

func TestRegisterLazyProviderModuleLoadsOnDemand(t *testing.T) {
	provider := Provider("test-lazy-provider")
	loadCount := 0

	RegisterLazyProviderModule(provider, func() ProviderModule {
		loadCount++
		return ProviderModule{
			Models: map[string]Model{
				"lazy-model": {
					Name:          "Lazy Model",
					API:           "lazy-api",
					BaseURL:       "https://example.invalid",
					ContextWindow: 1024,
					MaxTokens:     256,
				},
			},
		}
	})

	providers := GetProviders()
	if !containsProvider(providers, provider) {
		t.Fatal("expected lazy provider to appear before module load")
	}
	if loadCount != 0 {
		t.Fatalf("expected lazy provider to remain unloaded before use, got %d loads", loadCount)
	}

	model := GetModel(provider, "lazy-model")
	if model == nil {
		t.Fatal("expected lazy model to load on first use")
	}
	if loadCount != 1 {
		t.Fatalf("expected single lazy load, got %d", loadCount)
	}
	if model.Provider != provider {
		t.Fatalf("expected lazy model provider to be normalized, got %q", model.Provider)
	}

	models := GetModels(provider)
	if len(models) != 1 || models[0].ID != "lazy-model" {
		t.Fatalf("expected lazy model list after load, got %+v", models)
	}
	if loadCount != 1 {
		t.Fatalf("expected lazy module to load only once, got %d", loadCount)
	}
}

func TestRegisterProviderModuleDispatchesStreamThroughRegistry(t *testing.T) {
	provider := Provider("test-stream-provider")
	modelID := "stream-model"
	called := false

	RegisterProviderModule(ProviderModule{
		Provider: provider,
		Models: map[string]Model{
			modelID: {
				ID:      modelID,
				Name:    "Stream Model",
				API:     "stream-api",
				BaseURL: "https://example.invalid",
			},
		},
		Stream: func(model Model, ctx Context, options CompleteOptions) *AssistantMessageEventStream {
			called = true
			stream := newAssistantMessageEventStream()
			message := AssistantMessage{
				API:        model.API,
				Provider:   model.Provider,
				Model:      model.ID,
				StopReason: StopReasonStop,
				Content: []ContentBlock{
					TextContent{Text: "from plugin"},
				},
			}
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventStart, Partial: message})
			stream.push(AssistantMessageEvent{Type: AssistantMessageEventDone, Reason: message.StopReason, Message: message})
			stream.finish(message)
			return stream
		},
	})

	model := GetModel(provider, modelID)
	if model == nil {
		t.Fatal("expected registered stream model to exist")
	}

	stream := StreamSimple(*model, Context{}, CompleteOptions{})
	var events []AssistantMessageEvent
	for event := range stream.Events() {
		events = append(events, event)
	}
	result := stream.Result()

	if !called {
		t.Fatal("expected stream dispatch to use registered provider module")
	}
	if len(events) != 2 {
		t.Fatalf("expected start + done events, got %d", len(events))
	}
	if events[1].Type != AssistantMessageEventDone {
		t.Fatalf("expected done event, got %q", events[1].Type)
	}
	if !reflect.DeepEqual(events[1].Message, result) {
		t.Fatalf("expected registry stream result to match done event, got done=%+v result=%+v", events[1].Message, result)
	}
}

func TestGetProviderCapabilitiesReturnsBuiltInMetadata(t *testing.T) {
	codex := GetProviderCapabilities("openai-codex")
	if !codex.SupportsStreaming || !codex.SupportsSession || !codex.SupportsReasoningSummary || !codex.SupportsTextVerbosity || !codex.SupportsToolChoice {
		t.Fatalf("expected openai-codex capabilities to be registered, got %+v", codex)
	}
	if codex.SupportsPromptCacheControl {
		t.Fatalf("expected openai-codex to not advertise prompt cache control, got %+v", codex)
	}

	kimi := GetProviderCapabilities("kimi-coding")
	if !kimi.SupportsStreaming || !kimi.SupportsPromptCacheControl || !kimi.SupportsThinkingBudget {
		t.Fatalf("expected kimi capabilities to be registered, got %+v", kimi)
	}
	if kimi.SupportsToolChoice {
		t.Fatalf("expected kimi to not advertise tool choice support, got %+v", kimi)
	}
}

func TestNormalizeCompleteOptionsUsesProviderModuleRules(t *testing.T) {
	codex := GetModel("openai-codex", "gpt-5.1")
	if codex == nil {
		t.Fatal("expected openai-codex model")
	}

	codexOptions := NormalizeCompleteOptions(*codex, CompleteOptions{
		Reasoning:      ThinkingLevelXHigh,
		CacheRetention: CacheRetentionLong,
	})
	if codexOptions.Reasoning != ThinkingLevelHigh {
		t.Fatalf("expected codex reasoning to be normalized through module, got %q", codexOptions.Reasoning)
	}
	if codexOptions.ReasoningSummary != "auto" {
		t.Fatalf("expected codex reasoning summary default, got %q", codexOptions.ReasoningSummary)
	}
	if codexOptions.TextVerbosity != "medium" {
		t.Fatalf("expected codex verbosity default, got %q", codexOptions.TextVerbosity)
	}
	if codexOptions.CacheRetention != CacheRetentionNone {
		t.Fatalf("expected codex cache retention to be cleared, got %q", codexOptions.CacheRetention)
	}

	kimi := GetModel("kimi-coding", "kimi-k2-thinking")
	if kimi == nil {
		t.Fatal("expected kimi model")
	}

	kimiOptions := NormalizeCompleteOptions(*kimi, CompleteOptions{
		ToolChoice:       "required",
		TextVerbosity:    "high",
		ReasoningSummary: "detailed",
		SessionID:        "session-1",
	})
	if kimiOptions.ToolChoice != "" || kimiOptions.TextVerbosity != "" || kimiOptions.ReasoningSummary != "" || kimiOptions.SessionID != "" {
		t.Fatalf("expected kimi unsupported options to be cleared, got %+v", kimiOptions)
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
