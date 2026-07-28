package pigo

import "strings"

// MistralProviderOptions captures provider-specific options for the Mistral API.
type MistralProviderOptions struct {
	StreamOptions
	ToolChoice      string
	PromptMode      string
	ReasoningEffort string
}

func buildMistralProviderStreamOptions(model Model, options SimpleStreamOptions) ProviderStreamOptions {
	return buildMistralProviderOptions(model, options).toProviderStreamOptions(model)
}

func normalizeMistralProviderStreamOptions(model Model, options ProviderStreamOptions) ProviderStreamOptions {
	return resolveMistralProviderOptions(model, options).toProviderStreamOptions(model)
}

func buildMistralProviderOptions(model Model, options SimpleStreamOptions) MistralProviderOptions {
	streamOptions := streamOptionsFromSimple(model, options)
	streamOptions = streamOptions.withCommonSnapshot(model)

	reasoning := streamOptions.Reasoning
	if reasoning == "" {
		return MistralProviderOptions{StreamOptions: streamOptions}
	}

	clamped := ClampThinkingLevel(model, ModelThinkingLevel(reasoning))
	if clamped == ModelThinkingLevelOff {
		return MistralProviderOptions{StreamOptions: streamOptions}
	}

	result := MistralProviderOptions{StreamOptions: streamOptions}
	if model.Reasoning {
		if usesPromptModeReasoning(model) {
			result.PromptMode = "reasoning"
		}
		if usesReasoningEffort(model) {
			result.ReasoningEffort = mapMistralReasoningEffort(model, ThinkingLevel(clamped))
		}
	}
	return result
}

func resolveMistralProviderOptions(model Model, options ProviderStreamOptions) MistralProviderOptions {
	streamOptions := streamOptionsFromProvider(model, options)
	streamOptions = streamOptions.withCommonSnapshot(model)
	result := MistralProviderOptions{
		StreamOptions: streamOptions,
		ToolChoice:    streamOptions.ToolChoice,
	}

	reasoning := streamOptions.Reasoning
	if reasoning == "" {
		return result
	}

	clamped := ClampThinkingLevel(model, ModelThinkingLevel(reasoning))
	if clamped == ModelThinkingLevelOff {
		return result
	}

	if model.Reasoning {
		if usesPromptModeReasoning(model) {
			result.PromptMode = "reasoning"
		}
		if usesReasoningEffort(model) {
			result.ReasoningEffort = mapMistralReasoningEffort(model, ThinkingLevel(clamped))
		}
	}
	return result
}

func (options MistralProviderOptions) toProviderStreamOptions(model Model) ProviderStreamOptions {
	return options.StreamOptions.providerStreamOptions(model)
}

func usesReasoningEffort(model Model) bool {
	switch model.ID {
	case "mistral-small-2603", "mistral-small-latest", "mistral-medium-3.5":
		return true
	}
	return false
}

func usesPromptModeReasoning(model Model) bool {
	return model.Reasoning && !usesReasoningEffort(model)
}

func mapMistralReasoningEffort(model Model, level ThinkingLevel) string {
	mapped := strings.TrimSpace(model.ThinkingLevelMap[ModelThinkingLevel(level)])
	if mapped != "" {
		return mapped
	}
	return "high"
}
