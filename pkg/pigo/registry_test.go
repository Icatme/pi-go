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
	if len(models) < 2 {
		t.Fatalf("expected kimi-coding models to be registered, got %d", len(models))
	}

	if GetModel("kimi-coding", "k2p5") == nil {
		t.Fatal("expected k2p5 model to exist")
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
	apiID := API("test-stream-api")
	called := false
	emit := func(model Model) *AssistantMessageEventStream {
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
	}

	RegisterProviderModule(ProviderModule{
		Provider: provider,
		Models: map[string]Model{
			modelID: {
				ID:      modelID,
				Name:    "Stream Model",
				API:     apiID,
				BaseURL: "https://example.invalid",
			},
		},
	})
	RegisterAPIModule(APIModule{
		API: apiID,
		Stream: func(model Model, ctx Context, options ProviderStreamOptions) *AssistantMessageEventStream {
			return emit(model)
		},
		StreamSimple: func(model Model, ctx Context, options SimpleStreamOptions) *AssistantMessageEventStream {
			return emit(model)
		},
	})

	model := GetModel(provider, modelID)
	if model == nil {
		t.Fatal("expected registered stream model to exist")
	}

	stream := StreamSimple(*model, Context{}, SimpleStreamOptions{})
	var events []AssistantMessageEvent
	for event := range stream.Events() {
		events = append(events, event)
	}
	result := stream.Result()

	if !called {
		t.Fatal("expected stream dispatch to use registered api module")
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
	anthropic := GetProviderCapabilities("anthropic")
	if !anthropic.SupportsStreaming || !anthropic.SupportsPromptCacheControl || !anthropic.SupportsThinkingBudget || !anthropic.SupportsToolChoice {
		t.Fatalf("expected anthropic capabilities to be registered, got %+v", anthropic)
	}
	if anthropic.SupportsSession {
		t.Fatalf("expected anthropic to not advertise sessions, got %+v", anthropic)
	}

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
	if !kimi.HostedTools.WebSearch {
		t.Fatalf("expected kimi to advertise hosted web search support, got %+v", kimi)
	}
	if anthropic.HostedTools.WebSearch || codex.HostedTools.WebSearch {
		t.Fatalf("expected built-in hosted web search support to remain kimi-only, got anthropic=%+v codex=%+v", anthropic, codex)
	}
}

func TestSupportsHostedToolUsesModelAndProviderCapabilities(t *testing.T) {
	kimi := GetModel("kimi-coding", "k2p5")
	if kimi == nil {
		t.Fatal("expected kimi model")
	}
	if !SupportsHostedTool(*kimi, HostedToolTypeWebSearch) {
		t.Fatalf("expected kimi model to support hosted web search, got %+v", kimi)
	}

	codex := GetModel("openai-codex", "gpt-5.4")
	if codex == nil {
		t.Fatal("expected codex model")
	}
	if SupportsHostedTool(*codex, HostedToolTypeWebSearch) {
		t.Fatalf("expected codex model to not support hosted web search, got %+v", codex)
	}
}

func TestNormalizeCompleteOptionsUsesProviderModuleRules(t *testing.T) {
	codex := GetModel("openai-codex", "gpt-5.1")
	if codex == nil {
		t.Fatal("expected openai-codex model")
	}

	codexOptions := NormalizeProviderStreamOptions(*codex, ProviderStreamOptions{
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
	if codexOptions.CacheRetention != CacheRetentionLong {
		t.Fatalf("expected codex cache retention to be preserved, got %q", codexOptions.CacheRetention)
	}

	kimi := GetModel("kimi-coding", "kimi-k2-thinking")
	if kimi == nil {
		t.Fatal("expected kimi model")
	}

	kimiOptions := NormalizeProviderStreamOptions(*kimi, ProviderStreamOptions{
		ToolChoice:       "required",
		TextVerbosity:    "high",
		ReasoningSummary: "detailed",
		SessionID:        "session-1",
	})
	if kimiOptions.ToolChoice != "" || kimiOptions.TextVerbosity != "" || kimiOptions.ReasoningSummary != "" || kimiOptions.SessionID != "" {
		t.Fatalf("expected kimi unsupported options to be cleared, got %+v", kimiOptions)
	}
}

func TestBuildProviderStreamOptionsUsesProviderModuleBuilders(t *testing.T) {
	codex := GetModel("openai-codex", "gpt-5.1")
	if codex == nil {
		t.Fatal("expected openai-codex model")
	}

	codexOptions := BuildProviderStreamOptions(*codex, SimpleStreamOptions{
		Reasoning:      ThinkingLevelXHigh,
		CacheRetention: CacheRetentionLong,
	})
	if codexOptions.Reasoning != ThinkingLevelHigh {
		t.Fatalf("expected codex builder to clamp reasoning, got %q", codexOptions.Reasoning)
	}
	if codexOptions.ReasoningSummary != "auto" || codexOptions.TextVerbosity != "medium" {
		t.Fatalf("expected codex builder defaults, got %+v", codexOptions)
	}
	if codexOptions.CacheRetention != CacheRetentionLong {
		t.Fatalf("expected codex builder to preserve cache retention, got %q", codexOptions.CacheRetention)
	}

	kimi := GetModel("kimi-coding", "kimi-k2-thinking")
	if kimi == nil {
		t.Fatal("expected kimi model")
	}

	kimiOptions := BuildProviderStreamOptions(*kimi, SimpleStreamOptions{
		Reasoning: ThinkingLevelHigh,
		SessionID: "session-1",
	})
	if kimiOptions.SessionID != "" {
		t.Fatalf("expected kimi builder to clear session id, got %+v", kimiOptions)
	}
	if kimiOptions.ThinkingBudgetTokens <= 0 {
		t.Fatalf("expected kimi builder to compute thinking budget, got %+v", kimiOptions)
	}

	anthropic := GetModel("anthropic", "claude-sonnet-4-5")
	if anthropic == nil {
		t.Fatal("expected anthropic model")
	}

	anthropicOptions := BuildProviderStreamOptions(*anthropic, SimpleStreamOptions{
		Reasoning: ThinkingLevelHigh,
	})
	if anthropicOptions.ThinkingBudgetTokens <= 0 || anthropicOptions.MaxTokens <= 0 {
		t.Fatalf("expected anthropic builder to preserve thinking budget behavior, got %+v", anthropicOptions)
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
