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

	result := MistralProviderOptions{StreamOptions: streamOptions}
	result.applyReasoning(model)
	return result
}

func resolveMistralProviderOptions(model Model, options ProviderStreamOptions) MistralProviderOptions {
	streamOptions := streamOptionsFromProvider(model, options)
	streamOptions = streamOptions.withCommonSnapshot(model)
	result := MistralProviderOptions{
		StreamOptions: streamOptions,
		ToolChoice:    streamOptions.ToolChoice,
	}

	result.applyReasoning(model)
	return result
}

func (options MistralProviderOptions) toProviderStreamOptions(model Model) ProviderStreamOptions {
	return options.StreamOptions.providerStreamOptions(model)
}

func (options *MistralProviderOptions) applyReasoning(model Model) {
	if !model.Reasoning {
		return
	}
	level := ModelThinkingLevelOff
	if options.Reasoning != "" {
		level = ClampThinkingLevel(model, ModelThinkingLevel(options.Reasoning))
	}
	if len(model.ThinkingLevelMap) > 0 {
		if level == ModelThinkingLevelOff {
			options.ReasoningEffort = strings.TrimSpace(model.ThinkingLevelMap[ModelThinkingLevelOff])
		} else if mapped, ok := model.ThinkingLevelMap[level]; ok {
			options.ReasoningEffort = strings.TrimSpace(mapped)
		} else {
			options.ReasoningEffort = "high"
		}
	} else if level != ModelThinkingLevelOff {
		options.PromptMode = "reasoning"
	}
}
