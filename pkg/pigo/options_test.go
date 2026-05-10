package pigo

import (
	"context"
	"testing"
)

func TestNewStreamOptionsAppliesFunctionalOptions(t *testing.T) {
	temperature := 0.7
	options := NewStreamOptions(
		WithTemperature(temperature),
		WithMaxTokens(256),
		WithToolChoice("required"),
	)

	if options.Common.Temperature == nil || *options.Common.Temperature != temperature {
		t.Fatalf("expected functional options to populate common temperature, got %+v", options.Common)
	}
	if options.Common.MaxTokens != 256 {
		t.Fatalf("expected functional options to populate common max tokens, got %d", options.Common.MaxTokens)
	}
	if options.ToolChoice != "required" {
		t.Fatalf("expected functional options to set tool choice, got %q", options.ToolChoice)
	}

	providerOptions := options.providerStreamOptions(Model{MaxTokens: 4096})
	if providerOptions.MaxTokens != 256 || providerOptions.ToolChoice != "required" {
		t.Fatalf("expected provider conversion to preserve functional options, got %+v", providerOptions)
	}
}

func TestNewStreamOptionsPreservesCustomFlatFieldOptions(t *testing.T) {
	requestContext := context.Background()
	options := NewStreamOptions(func(options *StreamOptions) {
		options.APIKey = "test-key"
		options.SessionID = "session-1"
		options.Headers = map[string]string{"x-test": "value"}
		options.RequestContext = requestContext
	})

	if options.APIKey != "test-key" || options.Common.APIKey != "test-key" {
		t.Fatalf("expected custom option to preserve api key across common and flat fields, got %+v", options)
	}
	if options.SessionID != "session-1" || options.Common.SessionID != "session-1" {
		t.Fatalf("expected custom option to preserve session id across common and flat fields, got %+v", options)
	}
	if options.Headers["x-test"] != "value" || options.Common.Headers["x-test"] != "value" {
		t.Fatalf("expected custom option to preserve headers across common and flat fields, got %+v", options)
	}
	if options.RequestContext != requestContext || options.Common.RequestContext != requestContext {
		t.Fatalf("expected custom option to preserve request context across common and flat fields, got %+v", options)
	}
}

func TestStreamOptionsFromSimplePreservesDefaultsAndProviderFields(t *testing.T) {
	model := Model{MaxTokens: 8192}
	options := streamOptionsFromSimple(model, SimpleStreamOptions{
		Reasoning:          ThinkingLevelHigh,
		ThinkingBudgets:    ThinkingBudgets{High: 2048},
		PreviousResponseID: "prev-1",
		Truncation:         "auto",
	})

	if options.Common.MaxTokens != 8192 {
		t.Fatalf("expected simple conversion to preserve model-based max tokens default, got %d", options.Common.MaxTokens)
	}
	if options.Reasoning != ThinkingLevelHigh {
		t.Fatalf("expected simple conversion to keep reasoning level, got %q", options.Reasoning)
	}
	if options.ThinkingBudgets.High != 2048 {
		t.Fatalf("expected simple conversion to keep thinking budgets, got %+v", options.ThinkingBudgets)
	}
	if options.PreviousResponseID != "prev-1" || options.Truncation != "auto" {
		t.Fatalf("expected simple conversion to keep provider-specific fields, got %+v", options)
	}
}

func TestStreamOptionsFromProviderPreservesLegacyCallingStyle(t *testing.T) {
	temperature := 0.2
	options := streamOptionsFromProvider(Model{}, ProviderStreamOptions{
		APIKey:               "test-key",
		MaxTokens:            1024,
		Temperature:          &temperature,
		Reasoning:            ThinkingLevelMedium,
		ReasoningSummary:     "auto",
		ThinkingBudgetTokens: 512,
		ToolChoice:           "auto",
		PreviousResponseID:   "resp-1",
		Truncation:           "disabled",
	})

	if options.Common.APIKey != "test-key" || options.Common.MaxTokens != 1024 {
		t.Fatalf("expected provider conversion to populate common fields, got %+v", options.Common)
	}
	if options.Common.Temperature == nil || *options.Common.Temperature != temperature {
		t.Fatalf("expected provider conversion to preserve temperature, got %+v", options.Common)
	}
	if options.Reasoning != ThinkingLevelMedium || options.ToolChoice != "auto" {
		t.Fatalf("expected provider conversion to preserve provider-specific fields, got %+v", options)
	}
	if options.ThinkingBudgetTokens != 512 || options.PreviousResponseID != "resp-1" || options.Truncation != "disabled" {
		t.Fatalf("expected provider conversion to preserve streaming-specific fields, got %+v", options)
	}
}
