package pigo

type AnthropicMessagesProviderOptions struct {
	StreamOptions
}

type KimiCodingProviderOptions struct {
	AnthropicMessagesProviderOptions
}

func buildAnthropicMessagesProviderStreamOptions(model Model, options SimpleStreamOptions) ProviderStreamOptions {
	return buildAnthropicMessagesProviderOptions(model, options).toProviderStreamOptions(model)
}

func buildAnthropicMessagesProviderOptions(model Model, options SimpleStreamOptions) AnthropicMessagesProviderOptions {
	providerOptions := AnthropicMessagesProviderOptions{
		StreamOptions: streamOptionsFromSimple(model, options),
	}
	if providerOptions.Reasoning != "" {
		maxTokens, thinkingBudget := adjustMaxTokensForThinking(
			providerOptions.MaxTokens,
			model.MaxTokens,
			providerOptions.Reasoning,
			options.ThinkingBudgets,
		)
		providerOptions.MaxTokens = maxTokens
		providerOptions.ThinkingBudgetTokens = thinkingBudget
	}
	providerOptions.StreamOptions = providerOptions.StreamOptions.withCommonSnapshot(model)
	return providerOptions
}

func resolveAnthropicMessagesProviderOptions(_ Model, options ProviderStreamOptions) AnthropicMessagesProviderOptions {
	return AnthropicMessagesProviderOptions{
		StreamOptions: streamOptionsFromProvider(Model{}, options),
	}
}

func buildKimiCodingProviderStreamOptions(model Model, options SimpleStreamOptions) ProviderStreamOptions {
	return buildKimiCodingProviderOptions(model, options).toProviderStreamOptions(model)
}

func normalizeKimiCodingProviderStreamOptions(model Model, options ProviderStreamOptions) ProviderStreamOptions {
	return resolveKimiCodingProviderOptions(model, options).toProviderStreamOptions(model)
}

func buildKimiCodingProviderOptions(model Model, options SimpleStreamOptions) KimiCodingProviderOptions {
	providerOptions := KimiCodingProviderOptions{
		AnthropicMessagesProviderOptions: buildAnthropicMessagesProviderOptions(model, options),
	}
	providerOptions.SessionID = ""
	providerOptions.ReasoningSummary = ""
	providerOptions.TextVerbosity = ""
	providerOptions.ToolChoice = ""
	providerOptions.StreamOptions = providerOptions.StreamOptions.withCommonSnapshot(model)
	return providerOptions
}

func resolveKimiCodingProviderOptions(model Model, options ProviderStreamOptions) KimiCodingProviderOptions {
	providerOptions := KimiCodingProviderOptions{
		AnthropicMessagesProviderOptions: resolveAnthropicMessagesProviderOptions(model, options),
	}
	providerOptions.SessionID = ""
	providerOptions.ReasoningSummary = ""
	providerOptions.TextVerbosity = ""
	providerOptions.ToolChoice = ""
	providerOptions.StreamOptions = providerOptions.StreamOptions.withCommonSnapshot(model)
	return providerOptions
}

func (options AnthropicMessagesProviderOptions) toProviderStreamOptions(model Model) ProviderStreamOptions {
	return options.StreamOptions.providerStreamOptions(model)
}

func (options KimiCodingProviderOptions) toProviderStreamOptions(model Model) ProviderStreamOptions {
	return options.AnthropicMessagesProviderOptions.toProviderStreamOptions(model)
}
