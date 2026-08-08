package pigo

type OpenAICompletionsProviderOptions struct {
	StreamOptions
}

func buildOpenAICompletionsProviderStreamOptions(model Model, options SimpleStreamOptions) ProviderStreamOptions {
	return buildOpenAICompletionsProviderOptions(model, options).toProviderStreamOptions(model)
}

func normalizeOpenAICompletionsProviderStreamOptions(model Model, options ProviderStreamOptions) ProviderStreamOptions {
	return resolveOpenAICompletionsProviderOptions(model, options).toProviderStreamOptions(model)
}

func buildOpenAICompletionsProviderOptions(model Model, options SimpleStreamOptions) OpenAICompletionsProviderOptions {
	providerOptions := OpenAICompletionsProviderOptions{
		StreamOptions: streamOptionsFromSimple(model, options),
	}
	providerOptions.Reasoning = clampOpenAICompletionsReasoning(model, providerOptions.Reasoning)
	providerOptions.StreamOptions = providerOptions.StreamOptions.withCommonSnapshot(model)
	return providerOptions
}

func resolveOpenAICompletionsProviderOptions(model Model, options ProviderStreamOptions) OpenAICompletionsProviderOptions {
	providerOptions := OpenAICompletionsProviderOptions{
		StreamOptions: streamOptionsFromProvider(model, options),
	}
	providerOptions.Reasoning = clampOpenAICompletionsReasoning(model, providerOptions.Reasoning)
	providerOptions.StreamOptions = providerOptions.StreamOptions.withCommonSnapshot(model)
	return providerOptions
}

func clampOpenAICompletionsReasoning(model Model, level ThinkingLevel) ThinkingLevel {
	if level == "" || !model.Reasoning {
		return ""
	}
	clamped := ClampThinkingLevel(model, ModelThinkingLevel(level))
	if clamped == ModelThinkingLevelOff {
		return ""
	}
	return ThinkingLevel(clamped)
}

func (options OpenAICompletionsProviderOptions) toProviderStreamOptions(model Model) ProviderStreamOptions {
	return options.StreamOptions.providerStreamOptions(model)
}
