package pigo

import (
	"fmt"
	"regexp"
	"strings"
)

// GoogleProviderOptions captures provider-specific options for the Google
// Generative AI (Gemini) provider.
type GoogleProviderOptions struct {
	StreamOptions
	ToolChoice string
	Thinking   *GoogleThinkingConfig
}

// GoogleThinkingConfig controls Gemini reasoning/thinking behavior.
type GoogleThinkingConfig struct {
	Enabled      bool
	BudgetTokens int // -1 for dynamic, 0 to disable
	Level        string
}

func buildGoogleProviderStreamOptions(model Model, options SimpleStreamOptions) ProviderStreamOptions {
	return streamOptionsFromSimple(model, options).providerStreamOptions(model)
}

func normalizeGoogleProviderStreamOptions(model Model, options ProviderStreamOptions) ProviderStreamOptions {
	return streamOptionsFromProvider(model, options).providerStreamOptions(model)
}

func resolveGoogleProviderOptions(model Model, options ProviderStreamOptions) (GoogleProviderOptions, error) {
	streamOptions := streamOptionsFromProvider(model, options)
	thinking, err := buildGoogleThinkingFromSimple(model, streamOptions)
	if err != nil {
		return GoogleProviderOptions{}, err
	}
	return GoogleProviderOptions{
		StreamOptions: streamOptions,
		ToolChoice:    streamOptions.ToolChoice,
		Thinking:      thinking,
	}, nil
}

func buildGoogleThinkingFromSimple(model Model, options StreamOptions) (*GoogleThinkingConfig, error) {
	if !model.Reasoning {
		return nil, nil
	}

	requested := ModelThinkingLevel(options.Reasoning)
	includeThoughts := requested != "" && requested != ModelThinkingLevelOff
	if !includeThoughts {
		requested = ModelThinkingLevelOff
	}

	level := ClampThinkingLevel(model, requested)
	if level == ModelThinkingLevelOff {
		return &GoogleThinkingConfig{Enabled: false}, nil
	}
	resolvedLevel, err := resolveGoogleThinkingLevel(model, level)
	if err != nil {
		return nil, err
	}

	if usesGoogleThinkingLevel(model) {
		return &GoogleThinkingConfig{
			Enabled: includeThoughts,
			Level:   strings.ToUpper(string(resolvedLevel)),
		}, nil
	}

	budget := options.ThinkingBudgets.ForLevel(resolvedLevel)
	if budget == 0 {
		budget = defaultGoogleThinkingBudgets(model).ForLevel(resolvedLevel)
	}

	if budget < 0 {
		budget = -1
	}

	return &GoogleThinkingConfig{
		Enabled:      includeThoughts,
		BudgetTokens: budget,
	}, nil
}

func (b ThinkingBudgets) IsEmpty() bool {
	return b.Minimal == 0 && b.Low == 0 && b.Medium == 0 && b.High == 0
}

func (b ThinkingBudgets) ForLevel(level ThinkingLevel) int {
	switch level {
	case ThinkingLevelMinimal:
		return b.Minimal
	case ThinkingLevelLow:
		return b.Low
	case ThinkingLevelMedium:
		return b.Medium
	case ThinkingLevelHigh, ThinkingLevelXHigh, ThinkingLevelMax:
		return b.High
	default:
		return -1
	}
}

func defaultGoogleThinkingBudgets(model Model) ThinkingBudgets {
	id := strings.ToLower(model.ID)
	switch {
	case strings.Contains(id, "2.5-pro"):
		return ThinkingBudgets{Minimal: 128, Low: 2048, Medium: 8192, High: 32768}
	case strings.Contains(id, "2.5-flash-lite"):
		return ThinkingBudgets{Minimal: 512, Low: 2048, Medium: 8192, High: 24576}
	case strings.Contains(id, "2.5-flash"):
		return ThinkingBudgets{Minimal: 128, Low: 2048, Medium: 8192, High: 24576}
	default:
		return ThinkingBudgets{Minimal: -1, Low: -1, Medium: -1, High: -1}
	}
}

var googleThinkingLevelModelPattern = regexp.MustCompile(`gemini-3(?:\.\d+)?-(?:pro|flash)|gemma-?4`)

// Model names select the wire format; supported levels come from model metadata.
func usesGoogleThinkingLevel(model Model) bool {
	id := strings.ToLower(model.ID)
	return googleThinkingLevelModelPattern.MatchString(id) ||
		id == "gemini-flash-latest" || id == "gemini-flash-lite-latest"
}

func resolveGoogleThinkingLevel(model Model, level ModelThinkingLevel) (ThinkingLevel, error) {
	resolved := string(level)
	if mapped, ok := model.ThinkingLevelMap[level]; ok {
		resolved = strings.ToLower(mapped)
	}
	switch ThinkingLevel(resolved) {
	case ThinkingLevelMinimal, ThinkingLevelLow, ThinkingLevelMedium, ThinkingLevelHigh:
		return ThinkingLevel(resolved), nil
	default:
		return "", fmt.Errorf("unsupported Google thinking level mapping for %s/%s: %s -> %s", model.Provider, model.ID, level, resolved)
	}
}
